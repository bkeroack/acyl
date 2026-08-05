package ghevent

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/bkeroack/acyl/pkg/eventlogger"
	"github.com/bkeroack/acyl/pkg/ghclient"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// ActionType enumerates the different actions we need to take
//go:generate stringer -type=ActionType
type ActionType int

// Various action types
const (
	NotRelevant ActionType = iota // This action is not relevant, ignore
	CreateNew                     // Create a new QA
	Update                        // Destroy and replace an existing QA
	Destroy                       // Destroy an existing QA
)

// GitHub actions that are relevant
var supportedActions = map[string]ActionType{
	"opened":      CreateNew,
	"reopened":    CreateNew,
	"synchronize": Update,
	"closed":      Destroy,
}

// BadSignature is the error type for invalid signatures
type BadSignature struct{}

func (bs BadSignature) Error() string {
	return "invalid hub signature"
}

// GitHubPRReference represents a PR head/base reference
type GitHubPRReference struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// GitHubEventRepository models a repository in a GH webhook
type GitHubEventRepository struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

// GitHubEventPullRequest models a pull request in a GH webhook
type GitHubEventPullRequest struct {
	Number uint              `json:"number"`
	User   GitHubEventUser   `json:"user"`
	Head   GitHubPRReference `json:"head"`
	Base   GitHubPRReference `json:"base"`
}

// GitHubEventUser models a user in a GH webhook
type GitHubEventUser struct {
	Login string `json:"login"`
}

// GitHubEventType enumerates the types of webhook events we support
//go:generate stringer -type=GitHubEventType
type GitHubEventType int

const (
	// UnknownEvent is an unknown webhook event
	UnknownEvent GitHubEventType = iota
	// PullRequestEvent is a PR event
	PullRequestEvent
)

// GitHubEvent represents an incoming GitHub webhook payload
type GitHubEvent struct {
	Action      string                 `json:"action"`       //PR
	Repository  GitHubEventRepository  `json:"repository"`   //PR/Push
	PullRequest GitHubEventPullRequest `json:"pull_request"` //PR
	Sender      GitHubEventUser
}

// Type returns the event type
func (event GitHubEvent) Type() GitHubEventType {
	switch {
	case event.Action != "":
		return PullRequestEvent
	}
	return UnknownEvent
}

// WebhookEventHandler describes an object that can handle incoming webhooks
type WebhookEventHandler interface {
	GenerateSignatureString(body []byte) string
	New(body []byte, sig string) (ActionType, *models.RepoRevisionData, error)
}

// GitHubEventWebhook is an object that processes incoming GitHub webhooks
type GitHubEventWebhook struct {
	rc       ghclient.RepoClient
	dl       persistence.EventLoggerDataLayer
	secret   string // Hook secret
	typepath string
}

// NewGitHubEventWebhook returns a GitHubEventWebhook object using the supplied dependencies
func NewGitHubEventWebhook(rc ghclient.RepoClient, secret string, typepath string, dl persistence.EventLoggerDataLayer) *GitHubEventWebhook {
	return &GitHubEventWebhook{
		rc:       rc,
		dl:       dl,
		secret:   secret,
		typepath: typepath,
	}
}

// GenerateSignatureString calculates the hub signature string for the supplied body
func (ge *GitHubEventWebhook) GenerateSignatureString(body []byte) string {
	mac := hmac.New(sha1.New, []byte(ge.secret))
	mac.Write(body)
	expectedMAC := mac.Sum(nil)
	return "sha1=" + hex.EncodeToString(expectedMAC)
}

func (ge *GitHubEventWebhook) validateHubSignature(body []byte, sig string) bool {
	expectedSig := ge.GenerateSignatureString(body)
	return hmac.Equal([]byte(expectedSig), []byte(sig))
}

// checkRelevancyPR checks relevancy for PR events
func (ge *GitHubEventWebhook) checkRelevancyPR(log func(string, ...interface{}), event *GitHubEvent, qat *models.QAType) bool {
	if _, ok := supportedActions[event.Action]; !ok {
		log("unsupported PR action: %v", event.Action)
		return false
	}
	if qat.TargetBranch != "" {
		return qat.TargetBranch == event.PullRequest.Base.Ref
	}
	for _, tb := range qat.TargetBranches {
		if tb == event.PullRequest.Base.Ref {
			log("PR base branch found in target branches: %v", tb)
			return true
		}
	}
	log("PR base branch not found in target branches: %v", event.PullRequest.Base.Ref)
	return false
}

func (ge *GitHubEventWebhook) generateRepoMetadataPR(event *GitHubEvent) *models.RepoRevisionData {
	return &models.RepoRevisionData{
		User:         event.PullRequest.User.Login,
		Repo:         event.Repository.FullName,
		PullRequest:  event.PullRequest.Number,
		SourceSHA:    event.PullRequest.Head.SHA,
		BaseSHA:      event.PullRequest.Base.SHA,
		SourceBranch: event.PullRequest.Head.Ref,
		BaseBranch:   event.PullRequest.Base.Ref,
	}
}

// getRelevantType returns the QAType (parsed from acyl.yml) for the repo
func (ge *GitHubEventWebhook) getRelevantType(repo string, ref string) (*models.QAType, error) {
	tb, err := ge.rc.GetFileContents(context.Background(), repo, ge.typepath, ref)
	if err != nil {
		return nil, fmt.Errorf("error fetching acyl.yml: %v", err)
	}
	qat := models.QAType{}
	err = qat.FromYAML(tb)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling acyl.yml: %v", err)
	}
	qat.TargetRepo = repo
	return &qat, nil
}

type WebhookResponse struct {
	Action ActionType
	RRD    *models.RepoRevisionData
	Logger *eventlogger.Logger
}

func (ge *GitHubEventWebhook) getlogger(body []byte, repo string, pr uint, deliveryID uuid.UUID) (*eventlogger.Logger, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, errors.Wrap(err, "error getting random UUID")
	}
	logger := &eventlogger.Logger{
		ID:         id,
		DeliveryID: deliveryID,
		DL:         ge.dl,
		Sink:       os.Stdout,
	}
	if err := logger.Init(body, repo, pr); err != nil {
		return nil, errors.Wrap(err, "error initializing event logger")
	}
	return logger, nil
}

// New processes a new incoming request body from GitHub, validates it and returns
// necessary info to create a QA if needed. First param will be false if event is not
// relevant but there is no error otherwise.
func (ge *GitHubEventWebhook) New(body []byte, deliveryID uuid.UUID, sig string) (WebhookResponse, error) {
	out := WebhookResponse{Action: NotRelevant}
	if !ge.validateHubSignature(body, sig) {
		return out, BadSignature{}
	}
	event := GitHubEvent{}
	err := json.Unmarshal(body, &event)
	if err != nil {
		return out, fmt.Errorf("error unmarshaling event: %v", err)
	}

	logger, err := ge.getlogger(body, event.Repository.FullName, event.PullRequest.Number, deliveryID)
	if err != nil {
		return out, errors.Wrap(err, "error setting event logger")
	}
	out.Logger = logger
	log := logger.Printf

	if event.Type() != PullRequestEvent {
		log("not a PR event; ignoring")
		return out, nil
	}

	if _, ok := supportedActions[event.Action]; !ok {
		log("PR event but unsupported action: %v", event)
		return out, nil
	}

	if event.PullRequest.Head.SHA == "" {
		log("no head SHA found, aborting")
		return out, fmt.Errorf("malformed payload: no SHA found")
	}

	headsha := event.PullRequest.Head.SHA

	log("fetching acyl.yml for %v@%v", event.Repository.FullName, headsha)

	qat, err := ge.getRelevantType(event.Repository.FullName, headsha)
	if err != nil {
		log("error fetching acyl.yml for webhook: %v", err)
		return out, fmt.Errorf("error fetching acyl.yml: %v", err)
	}

	at := supportedActions[event.Action]
	if !ge.checkRelevancyPR(log, &event, qat) {
		log("github event: relevancy check failed: %v: %v", at.String(), event)
		return out, nil
	}
	repoData := ge.generateRepoMetadataPR(&event)
	if repoData.SourceBranch == "" {
		log("event.Type(): %v \n body parse SourceBranch is empty: %v \n headsha: %v , QaType.Name: %v", event.Type(), string(body), headsha, qat.Name)
	}
	log("relevant action: %v", at.String())
	out.Action = at
	out.RRD = repoData
	return out, nil
}
