package cmd

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"

	"github.com/bkeroack/acyl/pkg/ghevent"
	"github.com/google/go-github/github"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// webhookCmd represents the webhook command
var webhookCmd = &cobra.Command{
	Use:   "webhook",
	Short: "Simulate a GitHub webhook",
	Long:  `Send a simulated GitHub webhook to an acyl server`,
	Run:   webhook,
}

type webhookOptions struct {
	action         string
	installationID int64
	repo           string
	pr             uint
	user           string
	headRef        string
	headSHA        string
	baseRef        string
	baseSHA        string
	host           string
	secret         string
	endpoint       string
	ignorecert     bool
	disableHTTPS   bool
	verbose        bool
}

var whOptions = &webhookOptions{}

func init() {
	webhookCmd.PersistentFlags().StringVar(&whOptions.action, "action", "opened", "Webhook action type (one of: opened, reopened, synchronize, closed)")
	webhookCmd.PersistentFlags().StringVar(&whOptions.repo, "repo", "", "Repository name including owner")
	webhookCmd.PersistentFlags().Int64Var(&whOptions.installationID, "installation-id", 1, "GitHub App installation ID (req'd and must be valid if using GitHub App webhook endpoint)")
	webhookCmd.PersistentFlags().UintVar(&whOptions.pr, "pr", 1, "PR number")
	webhookCmd.PersistentFlags().StringVar(&whOptions.user, "user", "", "Username")
	webhookCmd.PersistentFlags().StringVar(&whOptions.headRef, "head-ref", "", "HEAD ref (branch/tag)")
	webhookCmd.PersistentFlags().StringVar(&whOptions.baseRef, "base-ref", "", "base ref (branch/tag)")
	webhookCmd.PersistentFlags().StringVar(&whOptions.headSHA, "head-sha", "", "HEAD SHA")
	webhookCmd.PersistentFlags().StringVar(&whOptions.baseSHA, "base-sha", "", "base SHA")
	webhookCmd.PersistentFlags().StringVar(&whOptions.host, "acyl-host", "", "Acyl hostname:port")
	webhookCmd.PersistentFlags().StringVar(&whOptions.secret, "secret", "", "Hub signature secret (must match the secret expected by the Acyl host)")
	webhookCmd.PersistentFlags().StringVar(&whOptions.endpoint, "endpoint", "/webhook", "acyl webhook endpoint")
	webhookCmd.PersistentFlags().BoolVar(&whOptions.ignorecert, "ignore-cert", true, "Ignore TLS certificate validity (INSECURE)")
	webhookCmd.PersistentFlags().BoolVar(&whOptions.disableHTTPS, "disable-https", false, "Do not use TLS/HTTPS (INSECURE)")
	webhookCmd.PersistentFlags().BoolVarP(&whOptions.verbose, "verbose", "v", false, "print request headers and body to stdout")
	RootCmd.AddCommand(webhookCmd)
}

func intp(i int) *int    { return &i }
func boolp(b bool) *bool { return &b }

func webhook(cmd *cobra.Command, args []string) {
	if whOptions.repo == "" ||
		whOptions.user == "" ||
		whOptions.headRef == "" ||
		whOptions.baseRef == "" ||
		whOptions.headSHA == "" ||
		whOptions.baseSHA == "" ||
		whOptions.host == "" {
		log.Fatalf("repo, user, host and refs/SHAs are required")
	}
	if whOptions.action != "opened" && whOptions.action != "reopened" && whOptions.action != "synchronize" && whOptions.action != "closed" {
		log.Fatalf("invalid action: %v", whOptions.action)
	}
	rl := strings.Split(whOptions.repo, "/")
	if len(rl) != 2 {
		log.Fatalf("malformed repo: %v", whOptions.repo)
	}

	event := github.PullRequestEvent{
		Action: &whOptions.action,
		Installation: &github.Installation{
			ID: &whOptions.installationID,
		},
		Repo: &github.Repository{
			Name:     &rl[1],
			FullName: &whOptions.repo,
		},
		PullRequest: &github.PullRequest{
			Number: intp(int(whOptions.pr)),
			User: &github.User{
				Login: &whOptions.user,
			},
			Head: &github.PullRequestBranch{
				Ref: &whOptions.headRef,
				SHA: &whOptions.headSHA,
				Repo: &github.Repository{
					Fork: boolp(false),
				},
			},
			Base: &github.PullRequestBranch{
				Ref: &whOptions.baseRef,
				SHA: &whOptions.baseSHA,
				Repo: &github.Repository{
					FullName: &whOptions.repo,
				},
			},
		},
	}

	var jb []byte
	var err error
	if whOptions.verbose {
		jb, err = json.MarshalIndent(&event, "", "    ")
	} else {
		jb, err = json.Marshal(&event)
	}
	if err != nil {
		log.Fatalf("error marshaling event body: %v", err)
	}

	geh := ghevent.NewGitHubEventWebhook(nil, whOptions.secret, "", nil)
	sig := geh.GenerateSignatureString(jb)

	urlpfx := "https://"
	if whOptions.disableHTTPS {
		urlpfx = "http://"
	}
	url := urlpfx + whOptions.host + whOptions.endpoint
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jb))
	if err != nil {
		log.Fatalf("error creating http request: %v", err)
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("X-Hub-Signature", sig)
	req.Header.Add("X-GitHub-Event", "pull_request")
	req.Header.Add("X-GitHub-Delivery", uuid.Must(uuid.NewRandom()).String())

	if whOptions.verbose {
		fmt.Printf("POST %v\n", url)
		for k, v := range req.Header {
			for _, h := range v {
				fmt.Printf("%v: %v\n", k, h)
			}
		}
		fmt.Printf("\n%v\n", string(jb))
	}

	var tr *http.Transport
	if whOptions.ignorecert {
		tr = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	hc := http.Client{Transport: tr}
	resp, err := hc.Do(req)
	if err != nil {
		log.Fatalf("error performing http request: %v", err)
	}
	defer resp.Body.Close()
	rb, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("error reading response body: %v", err)
	}
	fmt.Printf("response: %v: %v\n", resp.Status, string(rb))
}
