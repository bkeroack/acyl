package api

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bkeroack/acyl/pkg/config"
	"github.com/bkeroack/acyl/pkg/ghclient"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/testhelper/testdatalayer"
	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
)

const (
	testDataPath = "../testhelper/testdatalayer/data/db.json"
)

var testlogger = log.New(ioutil.Discard, "", log.LstdFlags)

// fake testing key
var key = `-----BEGIN RSA PRIVATE KEY-----
MIIEpQIBAAKCAQEA0BUezcR7uycgZsfVLlAf4jXP7uFpVh4geSTY39RvYrAll0yh
q7uiQypP2hjQJ1eQXZvkAZx0v9lBYJmX7e0HiJckBr8+/O2kARL+GTCJDJZECpjy
97yylbzGBNl3s76fZ4CJ+4f11fCh7GJ3BJkMf9NFhe8g1TYS0BtSd/sauUQEuG/A
3fOJxKTNmICZr76xavOQ8agA4yW9V5hKcrbHzkfecg/sQsPMmrXixPNxMsqyOMmg
jdJ1aKr7ckEhd48ft4bPMO4DtVL/XFdK2wJZZ0gXJxWiT1Ny41LVql97Odm+OQyx
tcayMkGtMb1nwTcVVl+RG2U5E1lzOYpcQpyYFQIDAQABAoIBAAfUY55WgFlgdYWo
i0r81NZMNBDHBpGo/IvSaR6y/aX2/tMcnRC7NLXWR77rJBn234XGMeQloPb/E8iw
vtjDDH+FQGPImnQl9P/dWRZVjzKcDN9hNfNAdG/R9JmGHUz0JUddvNNsIEH2lgEx
C01u/Ntqdbk+cDvVlwuhm47MMgs6hJmZtS1KDPgYJu4IaB9oaZFN+pUyy8a1w0j9
RAhHpZrsulT5ThgCra4kKGDNnk2yfI91N9lkP5cnhgUmdZESDgrAJURLS8PgInM4
YPV9L68tJCO4g6k+hFiui4h/4cNXYkXnaZSBUoz28ICA6e7I3eJ6Y1ko4ou+Xf0V
csM8VFkCgYEA7y21JfECCfEsTHwwDg0fq2nld4o6FkIWAVQoIh6I6o6tYREmuZ/1
s81FPz/lvQpAvQUXGZlOPB9eW6bZZFytcuKYVNE/EVkuGQtpRXRT630CQiqvUYDZ
4FpqdBQUISt8KWpIofndrPSx6JzI80NSygShQsScWFw2wBIQAnV3TpsCgYEA3reL
L7AwlxCacsPvkazyYwyFfponblBX/OvrYUPPaEwGvSZmE5A/E4bdYTAixDdn4XvE
ChwpmRAWT/9C6jVJ/o1IK25dwnwg68gFDHlaOE+B5/9yNuDvVmg34PWngmpucFb/
6R/kIrF38lEfY0pRb05koW93uj1fj7Uiv+GWRw8CgYEAn1d3IIDQl+kJVydBKItL
tvoEur/m9N8wI9B6MEjhdEp7bXhssSvFF/VAFeQu3OMQwBy9B/vfaCSJy0t79uXb
U/dr/s2sU5VzJZI5nuDh67fLomMni4fpHxN9ajnaM0LyI/E/1FFPgqM+Rzb0lUQb
yqSM/ptXgXJls04VRl4VjtMCgYEAprO/bLx2QjxdPpXGFcXbz6OpsC92YC2nDlsP
3cfB0RFG4gGB2hbX/6eswHglLbVC/hWDkQWvZTATY2FvFps4fV4GrOt5Jn9+rL0U
elfC3e81Dw+2z7jhrE1ptepprUY4z8Fu33HNcuJfI3LxCYKxHZ0R2Xvzo+UYSBqO
ng0eTKUCgYEAxW9G4FjXQH0bjajntjoVQGLRVGWnteoOaQr/cy6oVii954yNMKSP
rezRkSNbJ8cqt9XQS+NNJ6Xwzl3EbuAt6r8f8VO1TIdRgFOgiUXRVNZ3ZyW8Hegd
kGTL0A6/0yAu9qQZlFbaD5bWhQo7eyx63u4hZGppBhkTSPikOYUPCH8=
-----END RSA PRIVATE KEY-----`

var ghcfg = config.GithubConfig{
	PrivateKeyPEM: []byte(key),
	AppID:         1,
	HookSecret:    "asdf",
	AppHookSecret: "asdf",
}

func TestAPIv0List(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs?full_details=true", nil)
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
	res := []v0QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 7 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv0ListUserAPIKey(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	id, err := dl.CreateAPIKey(context.Background(), models.ReadOnlyPermission, "foo-description", "bobsmith")
	if err != nil {
		t.Fatalf("api key creation should have succeeded")
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs?full_details=true", nil)
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
	res := []v0QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv0SearchSimple(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/_search?repo=dollarshaveclub%2Ffoo-bar", nil)
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
	res := []v0QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv0SearchComplex(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/_search?user=joshritter&source_branch=feature-rm-rf-slash&status=success", nil)
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
	res := []v0QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv0SearchBadPROnly(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/_search?pr=99", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, bb)
	}
}

func TestAPIv0SearchNotFound(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/_search?user=nonexistant", nil)
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
	res := []v0QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv0SearchEmptyQuery(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/_search", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, bb)
	}
}

func TestAPIv0SearchBadStatusOnly(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/_search?status=destroyed", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("should have succeeded: %v: %v", resp.StatusCode, bb)
	}
}

func TestAPIv0SearchSuccessOnly(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/_search?status=success", nil)
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
}

func TestAPIv0EnvDetails(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/foo-bar", nil)
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
	res := v0QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res.RefMap) != 2 {
		t.Fatalf("unexpected length for RefMap: %v", len(res.RefMap))
	}
}

func TestAPIv0EnvDetailsUserAPIKey(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	id, err := dl.CreateAPIKey(context.Background(), models.ReadOnlyPermission, "foo-description", "bobsmith")
	if err != nil {
		t.Fatalf("api key creation should have succeeded")
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/foo-bar", nil)
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
	res := v0QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res.RefMap) != 2 {
		t.Fatalf("unexpected length for RefMap: %v", len(res.RefMap))
	}
}

func TestAPIv0EnvDetailsUserAPIKeyForbidden(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv0, err := newV0API(dl, nil, nil, &ghclient.FakeRepoClient{}, ghcfg, sc, testlogger)
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
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/foo-bar", nil)
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
