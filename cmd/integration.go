package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"time"

	"github.com/google/go-github/github"

	"github.com/google/uuid"

	"github.com/bkeroack/acyl/pkg/ghapp"

	"github.com/bkeroack/acyl/pkg/config"
	"github.com/bkeroack/acyl/pkg/ghclient"
	"github.com/bkeroack/acyl/pkg/ghevent"
	"github.com/bkeroack/acyl/pkg/locker"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/namegen"
	nitroenv "github.com/bkeroack/acyl/pkg/nitro/env"
	"github.com/bkeroack/acyl/pkg/nitro/images"
	"github.com/bkeroack/acyl/pkg/nitro/meta"
	"github.com/bkeroack/acyl/pkg/nitro/metahelm"
	"github.com/bkeroack/acyl/pkg/nitro/metrics"
	"github.com/bkeroack/acyl/pkg/nitro/notifier"
	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/bkeroack/acyl/pkg/spawner"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"gopkg.in/src-d/go-billy.v4/osfs"
)

type integrationConfig struct {
	dataFile, webhookFile, githubToken string
	appHookSecret                      string
	PrivateKeyPEM                      string
	appIDstr                           string
}

var integrationcfg integrationConfig

// integrationCmd represents the integration command
var integrationCmd = &cobra.Command{
	Use:   "integration",
	Short: "Run a set of integration tests",
	Long: `Intended to be executed as a Kubernetes Job. Runs creation, update and deletion tests using fake implementations of the database, notifier and Furan.
Uses a mocked GitHub webhook payload. The referenced repository must exist, as well as acyl.yml and dependencies. All referenced images and tags must exist.
Must be run under a k8s service account with the ClusterAdmin role.`,
	Run: integration,
}

func init() {
	integrationCmd.Flags().StringVar(&integrationcfg.dataFile, "data-file", "testdata/integration/data.json", "path to JSON data file")
	integrationCmd.Flags().StringVar(&integrationcfg.webhookFile, "webhook-file", "testdata/integration/webhook.json", "path to JSON webhook file")
	integrationCmd.Flags().StringVar(&integrationcfg.githubToken, "github-token", os.Getenv("GITHUB_TOKEN"), "GitHub access token")
	integrationCmd.Flags().StringVar(&integrationcfg.appIDstr, "github-app-id", os.Getenv("GITHUB_APP_ID"), "GitHub App ID")
	integrationCmd.Flags().StringVar(&integrationcfg.PrivateKeyPEM, "github-app-private-key", os.Getenv("GITHUB_APP_PRIVATE_KEY"), "GitHub App private key")
	integrationCmd.Flags().StringVar(&integrationcfg.appHookSecret, "github-app-hook-secret", os.Getenv("GITHUB_APP_HOOK_SECRET"), "GitHub App webhook secret")
	RootCmd.AddCommand(integrationCmd)
}

func integration(cmd *cobra.Command, args []string) {
	setupServerLogger()
	wm, err := loadWebhooks()
	if err != nil {
		clierr("error loading webhook: %v", err)
	}
	ctx, cf := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cf()

	g, ctx := errgroup.WithContext(ctx)

	// github app
	g.Go(func() error {
		dl2, err := loadData()
		if err != nil {
			return errors.Wrap(err, "error loading data")
		}
		nmgr2, _, err := setupNitro(dl2, false)
		if err != nil {
			return errors.Wrap(err, "error setting up app Nitro")
		}
		if integrationcfg.PrivateKeyPEM == "" {
			return errors.New("empty private key")
		}
		appid, err := strconv.Atoi(integrationcfg.appIDstr)
		if err != nil || appid < 1 {
			return errors.Wrap(err, "invalid app id")
		}

		// this is only used to create valid payload signatures
		ge := ghevent.NewGitHubEventWebhook(nil, integrationcfg.appHookSecret, "", dl2)

		prh := func(ctx context.Context, action string, rrd models.RepoRevisionData) error {
			switch action {
			case "opened":
				_, err := nmgr2.Create(ctx, rrd)
				return errors.Wrap(err, "error running github app create test")
			case "synchronize":
				_, err := nmgr2.Update(ctx, rrd)
				return errors.Wrap(err, "error running github app update test")
			case "closed":
				err := nmgr2.Destroy(ctx, rrd, models.DestroyApiRequest)
				return errors.Wrap(err, "error running github app destroy test")
			default:
				return fmt.Errorf("unexpected action: %v", action)
			}
		}

		gha, err := ghapp.NewGitHubApp([]byte(integrationcfg.PrivateKeyPEM), uint(appid), integrationcfg.appHookSecret, []string{"opened", "closed", "synchronize"}, prh, dl2)
		if err != nil {
			return errors.Wrap(err, "error creating GitHub app")
		}

		runevent := func(event github.PullRequestEvent) error {
			payload, err := json.Marshal(event)
			if err != nil {
				return errors.Wrapf(err, "error marshaling %v webhook", event.Action)
			}
			r := httptest.NewRequest("POST", "/ghapp/webhook", bytes.NewBuffer(payload))
			r.Header.Add("X-GitHub-Delivery", uuid.Must(uuid.NewRandom()).String())
			r.Header.Add("X-GitHub-Event", "pull_request")
			r.Header.Add("Content-Type", "application/json")
			r.Header.Add("X-Hub-Signature", ge.GenerateSignatureString(payload))
			w := httptest.NewRecorder()
			gha.Handler().ServeHTTP(w, r)
			if w.Result().StatusCode != http.StatusAccepted {
				out, _ := ioutil.ReadAll(w.Result().Body)
				return fmt.Errorf("error in %v response: %v: %v", event.Action, w.Result().StatusCode, string(out))
			}
			return nil
		}

		if err := runevent(wm["create"]); err != nil {
			return err
		}

		if err := runevent(wm["update"]); err != nil {
			return err
		}

		if err := runevent(wm["delete"]); err != nil {
			return err
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		clierr("error running tests: %v", err)
	}

	// allow the async namespace delete to finish
	time.Sleep(5 * time.Second)

	logger.Printf("integration tests successful")
}

func setupNitro(dl persistence.DataLayer, useGHToken bool) (spawner.EnvironmentSpawner, ghclient.RepoClient, error) {
	ghtkn := "invalid"
	if useGHToken {
		ghtkn = integrationcfg.githubToken
	}
	rc := ghclient.NewGitHubClient(ghtkn)
	ng := &namegen.FakeNameGenerator{Unique: true}
	mc := &metrics.FakeCollector{}
	plf, err := locker.NewFakePreemptiveLockerFactory([]locker.LockProviderOption{
		locker.WithLockTimeout(1 * time.Second),
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "unable to create new fake preeemptive locker factory")
	}
	nf := func(lf func(string, ...interface{}), notifications models.Notifications, user string) notifier.Router {
		sb := &notifier.SlackBackend{
			Username: "john.doe",
			API:      &notifier.FakeSlackAPIClient{},
		}
		return &notifier.MultiRouter{Backends: []notifier.Backend{sb}}
	}
	fs := osfs.New("")
	mg := &meta.DataGetter{RC: rc, FS: fs}
	ib := &images.FakeImageBuilder{BatchCompletedFunc: func(envname, repo string) (bool, error) { return true, nil }}
	ci, err := metahelm.NewChartInstaller(ib, dl, fs, mc, map[string]string{}, []string{}, map[string]config.K8sSecret{}, k8sClientConfig.JWTPath, false, helmClientConfig)
	if err != nil {
		return nil, nil, errors.Wrap(err, "error getting metahelm chart installer")
	}
	return &nitroenv.Manager{
		NF:  nf,
		DL:  dl,
		RC:  rc,
		MC:  mc,
		NG:  ng,
		FS:  fs,
		MG:  mg,
		CI:  ci,
		PLF: plf,
	}, rc, nil
}

type testData struct {
	QAEnvironments  []models.QAEnvironment         `json:"qa_environments"`
	K8sEnvironments []models.KubernetesEnvironment `json:"kubernetes_environments"`
	HelmReleases    []models.HelmRelease           `json:"helm_releases"`
	APIKeys         []models.APIKey                `json:"api_keys"`
}

func loadData() (persistence.DataLayer, error) {
	d, err := ioutil.ReadFile(integrationcfg.dataFile)
	if err != nil {
		return nil, errors.Wrap(err, "error opening data file")
	}
	td := testData{}
	if err := json.Unmarshal(d, &td); err != nil {
		return nil, errors.Wrap(err, "error unmarshaling data file")
	}
	return persistence.NewPopulatedFakeDataLayer(td.QAEnvironments, td.K8sEnvironments, td.HelmReleases, td.APIKeys), nil
}

type testWebhooks struct {
	Create github.PullRequestEvent `json:"create"`
	Update github.PullRequestEvent `json:"update"`
	Delete github.PullRequestEvent `json:"delete"`
}

func loadWebhooks() (map[string]github.PullRequestEvent, error) {
	d, err := ioutil.ReadFile(integrationcfg.webhookFile)
	if err != nil {
		return nil, errors.Wrap(err, "error opening webhook file")
	}
	twh := testWebhooks{}
	if err := json.Unmarshal(d, &twh); err != nil {
		return nil, errors.Wrap(err, "error unmarshaling webhook file")
	}
	out := make(map[string]github.PullRequestEvent, 3)
	out["create"] = twh.Create
	out["update"] = twh.Update
	out["delete"] = twh.Delete
	return out, nil
}
