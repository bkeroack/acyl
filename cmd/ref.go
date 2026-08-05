package cmd

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/spf13/cobra"
)

const (
	refEndpoint     = "%v/envs/%v"
	httpTimeoutSecs = 30
)

var acylServer, acylToken, repo, envName string

var refCmd = &cobra.Command{
	Use:   "ref",
	Short: "Get ref for a repo and environment",
	Long:  `Query API for the required ref for a given repo and environment and write to stdout`,
	Run:   ref,
}

func init() {
	refCmd.PersistentFlags().StringVar(&acylServer, "acyl-server", "https://acyl.shave.io", "Acyl API server")
	refCmd.PersistentFlags().StringVar(&repo, "repo", "", "Source repo (required)")
	refCmd.PersistentFlags().StringVar(&envName, "env-name", "", "Environment name (required)")
	refCmd.PersistentFlags().StringVar(&acylToken, "api-token", "", "Acyl API token")
	RootCmd.AddCommand(refCmd)
}

func ref(cmd *cobra.Command, args []string) {
	var qa models.QAEnvironment

	if repo == "" {
		clierr("repo is required")
	}
	if envName == "" {
		clierr("env-name is required")
	}
	if acylToken == "" {
		clierr("token is required")
	}

	c := http.Client{
		Timeout: httpTimeoutSecs * time.Second,
	}
	req, err := http.NewRequest("GET", fmt.Sprintf(refEndpoint, acylServer, envName), nil)
	if err != nil {
		clierr("error creating http request: %v", err)
	}
	req.Header.Add("API-Key", acylToken)
	resp, err := c.Do(req)
	if err != nil {
		clierr("error performing http request: %v", err)
	}
	rb, _ := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		clierr("status code indicates failure: %v: %v", resp.StatusCode, string(rb))
	}
	err = json.Unmarshal(rb, &qa)
	if err != nil {
		clierr("error unmarshaling response (client/server version mismatch?): %v", err)
	}
	ref, ok := qa.RefMap[repo]
	if !ok {
		clierr("repo '%v' not found in RefMap: %v", repo, qa.RefMap)
	}
	fmt.Printf("%v", ref)
}
