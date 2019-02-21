package api

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/testhelper/testdatalayer"
	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
)

func TestAPIv1SearchSimple(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv1, err := newV1API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	req, _ := http.NewRequest("GET", "/v1/envs/_search?repo=dollarshaveclub%2Ffoo-bar", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv1.envSearchHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", rc.Code, string(rc.Body.Bytes()))
	}
	res := []v1QAEnvironment{}
	err = json.Unmarshal(rc.Body.Bytes(), &res)
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

	apiv1, err := newV1API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	authMiddleware.apiKeys = []string{"foo"}

	r := muxtrace.NewRouter()
	apiv1.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/v1/envs/foo-bar", nil)
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

func TestAPIv1RecentDefault(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv1, err := newV1API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	req, _ := http.NewRequest("GET", "/v1/envs/_recent", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv1.envRecentHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", rc.Code, string(rc.Body.Bytes()))
	}
	res := []v1QAEnvironment{}
	err = json.Unmarshal(rc.Body.Bytes(), &res)
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

func TestAPIv1RecentEmpty(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv1, err := newV1API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	req, _ := http.NewRequest("GET", "/v1/envs/_recent?days=0", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv1.envRecentHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", rc.Code, string(rc.Body.Bytes()))
	}
	res := []v1QAEnvironment{}
	err = json.Unmarshal(rc.Body.Bytes(), &res)
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
	rc := httptest.NewRecorder()
	apiv1, err := newV1API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	req, _ := http.NewRequest("GET", "/v1/envs/_recent?days=foo", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv1.envRecentHandler(rc, req)
	if rc.Code != http.StatusBadRequest {
		t.Fatalf("should have failed with bad request: %v: %v", rc.Code, string(rc.Body.Bytes()))
	}
}

func TestAPIv1RecentNegativeValue(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv1, err := newV1API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	req, _ := http.NewRequest("GET", "/v1/envs/_recent?days=-23", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv1.envRecentHandler(rc, req)
	if rc.Code != http.StatusBadRequest {
		t.Fatalf("should have failed with bad request: %v: %v", rc.Code, string(rc.Body.Bytes()))
	}
}

func TestAPIv1RecentTwoDays(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv1, err := newV1API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	req, _ := http.NewRequest("GET", "/v1/envs/_recent?days=2", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv1.envRecentHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", rc.Code, string(rc.Body.Bytes()))
	}
	res := []v1QAEnvironment{}
	err = json.Unmarshal(rc.Body.Bytes(), &res)
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
	rc := httptest.NewRecorder()
	apiv1, err := newV1API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	req, _ := http.NewRequest("GET", "/v1/envs/_recent?days=5", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv1.envRecentHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", rc.Code, string(rc.Body.Bytes()))
	}
	res := []v1QAEnvironment{}
	err = json.Unmarshal(rc.Body.Bytes(), &res)
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
	rc := httptest.NewRecorder()
	apiv1, err := newV1API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	req, _ := http.NewRequest("GET", "/v1/envs/_recent?days=5&include_destroyed=true", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv1.envRecentHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", rc.Code, string(rc.Body.Bytes()))
	}
	res := []v1QAEnvironment{}
	err = json.Unmarshal(rc.Body.Bytes(), &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res) != 5 {
		t.Fatalf("unexpected results length: %v", len(res))
	}
}
