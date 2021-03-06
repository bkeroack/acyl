// Package persistence provides an implementation of datalayer.go DataLayer interface.
// This particular implementation is for Postgres and uses the models in pkg/models/models.go.
// The migrations are in migrations/[0-9]_create_acyl_models_up.sql.
package persistence

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
)

var _ DataLayer = &PGLayer{}

// PGClient returns a Postgres DB client.
func PGClient(config *config.PGConfig) (*sqlx.DB, error) {
	db, err := sqlx.Open("postgres", config.PostgresURI)
	if err != nil {
		return nil, errors.Wrap(err, "error opening db")
	}
	return db, nil
}

// PGLayer contains the data layer implementation for a Postgres
// database.
type PGLayer struct {
	db     *sqlx.DB
	logger *log.Logger
}

// NewPGLayer instantiates a new PGLayer.
func NewPGLayer(cfg *config.PGConfig, logger *log.Logger) (*PGLayer, error) {
	db, err := PGClient(cfg)
	if err != nil {
		return nil, err
	}
	return &PGLayer{
		db:     db,
		logger: logger,
	}, nil
}

// DB returns the raw sqlx DB client
func (p *PGLayer) DB() *sqlx.DB {
	return p.db
}

// CreateQAEnvironment persists a new QA record.
func (p *PGLayer) CreateQAEnvironment(qae *QAEnvironment) error {
	if qae.Name == "" {
		return fmt.Errorf("QAEnvironment Name is required")
	}

	event := QAEnvironmentEvent{
		Timestamp: time.Now().UTC(),
		Message:   "spawned",
	}
	qae.Created = qae.Created.Truncate(1 * time.Microsecond)
	qae.Status = models.Spawned
	qae.RawStatus = "spawned"
	qae.Events = []QAEnvironmentEvent{event}
	err := qae.SetRaw()
	if err != nil {
		return fmt.Errorf("error setting raw fields: %v", err)
	}

	q := `INSERT into qa_environments (` + qae.Columns() + `) VALUES (` + qae.InsertParams() + `);`
	if _, err := p.db.Exec(q, qae.ScanValues()...); err != nil {
		return errors.Wrapf(err, "error inserting QAEnvironment into database")
	}
	return nil
}

// GetQAEnvironment finds a QAEnvironment by the name field.
func (p *PGLayer) GetQAEnvironment(name string) (*QAEnvironment, error) {
	var qae QAEnvironment
	q := `SELECT ` + qae.Columns() + ` FROM qa_environments WHERE name = $1;`
	if err := p.db.QueryRow(q, name).Scan(qae.ScanValues()...); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "error selecting QAEnvironment by name")
	}
	qae.RawStatus = qae.Status.String()
	if err := qae.ProcessHStores(); err != nil {
		return nil, errors.Wrap(err, "error processing hstores")
	}
	if err := qae.ProcessRaw(); err != nil {
		return nil, errors.Wrap(err, "error processing raw fields")
	}
	return &qae, nil
}

// GetQAEnvironmentConsistently finds a QAEnvironment by the name
// field consistently.
func (p *PGLayer) GetQAEnvironmentConsistently(name string) (*QAEnvironment, error) {
	// All writes are consistent in PG.
	return p.GetQAEnvironment(name)
}

func (p *PGLayer) collectRows(rows *sql.Rows, err error) ([]QAEnvironment, error) {
	var qae []QAEnvironment
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "error querying")
	}
	defer rows.Close()
	for rows.Next() {
		qa := QAEnvironment{}
		if err := rows.Scan(qa.ScanValues()...); err != nil {
			return nil, errors.Wrap(err, "error scanning row")
		}
		qa.RawStatus = qa.Status.String()
		if err := qa.ProcessHStores(); err != nil {
			return nil, errors.Wrap(err, "error processing hstores")
		}
		if err := qa.ProcessRaw(); err != nil {
			return nil, errors.Wrap(err, "error processing raw fields")
		}
		qae = append(qae, qa)
	}
	return qae, nil
}

// GetQAEnvironments returns all QA records
func (p *PGLayer) GetQAEnvironments() ([]QAEnvironment, error) {
	return p.collectRows(p.db.Query(`SELECT ` + models.QAEnvironment{}.Columns() + ` FROM qa_environments;`))
}

// DeleteQAEnvironment deletes a QA environment record. The environment must have status Destroyed.
// Callers must ensure that the underlying k8s environment has been deleted prior to calling this, otherwise potentially orphan k8s resources will be left running.
func (p *PGLayer) DeleteQAEnvironment(name string) (err error) {
	txn, err := p.db.Begin()
	if err != nil {
		return errors.Wrap(err, "error beginning txn")
	}
	defer func() {
		if err != nil {
			txn.Rollback()
		}
	}()
	// this may leave orphan namespaces, but we have to delete these rows due to foreign keys
	q := `DELETE FROM kubernetes_environments WHERE env_name = $1;`
	res, err := txn.Exec(q, name)
	if err != nil {
		return errors.Wrap(err, "error deleting from kubernetes_environments")
	}
	if i, _ := res.RowsAffected(); i != 0 {
		p.logger.Printf("warning: deleted %v rows from kubernetes_environments when deleting environment: %v", i, name)
	}
	q = `DELETE FROM helm_releases WHERE env_name = $1;`
	res, err = txn.Exec(q, name)
	if err != nil {
		return errors.Wrap(err, "error deleting from helm_releases")
	}
	if i, _ := res.RowsAffected(); i != 0 {
		p.logger.Printf("warning: deleted %v rows from helm_releases when deleting environment: %v", i, name)
	}
	q = `DELETE FROM qa_environments WHERE name = $1 AND status = $2;`
	res, err = txn.Exec(q, name, models.Destroyed)
	if err != nil {
		return errors.Wrapf(err, "error deleting item from main table")
	}
	if i, _ := res.RowsAffected(); i == 0 {
		return fmt.Errorf("environment not destroyed (zero rows affected): make sure status is Destroyed prior to deleting environment record: %v", name)
	}
	return txn.Commit()
}

// GetQAEnvironmentsByStatus gets all environmens matching status. TODO(geoffrey): Revisit raw_status with @benjamen
func (p *PGLayer) GetQAEnvironmentsByStatus(status string) ([]QAEnvironment, error) {
	s, err := models.EnvironmentStatusFromString(status)
	if err != nil {
		return nil, errors.Wrap(err, "error in status")
	}
	return p.collectRows(p.db.Query(`SELECT `+models.QAEnvironment{}.Columns()+` from qa_environments WHERE status = $1;`, s))
}

// GetRunningQAEnvironments returns all environments with status "success", "updating" or "spawned".
func (p *PGLayer) GetRunningQAEnvironments() ([]QAEnvironment, error) {
	return p.collectRows(p.db.Query(`SELECT `+models.QAEnvironment{}.Columns()+` from qa_environments WHERE status = $1 OR status = $2 OR status = $3;`, models.Spawned, models.Success, models.Updating))
}

// GetQAEnvironmentsByRepoAndPR teturns all environments which have matching repo AND pull request.
func (p *PGLayer) GetQAEnvironmentsByRepoAndPR(repo string, pr uint) ([]QAEnvironment, error) {
	return p.collectRows(p.db.Query(`SELECT `+models.QAEnvironment{}.Columns()+` from qa_environments WHERE repo = $1 AND pull_request = $2;`, repo, pr))
}

// GetQAEnvironmentsByRepo returns all environments which have matching repo.
func (p *PGLayer) GetQAEnvironmentsByRepo(repo string) ([]QAEnvironment, error) {
	return p.collectRows(p.db.Query(`SELECT `+models.QAEnvironment{}.Columns()+` from qa_environments WHERE repo = $1;`, repo))
}

// GetQAEnvironmentBySourceSHA returns an environment with a matching sourceSHA.
func (p *PGLayer) GetQAEnvironmentBySourceSHA(sourceSHA string) (*QAEnvironment, error) {
	var qae QAEnvironment
	q := `SELECT ` + models.QAEnvironment{}.Columns() + ` FROM qa_environments WHERE source_sha = $1;`
	if err := p.db.QueryRow(q, sourceSHA).Scan(qae.ScanValues()...); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "error selecting QAEnvironment by source SHA")
	}
	if err := qae.ProcessHStores(); err != nil {
		return nil, errors.Wrap(err, "error processing hstores")
	}
	return &qae, nil
}

// GetQAEnvironmentsBySourceBranch returns all environments which have matching sourceBranch.
func (p *PGLayer) GetQAEnvironmentsBySourceBranch(sourceBranch string) ([]QAEnvironment, error) {
	return p.collectRows(p.db.Query(`SELECT `+models.QAEnvironment{}.Columns()+` from qa_environments WHERE source_branch = $1;`, sourceBranch))
}

// GetQAEnvironmentsByUser retrieve all QAEnvironment by user (User is username in the DB see models/models.go).
func (p *PGLayer) GetQAEnvironmentsByUser(user string) ([]QAEnvironment, error) {
	return p.collectRows(p.db.Query(`SELECT `+models.QAEnvironment{}.Columns()+` from qa_environments WHERE username = $1;`, user))
}

// SetQAEnvironmentStatus sets a specific QAEnvironment's status
func (p *PGLayer) SetQAEnvironmentStatus(name string, status EnvironmentStatus) error {
	_, err := p.db.Exec(`UPDATE qa_environments SET status = $1 WHERE name = $2;`, status, name)
	if err != nil {
		return errors.Wrap(err, "error setting status")
	}
	msg := fmt.Sprintf("Marked as %v", status.String())
	if err := p.AddEvent(name, msg); err != nil {
		return errors.Wrapf(err, "error setting event for QAEnvironment: %v ", name)
	}
	return nil
}

// SetQAEnvironmentRepoData sets a specific QAEnvironment's RepoRevisionData.
func (p *PGLayer) SetQAEnvironmentRepoData(name string, repo *RepoRevisionData) error {
	if repo == nil {
		return fmt.Errorf("RepoRevisionData is nil")
	}
	if repo.User == "" ||
		repo.Repo == "" ||
		repo.SourceSHA == "" ||
		repo.BaseSHA == "" ||
		repo.SourceBranch == "" ||
		repo.BaseBranch == "" {
		return fmt.Errorf("all fields of RepoRevisionData must be supplied")
	}

	_, err := p.db.Exec(`UPDATE qa_environments SET repo = $1, source_sha = $2, pull_request = $3, base_sha = $4, source_branch = $5, base_branch = $6, username = $7 WHERE name = $8;`, repo.Repo, repo.SourceSHA, repo.PullRequest, repo.BaseSHA, repo.SourceBranch, repo.BaseBranch, repo.User, name)
	return err
}

// SetQAEnvironmentRefMap sets a specific QAEnvironment's RefMap.
func (p *PGLayer) SetQAEnvironmentRefMap(name string, refMap RefMap) error {
	qae := models.QAEnvironment{RefMap: refMap}
	_, err := p.db.Exec(`UPDATE qa_environments SET ref_map = $1 WHERE name = $2;`, qae.RefMapHStore(), name)
	return err
}

// SetQAEnvironmentCommitSHAMap sets a specific QAEnvironment's commitSHAMap.
func (p *PGLayer) SetQAEnvironmentCommitSHAMap(name string, commitSHAMap RefMap) error {
	qae := models.QAEnvironment{CommitSHAMap: commitSHAMap}
	_, err := p.db.Exec(`UPDATE qa_environments SET commit_sha_map = $1 WHERE name = $2;`, qae.CommitSHAMapHStore(), name)
	return err
}

// SetQAEnvironmentCreated sets a specific QAEnvironment's created time.
func (p *PGLayer) SetQAEnvironmentCreated(name string, created time.Time) error {
	created = created.Truncate(1 * time.Microsecond)
	_, err := p.db.Exec(`UPDATE qa_environments SET created = $1 WHERE name = $2;`, created, name)
	return err
}

// GetExtantQAEnvironments finds any environments for the given repo/PR combination that
// are not status Destroyed
func (p *PGLayer) GetExtantQAEnvironments(repo string, pr uint) ([]QAEnvironment, error) {
	return p.collectRows(p.db.Query(`SELECT `+models.QAEnvironment{}.Columns()+` FROM qa_environments WHERE repo = $1 AND pull_request = $2 AND status != $3 AND status != $4;`, repo, pr, models.Destroyed, models.Failure))
}

// SetAminoEnvironmentID sets the Amino environment ID for an environment.
func (p *PGLayer) SetAminoEnvironmentID(name string, did int) error {
	_, err := p.db.Exec(`UPDATE qa_environments SET amino_environment_id = $1 WHERE name = $2;`, did, name)
	return err
}

// SetAminoServiceToPort sets the Amino service port metadata for an
// environment.
func (p *PGLayer) SetAminoServiceToPort(name string, serviceToPort map[string]int64) error {
	qae := models.QAEnvironment{AminoServiceToPort: serviceToPort}
	_, err := p.db.Exec(`UPDATE qa_environments SET amino_service_to_port = $1 WHERE name = $2;`, qae.AminoServiceToPortHStore(), name)
	return err
}

// SetAminoKubernetesNamespace sets the Kubernetes namespace for an
// environment.
func (p *PGLayer) SetAminoKubernetesNamespace(name string, namespace string) error {
	_, err := p.db.Exec(`UPDATE qa_environments SET amino_kubernetes_namespace = $1 WHERE name = $2;`, namespace, name)
	return err
}

// AddEvent adds an event a particular QAEnvironment.
func (p *PGLayer) AddEvent(name string, msg string) error {
	event := QAEnvironmentEvent{
		Timestamp: time.Now().UTC(),
		Message:   msg,
	}
	out, err := json.Marshal(event)
	if err != nil {
		return errors.Wrap(err, "error marshaling event")
	}
	_, err = p.db.Exec(`UPDATE qa_environments SET raw_events = array_append(raw_events, $1) WHERE name = $2;`, string(out), name)
	return err
}

// Search finds environments that satsify the parameters given.
// Multiple parameters are combined with implicit AND.
func (p *PGLayer) Search(opts models.EnvSearchParameters) ([]QAEnvironment, error) {
	if opts.Pr != 0 && opts.Repo == "" {
		return nil, fmt.Errorf("search by PR requires repo name")
	}
	if opts.TrackingRef != "" && opts.Repo == "" {
		return nil, fmt.Errorf("search by tracking ref requires repo name")
	}
	i := 1
	sopts := []string{}
	sargs := []interface{}{}
	if opts.Pr != 0 {
		sopts = append(sopts, fmt.Sprintf("pull_request = $%v", i))
		sargs = append(sargs, opts.Pr)
		i++
	}
	if opts.Repo != "" {
		sopts = append(sopts, fmt.Sprintf("repo = $%v", i))
		sargs = append(sargs, opts.Repo)
		i++
	}
	if opts.SourceSHA != "" {
		sopts = append(sopts, fmt.Sprintf("source_sha = $%v", i))
		sargs = append(sargs, opts.SourceSHA)
		i++
	}
	if opts.SourceBranch != "" {
		sopts = append(sopts, fmt.Sprintf("source_branch = $%v", i))
		sargs = append(sargs, opts.SourceBranch)
		i++
	}
	if opts.User != "" {
		sopts = append(sopts, fmt.Sprintf("username = $%v", i))
		sargs = append(sargs, opts.User)
		i++
	}
	if opts.Status != models.UnknownStatus {
		sopts = append(sopts, fmt.Sprintf("status = $%v", i))
		sargs = append(sargs, opts.Status)
		i++
	}
	if opts.TrackingRef != "" {
		sopts = append(sopts, fmt.Sprintf("source_ref = $%v", i))
		sargs = append(sargs, opts.TrackingRef)
	}
	q := "SELECT " + models.QAEnvironment{}.Columns() + " FROM qa_environments WHERE " + strings.Join(sopts, " AND ") + ";"
	return p.collectRows(p.db.Query(q, sargs...))
}

// GetMostRecent finds the most recent environments from the last n days.
// Recency is defined by created/updated timestamps.
// The returned slice is in descending order of recency.
func (p *PGLayer) GetMostRecent(n uint) ([]QAEnvironment, error) {
	q := `SELECT ` + models.QAEnvironment{}.Columns() + fmt.Sprintf(` from qa_environments WHERE created >= (current_timestamp - interval '%v days');`, n)
	return p.collectRows(p.db.Query(q))
}

// Close closes the database and any open connections
func (p *PGLayer) Close() error {
	return p.db.Close()
}
