package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/persistence"
	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/pflag"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func ferr(msg string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, msg, args...)
	os.Exit(1)
}

var contextName string

// MustK8sClient returns a kubernetes client configured via local kubeconfig if possible, otherwise, it exits the program.
func MustK8sClient() (*rest.Config, *kubernetes.Clientset) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	config, err := kubeConfig.ClientConfig()
	if err != nil {
		ferr("error getting k8s client config: %v", err)
	}
	rawcfg, err := kubeConfig.RawConfig()
	if err != nil {
		ferr("error getting raw k8s config: %v", err)
	}
	if rawcfg.CurrentContext != contextName {
		ferr("incorrect kube context: %v (expected %v)", rawcfg.CurrentContext, contextName)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		ferr("error getting k8s clientset: %v", err)
	}
	return config, clientset
}

// read in local data, get the latest Acyl environment
func getAcylEnv() (models.QAEnvironment, *models.KubernetesEnvironment) {
	hd, err := homedir.Dir()
	if err != nil {
		ferr("error getting home directory: %v", err)
	}

	defaultStateDir := os.Getenv("XDG_DATA_HOME")
	if defaultStateDir == "" {
		defaultStateDir = filepath.Join(hd, ".local", "share", "acyl")
	}

	dl := persistence.NewFakeDataLayer()
	if err := dl.Load(defaultStateDir); err != nil {
		ferr("error loading state: %v", err)
	}

	envs, err := dl.Search(context.Background(), models.EnvSearchParameters{Repo: "dollarshaveclub/acyl", Status: models.Success})
	if err != nil {
		ferr("error searching environments: %v", err)
	}

	if n := len(envs); n != 1 {
		ferr("error finding running acyl envs (found: %v)", n)
	}

	kenv, err := dl.GetK8sEnv(context.Background(), envs[0].Name)
	if err != nil {
		ferr("error getting k8s env: %v", err)
	}

	return envs[0], kenv
}

var GitHubTokenEnvVar, QuayTokenEnvVar string

func getGitHubToken() string {
	return os.Getenv(GitHubTokenEnvVar)
}

func getQuayToken() string {
	return os.Getenv(QuayTokenEnvVar)
}

var imagePullSecretPath string

func getImagePullSecret() string {
	d, err := ioutil.ReadFile(imagePullSecretPath)
	if err != nil {
		ferr("error reading image pull secret file: %v", err)
	}
	return string(d)
}

var FuranDockerCfgPath string

func getFuranDockerCfg() string {
	d, err := ioutil.ReadFile(FuranDockerCfgPath)
	if err != nil {
		ferr("error reading docker cfg file: %v", err)
	}
	return string(d)
}

var SlackTokenEnvVar string

func getSlackToken() string {
	return os.Getenv(SlackTokenEnvVar)
}

func injectFuran1Secrets(kc *kubernetes.Clientset, ns string) {
	fmt.Println("getting furan 1 vault configmap")
	cfgm, err := kc.CoreV1().ConfigMaps(ns).Get(context.Background(), "vault", metav1.GetOptions{})
	if err != nil {
		ferr("error getting furan 1 vault configmap: %v", err)
	}

	jd := cfgm.Data["secrets.json"]
	secrets := make(map[string]string)

	if err := json.Unmarshal([]byte(jd), &secrets); err != nil {
		ferr("error unmarshaling configmap json: %v", err)
	}

	secrets["production/furan/github/token"] = getGitHubToken()
	secrets["production/furan/dockercfg"] = getFuranDockerCfg()

	jdd, err := json.Marshal(&secrets)
	if err != nil {
		ferr("error marshaling configmap json: %v", err)
	}

	cfgm.Data["secrets.json"] = string(jdd)

	fmt.Println("updating furan 1 vault configmap")
	if _, err := kc.CoreV1().ConfigMaps(ns).Update(context.Background(), cfgm, metav1.UpdateOptions{}); err != nil {
		ferr("error updating furan vault configmap: %v", err)
	}

	fmt.Println("finding and bouncing furan 1 vault pod")
	if err := findAndBouncePod(kc, ns, "release=dollarshaveclub-furan-vault,app=vault"); err != nil {
		ferr("error restarting furan 1 vault pod: %v", err)
	}

	fmt.Println("giving a moment for furan 1 vault to configure itself")

	time.Sleep(5 * time.Second)

	fmt.Println("finding and bouncing furan 1 pods")

	if err := findAndBouncePod(kc, ns, "app=furan,appsel=furan"); err != nil {
		ferr("error restarting furan pod: %v", err)
	}
}

func injectFuran2Secrets(kc *kubernetes.Clientset, ns string) {
	fmt.Println("getting furan 2 vault configmap")
	cfgm, err := kc.CoreV1().ConfigMaps(ns).Get(context.Background(), "vault", metav1.GetOptions{})
	if err != nil {
		ferr("error getting furan 2 vault configmap: %v", err)
	}

	jd := cfgm.Data["secrets.json"]
	secrets := make(map[string]string)

	if err := json.Unmarshal([]byte(jd), &secrets); err != nil {
		ferr("error unmarshaling configmap json: %v", err)
	}

	secrets["github/token"] = getGitHubToken()
	secrets["quay/token"] = getQuayToken()
	secrets["aws/access_key_id"] = os.Getenv("AWS_ACCESS_KEY_ID")
	secrets["aws/secret_access_key"] = os.Getenv("AWS_SECRET_ACCESS_KEY")

	jdd, err := json.Marshal(&secrets)
	if err != nil {
		ferr("error marshaling configmap json: %v", err)
	}

	cfgm.Data["secrets.json"] = string(jdd)

	fmt.Println("updating furan 2 vault configmap")
	if _, err := kc.CoreV1().ConfigMaps(ns).Update(context.Background(), cfgm, metav1.UpdateOptions{}); err != nil {
		ferr("error updating furan 2 vault configmap: %v", err)
	}

	fmt.Println("finding and bouncing furan 2 vault pod")
	if err := findAndBouncePod(kc, ns, "release=dollarshaveclub-furan-vault,app=vault"); err != nil {
		ferr("error restarting furan 2 vault pod: %v", err)
	}

	fmt.Println("giving a moment for furan 2 vault to configure itself")

	time.Sleep(5 * time.Second)

	fmt.Println("finding and bouncing furan 2 pods")

	if err := findAndBouncePod(kc, ns, "app=furan2,release=furan"); err != nil {
		ferr("error restarting furan 2 pod: %v", err)
	}
}

func injectAcylSecrets(kc *kubernetes.Clientset, ns string) {
	fmt.Println("getting acyl secrets")
	s, err := kc.CoreV1().Secrets(ns).Get(context.Background(), "dummy-acyl-secrets", metav1.GetOptions{})
	if err != nil {
		ferr("error getting acyl secret: %v", err)
	}

	s.Data["furan2_api_key"] = []byte(os.Getenv("FURAN2_API_KEY"))
	s.Data["github_token"] = []byte(getGitHubToken())
	s.Data["image_pull_secret"] = []byte(getImagePullSecret())
	s.Data["slack_token"] = []byte(getSlackToken())
	s.Data["github_app_id"] = []byte(os.Getenv("GITHUB_APP_ID"))
	s.Data["github_app_oauth_installation_id"] = []byte(os.Getenv("GITHUB_APP_INSTALLATION_ID"))
	// base64 decode because k8s will re-encode
	data, err := base64.StdEncoding.DecodeString(os.Getenv("GITHUB_APP_PRIVATE_KEY"))
	if err != nil {
		ferr("error decoding github app private key: %v", err)
	}
	s.Data["github_app_private_key"] = data

	fmt.Println("updating acyl secrets")
	if _, err := kc.CoreV1().Secrets(ns).Update(context.Background(), s, metav1.UpdateOptions{}); err != nil {
		ferr("error updating acyl secret: %v", err)
	}

	fmt.Println("finding and bouncing acyl pod")
	if err := findAndBouncePod(kc, ns, "app=dollarshaveclub-acyl,appsel=acyl"); err != nil {
		ferr("error restarting acyl pod: %v", err)
	}
}

func findAndBouncePod(kc *kubernetes.Clientset, ns, label string) error {
	pods, err := kc.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{
		LabelSelector: label,
	})
	if err != nil {
		return fmt.Errorf("error listing pods: %w", err)
	}
	if n := len(pods.Items); n != 1 {
		return fmt.Errorf("unexpected pod count: %v (wanted 1)", n)
	}
	pod := pods.Items[0]
	if err := kc.CoreV1().Pods(ns).Delete(context.Background(), pod.Name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("error deleting pod: %w", err)
	}
	watch, err := kc.CoreV1().Pods(ns).Watch(context.Background(), metav1.ListOptions{
		LabelSelector: label,
	})
	if err != nil {
		return fmt.Errorf("error starting pod watch: %w", err)
	}
	defer watch.Stop()
	for {
		select {
		case event := <-watch.ResultChan():
			if event.Type == "MODIFIED" {
				pod, ok := event.Object.(*corev1.Pod)
				if !ok {
					return fmt.Errorf("unexpected type for event obj: %T", pod)
				}
				switch pod.Status.Phase {
				case corev1.PodRunning:
					fmt.Println("pod running! done")
					return nil
				case corev1.PodFailed:
					return fmt.Errorf("pod failed: %v", pod.Status.Reason)
				default:
					fmt.Printf("\t... got pod event: %v; ignoring\n", pod.Status.Phase)
					continue
				}
			}
		case <-time.After(60 * time.Second):
			return fmt.Errorf("timeout waiting for pod to become healthy")
		}
	}
}

var furan2Enabled bool

func init() {
	pflag.StringVar(&contextName, "kube-context", "minikube", "Enforce using only this local kubectl context")
	pflag.BoolVar(&furan2Enabled, "use-furan2", false, "use furan 2 instead of furan 1")
	pflag.StringVar(&imagePullSecretPath, "image-pull-secret-path", "image-pull-secret.json", "Path to file containing an image pull secret dockerfgjson")
	pflag.StringVar(&FuranDockerCfgPath, "dockercfg-path", "dockercfg.json", "Path to file containing dockercfg json for Furan")
	pflag.StringVar(&GitHubTokenEnvVar, "github-token-env-var", "GITHUB_TOKEN", "Environment variable containing GitHub API token")
	pflag.StringVar(&QuayTokenEnvVar, "quay-token-env-var", "QUAY_TOKEN", "Environment variable containing Quay API token")
	pflag.StringVar(&SlackTokenEnvVar, "slack-token-env-var", "SLACK_TOKEN", "Environment variable containing Slack API token")
}

func main() {
	pflag.Parse()

	qaenv, kenv := getAcylEnv()

	fmt.Printf("found acyl env: %v (ns: %v)\n", qaenv.Name, kenv.Namespace)

	_, kc := MustK8sClient()

	if furan2Enabled {
		injectFuran2Secrets(kc, kenv.Namespace)
	} else {
		injectFuran1Secrets(kc, kenv.Namespace)
	}

	injectAcylSecrets(kc, kenv.Namespace)

	fmt.Println("done!")
}
