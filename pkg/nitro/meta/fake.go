package meta

import (
	"context"

	"github.com/bkeroack/acyl/pkg/models"
)

type FakeGetter struct {
	GetFunc         func(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error)
	FetchChartsFunc func(ctx context.Context, rc *models.RepoConfig, basePath string) (ChartLocations, error)
	GetAcylYAMLFunc func(ctx context.Context, rc *models.RepoConfig, repo, ref string) (err error)
}

var _ Getter = &FakeGetter{}

func (fg *FakeGetter) Get(ctx context.Context, rd models.RepoRevisionData) (*models.RepoConfig, error) {
	if fg.GetFunc != nil {
		return fg.GetFunc(ctx, rd)
	}
	return &models.RepoConfig{}, nil
}

func (fg *FakeGetter) FetchCharts(ctx context.Context, rc *models.RepoConfig, basePath string) (ChartLocations, error) {
	if fg.FetchChartsFunc != nil {
		return fg.FetchChartsFunc(ctx, rc, basePath)
	}
	return ChartLocations{}, nil
}

func (fg *FakeGetter) GetAcylYAML(ctx context.Context, rc *models.RepoConfig, repo, ref string) (err error) {
	if fg.GetAcylYAMLFunc != nil {
		return fg.GetAcylYAMLFunc(ctx, rc, repo, ref)
	}
	return nil
}
