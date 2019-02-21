package secrets

import (
	"crypto/tls"
	"strings"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/pvc"
	"github.com/pkg/errors"
)

// Secret IDs
// These will be interpolated as .ID in the secrets mapping
const (
	awsAccessKeyIDid     = "aws/access_key_id"
	awsSecretAccessKeyid = "aws/secret_access_key"
	githubHookSecretid   = "github/hook_secret"
	githubTokenid        = "github/token"
	apiKeysid            = "api_keys"
	slackTokenid         = "slack/token"
	tlsCertid            = "tls/cert"
	tlsKeyid             = "tls/key"
	dbURIid              = "db/uri"
)

type SecretFetcher interface {
	PopulateAllSecrets(aws *config.AWSCreds, gh *config.GithubConfig, slack *config.SlackConfig, srv *config.ServerConfig, pg *config.PGConfig)
	PopulatePG(pg *config.PGConfig) error
	PopulateAWS(aws *config.AWSCreds) error
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
func (psf *PVCSecretsFetcher) PopulateAllSecrets(aws *config.AWSCreds, gh *config.GithubConfig, slack *config.SlackConfig, srv *config.ServerConfig, pg *config.PGConfig) error {
	if err := psf.PopulateAWS(aws); err != nil {
		return errors.Wrap(err, "error getting AWS secrets")
	}
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

// PopulateAWS populates AWS secrets into aws
func (psf *PVCSecretsFetcher) PopulateAWS(aws *config.AWSCreds) error {
	s, err := psf.sc.Get(awsAccessKeyIDid)
	if err != nil {
		return errors.Wrap(err, "error getting AWS access key ID")
	}
	aws.AccessKeyID = string(s)
	s, err = psf.sc.Get(awsSecretAccessKeyid)
	if err != nil {
		return errors.Wrap(err, "error getting AWS secret access key")
	}
	aws.SecretAccessKey = string(s)
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
	return nil
}
