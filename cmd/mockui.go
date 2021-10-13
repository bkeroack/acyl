// +build linux darwin freebsd netbsd openbsd

package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/nitro/metahelm"
	lorem "github.com/dollarshaveclub/acyl/pkg/persistence/golorem"
	"github.com/dollarshaveclub/acyl/pkg/spawner"
	"github.com/dollarshaveclub/furan/v2/pkg/generated/furanrpc"
	guuid "github.com/gofrs/uuid"
	"github.com/google/uuid"
	"github.com/lib/pq"
	v1 "k8s.io/api/core/v1"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/persistence"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/spf13/cobra"

	"github.com/dollarshaveclub/acyl/pkg/api"
	mh "github.com/dollarshaveclub/metahelm/pkg/metahelm"
)

// serverCmd represents the server command
var mockuiCmd = &cobra.Command{
	Use:   "mockui",
	Short: "Run a mock UI server",
	Long:  `Run a mock UI HTTP server for UI development/testing`,
	Run:   mockui,
}

var listenAddr string

func init() {
	mockuiCmd.PersistentFlags().StringVar(&listenAddr, "listen-addr", "localhost:4000", "Listen address")
	addUIFlags(mockuiCmd)
	RootCmd.AddCommand(mockuiCmd)
}

func mockEvents(fdl *persistence.FakeDataLayer, qae []models.QAEnvironment) {
	// add some mock events
	if len(qae) == 0 {
		return
	}
	env := qae[0]
	id := fdl.NewFakeEvent(env.Created.Add(1*time.Hour), env.Repo, env.User, env.Name, models.UpdateEvent, true)
	log.Printf("creating fake update event for %v: %v", env.Name, id)
	id = fdl.NewFakeEvent(env.Created.Add(2*time.Hour), env.Repo, env.User, env.Name, models.UpdateEvent, false)
	log.Printf("creating fake update event for %v (failure): %v", env.Name, id)
	mockFailedEventStatus(fdl, id)
	id = fdl.NewFakeEvent(env.Created.Add(3*time.Hour), env.Repo, env.User, env.Name, models.DestroyEvent, true)
	log.Printf("creating fake destroy event for %v: %v", env.Name, id)
}

func mockFailedEventStatus(fdl *persistence.FakeDataLayer, id uuid.UUID) {
	contStarted := false
	failedPod := mh.FailedPod{
		Name:    "foo-pod-name",
		Phase:   "foo-pod-phase",
		Message: "foo-pod-message",
		Reason:  "foo-pod-reason",
		Conditions: []v1.PodCondition{
			v1.PodCondition{
				Type:               v1.PodConditionType("foo-pod-condition-type"),
				Status:             v1.ConditionStatus("foo-pod-condition-status"),
				LastProbeTime:      meta.Now(),
				LastTransitionTime: meta.Now(),
				Reason:             "foo-pod-condition-reason",
				Message:            "foo-pod-condition-message",
			},
		},
		ContainerStatuses: []v1.ContainerStatus{
			v1.ContainerStatus{
				Name: "foo-container-name",
				State: v1.ContainerState{
					Waiting: &v1.ContainerStateWaiting{
						Reason:  "foo-container-state-reason",
						Message: "foo-container-state-message",
					},
				},
				Ready:        false,
				RestartCount: 7,
				Image:        "foo-container-image",
				ImageID:      "foo-container-image-id",
				Started:      &contStarted,
			},
		},
		Logs: map[string][]byte{"foo-container-name": []byte(
			fmt.Sprintf("%v\n%v\n%v\n%v\n%v\n",
				fmt.Sprintf("%v: %v", time.Now().UTC().Format(time.RFC822), lorem.Sentence(5, 10)),
				fmt.Sprintf("%v: %v", time.Now().UTC().Format(time.RFC822), lorem.Sentence(5, 10)),
				fmt.Sprintf("%v: %v", time.Now().UTC().Format(time.RFC822), lorem.Sentence(5, 10)),
				fmt.Sprintf("%v: %v", time.Now().UTC().Format(time.RFC822), lorem.Sentence(5, 10)),
				fmt.Sprintf("%v: %v", time.Now().UTC().Format(time.RFC822), lorem.Sentence(5, 10)),
			),
		)},
	}
	err := fdl.SetEventStatusFailed(id, mh.ChartError{
		HelmError:       errors.New("foo-helm-error"),
		HelmErrorString: "foo-helm-error-string",
		Level:           uint(1),
		FailedDaemonSets: map[string][]mh.FailedPod{
			"foo-failed-daemon-sets": {failedPod},
		},
		FailedDeployments: map[string][]mh.FailedPod{
			"foo-failed-deployments": {failedPod},
		},
		FailedJobs: map[string][]mh.FailedPod{
			"foo-failed-jobs": {failedPod},
		},
	})
	if err != nil {
		log.Fatal("SetEventStatusFailed error: ", err)
	}
}

func loadMockData(fpath string) *persistence.FakeDataLayer {
	f, err := os.Open(fpath)
	if err != nil {
		log.Fatalf("error opening mock data file: %v", err)
	}
	defer f.Close()
	td := testData{}
	if err := json.NewDecoder(f).Decode(&td); err != nil {
		log.Fatalf("error unmarshaling mock data file: %v", err)
	}
	now := time.Now().UTC()
	for i := range td.QAEnvironments {
		td.QAEnvironments[i].Created = now.AddDate(0, 0, -(i * 3))
	}
	for i := range td.K8sEnvironments {
		td.K8sEnvironments[i].Created = now.AddDate(0, 0, -(i * 3))
		td.K8sEnvironments[i].Updated.Time = td.K8sEnvironments[i].Created.Add(1 * time.Hour)
		td.K8sEnvironments[i].Updated.Valid = true
	}
	for i := range td.HelmReleases {
		td.HelmReleases[i].Created = now.AddDate(0, 0, -(i * 3))
	}
	for i := range td.APIKeys {
		td.APIKeys[i].Created = now.AddDate(0, 0, -(i * 3))
		if td.APIKeys[i].LastUsed.Valid {
			td.APIKeys[i].LastUsed = pq.NullTime{
				Time:  td.APIKeys[i].Created.Add(1 * time.Hour),
				Valid: true,
			}
		}
		log.Printf("updated time for fake api keys for user: %v, description: %v: permission level: %v created: %v last used: %v", td.APIKeys[i].GitHubUser, td.APIKeys[i].Description, td.APIKeys[i].PermissionLevel, td.APIKeys[i].Created, td.APIKeys[i].LastUsed)
	}
	fdl := persistence.NewPopulatedFakeDataLayer(td.QAEnvironments, td.K8sEnvironments, td.HelmReleases, td.APIKeys)
	for _, qae := range td.QAEnvironments {
		log.Printf("creating fake create event for env: %v, repo: %v, user: %v: %v", qae.Name, qae.Repo, qae.User, fdl.NewFakeCreateEvent(qae.Created, qae.Repo, qae.User, qae.Name))
	}
	mockEvents(fdl, td.QAEnvironments)
	return fdl
}

// randomPEMKey generates a random RSA key in PEM format
func randomPEMKey() []byte {
	reader := rand.Reader
	key, err := rsa.GenerateKey(reader, 2048)
	if err != nil {
		log.Fatalf("error generating random PEM key: %v", err)
	}

	var privateKey = &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}

	out := &bytes.Buffer{}
	if err := pem.Encode(out, privateKey); err != nil {
		log.Fatalf("error encoding PEM key: %v", err)
	}
	return out.Bytes()
}

func setDummyGHConfig() {
	githubConfig.OAuth.Enforce = true // using dummy session user
	githubConfig.PrivateKeyPEM = randomPEMKey()
	githubConfig.AppID = 1
	githubConfig.AppHookSecret = "asdf"
	copy(githubConfig.OAuth.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
}

type fakeFuran2Client struct {
	sync.RWMutex
	builds map[guuid.UUID][]string
}

func (fc *fakeFuran2Client) GetBuildEvents(ctx context.Context, id guuid.UUID) (*furanrpc.BuildEventsResponse, error) {
	if fc.builds == nil {
		fc.Lock()
		fc.builds = make(map[guuid.UUID][]string)
		fc.Unlock()
	}
	fc.RLock()
	events := fc.builds[id]
	fc.RUnlock()
	events = append(events, "foo", "bar", "baz")
	fc.Lock()
	fc.builds[id] = events
	ev := make([]string, len(events))
	copy(ev, events)
	fc.Unlock()
	return &furanrpc.BuildEventsResponse{
		BuildId:      id.String(),
		CurrentState: furanrpc.BuildState_SUCCESS,
		Messages:     ev,
	}, nil
}

func (fc *fakeFuran2Client) GetBuildStatus(ctx context.Context, id guuid.UUID) (*furanrpc.BuildStatusResponse, error) {
	now := time.Now().UTC()
	done := now.Add((3 * time.Minute) + (36 * time.Second))
	ts := &furanrpc.Timestamp{
		Seconds: now.Unix(),
		Nanos:   int32(now.Nanosecond()),
	}
	ts2 := &furanrpc.Timestamp{
		Seconds: done.Unix(),
		Nanos:   int32(done.Nanosecond()),
	}
	return &furanrpc.BuildStatusResponse{
		BuildId:      id.String(),
		BuildRequest: &furanrpc.BuildRequest{},
		State:        furanrpc.BuildState_SUCCESS,
		Started:      ts,
		Completed:    ts2,
	}, nil
}

func (fc *fakeFuran2Client) Close() {}

func mockui(cmd *cobra.Command, args []string) {

	logger := log.New(os.Stderr, "", log.LstdFlags)

	server := &http.Server{Addr: listenAddr}

	httpapi := api.NewDispatcher(server)
	var dl *persistence.FakeDataLayer
	if mockDataFile != "" {
		dl = loadMockData(mockDataFile)
	} else {
		dl = persistence.NewFakeDataLayer()
	}
	dl.CreateMissingEventLog = true
	uf := func(ctx context.Context, rd models.RepoRevisionData) (string, error) {
		return "updated environment", nil
	}

	deps := &api.Dependencies{
		DataLayer:          dl,
		ServerConfig:       serverConfig,
		Logger:             logger,
		EnvironmentSpawner: &spawner.FakeEnvironmentSpawner{UpdateFunc: uf},
		KubernetesReporter: metahelm.FakeKubernetesReporter{FakePodLogFilePath: "pkg/nitro/metahelm/testdata/pod_logs.log"},
		Furan2Client:       &fakeFuran2Client{},
	}

	serverConfig.UIBaseURL = "http://" + listenAddr

	var branding config.UIBrandingConfig
	if err := json.Unmarshal([]byte(serverConfig.UIBrandingJSON), &branding); err != nil {
		log.Fatalf("error unmarshaling branding config: %v", err)
	}

	setDummyGHConfig()

	httpapi.AppGHClientFactoryFunc = func(_ string) ghclient.GitHubAppInstallationClient {
		return &ghclient.FakeRepoClient{
			GetUserAppRepoPermissionsFunc: func(_ context.Context, instID int64) (map[string]ghclient.AppRepoPermissions, error) {
				out := make(map[string]ghclient.AppRepoPermissions, len(mockRepos))
				for _, r := range mockRepos {
					if readOnly {
						out[r] = ghclient.AppRepoPermissions{
							Repo: r,
							Pull: true,
						}
					} else {
						out[r] = ghclient.AppRepoPermissions{
							Repo: r,
							Pull: true,
							Push: true,
						}
					}
				}
				return out, nil
			},
		}
	}

	if err := httpapi.RegisterVersions(deps,
		api.WithGitHubConfig(githubConfig),
		api.WithUIBaseURL(serverConfig.UIBaseURL),
		api.WithUIAssetsPath(serverConfig.UIPath),
		api.WithUIRoutePrefix(serverConfig.UIBaseRoute),
		api.WithUIReload(),
		api.WithUIBranding(branding),
		api.WithUIDummySessionUser(mockUser)); err != nil {
		log.Fatalf("error registering api versions: %v", err)
	}

	go func() {
		logger.Printf("listening on: %v", listenAddr)
		logger.Println(server.ListenAndServe())
	}()

	opencmd := fmt.Sprintf("%v http://%v/ui/event/status?id=%v", openPath, listenAddr, uuid.Must(uuid.NewRandom()))
	shellsl := strings.Split(shell, " ")
	cmdsl := append(shellsl, opencmd)
	c := exec.Command(cmdsl[0], cmdsl[1:]...)
	if out, err := c.CombinedOutput(); err != nil {
		log.Fatalf("error opening UI browser: %v: %v: %v", strings.Join(cmdsl, " "), string(out), err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	output.Green().Progress().Println("Keeping UI server running (ctrl-c to exit)...")
	<-done
	if server != nil {
		server.Shutdown(context.Background())
	}
}
