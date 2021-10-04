package images

import (
	"context"

	"github.com/dollarshaveclub/acyl/pkg/models"
)

// FakeImageBuilder satisfies the Builder interface but does nothing
type FakeImageBuilder struct {
	// CompletedFunc is a function that is called when Completed() is invoked
	BatchCompletedFunc func(envname, title string) (bool, error)
	BatchStartedFunc   func(envname, title string) bool
	BatchDoneFunc      func() bool
	BatchStopFunc      func()
}

var _ Builder = &FakeImageBuilder{}
var _ Batch = &FakeBuildBatch{}

type FakeBuildBatch struct {
	BatchCompletedFunc func(envname, title string) (bool, error)
	BatchStartedFunc   func(envname, title string) bool
	BatchDoneFunc      func() bool
	BatchStopFunc      func()
}

func (fbb *FakeBuildBatch) Completed(envname, title string) (bool, error) {
	if fbb.BatchCompletedFunc != nil {
		return fbb.BatchCompletedFunc(envname, title)
	}
	return true, nil
}
func (fbb *FakeBuildBatch) Started(envname, title string) bool {
	if fbb.BatchStartedFunc != nil {
		return fbb.BatchStartedFunc(envname, title)
	}
	return true
}
func (fbb *FakeBuildBatch) Done() bool {
	if fbb.BatchDoneFunc != nil {
		return fbb.BatchDoneFunc()
	}
	return true
}
func (fbb *FakeBuildBatch) Stop() {
	if fbb.BatchStopFunc != nil {
		fbb.BatchStopFunc()
	}
}

func (fib *FakeImageBuilder) StartBuilds(ctx context.Context, envname string, rc *models.RepoConfig) (Batch, error) {
	return &FakeBuildBatch{fib.BatchCompletedFunc, fib.BatchStartedFunc, fib.BatchDoneFunc, fib.BatchStopFunc}, nil
}

// NoneBackend satisfies BuilderBackend and does nothing
type NoneBackend struct {
}

var _ BuilderBackend = &NoneBackend{}

func (nb *NoneBackend) BuildImage(ctx context.Context, envName, depName, githubRepo, imageRepo, ref string, ops BuildOptions) error {
	return nil
}
