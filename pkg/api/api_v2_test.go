package api

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/testhelper/testdatalayer"
	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
)

func TestAPIv2SearchByTrackingRef(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv2, err := newV2API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}
	req, _ := http.NewRequest("GET", "/v2/envs/_search?repo=dollarshaveclub%2Fbiz-baz&tracking_ref=master", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv2.envSearchHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have succeeded: %v: %v", rc.Code, string(rc.Body.Bytes()))
	}
	res := []v2QAEnvironment{}
	err = json.Unmarshal(rc.Body.Bytes(), &res)
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

func TestAPIv2EnvDetails(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	apiv2, err := newV2API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	authMiddleware.apiKeys = []string{"foo"}

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/v2/envs/biz-biz2", nil)
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
	res := v2QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if res.SourceRef != "master" {
		t.Fatalf("bad source ref: %v", res.SourceRef)
	}
}

func TestAPIv2HealthCheck(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()

	apiv2, err := newV2API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
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

	apiv2, err := newV2API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	authMiddleware.apiKeys = []string{"foo"}

	r := muxtrace.NewRouter()
	apiv2.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/v2/eventlog/db20d1e7-1e0d-45c6-bfe1-4ea24b7f0000", nil)
	req.Header.Set(apiKeyHeader, "foo")

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
	req.Header.Set(apiKeyHeader, "foo")

	resp, err = hc.Do(req)
	if err != nil {
		t.Fatalf("error executing request 2: %v", err)
	}

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("should have been a 400: %v", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ = http.NewRequest("GET", ts.URL+"/v2/eventlog/9beb4f55-bc47-4411-b17d-78e2c0bccb25", nil)
	req.Header.Set(apiKeyHeader, "foo")

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
