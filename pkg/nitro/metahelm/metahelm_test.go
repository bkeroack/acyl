package metahelm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bkeroack/acyl/pkg/config"
	"github.com/bkeroack/acyl/pkg/eventlogger"
	"github.com/bkeroack/acyl/pkg/match"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/nitro/images"
	"github.com/bkeroack/acyl/pkg/nitro/metrics"
	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/dollarshaveclub/metahelm/pkg/metahelm"
	"github.com/pkg/errors"
	"gopkg.in/src-d/go-billy.v4/memfs"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	mtypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func chartMap(charts []metahelm.Chart) map[string]metahelm.Chart {
	out := map[string]metahelm.Chart{}
	for _, c := range charts {
		out[c.Title] = c
	}
	return out
}

// testKubeClient is a stub helm v3 internal kube client for testing purposes
type testKubeClient struct {
}

var _ kube.Interface = &testKubeClient{}

func (tkc *testKubeClient) Create(resources kube.ResourceList) (*kube.Result, error) {
	return &kube.Result{}, nil
}

func (tkc *testKubeClient) Wait(resources kube.ResourceList, timeout time.Duration) error {
	return nil
}

func (tkc *testKubeClient) WaitWithJobs(resources kube.ResourceList, timeout time.Duration) error {
	return nil
}

func (tkc *testKubeClient) Delete(resources kube.ResourceList) (*kube.Result, []error) {
	return &kube.Result{}, nil
}

func (tkc *testKubeClient) WatchUntilReady(resources kube.ResourceList, timeout time.Duration) error {
	return nil
}

func (tkc *testKubeClient) Update(original, target kube.ResourceList, force bool) (*kube.Result, error) {
	return &kube.Result{}, nil
}

func (tkc *testKubeClient) Build(reader io.Reader, validate bool) (kube.ResourceList, error) {
	return kube.ResourceList{}, nil
}

func (tkc *testKubeClient) WaitAndGetCompletedPodPhase(name string, timeout time.Duration) (v1.PodPhase, error) {
	return "", nil
}

func (tkc *testKubeClient) IsReachable() error {
	return nil
}

var _ action.RESTClientGetter = &testKubeClient{}

func (tkc *testKubeClient) ToRESTConfig() (*rest.Config, error) {
	return nil, nil
}

func (tkc *testKubeClient) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	return nil, nil
}

func (tkc *testKubeClient) ToRESTMapper() (meta.RESTMapper, error) {
	return nil, nil
}

func (tkc *testKubeClient) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return nil
}

func fakeHelmConfiguration(t *testing.T) *action.Configuration {
	t.Helper()
	ac := &action.Configuration{
		Releases:       storage.Init(driver.NewMemory()),
		KubeClient:     &testKubeClient{},
		Capabilities:   chartutil.DefaultCapabilities,
		Log: func(format string, v ...interface{}) {
			t.Helper()
			t.Logf(format, v)
		},
	}
	return ac
}

// generate mock k8s objects for the supplied charts
func gentestobjs(charts []metahelm.Chart, namespace string) []runtime.Object {
	if overrideNamespace != "" {
		namespace = overrideNamespace
	}
	objs := []runtime.Object{}
	reps := int32(1)
	iscontroller := true
	rsl := appsv1.ReplicaSetList{Items: []appsv1.ReplicaSet{}}
	for _, c := range charts {
		r := &appsv1.ReplicaSet{}
		d := &appsv1.Deployment{}
		d.Spec.Replicas = &reps
		d.Spec.Template.Labels = map[string]string{"app": c.Name()}
		d.Spec.Template.Spec.NodeSelector = map[string]string{}
		d.Spec.Template.Name = c.Name()
		d.Spec.Selector = &metav1.LabelSelector{}
		d.Spec.Selector.MatchLabels = map[string]string{"app": c.Name()}
		r.Spec.Selector = d.Spec.Selector
		r.Spec.Replicas = &reps
		r.Status.ReadyReplicas = 1
		r.Name = "replicaset-" + c.Name()
		r.Namespace = namespace
		d.Spec.Replicas = &reps
		d.Status.ReadyReplicas = 1
		r.Labels = d.Spec.Template.Labels
		d.Labels = d.Spec.Template.Labels
		d.ObjectMeta.UID = mtypes.UID(c.Name() + "-deployment")
		r.ObjectMeta.OwnerReferences = []metav1.OwnerReference{metav1.OwnerReference{UID: d.ObjectMeta.UID, Controller: &iscontroller}}
		d.Name = c.Name()
		d.Namespace = namespace
		r.Spec.Template = d.Spec.Template
		objs = append(objs, d)
		rsl.Items = append(rsl.Items, *r)
	}
	return append(objs, &rsl)
}

func TestMetahelmGenerateCharts(t *testing.T) {
	cases := []struct {
		name, inputNS, inputEnvName string
		inputRC                     models.RepoConfig
		inputCL                     ChartLocations
		isError, disableDefaults    bool
		errContains                 string
		verifyf                     func([]metahelm.Chart) error
	}{
		{
			name:         "Valid",
			inputNS:      "fake-name",
			inputEnvName: "fake-env-name",
			inputRC: models.RepoConfig{
				Application: models.RepoConfigAppMetadata{
					ChartTagValue:  "image.tag",
					Repo:           "foo/bar",
					Ref:            "aaaa",
					ValueOverrides: []string{"something=qqqq"},
				},
				Dependencies: models.DependencyDeclaration{
					Direct: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "bar-baz",
							Repo: "bar/baz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue:  "image.tag",
								Repo:           "bar/baz",
								Ref:            "bbbb",
								ValueOverrides: []string{"somethingelse=zzzz", "yetanotherthing=xxxx"},
							},
							ValueOverrides: []string{"yetanotherthing=yyyy"},
						},
					},
					Environment: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "car-buz",
							Repo: "car/buz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue:  "image.tag",
								Repo:           "car/buz",
								Ref:            "cccc",
								ValueOverrides: []string{"somethingmore=abcd", "yetanotherthingmore=xxff"},
							},
							ValueOverrides: []string{"yetanotherthingmore=yyza"},
						},
					},
				},
			},
			inputCL: ChartLocations{
				"foo-bar": ChartLocation{ChartPath: "testdata/chart"},
				"bar-baz": ChartLocation{ChartPath: "testdata/chart"},
				"car-buz": ChartLocation{ChartPath: "testdata/chart"},
			},
			verifyf: func(charts []metahelm.Chart) error {
				if len(charts) != 3 {
					return fmt.Errorf("bad chart length: %v", len(charts))
				}
				cm := chartMap(charts)
				if c, ok := cm["foo-bar"]; ok {
					if len(c.DependencyList) != 2 {
						return fmt.Errorf("bad dependencies for testdata/chart: %v", c.DependencyList)
					}
					if c.DependencyList[0] != "bar-baz" {
						return fmt.Errorf("bad dependency for testdata/chart: %v", c.DependencyList[0])
					}
					if c.DependencyList[1] != "car-buz" {
						return fmt.Errorf("bad dependency for testdata/chart: %v", c.DependencyList[0])
					}
				} else {
					return errors.New("foo-bar missing")
				}
				if c, ok := cm["bar-baz"]; ok {
					if len(c.DependencyList) != 0 {
						return fmt.Errorf("bad dependencies for bar-baz: %v", c.DependencyList)
					}
				} else {
					return errors.New("bar-baz missing")
				}
				if c, ok := cm["car-buz"]; ok {
					if len(c.DependencyList) != 0 {
						return fmt.Errorf("bad dependencies for car-buz: %v", c.DependencyList)
					}
				} else {
					return errors.New("car-buz missing")
				}
				checkOverrideString := func(overrides []byte, name, value string) error {
					om := map[string]interface{}{}
					if err := yaml.Unmarshal(overrides, &om); err != nil {
						return fmt.Errorf("error unmarshaling overrides: %w", err)
					}
					vi, ok := om[name]
					if !ok {
						return fmt.Errorf("override is missing: %v", name)
					}
					v, ok := vi.(string)
					if !ok {
						return fmt.Errorf("%v value is unexpected type: %T", name, vi)
					}
					if v != value {
						return fmt.Errorf("bad value for %v: %v", name, v)
					}
					return nil
				}
				for _, chart := range charts {
					if err := checkOverrideString(chart.ValueOverrides, models.DefaultNamespaceValue, "fake-name"); err != nil {
						return fmt.Errorf("error checking override: %w", err)
					}
				}
				chart := cm["foo-bar"]
				if err := checkOverrideString(chart.ValueOverrides, "something", "qqqq"); err != nil {
					return fmt.Errorf("error checking something override: %w", err)
				}
				chart = cm["bar-baz"]
				if err := checkOverrideString(chart.ValueOverrides, "somethingelse", "zzzz"); err != nil {
					return fmt.Errorf("error checking somethingelse override: %w", err)
				}
				if err := checkOverrideString(chart.ValueOverrides, "yetanotherthing", "yyyy"); err != nil {
					return fmt.Errorf("error checking yetanotherthing override: %w", err)
				}
				chart = cm["car-buz"]
				if err := checkOverrideString(chart.ValueOverrides, "somethingmore", "abcd"); err != nil {
					return fmt.Errorf("error checking somethingmore override: %w", err)
				}
				return nil
			},
		},
		{
			name:         "missing ref on dep",
			inputNS:      "fake-name",
			inputEnvName: "fake-env-name",
			inputRC: models.RepoConfig{
				Application: models.RepoConfigAppMetadata{
					ChartTagValue: "image.tag",
					Repo:          "foo/bar",
					Ref:           "aaaa",
				},
				Dependencies: models.DependencyDeclaration{
					Direct: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "bar-baz",
							Repo: "bar/baz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "bar/baz",
							},
						},
					},
					Environment: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "car-buz",
							Repo: "car/buz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "car/buz",
								Ref:           "cccc",
							},
						},
					},
				},
			},
			inputCL: ChartLocations{
				"foo-bar": ChartLocation{ChartPath: "testdata/chart"},
				"bar-baz": ChartLocation{ChartPath: "testdata/chart"},
				"car-buz": ChartLocation{ChartPath: "testdata/chart"},
			},
			isError:     true,
			errContains: "Ref is empty",
		},
		{
			name:         "missing name on dep",
			inputNS:      "fake-name",
			inputEnvName: "fake-env-name",
			inputRC: models.RepoConfig{
				Application: models.RepoConfigAppMetadata{
					ChartTagValue: "image.tag",
					Repo:          "foo/bar",
					Ref:           "aaaa",
				},
				Dependencies: models.DependencyDeclaration{
					Direct: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Repo: "bar/baz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "bar/baz",
								Ref:           "bbbb",
							},
						},
					},
					Environment: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "car-buz",
							Repo: "car/buz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "car/buz",
								Ref:           "cccc",
							},
						},
					},
				},
			},
			inputCL: ChartLocations{
				"foo-bar": ChartLocation{ChartPath: "testdata/chart"},
				"bar-baz": ChartLocation{ChartPath: "testdata/chart"},
				"car-buz": ChartLocation{ChartPath: "testdata/chart"},
			},
			isError:     true,
			errContains: "Name is empty",
		},
		{
			name:         "missing dep from chart locations",
			inputNS:      "fake-name",
			inputEnvName: "fake-env-name",
			inputRC: models.RepoConfig{
				Application: models.RepoConfigAppMetadata{
					ChartTagValue: "image.tag",
					Repo:          "foo/bar",
					Ref:           "aaaa",
				},
				Dependencies: models.DependencyDeclaration{
					Direct: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "bar-baz",
							Repo: "bar/baz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "bar/baz",
								Ref:           "bbbb",
							},
						},
					},
					Environment: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "car-buz",
							Repo: "car/buz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "car/buz",
								Ref:           "cccc",
							},
						},
					},
				},
			},
			inputCL: ChartLocations{
				"foo-bar": ChartLocation{ChartPath: "testdata/chart"},
				"bar-baz": ChartLocation{ChartPath: "testdata/chart"},
			},
			isError:     true,
			errContains: "dependency not found in ChartLocations",
		},
		{
			name:         "unknown requires",
			inputNS:      "fake-name",
			inputEnvName: "fake-env-name",
			inputRC: models.RepoConfig{
				Application: models.RepoConfigAppMetadata{
					ChartTagValue: "image.tag",
					Repo:          "foo/bar",
					Ref:           "aaaa",
				},
				Dependencies: models.DependencyDeclaration{
					Direct: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "bar-baz",
							Repo: "bar/baz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "bar/baz",
								Ref:           "bbbb",
							},
							Requires: []string{"doesnotexist"},
						},
					},
					Environment: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "car-buz",
							Repo: "car/buz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "car/buz",
								Ref:           "cccc",
							},
						},
					},
				},
			},
			inputCL: ChartLocations{
				"foo-bar": ChartLocation{ChartPath: "testdata/chart"},
				"bar-baz": ChartLocation{ChartPath: "testdata/chart"},
				"car-buz": ChartLocation{ChartPath: "testdata/chart"},
			},
			isError:     true,
			errContains: "unknown requires on chart",
		},
		{
			name:            "empty chart tag value",
			inputNS:         "fake-name",
			inputEnvName:    "fake-env-name",
			disableDefaults: true,
			inputRC: models.RepoConfig{
				Application: models.RepoConfigAppMetadata{
					ChartTagValue: "",
					Repo:          "foo/bar",
					Ref:           "aaaa",
				},
				Dependencies: models.DependencyDeclaration{
					Direct: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "bar-baz",
							Repo: "bar/baz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "bar/baz",
								Ref:           "bbbb",
							},
						},
					},
					Environment: []models.RepoConfigDependency{
						models.RepoConfigDependency{
							Name: "car-buz",
							Repo: "car/buz",
							AppMetadata: models.RepoConfigAppMetadata{
								ChartTagValue: "image.tag",
								Repo:          "car/buz",
								Ref:           "cccc",
							},
						},
					},
				},
			},
			inputCL: ChartLocations{
				"foo-bar": ChartLocation{ChartPath: "testdata/chart"},
				"bar-baz": ChartLocation{ChartPath: "testdata/chart"},
				"car-buz": ChartLocation{ChartPath: "testdata/chart"},
			},
			isError:     true,
			errContains: "ChartTagValue is empty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !c.disableDefaults {
				c.inputRC.Application.SetValueDefaults()
				for i := range c.inputRC.Dependencies.Direct {
					c.inputRC.Dependencies.Direct[i].AppMetadata.SetValueDefaults()
				}
				for i := range c.inputRC.Dependencies.Environment {
					c.inputRC.Dependencies.Environment[i].AppMetadata.SetValueDefaults()
				}
			}
			newenv := &EnvInfo{Env: &models.QAEnvironment{Name: c.inputEnvName}, RC: &c.inputRC}
			cl, err := ChartInstaller{mc: &metrics.FakeCollector{}}.GenerateCharts(context.Background(), c.inputNS, newenv, c.inputCL)
			if err != nil {
				if !c.isError {
					t.Fatalf("should have succeeded: %v", err)
				}
				if !strings.Contains(err.Error(), c.errContains) {
					t.Fatalf("error missing string (%v): %v", c.errContains, err)
				}
				return
			}
			if c.isError {
				t.Fatalf("should have failed")
			}
			if c.verifyf != nil {
				if err := c.verifyf(cl); err != nil {
					t.Fatalf(err.Error())
				}
			}
		})
	}
}

func TestMetahelmCreateNamespace(t *testing.T) {
	fkc := fake.NewSimpleClientset()
	dl := persistence.NewFakeDataLayer()
	ci := ChartInstaller{kc: fkc, dl: dl}
	valid := func(ns string) error {
		if !strings.HasPrefix(ns, "nitro-") {
			return errors.New("prefix missing")
		}
		if utf8.RuneCountInString(ns) > 63 {
			return errors.New("namespace name too long")
		}
		return nil
	}
	cases := []struct {
		name, env string
	}{
		{"Valid", "foo-bar"},
		{"Name too long", "sadfasdfasdfasdfasdfasdfasdfasdfasdfasdfasdfasdfasdfasdfasdfasdf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ns, err := ci.createNamespace(context.Background(), c.env)
			if err != nil {
				t.Fatalf("should have succeeded: %v", err)
			}
			if err := valid(ns); err != nil {
				t.Fatalf(err.Error())
			}
		})
	}
}

func TestMetahelmInstallCharts(t *testing.T) {
	charts := []metahelm.Chart{
		metahelm.Chart{Title: "foo", Location: "testdata/chart", DeploymentHealthIndication: metahelm.AtLeastOnePodHealthy, WaitUntilDeployment: "foo", DependencyList: []string{"bar"}},
		metahelm.Chart{Title: "bar", Location: "testdata/chart", DeploymentHealthIndication: metahelm.AtLeastOnePodHealthy, WaitUntilDeployment: "bar"},
	}
	ns := "nitro-foo"
	tobjs := gentestobjs(charts, ns)
	fkc := fake.NewSimpleClientset(tobjs...)
	ib := &images.FakeImageBuilder{BatchCompletedFunc: func(envname, repo string) (bool, error) { return true, nil }}
	rc := &models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:          "foo",
			Ref:           "aaaa",
			Branch:        "master",
			Image:         "foo",
			ChartTagValue: "image.tag",
		},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "bar",
					Repo: "bar",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:          "bar",
						Ref:           "bbbbbb",
						Branch:        "foo",
						Image:         "bar",
						ChartTagValue: "image.tag",
					},
				},
			},
		},
	}
	b, err := ib.StartBuilds(context.Background(), "foo-bar", rc)
	if err != nil {
		t.Fatalf("StartBuilds failed: %v", err)
	}
	defer b.Stop()
	nenv := &EnvInfo{
		Env: &models.QAEnvironment{Name: "foo-bar"},
		RC:  rc,
	}
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), nenv.Env)
	hcfg := fakeHelmConfiguration(t)
	ci := ChartInstaller{
		kc: fkc,
		dl: dl,
		ib: ib,
		mc: &metrics.FakeCollector{},
		mhmf: func(ctx context.Context, kc kubernetes.Interface, hccfg config.HelmClientConfig, namespace string) (*metahelm.Manager, error) {
			return &metahelm.Manager{
				K8c: fkc,
				HCfg: hcfg,
				LogF: metahelm.LogFunc(func(msg string, args ...interface{}) {
					eventlogger.GetLogger(context.Background()).Printf("metahelm-test: "+msg, args...)
				}),
			}, nil
		},
	}
	metahelm.ChartWaitPollInterval = 10 * time.Millisecond
	el := &eventlogger.Logger{DL: dl}
	el.Init([]byte{}, rc.Application.Repo, 99)
	ctx := eventlogger.NewEventLoggerContext(context.Background(), el)
	if err := ci.installOrUpgradeCharts(ctx, ns, charts, nenv, b, false); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestMetahelmInstallAndUpgradeChartsBuildError(t *testing.T) {
	charts := []metahelm.Chart{
		metahelm.Chart{Title: "foo", Location: "testdata/chart", DeploymentHealthIndication: metahelm.AtLeastOnePodHealthy, WaitUntilDeployment: "foo", DependencyList: []string{"bar"}},
		metahelm.Chart{Title: "bar", Location: "testdata/chart", DeploymentHealthIndication: metahelm.AtLeastOnePodHealthy, WaitUntilDeployment: "bar"},
	}
	ns := "nitro-foo"
	tobjs := gentestobjs(charts, ns)
	fkc := fake.NewSimpleClientset(tobjs...)
	berr := errors.New("build error")
	ib := &images.FakeImageBuilder{
		BatchCompletedFunc: func(envname, repo string) (bool, error) {
			return true, berr
		},
	}
	rc := &models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:          "foo",
			Ref:           "aaaa",
			Branch:        "master",
			Image:         "foo",
			ChartTagValue: "image.tag",
		},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "bar",
					Repo: "bar",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:          "bar",
						Ref:           "bbbbbb",
						Branch:        "foo",
						Image:         "bar",
						ChartTagValue: "image.tag",
					},
				},
			},
		},
	}
	b, err := ib.StartBuilds(context.Background(), "foo-bar", rc)
	if err != nil {
		t.Fatalf("StartBuilds failed: %v", err)
	}
	defer b.Stop()
	nenv := &EnvInfo{
		Env: &models.QAEnvironment{Name: "foo-bar"},
		RC:  rc,
	}
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), nenv.Env)
	hcfg := fakeHelmConfiguration(t)
	ci := ChartInstaller{
		kc:  fkc,
		dl:  dl,
		ib:  ib,
		mc:  &metrics.FakeCollector{},
		mhmf: func(ctx context.Context, kc kubernetes.Interface, hccfg config.HelmClientConfig, namespace string) (*metahelm.Manager, error) {
			return &metahelm.Manager{
				K8c: fkc,
				HCfg: hcfg,
				LogF: metahelm.LogFunc(func(msg string, args ...interface{}) {
					eventlogger.GetLogger(context.Background()).Printf("metahelm-test: "+msg, args...)
				}),
			}, nil
		},
	}
	metahelm.ChartWaitPollInterval = 10 * time.Millisecond
	err = ci.installOrUpgradeCharts(context.Background(), ns, charts, nenv, b, false)
	if err == nil {
		t.Fatalf("install should have failed")
	}
	if err != berr {
		t.Fatalf("install did not return build error: %v", err)
	}
	b2, err := ib.StartBuilds(context.Background(), "foo-bar", rc)
	if err != nil {
		t.Fatalf("StartBuilds failed: %v", err)
	}
	defer b2.Stop()
	nenv.Releases = map[string]string{"foo": "foo", "bar": "bar"}
	ci = ChartInstaller{
		kc:  fkc,
		dl:  dl,
		ib:  ib,
		mc:  &metrics.FakeCollector{},
		mhmf: func(ctx context.Context, kc kubernetes.Interface, hccfg config.HelmClientConfig, namespace string) (*metahelm.Manager, error) {
			return &metahelm.Manager{
				K8c: fkc,
				HCfg: hcfg,
				LogF: metahelm.LogFunc(func(msg string, args ...interface{}) {
					eventlogger.GetLogger(context.Background()).Printf("metahelm-test: "+msg, args...)
				}),
			}, nil
		},
	}
	metahelm.ChartWaitPollInterval = 10 * time.Millisecond
	err = ci.installOrUpgradeCharts(context.Background(), ns, charts, nenv, b2, true)
	if err == nil {
		t.Fatalf("upgrade should have failed")
	}
	if err != berr {
		t.Fatalf("upgrade did not return build error: %v", err)
	}
}

func TestMetahelmWriteReleaseNames(t *testing.T) {
	rmap := map[string]string{
		"foo-bar":  "random",
		"foo-bar2": "random2",
	}
	rc := models.RepoConfig{
		Application: models.RepoConfigAppMetadata{Repo: "foo/bar", Ref: "asdf", Branch: "random"},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "foo-bar2",
					AppMetadata: models.RepoConfigAppMetadata{
						Ref:    "1234",
						Branch: "random2",
					},
				},
			},
		},
	}
	name := "foo-bar"
	newenv := &EnvInfo{Env: &models.QAEnvironment{Name: name}, RC: &rc}
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), newenv.Env)
	ci := ChartInstaller{dl: dl}
	ns := "fake-namespace"
	if err := ci.writeReleaseNames(context.Background(), rmap, ns, newenv); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	releases, err := dl.GetHelmReleasesForEnv(context.Background(), name)
	if err != nil {
		t.Fatalf("get should have succeeded: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("bad length: %v", len(releases))
	}
	for i, r := range releases {
		if r.K8sNamespace != ns {
			t.Fatalf("bad namespace at offset %v: %v", i, r.K8sNamespace)
		}
	}
	// test writing with existing releases
	if err := ci.writeReleaseNames(context.Background(), rmap, "fake-namespace", newenv); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestMetahelmUpdateReleaseRevisions(t *testing.T) {
	rmap := map[string]string{
		"foo-bar":  "random",
		"foo-bar2": "random2",
		"foo-bar3": "random3",
	}
	rc := models.RepoConfig{
		Application: models.RepoConfigAppMetadata{Repo: "foo/bar", Ref: "1234", Branch: "random"},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "foo-bar2",
					AppMetadata: models.RepoConfigAppMetadata{
						Ref:    "1234",
						Branch: "random2",
					},
				},
			},
			Environment: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "foo-bar3",
					AppMetadata: models.RepoConfigAppMetadata{
						Ref:    "1234",
						Branch: "random3",
					},
				},
			},
		},
	}
	name := "foo-bar"
	env := &EnvInfo{Env: &models.QAEnvironment{Name: name}, Releases: rmap, RC: &rc}
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), env.Env)
	releases := []models.HelmRelease{
		models.HelmRelease{EnvName: name, Release: "random", RevisionSHA: "9999"},
		models.HelmRelease{EnvName: name, Release: "random2", RevisionSHA: "9999"},
		models.HelmRelease{EnvName: name, Release: "random3", RevisionSHA: "9999"},
	}
	dl.CreateHelmReleasesForEnv(context.Background(), releases)
	ci := ChartInstaller{dl: dl}
	if err := ci.updateReleaseRevisions(context.Background(), env); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	releases, err := dl.GetHelmReleasesForEnv(context.Background(), name)
	if err != nil {
		t.Fatalf("get should have succeeded: %v", err)
	}
	if len(releases) != 3 {
		t.Fatalf("bad length: %v", len(releases))
	}
	for _, r := range releases {
		if r.RevisionSHA != "1234" {
			t.Fatalf("bad SHA: %v", r.RevisionSHA)
		}
	}
}

func TestMetahelmWriteK8sEnvironment(t *testing.T) {
	name := "foo-bar"
	rc := &models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:   "foo/bar",
			Ref:    "aaaa",
			Branch: "bar",
		},
	}
	newenv := &EnvInfo{Env: &models.QAEnvironment{Name: name}, RC: rc}
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), newenv.Env)
	ci := ChartInstaller{dl: dl}
	ns := "nitro-foo"
	if err := ci.writeK8sEnvironment(context.Background(), newenv, ns); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	k8s, err := dl.GetK8sEnv(context.Background(), name)
	if err != nil {
		t.Fatalf("get should have succeeded: %v", err)
	}
	refmap2 := map[string]string{}
	if err := json.Unmarshal([]byte(k8s.RefMapJSON), &refmap2); err != nil {
		t.Fatalf("json umarshal failed: %v", err)
	}
	rm, _ := newenv.RC.RefMap()
	if refmap2["foo/bar"] != rm["foo/bar"] {
		t.Fatalf("bad refmap value: %v", refmap2["foo/bar"])
	}
	if k8s.Namespace != ns {
		t.Fatalf("bad namespace: %v", k8s.Namespace)
	}
	// test writing an existing record
	fkc := fake.NewSimpleClientset(&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	ci.kc = fkc
	if err := ci.writeK8sEnvironment(context.Background(), newenv, "foo"); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestMetahelmMergeVars(t *testing.T) {
	cases := []struct {
		name, inputYAML string
		inputOverrides  map[string]string
		output          []byte
		isError         bool
		errContains     string
	}{
		{
			"empty", "", map[string]string{"image": "foo"}, []byte("image: foo\n"), false, "",
		},
		{
			"simple", "image: foo\n", map[string]string{"image": "bar"}, []byte("image: bar\n"), false, "",
		},
		{
			"adding", "image: foo\n", map[string]string{"image": "bar", "something": "else"}, []byte("image: bar\nsomething: else\n"), false, "",
		},
		{
			"array item replacement", "array:\n - asdf\n - qwerty\n", map[string]string{"array[1]": "bar"}, []byte("array:\n- asdf\n- bar\n"), false, "",
		},
		{
			"nested", "image:\n tag: foo\n", map[string]string{"image.tag": "bar"}, []byte("image:\n  tag: bar\n"), false, "",
		},
		{
			"nested array item replacement", "image:\n stuff:\n  - asdf\n", map[string]string{"image.stuff[0]": "1234"}, []byte("image:\n  stuff:\n  - 1234\n"), false, "",
		},
		{
			"integer", "count: 1\n", map[string]string{"count": "75"}, []byte("count: 75\n"), false, "",
		},
		{
			"boolean", "enabled: true\n", map[string]string{"enabled": "false"}, []byte("enabled: false\n"), false, "",
		},
	}

	cl := ChartLocation{
		VarFilePath: "foo.yml",
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := memfs.New()
			f, _ := fs.Create(cl.VarFilePath)
			f.Write([]byte(c.inputYAML))
			f.Close()
			out, err := cl.MergeVars(fs, c.inputOverrides)
			if err != nil {
				if c.isError {
					if !strings.Contains(err.Error(), c.errContains) {
						t.Fatalf("error does not contain expected string (%v): %v", c.errContains, err)
					}
				} else {
					t.Fatalf("should have succeeded: %v", err)
				}
			}
			if !bytes.Equal(out, c.output) {
				fmt.Printf("out: %v\n", string(out))
				fmt.Printf("wanted: %v\n", string(c.output))
				t.Fatalf("bad output: %v; expected: %v", out, c.output)
			}
		})
	}
}

func TestMetahelmBuildAndInstallCharts(t *testing.T) {
	cl := ChartLocations{
		"foo": ChartLocation{ChartPath: "testdata/chart"},
		"bar": ChartLocation{ChartPath: "testdata/chart"},
	}
	charts := []metahelm.Chart{
		metahelm.Chart{Title: "foo", Location: "testdata/chart", DeploymentHealthIndication: metahelm.AtLeastOnePodHealthy, WaitUntilDeployment: "foo", DependencyList: []string{"bar"}},
		metahelm.Chart{Title: "bar", Location: "testdata/chart", DeploymentHealthIndication: metahelm.AtLeastOnePodHealthy, WaitUntilDeployment: "bar"},
	}
	ns := "nitro-foo"
	tobjs := gentestobjs(charts, ns)
	fkc := fake.NewSimpleClientset(tobjs...)
	ib := &images.FakeImageBuilder{BatchCompletedFunc: func(envname, repo string) (bool, error) { return true, nil }}
	rc := &models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:          "foo",
			Ref:           "asdf",
			Branch:        "feature-foo",
			Image:         "foo",
			ChartTagValue: "image.tag",
		},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "bar",
					Repo: "bar",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:          "bar",
						Ref:           "asdf",
						Branch:        "feature-foo",
						Image:         "bar",
						ChartTagValue: "image.tag",
					},
				},
			},
		},
	}
	nenv := &EnvInfo{
		Env: &models.QAEnvironment{Name: "foo-bar"},
		RC:  rc,
	}
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), nenv.Env)
	hcfg := fakeHelmConfiguration(t)
	ci := ChartInstaller{
		kc:  fkc,
		dl:  dl,
		ib:  ib,
		mc:  &metrics.FakeCollector{},
		mhmf: func(ctx context.Context, kc kubernetes.Interface, hccfg config.HelmClientConfig, namespace string) (*metahelm.Manager, error) {
			return &metahelm.Manager{
				K8c: fkc,
				HCfg: hcfg,
				LogF: metahelm.LogFunc(func(msg string, args ...interface{}) {
					eventlogger.GetLogger(context.Background()).Printf("metahelm-test: "+msg, args...)
				}),
			}, nil
		},
	}
	metahelm.ChartWaitPollInterval = 10 * time.Millisecond
	overrideNamespace = "nitro-foo-bar"
	defer func() { overrideNamespace = "" }()
	if err := ci.BuildAndInstallCharts(context.Background(), nenv, cl); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestMetahelmBuildAndInstallThenUpgradeCharts(t *testing.T) {
	ns := "nitro-foo-bar"
	overrideNamespace = ns
	defer func() { overrideNamespace = "" }()
	cl := ChartLocations{
		"foo": ChartLocation{ChartPath: "testdata/chart"},
		"bar": ChartLocation{ChartPath: "testdata/chart"},
		"cuz": ChartLocation{ChartPath: "testdata/chart"},
	}
	charts := []metahelm.Chart{
		metahelm.Chart{Title: "foo", Location: "testdata/chart", DeploymentHealthIndication: metahelm.AtLeastOnePodHealthy, WaitUntilDeployment: "foo", DependencyList: []string{"bar", "cuz"}},
		metahelm.Chart{Title: "bar", Location: "testdata/chart", DeploymentHealthIndication: metahelm.AtLeastOnePodHealthy, WaitUntilDeployment: "bar"},
		metahelm.Chart{Title: "cuz", Location: "testdata/chart", DeploymentHealthIndication: metahelm.AtLeastOnePodHealthy, WaitUntilDeployment: "cuz"},
	}
	rm := match.RefMap{"foo": match.BranchInfo{Name: "master", SHA: "aaaa"}, "bar": match.BranchInfo{Name: "bar", SHA: "bbbbbb"}, "cuz": match.BranchInfo{Name: "cuz", SHA: "ccccccc"}}
	rc := &models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:          "foo",
			Ref:           rm["foo"].SHA,
			Branch:        "feature-foo",
			Image:         "foo",
			ChartTagValue: "image.tag",
		},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "bar",
					Repo: "bar",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:          "bar",
						Ref:           rm["bar"].SHA,
						Branch:        "feature-foo",
						Image:         "bar",
						ChartTagValue: "image.tag",
					},
				},
			},
			Environment: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "cuz",
					Repo: "cuz",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:          "cuz",
						Ref:           rm["cuz"].SHA,
						Branch:        "feature-foo",
						Image:         "cuz",
						ChartTagValue: "image.tag",
					},
				},
			},
		},
	}
	nenv := &EnvInfo{
		Env: &models.QAEnvironment{Name: "foo-bar"},
		RC:  rc,
		Releases: map[string]string{
			"foo": "foo",
			"bar": "bar",
			"cuz": "cuz",
		},
	}
	tobjs := gentestobjs(charts, ns)
	fkc := fake.NewSimpleClientset(tobjs...)
	ib := &images.FakeImageBuilder{BatchCompletedFunc: func(envname, repo string) (bool, error) { return true, nil }}
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), nenv.Env)
	hcfg := fakeHelmConfiguration(t)
	ci := ChartInstaller{
		kc:  fkc,
		dl:  dl,
		ib:  ib,
		mc:  &metrics.FakeCollector{},
		mhmf: func(ctx context.Context, kc kubernetes.Interface, hccfg config.HelmClientConfig, namespace string) (*metahelm.Manager, error) {
			return &metahelm.Manager{
				K8c: fkc,
				HCfg: hcfg,
				LogF: metahelm.LogFunc(func(msg string, args ...interface{}) {
					eventlogger.GetLogger(context.Background()).Printf("metahelm-test: "+msg, args...)
				}),
			}, nil
		},
	}
	metahelm.ChartWaitPollInterval = 10 * time.Millisecond
	t.Logf("running BuildAndInstallCharts...")
	if err := ci.BuildAndInstallCharts(context.Background(), nenv, cl); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	k8senv, err := dl.GetK8sEnv(context.Background(), nenv.Env.Name)
	if err != nil {
		t.Fatalf("get k8s env should have succeeded: %v", err)
	}
	t.Logf("running BuildAndUpgradeCharts...")
	if err := ci.BuildAndUpgradeCharts(context.Background(), nenv, k8senv, cl); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	releases, err := dl.GetHelmReleasesForEnv(context.Background(), nenv.Env.Name)
	if err != nil {
		t.Fatalf("get helm releases should have succeeded: %v", err)
	}
	if len(releases) != 3 {
		t.Fatalf("bad release count: %v", len(releases))
	}
	for _, r := range releases {
		if r.Name != "foo" && r.Name != "bar" && r.Name != "cuz" {
			t.Fatalf("bad release name: %v", r.Name)
		}
		for _, n := range []string{"foo", "bar", "cuz"} {
			if r.Name == n {
				if r.RevisionSHA != rm[n].SHA {
					t.Fatalf("bad revision for %v release: %v", n, r.RevisionSHA)
				}
			}
		}
	}
}

func TestMetahelmDeleteNamespace(t *testing.T) {
	ns := "nitro-foo"
	nenv := &EnvInfo{
		Env: &models.QAEnvironment{Name: "foo-bar"},
	}
	k8senv := &models.KubernetesEnvironment{
		EnvName:   nenv.Env.Name,
		Namespace: ns,
	}
	dl := persistence.NewFakeDataLayer()
	dl.CreateQAEnvironment(context.Background(), nenv.Env)
	dl.CreateK8sEnv(context.Background(), k8senv)
	fkc := fake.NewSimpleClientset(&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	ci := ChartInstaller{kc: fkc, dl: dl}
	if err := ci.DeleteNamespace(context.Background(), k8senv); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	ke, err := dl.GetK8sEnv(context.Background(), nenv.Env.Name)
	if err != nil {
		t.Fatalf("get k8s env should have succeeded: %v", err)
	}
	if ke != nil {
		t.Fatalf("get k8s env should have returned nothing: %v", ke)
	}
}

type fakeSecretFetcher struct{}

func (fsf *fakeSecretFetcher) Get(id string) ([]byte, error) { return []byte{}, nil }

func TestMetahelmSetupNamespace(t *testing.T) {
	ns := "nitro-foo"
	dl := persistence.NewFakeDataLayer()
	fkc := fake.NewSimpleClientset(&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	k8scfg := config.K8sConfig{}
	k8scfg.ProcessGroupBindings("foo=edit")
	k8scfg.ProcessPrivilegedRepos("testdata/chart")
	k8scfg.ProcessSecretInjections(&fakeSecretFetcher{}, "mysecret=some/vault/path")
	ci := ChartInstaller{kc: fkc, dl: dl, k8sgroupbindings: k8scfg.GroupBindings, k8srepowhitelist: k8scfg.PrivilegedRepoWhitelist, k8ssecretinjs: k8scfg.SecretInjections}
	if err := ci.setupNamespace(context.Background(), "some-name", "testdata/chart", ns); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestMetahelmCleanup(t *testing.T) {
	maxAge := 1 * time.Hour
	expires := time.Now().UTC().Add(-(maxAge + (72 * time.Hour)))
	orphanedNamespaces := []*v1.Namespace{
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "foo",
				CreationTimestamp: metav1.NewTime(expires),
				Labels: map[string]string{
					objLabelKey: objLabelValue,
				},
			},
		},
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "bar",
				CreationTimestamp: metav1.NewTime(expires),
				Labels: map[string]string{
					objLabelKey: objLabelValue,
				},
			},
		},
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "uninvolved",
				CreationTimestamp: metav1.NewTime(expires),
			},
		},
	}
	orphanedCRBs := []*rbacv1.ClusterRoleBinding{
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "foo",
				CreationTimestamp: metav1.NewTime(expires),
				Labels: map[string]string{
					objLabelKey: objLabelValue,
				},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "bar",
				CreationTimestamp: metav1.NewTime(expires),
				Labels: map[string]string{
					objLabelKey: objLabelValue,
				},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "uninvolved",
				CreationTimestamp: metav1.NewTime(expires),
			},
		},
	}
	var objs []runtime.Object
	for _, ns := range orphanedNamespaces {
		objs = append(objs, ns)
	}
	for _, crb := range orphanedCRBs {
		objs = append(objs, crb)
	}
	fkc := fake.NewSimpleClientset(objs...)
	dl := persistence.NewFakeDataLayer()
	ci := ChartInstaller{
		kc: fkc,
		dl: dl,
	}
	ci.Cleanup(context.Background(), maxAge)
	if _, err := fkc.CoreV1().Namespaces().Get(context.Background(),"foo", metav1.GetOptions{}); err == nil {
		t.Fatalf("should have failed to find namespace foo")
	}
	if _, err := fkc.CoreV1().Namespaces().Get(context.Background(),"bar", metav1.GetOptions{}); err == nil {
		t.Fatalf("should have failed to find namespace bar")
	}
	if _, err := fkc.CoreV1().Namespaces().Get(context.Background(),"uninvolved", metav1.GetOptions{}); err != nil {
		t.Fatalf("should have found namespace uninvolved: %v", err)
	}
	if _, err := fkc.RbacV1().ClusterRoleBindings().Get(context.Background(),"foo", metav1.GetOptions{}); err == nil {
		t.Fatalf("should have failed to find CRB foo")
	}
	if _, err := fkc.RbacV1().ClusterRoleBindings().Get(context.Background(),"bar", metav1.GetOptions{}); err == nil {
		t.Fatalf("should have failed to find CRB bar")
	}
	if _, err := fkc.RbacV1().ClusterRoleBindings().Get(context.Background(),"uninvolved", metav1.GetOptions{}); err != nil {
		t.Fatalf("should have found CRB uninvolved: %v", err)
	}
}

func TestTruncateLongDQAName(t *testing.T) {
	testCases := []struct {
		input          string
		expectedOutput string
	}{
		{
			input:          "amino-qa-80093-anosmic-basal-body-temperature-method-of-family-planning",
			expectedOutput: "amino-qa-80093-anosmic-basal-body-temperature-method-of-family",
		},
		{
			input:          "small-input",
			expectedOutput: "small-input",
		},
		{
			input:          "amino-qa-80093-anosmic-basal-body-temperature-method-of-familyȨȨȨȨȨ-planning",
			expectedOutput: "amino-qa-80093-anosmic-basal-body-temperature-method-of-family",
		},
	}

	for _, test := range testCases {
		output := truncateToDNS1123Label(test.input)
		if output != test.expectedOutput {
			t.Fatalf("string was truncated incorrectly (%v)", test.input)
		}
	}
}

func TestMetahelmGetK8sEnvPodList(t *testing.T) {
	ci := FakeKubernetesReporter{}
	pl, err := ci.GetPodList(context.Background(), "foo")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if len(pl) != 2 {
		t.Fatalf("expected 2, got %v", len(pl))
	}
	if pl[0].Ready != "2/2" && pl[0].Status != "Running" {
		t.Fatalf("expected Ready: 2/2 & Status: Running, got %v, %v", pl[0].Ready, pl[0].Status)
	}
	if pl[1].Ready != "1/2" && pl[1].Status != "Pending" {
		t.Fatalf("expected Ready: 1/2 & Status: Running, got %v, %v", pl[1].Ready, pl[1].Status)
	}
}

func TestMetahelmGetK8sEnvPodContainers(t *testing.T) {
	ci := FakeKubernetesReporter{}
	pc, err := ci.GetPodContainers(context.Background(), "foo", "foo-app-abc123")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if len(pc.Containers) != 2 {
		t.Fatalf("expected 2, got %v", len(pc.Containers))
	}
	pc, err = ci.GetPodContainers(context.Background(), "foo", "bar-app-abc123")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if len(pc.Containers) != 2 {
		t.Fatalf("expected 2, got %v", len(pc.Containers))
	}
	pc, err = ci.GetPodContainers(context.Background(), "foo", "baz-app-abc123")
	if err == nil {
		t.Fatalf("should have failed: %v", err)
	}
}

func TestMetahelmGetK8sEnvPodLogs(t *testing.T) {
	ci := FakeKubernetesReporter{
		FakePodLogFilePath: "testdata/pod_logs.log",
	}
	nLogLines := MaxPodContainerLogLines + 1
	_, err := ci.GetPodLogs(context.Background(), "foo", "foo-app-abc123", "", uint(nLogLines))
	if err != nil {
		t.Fatalf("should have failed: %v", err)
	}
	nLogLines--
	pl, err := ci.GetPodLogs(context.Background(), "foo", "foo-app-abc123", "", uint(nLogLines))
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	defer pl.Close()
	buf := make([]byte, 68*1024)
	_, err = pl.Read(buf)
	if err != nil {
		t.Fatalf("error reading pod logs: %v", err)
	}
	lineCount := bytes.Count(buf, []byte{'\n'})
	if lineCount > nLogLines {
		t.Fatalf("error lines returned exceeded expected %v, actual %v", nLogLines, lineCount)
	}
}
