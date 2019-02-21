// +build linux darwin freebsd netbsd openbsd

package cmd

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/api"
	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/ghevent"
	"github.com/dollarshaveclub/acyl/pkg/locker"
	"github.com/dollarshaveclub/acyl/pkg/metrics"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/namegen"
	nitroenv "github.com/dollarshaveclub/acyl/pkg/nitro/env"
	"github.com/dollarshaveclub/acyl/pkg/nitro/images"
	"github.com/dollarshaveclub/acyl/pkg/nitro/meta"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metahelm"
	nitrometrics "github.com/dollarshaveclub/acyl/pkg/nitro/metrics"
	"github.com/dollarshaveclub/acyl/pkg/nitro/notifier"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/dollarshaveclub/acyl/pkg/reap"
	"github.com/dollarshaveclub/acyl/pkg/slacknotifier"
	"github.com/dollarshaveclub/acyl/pkg/spawner"
	newrelic "github.com/newrelic/go-agent"
	"github.com/nlopes/slack"
	"github.com/spf13/cobra"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	"gopkg.in/src-d/go-billy.v4/osfs"
)

var serverConfig config.ServerConfig
var githubConfig config.GithubConfig
var consulConfig config.ConsulConfig
var slackConfig config.SlackConfig
var backendConfig config.BackendConfig
var aminoConfig config.AminoConfig

var k8sConfig config.K8sConfig
var k8sGroupBindingsStr, k8sSecretsStr, k8sPrivilegedReposStr string

var pgConfig config.PGConfig
var logger *log.Logger
var furanHostStr string
var aminoAddr string
var s3config config.S3Config
var failureTemplatePath string

// serverCmd represents the server command
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run acyl server",
	Long:  `Run an acyl HTTPS API server`,
	Run:   server,
	PreRun: func(cmd *cobra.Command, args []string) {
		getSecrets()
		setupServerLogger()
	},
}

func init() {
	serverCmd.PersistentFlags().UintVar(&serverConfig.HTTPSPort, "https-port", 4000, "REST HTTP(S) TCP port")
	serverCmd.PersistentFlags().StringVar(&serverConfig.HTTPSAddr, "https-addr", "0.0.0.0", "REST HTTP(S) listen address")
	serverCmd.PersistentFlags().BoolVar(&serverConfig.DisableTLS, "disable-tls", false, "Disable TLS for the REST HTTP(S) server")
	serverCmd.PersistentFlags().StringVar(&githubConfig.TypePath, "repo-type-path", "acyl.yml", "Relative path within the target repo to look for the type definition")
	serverCmd.PersistentFlags().StringVar(&serverConfig.WordnetPath, "wordnet-path", "/opt/words.json.gz", "Path to gzip-compressed JSON wordnet file")
	serverCmd.PersistentFlags().StringSliceVar(&serverConfig.FuranAddrs, "furan-addrs", []string{}, "Furan hosts (optional, otherwise use Consul discovery)")
	serverCmd.PersistentFlags().StringVar(&consulConfig.Addr, "consul-addr", "127.0.0.1:8500", "Consul agent address")
	serverCmd.PersistentFlags().StringVar(&consulConfig.LockPrefix, "consul-lock-prefix", "acyl/", "Consul lock name prefix")
	serverCmd.PersistentFlags().StringVar(&slackConfig.Channel, "slack-channel", "dyn-qa-notifications", "Slack channel for notifications")
	serverCmd.PersistentFlags().StringVar(&slackConfig.Username, "slack-username", "Acyl Environment Notifier", "Slack username for notifications")
	serverCmd.PersistentFlags().StringVar(&slackConfig.IconURL, "slack-icon-url", "https://picsum.photos/48/48", "Slack user avatar icon for notifications")
	serverCmd.PersistentFlags().StringVar(&slackConfig.MapperRepo, "slack-mapper-repo", "dollarshaveclub/dqa-dev-tools", "Github repo containing github -> slack username map")
	serverCmd.PersistentFlags().StringVar(&slackConfig.MapperRepoRef, "slack-mapper-repo-ref", "master", "Ref for username map Github repo")
	serverCmd.PersistentFlags().StringVar(&slackConfig.MapperMapPath, "slack-mapper-map-path", "lib/user_map.json", "Path to username map JSON within the Github repo")
	serverCmd.PersistentFlags().UintVar(&slackConfig.MapperUpdateIntervalSeconds, "slack-mapper-update-interval-seconds", 60, "Username map update interval")
	serverCmd.PersistentFlags().StringVar(&backendConfig.AminoAddr, "amino-addr", "internal-qakube-amino-app-elb-1632933870.us-west-2.elb.amazonaws.com:3000", "the address to the Amino server")
	serverCmd.PersistentFlags().UintVar(&serverConfig.ReaperIntervalSecs, "cleanup-interval", 600, "Approximate interval between cleanup runs in seconds (set to 0 to disable)")
	serverCmd.PersistentFlags().UintVar(&serverConfig.EventRateLimitPerSecond, "event-rate-limit", 25, "Event rate limit in events per second (any in excess will be dropped)")
	serverCmd.PersistentFlags().UintVar(&serverConfig.GlobalEnvironmentLimit, "global-environment-limit", 0, "Maximum number of running environments (0 means no limit)")
	serverCmd.PersistentFlags().StringVar(&aminoConfig.HelmChartToRepoRaw, "helm-chart-to-repo", "{}", "Mapping of Helm chart to Github repo")
	serverCmd.PersistentFlags().StringVar(&aminoConfig.AminoDeploymentToRepoRaw, "deployment-to-repo", "{}", "Mapping of Amino deployments to Github repo")
	serverCmd.PersistentFlags().StringVar(&aminoConfig.AminoJobToRepoRaw, "job-to-repo", "{}", "Mapping of Amino jobs to Github repo")
	serverCmd.PersistentFlags().StringVar(&serverConfig.HostnameTemplate, "hostname-template", "{{ .Name }}.qa.shave.io", "Environment hostname")
	serverCmd.PersistentFlags().BoolVar(&serverConfig.DebugEndpoints, "debug-endpoints", false, "Enable debugging HTTP endpoints (pprof)")
	serverCmd.PersistentFlags().StringArrayVar(&serverConfig.DebugEndpointsIPWhitelists, "debug-endpoints-ip-whitelists", []string{"10.10.0.0/16"}, "IP CIDR ranges to allow access to debug endpoints")
	serverCmd.PersistentFlags().BoolVar(&serverConfig.NitroFeatureFlag, "enable-nitro", false, "Use Nitro environment engine (EXPERIMENTAL)")
	serverCmd.PersistentFlags().StringVar(&serverConfig.NotificationsDefaultsJSON, "nitro-notifications-defaults-json", "{}", "JSON-encoded notifications defaults for Nitro")
	serverCmd.PersistentFlags().StringVar(&k8sGroupBindingsStr, "k8s-group-bindings", "", "optional k8s RBAC group bindings (comma-separated) for new environment namespaces in GROUP1=CLUSTER_ROLE1,GROUP2=CLUSTER_ROLE2 format (ex: users=edit) (Nitro)")
	serverCmd.PersistentFlags().StringVar(&k8sSecretsStr, "k8s-secret-injections", "", "optional k8s secret injections (comma-separated) for new environment namespaces in SECRET_NAME=VAULT_ID (Vault path using secrets mapping) format. Secret value in Vault must be a JSON-encoded object with two keys: 'data' (map of string to base64-encoded bytes), 'type' (string). (Nitro)")
	serverCmd.PersistentFlags().StringVar(&k8sPrivilegedReposStr, "k8s-privileged-repo-whitelist", "dollarshaveclub/acyl", "optional comma-separated whitelist of GitHub repositories whose environment service accounts will be allowed cluster-admin privileges (Nitro)")
	serverCmd.PersistentFlags().StringVar(&failureTemplatePath, "failure-template-path", "/opt/html/failedenv.html.tmpl", "path to HTML failure report template (if missing, failure reports will be disabled")
	serverCmd.PersistentFlags().StringVar(&s3config.Region, "failure-report-s3-region", "us-west-2", "AWS S3 region for environment failure reports")
	serverCmd.PersistentFlags().StringVar(&s3config.Bucket, "failure-report-s3-bucket", "", "AWS S3 bucket for environment failure reports")
	serverCmd.PersistentFlags().StringVar(&s3config.KeyPrefix, "failure-report-s3-key-prefix", "", "AWS S3 key prefix for environment failure reports (key format: <prefix>envfailures/<timestamp>/<env name>.html)")
	RootCmd.AddCommand(serverCmd)
}

func setupServerLogger() {
	logger = log.New(os.Stderr, "", log.LstdFlags)
}

func loadFailureTemplate(m *nitroenv.Manager) {
	ftd, err := ioutil.ReadFile(failureTemplatePath)
	if err != nil {
		log.Printf("error reading failure template: %v: %v", failureTemplatePath, err)
		s3config.Bucket = ""
		s3config.Region = ""
		return
	}
	if err := m.InitFailureTemplate(ftd); err != nil {
		log.Fatalf("error processing failure template: %v", err)
	}
}

func startDatadogTracer() {
	opts := []tracer.StartOption{tracer.WithAgentAddr(datadogTracingAgentAddr)}
	opts = append(opts, tracer.WithServiceName(datadogServiceName))
	for _, tag := range strings.Split(dogstatsdTags, ",") {
		keyValPair := strings.Split(tag, ":")
		if len(keyValPair) != 2 {
			log.Fatalf("invalid tags: %v", dogstatsdTags)
		}
		key, val := keyValPair[0], keyValPair[1]
		opts = append(opts, tracer.WithGlobalTag(key, val))
	}
	tracer.Start(opts...)
}

func server(cmd *cobra.Command, args []string) {
	var err error
	nrapp := nullNewRelicApp{}

	mc, err := metrics.NewDatadogCollector(dogstatsdAddr, logger)
	if err != nil {
		log.Fatalf("instantiating datadog: %v", err)
	}

	if err := aminoConfig.Parse(); err != nil {
		log.Fatalf("error parsing Amino config: %s", err)
	}

	dl, err := persistence.NewPGLayer(&pgConfig, logger)
	if err != nil {
		log.Fatalf("error opening PG database: %v", err)
	}
	defer dl.Close()

	rc := ghclient.NewGitHubClient(githubConfig.Token)
	ng, err := namegen.NewWordnetNameGenerator(serverConfig.WordnetPath, logger)
	if err != nil {
		log.Fatalf("error opening wordnet file: %v", err)
	}

	lp, err := locker.NewConsulLocker(consulConfig.Addr, consulConfig.LockPrefix)
	if err != nil {
		log.Fatalf("error creating Consul lock service: %v", err)
	}

	slackapi := slack.New(slackConfig.Token)
	mapper := slacknotifier.NewRepoBackedSlackUsernameMapper(rc, slackConfig.MapperRepo, slackConfig.MapperMapPath, slackConfig.MapperRepoRef, time.Duration(slackConfig.MapperUpdateIntervalSeconds)*time.Second)
	cn := slacknotifier.NewSlackNotifier(slackConfig.Channel, slackapi, mapper)

	var envspawner spawner.EnvironmentSpawner

	es, err := spawner.NewQASpawner(logger, dl, ng, rc, lp, serverConfig.FuranAddrs, consulConfig.Addr, cn, mc, mc, nrapp, &awsCreds, &awsConfig, &backendConfig, &aminoConfig, githubConfig.TypePath, serverConfig.GlobalEnvironmentLimit, serverConfig.HostnameTemplate)
	if err != nil {
		log.Fatalf("error creating spawner: %s", err)
	}

	if serverConfig.NitroFeatureFlag {
		logger.Printf("nitro enabled")
		nmc, err := nitrometrics.NewDatadogCollector("acyl.nitro.", dogstatsdAddr, strings.Split(dogstatsdTags, ","))
		if err != nil {
			log.Fatalf("error setting up nitro metrics collector: %v", err)
		}
		fbb, err := images.NewFuranBuilderBackend(serverConfig.FuranAddrs, consulConfig.Addr, dl, mc, os.Stderr)
		if err != nil {
			log.Fatalf("error getting Furan image builder backend: %v", err)
		}
		ib := &images.ImageBuilder{
			DL:      dl,
			MC:      nmc,
			Backend: fbb,
		}
		fs := osfs.New("")
		if err := k8sConfig.ProcessPrivilegedRepos(k8sPrivilegedReposStr); err != nil {
			log.Fatalf("error in k8s privileged repos: %v", err)
		}
		if err := k8sConfig.ProcessGroupBindings(k8sGroupBindingsStr); err != nil {
			log.Fatalf("error in k8s group bindings: %v", err)
		}
		sc, err := getSecretClient()
		if err != nil {
			log.Fatalf("error getting secrets client: %v", err)
		}
		if err := k8sConfig.ProcessSecretInjections(sc, k8sSecretsStr); err != nil {
			log.Fatalf("error in k8s secret injections: %v", err)
		}
		ci, err := metahelm.NewChartInstaller(ib, dl, fs, nmc, k8sConfig.GroupBindings, k8sConfig.PrivilegedRepoWhitelist, k8sConfig.SecretInjections, metahelm.TillerConfig{})
		if err != nil {
			log.Fatalf("error getting metahelm chart installer: %v", err)
		}
		mg := &meta.DataGetter{RC: rc, FS: fs}
		ncfg := models.Notifications{}
		if err := json.Unmarshal([]byte(serverConfig.NotificationsDefaultsJSON), &ncfg); err != nil {
			log.Printf("error unmarshaling notifications defaults: %v", err)
		}
		ncfg.FillMissingTemplates()
		ncfg.Slack.Channels = &[]string{slackConfig.Channel}
		nitromgr := &nitroenv.Manager{
			NF: func(lf func(string, ...interface{}), notifications models.Notifications, user string) notifier.Router {
				if notifications.Slack.Channels == nil {
					// Channels isn't set, so use defaults
					notifications.Slack.Channels = ncfg.Slack.Channels
				}
				sb := &notifier.SlackBackend{
					Username: slackConfig.Username,
					IconURL:  slackConfig.IconURL,
					Users:    notifications.Slack.Users,
					Channels: *notifications.Slack.Channels,
					API:      slackapi,
				}
				if !notifications.Slack.DisableGithubUserDM {
					sluser, err := mapper.UsernameFromGithubUsername(user)
					if err != nil {
						lf("error getting slack username: %v", err)
					} else {
						sb.Users = append(sb.Users, sluser)
					}
				}
				return &notifier.MultiRouter{Backends: []notifier.Backend{sb}}
			},
			DefaultNotifications: ncfg,
			DL:                   dl,
			RC:                   rc,
			MC:                   nmc,
			NG:                   ng,
			LP:                   lp,
			FS:                   fs,
			MG:                   mg,
			CI:                   ci,
			AWSCreds:             awsCreds,
			S3Config:             s3config,
		}
		loadFailureTemplate(nitromgr)
		envspawner = &nitroenv.CombinedSpawner{
			DL:      dl,
			MG:      mg,
			Spawner: es,
			Nitro:   nitromgr,
		}
	} else {
		envspawner = es
	}
	ge := ghevent.NewGitHubEventWebhook(rc, githubConfig.HookSecret, githubConfig.TypePath, dl)

	if serverConfig.ReaperIntervalSecs > 0 {
		log.Printf("starting reaper: %v sec interval", serverConfig.ReaperIntervalSecs)
		reaper := reap.NewReaper(lp, dl, envspawner, rc, mc, serverConfig.GlobalEnvironmentLimit, logger)
		ticker := time.NewTicker(time.Duration(serverConfig.ReaperIntervalSecs) * time.Second)
		go func() {
			var delta int64
			for {
				select {
				case <-ticker.C:
					delta, _ = randomRange(21)
					time.Sleep(time.Duration(delta) * time.Second)
					reaper.Reap()
				}
			}
		}()
	} else {
		log.Printf("reaper disabled")
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM) //non-portable outside of POSIX systems
	signal.Notify(stop, os.Interrupt)

	var tlsconfig *tls.Config
	if !serverConfig.DisableTLS {
		tlsconfig = &tls.Config{
			MinVersion:   tls.VersionTLS10,
			Certificates: []tls.Certificate{serverConfig.TLSCert},
			NextProtos:   []string{"http/1.1"},
		}
	}
	addr := fmt.Sprintf("%v:%v", serverConfig.HTTPSAddr, serverConfig.HTTPSPort)
	server := &http.Server{Addr: addr, TLSConfig: tlsconfig}

	httpapi := api.NewDispatcher(server)
	apiServiceName := strings.Join([]string{datadogServiceName, "http"}, ".")
	deps := &api.Dependencies{
		DataLayer:          dl,
		GitHubEventWebhook: ge,
		EnvironmentSpawner: envspawner,
		ServerConfig:       serverConfig,
		Logger:             logger,
		DatadogServiceName: apiServiceName,
	}
	regops := []api.RegisterOption{api.WithAPIKeys(serverConfig.APIKeys)}
	if serverConfig.DebugEndpoints {
		regops = append(regops, api.WithDebugEndpoints(), api.WithIPWhitelist(serverConfig.DebugEndpointsIPWhitelists))
	}

	httpapi.RegisterVersions(deps, regops...)
	go func() {
		for _ = range stop {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			server.Shutdown(ctx)
			cancel()
		}
	}()

	stype := "HTTPS"
	if serverConfig.DisableTLS {
		stype = "HTTP"
	}

	startDatadogTracer()
	logger.Printf("%v REST listening on: %v", stype, addr)
	if serverConfig.DisableTLS {
		logger.Println(server.ListenAndServe())
	} else {
		logger.Println(server.ListenAndServeTLS("", ""))
	}
	logger.Printf("waiting for handlers to finish...")
	httpapi.WaitForHandlers()
	logger.Printf("waiting for async goroutines to finish...")
	httpapi.WaitForAsync()
	tracer.Stop()
	logger.Printf("done, terminating")
}

// Todo (mk): Remove this and all references to New Relic once we deprecate amino.
// nullNewRelicApp conforms to the newrelic.Application interface but does nothing
type nullNewRelicApp struct {
}

func (nnrapp nullNewRelicApp) StartTransaction(name string, w http.ResponseWriter, r *http.Request) newrelic.Transaction {
	return &nullNewRelicTxn{}
}

func (nnrapp nullNewRelicApp) RecordCustomEvent(eventType string, params map[string]interface{}) error {
	return nil
}

func (nnrapp nullNewRelicApp) WaitForConnection(timeout time.Duration) error {
	return nil
}

func (nnrapp nullNewRelicApp) Shutdown(timeout time.Duration) {}

// nullNewRelicTxn conforms to the newrelic.Transaction interface but does nothing
type nullNewRelicTxn struct {
}

func (nnrt nullNewRelicTxn) Header() http.Header {
	return make(http.Header)
}

func (nnrt nullNewRelicTxn) Write(b []byte) (int, error) {
	return len(b), nil
}

func (nnrt nullNewRelicTxn) WriteHeader(n int) {}

func (nnrt nullNewRelicTxn) End() error {
	return nil
}

func (nnrt nullNewRelicTxn) Ignore() error {
	return nil
}

func (nnrt nullNewRelicTxn) SetName(name string) error {
	return nil
}

func (nnrt nullNewRelicTxn) NoticeError(err error) error {
	return nil
}

func (nnrt nullNewRelicTxn) AddAttribute(key string, value interface{}) error {
	return nil
}

func (nnrt nullNewRelicTxn) StartSegmentNow() newrelic.SegmentStartTime {
	return newrelic.SegmentStartTime{}
}

// randomRange returns a random integer (using rand.Reader as the entropy source) between 0 and max
func randomRange(max int64) (int64, error) {
	maxBig := *big.NewInt(max)
	n, err := rand.Int(rand.Reader, &maxBig)
	if err != nil {
		return 0, err
	}
	return n.Int64(), nil
}
