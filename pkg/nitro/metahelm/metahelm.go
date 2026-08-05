package metahelm

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	kubernetestrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/k8s.io/client-go/kubernetes"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/strvals"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/restmapper"

	"github.com/bkeroack/acyl/pkg/config"
	"github.com/bkeroack/acyl/pkg/eventlogger"
	"github.com/bkeroack/acyl/pkg/models"
	nitroerrors "github.com/bkeroack/acyl/pkg/nitro/errors"
	"github.com/bkeroack/acyl/pkg/nitro/images"
	"github.com/bkeroack/acyl/pkg/nitro/metrics"
	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/dollarshaveclub/metahelm/pkg/metahelm"
	"github.com/ghodss/yaml"
	"github.com/pkg/errors"
	"gopkg.in/src-d/go-billy.v4"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/transport"

	// this is to include all auth plugins
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// LogFunc is a function that logs a formatted string somewhere
type LogFunc func(string, ...interface{})

// EnvInfo models all the data required to create a new environment or upgrade an existing one
type EnvInfo struct {
	Env      *models.QAEnvironment
	RC       *models.RepoConfig
	Releases map[string]string // map of repo to release name
}

// Installer describes an object that installs Helm charts and manages image builds
type Installer interface {
	BuildAndInstallCharts(ctx context.Context, newenv *EnvInfo, cl ChartLocations) error
	BuildAndInstallChartsIntoExisting(ctx context.Context, newenv *EnvInfo, k8senv *models.KubernetesEnvironment, cl ChartLocations) error
	BuildAndUpgradeCharts(ctx context.Context, env *EnvInfo, k8senv *models.KubernetesEnvironment, cl ChartLocations) error
	DeleteNamespace(ctx context.Context, k8senv *models.KubernetesEnvironment) error
}

// KubernetesReporter describes an object that returns k8s environment data
type KubernetesReporter interface {
	GetPodList(ctx context.Context, ns string) (out []K8sPod, err error)
	GetPodContainers(ctx context.Context, ns, podname string) (out K8sPodContainers, err error)
	GetPodLogs(ctx context.Context, ns, podname, container string, lines uint) (out io.ReadCloser, err error)
}

// metrics prefix
var mpfx = "metahelm."

type K8sClientFactoryFunc func(kubecfgpath, kubectx string) (*kubernetes.Clientset, *rest.Config, error)
type MetahelmManagerFactoryFunc func(ctx context.Context, kc kubernetes.Interface, hccfg config.HelmClientConfig, namespace string) (*metahelm.Manager, error)

// Defaults configuration options, if not specified otherwise
const (
	DefaultHelmDriver       = "secrets"
	MaxPodContainerLogLines = 1000
	DefaultRestConfigQPS    = 100000
	DefaultRestConfigBurst  = 100000
)

// ChartInstaller is an object that manages namespaces and install/upgrades/deletes metahelm chart graphs
type ChartInstaller struct {
	ib               images.Builder
	kc               kubernetes.Interface
	rcfg             *rest.Config
	kcf              K8sClientFactoryFunc
	dl               persistence.DataLayer
	fs               billy.Filesystem
	mc               metrics.Collector
	k8sgroupbindings map[string]string
	k8srepowhitelist []string
	k8ssecretinjs    map[string]config.K8sSecret
	mhmf             MetahelmManagerFactoryFunc
	hccfg            config.HelmClientConfig
}

var _ Installer = &ChartInstaller{}

// NewChartInstaller returns a ChartInstaller configured with an in-cluster K8s clientset
func NewChartInstaller(ib images.Builder, dl persistence.DataLayer, fs billy.Filesystem, mc metrics.Collector, k8sGroupBindings map[string]string, k8sRepoWhitelist []string, k8sSecretInjs map[string]config.K8sSecret, k8sJWTPath string, enableK8sTracing bool, hccfg config.HelmClientConfig) (*ChartInstaller, error) {
	kc, rcfg, err := NewInClusterK8sClientset(k8sJWTPath, enableK8sTracing)
	if err != nil {
		return nil, fmt.Errorf("error getting k8s client: %w", err)
	}
	return &ChartInstaller{
		ib:               ib,
		kc:               kc,
		rcfg:             rcfg,
		dl:               dl,
		fs:               fs,
		mc:               mc,
		k8sgroupbindings: k8sGroupBindings,
		k8srepowhitelist: k8sRepoWhitelist,
		k8ssecretinjs:    k8sSecretInjs,
		mhmf:             NewInClusterHelmConfiguration,
		hccfg:            hccfg,
	}, nil
}

// NewChartInstallerWithClientsetFromContext returns a ChartInstaller configured with a K8s clientset from the current kubeconfig context
func NewChartInstallerWithClientsetFromContext(ib images.Builder, dl persistence.DataLayer, fs billy.Filesystem, mc metrics.Collector, k8sGroupBindings map[string]string, k8sRepoWhitelist []string, k8sSecretInjs map[string]config.K8sSecret, kubeconfigpath string, hccfg config.HelmClientConfig) (*ChartInstaller, error) {
	kc, rcfg, err := NewKubecfgContextK8sClientset(kubeconfigpath, hccfg.KubeContext)
	if err != nil {
		return nil, fmt.Errorf("error getting k8s client: %w", err)
	}
	return &ChartInstaller{
		ib:               ib,
		kc:               kc,
		rcfg:             rcfg,
		dl:               dl,
		fs:               fs,
		mc:               mc,
		k8sgroupbindings: k8sGroupBindings,
		k8srepowhitelist: k8sRepoWhitelist,
		k8ssecretinjs:    k8sSecretInjs,
		mhmf:             NewInClusterHelmConfiguration,
		hccfg:            hccfg,
	}, nil
}

func NewKubecfgContextK8sClientset(kubecfgpath, kubectx string) (*kubernetes.Clientset, *rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.DefaultClientConfig = &clientcmd.DefaultClientConfig
	overrides := &clientcmd.ConfigOverrides{ClusterDefaults: clientcmd.ClusterDefaults}
	if kubectx != "" {
		overrides.CurrentContext = kubectx
	}

	if kubecfgpath != "" {
		rules.ExplicitPath = kubecfgpath
	}
	kcfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	rcfg, err := kcfg.ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting rest config: %w", err)
	}
	kc, err := kubernetes.NewForConfig(rcfg)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting k8s clientset: %w", err)
	}
	return kc, rcfg, nil
}

func NewInClusterK8sClientset(k8sJWTPath string, enableK8sTracing bool) (*kubernetes.Clientset, *rest.Config, error) {
	kcfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting k8s in-cluster config: %w", err)
	}

	kcfg.WrapTransport = wrapTransport(k8sJWTPath, enableK8sTracing)
	kc, err := kubernetes.NewForConfig(kcfg)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting k8s clientset: %w", err)
	}
	return kc, kcfg, nil
}

// wrapTransport encapsulates the kubernetestrace WrapTransport and Kubernetes'
// default TokenSource WrapTransport.
func wrapTransport(k8sJWTPath string, enableK8sTracing bool) func(rt http.RoundTripper) http.RoundTripper {
	ts := transport.NewCachedFileTokenSource(k8sJWTPath)
	tokenWrappedTransport := transport.TokenSourceWrapTransport(ts)
	if enableK8sTracing {
		return func(rt http.RoundTripper) http.RoundTripper {
			return kubernetestrace.WrapRoundTripper(tokenWrappedTransport(rt))
		}
	}
	return func(rt http.RoundTripper) http.RoundTripper {
		return tokenWrappedTransport(rt)
	}
}

func (g *restClientGetter) ToRESTConfig() (*rest.Config, error) {
	return g.restConfig, nil
}

func (g *restClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	return g.discoveryClient, nil
}

func (g *restClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	return g.restMapper, nil
}

func (g *restClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return g.rawKubeConfigLoader
}

type cachedDiscoveryInterface struct {
	discovery.DiscoveryInterface
}

var _ discovery.CachedDiscoveryInterface = &cachedDiscoveryInterface{}

func (d *cachedDiscoveryInterface) Fresh() bool {
	return false
}

func (d *cachedDiscoveryInterface) Invalidate() {}

type restClientGetter struct {
	restConfig          *rest.Config
	discoveryClient     discovery.CachedDiscoveryInterface
	restMapper          meta.RESTMapper
	rawKubeConfigLoader clientcmd.ClientConfig
}

var _ genericclioptions.RESTClientGetter = &restClientGetter{}

func newRestClientGetter(namespace, kctx string) (*restClientGetter, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.DefaultClientConfig = &clientcmd.DefaultClientConfig
	overrides := &clientcmd.ConfigOverrides{ClusterDefaults: clientcmd.ClusterDefaults}
	if kctx != "" {
		overrides.CurrentContext = kctx
	}
	if namespace != "" {
		overrides.Context.Namespace = namespace
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("could not get Kubernetes config for context %q: %s", kctx, err)
	}
	restConfig.QPS = DefaultRestConfigQPS
	restConfig.Burst = DefaultRestConfigBurst
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("could not get Kubernetes client: %w", err)
	}
	discoveryClient := &cachedDiscoveryInterface{clientset.DiscoveryClient}
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
	return &restClientGetter{
		restConfig:          restConfig,
		discoveryClient:     discoveryClient,
		restMapper:          restMapper,
		rawKubeConfigLoader: clientConfig,
	}, nil
}

// NewInClusterHelmConfiguration is a HelmClientConfigurationFunc that returns a Helm v3 client configured for use within the k8s cluster
func NewInClusterHelmConfiguration(ctx context.Context, kc kubernetes.Interface, hccfg config.HelmClientConfig, namespace string) (*metahelm.Manager, error) {
	if hccfg.HelmDriver == "" {
		hccfg.HelmDriver = DefaultHelmDriver
	}
	getter, err := newRestClientGetter(namespace, hccfg.KubeContext)
	if err != nil {
		return nil, fmt.Errorf("error getting kube client: %w", err)
	}
	logf := func(msg string, args ...interface{}) {
		eventlogger.GetLogger(ctx).Printf("metahelm: helm v3: "+msg, args...)
	}
	cfg := &action.Configuration{
		Log: logf,
	}
	if err := cfg.Init(getter, namespace, hccfg.HelmDriver, logf); err != nil {
		return nil, fmt.Errorf("error initializing Helm config: %w", err)
	}
	return &metahelm.Manager{
		K8c:  kc,
		HCfg: cfg,
		LogF: func(msg string, args ...interface{}) {
			eventlogger.GetLogger(ctx).Printf("metahelm: "+msg, args...)
		},
	}, nil
}

func (ci ChartInstaller) log(ctx context.Context, msg string, args ...interface{}) {
	eventlogger.GetLogger(ctx).Printf(msg, args...)
}

// ChartLocation models the local filesystem path for the chart and the associated vars file
type ChartLocation struct {
	ChartPath, VarFilePath string
}

// mergeVars merges overrides with the variables defined in the file at VarFilePath and returns the merged YAML stream
func (cl *ChartLocation) MergeVars(fs billy.Filesystem, overrides map[string]string) ([]byte, error) {
	base := map[string]interface{}{}
	if cl.VarFilePath != "" {
		d, err := readFileSafely(fs, cl.VarFilePath)
		if err != nil {
			return nil, fmt.Errorf("error reading vars file: %w", err)
		}
		if err := yaml.Unmarshal(d, &base); err != nil {
			return nil, fmt.Errorf("error parsing vars file: %w", nitroerrors.User(err))
		}
		if base == nil {
			base = map[string]interface{}{}
		}
	}
	for k, v := range overrides {
		if err := strvals.ParseInto(fmt.Sprintf("%v=%v", k, v), base); err != nil {
			return nil, fmt.Errorf("error parsing override: %v=%v: %w", k, v, nitroerrors.User(err))
		}
	}
	return yaml.Marshal(base)
}

// ChartLocations is a map of repo name to ChartLocation
type ChartLocations map[string]ChartLocation

func (ci ChartInstaller) BuildAndUpgradeCharts(ctx context.Context, env *EnvInfo, k8senv *models.KubernetesEnvironment, cl ChartLocations) error {
	return ci.installOrUpgradeIntoExisting(ctx, env, k8senv, cl, true)
}

func (ci ChartInstaller) BuildAndInstallChartsIntoExisting(ctx context.Context, env *EnvInfo, k8senv *models.KubernetesEnvironment, cl ChartLocations) error {
	return ci.installOrUpgradeIntoExisting(ctx, env, k8senv, cl, false)
}

func (ci ChartInstaller) installOrUpgradeIntoExisting(ctx context.Context, env *EnvInfo, k8senv *models.KubernetesEnvironment, cl ChartLocations, upgrade bool) (err error) {
	span, ctx := tracer.StartSpanFromContext(ctx, "chart_installer.install_or_upgrade")
	if ci.kc == nil {
		return errors.New("k8s client is nil")
	}
	ci.dl.SetQAEnvironmentStatus(tracer.ContextWithSpan(context.Background(), span), env.Env.Name, models.Updating)
	defer func() {
		if err != nil {
			// clean up namespace on error
			err2 := ci.cleanUpNamespace(ctx, k8senv.Namespace, env.Env.Name, ci.isRepoPrivileged(env.Env.Repo))
			if err2 != nil {
				ci.log(ctx, "error cleaning up namespace: %v", err2)
			}
			ci.dl.SetQAEnvironmentStatus(tracer.ContextWithSpan(context.Background(), span), env.Env.Name, models.Failure)
			span.Finish(tracer.WithError(err))
			return
		}
		span.Finish()
		ci.dl.SetQAEnvironmentStatus(tracer.ContextWithSpan(context.Background(), span), env.Env.Name, models.Success)
	}()
	csl, err := ci.GenerateCharts(ctx, k8senv.Namespace, env, cl)
	if err != nil {
		return fmt.Errorf("error generating metahelm charts: %w", err)
	}
	b, err := ci.ib.StartBuilds(ctx, env.Env.Name, env.RC)
	if err != nil {
		return fmt.Errorf("error starting image builds: %w", err)
	}
	defer b.Stop()
	if k8senv == nil {
		err = fmt.Errorf("no extant k8s environment for env: %v", env.Env.Name)
		return err
	}
	err = ci.installOrUpgradeCharts(ctx, k8senv.Namespace, csl, env, b, upgrade)
	return err
}

// overrideNamespace is used for testing purposes
var overrideNamespace string

// BuildAndInstallCharts builds images for the environment while simultaneously installing the associated helm charts, returning the k8s namespace or error
func (ci ChartInstaller) BuildAndInstallCharts(ctx context.Context, newenv *EnvInfo, cl ChartLocations) (err error) {
	if ci.kc == nil {
		return errors.New("k8s client is nil")
	}
	var ns string
	if overrideNamespace == "" {
		ns, err = ci.createNamespace(ctx, newenv.Env.Name)
		if err != nil {
			return fmt.Errorf("error creating namespace: %w", err)
		}
	} else {
		ns = overrideNamespace
	}
	defer func() {
		if err != nil {
			// clean up namespace on error
			err2 := ci.cleanUpNamespace(ctx, ns, newenv.Env.Name, ci.isRepoPrivileged(newenv.Env.Repo))
			if err2 != nil {
				ci.log(ctx, "error cleaning up namespace: %v", err2)
			}
			ci.dl.SetQAEnvironmentStatus(context.Background(), newenv.Env.Name, models.Failure)
		} else {
			ci.dl.SetQAEnvironmentStatus(context.Background(), newenv.Env.Name, models.Success)
		}
	}()
	if err := ci.writeK8sEnvironment(ctx, newenv, ns); err != nil {
		return fmt.Errorf("error writing k8s environment: %w", err)
	}
	csl, err := ci.GenerateCharts(ctx, ns, newenv, cl)
	if err != nil {
		return fmt.Errorf("error generating metahelm charts: %w", err)
	}
	b, err := ci.ib.StartBuilds(ctx, newenv.Env.Name, newenv.RC)
	if err != nil {
		return fmt.Errorf("error starting image builds: %w", err)
	}
	defer b.Stop()
	endNamespaceSetup := ci.mc.Timing(mpfx+"namespace_setup", "triggering_repo:"+newenv.RC.Application.Repo)
	if err = ci.setupNamespace(ctx, newenv.Env.Name, newenv.Env.Repo, ns); err != nil {
		return fmt.Errorf("error setting up namespace: %w", err)
	}
	endNamespaceSetup()
	return ci.installOrUpgradeCharts(ctx, ns, csl, newenv, b, false)
}

func (ci ChartInstaller) installOrUpgradeCharts(ctx context.Context, namespace string, csl []metahelm.Chart, env *EnvInfo, b images.Batch, upgrade bool) error {
	eventlogger.GetLogger(ctx).SetK8sNamespace(namespace)
	mhm, err := ci.mhmf(ctx, ci.kc, ci.hccfg, namespace)
	if err != nil || mhm == nil {
		return fmt.Errorf("error getting helm client configuration: %w", err)
	}
	actStr, actingStr := "install", "install"
	if upgrade {
		actStr, actingStr = "upgrade", "upgrad"
	}
	var builderr error
	imageReady := func(c metahelm.Chart) metahelm.InstallCallbackAction {
		status := models.InstallingChartStatus
		if upgrade {
			status = models.UpgradingChartStatus
		}
		if !b.Started(env.Env.Name, c.Title) { // if it hasn't been started, we aren't doing an image build so there's no need to wait
			ci.log(ctx, "metahelm: %v: not waiting on build for chart install/upgrade; continuing", c.Title)
			eventlogger.GetLogger(ctx).SetChartStarted(c.Title, status)
			return metahelm.Continue
		}
		done, err := b.Completed(env.Env.Name, c.Title)
		if err != nil {
			ci.log(ctx, "metahelm: %v: aborting "+actStr+": error building image: %v", c.Title, err)
			builderr = err
			return metahelm.Abort
		}
		if done {
			ci.dl.AddEvent(ctx, env.Env.Name, "image build complete; "+actingStr+"ing chart for "+c.Title)
			eventlogger.GetLogger(ctx).SetChartStarted(c.Title, status)
			return metahelm.Continue
		}
		ci.dl.AddEvent(ctx, env.Env.Name, "image build still pending; waiting to "+actStr+" chart for "+c.Title)
		return metahelm.Wait
	}
	if upgrade {
		err = ci.upgrade(ctx, mhm, imageReady, namespace, csl, env)
	} else {
		err = ci.install(ctx, mhm, imageReady, namespace, csl, env)
	}
	if err != nil && builderr != nil {
		return builderr
	}
	return err
}

var metahelmTimeout = 60 * time.Minute

func completedCB(ctx context.Context, c metahelm.Chart, err error) {
	status := models.DoneChartStatus
	if err != nil {
		status = models.FailedChartStatus
	}
	eventlogger.GetLogger(ctx).SetChartCompleted(c.Title, status)
}

func (ci ChartInstaller) install(ctx context.Context, mhm *metahelm.Manager, cb func(c metahelm.Chart) metahelm.InstallCallbackAction, namespace string, csl []metahelm.Chart, env *EnvInfo) error {
	defer ci.mc.Timing(mpfx+"install", "triggering_repo:"+env.Env.Repo)()
	ctx, cf := context.WithTimeout(ctx, 30*time.Minute)
	defer cf()
	relmap, err := mhm.Install(ctx, csl, metahelm.WithK8sNamespace(namespace), metahelm.WithInstallCallback(cb), metahelm.WithCompletedCallback(func(c metahelm.Chart, err error) { completedCB(ctx, c, err) }), metahelm.WithTimeout(metahelmTimeout))
	if err != nil {
		if _, ok := err.(metahelm.ChartError); ok {
			return err
		}
		return fmt.Errorf("error installing metahelm charts: %w", nitroerrors.User(err))
	}
	ci.dl.AddEvent(ctx, env.Env.Name, fmt.Sprintf("all charts installed; release names: %v", relmap))
	if err := ci.writeReleaseNames(ctx, relmap, namespace, env); err != nil {
		return fmt.Errorf("error writing release names: %w", err)
	}
	return nil
}

func (ci ChartInstaller) upgrade(ctx context.Context, mhm *metahelm.Manager, cb func(c metahelm.Chart) metahelm.InstallCallbackAction, namespace string, csl []metahelm.Chart, env *EnvInfo) error {
	defer ci.mc.Timing(mpfx+"upgrade", "triggering_repo:"+env.Env.Repo)()
	ctx, cf := context.WithTimeout(ctx, 30*time.Minute)
	defer cf()
	err := mhm.Upgrade(ctx, env.Releases, csl, metahelm.WithK8sNamespace(namespace), metahelm.WithInstallCallback(cb), metahelm.WithCompletedCallback(func(c metahelm.Chart, err error) { completedCB(ctx, c, err) }), metahelm.WithTimeout(metahelmTimeout))
	if err != nil {
		if _, ok := err.(metahelm.ChartError); ok {
			return err
		}
		return fmt.Errorf("error upgrading metahelm charts: %w", err)
	}
	ci.dl.AddEvent(ctx, env.Env.Name, fmt.Sprintf("all charts upgraded; release names: %v", env.Releases))
	if err := ci.updateReleaseRevisions(ctx, env); err != nil {
		return fmt.Errorf("error updating release revisions: %w", err)
	}
	return nil
}

func (ci ChartInstaller) writeK8sEnvironment(ctx context.Context, env *EnvInfo, ns string) (err error) {
	k8senv, err := ci.dl.GetK8sEnv(ctx, env.Env.Name)
	if err != nil {
		return fmt.Errorf("error checking if k8s env exists: %w", err)
	}
	if k8senv != nil {
		if err := ci.cleanUpNamespace(ctx, k8senv.Namespace, k8senv.EnvName, k8senv.Privileged); err != nil {
			ci.log(ctx, "error cleaning up namespace for existing k8senv: %v", err)
		}
		if err := ci.dl.DeleteK8sEnv(ctx, env.Env.Name); err != nil {
			return fmt.Errorf("error deleting old k8s env: %w", err)
		}
	}
	rcy, err := yaml.Marshal(env.RC)
	if err != nil {
		return fmt.Errorf("error marshaling RepoConfig YAML: %w", err)
	}
	rm, err := env.RC.RefMap()
	if err != nil {
		return fmt.Errorf("error generating refmap from repoconfig: %w", err)
	}
	rmj, err := json.Marshal(rm)
	if err != nil {
		return fmt.Errorf("error marshaling RefMap JSON: %w", err)
	}
	sig := env.RC.ConfigSignature()
	kenv := &models.KubernetesEnvironment{
		EnvName:         env.Env.Name,
		Namespace:       ns,
		ConfigSignature: sig[:],
		RefMapJSON:      string(rmj),
		RepoConfigYAML:  rcy,
		Privileged:      ci.isRepoPrivileged(env.Env.Repo),
	}
	return ci.dl.CreateK8sEnv(ctx, kenv)
}

func (ci ChartInstaller) updateReleaseRevisions(ctx context.Context, env *EnvInfo) error {
	nrmap := env.RC.NameToRefMap()
	for title, release := range env.Releases {
		ref, ok := nrmap[title]
		if !ok {
			return fmt.Errorf("update release revisions: name missing from name ref map: %v", title)
		}
		if err := ci.dl.UpdateHelmReleaseRevision(ctx, env.Env.Name, release, ref); err != nil {
			ci.log(ctx, "error updating helm release revision: %v", err)
		}
	}
	return nil
}

func (ci ChartInstaller) writeReleaseNames(ctx context.Context, rm metahelm.ReleaseMap, ns string, newenv *EnvInfo) error {
	n, err := ci.dl.DeleteHelmReleasesForEnv(ctx, newenv.Env.Name)
	if err != nil {
		return fmt.Errorf("error deleting existing helm releases: %w", err)
	}
	if n > 0 {
		ci.log(ctx, "deleted %v old helm releases", n)
	}
	releases := []models.HelmRelease{}
	nrmap := newenv.RC.NameToRefMap()
	for title, release := range rm {
		ref, ok := nrmap[title]
		if !ok {
			return fmt.Errorf("write release names: name missing from name ref map: %v", title)
		}
		r := models.HelmRelease{
			EnvName:      newenv.Env.Name,
			Name:         title,
			K8sNamespace: ns,
			Release:      release,
			RevisionSHA:  ref,
		}
		releases = append(releases, r)
	}
	return ci.dl.CreateHelmReleasesForEnv(ctx, releases)
}

// GenerateCharts processes the fetched charts, adds and merges overrides and returns metahelm Charts ready to be installed/upgraded
func (ci ChartInstaller) GenerateCharts(ctx context.Context, ns string, newenv *EnvInfo, cloc ChartLocations) (out []metahelm.Chart, err error) {
	defer ci.mc.Timing(mpfx+"generate_metahelm_charts", "triggering_repo:"+newenv.Env.Repo)()
	genchart := func(i int, rcd models.RepoConfigDependency) (_ metahelm.Chart, err error) {
		defer func() {
			label := "triggering repo"
			if i > 0 {
				label = fmt.Sprintf("dependency: %v (offset %v)", rcd.Name, i)
			}
			if err != nil {
				ci.log(ctx, "error generating chart for %v: %v", label, err)
				return
			}
			ci.log(ctx, "chart generated for %v", label)
		}()
		out := metahelm.Chart{}
		if rcd.Repo != "" {
			if rcd.AppMetadata.ChartTagValue == "" {
				return out, fmt.Errorf("ChartTagValue is empty: offset: %v: %v", i, rcd.Name)
			}
		}
		if rcd.Name == "" {
			return out, fmt.Errorf("Name is empty: offset %v", i)
		}
		if rcd.AppMetadata.Ref == "" {
			return out, fmt.Errorf("Ref is empty: offset %v", i)
		}
		loc, ok := cloc[rcd.Name]
		if !ok {
			return out, fmt.Errorf("dependency not found in ChartLocations: offset %v: %v", i, rcd.Name)
		}
		overrides := map[string]string{
			rcd.AppMetadata.EnvNameValue:   newenv.Env.Name,
			rcd.AppMetadata.NamespaceValue: ns,
		}
		if rcd.Repo != "" {
			overrides[rcd.AppMetadata.ChartTagValue] = rcd.AppMetadata.Ref
		}
		for i, lo := range rcd.AppMetadata.ValueOverrides {
			los := strings.SplitN(lo, "=", 2)
			if len(los) != 2 {
				return out, fmt.Errorf("malformed application ValueOverride: %v: offset %v: %v", rcd.Repo, i, lo)
			}
			overrides[los[0]] = los[1]
		}
		for i, lo := range rcd.ValueOverrides {
			los := strings.SplitN(lo, "=", 2)
			if len(los) != 2 {
				return out, fmt.Errorf("malformed dependency ValueOverride: %v: offset %v: %v", rcd.Repo, i, lo)
			}
			overrides[los[0]] = los[1]
		}
		vo, err := loc.MergeVars(ci.fs, overrides)
		if err != nil {
			return out, fmt.Errorf("error merging chart overrides: %v: %w", rcd.Name, err)
		}
		if err := yaml.Unmarshal(vo, &map[string]interface{}{}); err != nil {
			return out, fmt.Errorf("error in generated YAML overrides for %v: %w", rcd.Name, err)
		}
		out.Title = rcd.Name
		out.Location = loc.ChartPath
		out.ValueOverrides = vo
		out.WaitUntilHelmSaysItsReady = true
		out.DependencyList = rcd.Requires
		return out, nil
	}
	prc := models.RepoConfigDependency{Name: models.GetName(newenv.RC.Application.Repo), Repo: newenv.RC.Application.Repo, AppMetadata: newenv.RC.Application, Requires: []string{}}
	dmap := map[string]struct{}{}
	reqlist := []string{}
	for i, d := range newenv.RC.Dependencies.All() {
		dmap[d.Name] = struct{}{}
		reqlist = append(reqlist, d.Requires...)
		dc, err := genchart(i+1, d)
		if err != nil {
			return out, fmt.Errorf("error generating chart: %v: %w", d.Name, err)
		}
		out = append(out, dc)
		prc.Requires = append(prc.Requires, d.Name)
	}
	for _, r := range reqlist { // verify that everything referenced in 'requires' exists
		if _, ok := dmap[r]; !ok {
			return out, fmt.Errorf("unknown requires on chart: %v", r)
		}
	}
	pc, err := genchart(0, prc)
	if err != nil {
		return out, fmt.Errorf("error generating primary application chart: %w", err)
	}
	out = append(out, pc)
	return out, nil
}

const (
	objLabelKey   = "acyl.dev/managed-by"
	objLabelValue = "nitro"
)

// createNamespace creates the new namespace, returning the namespace name
func (ci ChartInstaller) createNamespace(ctx context.Context, envname string) (string, error) {
	id, err := rand.Int(rand.Reader, big.NewInt(99999))
	if err != nil {
		return "", fmt.Errorf("error getting random integer: %w", err)
	}
	nsn := truncateToDNS1123Label(fmt.Sprintf("nitro-%d-%s", id, envname))
	if err := ci.dl.AddEvent(ctx, envname, "creating namespace: "+nsn); err != nil {
		ci.log(ctx, "error adding create namespace event: %v: %v", envname, err.Error())
	}
	ns := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				objLabelKey: objLabelValue,
			},
		},
	}
	ns.Name = nsn
	ci.log(ctx, "creating namespace: %v", nsn)
	if _, err := ci.kc.CoreV1().Namespaces().Create(ctx, &ns, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("error creating namespace: %w", err)
	}
	return nsn, nil
}

// truncateToDNS1123Label takes a string and truncates it so that it's a valid DNS1123 label.
func truncateToDNS1123Label(str string) string {
	if rs := []rune(str); len(rs) > 63 {
		truncStr := string(rs[:63])
		return strings.TrimRightFunc(truncStr, func(r rune) bool {
			return !isASCIIDigit(r) && !isASCIILetter(r)
		})
	}
	return str
}

func isASCIIDigit(r rune) bool {
	return r >= 48 && r <= 57
}

func isASCIILetter(r rune) bool {
	return (r >= 65 && r <= 90) || (r >= 97 && r <= 122)
}

func clusterRoleBindingName(envname string) string {
	return "nitro-" + envname
}

func envNameFromClusterRoleBindingName(crbName string) string {
	if strings.HasPrefix(crbName, "nitro-") {
		return crbName[6:len(crbName)]
	}
	return ""
}

func (ci ChartInstaller) isRepoPrivileged(repo string) bool {
	for _, r := range ci.k8srepowhitelist {
		if repo == r {
			return true
		}
	}
	return false
}

// setupNamespace prepares the namespace for Tiller and chart installations by creating a service account and any required RBAC settings
func (ci ChartInstaller) setupNamespace(ctx context.Context, envname, repo, ns string) error {
	ci.log(ctx, "setting up namespace: %v", ns)

	// create service account
	ci.log(ctx, "creating service account: %v", serviceAccount)
	if _, err := ci.kc.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: serviceAccount}}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("error creating service acount: %w", err)
	}
	roleName := "nitro"
	// create a role for the service account
	ci.log(ctx, "creating role for service account: %v", roleName)
	if _, err := ci.kc.RbacV1().Roles(ns).Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: ns,
		},
		Rules: []rbacv1.PolicyRule{
			rbacv1.PolicyRule{
				Verbs:     []string{"*"},
				APIGroups: []string{"*"},
				Resources: []string{"*"},
			},
		},
	}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("error creating service account role: %w", err)
	}
	// bind the service account to the role
	ci.log(ctx, "binding service account to role")
	if _, err := ci.kc.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nitro",
		},
		Subjects: []rbacv1.Subject{
			rbacv1.Subject{
				Kind: "ServiceAccount",
				Name: serviceAccount,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     roleName,
		},
	}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("error creating service account cluster role binding: %w", err)
	}
	// if the repo is privileged, bind the service account to the cluster-admin ClusterRole
	if ci.isRepoPrivileged(repo) {
		ci.log(ctx, "creating privileged ClusterRoleBinding: %v", clusterRoleBindingName(envname))
		if _, err := ci.kc.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterRoleBindingName(envname),
				Labels: map[string]string{
					objLabelKey: objLabelValue,
				},
			},
			Subjects: []rbacv1.Subject{
				rbacv1.Subject{
					Kind:      "ServiceAccount",
					Namespace: ns,
					Name:      serviceAccount,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "cluster-admin",
			},
		}, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("error creating cluster role binding (privileged repo): %w", err)
		}
	}
	// create optional user group role bindings
	for group, crole := range ci.k8sgroupbindings {
		ci.log(ctx, "creating user group role binding: %v to %v", group, crole)
		if _, err := ci.kc.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nitro-" + group + "-to-" + crole,
				Namespace: ns,
			},
			Subjects: []rbacv1.Subject{
				rbacv1.Subject{
					Kind: "Group",
					Name: group,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     crole,
			},
		}, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("error creating group role binding: %w", err)
		}
	}
	// create optional secrets
	for name, value := range ci.k8ssecretinjs {
		ci.log(ctx, "injecting secret: %v of type %v (value is from Vault)", name, value.Type)
		if _, err := ci.kc.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Data: value.Data,
			Type: corev1.SecretType(value.Type),
		}, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("error creating secret: %v: %w", name, err)
		}
	}
	return nil
}

var (
	serviceAccount = "nitro"
)

// cleanUpNamespace deletes an environment's namespace and ClusterRoleBinding, if they exist
func (ci ChartInstaller) cleanUpNamespace(ctx context.Context, ns, envname string, privileged bool) error {
	var zero int64
	// Delete in background so that we can release the lock as soon as possible
	bg := metav1.DeletePropagationBackground
	ci.log(ctx, "deleting namespace: %v", ns)
	// the context might be cancelled so use a new one for k8s resource deletion
	ctx2 := context.Background()
	if err := ci.kc.CoreV1().Namespaces().Delete(ctx2, ns, metav1.DeleteOptions{GracePeriodSeconds: &zero, PropagationPolicy: &bg}); err != nil {
		// If the namespace is not found, we do not need to return the error as there is nothing to delete
		if !k8serrors.IsNotFound(err) {
			return fmt.Errorf("error deleting namespace: %w", err)
		}
	}
	if privileged {
		ci.log(ctx, "deleting privileged ClusterRoleBinding: %v", clusterRoleBindingName(envname))
		if err := ci.kc.RbacV1().ClusterRoleBindings().Delete(ctx2, clusterRoleBindingName(envname), metav1.DeleteOptions{}); err != nil {
			ci.log(ctx, "error cleaning up cluster role binding (privileged repo): %v", err)
		}
	}
	return nil
}

// DeleteNamespace deletes the kubernetes namespace and removes k8senv from the database if they exist
func (ci ChartInstaller) DeleteNamespace(ctx context.Context, k8senv *models.KubernetesEnvironment) error {
	if k8senv == nil {
		ci.log(ctx, "unable to delete namespace because k8s env is nil")
		return nil
	}
	if err := ci.cleanUpNamespace(ctx, k8senv.Namespace, k8senv.EnvName, k8senv.Privileged); err != nil {
		return fmt.Errorf("error cleaning up namespace: %w", err)
	}
	return ci.dl.DeleteK8sEnv(ctx, k8senv.EnvName)
}

// Cleanup runs various processes to clean up. For example, it removes orphaned k8s resources older than objMaxAge.
// It is intended to be run periodically via a cronjob.
func (ci ChartInstaller) Cleanup(ctx context.Context, objMaxAge time.Duration) {
	if err := ci.removeOrphanedNamespaces(ctx, objMaxAge); err != nil {
		ci.log(ctx, "error cleaning up orphaned namespaces: %v", err)
	}
	if err := ci.removeOrphanedCRBs(ctx, objMaxAge); err != nil {
		ci.log(ctx, "error cleaning up orphaned ClusterRoleBindings: %v", err)
	}
}

// removeOrphanedNamespaces removes orphaned namespaces
func (ci ChartInstaller) removeOrphanedNamespaces(ctx context.Context, maxAge time.Duration) error {
	if maxAge == 0 {
		return errors.New("maxAge must be greater than zero")
	}
	nsl, err := ci.kc.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: objLabelKey + "=" + objLabelValue})
	if err != nil {
		return fmt.Errorf("error listing namespaces: %w", err)
	}
	ci.log(ctx, "cleanup: found %v nitro namespaces", len(nsl.Items))
	expires := metav1.NewTime(time.Now().UTC().Add(-maxAge))
	for _, ns := range nsl.Items {
		if ns.ObjectMeta.CreationTimestamp.Before(&expires) {
			envs, err := ci.dl.GetK8sEnvsByNamespace(ctx, ns.Name)
			if err != nil {
				return fmt.Errorf("error querying k8senvs by namespace: %v: %w", ns.Name, err)
			}
			if len(envs) == 0 {
				ci.log(ctx, "deleting orphaned namespace: %v", ns.Name)
				bg := metav1.DeletePropagationBackground
				var zero int64
				if err := ci.kc.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, PropagationPolicy: &bg}); err != nil {
					return fmt.Errorf("error deleting namespace: %w", err)
				}
			}
		}
	}
	return nil
}

// removeOrphanedCRBs removes orphaned ClusterRoleBindings
func (ci ChartInstaller) removeOrphanedCRBs(ctx context.Context, maxAge time.Duration) error {
	if maxAge == 0 {
		return errors.New("maxAge must be greater than zero")
	}
	crbl, err := ci.kc.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{LabelSelector: objLabelKey + "=" + objLabelValue})
	if err != nil {
		return fmt.Errorf("error listing ClusterRoleBindings: %w", err)
	}
	ci.log(ctx, "cleanup: found %v nitro ClusterRoleBindings", len(crbl.Items))
	expires := metav1.NewTime(time.Now().UTC().Add(-maxAge))
	for _, crb := range crbl.Items {
		if crb.ObjectMeta.CreationTimestamp.Before(&expires) {
			envname := envNameFromClusterRoleBindingName(crb.ObjectMeta.Name)
			env, err := ci.dl.GetQAEnvironment(ctx, envname)
			if err != nil {
				return fmt.Errorf("error getting environment for ClusterRoleBinding: %v: %w", envname, err)
			}
			if env == nil || env.Status == models.Failure || env.Status == models.Destroyed {
				ci.log(ctx, "deleting orphaned ClusterRoleBinding: %v", crb.ObjectMeta.Name)
				bg := metav1.DeletePropagationBackground
				var zero int64
				if err := ci.kc.RbacV1().ClusterRoleBindings().Delete(ctx, crb.ObjectMeta.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, PropagationPolicy: &bg}); err != nil {
					return fmt.Errorf("error deleting ClusterRoleBinding: %w", err)
				}
			}
		}
	}
	return nil
}

// K8sPod models the returned pod details
type K8sPod struct {
	Name, Ready, Status string
	Restarts            int32
	Age                 time.Duration
}

// GetK8sEnvPodList returns a kubernetes environment pod list for the namespace provided
func (ci ChartInstaller) GetPodList(ctx context.Context, ns string) (out []K8sPod, err error) {
	pl, err := ci.kc.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return []K8sPod{}, fmt.Errorf("error unable to retrieve pods for namespace %v: %w", ns, err)
	}
	if len(pl.Items) == 0 {
		// return blank K8sPod struct if no pods found
		return []K8sPod{}, nil
	}
	for _, p := range pl.Items {
		age := time.Since(p.CreationTimestamp.Time)
		var nReady int
		nContainers := len(p.Spec.Containers)
		for _, c := range p.Status.ContainerStatuses {
			if c.Ready {
				nReady += 1
			}
		}
		rc := int32(0)
		if p.Status.Size() != 0 &&
			len(p.Status.ContainerStatuses) > 0 &&
			p.Status.ContainerStatuses[0].Size() != 0 {
			rc = p.Status.ContainerStatuses[0].RestartCount
		}
		out = append(out, K8sPod{
			Name:     p.Name,
			Ready:    fmt.Sprintf("%v/%v", nReady, nContainers),
			Status:   string(p.Status.Phase),
			Restarts: rc,
			Age:      age,
		})
	}
	return out, nil
}

type K8sPodContainers struct {
	Pod        string
	Containers []string
}

// GetK8sEnvPodContainers returns all container names for the specified pod
func (ci ChartInstaller) GetPodContainers(ctx context.Context, ns, podname string) (out K8sPodContainers, err error) {
	pod, err := ci.kc.CoreV1().Pods(ns).Get(ctx, podname, metav1.GetOptions{})
	if err != nil {
		return K8sPodContainers{}, fmt.Errorf("error unable to retrieve pods for namespace %v: %w", ns, err)
	}
	if pod == nil {
		return K8sPodContainers{}, nil
	}
	var containers []string
	for _, c := range pod.Spec.Containers {
		if c.Name != "" {
			containers = append(containers, c.Name)
		}
	}
	return K8sPodContainers{
		Pod:        pod.Name,
		Containers: containers,
	}, nil
}

// GetK8sEnvPodLogs returns
func (ci ChartInstaller) GetPodLogs(ctx context.Context, ns, podname, container string, lines uint) (out io.ReadCloser, err error) {
	if lines > MaxPodContainerLogLines {
		return nil, errors.Errorf("error line request exceeds limit")
	}
	tl := int64(lines)
	plo := corev1.PodLogOptions{
		Container: container,
		TailLines: &tl,
	}
	req := ci.kc.CoreV1().Pods(ns).GetLogs(podname, &plo)
	if req == nil {
		return nil, errors.Errorf("pod logs request is nil")
	}
	req.BackOff(nil)
	plRC, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("error getting request stream: %w", err)
	}
	return plRC, nil
}
