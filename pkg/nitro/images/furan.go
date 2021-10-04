package images

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/dollarshaveclub/acyl/pkg/eventlogger"
	"github.com/dollarshaveclub/acyl/pkg/metrics"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	furan "github.com/dollarshaveclub/furan/rpcclient"
	"github.com/pkg/errors"
)

type FuranBuilderBackend struct {
	dl  persistence.DataLayer
	mc  metrics.Collector
	ibp furan.ImageBuildPusher
}

var _ BuilderBackend = &FuranBuilderBackend{}

func NewFuranBuilderBackend(addrs []string, dl persistence.DataLayer, mc metrics.Collector, logout io.Writer, datadogServiceNamePrefix string) (*FuranBuilderBackend, error) {
	fcopts := &furan.DiscoveryOptions{}
	if len(addrs) == 0 {
		return nil, errors.New("must provide at least a single furan address")
	}
	fcopts.NodeList = addrs
	logger := log.New(logout, "", log.LstdFlags)
	fc, err := furan.NewFuranClient(fcopts, logger, datadogServiceNamePrefix)
	if err != nil {
		return nil, fmt.Errorf("error creating Furan client: %w", err)
	}
	return &FuranBuilderBackend{
		dl:  dl,
		mc:  mc,
		ibp: fc,
	}, nil
}

// BuildImage synchronously builds the image using Furan, returning when the build completes.
func (fib *FuranBuilderBackend) BuildImage(ctx context.Context, envName, depName, githubRepo, imageRepo, ref string, ops BuildOptions) error {
	logger := eventlogger.GetLogger(ctx)
	if ops.DockerfilePath == "" {
		ops.DockerfilePath = "Dockerfile"
	}
	if ops.BuildArgs == nil {
		ops.BuildArgs = make(map[string]string)
	}
	ops.BuildArgs["GIT_COMMIT_SHA"] = ref
	req := furan.BuildRequest{
		Build: &furan.BuildDefinition{
			GithubRepo:     githubRepo,
			Ref:            ref,
			Tags:           []string{ref},
			DockerfilePath: ops.DockerfilePath,
			Args:           ops.BuildArgs,
		},
		Push: &furan.PushDefinition{
			Registry: &furan.PushRegistryDefinition{
				Repo: imageRepo,
			},
			S3: &furan.PushS3Definition{},
		},
		SkipIfExists: true,
	}
	bchan := make(chan *furan.BuildEvent)
	go func() {
		var build, push bool
		for event := range bchan {
			if event.EventType == furan.BuildEvent_DOCKER_BUILD_STREAM && !build {
				logger.Printf("furan: %v: building (build id: %v)", githubRepo, event.BuildId)
				build = true
			}
			if event.EventType == furan.BuildEvent_DOCKER_PUSH_STREAM && !push {
				logger.Printf("furan: %v: pushing (build id: %v)", githubRepo, event.BuildId)
				push = true
			}
		}
	}()
	fib.dl.AddEvent(ctx, envName, fmt.Sprintf("building container: %v:%v", githubRepo, ref))

	var err error
	defer fib.mc.TimeContainerBuild(envName, githubRepo, ref, githubRepo, ref, &err)()

	retries := 1
	var buildErr error
	for i := 0; i < retries; i++ {
		logger.Printf("starting image build (retry: %v): %v (ref: %v)", i+1, githubRepo, ref)
		id, err := fib.ibp.Build(ctx, bchan, &req)
		if err != nil {
			if err == furan.ErrCanceled {
				break // suppress error
			}
			buildErr = err
			errmsg := fmt.Sprintf("build failed: %v: %v: %v", githubRepo, id, err)
			logger.Printf(errmsg)
			fib.dl.AddEvent(ctx, envName, errmsg)
			if i != retries-1 {
				fib.dl.AddEvent(ctx, envName, fmt.Sprintf("retrying image build: %v", githubRepo))
			}
			continue
		}

		okmsg := fmt.Sprintf("build finished: %v: %v", githubRepo, id)
		logger.Printf(okmsg)
		fib.dl.AddEvent(ctx, envName, okmsg)
		buildErr = nil
		break
	}
	close(bchan)

	if buildErr != nil {
		return fmt.Errorf("build failed: %v: %w", githubRepo, buildErr)
	}
	return nil
}
