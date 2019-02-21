package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/ghevent"
	"github.com/dollarshaveclub/acyl/pkg/locker"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/namegen"
	nitroenv "github.com/dollarshaveclub/acyl/pkg/nitro/env"
	"github.com/dollarshaveclub/acyl/pkg/nitro/images"
	"github.com/dollarshaveclub/acyl/pkg/nitro/meta"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metahelm"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metrics"
	"github.com/dollarshaveclub/acyl/pkg/nitro/notifier"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/dollarshaveclub/acyl/pkg/spawner"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"gopkg.in/src-d/go-billy.v4/osfs"
)

type integrationConfig struct {
	dataFile, webhookFile, githubToken string
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
	RootCmd.AddCommand(integrationCmd)
}

func integration(cmd *cobra.Command, args []string) {
	setupServerLogger()
	dl, err := loadData()
	if err != nil {
		clierr("error loading data: %v", err)
	}
	wm, err := loadWebhooks()
	if err != nil {
		clierr("error loading webhook: %v", err)
	}
	nmgr, rc, err := setupNitro(dl)
	if err != nil {
		clierr("error setting up Nitro: %v", err)
	}
	eh := setupEventHandler(rc, dl)
	if err := createIntegrationTest(wm["create"], eh, nmgr); err != nil {
		clierr("error performing create integration test: %v", err)
	}
	if err := updateIntegrationTest(wm["update"], eh, nmgr); err != nil {
		clierr("error performing update integration test: %v", err)
	}
	if err := deleteIntegrationTest(wm["delete"], eh, nmgr); err != nil {
		clierr("error performing delete integration test: %v", err)
	}
	logger.Printf("integration tests successful")
}

func createIntegrationTest(e *ghevent.GitHubEvent, eh *ghevent.GitHubEventWebhook, nmgr spawner.EnvironmentSpawner) error {
	d, err := json.Marshal(e)
	if err != nil {
		return errors.Wrap(err, "error marshaling event")
	}
	wh, err := eh.New(d, eh.GenerateSignatureString(d))
	action := wh.Action
	rdd := wh.RRD
	if err != nil || rdd == nil {
		return errors.Wrap(err, "error processing event")
	}
	if action != ghevent.CreateNew {
		return fmt.Errorf("unexpected event action (wanted CreateNew): %v", action.String())
	}
	name, err := nmgr.Create(context.Background(), *rdd)
	if err != nil {
		return errors.Wrap(err, "error creating environment")
	}
	logger.Printf("environment created: %v", name)
	return nil
}

func updateIntegrationTest(e *ghevent.GitHubEvent, eh *ghevent.GitHubEventWebhook, nmgr spawner.EnvironmentSpawner) error {
	d, err := json.Marshal(e)
	if err != nil {
		return errors.Wrap(err, "error marshaling event")
	}
	wh, err := eh.New(d, eh.GenerateSignatureString(d))
	action := wh.Action
	rdd := wh.RRD
	if err != nil || rdd == nil {
		return errors.Wrap(err, "error processing event")
	}
	if action != ghevent.Update {
		return fmt.Errorf("unexpected event action (wanted Update): %v", action.String())
	}
	name, err := nmgr.Update(context.Background(), *rdd)
	if err != nil {
		return errors.Wrap(err, "error updating environment")
	}
	logger.Printf("environment updated: %v", name)
	return nil
}

func deleteIntegrationTest(e *ghevent.GitHubEvent, eh *ghevent.GitHubEventWebhook, nmgr spawner.EnvironmentSpawner) error {
	d, err := json.Marshal(e)
	if err != nil {
		return errors.Wrap(err, "error marshaling event")
	}
	wh, err := eh.New(d, eh.GenerateSignatureString(d))
	action := wh.Action
	rdd := wh.RRD
	if err != nil || rdd == nil {
		return errors.Wrap(err, "error processing event")
	}
	if action != ghevent.Destroy {
		return fmt.Errorf("unexpected event action (wanted Destroy): %v", action.String())
	}
	err = nmgr.Destroy(context.Background(), *rdd, models.DestroyApiRequest)
	if err != nil {
		return errors.Wrap(err, "error destroying environment")
	}
	logger.Printf("environment destroyed")
	return nil
}

func setupEventHandler(rc ghclient.RepoClient, dl persistence.DataLayer) *ghevent.GitHubEventWebhook {
	return ghevent.NewGitHubEventWebhook(rc, "foobar", "acyl.yml", dl)
}

func setupNitro(dl persistence.DataLayer) (spawner.EnvironmentSpawner, ghclient.RepoClient, error) {
	rc := ghclient.NewGitHubClient(integrationcfg.githubToken)
	ng := &namegen.FakeNameGenerator{Unique: true}
	mc := &metrics.FakeCollector{}
	lp := &locker.FakePreemptiveLockProvider{
		ChannelFactory: func() chan struct{} {
			return make(chan struct{})
		},
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
	ci, err := metahelm.NewChartInstaller(ib, dl, fs, mc, map[string]string{}, []string{}, map[string]config.K8sSecret{}, metahelm.TillerConfig{})
	if err != nil {
		return nil, nil, errors.Wrap(err, "error getting metahelm chart installer")
	}
	return &nitroenv.Manager{
		NF: nf,
		DL: dl,
		RC: rc,
		MC: mc,
		NG: ng,
		LP: lp,
		FS: fs,
		MG: mg,
		CI: ci,
	}, rc, nil
}

type testData struct {
	QAEnvironments  []models.QAEnvironment         `json:"qa_environments"`
	K8sEnvironments []models.KubernetesEnvironment `json:"kubernetes_environments"`
	HelmReleases    []models.HelmRelease           `json:"helm_releases"`
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
	return persistence.NewPopulatedFakeDataLayer(td.QAEnvironments, td.K8sEnvironments, td.HelmReleases), nil
}

type testWebhooks struct {
	Create ghevent.GitHubEvent `json:"create"`
	Update ghevent.GitHubEvent `json:"update"`
	Delete ghevent.GitHubEvent `json:"delete"`
}

func loadWebhooks() (map[string]*ghevent.GitHubEvent, error) {
	d, err := ioutil.ReadFile(integrationcfg.webhookFile)
	if err != nil {
		return nil, errors.Wrap(err, "error opening webhook file")
	}
	twh := testWebhooks{}
	if err := json.Unmarshal(d, &twh); err != nil {
		return nil, errors.Wrap(err, "error unmarshaling webhook file")
	}
	out := make(map[string]*ghevent.GitHubEvent, 3)
	out["create"] = &twh.Create
	out["update"] = &twh.Update
	out["delete"] = &twh.Delete
	return out, nil
}
