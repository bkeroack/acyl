package meta

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"testing"

	"github.com/bkeroack/acyl/pkg/ghclient"
	"github.com/bkeroack/acyl/pkg/memfs"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/pkg/errors"
)

func readFiles() (map[string][]byte, error) {
	files, err := ioutil.ReadDir("./testdata/")
	if err != nil {
		return nil, fmt.Errorf("error reading directory: %w", err)
	}
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		d, err := ioutil.ReadFile("./testdata/" + f.Name())
		if err != nil {
			return nil, fmt.Errorf("error reading file: %v: %w", f.Name(), err)
		}
		out[f.Name()] = d
	}
	return out, nil
}

func TestMetaGetterGetV2(t *testing.T) {
	fd, err := readFiles()
	if err != nil {
		t.Fatalf("error reading files: %v", err)
	}
	rc := &ghclient.FakeRepoClient{
		GetFileContentsFunc: func(ctx context.Context, repo string, path string, ref string) ([]byte, error) {
			switch repo {
			case "foo/bar":
				switch path {
				case "acyl.yml":
					return fd["acyl-v2.yml"], nil
				case ".charts/my-chart/Chart.yaml":
					return nil, errors.New("should not be reading top-level repo chart")
				case ".charts/some-dependency/Chart.yaml":
					return fd["chart-envdep.yaml"], nil
				default:
					return nil, fmt.Errorf("foo/bar: unknown path: %v", path)
				}
			case "acme/widgets":
				switch path {
				case "acyl.yml":
					return fd["acyl-v2-dep.yml"], nil
				case ".charts/dependency/Chart.yaml":
					return fd["chart-dep.yaml"], nil
				case ".charts/transitive-dependency/Chart.yaml":
					return fd["chart-tdep.yaml"], nil
				case ".charts/transitive-dependency-2/Chart.yaml":
					return fd["chart-tdep2.yaml"], nil
				default:
					return nil, fmt.Errorf("acme/widgets: unknown path: %v", path)
				}
			case "acme/sprockets":
				switch path {
				case "acyl.yml":
					return fd["acyl-v2-env-dep.yml"], nil
				case ".charts/repo-dependency/Chart.yaml":
					return fd["chart-repo-envdep.yaml"], nil
				case ".charts/repo-env-transitive-dependency/Chart.yaml":
					return fd["chart-repo-tdep.yaml"], nil
				default:
					return nil, fmt.Errorf("acme/sprockets: unknown path: %v", path)
				}
			}
			return nil, fmt.Errorf("bad repo: %v", repo)
		},
		GetBranchesFunc: func(context.Context, string) ([]ghclient.BranchInfo, error) {
			return []ghclient.BranchInfo{ghclient.BranchInfo{Name: "master", SHA: "1234"}, ghclient.BranchInfo{Name: "foo", SHA: "zzzzz"}}, nil
		},
	}
	rd := models.RepoRevisionData{
		SourceBranch: "foo",
		BaseBranch:   "master",
		SourceSHA:    "zzzzz",
		BaseSHA:      "1234",
		PullRequest:  1,
		Repo:         "foo/bar",
	}
	g := DataGetter{
		RC: rc,
	}
	rcfg, err := g.Get(context.Background(), rd)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if i := len(rcfg.Dependencies.Direct); i != 4 {
		t.Fatalf("bad direct dependency count: %v", i)
	}
	if i := len(rcfg.Dependencies.Environment); i != 3 {
		t.Fatalf("bad env dependency count: %v", i)
	}
	ddm := make(map[string]*models.RepoConfigDependency, 4)
	edm := make(map[string]*models.RepoConfigDependency, 3)
	for i := range rcfg.Dependencies.Direct {
		d := &rcfg.Dependencies.Direct[i]
		ddm[d.Name] = d
	}
	for i := range rcfg.Dependencies.Environment {
		d := &rcfg.Dependencies.Environment[i]
		edm[d.Name] = d
	}
	if d, ok := ddm["widgets"]; ok {
		if d.Repo != "acme/widgets" {
			t.Fatalf("widgets: bad repo: %v", d.Repo)
		}
		if i := len(d.Requires); i != 2 {
			t.Fatalf("widgets: bad requires: %v", i)
		}
		for _, r := range d.Requires {
			if !strings.HasPrefix(r, "acme-widgets-transitive") {
				t.Fatalf("widgets: bad requirement: %v", r)
			}
		}
	} else {
		t.Fatalf("widgets missing from deps")
	}
	if _, ok := ddm["postgres-database"]; !ok {
		t.Fatalf("postgres missing from deps")
	}
	if _, ok := ddm["acme-widgets-transitive"]; !ok {
		t.Fatalf("transitive 1 missing from deps")
	}
	if d, ok := ddm["acme-widgets-transitive2"]; ok {
		if i := len(d.Requires); i != 1 {
			t.Fatalf("transitive 2: bad requires: %v", i)
		}
		if d.Requires[0] != "acme-widgets-transitive" {
			t.Fatalf("transitive 2: bad req value: %v", d.Requires[0])
		}
	} else {
		t.Fatalf("transitive 2 missing from deps")
	}
	if d, ok := edm["some-env-dependency"]; ok {
		if d.ChartPath != ".charts/some-dependency" {
			t.Fatalf("env dep: bad chart path: %v", d.ChartPath)
		}
	} else {
		t.Fatalf("'.charts/some-dependency' env dep missing from deps")
	}

	if d, ok := edm["sprockets"]; ok {
		if d.Repo != "acme/sprockets" {
			t.Fatalf("env dep: bad repo: %v", d.Repo)
		}
	} else {
		t.Fatalf("'acme/sprockets' env dep missing from deps")
	}

	if d, ok := edm["acme-sprockets-repo-transitive"]; ok {
		if d.ChartPath != ".charts/repo-env-transitive-dependency" {
			t.Fatalf("env dep: bad chart path: %v", d.ChartPath)
		}
	} else {
		t.Fatalf("acme-sprockets-repo-transitive missing from deps")
	}
}

func TestMetaGetterGetV1(t *testing.T) {
	d, err := ioutil.ReadFile("./testdata/acyl-v1.yml")
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	rc := &ghclient.FakeRepoClient{
		GetFileContentsFunc: func(ctx context.Context, repo string, path string, ref string) ([]byte, error) {
			return d, nil
		},
	}
	g := DataGetter{
		RC: rc,
	}
	_, err = g.Get(context.Background(), models.RepoRevisionData{})
	if err == nil {
		t.Fatalf("should have failed")
	}
}

func TestMetaGetterFetchCharts(t *testing.T) {
	rc := &ghclient.FakeRepoClient{
		GetDirectoryContentsFunc: func(ctx context.Context, repo string, path string, ref string) (map[string]ghclient.FileContents, error) {
			switch repo {
			case "foo/bar":
				switch path {
				case ".chart/foo":
					return map[string]ghclient.FileContents{
						".chart/foo/asdf.txt": ghclient.FileContents{
							Contents: []byte("foo"),
						},
					}, nil
				case ".chart2/bar":
					return map[string]ghclient.FileContents{
						".chart2/bar/qwerty.txt": ghclient.FileContents{
							Contents: []byte("bar"),
						},
					}, nil
				default:
					t.Fatalf("bad path for repo: %v: %v", repo, path)
				}
			case "foo/biz":
				if path != ".chart2/bar" {
					t.Fatalf("bad path for repo: %v: %v", repo, path)
				}
				return map[string]ghclient.FileContents{
					".chart2/bar/qwerty.txt": ghclient.FileContents{
						Contents: []byte("bar"),
					},
				}, nil
			case "acme/asdf":
				if path != ".chart2/bar" {
					t.Fatalf("bad path for repo: %v: %v", repo, path)
				}
				return map[string]ghclient.FileContents{
					".chart2/bar/qwerty.txt": ghclient.FileContents{
						Contents: []byte("bar"),
					},
				}, nil
			default:
				t.Fatalf("unknown repo: %v", repo)
			}
			return nil, nil
		},
		GetFileContentsFunc: func(ctx context.Context, repo string, path string, ref string) ([]byte, error) {
			return []byte("asdf"), nil
		},
	}
	bp := "/tmp/foo"
	mfs := memfs.New()
	mfs.MkdirAll(bp, os.ModePerm)
	g := DataGetter{
		RC: rc,
		FS: mfs,
	}
	pmd := models.RepoConfigAppMetadata{
		Repo:           "foo/bar",
		ChartPath:      ".chart/foo",
		ChartVarsPath:  ".chart/vars.yml",
		ValueOverrides: []string{"foo=bar", "bar=baz"},
	}
	rcfg := &models.RepoConfig{
		Application: pmd,
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "foo-biz",
					Repo: "foo/biz",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:           "foo/biz",
						ChartPath:      ".chart2/bar",
						ChartVarsPath:  ".chart2/vars.yml",
						ValueOverrides: []string{"asdf=1234", "qwerty=5678"},
					},
					ValueOverrides: []string{"qwerty=foo"},
				},
				models.RepoConfigDependency{
					Name:           "bar",
					ChartPath:      ".chart2/bar",
					ChartVarsPath:  ".chart2/vars.yml",
					ValueOverrides: []string{"asdf=1234", "qwerty=5678"},
					AppMetadata:    pmd,
				},
				models.RepoConfigDependency{
					Name: "acme-asdf",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:              "acme/asdf",
						Ref:               "master",
						ChartRepoPath:     "acme/asdf@master:.chart2/bar",
						ChartVarsRepoPath: "acme/asdf@master:.chart2/vars.yml",
						ValueOverrides:    []string{"asdf=1234", "qwerty=5678"},
					},
				},
			},
		},
	}
	d, err := g.FetchCharts(context.Background(), rcfg, bp)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if len(d) != 4 {
		t.Fatalf("bad length (wanted 4): %v", len(d))
	}
	if v, ok := d["foo-bar"]; !ok {
		t.Fatalf("repo foo/bar missing")
	} else {
		if v.ChartPath != bp+"/0/foo-bar" {
			t.Fatalf("bad path for foo/bar chart: %v", v)
		}
	}
	if v, ok := d["foo-biz"]; !ok {
		t.Fatalf("repo foo/biz missing")
	} else {
		if v.ChartPath != bp+"/1/foo-biz" {
			t.Fatalf("bad path for foo/biz chart: %v", v)
		}
	}
	f, err := mfs.Open(bp + "/0/foo-bar/asdf.txt")
	if err != nil {
		t.Fatalf("error opening file: %v", err)
	}
	defer f.Close()
	fd := make([]byte, 3)
	if n, err := f.Read(fd); err != nil || n != 3 {
		t.Fatalf("error reading file: %v (%v)", err, n)
	} else {
		if string(fd) != "foo" {
			t.Fatalf("bad value for file contents: %v", string(fd))
		}
	}
	f.Close()
	f, err = mfs.Open(bp + "/1/foo-biz/qwerty.txt")
	if err != nil {
		t.Fatalf("error opening file: %v", err)
	}
	fd = make([]byte, 3)
	if n, err := f.Read(fd); err != nil || n != 3 {
		t.Fatalf("error reading file: %v (%v)", err, n)
	} else {
		if string(fd) != "bar" {
			t.Fatalf("bad value for file contents: %v", string(fd))
		}
	}
	vo1 := d["foo-bar"].ValueOverrides
	if len(vo1) != 2 {
		t.Fatalf("bad length for foo/bar overrides: %v", len(vo1))
	}
	if v, ok := vo1["foo"]; ok {
		if v != "bar" {
			t.Fatalf("bad value for override foo: %v", v)
		}
	} else {
		t.Fatalf("override foo missing")
	}
	vo2 := d["foo-biz"].ValueOverrides
	if len(vo2) != 2 {
		t.Fatalf("bad length for foo/biz overrides: %v", len(vo2))
	}
	if v, ok := vo2["qwerty"]; ok {
		if v != "foo" {
			t.Fatalf("bad value for overrided override qwerty: %v", v)
		}
	} else {
		t.Fatalf("overrided override qwerty missing")
	}
}

func TestMetaGetChartLocation(t *testing.T) {
	cases := []struct {
		name, chartPath, chartRepoPath, varsPath, varsRepoPath, expectedRepo, expectedRef, expectedPath string
		isError                                                                                         bool
		errContains                                                                                     string
	}{
		{"Valid", "", "foo/bar@master:/a/b/c", ".chart/vars.yml", "", "foo/bar", "master", "/a/b/c", false, ""},
		{"Valid, no ref", "", "foo/bar:/a/b/c", ".chart/vars.yml", "", "foo/bar", "", "/a/b/c", false, ""},
		{"Valid, ChartPath supplied", "./chart", "", ".chart/vars.yml", "", "", "", "./chart", false, ""},
		{"Invalid, bad repo", "", "asdf:/a/b/c", ".chart/vars.yml", "", "", "", "", true, "malformed repo"},
		{"Invalid, bad ref", "", "asdf/foo@zzz@123:/a/b/c", ".chart/vars.yml", "", "", "", "", true, "no more than one '@'"},
		{"Invalid, bad path", "", "asdf/foo@zzz:/a/b/c:/d/e/f", ".chart/vars.yml", "", "", "", "", true, "exactly one ':'"},
		{"Invalid, both empty", "", "", "", "", "", "", "", true, "one of ChartPath or ChartRepoPath"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc := models.RepoConfigAppMetadata{
				ChartPath:         c.chartPath,
				ChartRepoPath:     c.chartRepoPath,
				ChartVarsPath:     c.varsPath,
				ChartVarsRepoPath: c.varsRepoPath,
			}
			cl, err := getChartLocation(models.RepoConfigDependency{AppMetadata: rc})
			if c.isError {
				if err == nil {
					t.Fatalf("should have failed with %v", c.errContains)
				}
				if !strings.Contains(err.Error(), c.errContains) {
					t.Fatalf("error did not contain expected text ('%v'): %v", c.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("should have succeeded: %v", err)
			}
			if c.expectedRepo != "" && cl.chart.repo != c.expectedRepo {
				t.Fatalf("repo mismatch: %v vs %v", cl.chart.repo, c.expectedRepo)
			}
			if c.expectedRef != "" && cl.chart.ref != c.expectedRef {
				t.Fatalf("ref mismatch: %v vs %v", cl.chart.ref, c.expectedRef)
			}
			if c.expectedPath != "" && cl.chart.path != c.expectedPath {
				t.Fatalf("path mismatch: %v vs %v", cl.chart.path, c.expectedPath)
			}
		})
	}
}
