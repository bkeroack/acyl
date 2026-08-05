package ghclient

import (
	"context"
	"fmt"
	"github.com/bkeroack/acyl/pkg/eventlogger"
	"golang.org/x/time/rate"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bkeroack/acyl/pkg/ghapp"

	"github.com/google/go-github/v38/github"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
)

const (
	ghTimeout = 15 * time.Second
)

// BranchInfo includes the information about a specific branch of a git repo
type BranchInfo struct {
	Name string
	SHA  string
}

// CommitStatus describes a status associated with a git commit
type CommitStatus struct {
	Context     string
	Status      string
	Description string
	TargetURL   string
}

// RepoClient describes an object capable of operating on git repositories
type RepoClient interface {
	GetBranch(context.Context, string, string) (BranchInfo, error)
	GetBranches(context.Context, string) ([]BranchInfo, error)
	GetTags(context.Context, string) ([]BranchInfo, error)
	SetStatus(context.Context, string, string, *CommitStatus) error
	GetPRStatus(context.Context, string, uint) (string, error)
	GetCommitMessage(context.Context, string, string) (string, error)
	GetFileContents(ctx context.Context, repo string, path string, ref string) ([]byte, error)
	GetDirectoryContents(ctx context.Context, repo, path, ref string) (map[string]FileContents, error)
	GetRepoArchive(ctx context.Context, repo, ref string) (string, error)
}

type RateLimitedHTTPClient struct {
	hc http.Client
	rl *rate.Limiter
}

func (c *RateLimitedHTTPClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	err := c.rl.Wait(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "error waiting on rate limiter")
	}
	return c.hc.Do(req)
}

func (c *RateLimitedHTTPClient) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "error creating http request")
	}
	return c.Do(ctx, req)
}

// GitHubClient is an object that interacts with the GitHub API
type GitHubClient struct {
	c   *github.Client
	rhc *RateLimitedHTTPClient
}

// NewGitHubClient returns a GitHubClient instance that authenticates using
// the personal access token provided
func NewGitHubClient(token string) *GitHubClient {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return &GitHubClient{
		c: github.NewClient(tc),
		rhc: &RateLimitedHTTPClient{
			// Max rate: 2 per second sustained with up to 15 req burst
			rl: rate.NewLimiter(rate.Every(500*time.Millisecond), 15),
		},
	}
}

var _ RepoClient = &GitHubClient{}

func (ghc *GitHubClient) getClient(ctx context.Context) *github.Client {
	return ghapp.GetGitHubInstallationClient(ctx, ghc.c) // return the client embedded in ctx if present, otherwise ghc.c
}

// GetBranch gets the information for a specific branch
func (ghc *GitHubClient) GetBranch(ctx context.Context, repo, branch string) (BranchInfo, error) {
	rs := strings.Split(repo, "/")
	if len(rs) != 2 {
		return BranchInfo{}, fmt.Errorf("malformed repo: %v", repo)
	}
	ctx, cf := context.WithTimeout(ctx, ghTimeout)
	defer cf()
	b, _, err := ghc.getClient(ctx).Repositories.GetBranch(ctx, rs[0], rs[1], branch, false)
	if err != nil {
		return BranchInfo{}, fmt.Errorf("error getting branch: %v", err)
	}
	return BranchInfo{
		Name: branch,
		SHA:  *b.Commit.SHA,
	}, nil
}

// GetBranches returns the extant branches for repo (assumed to be "owner/repo-name")
func (ghc *GitHubClient) GetBranches(ctx context.Context, repo string) ([]BranchInfo, error) {
	rs := strings.Split(repo, "/")
	if len(rs) != 2 {
		return nil, fmt.Errorf("malformed repo: %v", repo)
	}
	output := []BranchInfo{}
	blopt := github.BranchListOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		ctx, cf := context.WithTimeout(ctx, ghTimeout)
		defer cf()
		r, resp, err := ghc.getClient(ctx).Repositories.ListBranches(ctx, rs[0], rs[1], &blopt)
		if err != nil {
			return nil, fmt.Errorf("error listing branches: %v", err)
		}
		for _, b := range r {
			output = append(output, BranchInfo{
				Name: *b.Name,
				SHA:  *b.Commit.SHA,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		blopt.Page = resp.NextPage
	}
	return output, nil
}

// GetTags returns the extant tags for repo (assumed to be "owner/repo-name")
func (ghc *GitHubClient) GetTags(ctx context.Context, repo string) ([]BranchInfo, error) {
	rs := strings.Split(repo, "/")
	if len(rs) != 2 {
		return nil, fmt.Errorf("malformed repo: %v", repo)
	}
	output := []BranchInfo{}
	lopt := github.ListOptions{PerPage: 100}
	for {
		ctx, cf := context.WithTimeout(ctx, ghTimeout)
		defer cf()
		r, resp, err := ghc.getClient(ctx).Repositories.ListTags(ctx, rs[0], rs[1], &lopt)
		if err != nil {
			return nil, fmt.Errorf("error listing branches: %v", err)
		}
		for _, b := range r {
			output = append(output, BranchInfo{
				Name: *b.Name,
				SHA:  *b.Commit.SHA,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		lopt.Page = resp.NextPage
	}
	return output, nil
}

// SetStatus creates or updates a status on repo at commit sha
// Note that the github client CreateStatus function will bail if the context was canceled. It is often recommended to pass a fresh context to this function.
func (ghc *GitHubClient) SetStatus(ctx context.Context, repo string, sha string, status *CommitStatus) error {
	rs := strings.Split(repo, "/")
	if len(rs) != 2 {
		return fmt.Errorf("malformed repo: %v", repo)
	}
	gs := &github.RepoStatus{
		State:       &status.Status,
		TargetURL:   &status.TargetURL,
		Description: &status.Description,
		Context:     &status.Context,
	}
	ctx, cf := context.WithTimeout(ctx, ghTimeout)
	defer cf()
	_, _, err := ghc.getClient(ctx).Repositories.CreateStatus(ctx, rs[0], rs[1], sha, gs)
	return err
}

// GetPRStatus returns the status of a PR on a repo
func (ghc *GitHubClient) GetPRStatus(ctx context.Context, repo string, pr uint) (string, error) {
	rs := strings.Split(repo, "/")
	if len(rs) != 2 {
		return "", fmt.Errorf("malformed repo: %v", repo)
	}
	ctx, cf := context.WithTimeout(ctx, ghTimeout)
	defer cf()
	pullreq, _, err := ghc.getClient(ctx).PullRequests.Get(ctx, rs[0], rs[1], int(pr))
	if err != nil {
		return "", err
	}
	if pullreq == nil || pullreq.State == nil {
		return "", fmt.Errorf("pull request is nil")
	}
	return *pullreq.State, nil
}

// GetCommitMessage returns the git message associated with a particular commit
func (ghc *GitHubClient) GetCommitMessage(ctx context.Context, repo string, sha string) (string, error) {
	rs := strings.Split(repo, "/")
	if len(rs) != 2 {
		return "", fmt.Errorf("malformed repo: %v", repo)
	}
	ctx, cf := context.WithTimeout(ctx, ghTimeout)
	defer cf()
	rc, _, err := ghc.getClient(ctx).Repositories.GetCommit(ctx, rs[0], rs[1], sha, &github.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("error getting commit info: %v", err)
	}
	return *rc.Commit.Message, nil
}

// MaxFileDownloadSizeBytes contains the size limit for file content downloads. Attempting to GetFileContents for a file larger than this will return an error.
var MaxFileDownloadSizeBytes = 500 * 1000000

// GetFileContents returns the file contents of path (which must be a regular file or a symlink to a regular file) in the specified repo at ref
func (ghc *GitHubClient) GetFileContents(ctx context.Context, repo string, path string, ref string) ([]byte, error) {
	rs := strings.Split(repo, "/")
	if len(rs) != 2 {
		return nil, fmt.Errorf("malformed repo: %v", repo)
	}
	retries := 3
	var err error
	var fc *github.RepositoryContent
	for i := 0; i < retries; i++ {
		if err != nil {
			eventlogger.GetLogger(ctx).Printf("ghclient: GetFileContents: GetContents retry (%v/%v), prev error: %v", i+1, retries, err)
			time.Sleep(20 * time.Millisecond)
		}
		ctx2, cf := context.WithTimeout(ctx, ghTimeout)
		defer cf()
		fc, _, _, err = ghc.getClient(ctx).Repositories.GetContents(ctx2, rs[0], rs[1], path, &github.RepositoryContentGetOptions{Ref: ref})
		if err != nil {
			err = errors.Wrap(err, "error getting GitHub repo contents")
			continue
		}
		break
	}
	if err != nil {
		return nil, err
	}
	if fc.GetSize() > MaxFileDownloadSizeBytes {
		return nil, fmt.Errorf("file size exceeds max (%v bytes): %v", MaxFileDownloadSizeBytes, fc.GetSize())
	}
	switch {
	case fc.GetType() == "file" || fc.GetType() == "symlink":
		if fc.GetTarget() != "" {
			return nil, fmt.Errorf("path is a symlink and target isn't a normal file: %v", fc.GetTarget())
		}
		if fc.GetSize() <= 1000*1024 {
			c, err := fc.GetContent()
			if err != nil {
				return nil, fmt.Errorf("error decoding GitHub repo contents: %v", err)
			}
			return []byte(c), nil
		}
		rc, resp, err := ghc.getClient(ctx).Repositories.DownloadContents(ctx, rs[0], rs[1], path, &github.RepositoryContentGetOptions{Ref: ref})
		if err != nil {
			return nil, errors.Wrap(err, "error downloading file")
		}
		defer rc.Close()
		c, err := ioutil.ReadAll(rc)
		if err != nil {
			return nil, errors.Wrap(err, "error reading file contents")
		}
		if resp.StatusCode > 299 || resp.StatusCode < 200 {
			return nil, fmt.Errorf("unexpected status code downloading file content: %v: %v", resp.StatusCode, string(c))
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unsupported type for path: %v", fc.GetType())
	}
}

// FileContents models a file from a repository
type FileContents struct {
	Path, SymlinkTarget string
	Symlink             bool
	Contents            []byte
}

// GetDirectoryContents fetches path from repo at ref, returning a map of file path to file contents. If path is not a directory, an error is returned.
// The contents may include symlinks that point to arbitrary locations (ie, outside of the directory root).
func (ghc *GitHubClient) GetDirectoryContents(ctx context.Context, repo, path, ref string) (map[string]FileContents, error) {
	rs := strings.Split(repo, "/")
	if len(rs) != 2 {
		return nil, fmt.Errorf("malformed repo: %v", repo)
	}
	// recursively fetch all directories and files
	var getDirContents func(dirpath string) (map[string]FileContents, error)
	getDirContents = func(dirpath string) (map[string]FileContents, error) {
		retries := 3
		var err error
		var dc []*github.RepositoryContent
		for i := 0; i < retries; i++ {
			if err != nil {
				eventlogger.GetLogger(ctx).Printf("ghclient: GetDirectoryContents: GetContents retry (%v/%v), prev error: %v", i+1, retries, err)
				time.Sleep(1 * time.Second)
			}
			ctx2, cf := context.WithTimeout(ctx, ghTimeout)
			defer cf()
			_, dc, _, err = ghc.getClient(ctx).Repositories.GetContents(ctx2, rs[0], rs[1], dirpath, &github.RepositoryContentGetOptions{Ref: ref})
			if err != nil {
				err = errors.Wrap(err, "error getting GitHub repo contents")
				continue
			}
			break
		}
		if err != nil {
			return nil, err
		}
		if dc == nil {
			return nil, fmt.Errorf("directory contents is nil, path may not be a directory: %v", path)
		}
		output := map[string]FileContents{}
		getFile := func(fc *github.RepositoryContent) error {
			var err error
			var c []byte
			var ghresp *github.Response
			var cts io.ReadCloser
			for i := 0; i < retries; i++ {
				if err != nil {
					eventlogger.GetLogger(ctx).Printf("ghclient: GetDirectoryContents: getFile retry (%v/%v), prev error: %v", i+1, retries, err)
					time.Sleep(1 * time.Second)
				}
				ctx2, cf := context.WithTimeout(ctx, ghTimeout)
				defer cf()
				cts, ghresp, err = ghc.getClient(ctx).Repositories.DownloadContents(ctx2, rs[0], rs[1], fc.GetPath(), &github.RepositoryContentGetOptions{Ref: ref})
				if err != nil {
					err = errors.Wrap(err, "error downloading contents")
					continue
				}
				defer cts.Close()
				c, err = ioutil.ReadAll(cts)
				if err != nil {
					err = errors.Wrapf(err, "error reading HTTP body for file: %v", fc.GetPath())
					continue
				}
				if ghresp.StatusCode > 299 || ghresp.StatusCode < 200 {
					err = fmt.Errorf("unexpected status code downloading file content: %v: %v", ghresp.StatusCode, string(c))
					continue
				}
				if len(c) != fc.GetSize() {
					err = fmt.Errorf("unexpected size for %v: %v (wanted %v)", fc.GetPath(), len(c), fc.GetSize())
					continue
				}
				output[fc.GetPath()] = FileContents{
					Path:     fc.GetPath(),
					Contents: c,
				}
				return nil
			}
			return err
		}
		for _, fc := range dc {
			switch fc.GetType() {
			case "file":
				if err := getFile(fc); err != nil {
					return nil, err
				}
			case "dir":
				out, err := getDirContents(fc.GetPath())
				if err != nil {
					return nil, errors.Wrapf(err, "error getting directory contents for %v", fc.GetPath())
				}
				for k, v := range out {
					output[k] = v
				}
			case "symlink":
				// if it's a symlink, we have to do another API call to get details
				ctx2, cf := context.WithTimeout(ctx, ghTimeout)
				defer cf()
				fc2, _, _, err := ghc.getClient(ctx).Repositories.GetContents(ctx2, rs[0], rs[1], fc.GetPath(), &github.RepositoryContentGetOptions{Ref: ref})
				if err != nil {
					return nil, errors.Wrap(err, "error getting symlink details")
				}
				if fc2.GetTarget() != "" {
					output[fc.GetPath()] = FileContents{
						Path:          fc2.GetPath(),
						SymlinkTarget: fc2.GetTarget(),
						Symlink:       true,
					}
					continue
				}
				if err := getFile(fc2); err != nil {
					return nil, err
				}
			default:
				// ignore any other types (submodule, etc)
				continue
			}
		}
		return output, nil
	}
	return getDirContents(path)
}

// GetRepoArchive fetches repo at ref as a gzip-compressed tar archive and returns the local filesystem path, or error
// It is the caller's responsibility to clean up the file when finished
func (ghc *GitHubClient) GetRepoArchive(ctx context.Context, repo, ref string) (string, error) {
	rs := strings.SplitN(repo, "/", 2)
	opt := &github.RepositoryContentGetOptions{
		Ref: ref,
	}
	aurl, resp, err := ghc.c.Repositories.GetArchiveLink(ctx, rs[0], rs[1], github.Tarball, opt, false)
	if err != nil {
		return "", fmt.Errorf("error getting archive link: %v", err)
	}
	if resp.StatusCode > 399 {
		return "", fmt.Errorf("error status when getting archive link: %v", resp.Status)
	}
	if aurl == nil {
		return "", fmt.Errorf("url is nil")
	}
	hr, err := http.NewRequest("GET", aurl.String(), nil)
	if err != nil {
		return "", fmt.Errorf("error creating http request: %v", err)
	}
	f, err := ioutil.TempFile("", "github-getrepoarchive-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("error creating temp file: %w", err)
	}
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	resp2, err := ghc.rhc.Do(ctx, hr)
	if err != nil {
		return f.Name(), fmt.Errorf("error performing archive http request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode > 299 {
		err = fmt.Errorf("archive http request failed: %v", resp.StatusCode)
		return f.Name(), err
	}
	_, err = io.Copy(f, resp2.Body)
	if err != nil {
		return "", fmt.Errorf("error writing to temp file: %w", err)
	}
	return f.Name(), nil
}
