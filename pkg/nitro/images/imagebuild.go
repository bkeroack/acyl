package images

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/eventlogger"
	"github.com/dollarshaveclub/acyl/pkg/models"
	nitroerrors "github.com/dollarshaveclub/acyl/pkg/nitro/errors"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metrics"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/pkg/errors"
)

// BuildOptions models optional image build options
type BuildOptions struct {
	// Relative path to Dockerfile within build context
	DockerfilePath string
	// key-value pairs for optional build arguments
	BuildArgs map[string]string
}

// BuilderBackend describes the object that actually does image builds
type BuilderBackend interface {
	BuildImage(ctx context.Context, envName, depName, githubRepo, imageRepo, ref string, ops BuildOptions) error
}

// Builder describes an object that builds a set of container images
type Builder interface {
	StartBuilds(ctx context.Context, envname string, rc *models.RepoConfig) (Batch, error)
}

// Batch describes a BuildBatch object
type Batch interface {
	Completed(envname, name string) (bool, error)
	Started(envname, name string) bool
	Done() bool
	Stop()
}

type lockingOutcomes struct {
	sync.RWMutex
	started   map[string]struct{}
	completed map[string]error
}

var _ Builder = &ImageBuilder{}

// ImageBuilder is an object that builds images using imageBuilderBackend
// it is intended to be a singleton instance shared among multiple concurrent environment
// creation procedures. Consequently, build IDs contain the name of the environment to avoid collisions
type ImageBuilder struct {
	Backend      BuilderBackend
	BuildTimeout time.Duration
	DL           persistence.DataLayer
	MC           metrics.Collector
}

var DefaultBuildTimeout = 1 * time.Hour

type BuildBatch struct {
	outcomes *lockingOutcomes
	stopf    func()
}

func buildid(envname, name string) string {
	return envname + "-" + name
}

// Completed returns if build for repo has completed along with outcome
func (b *BuildBatch) Completed(envname, name string) (bool, error) {
	b.outcomes.RLock()
	defer b.outcomes.RUnlock()
	err, ok := b.outcomes.completed[buildid(envname, name)]
	return ok, err
}

func (b *BuildBatch) Started(envname, name string) bool {
	b.outcomes.RLock()
	defer b.outcomes.RUnlock()
	_, ok := b.outcomes.started[buildid(envname, name)]
	return ok
}

// Done returns whether all builds in the batch have completed
func (b *BuildBatch) Done() bool {
	b.outcomes.RLock()
	defer b.outcomes.RUnlock()
	return len(b.outcomes.completed) == len(b.outcomes.started)
}

// Stop aborts any running builds in the batch and cleans up orphaned resources
func (b *BuildBatch) Stop() {
	b.stopf()
}

// StartBuilds begins asynchronously building all container images according to rm, pushing to image repositories specified in rc.
func (b *ImageBuilder) StartBuilds(ctx context.Context, envname string, rc *models.RepoConfig) (Batch, error) {
	batch := &BuildBatch{outcomes: &lockingOutcomes{started: make(map[string]struct{}), completed: make(map[string]error)}}
	if envname == "" || rc == nil {
		return batch, errors.New("at least one parameter is nil")
	}
	if b.BuildTimeout == time.Duration(0) {
		b.BuildTimeout = DefaultBuildTimeout
	}
	buildimage := func(ctx context.Context, name, repo, image, ref, dockerfilepath string) {
		batch.outcomes.Lock()
		batch.outcomes.started[buildid(envname, name)] = struct{}{}
		batch.outcomes.Unlock()

		eventlogger.GetLogger(ctx).SetImageStarted(name)

		end := b.MC.Timing("images.build", "repo:"+repo, "triggering_repo:"+rc.Application.Repo)
		err := b.Backend.BuildImage(ctx, envname, name, repo, image, ref, BuildOptions{DockerfilePath: dockerfilepath})
		end(fmt.Sprintf("success:%v", err == nil))

		eventlogger.GetLogger(ctx).SetImageCompleted(name, err != nil)

		batch.outcomes.Lock()
		batch.outcomes.completed[buildid(envname, name)] = nitroerrors.User(err)
		batch.outcomes.Unlock()
	}
	cfs := []context.CancelFunc{}
	ctx2, cf := context.WithTimeout(ctx, b.BuildTimeout)
	cfs = append(cfs, cf)
	go buildimage(ctx2, models.GetName(rc.Application.Repo), rc.Application.Repo, rc.Application.Image, rc.Application.Ref, rc.Application.DockerfilePath)
	for _, d := range rc.Dependencies.All() {
		if d.Repo != "" { // only build images for Repo (branch-matched) dependencies
			ctx3, cf := context.WithTimeout(ctx, b.BuildTimeout)
			cfs = append(cfs, cf)
			go buildimage(ctx3, d.Name, d.Repo, d.AppMetadata.Image, d.AppMetadata.Ref, d.AppMetadata.DockerfilePath)
		}
	}
	stopf := func() {
		for _, cf := range cfs {
			cf()
		}
	}
	batch.stopf = stopf
	return batch, nil
}
