package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/google/uuid"
)

func TestCleanupPruneDestroyedEnvRecords(t *testing.T) {
	if dltype == "fake" {
		t.SkipNow()
	}
	dl, tdl := NewTestDataLayer(t)
	defer tdl.TearDown()
	if err := tdl.CreateTables(); err != nil {
		t.Fatalf("error creating tables: %v", err)
	}

	c := Cleaner{DB: tdl.pgdb, LogFunc: t.Logf}
	if err := dl.CreateQAEnvironment(context.Background(), &models.QAEnvironment{Created: time.Now().UTC().Add(-1 * time.Hour), Name: "foo-bar"}); err != nil {
		t.Fatalf("environment create should have succeeded: %v", err)
	}
	if err := dl.SetQAEnvironmentStatus(context.Background(), "foo-bar", models.Destroyed); err != nil {
		t.Fatalf("environment set status should have succeeded: %v", err)
	}
	expires := 1 * time.Millisecond
	time.Sleep(expires * 2)
	if err := dl.CreateQAEnvironment(context.Background(), &models.QAEnvironment{Created: time.Now().UTC(), Name: "foo-bar-2"}); err != nil {
		t.Fatalf("environment create 2 should have succeeded: %v", err)
	}
	if err := dl.SetQAEnvironmentStatus(context.Background(), "foo-bar-2", models.Destroyed); err != nil {
		t.Fatalf("environment set status 2 should have succeeded: %v", err)
	}
	dl.CreateK8sEnv(context.Background(), &models.KubernetesEnvironment{EnvName: "foo-bar"})
	dl.CreateHelmReleasesForEnv(context.Background(), []models.HelmRelease{models.HelmRelease{EnvName: "foo-bar"}})
	dl.SetQAEnvironmentCreated(context.Background(), "foo-bar-2", time.Now().UTC().Add(1*time.Hour))
	if err := c.PruneDestroyedEnvRecords(expires); err != nil {
		t.Fatalf("prune should have succeeded: %v", err)
	}
	env, err := dl.GetQAEnvironment(context.Background(), "foo-bar")
	if err != nil {
		t.Fatalf("environment get 1 should have succeeded: %v", err)
	}
	if env != nil {
		t.Fatalf("environment should have been pruned")
	}
	k8senv, err := dl.GetK8sEnv(context.Background(), "foo-bar")
	if err != nil {
		t.Fatalf("k8senv get should have succeeded: %v", err)
	}
	if k8senv != nil {
		t.Fatalf("k8senv should have been pruned")
	}
	hr, err := dl.GetHelmReleasesForEnv(context.Background(), "foo-bar")
	if err != nil {
		t.Fatalf("helm releases get should have succeeded: %v", err)
	}
	if len(hr) != 0 {
		t.Fatalf("helm releases should have been pruned")
	}
	env, err = dl.GetQAEnvironment(context.Background(), "foo-bar-2")
	if err != nil {
		t.Fatalf("environment get 2 should have succeeded: %v", err)
	}
	if env == nil {
		t.Fatalf("environment should not have been pruned")
	}
}

func TestCleanupPruneEventLogs(t *testing.T) {
	if dltype == "fake" {
		t.SkipNow()
	}
	dl, tdl := NewTestDataLayer(t)
	defer tdl.TearDown()
	if err := tdl.CreateTables(); err != nil {
		t.Fatalf("error creating tables: %v", err)
	}

	expires := 1 * time.Millisecond

	c := Cleaner{DB: tdl.pgdb, LogFunc: t.Logf}
	uuid1, uuid2 := uuid.Must(uuid.NewRandom()), uuid.Must(uuid.NewRandom())

	ctx := context.Background()
	if err := dl.CreateQAEnvironment(ctx, &models.QAEnvironment{Name: "foo-bar"}); err != nil {
		t.Fatalf("error creating qa env: %v", err)
	}
	if err := dl.CreateEventLog(&models.EventLog{ID: uuid1, EnvName: "foo-bar"}); err != nil {
		t.Fatalf("error creating event log 1: %v", err)
	}
	time.Sleep(2 * expires)
	if err := dl.CreateEventLog(&models.EventLog{ID: uuid2, EnvName: "foo-bar"}); err != nil {
		t.Fatalf("error creating event log 2: %v", err)
	}
	tdl.pgdb.Exec(`UPDATE event_logs SET created = $1 WHERE id = $2;`, time.Now().UTC().Add(-1*time.Hour), uuid1)
	tdl.pgdb.Exec(`UPDATE event_logs SET created = $1 WHERE id = $2;`, time.Now().UTC().Add(1*time.Hour), uuid2)
	if err := c.PruneEventLogs(expires); err != nil {
		t.Fatalf("prune should have succeeded: %v", err)
	}
	el, err := dl.GetEventLogByID(uuid1)
	if err != nil {
		t.Fatalf("get 1 should have succeeded: %v", err)
	}
	if el != nil {
		t.Fatalf("event log should have been pruned")
	}
	el, err = dl.GetEventLogByID(uuid2)
	if err != nil {
		t.Fatalf("get 2 should have succeeded: %v", err)
	}
	if el == nil {
		t.Fatalf("event log 2 should not have been pruned")
	}
}
