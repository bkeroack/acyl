package api

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/distribution/uuid"
	"github.com/bkeroack/acyl/pkg/config"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/testhelper/testdatalayer"
	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
)

func TestAPIv1SearchSimple(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_search?repo=dollarshaveclub%2Ffoo-bar", nil)
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
	res := []v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
	for i, r := range res {
		if len(r.CommitSHAMap) == 0 {
			t.Fatalf("r[%v] empty CommitSHAMap", i)
		}
	}
}

func TestAPIv1EnvDetails(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/foo-bar", nil)
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
	res := v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res.CommitSHAMap) != 2 {
		t.Fatalf("unexpected length for CommitSHAMap: %v", len(res.CommitSHAMap))
	}
	for _, v := range res.CommitSHAMap {
		if v != "37a1218def12549a56e4e48be95d9cdf9a20d45d" {
			t.Fatalf("bad value for commit SHA: %v", v)
		}
	}
}

func TestAPIv1EnvDetailsUserAPIKey(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
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
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/foo-bar", nil)
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
	res := v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res.CommitSHAMap) != 2 {
		t.Fatalf("unexpected length for CommitSHAMap: %v", len(res.CommitSHAMap))
	}
	for _, v := range res.CommitSHAMap {
		if v != "37a1218def12549a56e4e48be95d9cdf9a20d45d" {
			t.Fatalf("bad value for commit SHA: %v", v)
		}
	}
}

func TestAPIv1EnvDetailsUserAPIKeyForbidden(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
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
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/foo-bar", nil)
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

func TestAPIv1RecentDefault(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent", nil)
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
	res := []v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
	for i, r := range res {
		if len(r.CommitSHAMap) == 0 {
			t.Fatalf("r[%v] empty CommitSHAMap", i)
		}
	}
	if !strings.HasPrefix(res[0].Created.String(), time.Now().UTC().Format("2006-01-02")) {
		t.Fatalf("bad created: %v", res[0].Created.String())
	}
}

func TestAPIv1RecentDefaultUserAPIKey(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	id, err := dl.CreateAPIKey(context.Background(), models.ReadOnlyPermission,"foo-description", "bobsmith")
	if err != nil {
		t.Fatalf("api key creation should have succeeded")
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent", nil)
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
	res := []v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
	for i, r := range res {
		if len(r.CommitSHAMap) == 0 {
			t.Fatalf("r[%v] empty CommitSHAMap", i)
		}
	}
	if !strings.HasPrefix(res[0].Created.String(), time.Now().UTC().Format("2006-01-02")) {
		t.Fatalf("bad created: %v", res[0].Created.String())
	}
}

func TestAPIv1RecentDefaultUserAPIKeyEmpty(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	id, err := dl.CreateAPIKey(context.Background(), models.ReadOnlyPermission,"foo-description", "foo-user")
	if err != nil {
		t.Fatalf("api key creation should have succeeded")
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent", nil)
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
	res := []v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv1RecentDefaultUserAPIKeyUnauthorized(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent", nil)
	req.Header.Set(apiKeyHeader, uuid.Generate().String())
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("should have rejected: %v: %v", resp.StatusCode, bb)
	}
}

func TestAPIv1RecentEmpty(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent?days=0", nil)
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
	res := []v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv1RecentBadValue(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent?days=foo", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("should have failed with bad request: %v: %v", resp.StatusCode, string(bb))
	}
}

func TestAPIv1RecentNegativeValue(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent?days=-23", nil)
	req.Header.Set(apiKeyHeader, sc.APIKeys[0])
	hc := &http.Client{}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request: %v", err)
	}
	bb, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("should have failed with bad request: %v: %v", resp.StatusCode, string(bb))
	}
}

func TestAPIv1RecentTwoDays(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent?days=2", nil)
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
	res := []v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv1RecentFiveDays(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent?days=5", nil)
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
	res := []v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 4 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}

func TestAPIv1RecentIncludeDestroyed(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	sc := config.ServerConfig{APIKeys: []string{"foo","bar","baz"}}
	apiv1, err := newV1API(dl, nil, nil, sc, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	authMiddleware.apiKeys = sc.APIKeys
	authMiddleware.DL = dl

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/_recent?days=5&include_destroyed=true", nil)
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
	res := []v1QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 5 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}
