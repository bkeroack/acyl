package persistence

import (
	"context"
	"database/sql"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/pkg/errors"
)

var _ K8sEnvDataLayer = &PGLayer{}

// GetK8sEnv gets a k8s environment by environment name
func (pg *PGLayer) GetK8sEnv(ctx context.Context, name string) (*models.KubernetesEnvironment, error) {
	out := &models.KubernetesEnvironment{}
	if isCancelled(ctx) {
		return nil, errors.Wrap(ctx.Err(), "error getting k8s env")
	}
	q := `SELECT ` + models.KubernetesEnvironment{}.Columns() + ` FROM kubernetes_environments WHERE env_name = $1;`
	if err := pg.db.QueryRowContext(ctx, q, name).Scan(out.ScanValues()...); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrap(err, "error getting k8s environment")
	}
	return out, nil
}

// GetK8sEnvsByNamespace returns any KubernetesEnvironments associated with a specific namespace name
func (pg *PGLayer) GetK8sEnvsByNamespace(ctx context.Context, ns string) ([]models.KubernetesEnvironment, error) {
	if isCancelled(ctx) {
		return nil, errors.Wrap(ctx.Err(), "error getting k8s env by namespace")
	}
	q := `SELECT ` + models.KubernetesEnvironment{}.Columns() + ` FROM kubernetes_environments WHERE namespace = $1;`
	return collectK8sEnvRows(pg.db.QueryContext(ctx, q, ns))
}

// CreateK8sEnv inserts a new k8s environment into the DB
func (pg *PGLayer) CreateK8sEnv(ctx context.Context, env *models.KubernetesEnvironment) error {
	if isCancelled(ctx) {
		return errors.Wrap(ctx.Err(), "error creating k8s env")
	}
	q := `INSERT INTO kubernetes_environments (` + env.InsertColumns() + `) VALUES (` + env.InsertParams() + `);`
	_, err := pg.db.ExecContext(ctx, q, env.InsertValues()...)
	return errors.Wrap(err, "error inserting k8s environment")
}

// DeleteK8sEnv deletes a k8s environment from the DB
func (pg *PGLayer) DeleteK8sEnv(ctx context.Context, name string) error {
	if isCancelled(ctx) {
		return errors.Wrap(ctx.Err(), "error deleting k8s env")
	}
	q := `DELETE FROM kubernetes_environments WHERE env_name = $1;`
	_, err := pg.db.ExecContext(ctx, q, name)
	return errors.Wrap(err, "error deleting k8s environment")
}

// UpdateK8sEnvConfigSignature updates an existing k8s environment config signature
func (pg *PGLayer) UpdateK8sEnvConfigSignature(ctx context.Context, name string, confSig [32]byte) error {
	if isCancelled(ctx) {
		return errors.Wrap(ctx.Err(), "error update k8s env config signature")
	}
	insConfSig := confSig[:]
	q := `UPDATE kubernetes_environments SET config_signature = $1 WHERE env_name = $2;`
	_, err := pg.db.ExecContext(ctx, q, insConfSig, name)
	return errors.Wrap(err, "error updating k8s environment")
}

func collectK8sEnvRows(rows *sql.Rows, err error) ([]models.KubernetesEnvironment, error) {
	var k8senvs []models.KubernetesEnvironment
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "error querying")
	}
	defer rows.Close()
	for rows.Next() {
		ke := models.KubernetesEnvironment{}
		if err := rows.Scan(ke.ScanValues()...); err != nil {
			return nil, errors.Wrap(err, "error scanning row")
		}
		k8senvs = append(k8senvs, ke)
	}
	return k8senvs, nil
}
