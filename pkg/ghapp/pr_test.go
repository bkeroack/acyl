package ghapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bkeroack/acyl/pkg/eventlogger"

	"github.com/google/go-github/github"

	"github.com/google/uuid"

	"github.com/bkeroack/acyl/pkg/models"

	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/palantir/go-githubapp/githubapp"
)

// fake testing key
var key = `-----BEGIN RSA PRIVATE KEY-----
MIIEpQIBAAKCAQEA0BUezcR7uycgZsfVLlAf4jXP7uFpVh4geSTY39RvYrAll0yh
q7uiQypP2hjQJ1eQXZvkAZx0v9lBYJmX7e0HiJckBr8+/O2kARL+GTCJDJZECpjy
97yylbzGBNl3s76fZ4CJ+4f11fCh7GJ3BJkMf9NFhe8g1TYS0BtSd/sauUQEuG/A
3fOJxKTNmICZr76xavOQ8agA4yW9V5hKcrbHzkfecg/sQsPMmrXixPNxMsqyOMmg
jdJ1aKr7ckEhd48ft4bPMO4DtVL/XFdK2wJZZ0gXJxWiT1Ny41LVql97Odm+OQyx
tcayMkGtMb1nwTcVVl+RG2U5E1lzOYpcQpyYFQIDAQABAoIBAAfUY55WgFlgdYWo
i0r81NZMNBDHBpGo/IvSaR6y/aX2/tMcnRC7NLXWR77rJBn234XGMeQloPb/E8iw
vtjDDH+FQGPImnQl9P/dWRZVjzKcDN9hNfNAdG/R9JmGHUz0JUddvNNsIEH2lgEx
C01u/Ntqdbk+cDvVlwuhm47MMgs6hJmZtS1KDPgYJu4IaB9oaZFN+pUyy8a1w0j9
RAhHpZrsulT5ThgCra4kKGDNnk2yfI91N9lkP5cnhgUmdZESDgrAJURLS8PgInM4
YPV9L68tJCO4g6k+hFiui4h/4cNXYkXnaZSBUoz28ICA6e7I3eJ6Y1ko4ou+Xf0V
csM8VFkCgYEA7y21JfECCfEsTHwwDg0fq2nld4o6FkIWAVQoIh6I6o6tYREmuZ/1
s81FPz/lvQpAvQUXGZlOPB9eW6bZZFytcuKYVNE/EVkuGQtpRXRT630CQiqvUYDZ
4FpqdBQUISt8KWpIofndrPSx6JzI80NSygShQsScWFw2wBIQAnV3TpsCgYEA3reL
L7AwlxCacsPvkazyYwyFfponblBX/OvrYUPPaEwGvSZmE5A/E4bdYTAixDdn4XvE
ChwpmRAWT/9C6jVJ/o1IK25dwnwg68gFDHlaOE+B5/9yNuDvVmg34PWngmpucFb/
6R/kIrF38lEfY0pRb05koW93uj1fj7Uiv+GWRw8CgYEAn1d3IIDQl+kJVydBKItL
tvoEur/m9N8wI9B6MEjhdEp7bXhssSvFF/VAFeQu3OMQwBy9B/vfaCSJy0t79uXb
U/dr/s2sU5VzJZI5nuDh67fLomMni4fpHxN9ajnaM0LyI/E/1FFPgqM+Rzb0lUQb
yqSM/ptXgXJls04VRl4VjtMCgYEAprO/bLx2QjxdPpXGFcXbz6OpsC92YC2nDlsP
3cfB0RFG4gGB2hbX/6eswHglLbVC/hWDkQWvZTATY2FvFps4fV4GrOt5Jn9+rL0U
elfC3e81Dw+2z7jhrE1ptepprUY4z8Fu33HNcuJfI3LxCYKxHZ0R2Xvzo+UYSBqO
ng0eTKUCgYEAxW9G4FjXQH0bjajntjoVQGLRVGWnteoOaQr/cy6oVii954yNMKSP
rezRkSNbJ8cqt9XQS+NNJ6Xwzl3EbuAt6r8f8VO1TIdRgFOgiUXRVNZ3ZyW8Hegd
kGTL0A6/0yAu9qQZlFbaD5bWhQo7eyx63u4hZGppBhkTSPikOYUPCH8=
-----END RSA PRIVATE KEY-----`

func Test_prEventHandler_Handle(t *testing.T) {
	str := func(s string) *string { return &s }
	intp := func(i int) *int { return &i }
	boolp := func(b bool) *bool { return &b }
	basep := github.PullRequestEvent{
		Action: str("opened"),
		Number: intp(1),
		PullRequest: &github.PullRequest{
			Number: intp(1),
			User: &github.User{
				Login: str("john.doe"),
			},
			Head: &github.PullRequestBranch{
				Repo: &github.Repository{
					FullName: str("foo/bar"),
					Fork:     boolp(false),
				},
				Label: str("asdf"),
				Ref:   str("zxcv"),
				SHA:   str("1234"),
			},
			Base: &github.PullRequestBranch{
				Repo: &github.Repository{
					FullName: str("foo/bar"),
				},
				Label: str("qwer"),
				Ref:   str("tyui"),
				SHA:   str("5678"),
			},
		},
	}
	reopenedp := basep
	reopenedp.Action = str("reopened")
	updatep := basep
	updatep.Action = str("synchronize")
	closedp := basep
	closedp.Action = str("closed")
	forkp := basep
	forkp.PullRequest.Head.Repo.FullName = str("someotherowner/bar")
	forkp.PullRequest.Head.Repo.Fork = boolp(true)
	basecb := func(ctx context.Context, action string, rrd models.RepoRevisionData) error {
		if rrd.Repo != "foo/bar" {
			return fmt.Errorf("bad repo: %v", rrd.Repo)
		}
		if rrd.PullRequest != 1 {
			return fmt.Errorf("bad pr: %v", rrd.PullRequest)
		}
		if eventlogger.GetLogger(ctx).ID == uuid.Nil {
			return fmt.Errorf("missing eventlogger")
		}
		if GetGitHubAppClient(ctx, nil) == nil {
			return fmt.Errorf("missing app client")
		}
		if GetGitHubInstallationClient(ctx, nil) == nil {
			return fmt.Errorf("missing installation client")
		}
		return nil
	}
	type args struct {
		deliveryID string
		payload    github.PullRequestEvent
		cb         PRCallback
	}
	tests := []struct {
		name       string
		args       args
		wantErr    bool
		wantErrStr string
	}{
		{
			name: "opened",
			args: args{
				deliveryID: uuid.Must(uuid.NewRandom()).String(),
				payload:    basep,
				cb: func(ctx context.Context, action string, rrd models.RepoRevisionData) error {
					if action != "opened" {
						return fmt.Errorf("bad action: %v", action)
					}
					return basecb(ctx, action, rrd)
				},
			},
		},
		{
			name: "reopened",
			args: args{
				deliveryID: uuid.Must(uuid.NewRandom()).String(),
				payload:    reopenedp,
				cb: func(ctx context.Context, action string, rrd models.RepoRevisionData) error {
					if action != "reopened" {
						return fmt.Errorf("bad action: %v", action)
					}
					return basecb(ctx, action, rrd)
				},
			},
		},
		{
			name: "synchronize",
			args: args{
				deliveryID: uuid.Must(uuid.NewRandom()).String(),
				payload:    updatep,
				cb: func(ctx context.Context, action string, rrd models.RepoRevisionData) error {
					if action != "synchronize" {
						return fmt.Errorf("bad action: %v", action)
					}
					return basecb(ctx, action, rrd)
				},
			},
		},
		{
			name: "closed",
			args: args{
				deliveryID: uuid.Must(uuid.NewRandom()).String(),
				payload:    closedp,
				cb: func(ctx context.Context, action string, rrd models.RepoRevisionData) error {
					if action != "closed" {
						return fmt.Errorf("bad action: %v", action)
					}
					return basecb(ctx, action, rrd)
				},
			},
		},
		{
			name: "invalid payload",
			args: args{
				deliveryID: uuid.Must(uuid.NewRandom()).String(),
				payload:    github.PullRequestEvent{},
			},
			wantErr:    true,
			wantErrStr: "error unmarshaling event",
		},
		{
			name: "invalid delivery id",
			args: args{
				deliveryID: "asdf",
				payload:    basep,
			},
			wantErr:    true,
			wantErrStr: "malformed delivery id",
		},
		{
			name: "forked repo",
			args: args{
				deliveryID: uuid.Must(uuid.NewRandom()).String(),
				payload:    forkp,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := githubapp.Config{}
			c.App.IntegrationID = 10
			c.App.PrivateKey = key
			cc, _ := githubapp.NewDefaultCachingClientCreator(c)
			prh := &prEventHandler{
				ClientCreator: cc,
				dl:            persistence.NewFakeDataLayer(),
				RRDCallback:   tt.args.cb,
			}
			var p []byte
			if tt.args.payload.Action != nil {
				j, err := json.Marshal(&tt.args.payload)
				if err != nil {
					t.Fatalf("error marshaling payload: %v", err)
				}
				p = j
			} else {
				p = []byte("invalid")
			}
			ctx := githubapp.InitializeResponder(context.Background())
			err := prh.Handle(ctx, "pull_request", tt.args.deliveryID, p)
			if (err != nil) != tt.wantErr {
				t.Errorf("prEventHandler.Handle() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), tt.wantErrStr) {
				t.Errorf("unexpected error: %v (wanted containing %v)", err, tt.wantErrStr)
			}
		})
	}
}
