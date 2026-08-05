package spawner

import (
	"github.com/bkeroack/acyl/pkg/models"
	"golang.org/x/net/context"
)

// EnvironmentSpawner describes an object capable of managing environments
type EnvironmentSpawner interface {
	Create(context.Context, models.RepoRevisionData) (string, error)
	Update(context.Context, models.RepoRevisionData) (string, error)
	Destroy(context.Context, models.RepoRevisionData, models.QADestroyReason) error
	DestroyExplicitly(context.Context, *models.QAEnvironment, models.QADestroyReason) error
	Success(context.Context, string) error
	Failure(context.Context, string, string) error
}
