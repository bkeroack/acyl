package ghapp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/bkeroack/acyl/pkg/models"

	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
)

// PRCallback is a function that gets called when a validated, parsed PR webhook event is received
// - action is the PR webhook action string ("opened", "closed", "labeled", etc), see https://developer.github.com/v3/activity/events/types/#pullrequestevent
// - rrd is the parsed repo/revision information from the webhook payload
// - ctx is pre-populated with an eventlogger and authenticated GitHub clients (app and installation)
// If the callback returns a non-nil error, the webhook request client will be returned a 500 error with the error details in the body
// If the error is nil, the webhook client will be returned a 202 Accepted response with the eventlog ID
type PRCallback func(ctx context.Context, action string, rrd models.RepoRevisionData) error

// GitHubApp implements a GitHub app
type GitHubApp struct {
	cfg githubapp.Config
	prh *prEventHandler
	ch  *checksEventHandler
}

var (
	GitHubV3APIURL = "https://api.github.com/"
	GitHubV4APIURL = "https://api.github.com/graphql"
)

// NewGitHubApp returns a GitHubApp with the given private key, app ID and webhook secret, or error
// supportedPRActions is at least one PR webhook action that is supported (prcallback will be executed).
// Any unsupported actions will be ignored and the webhook request will be responded with 200 OK, "action not relevant".
func NewGitHubApp(privateKeyPEM []byte, appID uint, webhookSecret string, supportedPRActions []string, prcb PRCallback, dl persistence.DataLayer) (*GitHubApp, error) {
	if len(privateKeyPEM) == 0 {
		return nil, errors.New("invalid private key")
	}
	if appID == 0 {
		return nil, errors.New("invalid app ID")
	}
	if len(webhookSecret) == 0 {
		return nil, errors.New("invalid webhook secret")
	}
	if dl == nil {
		return nil, errors.New("DataLayer is required")
	}
	if prcb == nil {
		return nil, errors.New("PR callback is required")
	}
	if len(supportedPRActions) == 0 {
		return nil, errors.New("at least one supported PR action is required")
	}
	sa := make(map[string]struct{}, len(supportedPRActions))
	for _, a := range supportedPRActions {
		sa[a] = struct{}{}
	}
	c := githubapp.Config{}
	c.V3APIURL = GitHubV3APIURL
	c.V4APIURL = GitHubV4APIURL
	c.App.IntegrationID = int64(appID)
	c.App.PrivateKey = string(privateKeyPEM)
	c.App.WebhookSecret = webhookSecret
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 60 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 60 * time.Second,
	}
	cc, err := githubapp.NewDefaultCachingClientCreator(c,
		githubapp.WithClientTimeout(60*time.Second),
		githubapp.WithTransport(tr))
	if err != nil {
		return nil, errors.Wrap(err, "error initializing github app default client")
	}
	return &GitHubApp{
		prh: &prEventHandler{
			ClientCreator:      cc,
			dl:                 dl,
			supportedPRActions: sa,
			RRDCallback:        prcb,
		},
		ch: &checksEventHandler{
			ClientCreator: cc,
		},
		cfg: c,
	}, nil
}

// Handler returns the http.Handler that should handle the webhook HTTP endpoint
func (gha *GitHubApp) Handler() http.Handler {
	return githubapp.NewEventDispatcher(
		[]githubapp.EventHandler{gha.prh, gha.ch},
		gha.cfg.App.WebhookSecret,
		githubapp.WithErrorCallback(func(w http.ResponseWriter, r *http.Request, err error) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Header().Add("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"error_details":"%v"}`, err)))
		}))
}
