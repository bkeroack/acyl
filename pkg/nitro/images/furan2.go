package images

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/dollarshaveclub/acyl/pkg/eventlogger"
	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/metrics"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	furan "github.com/dollarshaveclub/furan/v2/pkg/client"
	"github.com/dollarshaveclub/furan/v2/pkg/generated/furanrpc"
)

type Furan2BuilderBackend struct {
	dl          persistence.DataLayer
	mc          metrics.Collector
	rb          *furan.RemoteBuilder
	rac         ghclient.RepoAppClient
	ghappInstID int64
}

var _ BuilderBackend = &Furan2BuilderBackend{}

func NewFuran2BuilderBackend(addr, apikey string, ghappInstID int64, skipVerifyTLS bool, dl persistence.DataLayer, rac ghclient.RepoAppClient, mc metrics.Collector) (*Furan2BuilderBackend, error) {
	rb, err := furan.New(furan.Options{
		Address:               addr,
		APIKey:                apikey,
		TLSInsecureSkipVerify: skipVerifyTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("error creating Furan client: %w", err)
	}
	return &Furan2BuilderBackend{
		dl:          dl,
		mc:          mc,
		rb:          rb,
		rac:         rac,
		ghappInstID: ghappInstID,
	}, nil
}

// BuildImage synchronously builds the image using Furan, returning when the build completes.
func (fib *Furan2BuilderBackend) BuildImage(ctx context.Context, envName, depName, githubRepo, imageRepo, ref string, ops BuildOptions) error {
	logger := eventlogger.GetLogger(ctx)

	// Furan 2 (via BuildKit) only supports image builds with files named "Dockerfile" or "dockerfile"
	if ops.DockerfilePath != "" && !strings.Contains(ops.DockerfilePath, "Dockerfile") && !strings.Contains(ops.DockerfilePath, "dockerfile") {
		return fmt.Errorf("image build requires a file named Dockerfile or dockerfile")
	}
	ops.DockerfilePath = filepath.Dir(ops.DockerfilePath)

	if ops.BuildArgs == nil {
		ops.BuildArgs = make(map[string]string)
	}
	ops.BuildArgs["GIT_COMMIT_SHA"] = ref

	tkn, err := fib.rac.GetInstallationTokenForRepo(ctx, fib.ghappInstID, githubRepo)
	if err != nil {
		return fmt.Errorf("error creating github app installation token: %w", err)
	}

	req := furanrpc.BuildRequest{
		Build: &furanrpc.BuildDefinition{
			GithubRepo:       githubRepo,
			GithubCredential: tkn,
			Ref:              ref,
			Tags:             []string{ref},
			DockerfilePath:   ops.DockerfilePath,
			Args:             ops.BuildArgs,
		},
		Push: &furanrpc.PushDefinition{
			Registries: []*furanrpc.PushRegistryDefinition{
				&furanrpc.PushRegistryDefinition{
					Repo: imageRepo,
				},
			},
		},
		SkipIfExists: true,
	}
	fib.dl.AddEvent(ctx, envName, fmt.Sprintf("building container: %v:%v", githubRepo, ref))

	err = nil
	defer fib.mc.TimeContainerBuild(envName, githubRepo, ref, githubRepo, ref, &err)()

	retries := 1
	var buildErr error
	for i := 0; i < retries; i++ {
		logger.Printf("starting image build (attempt: %v/%v): %v (ref: %v)", i+1, retries, githubRepo, ref)
		id, err := fib.rb.StartBuild(ctx, req)
		if err != nil {
			buildErr = err
			errmsg := fmt.Sprintf("build failed: %v: %v: %v", githubRepo, id, err)
			logger.Printf(errmsg)
			fib.dl.AddEvent(ctx, envName, errmsg)
			if i != retries-1 {
				fib.dl.AddEvent(ctx, envName, fmt.Sprintf("retrying image build: %v", githubRepo))
			}
			continue
		}

		eventlogger.GetLogger(ctx).SetImageBuildID(depName, id)

		mbc, err := fib.rb.MonitorBuild(ctx, id)
		if err != nil {
			buildErr = err
			errmsg := fmt.Sprintf("monitor build failed: %v: %v: %v", githubRepo, id, err)
			logger.Printf(errmsg)
			fib.dl.AddEvent(ctx, envName, errmsg)
			continue
		}

		for {
			_, err := mbc.Recv()
			if err != nil {
				if err == io.EOF {
					buildErr = nil
					break
				}
				buildErr = err
				errmsg := fmt.Sprintf("getting build event failed: %v: %v: %v", githubRepo, id, err)
				logger.Printf(errmsg)
				fib.dl.AddEvent(ctx, envName, errmsg)
				break
			}
		}

		bs, err := fib.rb.GetBuildStatus(ctx, id)
		if err != nil {
			buildErr = err
			errmsg := fmt.Sprintf("getting getting final build status: %v: %v: %v", githubRepo, id, err)
			logger.Printf(errmsg)
			fib.dl.AddEvent(ctx, envName, errmsg)
			break
		}

		if buildErr != nil {
			continue
		}

		if bs.State != furanrpc.BuildState_SUCCESS && bs.State != furanrpc.BuildState_SKIPPED {
			buildErr = fmt.Errorf("build attempt not successful: %v: %v: %v", githubRepo, id, bs.State)
			logger.Printf(buildErr.Error())
			fib.dl.AddEvent(ctx, envName, buildErr.Error())
			continue
		}

		okmsg := fmt.Sprintf("build finished (%v): %v: %v", bs.State, githubRepo, id)
		logger.Printf(okmsg)
		fib.dl.AddEvent(ctx, envName, okmsg)
		buildErr = nil
		break
	}

	if buildErr != nil {
		return fmt.Errorf("build failed: %v: %w", githubRepo, buildErr)
	}

	return nil
}
