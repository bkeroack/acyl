package images

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dollarshaveclub/acyl/pkg/eventlogger"
	"github.com/dollarshaveclub/acyl/pkg/metrics"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/dollarshaveclub/furan/rpcclient"
)

func TestFuranImageBackendBuildImage(t *testing.T) {
	var i int
	bf := func() (*rpcclient.BuildEvent, error) {
		defer func() { i++ }()
		if i < 5 {
			return &rpcclient.BuildEvent{Message: "building", EventType: rpcclient.BuildEvent_DOCKER_BUILD_STREAM, EventError: &rpcclient.BuildEventError{}}, nil
		}
		if i >= 5 && i < 10 {
			return &rpcclient.BuildEvent{Message: "pushing", EventType: rpcclient.BuildEvent_DOCKER_PUSH_STREAM, EventError: &rpcclient.BuildEventError{}}, nil
		}
		return &rpcclient.BuildEvent{Message: "done", EventType: rpcclient.BuildEvent_DOCKER_PUSH_STREAM, EventError: &rpcclient.BuildEventError{}}, io.EOF
	}
	fc, _ := rpcclient.NewFakeFuranClient(bf)
	dl := persistence.NewFakeDataLayer()
	fib := FuranBuilderBackend{
		dl:  dl,
		mc:  &metrics.FakeCollector{},
		ibp: fc,
	}
	cases := []struct {
		name, envName, githubRepo, imageRepo, ref string
		isError                                   bool
		errContains                               string
	}{
		{"success", "foo-bar", "foo/bar", "foo/bar", "master", false, ""},
		{"empty github repo", "foo-bar", "", "foo/bar", "master", true, "GithubRepo is required"},
		{"empty image repo", "foo-bar", "foo/bar", "", "master", true, "you must specify either a Docker registry"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() { i = 0 }()
			el := &eventlogger.Logger{DL: dl}
			el.Init([]byte{}, c.githubRepo, 99)
			ctx := eventlogger.NewEventLoggerContext(context.Background(), el)
			if err := fib.BuildImage(ctx, c.envName, "foo-bar", c.githubRepo, c.imageRepo, c.ref, BuildOptions{}); err != nil {
				if c.isError {
					if !strings.Contains(err.Error(), c.errContains) {
						t.Fatalf("error missing string (%v): %v", c.errContains, err)
					}
				} else {
					t.Fatalf("should have succeeded: %v", err)
				}
			}
		})
	}
}
