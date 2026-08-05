package persistence

import (
	"context"
	"database/sql"
	"strings"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/lib/pq"
	"github.com/pkg/errors"
)

var _ HelmDataLayer = &PGLayer{}

func (pg *PGLayer) GetHelmReleasesForEnv(ctx context.Context, name string) ([]models.HelmRelease, error) {
	if isCancelled(ctx) {
		return nil, errors.Wrap(ctx.Err(), "error getting helm releases for env")
	}
	q := `SELECT ` + models.HelmRelease{}.Columns() + ` FROM helm_releases WHERE env_name = $1;`
	return collectHelmRows(pg.db.QueryContext(ctx, q, name))
}

func (pg *PGLayer) UpdateHelmReleaseRevision(ctx context.Context, envname, release, revision string) error {
	if isCancelled(ctx) {
		return errors.Wrap(ctx.Err(), "error updating helm relese revision")
	}
	q := `UPDATE helm_releases SET revision_sha = $1 WHERE env_name = $2 AND release = $3;`
	_, err := pg.db.ExecContext(ctx, q, revision, envname, release)
	return errors.Wrap(err, "error updating helm release")
}

func (pg *PGLayer) CreateHelmReleasesForEnv(ctx context.Context, releases []models.HelmRelease) error {
	if isCancelled(ctx) {
		return errors.Wrap(ctx.Err(), "error creating helm releases for env")
	}
	tx, err := pg.db.Begin()
	if err != nil {
		return errors.Wrap(err, "error opening txn")
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(pq.CopyIn("helm_releases", strings.Split(models.HelmRelease{}.InsertColumns(), ",")...))
	if err != nil {
		return errors.Wrap(err, "error preparing statement")
	}
	for _, r := range releases {
		if _, err := stmt.Exec(r.InsertValues()...); err != nil {
			return errors.Wrap(err, "error executing insert")
		}
	}
	if _, err := stmt.Exec(); err != nil {
		return errors.Wrap(err, "error doing bulk insert exec")
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "error committing txn")
	}
	return nil
}

func (pg *PGLayer) DeleteHelmReleasesForEnv(ctx context.Context, name string) (uint, error) {
	if isCancelled(ctx) {
		return 0, errors.Wrap(ctx.Err(), "error deleting helm releases for env")
	}
	q := `DELETE FROM helm_releases WHERE env_name = $1;`
	res, err := pg.db.ExecContext(ctx, q, name)
	n, _ := res.RowsAffected()
	return uint(n), err
}

func collectHelmRows(rows *sql.Rows, err error) ([]models.HelmRelease, error) {
	var releases []models.HelmRelease
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "error querying")
	}
	defer rows.Close()
	for rows.Next() {
		r := models.HelmRelease{}
		if err := rows.Scan(r.ScanValues()...); err != nil {
			return nil, errors.Wrap(err, "error scanning row")
		}
		releases = append(releases, r)
	}
	return releases, nil
}
