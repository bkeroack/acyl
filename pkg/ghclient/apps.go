package ghclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/bkeroack/acyl/pkg/config"
	"github.com/google/go-github/v38/github"
	"golang.org/x/oauth2"
)

// GitHubAppInstallationClient describes a GitHub client that returns user-scoped metadata regarding an app installation
type GitHubAppInstallationClient interface {
	GetUserAppInstallations(ctx context.Context) (AppInstallations, error)
	GetUserAppRepos(ctx context.Context, appID int64) ([]string, error)
	GetUser(ctx context.Context) (string, error)
	GetUserAppRepoPermissions(ctx context.Context, instID int64) (map[string]AppRepoPermissions, error)
}

type AppInstallation struct {
	ID int64
}

type AppInstallations []AppInstallation

func (ai AppInstallations) IDPresent(id int64) bool {
	for _, inst := range ai {
		if inst.ID == id {
			return true
		}
	}
	return false
}

func appInstallationsFromGitHubInstallations(in []*github.Installation) AppInstallations {
	out := make(AppInstallations, len(in))
	for i, inst := range in {
		if inst != nil {
			if inst.ID != nil {
				out[i].ID = *inst.ID
			}
		}
	}
	return out
}

// GetUserAppInstallationCount returns the number of app installations that are accessible to the authenticated user
// This method only uses the static token associated with the GitHubClient and not anything present in the context
// GitHubClient should be populated with the user token returned by the oauth login endpoint via the oauth callback handler
func (ghc *GitHubClient) GetUserAppInstallations(ctx context.Context) (AppInstallations, error) {
	lopt := &github.ListOptions{PerPage: 100}
	out := []*github.Installation{}
	for {
		ctx, cf := context.WithTimeout(ctx, ghTimeout)
		defer cf()
		insts, resp, err := ghc.c.Apps.ListUserInstallations(ctx, lopt)
		if err != nil {
			return nil, fmt.Errorf("error listing user installations: %v", err)
		}
		out = append(out, insts...)
		if resp.NextPage == 0 {
			return appInstallationsFromGitHubInstallations(out), nil
		}
		lopt.Page = resp.NextPage
	}
}

// GetUserAppRepos gets repositories that are accessible to the authenticated user for an app installation
// This method only uses the static token associated with the GitHubClient and not anything present in the context
// GitHubClient should be populated with the user token returned by the oauth login endpoint via the oauth callback handler
func (ghc *GitHubClient) GetUserAppRepos(ctx context.Context, instID int64) ([]string, error) {
	lopt := &github.ListOptions{PerPage: 100}
	out := []string{}
	for {
		ctx, cf := context.WithTimeout(ctx, ghTimeout)
		defer cf()
		repos, resp, err := ghc.c.Apps.ListUserRepos(ctx, instID, lopt)
		if err != nil {
			return nil, fmt.Errorf("error listing user repos: %v", err)
		}
		for _, repo := range repos.Repositories {
			if repo != nil && repo.FullName != nil {
				out = append(out, *repo.FullName)
			}
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		lopt.Page = resp.NextPage
	}
}

// GetUser gets the authenticated user login name
// This method only uses the static token associated with the GitHubClient and not anything present in the context
// GitHubClient should be populated with the user token returned by the oauth login endpoint via the oauth callback handler
func (ghc *GitHubClient) GetUser(ctx context.Context) (string, error) {
	ctx, cf := context.WithTimeout(ctx, ghTimeout)
	defer cf()
	user, _, err := ghc.c.Users.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("error getting current authenticated user: %v", err)
	}
	return user.GetLogin(), nil
}

type AppRepoPermissions struct {
	Repo              string
	Admin, Push, Pull bool
}

func (ghc *GitHubClient) GetUserAppRepoPermissions(ctx context.Context, instID int64) (map[string]AppRepoPermissions, error) {
	lopt := &github.ListOptions{PerPage: 100}
	rs := []*github.Repository{}
	for {
		ctx, cf := context.WithTimeout(ctx, ghTimeout)
		defer cf()
		repos, resp, err := ghc.c.Apps.ListUserRepos(ctx, instID, lopt)
		if err != nil {
			return nil, fmt.Errorf("error listing user repos: %v", err)
		}
		rs = append(rs, repos.Repositories...)
		if resp.NextPage == 0 {
			break
		}
		lopt.Page = resp.NextPage
	}
	out := make(map[string]AppRepoPermissions, len(rs))
	for _, r := range rs {
		fn := r.GetFullName()
		p := r.GetPermissions()
		out[fn] = AppRepoPermissions{
			Repo:  fn,
			Admin: p["admin"],
			Push:  p["push"],
			Pull:  p["pull"],
		}
	}
	return out, nil
}

type RepoAppClient interface {
	GetInstallationTokenForRepo(ctx context.Context, instID int64, reponame string) (string, error)
}

var _ RepoAppClient = &InstallationClient{}

type InstallationClient struct {
	c, ci *github.Client
}

// NewGithubInstallationClient returns a GitHubClient that is configured to authenticate as a GitHub App
// using JWTs for requests. This is only useful for a small number of app-specific API calls:
// https://docs.github.com/en/rest/reference/apps
func NewGithubInstallationClient(cfg config.GithubConfig) (*InstallationClient, error) {
	itr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, int64(cfg.AppID), cfg.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("error getting github app installation key: %w", err)
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token})
	tc := oauth2.NewClient(context.Background(), ts)
	return &InstallationClient{
		ci: github.NewClient(&http.Client{Transport: itr}), // app client
		c:  github.NewClient(tc),                           // normal token client
	}, nil
}

// GetInstallationTokenForRepo gets a repo-scoped GitHub access token with read permissions for repo with a validity period of one hour,
// for use with subsequent GitHub API calls by external systems (Furan).
// This app installation must have access to repo or the call will return an error.
// The client must be created as an *app* client to allow JWT authentication to be used which is required for this endpoint.
func (ic *InstallationClient) GetInstallationTokenForRepo(ctx context.Context, instID int64, reponame string) (string, error) {
	// get repo id
	rs := strings.SplitN(reponame, "/", 2)
	if len(rs) != 2 {
		return "", fmt.Errorf("malformed repo name (expected: [owner]/[name]): %v", reponame)
	}
	// use the regular token client instead of the app client for repo details
	repo, _, err := ic.c.Repositories.Get(ctx, rs[0], rs[1])
	if err != nil || repo == nil || repo.ID == nil {
		return "", fmt.Errorf("error getting repo details: %w", err)
	}
	read := "read"
	tkn, _, err := ic.ci.Apps.CreateInstallationToken(ctx, instID, &github.InstallationTokenOptions{
		RepositoryIDs: []int64{*repo.ID},
		Permissions: &github.InstallationPermissions{
			Contents: &read,
			Metadata: &read,
		},
	})
	if err != nil || tkn == nil || tkn.Token == nil {
		return "", fmt.Errorf("error getting installation token: %w", err)
	}
	return *tkn.Token, nil
}
