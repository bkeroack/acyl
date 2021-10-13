package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/api"
	"github.com/dollarshaveclub/acyl/pkg/eventlogger"

	"github.com/dollarshaveclub/acyl/pkg/ghclient"

	"github.com/dollarshaveclub/acyl/pkg/nitro/notifier"

	"github.com/dollarshaveclub/acyl/pkg/locker"
	oldmetrics "github.com/dollarshaveclub/acyl/pkg/metrics"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/namegen"
	nitroenv "github.com/dollarshaveclub/acyl/pkg/nitro/env"
	"github.com/dollarshaveclub/acyl/pkg/nitro/images"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metahelm"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metrics"
	"gopkg.in/src-d/go-billy.v4/osfs"

	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/mitchellh/go-homedir"

	dockerconfig "github.com/docker/cli/cli/config"
	dockertypes "github.com/docker/docker/api/types"
	dockerclient "github.com/docker/docker/client"
	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/line"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
)

var configTestCmd = &cobra.Command{
	Use:   "test",
	Short: "test an acyl.yml",
	Long: `Test an acyl.yaml configuration by creating or updating an environment in the Kubernetes cluster pointed to be the current kubeconfig context according to acyl.yml in the current directory.
The kubeconfig context must have ClusterAdmin privileges.

Branch matching will use the currently checked-out branch and the value passed in for the base-branch flag.

Paths provided by --search-paths will be recursively searched for valid git repositories containing GitHub remotes,
and if found, they will be used as repo dependencies if those GitHub repository names are referenced by acyl.yml. Any branches present in the
local repositories will be used for branch-matching purposes (they do not need to exist in the remote GitHub repo).

Any repo or chart_repo_path dependencies that are referenced in acyl.yml (or transitively included acyl.yml files) but not found in the local filesystem will be accessed via the GitHub API.
Ensure that you have a valid GitHub token in the environment variable GITHUB_TOKEN and that it has at least read permissions
for the repositories referenced that are not present locally.`,
}

var configTestCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "test an acyl.yml by creating a new environment",
	Long: `Create an environment according to acyl.yml in the current directory, in the Kubernetes cluster configured in the current kubeconfig context.

Acyl will automatically inject a secret called "image-pull-secret" derived from your local Docker configuraiton (if present), so any images you can pull locally
will also be able to be pulled by your environment pods if they are configured to use this secret. If you would prefer Acyl to use a custom secret,
use the --image-pull-secret flag and supply the secret as a YAML file containing a valid k8s secret definition.

If your application interacts with the K8s API and requires ClusterAdmin privileges, use --privileged. Note that this has cluster security implications.

The following image build modes are available:

- "none": Do not run builds. This means that all image tags must already exist in their respective image repositories or the environment will fail.
- "furan://<host>:<port>": Use a remote Furan server to build images. Furan only has access to repository revisions that exist in GitHub, if a build references a commit that hasn't been pushed the build will fail.
- "docker": Use a Docker Engine to build and push images, configured by environment variables: https://docs.docker.com/engine/reference/commandline/cli/#environment-variables .
- "docker-nopush": Use a Docker Enginer to build images, but do not push them. This is useful if the Docker Engine is also used by the Kubernetes cluster, where there is no need to push the images to a remote repository.
`,
	Run: configTestCreate,
}

var configTestUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "test an acyl.yml by updating an existing environment",
	Long: `Update an existing environment according to acyl.yml in the current directory, in the Kubernetes cluster configured in the current kubeconfig context.

An environment must already exist both in the Kubernetes cluster and in the acyl state (~/.acyl/*.json).

The same behavior and options as the create subcommand are available. All local repositories will be built and updated.
`,
	Run: configTestUpdate,
}

var configTestDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "test an acyl.yml by deleting an existing environment",
	Long: `Delete an existing environment according to acyl.yml in the current directory, in the Kubernetes cluster configured in the current kubeconfig context.

An environment must already exist both in the Kubernetes cluster and in the acyl state (~/.acyl/*.json).

No image builds are performed.
`,
	Run: configTestDelete,
}

type testEnvConfig struct {
	buildMode, kubeCfgPath, kubeCtx, imagepullsecretPath string
	statedir, workingdir, wordnetpath                    string
	privileged, enableUI                                 bool
	pullRequest                                          uint
	dockerCfg                                            map[string]dockertypes.AuthConfig
	k8sCfg                                               config.K8sConfig
	uiPort                                               int
	uiAssets                                             string
	operationTimeout                                     time.Duration
}

var testEnvCfg testEnvConfig
var testMockUser = "john.doe"

func init() {
	configTestCmd.PersistentFlags().UintVar(&testEnvCfg.pullRequest, "pr", 999, "Pull request number for simulated create/update events")
	configTestCmd.PersistentFlags().StringVar(&testEnvCfg.buildMode, "image-build-mode", "none", "Image build mode (see help)")
	configTestCmd.PersistentFlags().StringVar(&testEnvCfg.kubeCfgPath, "kubecfg", "", "Path to kubeconfig (overrides KUBECONFIG)")
	configTestCmd.PersistentFlags().StringVar(&testEnvCfg.imagepullsecretPath, "image-pull-secret", "", "Path to manual image pull secret YAML file (optional, see help)")
	configTestCmd.PersistentFlags().BoolVar(&testEnvCfg.privileged, "privileged", false, "give the environment service account ClusterAdmin privileges via a ClusterRoleBinding (use this if your application requires ClusterAdmin abilities)")
	configTestCmd.PersistentFlags().StringVar(&k8sGroupBindingsStr, "k8s-group-bindings", "", "optional k8s RBAC group bindings for the environment namespace (comma-separated) in GROUP1=CLUSTER_ROLE1,GROUP2=CLUSTER_ROLE2 format (ex: users=edit)")
	configTestCmd.PersistentFlags().StringVar(&k8sSecretsStr, "k8s-secret-injections", "", "optional k8s secret injections (comma-separated) for new environment namespaces (other than image-pull-secret) in SECRET_NAME=VAULT_ID (Vault path using secrets mapping) format. Secret value in Vault must be a JSON-encoded object with two keys: 'data' (map of string to base64-encoded bytes), 'type' (string).")
	configTestCmd.PersistentFlags().BoolVar(&testEnvCfg.enableUI, "ui", false, "enable UI by opening a browser window with status page")
	configTestCmd.PersistentFlags().DurationVar(&testEnvCfg.operationTimeout, "operation-timeout", 10*time.Minute, "Operation timeout (ex: 10m)")
	wd, err := os.Getwd()
	if err != nil {
		log.Printf("error getting working directory: %v", err)
	}
	testEnvCfg.workingdir = wd
	hd, err := homedir.Dir()
	if err != nil {
		log.Printf("error getting home directory: %v", err)
		hd = wd
	}
	defaultStateDir := os.Getenv("XDG_DATA_HOME")
	if defaultStateDir == "" {
		defaultStateDir = filepath.Join(hd, ".local", "share")
	}
	configTestCmd.PersistentFlags().StringVar(&testEnvCfg.statedir, "state-dir", filepath.Join(defaultStateDir, "acyl"), "path to state files")
	defaultDataDirs := os.Getenv("XDG_DATA_DIRS")
	if defaultDataDirs == "" {
		defaultDataDirs = "/usr/local/share:/usr/share"
	}
	var defaultDataDir string
	for _, p := range strings.Split(defaultDataDirs, ":") {
		if _, err := os.Stat(p); err == nil {
			defaultDataDir = p
			break
		}
	}
	if defaultDataDir == "" {
		log.Printf("warning: no valid data directory found (tried: %v); using ~/.acyl", defaultDataDirs)
		defaultDataDir = filepath.Join(hd, ".acyl")
	}
	// prefer wordnet file within this working directory tree if it exists
	wnfpath := filepath.Join(wd, "data", "words.json.gz")
	if _, err := os.Stat(wnfpath); err != nil {
		wnfpath = filepath.Join(defaultDataDir, "acyl", "words.json.gz")
	}
	configTestCmd.PersistentFlags().StringVar(&testEnvCfg.wordnetpath, "wordnet-file", wnfpath, "path to wordnet file for name generation")
	// prefer assets within this working directory if they exist
	uiAssetsPath := filepath.Join(wd, "ui")
	if _, err := os.Stat(uiAssetsPath); err != nil {
		uiAssetsPath = filepath.Join(defaultDataDir, "acyl", "ui")
	}
	configTestCmd.PersistentFlags().StringVar(&testEnvCfg.uiAssets, "ui-assets", uiAssetsPath, "path to UI assets")
	configTestCmd.AddCommand(configTestCreateCmd)
	configTestCmd.AddCommand(configTestUpdateCmd)
	configTestCmd.AddCommand(configTestDeleteCmd)
}

var output = line.New(os.Stderr, "", "", line.WhiteColor)

func checkTestEnvConfig() error {
	if testEnvCfg.kubeCfgPath != "" {
		_, err := os.Stat(testEnvCfg.kubeCfgPath)
		if err != nil {
			if os.IsNotExist(err) {
				return errors.New("kubecfg not found")
			}
			return errors.Wrap(err, "error checking kubeconfig path")
		}
	}
	switch {
	case testEnvCfg.buildMode == "none":
		break
	case strings.HasPrefix(testEnvCfg.buildMode, "furan://") && len(testEnvCfg.buildMode) > 8:
		break
	case testEnvCfg.buildMode == "docker":
		break
	case testEnvCfg.buildMode == "docker-nopush":
		break
	default:
		return errors.New("build mode unimplemented: " + testEnvCfg.buildMode)
	}
	dcfg, err := dockerconfig.Load("")
	if err != nil {
		output.Yellow("warning: ").White("error loading Docker config: " + err.Error() + "\n")
		if testEnvCfg.imagepullsecretPath == "" {
			output.Yellow("--image-pull-secret is not set and Docker config not loaded; image-pull-secret will not be created in the environment\n")
			output.Red().Progress("If your environment uses private image repositories it will probably fail to start up correctly!\n")
		}
	}
	auths, err := dcfg.GetAllCredentials()
	if err != nil {
		return errors.Wrap(err, "error getting docker config credentials")
	}
	dcfg.AuthConfigs = auths
	dockerAuthCfg := make(map[string]dockertypes.AuthConfig, len(auths))
	for k, v := range auths {
		dockerAuthCfg[k] = dockertypes.AuthConfig(v)
	}
	testEnvCfg.dockerCfg = dockerAuthCfg
	if testEnvCfg.imagepullsecretPath != "" {
		_, err := os.Stat(testEnvCfg.imagepullsecretPath)
		if err != nil {
			if os.IsNotExist(err) {
				return errors.New("image pull secret file not found")
			}
			return errors.Wrap(err, "error checking image pull secret path")
		}
	}
	k8scfg := &config.K8sConfig{}
	if err := k8scfg.ProcessGroupBindings(k8sGroupBindingsStr); err != nil {
		return errors.Wrap(err, "error in k8s group bindings")
	}
	k8scfg.SecretInjections = map[string]config.K8sSecret{}
	if k8sSecretsStr != "" {
		sc, err := getSecretClient()
		if err != nil {
			return errors.Wrap(err, "error getting secrets client for secrets injections")
		}
		if err := k8scfg.ProcessSecretInjections(sc, k8sSecretsStr); err != nil {
			return errors.Wrap(err, "error in k8s group bindings")
		}
	}
	testEnvCfg.k8sCfg = *k8scfg
	return nil
}

func dockerAuthsToImagePullSecret() (config.K8sSecret, error) {
	out := config.K8sSecret{}
	if testEnvCfg.dockerCfg == nil {
		return out, errors.New("docker config not set")
	}
	auths := struct {
		Auths map[string]dockertypes.AuthConfig `json:"auths"`
	}{
		Auths: testEnvCfg.dockerCfg,
	}
	b, err := json.Marshal(auths)
	if err != nil {
		return out, errors.Wrap(err, "error marshaling docker auths")
	}
	out.Type = "kubernetes.io/dockerconfigjson"
	out.Data = map[string][]byte{
		".dockerconfigjson": b,
	}
	return out, nil
}

func loadImagePullSecret() (config.K8sSecret, error) {
	out := config.K8sSecret{}
	if testEnvCfg.imagepullsecretPath == "" {
		return out, errors.New("image pull secret path is empty")
	}
	s := &corev1.Secret{}
	d, err := ioutil.ReadFile(testEnvCfg.imagepullsecretPath)
	if err != nil {
		return out, errors.Wrap(err, "error reading image pull secret")
	}
	if err := s.Unmarshal(d); err != nil {
		return out, errors.Wrap(err, "error unmarshaling image pull secret into core/v1.Secret")
	}
	out.Type = string(s.Type)
	out.Data = s.Data
	return out, nil
}

func getState() (*persistence.FakeDataLayer, error) {
	dl := persistence.NewFakeDataLayer()
	_, err := os.Stat(testEnvCfg.statedir)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(testEnvCfg.statedir, os.ModeDir|os.ModePerm); err != nil {
				return nil, errors.Wrap(err, "error creating state dir")
			}
		} else {
			return nil, errors.Wrap(err, "error checking for state dir")
		}
	}
	matches, err := filepath.Glob(filepath.Join(testEnvCfg.statedir, "*.json"))
	if err != nil {
		return nil, errors.Wrap(err, "error checking for state files")
	}
	if len(matches) > 0 {
		if err := dl.Load(testEnvCfg.statedir); err != nil {
			return nil, errors.Wrap(err, "error loading state")
		}
	}
	return dl, nil
}

func getImageBackend(dl persistence.DataLayer, rc ghclient.RepoClient, auths map[string]dockertypes.AuthConfig) (images.BuilderBackend, error) {
	switch {
	case testEnvCfg.buildMode == "none":
		return &images.NoneBackend{}, nil
	case strings.HasPrefix(testEnvCfg.buildMode, "furan2://"):

		// TODO: add support for static GH token and Furan 2 API key
		clierr("Furan 2 support not fully implemented for local use yet")

		fb, err := images.NewFuran2BuilderBackend(testEnvCfg.buildMode[8:len(testEnvCfg.buildMode)], "", 0, false, dl, nil, &oldmetrics.FakeCollector{})
		if err != nil {
			return nil, errors.Wrap(err, "error getting furan 2 backend")
		}
		return fb, nil
	case testEnvCfg.buildMode == "docker" || testEnvCfg.buildMode == "docker-nopush":
		dc, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv)
		if err != nil {
			return nil, errors.Wrap(err, "error getting docker client")
		}
		dc.NegotiateAPIVersion(context.Background())
		return &images.DockerBuilderBackend{
			Auths: auths,
			DC:    dc,
			DL:    dl,
			RC:    rc,
			Push:  !strings.HasSuffix(testEnvCfg.buildMode, "-nopush"),
		}, nil
	default:
		return nil, errors.New("build mode unimplemented: " + testEnvCfg.buildMode)
	}
}

func getStatusCallback() ghclient.StatusCallback {
	if testEnvCfg.enableUI {
		return func(ctx context.Context, repo string, ref string, status *ghclient.CommitStatus) error {
			// only open a new browser window on the initial pending notification
			if status.Status == models.CommitStatusPending.Key() {
				opencmd := fmt.Sprintf("%v http://localhost:%v/ui/event/status?id=%v", openPath, testEnvCfg.uiPort, eventlogger.GetLogger(ctx).ID.String())
				shellsl := strings.Split(shell, " ")
				cmdsl := append(shellsl, opencmd)
				c := exec.Command(cmdsl[0], cmdsl[1:]...)
				if out, err := c.CombinedOutput(); err != nil {
					return errors.Wrapf(err, "error opening UI browser: %v: %v", strings.Join(cmdsl, " "), string(out))
				}
			}
			return nil
		}
	}
	return nil
}

func testConfigSetup(dl persistence.DataLayer) (*nitroenv.Manager, context.Context, *models.RepoRevisionData, error) {
	err := checkTestEnvConfig()
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "error validating options")
	}
	var s config.K8sSecret
	if testEnvCfg.imagepullsecretPath == "" {
		s, err = dockerAuthsToImagePullSecret()
	} else {
		s, err = loadImagePullSecret()
	}
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "error getting image pull secret")
	}
	fs := osfs.New("")
	ng, err := namegen.NewWordnetNameGenerator(testEnvCfg.wordnetpath, logger)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "error getting name generator")
	}
	mc := &metrics.FakeCollector{}
	plf, err := locker.NewFakePreemptiveLockerFactory([]locker.LockProviderOption{
		locker.WithLockTimeout(1 * time.Second),
	})
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "error creating preemptive locker factory")
	}
	mg, ri, _, ctx := generateLocalMetaGetter(dl, getStatusCallback())
	ibb, err := getImageBackend(dl, mg.RC, testEnvCfg.dockerCfg)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "error getting image builder")
	}
	ib := &images.ImageBuilder{
		Backend: ibb,
		MC:      mc,
		DL:      dl,
	}
	testEnvCfg.k8sCfg.SecretInjections["image-pull-secret"] = s
	if testEnvCfg.privileged {
		testEnvCfg.k8sCfg.PrivilegedRepoWhitelist = []string{ri.GitHubRepoName}
	}
	ci, err := metahelm.NewChartInstallerWithClientsetFromContext(ib, dl, fs, mc, testEnvCfg.k8sCfg.GroupBindings, testEnvCfg.k8sCfg.PrivilegedRepoWhitelist, testEnvCfg.k8sCfg.SecretInjections, testEnvCfg.kubeCfgPath, helmClientConfig)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "error getting chart installer")
	}
	ncfg := models.Notifications{}
	ncfg.FillMissingTemplates()
	return &nitroenv.Manager{
			NF: func(lf func(string, ...interface{}), notifications models.Notifications, user string) notifier.Router {
				tb := &notifier.TerminalBackend{Output: os.Stdout, Margin: 80}
				return &notifier.MultiRouter{Backends: []notifier.Backend{tb}}
			},
			DefaultNotifications: ncfg,
			DL:                   dl,
			RC:                   mg.RC,
			MC:                   mc,
			NG:                   ng,
			PLF:                  plf,
			FS:                   fs,
			MG:                   mg,
			CI:                   ci,
			OperationTimeout:     testEnvCfg.operationTimeout,
		}, ctx, &models.RepoRevisionData{
			PullRequest:  testEnvCfg.pullRequest,
			Repo:         ri.GitHubRepoName,
			BaseBranch:   baseBranch,
			BaseSHA:      "",
			SourceBranch: ri.HeadBranch,
			SourceSHA:    ri.HeadSHA,
			User:         testMockUser,
		}, nil
}

func runUI(dl persistence.DataLayer, nitromgr *nitroenv.Manager, repo string) (*http.Server, error) {
	if !testEnvCfg.enableUI {
		return nil, nil
	}
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, errors.Wrap(err, "error starting UI listener")
	}
	testEnvCfg.uiPort = l.Addr().(*net.TCPAddr).Port
	uilogger := log.New(os.Stderr, "ui server: ", log.LstdFlags)
	server := &http.Server{}
	httpapi := api.NewDispatcher(server)
	ci, ok := nitromgr.CI.(*metahelm.ChartInstaller)
	if !ok {
		return nil, fmt.Errorf("unexpected type for ChartInstaller: %T", nitromgr.CI)
	}
	deps := &api.Dependencies{
		DataLayer:          dl,
		ServerConfig:       serverConfig,
		Logger:             uilogger,
		EnvironmentSpawner: nitromgr,
		KubernetesReporter: ci,
	}
	setDummyGHConfig()
	httpapi.AppGHClientFactoryFunc = func(_ string) ghclient.GitHubAppInstallationClient {
		return &ghclient.FakeRepoClient{
			GetUserAppRepoPermissionsFunc: func(_ context.Context, instID int64) (map[string]ghclient.AppRepoPermissions, error) {
				return map[string]ghclient.AppRepoPermissions{
					repo: ghclient.AppRepoPermissions{Repo: repo, Pull: true, Push: true},
				}, nil
			},
		}
	}
	if err := httpapi.RegisterVersions(deps,
		api.WithGitHubConfig(githubConfig),
		api.WithUIBaseURL(fmt.Sprintf("http://localhost:%v", testEnvCfg.uiPort)),
		api.WithUIAssetsPath(testEnvCfg.uiAssets),
		api.WithUIRoutePrefix("/ui"),
		api.WithUIDummySessionUser(testMockUser)); err != nil {
		return nil, errors.Wrap(err, "error registering api versions")
	}
	go func() {
		uilogger.Printf("listening on :%v", testEnvCfg.uiPort)
		uilogger.Printf("error running UI http server: %v", server.Serve(l))
	}()
	return server, nil
}

func waitOnUI(srv *http.Server) {
	if testEnvCfg.enableUI {
		done := make(chan os.Signal, 1)
		signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
		output.Green().Progress().Println("Keeping UI server running (ctrl-c to exit)...")
		<-done
		if srv != nil {
			srv.Shutdown(context.Background())
		}
	}
}

func configTestCreate(cmd *cobra.Command, args []string) {
	perr := func(err error) {
		output.Red("error: ").White().Println(err.Error())
	}
	var err error
	defer func() {
		if err != nil {
			os.Exit(1)
		}
	}()
	dl, err := getState()
	if err != nil {
		perr(err)
		return
	}
	nitromgr, ctx, rrd, err := testConfigSetup(dl)
	if err != nil {
		perr(err)
		return
	}
	uisrv, err := runUI(dl, nitromgr, rrd.Repo)
	if err != nil {
		perr(err)
		return
	}
	defer func() {
		dl, ok := nitromgr.DL.(*persistence.FakeDataLayer)
		if ok {
			if _, err2 := dl.Save(testEnvCfg.statedir); err2 != nil {
				perr(err2)
			}
		}
	}()
	output.Green().Progress().Println("Creating environment...")
	name, err := nitromgr.Create(ctx, *rrd)
	if err != nil {
		perr(err)
		waitOnUI(uisrv)
		return
	}
	output.Green().Printf("Success creating environment: %v\n", name)
	waitOnUI(uisrv)
}

func configTestUpdate(cmd *cobra.Command, args []string) {
	perr := func(err error) {
		output.Red("error: ").White().Println(err.Error())
	}
	var err error
	defer func() {
		if err != nil {
			os.Exit(1)
		}
	}()
	dl, err := getState()
	if err != nil {
		perr(err)
		return
	}
	nitromgr, ctx, rrd, err := testConfigSetup(dl)
	if err != nil {
		perr(err)
		return
	}
	uisrv, err := runUI(dl, nitromgr, rrd.Repo)
	if err != nil {
		perr(err)
		return
	}
	defer func() {
		dl, ok := nitromgr.DL.(*persistence.FakeDataLayer)
		if ok {
			if _, err2 := dl.Save(testEnvCfg.statedir); err2 != nil {
				perr(err2)
			}
		}
	}()
	// make sure an extant environment exists
	envs, err := nitromgr.DL.GetExtantQAEnvironments(context.Background(), rrd.Repo, rrd.PullRequest)
	if err != nil {
		perr(err)
		return
	}
	if len(envs) != 1 {
		perr(fmt.Errorf("can't find any extant environments for repo %v and pull request %v! Update is not possible", rrd.Repo, rrd.PullRequest))
		return
	}
	output.Green().Progress().Println("Updating environment...")
	name, err := nitromgr.Update(ctx, *rrd)
	if err != nil {
		perr(err)
		waitOnUI(uisrv)
		return
	}
	output.Green().Printf("Success updating environment: %v\n", name)
	waitOnUI(uisrv)
}

func configTestDelete(cmd *cobra.Command, args []string) {
	perr := func(err error) {
		output.Red("error: ").White().Println(err.Error())
	}
	var err error
	defer func() {
		if err != nil {
			os.Exit(1)
		}
	}()
	dl, err := getState()
	if err != nil {
		perr(err)
		return
	}
	nitromgr, ctx, rrd, err := testConfigSetup(dl)
	if err != nil {
		perr(err)
		return
	}
	uisrv, err := runUI(dl, nitromgr, rrd.Repo)
	if err != nil {
		perr(err)
		return
	}
	defer func() {
		dl, ok := nitromgr.DL.(*persistence.FakeDataLayer)
		if ok {
			if _, err2 := dl.Save(testEnvCfg.statedir); err2 != nil {
				perr(err2)
			}
		}
	}()
	// make sure an extant environment exists
	envs, err := nitromgr.DL.GetExtantQAEnvironments(context.Background(), rrd.Repo, rrd.PullRequest)
	if err != nil {
		perr(err)
		return
	}
	if len(envs) != 1 {
		perr(fmt.Errorf("can't find any extant environments for repo %v and pull request %v! Delete is not possible", rrd.Repo, rrd.PullRequest))
		return
	}
	output.Green().Progress().Println("Deleting environment...")
	err = nitromgr.Delete(ctx, rrd, models.DestroyApiRequest)
	if err != nil {
		perr(err)
		return
	}
	output.Green().Printf("Success deleting environment: %v\n", envs[0].Name)
	waitOnUI(uisrv)
}
