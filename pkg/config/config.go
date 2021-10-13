package config

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dollarshaveclub/pvc"
	"github.com/pkg/errors"
)

type ServerConfig struct {
	HTTPSPort                  uint
	HTTPSAddr                  string
	DisableTLS                 bool
	TLSCert                    tls.Certificate
	WordnetPath                string
	Furan2Addr                 string
	Furan2APIKey               string
	Furan2SkipVerifyTLS        bool
	APIKeys                    []string
	ReaperIntervalSecs         uint
	EventRateLimitPerSecond    uint
	GlobalEnvironmentLimit     uint
	HostnameTemplate           string
	DatadogServiceName         string
	DebugEndpoints             bool
	DebugEndpointsIPWhitelists []string
	NitroFeatureFlag           bool
	NotificationsDefaultsJSON  string
	OperationTimeoutOverride   time.Duration
	UIBaseURL                  string
	UIPath                     string
	UIBaseRoute                string
	UIBrandingJSON             string
}

type PGConfig struct {
	PostgresURI            string
	PostgresMigrationsPath string
	DatadogServiceName     string
	EnableTracing          bool
}

// K8sClientConfig models the configuration required for a kubernetes client
// to communicate with the API server
type K8sClientConfig struct {
	JWTPath string
}

// K8sSecret models a kubernetes secret
type K8sSecret struct {
	Data map[string][]byte `json:"data"`
	Type string            `json:"type"`
}

type K8sConfig struct {
	// GroupBindings is a map of k8s group name to cluster role
	GroupBindings map[string]string
	// PrivilegedRepoWhitelist is a list of GitHub repositories whose environment service accounts will be given cluster-admin privileges
	PrivilegedRepoWhitelist []string
	// SecretInjections is a map of secret name to value that will be injected into each environment namespace
	SecretInjections map[string]K8sSecret
}

// ProcessPrivilegedRepos takes a comma-separated list of repositories and populates the PrivilegedRepoWhitelist field
func (kc *K8sConfig) ProcessPrivilegedRepos(repostr string) error {
	kc.PrivilegedRepoWhitelist = strings.Split(repostr, ",")
	for i, pr := range kc.PrivilegedRepoWhitelist {
		if rsl := strings.Split(pr, "/"); len(rsl) != 2 {
			return fmt.Errorf("malformed repo at offset %v: %v", i, pr)
		}
	}
	return nil
}

// ProcessGroupBindings takes a comma-separated list of group bindings and populates the GroupBindings field
func (kc *K8sConfig) ProcessGroupBindings(gbstr string) error {
	kc.GroupBindings = make(map[string]string)
	for i, gb := range strings.Split(gbstr, ",") {
		if gb == "" {
			continue
		}
		gbsl := strings.Split(gb, "=")
		if len(gbsl) != 2 {
			return fmt.Errorf("malformed group binding at offset %v: %v", i, gb)
		}
		if len(gbsl[0]) == 0 || len(gbsl[1]) == 0 {
			return fmt.Errorf("empty binding at offset %v: %v", i, gb)
		}
		kc.GroupBindings[gbsl[0]] = gbsl[1]
	}
	return nil
}

// SecretFetcher describes an object that fetches secrets
type SecretFetcher interface {
	Get(id string) ([]byte, error)
}

// ProcessSecretInjections takes a comma-separated list of injections and uses sf to populate the SecretInjections field
func (kc *K8sConfig) ProcessSecretInjections(sf SecretFetcher, injstr string) error {
	kc.SecretInjections = make(map[string]K8sSecret)
	for i, sstr := range strings.Split(injstr, ",") {
		if sstr == "" {
			continue
		}
		ssl := strings.Split(sstr, "=")
		if len(ssl) != 2 {
			return fmt.Errorf("malformed secret injection at offset %v: %v", i, sstr)
		}
		if len(ssl[0]) == 0 || len(ssl[1]) == 0 {
			return fmt.Errorf("empty secret injection at offset %v: %v", i, sstr)
		}
		val, err := sf.Get(ssl[1])
		if err != nil {
			return errors.Wrapf(err, "error fetching secret for id: %v", ssl[1])
		}
		secret := K8sSecret{}
		if err := json.Unmarshal(val, &secret); err != nil {
			return errors.Wrapf(err, "error unmarshaling secret for id: %v", ssl[1])
		}
		kc.SecretInjections[ssl[0]] = secret
	}
	return nil
}

type HelmClientConfig struct {
	HelmDriver  string
	KubeContext string
}

type ConsulConfig struct {
	Addr       string
	LockPrefix string
}

type SlackConfig struct {
	Username                    string
	IconURL                     string
	Token                       string
	Channel                     string
	MapperRepo                  string
	MapperRepoRef               string
	MapperMapPath               string
	MapperUpdateIntervalSeconds uint
}

type VaultConfig struct {
	Addr        string
	Token       string
	TokenAuth   bool
	K8sAuth     bool
	K8sJWTPath  string
	K8sAuthPath string
	K8sRole     string
	AppID       string
	UserIDPath  string
}

type GithubOAuthConfig struct {
	Enforce           bool
	AppInstallationID uint
	ClientID          string
	ClientSecret      string
	CookieAuthKey     [32]byte
	CookieEncKey      [32]byte
	UserTokenEncKey   [32]byte
}

type GithubConfig struct {
	HookSecret    string
	Token         string
	TypePath      string // relative path within repo to look for acyl.yml (QAType definition)
	AppID         uint
	PrivateKeyPEM []byte
	AppHookSecret string
	OAuth         GithubOAuthConfig
}

type BackendConfig struct {
	AminoAddr string
}

type MigrateConfig struct {
	CheckPending  bool
	MetaDataTable string
}

// SecretsConfig contains configuration values for retrieving secrets
type SecretsConfig struct {
	Backend pvc.SecretsClientOption
	Mapping string
}

// UIBrandingConfig defines optional non-default branding for UI pages
type UIBrandingConfig struct {
	FaviconURL        string            `json:"favicon_url"`
	LogoURL           string            `json:"logo_url"`
	LogoHeight        string            `json:"logo_height"`
	LogoWidth         string            `json:"logo_width"`
	LogoStyle         string            `json:"logo_style"`
	LogoLink          string            `json:"logo_link"`
	LogoAlt           string            `json:"logo_alt"`
	Title             string            `json:"title"`
	TitleStyle        string            `json:"title_style"`
	AdditionalDocURLs map[string]string `json:"additional_doc_urls"`
}

var DefaultUIBranding = UIBrandingConfig{
	FaviconURL: "https://dsc-misc.s3.amazonaws.com/img/acyl-favicon.png",
	LogoURL:    "https://s3.amazonaws.com/dsc-misc/img/acyl.png",
	LogoHeight: "30",
	LogoStyle:  "d-inline-block align-top mr-1",
	LogoLink:   "https://github.com/dollarshaveclub/acyl",
	LogoAlt:    "Acyl Chloride",
	Title:      "Acyl UI",
}
