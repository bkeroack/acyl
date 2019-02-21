package api

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/testhelper/testdatalayer"
	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
)

const (
	testDataPath = "../testhelper/testdatalayer/data/db.json"
)

var testlogger = log.New(ioutil.Discard, "", log.LstdFlags)

func TestAPIv0SearchSimple(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv0, err := newV0API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating apiv0: %v", err)
	}
	req, _ := http.NewRequest("GET", "/envs/_search?repo=dollarshaveclub%2Ffoo-bar", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv0.envSearchHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have succeeded: %v", rc.Code)
	}
	res := []v0QAEnvironment{}
	err = json.Unmarshal(rc.Body.Bytes(), &res)
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
	rc := httptest.NewRecorder()
	apiv0, err := newV0API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating apiv0: %v", err)
	}
	req, _ := http.NewRequest("GET", "/envs/_search?user=joshritter&source_branch=feature-rm-rf-slash&status=success", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv0.envSearchHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have succeeded: %v", rc.Code)
	}
	res := []v0QAEnvironment{}
	err = json.Unmarshal(rc.Body.Bytes(), &res)
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
	rc := httptest.NewRecorder()
	apiv0, err := newV0API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating apiv0: %v", err)
	}
	req, _ := http.NewRequest("GET", "/envs/_search?pr=99", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv0.envSearchHandler(rc, req)
	if rc.Code != http.StatusBadRequest {
		t.Fatalf("should have failed with bad request: %v", rc.Code)
	}
}

func TestAPIv0SearchNotFound(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv0, err := newV0API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating apiv0: %v", err)
	}
	req, _ := http.NewRequest("GET", "/envs/_search?user=nonexistant", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv0.envSearchHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("should have returned 200 OK: %v", rc.Code)
	}
	if !bytes.Equal(rc.Body.Bytes(), []byte(`[]`)) {
		t.Fatalf("should have returned an empty JSON array")
	}
}

func TestAPIv0SearchEmptyQuery(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv0, err := newV0API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating apiv0: %v", err)
	}
	req, _ := http.NewRequest("GET", "/envs/_search", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv0.envSearchHandler(rc, req)
	if rc.Code != http.StatusBadRequest {
		t.Fatalf("should have failed with bad request: %v", rc.Code)
	}
}

func TestAPIv0SearchBadStatusOnly(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv0, err := newV0API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating apiv0: %v", err)
	}
	req, _ := http.NewRequest("GET", "/envs/_search?status=destroyed", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv0.envSearchHandler(rc, req)
	if rc.Code != http.StatusBadRequest {
		t.Fatalf("should have failed with bad request: %v", rc.Code)
	}
}

func TestAPIv0SearchSuccessOnly(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	rc := httptest.NewRecorder()
	apiv0, err := newV0API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating apiv0: %v", err)
	}
	req, _ := http.NewRequest("GET", "/envs/_search?status=success", nil)
	req.Header.Set(apiKeyHeader, "foo")
	apiv0.envSearchHandler(rc, req)
	if rc.Code != http.StatusOK {
		t.Fatalf("request should have succeeded, got: %v", rc.Code)
	}
}

func TestAPIv0EnvDetails(t *testing.T) {
	dl, tdl := testdatalayer.New(testlogger, t)
	if err := tdl.Setup(testDataPath); err != nil {
		t.Fatalf("error setting up test database: %v", err)
	}
	defer tdl.TearDown()
	apiv0, err := newV0API(dl, nil, nil, config.ServerConfig{APIKeys: []string{"foo"}}, testlogger)
	if err != nil {
		t.Fatalf("error creating api: %v", err)
	}

	authMiddleware.apiKeys = []string{"foo"}

	r := muxtrace.NewRouter()
	apiv0.register(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/envs/foo-bar", nil)
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
	res := v0QAEnvironment{}
	err = json.Unmarshal(bb, &res)
	if err != nil {
		t.Fatalf("error unmarshaling results: %v", err)
	}
	if len(res.RefMap) != 2 {
		t.Fatalf("unexpected length for RefMap: %v", len(res.RefMap))
	}
}
