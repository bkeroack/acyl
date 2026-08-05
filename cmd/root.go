package cmd

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/bkeroack/acyl/pkg/config"
	"github.com/bkeroack/acyl/pkg/secrets"
	"github.com/dollarshaveclub/pvc"
	"github.com/spf13/cobra"
)

// set by the main pkg
var (
	Version, Commit, Date string
)

var vaultConfig config.VaultConfig
var secretsConfig config.SecretsConfig
var secretsbackend string
var k8sClientConfig config.K8sClientConfig
var helmClientConfig config.HelmClientConfig

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
	RootCmd.PersistentFlags().StringVar(&secretsbackend, "secrets-backend", "vault", "Secret backend (one of: vault,env)")
	RootCmd.PersistentFlags().StringVar(&secretsConfig.Mapping, "secrets-mapping", "", "Secrets mapping template string (required)")
	RootCmd.PersistentFlags().StringVar(&k8sClientConfig.JWTPath, "k8s-jwt-path", "/var/run/secrets/kubernetes.io/serviceaccount/token", "Path to the JWT used to authenticate the k8s client to the API server")
	RootCmd.PersistentFlags().StringVar(&helmClientConfig.HelmDriver, "helm-driver", os.Getenv("HELM_DRIVER"), "Sets helm driver (default: secrets)")
	RootCmd.PersistentFlags().StringVar(&helmClientConfig.KubeContext, "kubectx", "", "Sets kubernetes context (overrides current context)")
}

func clierr(msg string, params ...interface{}) {
	fmt.Fprintf(os.Stderr, msg+"\n", params...)
	os.Exit(1)
}

func getSecretClient() (*pvc.SecretsClient, error) {
	ops := []pvc.SecretsClientOption{}
	switch secretsbackend {
	case "vault":
		var vat pvc.VaultAuthentication
		switch {
		case vaultConfig.TokenAuth:
			log.Printf("secrets: using vault token auth")
			vat = pvc.TokenVaultAuth
			ops = []pvc.SecretsClientOption{
				pvc.WithVaultToken(vaultConfig.Token),
			}
		case vaultConfig.K8sAuth:
			log.Printf("secrets: using vault k8s auth")
			vat = pvc.K8sVaultAuth
			jwt, err := ioutil.ReadFile(vaultConfig.K8sJWTPath)
			if err != nil {
				clierr("error reading k8s jwt path: %v", err)
			}
			log.Printf("secrets: role: %v; auth path: %v", vaultConfig.K8sRole, vaultConfig.K8sAuthPath)
			ops = []pvc.SecretsClientOption{
				pvc.WithVaultK8sAuth(string(jwt), vaultConfig.K8sRole),
				pvc.WithVaultK8sAuthPath(vaultConfig.K8sAuthPath),
			}
		case vaultConfig.AppID != "" && vaultConfig.UserIDPath != "":
			clierr("app id auth no longer supported")
		default:
			clierr("no Vault authentication methods were supplied")
		}
		secretsConfig.Backend = pvc.WithVaultBackend(vat, vaultConfig.Addr)
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
	err = sf.PopulateAllSecrets(&githubConfig, &slackConfig, &serverConfig, &pgConfig)
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
