package secrets

import (
	"crypto/tls"
	"fmt"
	"strconv"
	"strings"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/pvc"
	"github.com/pkg/errors"
)

// Secret IDs
// These will be interpolated as .ID in the secrets mapping
const (
	githubHookSecretid         = "github/hook_secret"
	githubTokenid              = "github/token"
	githubAppID                = "github/app/id"
	githubAppPK                = "github/app/private_key"
	githubAppHookSecret        = "github/app/hook_secret"
	githubOAuthInstID          = "github/app/oauth/installation_id"
	githubOAuthClientID        = "github/app/oauth/client/id"
	githubOAuthClientSecret    = "github/app/oauth/client/secret"
	githubOAuthCookieEncKey    = "github/app/oauth/cookie/encryption_key"
	githubOAuthCookieAuthKey   = "github/app/oauth/cookie/authentication_key"
	githubOAuthUserTokenEncKey = "github/app/oauth/user_token/encryption_key"
	apiKeysid                  = "api_keys"
	slackTokenid               = "slack/token"
	tlsCertid                  = "tls/cert"
	tlsKeyid                   = "tls/key"
	dbURIid                    = "db/uri"
	furan2apikey               = "furan2/api_key"
)

type SecretFetcher interface {
	PopulateAllSecrets(gh *config.GithubConfig, slack *config.SlackConfig, srv *config.ServerConfig, pg *config.PGConfig)
	PopulatePG(pg *config.PGConfig) error
	PopulateGithub(gh *config.GithubConfig) error
	PopulateSlack(slack *config.SlackConfig) error
	PopulateServer(srv *config.ServerConfig) error
}

type PVCSecretsFetcher struct {
	sc *pvc.SecretsClient
}

func NewPVCSecretsFetcher(sc *pvc.SecretsClient) *PVCSecretsFetcher {
	return &PVCSecretsFetcher{
		sc: sc,
	}
}

// PopulateAllSecrets populates all secrets into the respective config structs
func (psf *PVCSecretsFetcher) PopulateAllSecrets(gh *config.GithubConfig, slack *config.SlackConfig, srv *config.ServerConfig, pg *config.PGConfig) error {
	if err := psf.PopulateGithub(gh); err != nil {
		return errors.Wrap(err, "error getting GitHub secrets")
	}
	if err := psf.PopulateSlack(slack); err != nil {
		return errors.Wrap(err, "error getting Slack secrets")
	}
	if err := psf.PopulateServer(srv); err != nil {
		return errors.Wrap(err, "error getting server secrets")
	}
	if err := psf.PopulatePG(pg); err != nil {
		return errors.Wrap(err, "error getting db secrets")
	}
	return nil
}

// PopulatePG populates postgres secrets into pg
func (psf *PVCSecretsFetcher) PopulatePG(pg *config.PGConfig) error {
	s, err := psf.sc.Get(dbURIid)
	if err != nil {
		return errors.Wrap(err, "error getting DB URI")
	}
	pg.PostgresURI = string(s)
	return nil
}

// PopulateGithub populates Github secrets into gh
func (psf *PVCSecretsFetcher) PopulateGithub(gh *config.GithubConfig) error {
	s, err := psf.sc.Get(githubHookSecretid)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub hook secret")
	}
	gh.HookSecret = string(s)
	s, err = psf.sc.Get(githubTokenid)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub token")
	}
	gh.Token = string(s)
	s, err = psf.sc.Get(githubAppID)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub App ID")
	}
	// GitHub App
	appid, err := strconv.Atoi(string(s))
	if err != nil {
		return errors.Wrap(err, "app ID must be a valid integer")
	}
	if appid < 1 {
		return fmt.Errorf("app id must be >= 1: %v", appid)
	}
	gh.AppID = uint(appid)
	s, err = psf.sc.Get(githubAppPK)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub App private key")
	}
	gh.PrivateKeyPEM = s
	s, err = psf.sc.Get(githubAppHookSecret)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub App hook secret")
	}
	gh.AppHookSecret = string(s)
	// GitHub App OAuth
	s, err = psf.sc.Get(githubOAuthInstID)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub App installation id")
	}
	iid, err := strconv.Atoi(string(s))
	if err != nil {
		return errors.Wrap(err, "error converting installation id into integer")
	}
	if iid < 1 {
		return fmt.Errorf("invalid installation id: %v", iid)
	}
	gh.OAuth.AppInstallationID = uint(iid)
	s, err = psf.sc.Get(githubOAuthClientID)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub App client id")
	}
	gh.OAuth.ClientID = string(s)
	s, err = psf.sc.Get(githubOAuthClientSecret)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub App client secret")
	}
	gh.OAuth.ClientSecret = string(s)
	s, err = psf.sc.Get(githubOAuthCookieAuthKey)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub App cookie auth key")
	}
	if len(s) != 32 {
		return fmt.Errorf("bad cookie auth key: length must be exactly 32 bytes, value size: %v", len(s))
	}
	copy(gh.OAuth.CookieAuthKey[:], s)
	s, err = psf.sc.Get(githubOAuthCookieEncKey)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub App cookie enc key")
	}
	if len(s) != 32 {
		return fmt.Errorf("bad cookie enc key: length must be exactly 32 bytes, value size: %v", len(s))
	}
	copy(gh.OAuth.CookieEncKey[:], s)
	s, err = psf.sc.Get(githubOAuthUserTokenEncKey)
	if err != nil {
		return errors.Wrap(err, "error getting GitHub App user token enc key")
	}
	if len(s) != 32 {
		return fmt.Errorf("bad user token enc key: length must be exactly 32 bytes, value size: %v", len(s))
	}
	copy(gh.OAuth.UserTokenEncKey[:], s)
	return nil
}

// PopulateSlack populates Slack secrets into slack
func (psf *PVCSecretsFetcher) PopulateSlack(slack *config.SlackConfig) error {
	s, err := psf.sc.Get(slackTokenid)
	if err != nil {
		return errors.Wrap(err, "error getting Slack token")
	}
	slack.Token = string(s)
	return nil
}

// PopulateServer populates server secrets into srv
func (psf *PVCSecretsFetcher) PopulateServer(srv *config.ServerConfig) error {
	s, err := psf.sc.Get(apiKeysid)
	if err != nil {
		return errors.Wrap(err, "error getting API keys")
	}
	srv.APIKeys = strings.Split(string(s), ",")
	if !srv.DisableTLS {
		s, err = psf.sc.Get(tlsCertid)
		if err != nil {
			return errors.Wrap(err, "error getting TLS certificate")
		}
		c := s
		s, err = psf.sc.Get(tlsKeyid)
		if err != nil {
			return errors.Wrap(err, "error getting TLS key")
		}
		k := s
		cert, err := tls.X509KeyPair(c, k)
		if err != nil {
			return errors.Wrap(err, "error parsing TLS cert/key")
		}
		srv.TLSCert = cert
	}
	s, err = psf.sc.Get(furan2apikey)
	if err != nil {
		return errors.Wrap(err, "error getting Furan 2 API key")
	}
	srv.Furan2APIKey = string(s)
	return nil
}
