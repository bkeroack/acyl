package secrets

import (
	"encoding/json"
	"io/ioutil"
	"testing"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/pvc"
)

const (
	testSecretsJSONfile = "testdata/fakesecrets.json"
	testSecretPrefix    = "secret/qa/acyl/"
)

var testAWSCreds = &config.AWSCreds{}
var testGithubConfig = &config.GithubConfig{}
var testSlackConfig = &config.SlackConfig{}
var testServerConfig = &config.ServerConfig{}
var testPGConfig = &config.PGConfig{}

func readSecretsJSON(t *testing.T) map[string]string {
	d, err := ioutil.ReadFile(testSecretsJSONfile)
	if err != nil {
		t.Fatalf("error reading secrets json file: %v", err)
	}
	sm := map[string]string{}
	err = json.Unmarshal(d, &sm)
	if err != nil {
		t.Fatalf("error unmarshaling secrets json: %v", err)
	}
	return sm
}

func TestSecretsPopulateAllSecrets(t *testing.T) {
	sm := readSecretsJSON(t)
	sc, err := pvc.NewSecretsClient(pvc.WithJSONFileBackend(), pvc.WithJSONFileLocation(testSecretsJSONfile), pvc.WithMapping(testSecretPrefix+"{{ .ID }}"))
	if err != nil {
		t.Fatalf("error getting secrets client: %v", err)
	}
	psf := PVCSecretsFetcher{sc: sc}
	err = psf.PopulateAllSecrets(testAWSCreds, testGithubConfig, testSlackConfig, testServerConfig, testPGConfig)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if testAWSCreds.AccessKeyID != sm[testSecretPrefix+awsAccessKeyIDid] {
		t.Fatalf("bad value for aws access key id: %v", testAWSCreds.AccessKeyID)
	}
	if testGithubConfig.HookSecret != sm[testSecretPrefix+githubHookSecretid] {
		t.Fatalf("bad value for github hook secret: %v", testGithubConfig.HookSecret)
	}
	if testSlackConfig.Token != sm[testSecretPrefix+slackTokenid] {
		t.Fatalf("bad value for slack token: %v", testSlackConfig.Token)
	}
	if testPGConfig.PostgresURI != sm[testSecretPrefix+dbURIid] {
		t.Fatalf("bad value for db uri: %v", testPGConfig.PostgresURI)
	}
}
