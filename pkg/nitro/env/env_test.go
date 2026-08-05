package env

import (
	"context"
	"fmt"
	mathrand "math/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bkeroack/acyl/pkg/eventlogger"
	"github.com/bkeroack/acyl/pkg/ghclient"
	"github.com/bkeroack/acyl/pkg/locker"
	"github.com/bkeroack/acyl/pkg/memfs"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/namegen"
	nitroerrors "github.com/bkeroack/acyl/pkg/nitro/errors"
	"github.com/bkeroack/acyl/pkg/nitro/meta"
	"github.com/bkeroack/acyl/pkg/nitro/metahelm"
	"github.com/bkeroack/acyl/pkg/nitro/metrics"
	"github.com/bkeroack/acyl/pkg/nitro/notifier"
	"github.com/bkeroack/acyl/pkg/persistence"
	metahelmlib "github.com/dollarshaveclub/metahelm/pkg/metahelm"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestLockingOperation(t *testing.T) {
	operationTimeout := 5 * time.Second
	el := &eventlogger.Logger{DL: persistence.NewFakeDataLayer()}
	plf, err := locker.NewFakePreemptiveLockerFactory(
		[]locker.LockProviderOption{
			locker.WithLockTimeout(time.Second),
			locker.WithMaxLockDuration(operationTimeout),
		},
		locker.WithLockDelay(time.Millisecond),
	)
	if err != nil {
		t.Fatalf("error creating new preemptive locker factory: %v", err)
	}
	m := Manager{
		PLF: plf,
		MC:  &metrics.FakeCollector{},
		DL:  persistence.NewFakeDataLayer(),
	}
	repo := "foo"
	pr := uint(mathrand.Uint32())
	preemptedFunc := func(ctx context.Context) error {
		timer := time.NewTimer(10 * time.Second)
		pl := m.PLF(repo, pr, "new operation")
		pl.Lock(ctx)
		releaseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer pl.Release(releaseCtx)
		defer cancel()
		select {
		case <-timer.C:
			return errors.New("timer expired")
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	longOpFunc := func(ctx context.Context) error {
		timer := time.NewTimer(1 * time.Second)
		select {
		case <-timer.C:
			return errors.New("timer expired")
		case <-ctx.Done():
			return nil
		}
	}

	hangingOpFunc := func(ctx context.Context) error {
		time.Sleep(2 * operationTimeout)
		return nil
	}

	ctx := eventlogger.NewEventLoggerContext(context.Background(), el)
	err = m.lockingOperation(ctx, repo, pr, preemptedFunc)
	cancelled := nitroerrors.IsCancelledError(err)
	if err == nil {
		t.Fatalf("expected preemption error")
	}
	if !cancelled {
		t.Fatalf("expected error to be annotated as cancelled")
	}
	if strings.Contains(err.Error(), "timer expired") {
		t.Fatalf("expected the context to be canceled before the timer expired")
	}

	pr++
	ctx2 := eventlogger.NewEventLoggerContext(context.Background(), el)
	err = m.lockingOperation(ctx2, repo, pr, longOpFunc)
	if err == nil {
		t.Fatalf("should have timed out")
	}
	if !strings.Contains(err.Error(), "timer expired") {
		t.Fatalf("unexpected error: %v", err)
	}

	pr++
	ctx3 := eventlogger.NewEventLoggerContext(context.Background(), el)
	err = m.lockingOperation(ctx3, repo, pr, hangingOpFunc)
	if err == nil {
		t.Fatalf("expected error from lockingOperation due to hanging operation function")
	}
}

func TestGetRepoConfig(t *testing.T) {
	fg := &meta.FakeGetter{
		GetFunc: func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) {
			if rd.Repo == "" || rd.SourceSHA == "" {
				return nil, fmt.Errorf("repo (%v) or ref (%v) is empty", rd.Repo, rd.SourceSHA)
			}
			return &models.RepoConfig{}, nil
		},
	}
	frc := &ghclient.FakeRepoClient{
		GetBranchesFunc: func(context.Context, string) ([]ghclient.BranchInfo, error) {
			return []ghclient.BranchInfo{ghclient.BranchInfo{Name: "master"}}, nil
		},
	}
	m := Manager{MG: fg, RC: frc, MC: &metrics.FakeCollector{}}
	_, err := m.getRepoConfig(context.Background(), &models.RepoRevisionData{Repo: "foo/bar", User: "foo", PullRequest: 1, BaseBranch: "master", SourceSHA: "aaaa"})
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	_, err = m.getRepoConfig(context.Background(), &models.RepoRevisionData{Repo: "foo/bar", User: "foo", PullRequest: 1, BaseBranch: "master", SourceSHA: ""})
	if err == nil {
		t.Fatalf("should have failed with missing ref")
	}
	_, err = m.getRepoConfig(context.Background(), &models.RepoRevisionData{Repo: "", User: "foo", PullRequest: 1, BaseBranch: "master", SourceSHA: "aaaa"})
	if err == nil {
		t.Fatalf("should have failed with missing repo")
	}
	fg.GetFunc = func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) { return nil, nil }
	if _, err := m.getRepoConfig(context.Background(), &models.RepoRevisionData{Repo: "foo/bar", User: "foo", PullRequest: 1, BaseBranch: "master", SourceSHA: "aaaa"}); err == nil {
		t.Fatalf("should have failed with nil rc")
	}
}

func TestGenerateNewEnv(t *testing.T) {
	fg := &meta.FakeGetter{
		GetFunc: func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) {
			return &models.RepoConfig{
				Application: models.RepoConfigAppMetadata{
					Repo:   "foo/bar",
					Ref:    "asdf",
					Branch: "foo",
				},
			}, nil
		},
	}
	frc := &ghclient.FakeRepoClient{
		GetBranchesFunc: func(context.Context, string) ([]ghclient.BranchInfo, error) {
			return []ghclient.BranchInfo{ghclient.BranchInfo{Name: "master"}}, nil
		},
	}
	dl := persistence.NewFakeDataLayer()
	m := Manager{MG: fg, RC: frc, NG: &namegen.FakeNameGenerator{}, DL: dl}
	env, err := m.generateNewEnv(context.Background(), &models.RepoRevisionData{Repo: "foo/bar", User: "foo", PullRequest: 1, BaseBranch: "master", SourceSHA: "asdf", SourceBranch: "foo"})
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if env.Repo != "foo/bar" {
		t.Fatalf("bad repo: %v", env.Repo)
	}
	if env.User != "foo" {
		t.Fatalf("bad user: %v", env.User)
	}
	if env.PullRequest != 1 {
		t.Fatalf("bad PR: %v", env.PullRequest)
	}
	if env.Status != models.Spawned {
		t.Fatalf("bad status: %v", env.Status)
	}
	oldname := env.Name
	// test reuse of an existing environment record
	env, err = m.generateNewEnv(context.Background(), &models.RepoRevisionData{Repo: "foo/bar", User: "foo", PullRequest: 1, BaseBranch: "master", SourceSHA: "1234", SourceBranch: "foo"})
	if err != nil {
		t.Fatalf("reuse should have succeeded: %v", err)
	}
	if env.Name != oldname {
		t.Fatalf("expected reuse of previous name: %v (vs %v)", env.Name, oldname)
	}
	if env.SourceSHA != "1234" {
		t.Fatalf("bad sha: %v", env.SourceSHA)
	}
}

func TestFetchCharts(t *testing.T) {
	fg := &meta.FakeGetter{
		GetFunc: func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) {
			return &models.RepoConfig{}, nil
		},
		FetchChartsFunc: func(ctx context.Context, rc *models.RepoConfig, basePath string) (meta.ChartLocations, error) {
			return meta.ChartLocations{
				"foo/bar": meta.ChartLocation{
					ChartPath:   "/tmp/foo/bar",
					VarFilePath: "/tmp/vars.yml",
				},
			}, nil
		},
	}
	frc := &ghclient.FakeRepoClient{
		GetBranchesFunc: func(context.Context, string) ([]ghclient.BranchInfo, error) {
			return []ghclient.BranchInfo{ghclient.BranchInfo{Name: "master"}}, nil
		},
	}
	m := Manager{
		MG: fg,
		RC: frc,
		FS: memfs.New(),
		MC: &metrics.FakeCollector{},
	}
	_, _, err := m.fetchCharts(context.Background(), "foo-bar", &models.RepoConfig{})
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestGetEnv(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), &models.QAEnvironment{Name: "foo-bar", Repo: "foo/bar", PullRequest: 99, Status: models.Success})
	m := Manager{
		DL: dl,
		MC: &metrics.FakeCollector{},
	}
	qa, err := m.getenv(context.Background(), &models.RepoRevisionData{Repo: "foo/bar", PullRequest: 99})
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if qa == nil {
		t.Fatalf("expected results")
	}
	if qa.Name != "foo-bar" {
		t.Fatalf("bad env name: %v", qa.Name)
	}
}

var testNF = func(lf func(string, ...interface{}), notifications models.Notifications, user string) notifier.Router {
	sb := &notifier.SlackBackend{
		Username: "john.doe",
		API:      &notifier.FakeSlackAPIClient{},
	}
	return &notifier.MultiRouter{Backends: []notifier.Backend{sb}}
}

type notificationTrackerSender struct {
	sync.Mutex
	sent []notifier.Notification
}

func (nts *notificationTrackerSender) FanOut(n notifier.Notification) error {
	nts.Lock()
	defer nts.Unlock()
	nts.sent = append(nts.sent, n)
	return nil
}

type notificationTracker struct {
	Sender *notificationTrackerSender
}

func newNotificationTracker() *notificationTracker {
	return &notificationTracker{Sender: &notificationTrackerSender{}}
}

func (nt *notificationTracker) sender(lf func(string, ...interface{}), notifications models.Notifications, user string) notifier.Router {
	return nt.Sender
}

func (nt *notificationTracker) get() []notifier.Notification {
	nt.Sender.Lock()
	defer nt.Sender.Unlock()
	out := make([]notifier.Notification, len(nt.Sender.sent))
	copy(out, nt.Sender.sent)
	return out
}

func (nt *notificationTracker) reset() {
	nt.Sender.Lock()
	defer nt.Sender.Unlock()
	nt.Sender.sent = nil
}

func TestCreate(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	rdd := models.RepoRevisionData{
		Repo:         "foo/bar",
		PullRequest:  1,
		SourceSHA:    "asdf",
		SourceBranch: "feature-spam",
		BaseSHA:      "1234",
		BaseBranch:   "release",
	}
	rc := models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:          "foo/bar",
			Ref:           "asdf",
			Branch:        "feature-spam",
			ChartPath:     ".chart/bar",
			ChartVarsPath: "./chart/vars.yml",
			Image:         "foo/bar",
			ChartTagValue: "image.tag",
		},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "foo-qwerty",
					Repo: "foo/qwerty",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:   "foo/qwerty",
						Ref:    "aaaa",
						Branch: "feature-spam",
					},
					Requires: []string{"foo/mysql"},
				},
				models.RepoConfigDependency{
					Name: "foo-mysql",
					Repo: "foo/mysql",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:   "foo/mysql",
						Ref:    "bbbb",
						Branch: "master",
					},
					DefaultBranch: "master",
				},
			},
		},
	}
	cl := meta.ChartLocations{
		"foo-bar":    meta.ChartLocation{ChartPath: "/tmp/foo/bar", VarFilePath: "/tmp/foo/bar/vars.yml"},
		"foo-qwerty": meta.ChartLocation{ChartPath: "/tmp/foo/qwerty", VarFilePath: "/tmp/foo/qwerty/vars.yml"},
		"foo-mysql":  meta.ChartLocation{ChartPath: "/tmp/foo/mysql", VarFilePath: "/tmp/foo/mysql/vars.yml"},
	}
	bm := map[string][]ghclient.BranchInfo{
		"foo/bar":    []ghclient.BranchInfo{ghclient.BranchInfo{Name: "feature-spam"}, ghclient.BranchInfo{Name: "release"}},
		"foo/qwerty": []ghclient.BranchInfo{ghclient.BranchInfo{Name: "feature-spam"}, ghclient.BranchInfo{Name: "release"}},
		"foo/mysql":  []ghclient.BranchInfo{ghclient.BranchInfo{Name: "feature-spam"}, ghclient.BranchInfo{Name: "release"}, ghclient.BranchInfo{Name: "master"}},
	}
	cir := map[string]error{
		"foo-bar":    nil,
		"foo-qwerty": nil,
		"foo-mysql":  nil,
	}
	// extant environments for global limit testing
	dl.CreateQAEnvironment(context.Background(), &models.QAEnvironment{
		Created:     time.Now().UTC(),
		Name:        "some-other-random-name",
		Repo:        rdd.Repo,
		PullRequest: rdd.PullRequest + 1,
		Status:      models.Success,
	})
	dl.CreateK8sEnv(context.Background(), &models.KubernetesEnvironment{EnvName: "some-other-random-name"})
	dl.CreateQAEnvironment(context.Background(), &models.QAEnvironment{
		Created:     time.Now().UTC().Add(10 * time.Millisecond),
		Name:        "some-other-random-name2",
		Repo:        rdd.Repo,
		PullRequest: rdd.PullRequest + 2,
		Status:      models.Success,
	})
	dl.CreateK8sEnv(context.Background(), &models.KubernetesEnvironment{EnvName: "some-other-random-name2"})
	cases := []struct {
		name               string
		inputRRD           models.RepoRevisionData
		inputRC            models.RepoConfig
		inputCL            meta.ChartLocations
		inputBranches      map[string][]ghclient.BranchInfo
		chartInstallResult map[string]error
		limit              uint
		delay, timeout     time.Duration
		verifyFunc         func(string, error, *notificationTracker, *testing.T)
	}{
		{
			"branch matching", rdd, rc, cl, bm, cir, 0, 0, 0,
			func(name string, err error, nt *notificationTracker, st *testing.T) {
				if err != nil {
					st.Fatalf("should have succeeded: %v", err)
				}
				env, err := dl.GetQAEnvironment(context.Background(), name)
				if err != nil {
					st.Fatalf("env missing: %v", err)
				}
				k8senv, err := dl.GetK8sEnv(context.Background(), name)
				if err != nil {
					st.Fatalf("k8senv missing: %v", err)
				}
				if k8senv == nil {
					st.Fatalf("k8senv missing")
				}
				v := env.RefMap["foo/bar"]
				if v != "feature-spam" {
					st.Fatalf("bad branch for foo/bar: %v", v)
				}
				v = env.RefMap["foo/qwerty"]
				if v != "feature-spam" {
					st.Fatalf("bad branch for foo/qwerty: %v", v)
				}
				v = env.RefMap["foo/mysql"]
				if v != "master" {
					st.Fatalf("bad branch for foo/mysql: %v", v)
				}
				releases, _ := dl.GetHelmReleasesForEnv(context.Background(), name)
				if len(releases) != 3 {
					st.Fatalf("bad release count: %v", len(releases))
				}
			},
		},
		{
			"install error", rdd, rc, cl, bm,
			map[string]error{
				"foo-bar":    errors.New("install error"),
				"foo-qwerty": nil,
				"foo-mysql":  nil,
			}, 0, 0, 0,
			func(name string, err error, nt *notificationTracker, st *testing.T) {
				if err == nil {
					st.Fatalf("should have failed with install error")
				}
				if !strings.Contains(err.Error(), "install error") {
					st.Fatalf("unexpected error: %v", err)
				}
			},
		},
		{
			"global limit enforced", rdd, rc, cl, bm, cir, 1, 0, 0,
			func(name string, err error, nt *notificationTracker, st *testing.T) {
				if err != nil {
					st.Fatalf("should have succeeded: %v", err)
				}
				env, err := dl.GetQAEnvironment(context.Background(), name)
				if err != nil {
					st.Fatalf("env missing: %v", err)
				}
				env, _ = dl.GetQAEnvironment(context.Background(), "some-other-random-name")
				if env == nil {
					st.Fatalf("destroyed env not found")
				}
				if env.Status != models.Destroyed {
					st.Fatalf("env should have been destroyed to enforce global limit")
				}
			},
		},
		{
			"timeout", rdd, rc, cl, bm, cir, 0, 50 * time.Millisecond, 50 * time.Millisecond,
			func(name string, err error, nt *notificationTracker, st *testing.T) {
				if err == nil {
					st.Fatalf("should have failed with timeout")
				}
				ns := nt.get()
				var found bool
				for _, n := range ns {
					if n.Event == notifier.Failure && strings.Contains(n.Data.ErrorMessage, "timeout reached") {
						found = true
					}
				}
				if !found {
					st.Fatalf("did not get timeout notification: %+v", ns)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dl.SetDelay(c.delay)
			fg := &meta.FakeGetter{
				GetFunc: func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) {
					return &c.inputRC, nil
				},
				FetchChartsFunc: func(ctx context.Context, rc *models.RepoConfig, basePath string) (meta.ChartLocations, error) {
					return c.inputCL, nil
				},
			}
			frc := &ghclient.FakeRepoClient{
				GetBranchesFunc: func(ctx context.Context, name string) ([]ghclient.BranchInfo, error) {
					return c.inputBranches[name], nil
				},
				GetCommitMessageFunc: func(context.Context, string, string) (string, error) { return "", nil },
				SetStatusFunc:        func(context.Context, string, string, *ghclient.CommitStatus) error { return nil },
			}
			ci := &metahelm.FakeInstaller{
				ChartInstallFunc: func(repo string, location metahelm.ChartLocation) error {
					return c.chartInstallResult[repo]
				},
				DL: dl,
				KC: k8sfake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nitro-1234-some-random-name"}}),
			}
			nt := newNotificationTracker()
			plf, err := locker.NewFakePreemptiveLockerFactory(
				[]locker.LockProviderOption{locker.WithLockTimeout(2 * time.Second)},
				locker.WithLockDelay(10*time.Millisecond),
			)
			if err != nil {
				t.Fatalf("error creating new preemptive locker factory: %v", err)
			}
			m := Manager{
				DL:               dl,
				PLF:              plf,
				NF:               nt.sender,
				MC:               &metrics.FakeCollector{},
				NG:               &namegen.FakeNameGenerator{},
				FS:               memfs.New(),
				MG:               fg,
				RC:               frc,
				CI:               ci,
				GlobalLimit:      c.limit,
				OperationTimeout: c.timeout,
			}

			el := &eventlogger.Logger{DL: dl}
			el.Init([]byte{}, c.inputRRD.Repo, c.inputRRD.PullRequest)
			ctx := eventlogger.NewEventLoggerContext(context.Background(), el)
			name, err := m.Create(ctx, c.inputRRD)
			c.verifyFunc(name, err, nt, t)
		})
	}
}

func TestUpdate(t *testing.T) {
	rdd := models.RepoRevisionData{
		Repo:         "foo/bar",
		PullRequest:  1,
		SourceSHA:    "asdf",
		SourceBranch: "feature-spam",
		BaseSHA:      "1234",
		BaseBranch:   "release",
	}
	env := models.QAEnvironment{
		Name:        "some-name",
		Repo:        rdd.Repo,
		PullRequest: rdd.PullRequest,
		Status:      models.Success,
		RefMap: models.RefMap{
			"foo/bar":    "feature-spam",
			"foo/qwerty": "feature-spam",
			"foo/mysql":  "master",
		},
		CommitSHAMap: models.RefMap{
			"foo/bar":    "1111",
			"foo/qwerty": "2222",
			"foo/mysql":  "3333",
		},
	}
	rc := models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:          rdd.Repo,
			Ref:           "asdf",
			Branch:        "feature-spam",
			ChartPath:     ".chart/bar",
			ChartVarsPath: "./chart/vars.yml",
			Image:         "foo/bar",
			ChartTagValue: "image.tag",
		},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "foo-qwerty",
					Repo: "foo/qwerty",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:   "foo/qwerty",
						Ref:    "9997",
						Branch: "feature-spam",
					},
					Requires: []string{"foo/mysql"},
				},
				models.RepoConfigDependency{
					Name: "foo-mysql",
					Repo: "foo/mysql",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:   "foo/mysql",
						Ref:    "9993",
						Branch: "master",
					},
					DefaultBranch: "master",
				},
			},
		},
	}
	sig := rc.ConfigSignature()
	k8senv := models.KubernetesEnvironment{
		EnvName:         env.Name,
		Namespace:       "nitro-1234-" + env.Name,
		ConfigSignature: sig[:],
	}
	releases := []models.HelmRelease{
		models.HelmRelease{EnvName: env.Name, Name: models.GetName(rdd.Repo), RevisionSHA: env.CommitSHAMap[rdd.Repo], Release: strings.Replace(rdd.Repo, "/", "-", -1)},
		models.HelmRelease{EnvName: env.Name, Name: "foo-qwerty", RevisionSHA: env.CommitSHAMap["foo/qwerty"], Release: strings.Replace("foo/qwerty", "/", "-", -1)},
		models.HelmRelease{EnvName: env.Name, Name: "foo-mysql", RevisionSHA: env.CommitSHAMap["foo/mysql"], Release: strings.Replace("foo/mysql", "/", "-", -1)},
	}
	cl := meta.ChartLocations{
		"foo-bar":    meta.ChartLocation{ChartPath: "/tmp/foo/bar", VarFilePath: "/tmp/foo/bar/vars.yml"},
		"foo-qwerty": meta.ChartLocation{ChartPath: "/tmp/foo/qwerty", VarFilePath: "/tmp/foo/qwerty/vars.yml"},
		"foo-mysql":  meta.ChartLocation{ChartPath: "/tmp/foo/mysql", VarFilePath: "/tmp/foo/mysql/vars.yml"},
	}
	bm := map[string][]ghclient.BranchInfo{
		"foo/bar":    []ghclient.BranchInfo{ghclient.BranchInfo{Name: "feature-spam", SHA: "9999"}, ghclient.BranchInfo{Name: "release", SHA: "9998"}},
		"foo/qwerty": []ghclient.BranchInfo{ghclient.BranchInfo{Name: "feature-spam", SHA: "9997"}, ghclient.BranchInfo{Name: "release", SHA: "9996"}},
		"foo/mysql":  []ghclient.BranchInfo{ghclient.BranchInfo{Name: "feature-spam", SHA: "9995"}, ghclient.BranchInfo{Name: "release", SHA: "9994"}, ghclient.BranchInfo{Name: "master", SHA: "9993"}},
	}
	cir := map[string]error{
		"foo-bar":    nil,
		"foo-qwerty": nil,
		"foo-mysql":  nil,
	}
	rc2 := rc
	rc2.Dependencies.Direct = append(rc2.Dependencies.Direct, models.RepoConfigDependency{Name: "foo-postgres", Repo: "foo/postgres", AppMetadata: models.RepoConfigAppMetadata{Repo: "foo/postgres", Ref: "9992", Branch: "master"}, DefaultBranch: "master"})
	cl2 := cl
	cl2["foo-postgres"] = meta.ChartLocation{ChartPath: "/tmp/foo/postgres", VarFilePath: "/tmp/foo/postgres/vars.yml"}
	bm2 := bm
	bm2["foo/postgres"] = []ghclient.BranchInfo{ghclient.BranchInfo{Name: "master", SHA: "9992"}}
	cir2 := cir
	cir2["foo-postgres"] = nil
	cases := []struct {
		name               string
		inputRDD           models.RepoRevisionData
		inputEnv           models.QAEnvironment
		inputK8sEnv        models.KubernetesEnvironment
		inputReleases      []models.HelmRelease
		inputRC            models.RepoConfig
		inputCL            meta.ChartLocations
		inputBranches      map[string][]ghclient.BranchInfo
		chartInstallResult map[string]error
		delay, timeout     time.Duration
		verifyFunc         func(error, persistence.DataLayer, *notificationTracker, *testing.T)
	}{
		{
			"update matching signature", rdd, env, k8senv, releases, rc, cl, bm, cir, 0, 0,
			func(err error, dl persistence.DataLayer, nt *notificationTracker, st *testing.T) {
				if err != nil {
					st.Fatalf("should have succeeded: %v", err)
				}
				rlses, _ := dl.GetHelmReleasesForEnv(context.Background(), env.Name)
				if len(rlses) != 3 {
					st.Fatalf("bad release count: %v", len(rlses))
				}
				for _, r := range rlses {
					switch r.Name {
					case models.GetName(rdd.Repo):
						if r.RevisionSHA != rdd.SourceSHA {
							st.Fatalf("bad revision for %v: %v", rdd.Repo, r.RevisionSHA)
						}
					case "foo-qwerty":
						if r.RevisionSHA != "9997" {
							st.Fatalf("bad revision for qwerty: %v", r.RevisionSHA)
						}
					case "foo-mysql":
						if r.RevisionSHA != "9993" {
							st.Fatalf("bad revision for mysql: %v", r.RevisionSHA)
						}
					default:
						st.Fatalf("unknown name: %v", r.Name)
					}
				}
			},
		},
		{
			"update different signature", rdd, env, k8senv, releases, rc2, cl2, bm2, cir2, 0, 0,
			func(err error, dl persistence.DataLayer, nt *notificationTracker, st *testing.T) {
				if err != nil {
					st.Fatalf("should have succeeded: %v", err)
				}
				rlses, _ := dl.GetHelmReleasesForEnv(context.Background(), env.Name)
				if len(rlses) != 4 {
					st.Fatalf("bad release count: %v: %v", len(rlses), rlses)
				}
				for _, r := range rlses {
					switch r.Name {
					case models.GetName(rdd.Repo):
						if r.RevisionSHA != rdd.SourceSHA {
							st.Fatalf("bad revision for %v: %v", rdd.Repo, r.RevisionSHA)
						}
					case "foo-qwerty":
						if r.RevisionSHA != "9997" {
							st.Fatalf("bad revision for qwerty: %v", r.RevisionSHA)
						}
					case "foo-mysql":
						if r.RevisionSHA != "9993" {
							st.Fatalf("bad revision for mysql: %v", r.RevisionSHA)
						}
					case "foo-postgres":
						if r.RevisionSHA != "9992" {
							st.Fatalf("bad revision for postgres: %v", r.RevisionSHA)
						}
					default:
						st.Fatalf("unknown name: %v", r.Name)
					}
				}
			},
		},
		{
			"missing k8senv", rdd, env, models.KubernetesEnvironment{}, releases, rc, cl, bm, cir, 0, 0,
			func(err error, dl persistence.DataLayer, nt *notificationTracker, st *testing.T) {
				if err == nil {
					st.Fatalf("should have failed with missing k8s env")
				}
				if !strings.Contains(err.Error(), "missing k8s environment") {
					st.Fatalf("unexpected error: %v", err)
				}
			},
		},
		{
			"timeout", rdd, env, k8senv, releases, rc, cl, bm, cir, 50 * time.Millisecond, 50 * time.Millisecond,
			func(err error, dl persistence.DataLayer, nt *notificationTracker, st *testing.T) {
				if err == nil {
					st.Fatalf("should have failed with timeout")
				}
				ns := nt.get()
				var found bool
				for _, n := range ns {
					if n.Event == notifier.Failure && strings.Contains(n.Data.ErrorMessage, "timeout reached") {
						found = true
					}
				}
				if !found {
					st.Fatalf("did not get timeout notification: %+v", ns)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dl := persistence.NewDelayedFakeDataLayer(c.delay)
			dl.CreateQAEnvironment(context.Background(), &c.inputEnv)
			if c.inputK8sEnv.EnvName != "" {
				dl.CreateK8sEnv(context.Background(), &c.inputK8sEnv)
			}
			dl.CreateHelmReleasesForEnv(context.Background(), c.inputReleases)
			fg := &meta.FakeGetter{
				GetFunc: func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) {
					return &c.inputRC, nil
				},
				FetchChartsFunc: func(ctx context.Context, rc *models.RepoConfig, basePath string) (meta.ChartLocations, error) {
					return c.inputCL, nil
				},
			}
			frc := &ghclient.FakeRepoClient{
				GetBranchesFunc: func(ctx context.Context, name string) ([]ghclient.BranchInfo, error) {
					return c.inputBranches[name], nil
				},
				GetCommitMessageFunc: func(ctx context.Context, repo string, ref string) (string, error) { return "commit msg", nil },
				SetStatusFunc:        func(context.Context, string, string, *ghclient.CommitStatus) error { return nil },
			}
			releases := []string{}
			for _, r := range c.inputReleases {
				releases = append(releases, r.Release)
			}
			ci := &metahelm.FakeInstaller{
				ChartUpgradeFunc: func(repo string, k8senv *models.KubernetesEnvironment, location metahelm.ChartLocation) error {
					return c.chartInstallResult[repo]
				},
				ChartInstallFunc: func(repo string, location metahelm.ChartLocation) error {
					return c.chartInstallResult[repo]
				},
				DL:           dl,
				HelmReleases: releases,
				KC:           k8sfake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: c.inputK8sEnv.Namespace}}),
			}
			nt := newNotificationTracker()
			plf, err := locker.NewFakePreemptiveLockerFactory(
				[]locker.LockProviderOption{locker.WithLockTimeout(time.Second)},
				locker.WithLockDelay(time.Millisecond),
			)
			if err != nil {
				t.Fatalf("error creating new preemptive locker factory: %v", err)
			}
			m := Manager{
				DL:               dl,
				PLF:              plf,
				NF:               nt.sender,
				MC:               &metrics.FakeCollector{},
				NG:               &namegen.FakeNameGenerator{},
				FS:               memfs.New(),
				MG:               fg,
				RC:               frc,
				CI:               ci,
				OperationTimeout: c.timeout,
			}

			el := &eventlogger.Logger{DL: dl}
			el.Init([]byte{}, c.inputRDD.Repo, c.inputRDD.PullRequest)
			ctx := eventlogger.NewEventLoggerContext(context.Background(), el)
			_, err = m.Update(ctx, c.inputRDD)
			c.verifyFunc(err, dl, nt, t)
		})
	}
}

func TestDelete(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	rdd := models.RepoRevisionData{
		Repo:         "foo/bar",
		PullRequest:  1,
		SourceSHA:    "asdf",
		SourceBranch: "feature-spam",
		BaseSHA:      "1234",
		BaseBranch:   "release",
	}
	env := models.QAEnvironment{
		Name:        "some-name",
		Repo:        rdd.Repo,
		PullRequest: rdd.PullRequest,
		Status:      models.Success,
		RefMap: models.RefMap{
			"foo/bar":    "feature-spam",
			"foo/qwerty": "feature-spam",
			"foo/mysql":  "master",
		},
		CommitSHAMap: models.RefMap{
			"foo/bar":    "1111",
			"foo/qwerty": "2222",
			"foo/mysql":  "3333",
		},
	}
	failedenv := models.QAEnvironment{
		Name:        "some-name",
		Repo:        rdd.Repo,
		PullRequest: rdd.PullRequest,
		Status:      models.Failure,
		RefMap: models.RefMap{
			"foo/bar":    "feature-spam",
			"foo/qwerty": "feature-spam",
			"foo/mysql":  "master",
		},
		CommitSHAMap: models.RefMap{
			"foo/bar":    "1111",
			"foo/qwerty": "2222",
			"foo/mysql":  "3333",
		},
	}
	k8senv := models.KubernetesEnvironment{
		EnvName:   env.Name,
		Namespace: "nitro-1234-" + env.Name,
	}
	releases := []models.HelmRelease{
		models.HelmRelease{EnvName: env.Name, Name: models.GetName(rdd.Repo), RevisionSHA: env.CommitSHAMap[rdd.Repo], Release: "release-name-" + rdd.Repo},
		models.HelmRelease{EnvName: env.Name, Name: "foo-qwerty", RevisionSHA: env.CommitSHAMap["foo/qwerty"], Release: "release-name-foo/qwerty"},
		models.HelmRelease{EnvName: env.Name, Name: "foo-mysql", RevisionSHA: env.CommitSHAMap["foo/mysql"], Release: "release-name-foo/mysql"},
	}
	cases := []struct {
		name          string
		inputRDD      models.RepoRevisionData
		inputEnv      models.QAEnvironment
		inputK8sEnv   models.KubernetesEnvironment
		inputReleases []models.HelmRelease
		verifyFunc    func(error, *testing.T)
	}{
		{
			"delete", rdd, env, k8senv, releases,
			func(err error, st *testing.T) {
				if err != nil {
					st.Fatalf("should have succeeded: %v", err)
				}
				rls, _ := dl.GetHelmReleasesForEnv(context.Background(), env.Name)
				if len(rls) != 0 {
					st.Fatalf("bad release count: %v", len(rls))
				}
				ke2, _ := dl.GetK8sEnv(context.Background(), env.Name)
				if ke2 != nil {
					st.Fatalf("k8s env should be deleted: %v", ke2)
				}
				e2, _ := dl.GetQAEnvironment(context.Background(), env.Name)
				if e2.Status != models.Destroyed {
					st.Fatalf("bad status: %v", e2.Status)
				}
			},
		},
		{
			"delete with failed env", rdd, failedenv, models.KubernetesEnvironment{}, []models.HelmRelease{},
			func(err error, st *testing.T) {
				if err != nil {
					st.Fatalf("should have succeeded: %v", err)
				}
				rls, _ := dl.GetHelmReleasesForEnv(context.Background(), failedenv.Name)
				if len(rls) != 0 {
					st.Fatalf("bad release count: %v", len(rls))
				}
				ke2, _ := dl.GetK8sEnv(context.Background(), failedenv.Name)
				if ke2 != nil {
					st.Fatalf("k8s env should be deleted: %v", ke2)
				}
				e2, _ := dl.GetQAEnvironment(context.Background(), failedenv.Name)
				if e2.Status != models.Destroyed {
					st.Fatalf("bad status: %v", e2.Status)
				}
			},
		},
		{
			"missing k8s environment", rdd, env, models.KubernetesEnvironment{}, []models.HelmRelease{},
			func(err error, st *testing.T) {
				if err == nil {
					st.Fatalf("should have failed with missing k8s env")
				}
				if !strings.Contains(err.Error(), "missing k8s environment") {
					st.Fatalf("unexpected error: %v", err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dl.CreateQAEnvironment(context.Background(), &c.inputEnv)
			if c.inputK8sEnv.EnvName != "" {
				dl.CreateK8sEnv(context.Background(), &c.inputK8sEnv)
			}
			if len(c.inputReleases) > 0 {
				dl.CreateHelmReleasesForEnv(context.Background(), c.inputReleases)
			}
			releases := []string{}
			for _, r := range c.inputReleases {
				releases = append(releases, r.Release)
			}
			fg := &meta.FakeGetter{
				GetFunc: func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) {
					return &models.RepoConfig{}, nil
				},
			}
			frc := &ghclient.FakeRepoClient{
				GetCommitMessageFunc: func(context.Context, string, string) (string, error) { return "", nil },
			}
			ci := &metahelm.FakeInstaller{
				DL:           dl,
				HelmReleases: releases,
			}
			plf, err := locker.NewFakePreemptiveLockerFactory(
				[]locker.LockProviderOption{locker.WithLockTimeout(time.Second)},
				locker.WithLockDelay(time.Millisecond),
			)
			if err != nil {
				t.Fatalf("error creating new preemptive locker factory: %v", err)
			}
			m := Manager{
				DL:  dl,
				PLF: plf,
				NF:  testNF,
				MC:  &metrics.FakeCollector{},
				MG:  fg,
				RC:  frc,
				CI:  ci,
			}

			el := &eventlogger.Logger{DL: dl}
			el.Init([]byte{}, c.inputRDD.Repo, c.inputRDD.PullRequest)
			ctx := eventlogger.NewEventLoggerContext(context.Background(), el)
			err = m.Delete(ctx, &c.inputRDD, models.DestroyApiRequest)
			time.Sleep(10 * time.Millisecond) // give time for async delete to complete
			c.verifyFunc(err, t)
		})
	}
}

var testChartError = metahelmlib.ChartError{
	HelmError: errors.New("some helm error"),
	Level:     1,
	FailedDeployments: map[string][]metahelmlib.FailedPod{
		"webserver": []metahelmlib.FailedPod{
			metahelmlib.FailedPod{
				Name:    "webserver-21341234",
				Phase:   "Pending",
				Reason:  "CrashLoopBackoff",
				Message: "container exited with status 128",
				Conditions: []corev1.PodCondition{
					corev1.PodCondition{
						Type:               "PodScheduled",
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
					},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					corev1.ContainerStatus{
						Name: "apache",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 128,
								Message:  "foo",
								Reason:   "some reason",
							},
						},
					},
				},
				Logs: map[string][]byte{
					"apache": []byte("started up\nsomething happened\nomg error, bail out\n"),
				},
			},
		},
	},
	FailedJobs: map[string][]metahelmlib.FailedPod{
		"migrations": []metahelmlib.FailedPod{
			metahelmlib.FailedPod{
				Name:    "migrations-21341234",
				Phase:   "Pending",
				Reason:  "CrashLoopBackoff",
				Message: "container exited with status 1",
				Conditions: []corev1.PodCondition{
					corev1.PodCondition{
						Type:               "PodScheduled",
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
					},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					corev1.ContainerStatus{
						Name: "migrations",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 1,
								Message:  "foo",
								Reason:   "some reason",
							},
						},
					},
				},
				Logs: map[string][]byte{
					"migrations": []byte("started up\nsomething happened\nomg error, bail out\n"),
				},
			},
		},
	},
	FailedDaemonSets: map[string][]metahelmlib.FailedPod{
		"something": []metahelmlib.FailedPod{
			metahelmlib.FailedPod{
				Name:    "something-21341234",
				Phase:   "Pending",
				Reason:  "CrashLoopBackoff",
				Message: "container exited with status 128",
				Conditions: []corev1.PodCondition{
					corev1.PodCondition{
						Type:               "PodScheduled",
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
					},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					corev1.ContainerStatus{
						Name: "something",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 128,
								Message:  "foo",
								Reason:   "some reason",
							},
						},
					},
				},
				Logs: map[string][]byte{
					"something": []byte("started up\nsomething happened\nomg error, bail out\n"),
				},
			},
		},
	},
}

func TestProcessEnvConfig(t *testing.T) {
	type fields struct {
		DL persistence.DataLayer
		MG meta.Getter
	}
	type args struct {
		ctx context.Context
		env *models.QAEnvironment
		rd  *models.RepoRevisionData
	}
	env := models.QAEnvironment{
		Name:         "foo-bar",
		Repo:         "acme/foobar",
		SourceBranch: "feature-foo",
		SourceSHA:    "asdf",
		PullRequest:  1,
	}
	rc := models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:   env.Repo,
			Branch: env.SourceBranch,
			Ref:    "aaaa",
		},
	}
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), &env)
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *newEnv
		wantErr bool
	}{
		{
			name: "updated env record",
			fields: fields{
				DL: dl,
				MG: &meta.FakeGetter{
					GetFunc: func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) {
						return &rc, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				env: &env,
				rd: &models.RepoRevisionData{
					Repo:         env.Repo,
					PullRequest:  env.PullRequest,
					SourceBranch: env.SourceBranch,
					BaseBranch:   "master",
					SourceSHA:    "aaaa",
					BaseSHA:      "bbbb",
				},
			},
			want: &newEnv{
				env: &models.QAEnvironment{
					Name:         "foo-bar",
					Repo:         "acme/foobar",
					PullRequest:  1,
					SourceBranch: "feature-foo",
					BaseBranch:   "master",
					SourceSHA:    "aaaa",
					BaseSHA:      "bbbb",
				},
				rc: &rc,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manager{
				DL: tt.fields.DL,
				MG: tt.fields.MG,
				MC: &metrics.FakeCollector{},
			}
			got, err := m.processEnvConfig(tt.args.ctx, tt.args.env, tt.args.rd)
			if (err != nil) != tt.wantErr {
				t.Errorf("Manager.processEnvConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got.env.Repo != tt.want.env.Repo {
				t.Fatalf("bad repo: %v wanted %v", got.env.Repo, tt.want.env.Repo)
			}
			if got.env.SourceBranch != tt.want.env.SourceBranch {
				t.Fatalf("bad source branch: %v wanted %v", got.env.SourceBranch, tt.want.env.SourceBranch)
			}
			if got.env.SourceSHA != tt.want.env.SourceSHA {
				t.Fatalf("bad source sha: %v wanted %v", got.env.SourceSHA, tt.want.env.SourceSHA)
			}
			if got.env.BaseBranch != tt.want.env.BaseBranch {
				t.Fatalf("bad base branch: %v wanted %v", got.env.BaseBranch, tt.want.env.BaseBranch)
			}
			if got.env.BaseSHA != tt.want.env.BaseSHA {
				t.Fatalf("bad base sha: %v wanted %v", got.env.BaseSHA, tt.want.env.BaseSHA)
			}
			if i := len(got.env.RefMap); i != 1 {
				t.Fatalf("bad length for ref map: %v wanted 1", i)
			}
			if i := len(got.env.CommitSHAMap); i != 1 {
				t.Fatalf("bad length for commit sha map: %v wanted 1", i)
			}
			if ref, ok := got.env.RefMap[env.Repo]; !ok || ref != env.SourceBranch {
				t.Fatalf("missing or bad ref map entry: %v, %v", ok, ref)
			}
			if sha, ok := got.env.CommitSHAMap[env.Repo]; !ok || sha != env.SourceSHA {
				t.Fatalf("missing or bad commit sha map entry: %v, %v", ok, sha)
			}
		})
	}
}

func TestSetGithubCommitStatus(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	m := &Manager{
		RC: &ghclient.FakeRepoClient{
			SetStatusFunc: func(context.Context, string, string, *ghclient.CommitStatus) error { return nil },
		},
		DL:        dl,
		UIBaseURL: "https://foobar.com",
	}

	tests := []struct {
		name    string
		env     *newEnv
		inputCS models.CommitStatus
		errMsg  string
		want    *ghclient.CommitStatus
	}{
		{
			name: "Configured template - success",
			env: &newEnv{
				env: &models.QAEnvironment{
					Name: "some-environment-name",
				},
				rc: &models.RepoConfig{
					Notifications: models.Notifications{
						GitHub: models.GitHubNotifications{
							CommitStatuses: models.CommitStatuses{
								Templates: map[string]models.CommitStatusTemplate{
									"success": models.CommitStatusTemplate{
										Description: "An environment for {{ .EnvName }} has been created",
										TargetURL:   "https://{{.EnvName}}.shave.io",
									},
								},
							},
						},
					},
				},
			},
			inputCS: models.CommitStatusSuccess,
			want: &ghclient.CommitStatus{
				Context:     "Acyl",
				Status:      "success",
				Description: "An environment for some-environment-name has been created",
				TargetURL:   fmt.Sprintf("%v/ui/event/status?id=%v", m.UIBaseURL, uuid.UUID{}.String()),
			},
		},
		{
			name: "Configured template - pending",
			env: &newEnv{
				env: &models.QAEnvironment{
					Name: "some-environment-name",
				},
				rc: &models.RepoConfig{
					Notifications: models.Notifications{
						GitHub: models.GitHubNotifications{
							CommitStatuses: models.CommitStatuses{
								Templates: map[string]models.CommitStatusTemplate{
									"pending": models.CommitStatusTemplate{
										Description: "An environment for {{ .EnvName }} is being created",
										TargetURL:   "https://{{ .EnvName }}.shave.io",
									},
								},
							},
						},
					},
				},
			},
			inputCS: models.CommitStatusPending,
			want: &ghclient.CommitStatus{
				Context:     "Acyl",
				Status:      "pending",
				Description: "An environment for some-environment-name is being created",
				TargetURL:   fmt.Sprintf("%v/ui/event/status?id=%v", m.UIBaseURL, uuid.UUID{}.String()),
			},
		},
		{
			name: "Configured template - failure",
			env: &newEnv{
				env: &models.QAEnvironment{
					Name: "some-environment-name",
				},
				rc: &models.RepoConfig{
					Notifications: models.Notifications{
						GitHub: models.GitHubNotifications{
							CommitStatuses: models.CommitStatuses{
								Templates: map[string]models.CommitStatusTemplate{
									"failure": models.CommitStatusTemplate{
										Description: "An environment for {{ .EnvName }} has failed",
										TargetURL:   "https://{{ .EnvName }}.shave.io",
									},
								},
							},
						},
					},
				},
			},
			inputCS: models.CommitStatusFailure,
			want: &ghclient.CommitStatus{
				Context:     "Acyl",
				Status:      "failure",
				Description: "An environment for some-environment-name has failed",
				TargetURL:   fmt.Sprintf("%v/ui/event/status?id=%v", m.UIBaseURL, uuid.UUID{}.String()),
			},
		},
		{
			name: "Default template - success",
			env: &newEnv{
				env: &models.QAEnvironment{
					Name: "some-environment-name",
				},
				rc: &models.RepoConfig{},
			},
			inputCS: models.CommitStatusSuccess,
			want: &ghclient.CommitStatus{
				Context:     "Acyl",
				Status:      "success",
				Description: "The Acyl environment some-environment-name was created successfully.",
				TargetURL:   fmt.Sprintf("%v/ui/event/status?id=%v", m.UIBaseURL, uuid.UUID{}.String()),
			},
		},
		{
			name: "Default template - pending",
			env: &newEnv{
				env: &models.QAEnvironment{
					Name: "some-environment-name",
				},
				rc: &models.RepoConfig{},
			},
			inputCS: models.CommitStatusPending,
			want: &ghclient.CommitStatus{
				Context:     "Acyl",
				Status:      "pending",
				Description: "The Acyl environment some-environment-name is being created.",
				TargetURL:   fmt.Sprintf("%v/ui/event/status?id=%v", m.UIBaseURL, uuid.UUID{}.String()),
			},
		},
		{
			name: "Default template - failure",
			env: &newEnv{
				env: &models.QAEnvironment{
					Name: "some-environment-name",
				},
				rc: &models.RepoConfig{},
			},
			inputCS: models.CommitStatusFailure,
			errMsg:  "invalid helm chart",
			want: &ghclient.CommitStatus{
				Context:     "Acyl",
				Status:      "failure",
				Description: "The Acyl environment some-environment-name failed.",
				TargetURL:   fmt.Sprintf("%v/ui/event/status?id=%v", m.UIBaseURL, uuid.UUID{}.String()),
			},
		},
		{
			name: "Template - error message",
			env: &newEnv{
				env: &models.QAEnvironment{
					Name: "some-environment-name",
				},
				rc: &models.RepoConfig{
					Notifications: models.Notifications{
						GitHub: models.GitHubNotifications{
							CommitStatuses: models.CommitStatuses{
								Templates: map[string]models.CommitStatusTemplate{
									"failure": models.CommitStatusTemplate{
										Description: "The Acyl environment for {{ .EnvName }} failed. Reason: {{ .ErrorMessage }}",
										TargetURL:   "",
									},
								},
							},
						},
					},
				},
			},
			inputCS: models.CommitStatusFailure,
			errMsg:  "invalid helm chart",
			want: &ghclient.CommitStatus{
				Context:     "Acyl",
				Status:      "failure",
				Description: "The Acyl environment for some-environment-name failed. Reason: invalid helm chart",
				TargetURL:   fmt.Sprintf("%v/ui/event/status?id=%v", m.UIBaseURL, uuid.UUID{}.String()),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := m.setGithubCommitStatus(context.Background(), &models.RepoRevisionData{}, tt.env, tt.inputCS, tt.errMsg)
			if err != nil {
				t.Errorf("Expected no error, got %v", err)
			}
			if diff := cmp.Diff(out, tt.want); diff != "" {
				t.Errorf("setGithubCommitStatus() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCreateEnvStatusUnknown(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	id, _ := uuid.NewRandom()
	elog := eventlogger.Logger{DL: dl, ID: id, Sink: os.Stderr}
	ctx := eventlogger.NewEventLoggerContext(context.Background(), &elog)
	rrd := models.RepoRevisionData{
		Repo:         "foo/bar",
		PullRequest:  1,
		SourceSHA:    "asdf",
		SourceBranch: "feature-spam",
		BaseSHA:      "1234",
		BaseBranch:   "release",
	}
	rc := models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:          "foo/bar",
			Ref:           "asdf",
			Branch:        "feature-spam",
			ChartPath:     ".chart/bar",
			ChartVarsPath: "./chart/vars.yml",
			Image:         "foo/bar",
			ChartTagValue: "image.tag",
		},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "foo-qwerty",
					Repo: "foo/qwerty",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:   "foo/qwerty",
						Ref:    "aaaa",
						Branch: "feature-spam",
					},
					Requires: []string{"foo/mysql"},
				},
				models.RepoConfigDependency{
					Name: "foo-mysql",
					Repo: "foo/mysql",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:   "foo/mysql",
						Ref:    "bbbb",
						Branch: "master",
					},
					DefaultBranch: "master",
				},
			},
		},
	}
	cl := meta.ChartLocations{
		"foo-bar":    meta.ChartLocation{ChartPath: "/tmp/foo/bar", VarFilePath: "/tmp/foo/bar/vars.yml"},
		"foo-qwerty": meta.ChartLocation{ChartPath: "/tmp/foo/qwerty", VarFilePath: "/tmp/foo/qwerty/vars.yml"},
		"foo-mysql":  meta.ChartLocation{ChartPath: "/tmp/foo/mysql", VarFilePath: "/tmp/foo/mysql/vars.yml"},
	}
	bm := map[string][]ghclient.BranchInfo{
		"foo/bar":    []ghclient.BranchInfo{ghclient.BranchInfo{Name: "feature-spam"}, ghclient.BranchInfo{Name: "release"}},
		"foo/qwerty": []ghclient.BranchInfo{ghclient.BranchInfo{Name: "feature-spam"}, ghclient.BranchInfo{Name: "release"}},
		"foo/mysql":  []ghclient.BranchInfo{ghclient.BranchInfo{Name: "feature-spam"}, ghclient.BranchInfo{Name: "release"}, ghclient.BranchInfo{Name: "master"}},
	}
	fg := &meta.FakeGetter{
		GetFunc: func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) {
			return &rc, nil
		},
		FetchChartsFunc: func(ctx context.Context, rc *models.RepoConfig, basePath string) (meta.ChartLocations, error) {
			return cl, nil
		},
	}
	frc := &ghclient.FakeRepoClient{
		GetBranchesFunc: func(ctx context.Context, name string) ([]ghclient.BranchInfo, error) {
			return bm[name], nil
		},
		GetCommitMessageFunc: func(context.Context, string, string) (string, error) { return "", nil },
		SetStatusFunc:        func(context.Context, string, string, *ghclient.CommitStatus) error { return nil },
	}
	ci := &metahelm.FakeInstaller{
		ChartInstallFunc: func(repo string, location metahelm.ChartLocation) error {
			return map[string]error{}[repo]
		},
		DL: dl,
		KC: k8sfake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nitro-1234-some-random-name"}}),
	}
	elog.Init([]byte{}, rrd.Repo, rrd.PullRequest)
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
	nt := newNotificationTracker()
	plf, err := locker.NewFakePreemptiveLockerFactory(
		[]locker.LockProviderOption{
			locker.WithLockTimeout(2 * time.Second),
		},
		locker.WithLockDelay(time.Second),
	)
	if err != nil {
		t.Fatalf("error creating new preemptive locker factory: %v", err)
	}
	m := Manager{
		DL:               dl,
		PLF:              plf,
		NF:               nt.sender,
		MC:               &metrics.FakeCollector{},
		NG:               &namegen.FakeNameGenerator{},
		FS:               memfs.New(),
		MG:               fg,
		RC:               frc,
		CI:               ci,
		GlobalLimit:      0,
		OperationTimeout: 0,
	}
	envName, err := m.create(ctx, &rrd)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if envName == unknown {
		t.Fatalf("env name should have updated from %v: %v", unknown, envName)
	}
	env, err := dl.GetEventStatus(id)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if env.Config.Status != models.DoneStatus {
		t.Fatalf("bad status: %v", env.Config.Status)
	}
}
