package slacknotifier

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bkeroack/acyl/pkg/ghclient"
)

var testAccountsMap1 = map[string]string{"githubUN1": "slackUN1"}
var testAccountsMap2 = map[string]string{"githubUN1": "slackUN2"}

func jsonBytes(jsonMap map[string]string) ([]byte, error) {
	b, err := json.Marshal(jsonMap)
	return b, err
}

func getTestRepoClient(t *testing.T, unMap map[string]string) ghclient.RepoClient {
	frc := &ghclient.FakeRepoClient{
		GetFileContentsFunc: func(ctx context.Context, repo string, path string, ref string) ([]byte, error) {
			b, err := jsonBytes(unMap)
			if err != nil {
				t.Fatal("test repo client failed to marshal json")
			}
			return b, nil
		},
	}
	return frc
}

func TestUsernameReturnedForGithubUsername(t *testing.T) {
	rc := getTestRepoClient(t, testAccountsMap1)
	m := NewRepoBackedSlackUsernameMapper(rc, "", "", "", time.Duration(10)*time.Second)

	sn, err := m.UsernameFromGithubUsername("githubUN1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "slackUN1"
	if sn != expected {
		t.Fatalf("wrong slack username returned: %v, expected: %v", sn, expected)
	}
}

func TestNilReturnedForMissingGithubUsername(t *testing.T) {
	rc := getTestRepoClient(t, testAccountsMap1)
	m := NewRepoBackedSlackUsernameMapper(rc, "", "", "", time.Duration(10)*time.Second)

	sn, err := m.UsernameFromGithubUsername("missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := ""
	if sn != "" {
		t.Fatalf("wrong slack username returned: %v, expected: %v", sn, expected)
	}
}

func TestUsernameMapIsCached(t *testing.T) {
	rc := getTestRepoClient(t, testAccountsMap1)
	m := NewRepoBackedSlackUsernameMapper(rc, "", "", "", time.Duration(10)*time.Second)

	first, err := m.UsernameFromGithubUsername("githubUN1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.client = getTestRepoClient(t, testAccountsMap2)

	second, err := m.UsernameFromGithubUsername("githubUN1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if first != second {
		t.Fatalf("wrong slack username returned: %v, expected: %v", second, first)
	}
}

func TestUsernameMapIsRefreshed(t *testing.T) {
	rc := getTestRepoClient(t, testAccountsMap1)
	m := NewRepoBackedSlackUsernameMapper(rc, "", "", "", time.Duration(2)*time.Second)

	_, err := m.UsernameFromGithubUsername("githubUN1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.client = getTestRepoClient(t, testAccountsMap2)
	time.Sleep(m.updateInterval)

	second, err := m.UsernameFromGithubUsername("githubUN1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "slackUN2"
	if second != expected {
		t.Fatalf("wrong slack username returned: %v, expected: %v", second, expected)
	}
}
