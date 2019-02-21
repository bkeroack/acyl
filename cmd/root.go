package cmd

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/secrets"
	"github.com/dollarshaveclub/pvc"
	"github.com/spf13/cobra"
)

var vaultConfig config.VaultConfig
var awsCreds config.AWSCreds
var awsConfig config.AWSConfig
var secretsConfig config.SecretsConfig
var secretsbackend string
var dogstatsdAddr, dogstatsdTags string
var datadogServiceName, datadogTracingAgentAddr string

var RootCmd = &cobra.Command{
	Use:   "acyl",
	Short: "Dynamic Environment System",
	Long:  `Dynamic environment server API and client utility`,
}

//shorthands in use by register: i p r m o
//shorthands here: abcdefg n jkl p q
func init() {
	RootCmd.PersistentFlags().StringVarP(&vaultConfig.Addr, "vault-addr", "a", os.Getenv("VAULT_ADDR"), "Vault URL (if using Vault secret backend)")
	RootCmd.PersistentFlags().StringVarP(&vaultConfig.Token, "vault-token", "b", os.Getenv("VAULT_TOKEN"), "Vault token (if using token auth & Vault secret backend)")
	RootCmd.PersistentFlags().BoolVarP(&vaultConfig.TokenAuth, "vault-token-auth", "c", false, "Use Vault token-based auth (if using Vault secret backend)")
	RootCmd.PersistentFlags().BoolVar(&vaultConfig.K8sAuth, "vault-k8s-auth", false, "Use Vault k8s auth (if using Vault secret backend)")
	RootCmd.PersistentFlags().StringVar(&vaultConfig.K8sJWTPath, "vault-k8s-jwt-path", "/var/run/secrets/kubernetes.io/serviceaccount/token", "Vault k8s JWT file path (if using k8s auth & Vault secret backend)")
	RootCmd.PersistentFlags().StringVar(&vaultConfig.K8sRole, "vault-k8s-role", "", "Vault k8s role (if using k8s auth & Vault secret backend)")
	RootCmd.PersistentFlags().StringVar(&vaultConfig.K8sAuthPath, "vault-k8s-auth-path", "kubernetes", "Vault k8s auth path (if using k8s auth & Vault secret backend)")
	RootCmd.PersistentFlags().StringVarP(&vaultConfig.AppID, "vault-app-id", "d", os.Getenv("APP_ID"), "Vault App-ID (if using Vault secret backend)")
	RootCmd.PersistentFlags().StringVarP(&vaultConfig.UserIDPath, "vault-user-id-path", "e", os.Getenv("USER_ID_PATH"), "Path to file containing Vault User-ID (if using Vault secret backend)")
	RootCmd.PersistentFlags().StringVarP(&awsConfig.Region, "aws-region", "k", "us-west-2", "AWS region")
	RootCmd.PersistentFlags().UintVarP(&awsConfig.MaxRetries, "aws-max-retries", "l", 3, "AWS max retries per operation")
	RootCmd.PersistentFlags().StringVarP(&dogstatsdAddr, "dogstatsd-addr", "q", "127.0.0.1:8125", "Address of dogstatsd for metrics")
	RootCmd.PersistentFlags().StringVar(&dogstatsdTags, "dogstatsd-tags", "", "Comma-separated list of tags to add to dogstatsd metrics (TAG:VALUE)")
	RootCmd.PersistentFlags().StringVar(&datadogTracingAgentAddr, "datadog-tracing-agent-addr", "127.0.0.1:8126", "Address of datadog tracing agent")
	RootCmd.PersistentFlags().StringVar(&datadogServiceName, "datadog-service-name", "acyl", "Default service name to be used for Datadog APM")
	RootCmd.PersistentFlags().StringVar(&secretsbackend, "secrets-backend", "vault", "Secret backend (one of: vault,env)")
	RootCmd.PersistentFlags().StringVar(&secretsConfig.Mapping, "secrets-mapping", "", "Secrets mapping template string (required)")
}

func clierr(msg string, params ...interface{}) {
	fmt.Fprintf(os.Stderr, msg+"\n", params...)
	os.Exit(1)
}

func getSecretClient() (*pvc.SecretsClient, error) {
	ops := []pvc.SecretsClientOption{}
	switch secretsbackend {
	case "vault":
		secretsConfig.Backend = pvc.WithVaultBackend()
		switch {
		case vaultConfig.TokenAuth:
			log.Printf("secrets: using vault token auth")
			ops = []pvc.SecretsClientOption{
				pvc.WithVaultAuthentication(pvc.Token),
				pvc.WithVaultToken(vaultConfig.Token),
			}
		case vaultConfig.K8sAuth:
			log.Printf("secrets: using vault k8s auth")
			jwt, err := ioutil.ReadFile(vaultConfig.K8sJWTPath)
			if err != nil {
				clierr("error reading k8s jwt path: %v", err)
			}
			log.Printf("secrets: role: %v; auth path: %v", vaultConfig.K8sRole, vaultConfig.K8sAuthPath)
			ops = []pvc.SecretsClientOption{
				pvc.WithVaultAuthentication(pvc.K8s),
				pvc.WithVaultK8sAuth(string(jwt), vaultConfig.K8sRole),
				pvc.WithVaultK8sAuthPath(vaultConfig.K8sAuthPath),
			}
		case vaultConfig.AppID != "" && vaultConfig.UserIDPath != "":
			log.Printf("secrets: using vault AppID auth")
			ops = []pvc.SecretsClientOption{
				pvc.WithVaultAuthentication(pvc.AppID),
				pvc.WithVaultAppID(vaultConfig.AppID),
				pvc.WithVaultUserIDPath(vaultConfig.UserIDPath),
			}
		default:
			clierr("no Vault authentication methods were supplied")
		}
		ops = append(ops, pvc.WithVaultHost(vaultConfig.Addr))
	case "env":
		secretsConfig.Backend = pvc.WithEnvVarBackend()
	default:
		clierr("invalid secrets backend: %v", secretsbackend)
	}
	if secretsConfig.Mapping == "" {
		clierr("secrets mapping is required")
	}
	ops = append(ops, pvc.WithMapping(secretsConfig.Mapping), secretsConfig.Backend)
	return pvc.NewSecretsClient(ops...)
}

func getSecrets() {
	sc, err := getSecretClient()
	if err != nil {
		clierr("error getting secrets client: %v", err)
	}
	sf := secrets.NewPVCSecretsFetcher(sc)
	err = sf.PopulateAllSecrets(&awsCreds, &githubConfig, &slackConfig, &serverConfig, &pgConfig)
	if err != nil {
		clierr("error getting secrets: %v", err)
	}
}

func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
