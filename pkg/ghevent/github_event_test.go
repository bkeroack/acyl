package ghevent

import (
	"encoding/json"
	"io/ioutil"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/bkeroack/acyl/pkg/persistence"

	"github.com/bkeroack/acyl/pkg/mocks"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/golang/mock/gomock"
)

const (
	testSecret                 = "secret"
	testTypePath               = "acyl.yaml"
	openedGithubEventJSON      = `{"action":"opened","pull_request":{"number":9577,"user":{"login":"hankjacobs"},"head":{"ref":"hank-test","sha":"5ef2b03b56f088d12a9db27f8039a760d25fb045"},"base":{"ref":"release","sha":"61381b4b0fb0881e7c2a1cacf343abab8a0b3593"}},"repository":{"name":"example-repo","full_name":"dollarshaveclub/example-repo"}}`
	reopenedGithubEventJSON    = `{"action":"reopened","pull_request":{"number":9577,"user":{"login":"hankjacobs"},"head":{"ref":"hank-test","sha":"5ef2b03b56f088d12a9db27f8039a760d25fb045"},"base":{"ref":"release","sha":"61381b4b0fb0881e7c2a1cacf343abab8a0b3593"}},"repository":{"name":"example-repo","full_name":"dollarshaveclub/example-repo"}}`
	synchronizeGithubEventJSON = `{"action":"synchronize","pull_request":{"number":9577,"user":{"login":"hankjacobs"},"head":{"ref":"hank-test","sha":"5ef2b03b56f088d12a9db27f8039a760d25fb045"},"base":{"ref":"release","sha":"61381b4b0fb0881e7c2a1cacf343abab8a0b3593"}},"repository":{"name":"example-repo","full_name":"dollarshaveclub/example-repo"}}`
	closedGithubEventJSON      = `{"action":"closed","pull_request":{"number":9577,"user":{"login":"hankjacobs"},"head":{"ref":"hank-test","sha":"5ef2b03b56f088d12a9db27f8039a760d25fb045"},"base":{"ref":"release","sha":"61381b4b0fb0881e7c2a1cacf343abab8a0b3593"}},"repository":{"name":"example-repo","full_name":"dollarshaveclub/example-repo"}}`
)

var eventActionTestTable = []struct {
	action string
	event  *GitHubEvent
}{
	{action: "opened", event: newDummyOpenedGitHubEvent()},
	{action: "reopened", event: newDummyReopenedGitHubEvent()},
	{action: "synchronize", event: newDummySynchronizeGitHubEvent()},
	{action: "closed", event: newDummyClosedGithubEvent()},
}

var testYAMLBytes = []byte(`name: example-repo
target_repo: dollarshaveclub/example-repo
other_repos:
  - dollarshaveclub/example-repo-2
  - dollarshaveclub/example-repo-3
  - dollarshaveclub/example-repo-4
  - dollarshaveclub/example-repo-5
  - dollarshaveclub/example-repo-6
  - dollarshaveclub/example-repo-7
  - dollarshaveclub/example-repo-8
  - dollarshaveclub/example-repo-9
target_branch: release`)

var testYAMLMasterTargetBytes = []byte(`name: example-repo
target_repo: dollarshaveclub/example-repo
other_repos:
  - dollarshaveclub/example-repo-2
  - dollarshaveclub/example-repo-3
  - dollarshaveclub/example-repo-4
  - dollarshaveclub/example-repo-5
  - dollarshaveclub/example-repo-6
  - dollarshaveclub/example-repo-7
  - dollarshaveclub/example-repo-8
  - dollarshaveclub/example-repo-9
target_branch: master`)

var testYAMLTrackRefsBytes = []byte(`name: example-repo
target_repo: dollarshaveclub/example-repo
other_repos:
  - dollarshaveclub/example-repo-2
  - dollarshaveclub/example-repo-3
  - dollarshaveclub/example-repo-4
  - dollarshaveclub/example-repo-5
  - dollarshaveclub/example-repo-6
  - dollarshaveclub/example-repo-7
  - dollarshaveclub/example-repo-8
  - dollarshaveclub/example-repo-9
target_branch: master
track_refs:
  - heads/master`)

var testYAMLMultipleTargetBranchesBytes = []byte(`name: example-repo
target_repo: dollarshaveclub/example-repo
other_repos:
  - dollarshaveclub/example-repo-2
  - dollarshaveclub/example-repo-3
  - dollarshaveclub/example-repo-4
  - dollarshaveclub/example-repo-5
  - dollarshaveclub/example-repo-6
  - dollarshaveclub/example-repo-7
  - dollarshaveclub/example-repo-8
  - dollarshaveclub/example-repo-9
target_branches:
  - release
  - foo-bar`)

func newDummyRailsSiteQAType() *models.QAType {
	qat := models.QAType{}
	qat.FromYAML(testYAMLBytes)
	return &qat
}

func newDummyOpenedGitHubEvent() *GitHubEvent {
	event := GitHubEvent{}
	_ = json.Unmarshal([]byte(openedGithubEventJSON), &event)
	return &event
}

func newDummyReopenedGitHubEvent() *GitHubEvent {
	event := GitHubEvent{}
	_ = json.Unmarshal([]byte(reopenedGithubEventJSON), &event)
	return &event
}

func newDummySynchronizeGitHubEvent() *GitHubEvent {
	event := GitHubEvent{}
	_ = json.Unmarshal([]byte(synchronizeGithubEventJSON), &event)
	return &event
}

func newDummyClosedGithubEvent() *GitHubEvent {
	event := GitHubEvent{}
	_ = json.Unmarshal([]byte(closedGithubEventJSON), &event)
	return &event
}

func newDummyPushGithubEvent(t *testing.T) ([]byte, *GitHubEvent) {
	event := GitHubEvent{}
	b, err := ioutil.ReadFile("./testdata/example_github_push_payload.json")
	if err != nil {
		t.Fatalf("error reading test payload: %v", err)
	}
	err = json.Unmarshal(b, &event)
	if err != nil {
		t.Fatalf("error unmarshaling test payload: %v", err)
	}
	return b, &event
}

func newTestGitHubEventWebhook(t *testing.T, secret string, typePath string) (*GitHubEventWebhook, *mocks.MockRepoClient, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	rc := mocks.NewMockRepoClient(ctrl)
	dl := persistence.NewFakeDataLayer()
	return NewGitHubEventWebhook(rc, secret, typePath, dl), rc, ctrl
}

func TestValidateHubSignatureSucceedsForValidSig(t *testing.T) {
	hook, _, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()

	sig := "sha1=a18991ff7e4513a1c2d2ee51e3a8e99ca891d9cd"
	valid := hook.validateHubSignature([]byte("body"), sig)

	if !valid {
		t.Fatalf(`Signature "%v" was invalid but should have been valid`, sig)
	}
}

func TestValidateHubSignatureFailsForInvalidSig(t *testing.T) {
	hook, _, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()

	invalidSig := "sha1=this_should_be_invalid"
	valid := hook.validateHubSignature([]byte("body"), invalidSig)

	if valid {
		t.Fatalf(`Signature "%v" was valid but should have been invalid`, invalidSig)
	}
}

func TestEventActionsAreRelevant(t *testing.T) {
	hook, _, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	qat := newDummyRailsSiteQAType()
	log := func(string, ...interface{}) {}

	for _, test := range eventActionTestTable {
		relevant := hook.checkRelevancyPR(log, test.event, qat)
		if !relevant {
			t.Errorf("Event %v for %v was not relevent but should have been", test.event, qat)
		}
	}
}

func TestInvalidEventActionIsIrrelevant(t *testing.T) {
	hook, _, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	qat := newDummyRailsSiteQAType()
	event := newDummyOpenedGitHubEvent()
	event.Action = "bad"
	log := func(string, ...interface{}) {}

	relevant := hook.checkRelevancyPR(log, event, qat)
	if relevant {
		t.Errorf("Event %v for %v was relevent but should not have been", event, qat)
	}
}

func TestMatchingTargetBranchPullRequestBaseIsRelevant(t *testing.T) {
	hook, _, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	qat := newDummyRailsSiteQAType()
	event := newDummyOpenedGitHubEvent()
	event.PullRequest.Base.Ref = qat.TargetBranch
	log := func(string, ...interface{}) {}

	relevant := hook.checkRelevancyPR(log, event, qat)
	if !relevant {
		t.Errorf("Event %v for %v was not relevent but should have been", event, qat)
	}
}

func TestNotMatchingTargetBranchPullRequestBaseIsNotRelevant(t *testing.T) {
	hook, _, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	qat := newDummyRailsSiteQAType()
	event := newDummyOpenedGitHubEvent()
	event.PullRequest.Base.Ref = "mismatch"
	log := func(string, ...interface{}) {}

	relevant := hook.checkRelevancyPR(log, event, qat)
	if relevant {
		t.Errorf("Event %v for %v was relevent but not should have been", event, qat)
	}
}

func TestGetRelevantType(t *testing.T) {
	repo := "dollarshaveclub/example-repo"
	ref := "master"

	hook, rc, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	rc.EXPECT().GetFileContents(gomock.Any(), repo, testTypePath, ref).Return(testYAMLBytes, nil)

	expected := &models.QAType{}
	expected.FromYAML(testYAMLBytes)

	qat, err := hook.getRelevantType(repo, ref)
	if err != nil {
		t.Fatalf("Encountered unexpected error %v", err)
	}

	if !reflect.DeepEqual(expected, qat) {
		t.Fatalf("Expected %v but received %v", expected, qat)
	}
}
func TestGenerateRepoMetadata(t *testing.T) {
	hook, _, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	event := newDummyOpenedGitHubEvent()

	expected := models.RepoRevisionData{
		User:         event.PullRequest.User.Login,
		Repo:         event.Repository.FullName,
		PullRequest:  event.PullRequest.Number,
		SourceSHA:    event.PullRequest.Head.SHA,
		BaseSHA:      event.PullRequest.Base.SHA,
		SourceBranch: event.PullRequest.Head.Ref,
		BaseBranch:   event.PullRequest.Base.Ref,
	}
	rrd := hook.generateRepoMetadataPR(event)

	if *rrd != expected {
		t.Fatalf("Expected %v but received %v", expected, rrd)
	}
}

func TestNewSucceedsOpenedCreateNew(t *testing.T) {
	hook, rc, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	rc.EXPECT().GetFileContents(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(testYAMLBytes, nil)

	out, err := hook.New([]byte(openedGithubEventJSON), uuid.Must(uuid.NewRandom()), "sha1=b76269055fa8fcf1f9c347416986ae7743a9f0b6")
	if err != nil {
		t.Fatalf("Encountered unexpected error %v", err)
	}

	if out.Action != CreateNew {
		t.Fatalf("Expected %v but received %v", CreateNew, out.Action)
	}
}

func TestNewSucceedsOpenedCreateNewTargetBranches(t *testing.T) {
	hook, rc, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	rc.EXPECT().GetFileContents(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(testYAMLMultipleTargetBranchesBytes, nil)

	out, err := hook.New([]byte(openedGithubEventJSON), uuid.Must(uuid.NewRandom()), "sha1=b76269055fa8fcf1f9c347416986ae7743a9f0b6")
	if err != nil {
		t.Fatalf("Encountered unexpected error %v", err)
	}

	if out.Action != CreateNew {
		t.Fatalf("Expected %v but received %v", CreateNew, out.Action)
	}
}

func TestNewSucceedsReopenedCreateNew(t *testing.T) {
	hook, rc, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	rc.EXPECT().GetFileContents(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(testYAMLBytes, nil)

	out, err := hook.New([]byte(reopenedGithubEventJSON), uuid.Must(uuid.NewRandom()), "sha1=c955f478baa65209a06ef86d525445e49d517fe0")
	if err != nil {
		t.Fatalf("Encountered unexpected error %v", err)
	}

	if out.Action != CreateNew {
		t.Fatalf("Expected %v but received %v", CreateNew, out.Action)
	}
}

func TestNewSucceedsSynchronizeUpdate(t *testing.T) {
	hook, rc, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	rc.EXPECT().GetFileContents(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(testYAMLBytes, nil)

	out, err := hook.New([]byte(synchronizeGithubEventJSON), uuid.Must(uuid.NewRandom()), "sha1=ff496315e4c2d899cd3b9463477717703847ba4f")
	if err != nil {
		t.Fatalf("Encountered unexpected error %v", err)
	}

	if out.Action != Update {
		t.Fatalf("Expected %v but received %v", Update, out.Action)
	}
}

func TestNewSucceedsClosedDestroy(t *testing.T) {
	hook, rc, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	rc.EXPECT().GetFileContents(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(testYAMLBytes, nil)

	out, err := hook.New([]byte(closedGithubEventJSON), uuid.Must(uuid.NewRandom()), "sha1=144e32eb6d3e4e7b1d02a16009e2058f3d85c381")
	if err != nil {
		t.Fatalf("Encountered unexpected error %v", err)
	}

	if out.Action != Destroy {
		t.Fatalf("Expected %v but received %v", Destroy, out.Action)
	}
}

func TestNewNotRelevant(t *testing.T) {
	hook, rc, ctrl := newTestGitHubEventWebhook(t, testSecret, testTypePath)
	defer ctrl.Finish()
	rc.EXPECT().GetFileContents(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(testYAMLMasterTargetBytes, nil)

	out, err := hook.New([]byte(closedGithubEventJSON), uuid.Must(uuid.NewRandom()), "sha1=144e32eb6d3e4e7b1d02a16009e2058f3d85c381")
	if err != nil {
		t.Fatalf("Encountered unexpected error %v", err)
	}

	if out.Action != NotRelevant {
		t.Fatalf("Expected %v but received %v", NotRelevant, out.Action)
	}
}
