package spawner

import (
	"context"

	"github.com/bkeroack/acyl/pkg/models"
)

var _ EnvironmentSpawner = &FakeEnvironmentSpawner{}

type FakeEnvironmentSpawner struct {
	CreateFunc            func(ctx context.Context, rd models.RepoRevisionData) (string, error)
	UpdateFunc            func(ctx context.Context, rd models.RepoRevisionData) (string, error)
	DestroyFunc           func(ctx context.Context, rd models.RepoRevisionData, reason models.QADestroyReason) error
	DestroyExplicitlyFunc func(ctx context.Context, env *models.QAEnvironment, reason models.QADestroyReason) error
	SuccessFunc           func(ctx context.Context, name string) error
	FailureFunc           func(ctx context.Context, name, msg string) error
}

func (fes *FakeEnvironmentSpawner) Create(ctx context.Context, rd models.RepoRevisionData) (string, error) {
	return fes.CreateFunc(ctx, rd)
}
func (fes *FakeEnvironmentSpawner) Update(ctx context.Context, rd models.RepoRevisionData) (string, error) {
	return fes.UpdateFunc(ctx, rd)
}
func (fes *FakeEnvironmentSpawner) Destroy(ctx context.Context, rd models.RepoRevisionData, reason models.QADestroyReason) error {
	return fes.DestroyFunc(ctx, rd, reason)
}
func (fes *FakeEnvironmentSpawner) DestroyExplicitly(ctx context.Context, env *models.QAEnvironment, reason models.QADestroyReason) error {
	return fes.DestroyExplicitlyFunc(ctx, env, reason)
}
func (fes *FakeEnvironmentSpawner) Success(ctx context.Context, name string) error {
	return fes.SuccessFunc(ctx, name)
}
func (fes *FakeEnvironmentSpawner) Failure(ctx context.Context, name, msg string) error {
	return fes.FailureFunc(ctx, name, msg)
}
