package ghclient

import (
	"context"
	"encoding/base64"
	"os"
	"strconv"
	"testing"

	"github.com/bkeroack/acyl/pkg/config"
)

func TestGetInstallationTokenForRepo(t *testing.T) {
	tkn, appid, pk, instid := os.Getenv("GITHUB_TOKEN"), os.Getenv("GITHUB_APP_ID"), os.Getenv("GITHUB_APP_PRIVATE_KEY"), os.Getenv("GITHUB_APP_INSTALLATION_ID")
	if appid == "" || pk == "" || instid == "" {
		t.SkipNow()
	}

	aid, err := strconv.Atoi(appid)
	if err != nil {
		t.Fatalf("invalid app id: %v", err)
	}

	iid, err := strconv.Atoi(instid)
	if err != nil {
		t.Fatalf("invalid installation id: %v", err)
	}

	pkb, err := base64.StdEncoding.DecodeString(pk)
	if err != nil {
		t.Fatalf("invalid private key: %v", err)
	}

	ic, err := NewGithubInstallationClient(config.GithubConfig{
		Token:         tkn,
		AppID:         uint(aid),
		PrivateKeyPEM: pkb,
		OAuth: config.GithubOAuthConfig{
			AppInstallationID: uint(iid),
		},
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	t.Logf("appid: %v; instid: %v\n", appid, instid)

	_, err = ic.GetInstallationTokenForRepo(context.Background(), int64(iid), "dollarshaveclub/acyl")
	if err != nil {
		t.Fatalf("error getting token: %v", err)
	}
}
