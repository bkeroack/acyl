package eventlogger

import (
	"os"
	"testing"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	guuid "github.com/gofrs/uuid"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
)

var testRC = models.RepoConfig{
	Version:        2,
	TargetBranches: []string{"master"},
	Application: models.RepoConfigAppMetadata{
		Repo:   "foo/bar",
		Image:  "quay.io/foo/bar",
		Ref:    "asdf",
		Branch: "master",
	},
	Dependencies: models.DependencyDeclaration{
		Direct: []models.RepoConfigDependency{
			models.RepoConfigDependency{
				Repo: "foo/something",
				Name: "something",
				AppMetadata: models.RepoConfigAppMetadata{
					Repo:   "foo/something",
					Image:  "quay.io/foo/something",
					Ref:    "1234",
					Branch: "master",
				},
				Parent:   models.GetName("foo/bar"),
				Requires: []string{"otherthing"},
			},
			models.RepoConfigDependency{
				Name:   "otherthing",
				Parent: "something",
			},
		},
	},
}

func TestSetNewStatus(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	elog.SetNewStatus(models.CreateEvent, "some-name", rrd)

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}

	if etype := el2.Config.Type; etype != models.CreateEvent {
		t.Fatalf("unexpected event type: %v", etype.String())
	}

	if el2.Config.Started.IsZero() {
		t.Fatalf("config should have been started")
	}

	if el2.Config.TriggeringRepo != rrd.Repo {
		t.Fatalf("bad repo: %v", el2.Config.TriggeringRepo)
	}

	if el2.Config.PullRequest != rrd.PullRequest {
		t.Fatalf("bad pr: %v", el2.Config.PullRequest)
	}

	if el2.Config.GitHubUser != rrd.User {
		t.Fatalf("bad user: %v", el2.Config.GitHubUser)
	}
	if el2.Config.Branch != rrd.SourceBranch {
		t.Fatalf("bad branch: %v", el2.Config.Branch)
	}
	if el2.Config.Revision != rrd.SourceSHA {
		t.Fatalf("bad revision: %v", el2.Config.Revision)
	}
}

func TestSetInitialStatus(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	elog.SetNewStatus(models.CreateEvent, "some-name", rrd)

	elog.SetInitialStatus(&testRC, 10*time.Millisecond)

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}

	if status := el2.Config.Status; status != models.PendingStatus {
		t.Fatalf("unexpected status: %v", status.String())
	}

	if etype := el2.Config.Type; etype != models.CreateEvent {
		t.Fatalf("unexpected event type: %v", etype.String())
	}

	if n := len(el2.Tree); n != 3 {
		t.Fatalf("unexpected tree size: %v", n)
	}

	if _, ok := el2.Tree["otherthing"]; !ok {
		t.Fatalf("missing tree node: %+v", el2.Tree)
	}

	if node := el2.Tree["otherthing"]; node.Parent != "something" {
		t.Fatalf("bad node: %+v", node)
	}
}

func TestSetSetK8sNamespace(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	elog.SetNewStatus(models.CreateEvent, "some-name", rrd)

	elog.SetInitialStatus(&testRC, 10*time.Millisecond)

	ns := "nitro-38393-some-name"
	elog.SetK8sNamespace(ns)

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}

	if el2.Config.K8sNamespace != ns {
		t.Fatalf("bad k8s ns: %v", el2.Config.K8sNamespace)
	}
}

func TestSetImageStarted(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	elog.SetNewStatus(models.CreateEvent, "some-name", rrd)

	elog.SetInitialStatus(&testRC, 10*time.Millisecond)

	elog.SetImageStarted("something")

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}
	if el2.Tree["something"].Image.Started.IsZero() {
		t.Fatalf("image should have been started")
	}
}

func TestSetImageBuildID(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	elog.SetNewStatus(models.CreateEvent, "some-name", rrd)

	elog.SetInitialStatus(&testRC, 10*time.Millisecond)

	bid := guuid.Must(guuid.NewV4())

	elog.SetImageBuildID("something", bid)

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}
	if id := el2.Tree["something"].Image.ID; id != bid {
		t.Fatalf("bad image build id: %v (wanted %v)", id, bid)
	}
}

func TestSetImageCompleted(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	elog.SetNewStatus(models.CreateEvent, "some-name", rrd)

	elog.SetInitialStatus(&testRC, 10*time.Millisecond)

	elog.SetImageCompleted("something", true)

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}

	if el2.Tree["something"].Image.Completed.IsZero() {
		t.Fatalf("image should have been completed")
	}

	if !el2.Tree["something"].Image.Error {
		t.Fatalf("image should have been marked as error")
	}
}

func TestSetChartStarted(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	elog.SetNewStatus(models.CreateEvent, "some-name", rrd)

	elog.SetInitialStatus(&testRC, 10*time.Millisecond)

	elog.SetChartStarted("something", models.InstallingChartStatus)

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}

	if el2.Tree["something"].Chart.Started.IsZero() {
		t.Fatalf("chart should have been started")
	}

	if status := el2.Tree["something"].Chart.Status; status != models.InstallingChartStatus {
		t.Fatalf("unexpected status: %v", status)
	}
}

func TestSetChartCompleted(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	elog.SetNewStatus(models.CreateEvent, "some-name", rrd)

	elog.SetInitialStatus(&testRC, 10*time.Millisecond)

	elog.SetChartCompleted("something", models.DoneChartStatus)

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}

	if el2.Tree["something"].Chart.Completed.IsZero() {
		t.Fatalf("chart should have been completed")
	}

	if status := el2.Tree["something"].Chart.Status; status != models.DoneChartStatus {
		t.Fatalf("unexpected status: %v", status)
	}
}

func TestSetCompletedStatus(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	elog.SetNewStatus(models.CreateEvent, "some-name", rrd)

	elog.SetInitialStatus(&testRC, 10*time.Millisecond)

	elog.SetCompletedStatus(models.DoneStatus)

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}

	if el2.Config.Completed.IsZero() {
		t.Fatalf("config should have been completed")
	}

	if status := el2.Config.Status; status != models.DoneStatus {
		t.Fatalf("unexpected status: %v", status)
	}
}

func TestSetNewStatusUnknown(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := Logger{DL: dl, ID: id, Sink: os.Stderr}
	elog.Init([]byte{}, "foo/bar", 99)

	rrd := models.RepoRevisionData{Repo: "foo/bar", PullRequest: 12, User: "john.doe", SourceBranch: "feature-foo", SourceSHA: "asdf"}
	unknown := "<unknown>"
	elog.SetNewStatus(models.UnknownEventStatusType, unknown, rrd)

	el2, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("error getting event status: %v", err)
	}
	exp := &models.EventStatusSummary{
		Config: models.EventStatusSummaryConfig{
			Type:           models.UnknownEventStatusType,
			Status:         models.PendingStatus,
			RenderedStatus: models.RenderedEventStatus{},
			EnvName:        unknown,
			TriggeringRepo: rrd.Repo,
			PullRequest:    rrd.PullRequest,
			GitHubUser:     rrd.User,
			Branch:         rrd.SourceBranch,
			Revision:       rrd.SourceSHA,
			Started:        el2.Config.Started,
			Completed:      el2.Config.Completed,
		},
	}
	if !cmp.Equal(el2, exp) {
		t.Fatalf("expected comparison to match:\nRsp: %+v\nExp: %+v", el2, exp)
	}
}
