package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/nitro/metahelm"
	"github.com/dollarshaveclub/acyl/pkg/spawner"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/lib/pq"
	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/testhelper/testdatalayer"
)

func TestAPIv2SearchByTrackingRef(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo", "bar", "baz"}}
	apiv2, err := newV2API(dl, nil, nil, nil, sc, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v2/envs/_search?repo=dollarshaveclub%2Fbiz-baz&tracking_ref=master", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, bb)
	}
	res := []v2QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
	if res[0].Name != "biz-biz2" {
		t.Fatalf("bad qa name: %v", res[0].Name)
	}
}

func TestAPIv2SearchByTrackingRefUserAPIKey(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo", "bar", "baz"}}
	apiv2, err := newV2API(dl, nil, nil, nil, sc, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	user := "joshritter"
	id, err := dl.CreateAPIKey(context.Background(), models.ReadOnlyPermission, "foo-description", user)
	if err != nil {
		t.Fatalf("api key creation should have succeeded")
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v2/envs/_search?repo=dollarshaveclub%2Fbiz-baz&tracking_ref=master&user="+user, nil)
	req.Header.Set(apiKeyHeader, id.String())
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, bb)
	}
	res := []v2QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
	if res[0].Name != "biz-biz2" {
		t.Fatalf("bad qa name: %v", res[0].Name)
	}
}

func TestAPIv2SearchByTrackingRefUserAPIKeyEmpty(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo", "bar", "baz"}}
	apiv2, err := newV2API(dl, nil, nil, nil, sc, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	id, err := dl.CreateAPIKey(context.Background(), models.ReadOnlyPermission, "foo-description", "new-user")
	if err != nil {
		t.Fatalf("api key creation should have succeeded")
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v2/envs/_search?repo=dollarshaveclub%2Fbiz-baz&tracking_ref=master", nil)
	req.Header.Set(apiKeyHeader, id.String())
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, bb)
	}
	res := []v2QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv2EnvDetails(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo", "bar", "baz"}}
	apiv2, err := newV2API(dl, nil, nil, nil, sc, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v2/envs/biz-biz2", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, bb)
	}
	res := v2QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if res.SourceRef != "master" {
		t.Fatalf("bad source ref: %v", res.SourceRef)
	}
}

func TestAPIv2EnvDetailsUserAPIKey(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo", "bar", "baz"}}
	apiv2, err := newV2API(dl, nil, nil, nil, sc, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	id, err := dl.CreateAPIKey(context.Background(), models.ReadOnlyPermission, "foo-description", "joshritter")
	if err != nil {
		t.Fatalf("api key creation should have succeeded")
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v2/envs/biz-biz2", nil)
	req.Header.Set(apiKeyHeader, id.String())
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, bb)
	}
	res := v2QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if res.SourceRef != "master" {
		t.Fatalf("bad source ref: %v", res.SourceRef)
	}
}

func TestAPIv2EnvDetailsUserAPIKeyForbidden(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo", "bar", "baz"}}
	apiv2, err := newV2API(dl, nil, nil, nil, sc, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	id, err := dl.CreateAPIKey(context.Background(), models.ReadOnlyPermission, "foo-description", "foo-user")
	if err != nil {
		t.Fatalf("api key creation should have succeeded")
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v2/envs/biz-biz2", nil)
	req.Header.Set(apiKeyHeader, id.String())
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("should have failed: %v: %v", resp.StatusCode, bb)
	}
}

func TestAPIv2EnvDetailsUserAPIKeyUnauthorized(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo", "bar", "baz"}}
	apiv2, err := newV2API(dl, nil, nil, nil, sc, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v2/envs/biz-biz2", nil)
	unauthorizedId, err := uuid.NewRandom()
	if err != nil {
		t.Fatalf("error creating new random uuid: %v", err)
	}
	req.Header.Set(apiKeyHeader, unauthorizedId.String())
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("should have failed: %v: %v", resp.StatusCode, bb)
	}
}

func TestAPIv2HealthCheck(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	authMiddleware.apiKeys = []string{"foo"}

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/v2/health-check", nil)
	req.Header.Set(apiKeyHeader, "foo")

	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}

	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, bb)
	}
	msg := map[string]string{}
	err = json.Unmarshal(bb, &msg)
	if err != nil {
		t.Fatalf("error unmarshalling health check response: %v\n", err)
	}
	if msg["message"] != "Todo es bueno!" {
		t.Fatalf("Incorrect health check response")
	}
}

func TestAPIv2EventLog(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo", "bar", "baz"}}
	apiv2, err := newV2API(dl, nil, nil, nil, sc, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v2/eventlog/db20d1e7-1e0d-45c6-bfe1-4ea24b7f0000", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("should have 404ed: %v", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ = http.NewRequest("GET", ts.URL+"/v2/eventlog/asdf", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	resp, err = hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request 2: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("should have been a 400: %v", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ = http.NewRequest("GET", ts.URL+"/v2/eventlog/9beb4f55-bc47-4411-b17d-78e2c0bccb25", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	resp, err = hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request 3: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, string(bb))
	}
	res := v2EventLog{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if res.EnvName != "foo-bar" {
		t.Fatalf("unexpected env name: %v", res.EnvName)
	}
}

func TestAPIv2EventStatus(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, OAuthConfig{}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/v2/event/c1e1e229-86d8-4d99-a3d5-62b2f6390bbe/status", nil)

	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request 3: %v", err)
	}

	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, string(bb))
	}
	res := V2EventStatusSummary{}
	fmt.Printf("res: %v\n", string(bb))
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if res.Config.Type != "create" {
		t.Fatalf("bad type: %v", res.Config.Type)
	}
	if res.Config.Status != "pending" {
		t.Fatalf("bad status: %v", res.Config.Status)
	}
	if res.Config.TriggeringRepo != "acme/somethingelse" {
		t.Fatalf("bad repo: %v", res.Config.TriggeringRepo)
	}
	if res.Config.EnvName != "asdf-asdf" {
		t.Fatalf("bad env name: %v", res.Config.EnvName)
	}
	if res.Config.PullRequest != 2 {
		t.Fatalf("bad pr: %v", res.Config.PullRequest)
	}
	if res.Config.GitHubUser != "john.smith" {
		t.Fatalf("bad user: %v", res.Config.GitHubUser)
	}
	if res.Config.Branch != "feature-foo" {
		t.Fatalf("bad branch: %v", res.Config.Branch)
	}
	if res.Config.Revision != "asdf1234" {
		t.Fatalf("bad revision: %v", res.Config.Revision)
	}
	if n := len(res.Tree); n != 1 {
		t.Fatalf("bad tree: %+v", res.Tree)
	}
	if rsd := res.Config.RenderedStatus.Description; rsd != "something happened" {
		t.Fatalf("bad rendered description: %+v", rsd)
	}
	if rsl := res.Config.RenderedStatus.LinkTargetURL; rsl != "https://foobar.com" {
		t.Fatalf("bad rendered link url: %+v", rsl)
	}
}

func TestAPIv2UserEnvs(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, OAuthConfig{Enforce: true}, testlogger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	req, _ := http.NewRequest("GET", "https://foo.com/v2/userenvs?history=48h", nil)
	req = req.Clone(withSession(req.Context(), uis))

	rc := httptest.NewRecorder()

	apiv2.userEnvsHandler(rc, req)

	res := rc.Result()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}

	out := []V2UserEnv{}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("error decoding response: %v", err)
	}
	res.Body.Close()

	if len(out) != 1 {
		t.Fatalf("expected 1, got %v", len(out))
	}
}

func TestAPIv2UserEnvDetail(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	logger := log.New(os.Stdout, "", log.LstdFlags)

	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)
	req, _ := http.NewRequest("GET", "https://foo.com/v2/userenv/foo-bar", nil)
	req = mux.SetURLVars(req, map[string]string{"name": "foo-bar"})
	req = req.Clone(withSession(req.Context(), uis))

	rc := httptest.NewRecorder()

	apiv2.userEnvDetailHandler(rc, req)

	res := rc.Result()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}

	out := V2EnvDetail{}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("error decoding response: %v", err)
	}
	res.Body.Close()
	if out.EnvName != "foo-bar" {
		t.Fatalf("bad name: %v", out.EnvName)
	}
}

func TestAPIv2UserEnvActionsRebuild(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	k8senv := &models.KubernetesEnvironment{
		Created: time.Now(),
		Updated: pq.NullTime{
			Time:  time.Now(),
			Valid: true,
		},
		EnvName:         "foo-bar",
		Namespace:       "nitro-1234-foo-bar",
		ConfigSignature: []byte("0f0o0o0b0a0r00000000000000000000"),
	}
	dl.CreateK8sEnv(context.Background(), k8senv)

	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
							Push: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	uf := func(ctx context.Context, rd models.RepoRevisionData) (string, error) {
		return "updated environment", nil
	}
	apiv2, err := newV2API(dl, nil, &spawner.FakeEnvironmentSpawner{UpdateFunc: uf}, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, nil)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)
	req, _ := http.NewRequest("POST", "https://foo.com/v2/userenvs/foo-bar/actions/rebuild", nil)
	req = mux.SetURLVars(req, map[string]string{"name": "foo-bar", "full": "false"})
	req = req.Clone(withSession(req.Context(), uis))

	rc := httptest.NewRecorder()
	apiv2.userEnvActionsRebuildHandler(rc, req)
	res := rc.Result()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
}

func TestAPIv2UserEnvNamePods(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	k8senv := &models.KubernetesEnvironment{
		Created: time.Now(),
		Updated: pq.NullTime{
			Time:  time.Now(),
			Valid: true,
		},
		EnvName:         "foo-bar",
		Namespace:       "nitro-1234-foo-bar",
		ConfigSignature: []byte("0f0o0o0b0a0r00000000000000000000"),
	}
	dl.CreateK8sEnv(context.Background(), k8senv)
	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, metahelm.FakeKubernetesReporter{})
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)
	req, _ := http.NewRequest("GET", "https://foo.com/v2/userenvs/foo-bar/namespace/pods", nil)
	req = mux.SetURLVars(req, map[string]string{"name": "foo-bar"})
	req = req.Clone(withSession(req.Context(), uis))

	rc := httptest.NewRecorder()
	apiv2.userEnvNamePodsHandler(rc, req)
	res := rc.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
	out := []V2EnvNamePods{}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("error decoding response: %v", err)
	}
	res.Body.Close()

	if len(out) != 2 {
		t.Fatalf("expected 2, got %v", len(out))
	}
	if out[0].Ready != "2/2" && out[0].Status != "Running" {
		t.Fatalf("expected Ready: 2/2 & Status: Running, got %v, %v", out[0].Ready, out[0].Status)
	}
	if out[1].Ready != "1/2" && out[1].Status != "Pending" {
		t.Fatalf("expected Ready: 1/2 & Status: Running, got %v, %v", out[1].Ready, out[1].Status)
	}
}

func TestAPIv2UserEnvNamePodContainers(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	k8senv := &models.KubernetesEnvironment{
		Created: time.Now(),
		Updated: pq.NullTime{
			Time:  time.Now(),
			Valid: true,
		},
		EnvName:         "foo-bar",
		Namespace:       "nitro-1234-foo-bar",
		ConfigSignature: []byte("0f0o0o0b0a0r00000000000000000000"),
	}
	dl.CreateK8sEnv(context.Background(), k8senv)
	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, metahelm.FakeKubernetesReporter{})
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)

	// test missing pod_name
	req, _ := http.NewRequest("GET", "https://foo.com/v2/userenvs/foo-bar/namespace//containers", nil)
	req = mux.SetURLVars(req, map[string]string{"name": "foo-bar"})
	req = req.Clone(withSession(req.Context(), uis))

	rc := httptest.NewRecorder()
	apiv2.userEnvPodContainersHandler(rc, req)
	res := rc.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}

	// test with pod_name
	podName := "foo-app-abc123"
	req, _ = http.NewRequest("GET", fmt.Sprintf("https://foo.com/v2/userenvs/foo-bar/namespace/%v/containers", podName), nil)
	req = mux.SetURLVars(req, map[string]string{"name": "foo-bar", "pod": podName})
	req = req.Clone(withSession(req.Context(), uis))

	rc = httptest.NewRecorder()
	apiv2.userEnvPodContainersHandler(rc, req)
	res = rc.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
	out := V2EnvNamePodContainers{}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("error decoding response: %v", err)
	}
	res.Body.Close()
	if out.Name != podName {
		t.Fatalf("expected pod & containers for podname")
	}
	if len(out.Containers) != 2 {
		t.Fatalf("expected 2 containers for pod, got: %v", len(out.Containers))
	}
}

func TestAPIv2UserEnvNamePodLogs(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	k8senv := &models.KubernetesEnvironment{
		Created: time.Now(),
		Updated: pq.NullTime{
			Time:  time.Now(),
			Valid: true,
		},
		EnvName:         "foo-bar",
		Namespace:       "nitro-1234-foo-bar",
		ConfigSignature: []byte("0f0o0o0b0a0r00000000000000000000"),
	}
	dl.CreateK8sEnv(context.Background(), k8senv)
	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, metahelm.FakeKubernetesReporter{FakePodLogFilePath: "../nitro/metahelm/testdata/pod_logs.log"})
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)

	// test 1000 line request maximum
	nLogLines := metahelm.MaxPodContainerLogLines + 1
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://foo.com/v2/userenvs/foo-bar/namespace/foo-bar-abc123/logs?lines=%v", nLogLines), nil)
	req = mux.SetURLVars(req, map[string]string{"name": "foo-bar", "pod": "foo-bar-abc123"})
	req = req.Clone(withSession(req.Context(), uis))

	rc := httptest.NewRecorder()
	apiv2.userEnvPodLogsHandler(rc, req)
	res := rc.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
	res.Body.Close()

	// test valid line request
	nLogLines = 777
	req, _ = http.NewRequest("GET", fmt.Sprintf("https://foo.com/v2/userenvs/foo-bar/namespace/foo-bar-abc123/logs?lines=%v", nLogLines), nil)
	req = mux.SetURLVars(req, map[string]string{"name": "foo-bar", "pod": "foo-bar-abc123"})
	req = req.Clone(withSession(req.Context(), uis))

	rc = httptest.NewRecorder()
	apiv2.userEnvPodLogsHandler(rc, req)
	res = rc.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
	defer res.Body.Close()
	buf := make([]byte, 68*1024)
	_, err = res.Body.Read(buf)
	if err != nil {
		t.Fatalf("error reading pod logs: %v", err)
	}
	br := bytes.NewReader(buf)
	scanner := bufio.NewScanner(br)
	scanner.Split(bufio.ScanLines)
	count := 0
	for scanner.Scan() {
		if count == int(nLogLines) {
			break
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("error lines returned exceeded expected %v, actual %v", nLogLines, count)
	}
}

func TestAPIv2UserTokenCreate(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	logger := log.New(os.Stdout, "", log.LstdFlags)
	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserFunc: func(_ context.Context) (string, error) {
					return "bobsmith", nil
				},
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, metahelm.FakeKubernetesReporter{})
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)
	apikey := models.APIKey{
		GitHubUser:      uis.GitHubUser,
		PermissionLevel: models.ReadOnlyPermission,
		Description:     "my token description",
	}
	body := fmt.Sprintf("{\"permission\":%v,\"description\":\"%v\"}", 7, apikey.Description)
	req, _ := http.NewRequest("POST", fmt.Sprintf("https://foo.com/v2/user/token"), bytes.NewReader([]byte(body)))
	req = req.Clone(withSession(req.Context(), uis))
	rc := httptest.NewRecorder()
	apiv2.apiKeyCreateHandler(rc, req)
	res := rc.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
	body = fmt.Sprintf("{\"permission\":%v,\"description\":\"%v\"}", int(apikey.PermissionLevel), apikey.Description)
	req, _ = http.NewRequest("POST", fmt.Sprintf("https://foo.com/v2/user/token"), bytes.NewReader([]byte(body)))
	req = req.Clone(withSession(req.Context(), uis))
	rc = httptest.NewRecorder()
	apiv2.apiKeyCreateHandler(rc, req)
	res = rc.Result()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
	out := V2UserAPIKeyResponse{}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("error decoding response: %v", err)
	}
	res.Body.Close()
	if out.Token == uuid.Nil {
		t.Fatalf("expected valid token")
	}
	if out.User != apikey.GitHubUser {
		t.Fatalf("expected user to match")
	}
	aks, err := dl.GetAPIKeysByGithubUser(context.Background(), out.User)
	if err != nil || aks == nil {
		t.Fatalf("expected api keys returned for new user")
	}
	if !aks[0].LastUsed.Valid && !time.Time.IsZero(aks[0].LastUsed.Time) {
		t.Fatalf("expected last used to not be set for newly created key")
	}
}

func TestAPIv2UserTokenCreateAdminDenied(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	logger := log.New(os.Stdout, "", log.LstdFlags)
	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserFunc: func(_ context.Context) (string, error) {
					return "bobsmith", nil
				},
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, metahelm.FakeKubernetesReporter{})
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)
	apikey := models.APIKey{
		GitHubUser:      uis.GitHubUser,
		PermissionLevel: models.AdminPermission,
		Description:     "my token description",
	}
	body := fmt.Sprintf("{\"permission\":%v,\"description\":\"%v\"}", int(apikey.PermissionLevel), apikey.Description)
	req, _ := http.NewRequest("POST", fmt.Sprintf("https://foo.com/v2/user/token"), bytes.NewReader([]byte(body)))
	req = req.Clone(withSession(req.Context(), uis))
	rc := httptest.NewRecorder()
	apiv2.apiKeyCreateHandler(rc, req)
	res := rc.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
}

func TestAPIv2UserTokenCreateLimit(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	logger := log.New(os.Stdout, "", log.LstdFlags)
	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserFunc: func(_ context.Context) (string, error) {
					return "bobsmith", nil
				},
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, metahelm.FakeKubernetesReporter{})
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)
	apikeys := []models.APIKey{
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.ReadOnlyPermission,
			Description:     "my write token description",
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.WritePermission,
			Description:     "my write token description",
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.WritePermission,
			Description:     "my write token description",
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.ReadOnlyPermission,
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.ReadOnlyPermission,
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.WritePermission,
			Description:     "my write token description",
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.WritePermission,
			Description:     "my write token description",
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.ReadOnlyPermission,
			Description:     "my read token description",
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.WritePermission,
			Description:     "my write token description",
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.ReadOnlyPermission,
			Description:     "my read token description",
		},
	}
	for _, apikey := range apikeys {
		body := fmt.Sprintf("{\"permission\":%v,\"description\":\"%v\"}", int(apikey.PermissionLevel), apikey.Description)
		req, _ := http.NewRequest("POST", fmt.Sprintf("https://foo.com/v2/user/token"), bytes.NewReader([]byte(body)))
		req = req.Clone(withSession(req.Context(), uis))
		rc := httptest.NewRecorder()
		apiv2.apiKeyCreateHandler(rc, req)
		res := rc.Result()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("bad status code: %v", res.StatusCode)
		}
		out := V2UserAPIKeyResponse{}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("error decoding response: %v", err)
		}
		res.Body.Close()
		if out.Token == uuid.Nil {
			t.Fatalf("expected valid token")
		}
		if out.User != apikey.GitHubUser {
			t.Fatalf("expected user to match")
		}
	}
	apikey := models.APIKey{
		GitHubUser:      uis.GitHubUser,
		PermissionLevel: models.ReadOnlyPermission,
		Description:     "my token description",
	}
	body := fmt.Sprintf("{\"permission\":%v,\"description\":\"%v\"}", int(apikey.PermissionLevel), apikey.Description)
	req, _ := http.NewRequest("POST", fmt.Sprintf("https://foo.com/v2/user/token"), bytes.NewReader([]byte(body)))
	req = req.Clone(withSession(req.Context(), uis))
	rc := httptest.NewRecorder()
	apiv2.apiKeyCreateHandler(rc, req)
	res := rc.Result()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
}

func TestAPIv2UserTokens(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	logger := log.New(os.Stdout, "", log.LstdFlags)
	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserFunc: func(_ context.Context) (string, error) {
					return "bobsmith", nil
				},
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, metahelm.FakeKubernetesReporter{})
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "jimjackson",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)
	apikeys := []models.APIKey{
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.ReadOnlyPermission,
			Description:     "my write token description",
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.WritePermission,
			Description:     "my write token description",
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.WritePermission,
		},
		models.APIKey{
			GitHubUser:      uis.GitHubUser,
			PermissionLevel: models.ReadOnlyPermission,
		},
	}
	ids := []uuid.UUID{}
	for _, ak := range apikeys {
		body := fmt.Sprintf("{\"permission\":%v,\"description\":\"%v\"}", int(ak.PermissionLevel), ak.Description)
		req, _ := http.NewRequest("POST", fmt.Sprintf("https://foo.com/v2/user/token"), bytes.NewReader([]byte(body)))
		req = req.Clone(withSession(req.Context(), uis))
		rc := httptest.NewRecorder()
		apiv2.apiKeyCreateHandler(rc, req)
		res := rc.Result()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("bad status code: %v", res.StatusCode)
		}
		out := V2UserAPIKeyResponse{}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("error decoding response: %v", err)
		}
		res.Body.Close()
		if out.Token == uuid.Nil {
			t.Fatalf("expected valid token")
		}
		if out.User != ak.GitHubUser {
			t.Fatalf("expected user to match")
		}
		ids = append(ids, out.Token)
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://foo.com/v2/user/tokens"), nil)
	req = req.Clone(withSession(req.Context(), uis))
	rc := httptest.NewRecorder()
	apiv2.apiKeysHandler(rc, req)
	res := rc.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
	out := []V2UserAPIKeyData{}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("error decoding response: %v", err)
	}
	if len(out) != len(apikeys) {
		t.Fatalf("error incorrect number of keys returned; expected: %v, got %v", len(apikeys), len(out))
	}
	res.Body.Close()
	for n, tk := range out {
		if tk.User != apikeys[n].GitHubUser {
			t.Fatalf("expected user to match; expected: %v, got: %v", apikeys[n].GitHubUser, tk.User)
		}
		if tk.Description != apikeys[n].Description {
			t.Fatalf("expected description to match; expected: %v, got: %v", apikeys[n].Description, tk.Description)
		}
		if tk.Permission != apikeys[n].PermissionLevel {
			t.Fatalf("expected permissions to match; expected: %v, got: %v", apikeys[n].PermissionLevel, tk.Permission)
		}
		if !tk.LastUsed.Valid && !time.Time.IsZero(tk.LastUsed.Time) {
			t.Fatalf("expected last used to be zero value; got: %v", tk.LastUsed.Time)
		}
	}
}

func TestAPIv2UserTokenDestroy(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	logger := log.New(os.Stdout, "", log.LstdFlags)
	oauthcfg := OAuthConfig{
		AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
			return &ghclient.FakeRepoClient{
				GetUserFunc: func(_ context.Context) (string, error) {
					return "bobsmith", nil
				},
				GetUserAppRepoPermissionsFunc: func(_ context.Context, _ int64) (map[string]ghclient.AppRepoPermissions, error) {
					return map[string]ghclient.AppRepoPermissions{
						"dollarshaveclub/foo-bar": ghclient.AppRepoPermissions{
							Repo: "dollarshaveclub/foo-bar",
							Pull: true,
						},
					}, nil
				},
			}
		},
	}
	copy(oauthcfg.UserTokenEncKey[:], []byte("00000000000000000000000000000000"))
	apiv2, err := newV2API(dl, nil, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, oauthcfg, logger, metahelm.FakeKubernetesReporter{})
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	uis := models.UISession{
		Authenticated: true,
		GitHubUser:    "bobsmith",
	}
	uis.EncryptandSetUserToken([]byte("foo"), oauthcfg.UserTokenEncKey)
	apikey := models.APIKey{
		GitHubUser:      uis.GitHubUser,
		PermissionLevel: models.ReadOnlyPermission,
		Description:     "my token description",
	}
	body := fmt.Sprintf("{\"permission\":%v,\"description\":\"%v\"}", int(apikey.PermissionLevel), apikey.Description)
	req, _ := http.NewRequest("POST", fmt.Sprintf("https://foo.com/v2/user/token"), bytes.NewReader([]byte(body)))
	req = req.Clone(withSession(req.Context(), uis))
	rc := httptest.NewRecorder()
	apiv2.apiKeyCreateHandler(rc, req)
	res := rc.Result()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
	out := V2UserAPIKeyResponse{}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("error decoding response: %v", err)
	}
	res.Body.Close()
	if out.Token == uuid.Nil {
		t.Fatalf("expected valid token")
	}
	req, _ = http.NewRequest("GET", fmt.Sprintf("https://foo.com/v2/user/tokens"), nil)
	req = req.Clone(withSession(req.Context(), uis))
	rc = httptest.NewRecorder()
	apiv2.apiKeysHandler(rc, req)
	res = rc.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
	aks := []V2UserAPIKeyData{}
	if err := json.NewDecoder(res.Body).Decode(&aks); err != nil {
		t.Fatalf("error decoding response: %v", err)
	}
	if len(aks) != 1 {
		t.Fatalf("error incorrect number of keys returned; expected: 1, got %v", len(aks))
	}
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("https://foo.com/v2/user/token/%v", aks[0].ID), nil)
	req = mux.SetURLVars(req, map[string]string{"id": aks[0].ID.String()})
	req = req.Clone(withSession(req.Context(), uis))
	rc = httptest.NewRecorder()
	apiv2.apiKeyDestroyHandler(rc, req)
	res = rc.Result()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("bad status code: %v", res.StatusCode)
	}
}
