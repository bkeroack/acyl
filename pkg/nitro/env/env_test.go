package env

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/locker"
	"github.com/dollarshaveclub/acyl/pkg/memfs"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/namegen"
	"github.com/dollarshaveclub/acyl/pkg/nitro/meta"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metahelm"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metrics"
	"github.com/dollarshaveclub/acyl/pkg/nitro/notifier"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/dollarshaveclub/acyl/pkg/s3"
	metahelmlib "github.com/dollarshaveclub/metahelm/pkg/metahelm"
	"github.com/nlopes/slack"
	"github.com/pkg/errors"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLockingOperation(t *testing.T) {
	c := make(chan struct{})
	close(c)
	m := Manager{
		LP: &locker.FakePreemptiveLockProvider{
			ChannelFactory: func() chan struct{} { return c },
		},
		MC: &metrics.FakeCollector{},
	}
	f := func(ctx context.Context) error {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-timer.C:
			return errors.New("timer expired")
		case <-ctx.Done():
			return nil
		}
	}
	if err := m.lockingOperation(context.Background(), "foo", "foo", "1", f); err != nil {
		t.Fatalf("should have been preempted")
	}
	c = make(chan struct{})
	err := m.lockingOperation(context.Background(), "foo", "foo", "1", f)
	if err == nil {
		t.Fatalf("should have timed out")
	}
	if !strings.Contains(err.Error(), "timer expired") {
		t.Fatalf("unexpected error: %v", err)
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
	testSpan, _ := tracer.SpanFromContext(context.Background())
	ctxWithSpan := newSpanContext(context.Background(), testSpan)
	env, err := m.generateNewEnv(ctxWithSpan, &models.RepoRevisionData{Repo: "foo/bar", User: "foo", PullRequest: 1, BaseBranch: "master", SourceSHA: "asdf", SourceBranch: "foo"})
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
	env, err = m.generateNewEnv(ctxWithSpan, &models.RepoRevisionData{Repo: "foo/bar", User: "foo", PullRequest: 1, BaseBranch: "master", SourceSHA: "1234", SourceBranch: "foo"})
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
	testSpan, _ := tracer.SpanFromContext(context.Background())
	ctxWithSpan := newSpanContext(context.Background(), testSpan)
	_, _, err := m.fetchCharts(ctxWithSpan, "foo-bar", &models.RepoConfig{})
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestGetEnv(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(&models.QAEnvironment{Name: "foo-bar", Repo: "foo/bar", PullRequest: 99, Status: models.Success})
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
	cases := []struct {
		name               string
		inputRRD           models.RepoRevisionData
		inputRC            models.RepoConfig
		inputCL            meta.ChartLocations
		inputBranches      map[string][]ghclient.BranchInfo
		chartInstallResult map[string]error
		verifyFunc         func(string, error, *testing.T)
	}{
		{
			"branch matching", rdd, rc, cl, bm, cir,
			func(name string, err error, st *testing.T) {
				if err != nil {
					st.Fatalf("should have succeeded: %v", err)
				}
				env, _ := dl.GetQAEnvironment(name)
				if env == nil {
					st.Fatalf("env missing")
				}
				k8senv, _ := dl.GetK8sEnv(name)
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
				releases, _ := dl.GetHelmReleasesForEnv(name)
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
			},
			func(name string, err error, st *testing.T) {
				if err == nil {
					st.Fatalf("should have failed with install error")
				}
				if !strings.Contains(err.Error(), "install error") {
					st.Fatalf("unexpected error: %v", err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
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
			}
			m := Manager{
				DL: dl,
				LP: &locker.FakePreemptiveLockProvider{
					ChannelFactory: func() chan struct{} {
						lch := make(chan struct{})
						//close(lch)
						return lch
					},
				},
				NF: testNF,
				MC: &metrics.FakeCollector{},
				NG: &namegen.FakeNameGenerator{},
				FS: memfs.New(),
				MG: fg,
				RC: frc,
				CI: ci,
			}
			name, err := m.Create(context.Background(), c.inputRRD)
			c.verifyFunc(name, err, t)
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
		verifyFunc         func(error, persistence.DataLayer, *testing.T)
	}{
		{
			"update matching signature", rdd, env, k8senv, releases, rc, cl, bm, cir,
			func(err error, dl persistence.DataLayer, st *testing.T) {
				if err != nil {
					st.Fatalf("should have succeeded: %v", err)
				}
				rlses, _ := dl.GetHelmReleasesForEnv(env.Name)
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
			"update different signature", rdd, env, k8senv, releases, rc2, cl2, bm2, cir2,
			func(err error, dl persistence.DataLayer, st *testing.T) {
				if err != nil {
					st.Fatalf("should have succeeded: %v", err)
				}
				rlses, _ := dl.GetHelmReleasesForEnv(env.Name)
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
			"missing k8senv", rdd, env, models.KubernetesEnvironment{}, releases, rc, cl, bm, cir,
			func(err error, dl persistence.DataLayer, st *testing.T) {
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
			dl := persistence.NewFakeDataLayer()
			dl.CreateQAEnvironment(&c.inputEnv)
			if c.inputK8sEnv.EnvName != "" {
				dl.CreateK8sEnv(&c.inputK8sEnv)
			}
			dl.CreateHelmReleasesForEnv(c.inputReleases)
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
			}
			m := Manager{
				DL: dl,
				LP: &locker.FakePreemptiveLockProvider{
					ChannelFactory: func() chan struct{} {
						lch := make(chan struct{})
						//close(lch)
						return lch
					},
				},
				NF: testNF,
				MC: &metrics.FakeCollector{},
				NG: &namegen.FakeNameGenerator{},
				FS: memfs.New(),
				MG: fg,
				RC: frc,
				CI: ci,
			}
			_, err := m.Update(context.Background(), c.inputRDD)
			c.verifyFunc(err, dl, t)
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
				rls, _ := dl.GetHelmReleasesForEnv(env.Name)
				if len(rls) != 0 {
					st.Fatalf("bad release count: %v", len(rls))
				}
				ke2, _ := dl.GetK8sEnv(env.Name)
				if ke2 != nil {
					st.Fatalf("k8s env should be deleted: %v", ke2)
				}
				e2, _ := dl.GetQAEnvironment(env.Name)
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
				rls, _ := dl.GetHelmReleasesForEnv(failedenv.Name)
				if len(rls) != 0 {
					st.Fatalf("bad release count: %v", len(rls))
				}
				ke2, _ := dl.GetK8sEnv(failedenv.Name)
				if ke2 != nil {
					st.Fatalf("k8s env should be deleted: %v", ke2)
				}
				e2, _ := dl.GetQAEnvironment(failedenv.Name)
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
			dl.CreateQAEnvironment(&c.inputEnv)
			if c.inputK8sEnv.EnvName != "" {
				dl.CreateK8sEnv(&c.inputK8sEnv)
			}
			if len(c.inputReleases) > 0 {
				dl.CreateHelmReleasesForEnv(c.inputReleases)
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
			m := Manager{
				DL: dl,
				LP: &locker.FakePreemptiveLockProvider{
					ChannelFactory: func() chan struct{} {
						lch := make(chan struct{})
						//close(lch)
						return lch
					},
				},
				NF: testNF,
				MC: &metrics.FakeCollector{},
				MG: fg,
				RC: frc,
				CI: ci,
			}
			err := m.Delete(context.Background(), &c.inputRDD, models.DestroyApiRequest)
			c.verifyFunc(err, t)
		})
	}
}

func TestRenderHTML(t *testing.T) {
	fd, err := ioutil.ReadFile("../../../assets/html/failedenv.html.tmpl")
	if err != nil {
		t.Fatalf("file read failed: %v", err)
	}
	m := Manager{}
	if err := m.InitFailureTemplate(fd); err != nil {
		t.Fatalf("load template failed: %v", err)
	}
	td := failureTemplateData{
		EnvName:        "clever-name",
		PullRequestURL: "https://github.com/dollarshaveclub/something/pull/99",
		StartedTime:    time.Now().UTC(),
		FailedTime:     time.Now().UTC(),
		CError: metahelmlib.ChartError{
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
		},
	}
	html, err := m.chartErrorRenderHTML(td)
	if err != nil {
		t.Fatalf("rendering failed: %v", err)
	}
	f, err := ioutil.TempFile("", "*.html")
	if err != nil {
		t.Fatalf("error creating temp file: %v", err)
	}
	defer os.Remove(f.Name())
	if n, err := f.Write(html); err != nil || n != len(html) {
		t.Fatalf("error writing or short write (%v/%v): %v", n, len(html), err)
	}
	if os.Getenv("ACYL_FAILED_HTML") != "" {
		if err := exec.Command("open", f.Name()).Run(); err != nil {
			t.Fatalf("error opening: %v", err)
		}
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

type fakeS3Pusher struct {
	t *testing.T
}

func (f *fakeS3Pusher) Push(contentType string, in io.Reader, opts s3.Options) (string, error) {
	f.t.Logf("pushing data (%v); opts: %+v", contentType, opts)
	return "https://s3.amazonaws.com/somepath", nil
}

func TestFailedEnvSlackNotification(t *testing.T) {
	dl := persistence.NewFakeDataLayer()
	m := Manager{
		DL: dl,
		MC: &metrics.FakeCollector{},
		AWSCreds: config.AWSCreds{
			AccessKeyID:     "asdf",
			SecretAccessKey: "asdf",
		},
		S3Config: config.S3Config{
			Region: "us-west-2",
			Bucket: "mybucket",
		},
		RC: &ghclient.FakeRepoClient{
			GetBranchesFunc: func(ctx context.Context, name string) ([]ghclient.BranchInfo, error) {
				return []ghclient.BranchInfo{ghclient.BranchInfo{Name: "branch", SHA: "abcd"}}, nil
			},
			GetCommitMessageFunc: func(ctx context.Context, repo string, ref string) (string, error) { return "commit msg", nil },
		},
		NF: func(lf func(string, ...interface{}), notifications models.Notifications, user string) notifier.Router {
			sb := &notifier.SlackBackend{
				Username: "john.doe",
				API: &notifier.FakeSlackAPIClient{
					PostFunc: func(channel, text string, params slack.PostMessageParameters) (string, string, error) {
						t.Logf("slack post: channel: %v; text: %v; params: %+v\n", channel, text, params)
						return "", "", nil
					},
				},
			}
			return &notifier.MultiRouter{Backends: []notifier.Backend{sb}}
		},
	}
	b, err := ioutil.ReadFile("../../../assets/html/failedenv.html.tmpl")
	if err != nil {
		t.Fatalf("file read failed: %v", err)
	}
	if err := m.InitFailureTemplate(b); err != nil {
		t.Fatalf("init template failed: %v", err)
	}
	m.s3p = &fakeS3Pusher{t: t}
	ne := &newEnv{
		env: &models.QAEnvironment{Name: "foo-bar", Repo: "foo/bar"},
		rc: &models.RepoConfig{
			Notifications: models.Notifications{
				Slack: models.SlackNotifications{
					Channels: &[]string{"engineering"},
				},
				Templates: models.DefaultNotificationTemplates,
			},
		},
	}
	dl.CreateQAEnvironment(ne.env)
	dl.CreateK8sEnv(&models.KubernetesEnvironment{
		EnvName:   ne.env.Name,
		Namespace: "nitro-1234-" + ne.env.Name,
	})
	err = m.handleMetahelmError(context.Background(), ne, testChartError, "chart installation failure")
	if err == nil {
		t.Fatalf("should have returned an error")
	}
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
	testSpan, _ := tracer.SpanFromContext(context.Background())
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(&env)
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
				ctx: newSpanContext(context.Background(), testSpan),
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
