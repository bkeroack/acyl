package ghapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/bkeroack/acyl/pkg/persistence"

	"github.com/bkeroack/acyl/pkg/eventlogger"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/google/go-github/github"
	"github.com/google/uuid"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
)

// prEventHandler is a ClientCreator that handles PR webhook events
type prEventHandler struct {
	githubapp.ClientCreator
	dl                 persistence.DataLayer
	supportedPRActions map[string]struct{}
	RRDCallback        PRCallback
}

// Handles specifies the type of events handled
func (prh *prEventHandler) Handles() []string {
	return []string{"pull_request"}
}

// Handle is called by the handler when an event is received
// The PR event handler validates the webhook, sets up the context with appropriate eventlogger and GH client factory
// and then executes the callback
func (prh *prEventHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {

	// response is used when a non-default response is needed
	response := func(status int, msg string, ctype string) {
		githubapp.SetResponder(ctx, func(w http.ResponseWriter, r *http.Request) {
			if ctype != "" {
				w.Header().Add("Content-Type", ctype)
			}
			w.WriteHeader(status)
			w.Write([]byte(msg))
		})
	}

	if eventType != "pull_request" {
		return errors.New("not a pull request event")
	}

	var event github.PullRequestEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		response(http.StatusBadRequest, fmt.Sprintf("error unmarshaling event: %v", err), "")
		return errors.Wrap(err, "error unmarshaling event")
	}

	did, err := uuid.Parse(deliveryID)
	if err != nil {
		response(http.StatusBadRequest, fmt.Sprintf("malformed delivery id: %v", err), "")
		return errors.Wrap(err, "malformed delivery id")
	}

	rrd := models.RepoRevisionData{
		BaseBranch:   event.GetPullRequest().GetBase().GetRef(),
		BaseSHA:      event.GetPullRequest().GetBase().GetSHA(),
		PullRequest:  uint(event.GetPullRequest().GetNumber()),
		Repo:         event.GetPullRequest().GetBase().GetRepo().GetFullName(),
		SourceBranch: event.GetPullRequest().GetHead().GetRef(),
		SourceRef:    event.GetPullRequest().GetHead().GetRef(),
		SourceSHA:    event.GetPullRequest().GetHead().GetSHA(),
		User:         event.GetPullRequest().GetUser().GetLogin(),
		IsFork:       event.GetPullRequest().GetHead().GetRepo().GetFork(),
	}
	action := event.GetAction()

	if rrd.IsFork {
		response(http.StatusOK, "ignoring event from forked HEAD repo", "")
		return nil
	}

	_, ok := prh.supportedPRActions[action]
	if !ok {
		response(http.StatusOK, "action not relevant: "+action, "")
		return nil
	}

	elogger, err := prh.getlogger(payload, did, rrd.Repo, rrd.PullRequest)
	if err != nil {
		return errors.Wrap(err, "error getting event logger")
	}

	// set up context
	ctx = eventlogger.NewEventLoggerContext(ctx, elogger)
	ctx = NewGitHubClientContext(ctx, event.GetInstallation().GetID(), prh)

	err = prh.RRDCallback(ctx, action, rrd)
	if err != nil {
		response(http.StatusInternalServerError, fmt.Sprintf(`{"error_details":"%v"}`, err), "application/json")
	} else {
		response(http.StatusAccepted, fmt.Sprintf(`{"event_log_id": "%v"}`, eventlogger.GetLogger(ctx).ID.String()), "application/json")
	}
	return err
}

func (prh *prEventHandler) getlogger(body []byte, deliveryID uuid.UUID, repo string, pr uint) (*eventlogger.Logger, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, errors.Wrap(err, "error getting random UUID")
	}
	logger := &eventlogger.Logger{
		ID:         id,
		DeliveryID: deliveryID,
		DL:         prh.dl,
		Sink:       os.Stdout,
	}
	if err := logger.Init(body, repo, pr); err != nil {
		return nil, errors.Wrap(err, "error initializing event logger")
	}
	return logger, nil
}
