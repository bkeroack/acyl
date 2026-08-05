package env

import (
	"context"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/spawner"
)

var _ spawner.EnvironmentSpawner = &Manager{}

// Destroy is the same as Delete and is needed to satisfy the interface
func (m *Manager) Destroy(ctx context.Context, rd models.RepoRevisionData, reason models.QADestroyReason) error {
	return m.Delete(ctx, &rd, reason)
}

// DestroyExplicitly destroys an environment and is triggered by API call
func (m *Manager) DestroyExplicitly(ctx context.Context, qa *models.QAEnvironment, reason models.QADestroyReason) error {
	return m.Delete(ctx, qa.RepoRevisionDataFromQA(), reason)
}

// Success isn't used by Nitro but is needed to satisfy the interface
func (m *Manager) Success(context.Context, string) error {
	return nil
}

// Failure isn't used by Nitro but is needed to satisfy the interface
func (m *Manager) Failure(context.Context, string, string) error {
	return nil
}
