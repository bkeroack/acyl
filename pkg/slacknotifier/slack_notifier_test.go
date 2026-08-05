package slacknotifier

import (
	"testing"

	"github.com/bkeroack/acyl/pkg/mocks"
	"github.com/golang/mock/gomock"
)

const (
	testSlackChannel     = "testchannel"
	testSlackUser        = "testSlackUser"
	testSlackUserChannel = "@testSlackUser"
)

var testRepoData = &RepoRevisionData{
	User:         "testGithubUser",
	Repo:         "foo/bar",
	PullRequest:  1,
	SourceBranch: "foo",
	BaseBranch:   "master",
}

type TestSlackUsernameMapper struct{}

func (tum *TestSlackUsernameMapper) UsernameFromGithubUsername(githubUsername string) (string, error) {
	if githubUsername == testRepoData.User {
		user := testSlackUser
		return user, nil
	}

	return "", nil
}

func getTestSlackNotifier(t *testing.T) (ChatNotifier, *mocks.MockSlackAPIClient) {
	ctrl := gomock.NewController(t)
	api := mocks.NewMockSlackAPIClient(ctrl)
	return NewSlackNotifier(testSlackChannel, api, &TestSlackUsernameMapper{}), api
}

func TestSlackNotifierCreating(t *testing.T) {
	sn, mapi := getTestSlackNotifier(t)
	mapi.EXPECT().PostMessage(testSlackChannel, gomock.Any(), gomock.Any()).Times(1)
	mapi.EXPECT().PostMessage(testSlackUserChannel, gomock.Any(), gomock.Any()).Times(1)
	err := sn.Creating("somename", testRepoData)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestSlackNotifierDestroying(t *testing.T) {
	sn, mapi := getTestSlackNotifier(t)
	mapi.EXPECT().PostMessage(testSlackChannel, gomock.Any(), gomock.Any()).Times(1)
	mapi.EXPECT().PostMessage(testSlackUserChannel, gomock.Any(), gomock.Any()).Times(1)
	err := sn.Destroying("somename", testRepoData, DestroyApiRequest)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestSlackNotifierUpdating(t *testing.T) {
	sn, mapi := getTestSlackNotifier(t)
	mapi.EXPECT().PostMessage(testSlackChannel, gomock.Any(), gomock.Any()).Times(1)
	mapi.EXPECT().PostMessage(testSlackUserChannel, gomock.Any(), gomock.Any()).Times(1)
	err := sn.Updating("somename", testRepoData, "some commit msg")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestSlackNotifierSuccess(t *testing.T) {
	sn, mapi := getTestSlackNotifier(t)
	mapi.EXPECT().PostMessage(testSlackChannel, gomock.Any(), gomock.Any()).Times(1)
	mapi.EXPECT().PostMessage(testSlackUserChannel, gomock.Any(), gomock.Any()).Times(1)
	err := sn.Success("somename", "foo.shave.io", nil, "amino-qa-12345-foo", testRepoData)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestSlackNotifierLockError(t *testing.T) {
	sn, mapi := getTestSlackNotifier(t)
	mapi.EXPECT().PostMessage(testSlackChannel, gomock.Any(), gomock.Any()).Times(1)
	mapi.EXPECT().PostMessage(testSlackUserChannel, gomock.Any(), gomock.Any()).Times(1)
	err := sn.LockError(testRepoData, "some lock error")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}

func TestSlackNotifierFailure(t *testing.T) {
	sn, mapi := getTestSlackNotifier(t)
	mapi.EXPECT().PostMessage(testSlackChannel, gomock.Any(), gomock.Any()).Times(1)
	mapi.EXPECT().PostMessage(testSlackUserChannel, gomock.Any(), gomock.Any()).Times(1)
	err := sn.Failure("somename", testRepoData, "some failure msg")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
}
