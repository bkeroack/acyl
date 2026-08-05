package env

import (
	"context"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/spawner"
)

// FakeManager stubs out a Manager, fulfilling the spawner.EnvironmentSpawner interface
type FakeManager struct {
}

var _ spawner.EnvironmentSpawner = &FakeManager{}

func (fm *FakeManager) Create(context.Context, models.RepoRevisionData) (string, error) {
	return "", nil
}

func (fm *FakeManager) Update(context.Context, models.RepoRevisionData) (string, error) {
	return "", nil
}

func (fm *FakeManager) Destroy(context.Context, models.RepoRevisionData, models.QADestroyReason) error {
	return nil
}

func (fm *FakeManager) DestroyExplicitly(context.Context, *models.QAEnvironment, models.QADestroyReason) error {
	return nil
}

func (fm *FakeManager) Success(context.Context, string) error {
	return nil
}

func (fm *FakeManager) Failure(context.Context, string, string) error {
	return nil
}
