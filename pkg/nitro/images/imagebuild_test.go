package images

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metrics"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
)

type testImageBuildBackend struct {
	f func(ctx context.Context, envName, repo, imagerepo, ref string, ops BuildOptions) error
}

func (tib *testImageBuildBackend) BuildImage(ctx context.Context, envName, depName, githubRepo, imageRepo, ref string, ops BuildOptions) error {
	return tib.f(ctx, envName, githubRepo, imageRepo, ref, ops)
}

var _ BuilderBackend = &testImageBuildBackend{}

func newTestBuilder(f func(ctx context.Context, envName, repo, imagerepo, ref string, ops BuildOptions) error) *ImageBuilder {
	return &ImageBuilder{
		DL:      persistence.NewFakeDataLayer(),
		Backend: &testImageBuildBackend{f: f},
		MC:      &metrics.FakeCollector{},
	}
}

func TestImageBuilderStartBuildsSimple(t *testing.T) {
	f := func(ctx context.Context, envName, repo, imagerepo, ref string, ops BuildOptions) error {
		return nil
	}
	ib := newTestBuilder(f)
	rc := &models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:   "foo/bar",
			Ref:    "abcdef",
			Branch: "master",
			Image:  "quay.io/foo/bar",
		},
	}
	envname := "this-is-a-name"
	b, err := ib.StartBuilds(context.Background(), envname, rc)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	defer b.Stop()
	time.Sleep(5 * time.Millisecond)
	done, err := b.Completed(envname, models.GetName(rc.Application.Repo))
	if !done {
		t.Fatalf("should be done")
	}
	if err != nil {
		t.Fatalf("build should have succeeded: %v", err)
	}
}

func TestImageBuilderStartBuildsError(t *testing.T) {
	f := func(ctx context.Context, envName, repo, imagerepo, ref string, ops BuildOptions) error {
		if repo == "foo/bar2" {
			return errors.New("build error")
		}
		return nil
	}
	ib := newTestBuilder(f)
	rc := &models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:   "foo/bar",
			Ref:    "abcdef",
			Branch: "master",
			Image:  "quay.io/foo/bar",
		},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "bar2",
					Repo: "foo/bar2",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:   "foo2/bar2",
						Ref:    "abcdef",
						Branch: "master",
						Image:  "quay.io/foo2/bar2",
					},
				},
			},
		},
	}
	envname := "env-name"
	b, err := ib.StartBuilds(context.Background(), envname, rc)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	defer b.Stop()
	time.Sleep(5 * time.Millisecond)
	done, err := b.Completed(envname, models.GetName(rc.Application.Repo))
	if !done {
		t.Fatalf("should be done")
	}
	if err != nil {
		t.Fatalf("build should have succeeded: %v", err)
	}
	done, err = b.Completed(envname, "bar2")
	if !done {
		t.Fatalf("should be done")
	}
	if err == nil {
		t.Fatalf("build should have failed")
	}
}

func TestImageBuilderStartBuildsDelay(t *testing.T) {
	bc1, bc2 := make(chan struct{}), make(chan struct{})
	f := func(ctx context.Context, envName, repo, imagerepo, ref string, ops BuildOptions) error {
		if repo == "foo2/bar2" {
			bc2 <- struct{}{}
			return nil
		}
		bc1 <- struct{}{}
		return nil
	}
	ib := newTestBuilder(f)
	rc := &models.RepoConfig{
		Application: models.RepoConfigAppMetadata{
			Repo:   "foo/bar",
			Ref:    "abcdef",
			Branch: "master",
			Image:  "quay.io/foo/bar",
		},
		Dependencies: models.DependencyDeclaration{
			Direct: []models.RepoConfigDependency{
				models.RepoConfigDependency{
					Name: "bar2",
					Repo: "foo2/bar2",
					AppMetadata: models.RepoConfigAppMetadata{
						Repo:   "foo2/bar2",
						Ref:    "abcdef",
						Branch: "master",
						Image:  "quay.io/foo2/bar2",
					},
				},
			},
		},
	}
	envname := "env-name"
	b, err := ib.StartBuilds(context.Background(), envname, rc)
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	defer b.Stop()
	<-bc1
	time.Sleep(5 * time.Millisecond)
	done, err := b.Completed(envname, models.GetName(rc.Application.Repo))
	if !done {
		t.Fatalf("should be done")
	}
	if err != nil {
		t.Fatalf("build should have succeeded: %v", err)
	}
	done, err = b.Completed(envname, "bar2")
	if done {
		t.Fatalf("should not be done yet")
	}
	if err != nil {
		t.Fatalf("build should not have returned an error: %v", err)
	}
	<-bc2
	time.Sleep(5 * time.Millisecond)
	done, err = b.Completed(envname, "bar2")
	if !done {
		t.Fatalf("should be done")
	}
	if !b.Done() {
		t.Fatalf("all builds should be done")
	}
	if err != nil {
		t.Fatalf("build should have succeeded: %v", err)
	}
}
