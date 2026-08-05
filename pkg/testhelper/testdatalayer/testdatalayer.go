package testdatalayer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	stdlibpath "path"
	"strconv"
	"testing"
	"time"

	"github.com/DavidHuie/gomigrate"
	"github.com/bkeroack/acyl/pkg/config"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/lib/pq/hstore"
	"github.com/pkg/errors"
)

type TestDataLayer struct {
	pgdb   *sqlx.DB
	logger *log.Logger
	t      *testing.T
}

var testpostgresURI = "postgres://acyl:acyl@localhost:5432/acyl?sslmode=disable"

func New(logger *log.Logger, t *testing.T) (persistence.DataLayer, *TestDataLayer) {
	return newTestPGDataLayer(logger, t)
}

func newTestPGDataLayer(logger *log.Logger, t *testing.T) (persistence.DataLayer, *TestDataLayer) {
	pcfg := &config.PGConfig{PostgresURI: testpostgresURI}
	dl, err := persistence.NewPGLayer(pcfg, logger)
	if err != nil {
		t.Fatalf("error getting PG datalayer: %v", err)
	}
	return dl, &TestDataLayer{
		logger: logger,
		t:      t,
		pgdb:   dl.DB(),
	}
}

func (tdl *TestDataLayer) createTables() error {
	return tdl.createPGTables()
}

func (tdl *TestDataLayer) createPGTables() error {
	if envURI := os.Getenv("ACYL_POSTGRES_URI"); envURI != "" {
		testpostgresURI = envURI
	}
	db, err := sqlx.Open("postgres", testpostgresURI)
	if err != nil {
		return errors.Wrap(err, "error creating postgres connection")
	}
	defer db.Close()
	logger := log.New(ioutil.Discard, "", log.LstdFlags)
	migrator, err := gomigrate.NewMigratorWithLogger(db.DB, gomigrate.Postgres{}, "./migrations", logger)
	if err != nil {
		return errors.Wrap(err, "error creating migrator")
	}
	if err := migrator.Migrate(); err != nil {
		return errors.Wrap(err, "error applying migration")
	}
	return nil
}

func (tdl *TestDataLayer) setTimestamps(qas []models.QAEnvironment) {
	now := time.Now().UTC()
	settimes := func(qa *models.QAEnvironment, basetime time.Time) {
		qa.Created = basetime
		qa.Events[0].Timestamp = basetime
		qa.Events[1].Timestamp = basetime.Add(1 * time.Minute)
	}
	for i := range qas {
		settimes(&qas[i], now.AddDate(0, 0, -i))
	}
}

func (tdl *TestDataLayer) insertPG(qae *models.QAEnvironment) error {
	q := `INSERT INTO qa_environments
	(name, created, raw_events, hostname, qa_type, username, repo, pull_request, source_sha, base_sha, source_branch, base_branch, source_ref, status, ref_map, commit_sha_map, amino_service_to_port, amino_kubernetes_namespace, amino_environment_id)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19);`

	crm2hstore := func(m models.RefMap) hstore.Hstore {
		out := hstore.Hstore{Map: make(map[string]sql.NullString)}
		for k, v := range m {
			out.Map[k] = sql.NullString{String: v, Valid: true}
		}
		return out
	}

	casp2hstore := func(m map[string]int64) hstore.Hstore {
		out := hstore.Hstore{Map: make(map[string]sql.NullString)}
		for k, v := range m {
			out.Map[k] = sql.NullString{String: strconv.Itoa(int(v)), Valid: true}
		}
		return out
	}

	args := []interface{}{qae.Name, qae.Created, pq.StringArray(qae.RawEvents), qae.Hostname, qae.QAType, qae.User, qae.Repo, qae.PullRequest, qae.SourceSHA, qae.BaseSHA, qae.SourceBranch, qae.BaseBranch, qae.SourceRef, qae.Status, crm2hstore(qae.RefMap), crm2hstore(qae.CommitSHAMap), casp2hstore(qae.AminoServiceToPort), qae.AminoKubernetesNamespace, qae.AminoEnvironmentID}
	if _, err := tdl.pgdb.Exec(q, args...); err != nil {
		return errors.Wrapf(err, "error inserting QAEnvironment into database")
	}
	return nil
}

func (tdl *TestDataLayer) insert(qa *models.QAEnvironment) error {
	return tdl.insertPG(qa)
}

func (tdl *TestDataLayer) insertEventLog(el models.EventLog) error {
	el.Created = time.Now().UTC()
	el.Updated = pq.NullTime{Time: time.Now().UTC(), Valid: true}
	q := `INSERT INTO event_logs (` + el.InsertColumns() + `) VALUES (` + el.InsertParams() + `);`
	if _, err := tdl.pgdb.Exec(q, el.InsertValues()...); err != nil {
		return errors.Wrap(err, "error inserting event log")
	}
	q = `UPDATE qa_environments SET event_ids = event_ids || $1::uuid WHERE name = $2`
	if _, err := tdl.pgdb.Exec(q, el.ID, el.EnvName); err != nil {
		return errors.Wrap(err, "error inserting event log ID into qa_environment")
	}
	return nil
}

func (tdl *TestDataLayer) Setup(path string) error {
	tdl.t.Helper()
	err := tdl.createTables()
	if err != nil {
		return fmt.Errorf("error creating test tables/indices: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("error opening test db file: %v", err)
	}
	defer f.Close()
	data := []models.QAEnvironment{}
	d := json.NewDecoder(f)
	err = d.Decode(&data)
	if err != nil {
		return fmt.Errorf("error unmarshaling test data: %v", err)
	}
	tdl.setTimestamps(data)
	for _, qa := range data {
		qa.CreatedDate = qa.Created.Format("2006-01-02") // YYYY-MM-DD
		if err := qa.SetRaw(); err != nil {
			return fmt.Errorf("error setting raw fields: %v", err)
		}
		if err := tdl.insert(&qa); err != nil {
			return errors.Wrap(err, "error inserting qa")
		}
	}
	elogs, err := readTestEventLogData(stdlibpath.Join(stdlibpath.Dir(path), "event_logs.json"))
	if err != nil {
		return errors.Wrap(err, "error reading event log data")
	}
	for _, el := range elogs {
		if err := tdl.insertEventLog(el); err != nil {
			return errors.Wrap(err, "error inserting event log")
		}
	}
	return nil
}

func readTestEventLogData(path string) ([]models.EventLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("error opening test event log file: %v", err)
	}
	defer f.Close()
	data := []models.EventLog{}
	d := json.NewDecoder(f)
	err = d.Decode(&data)
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling test event log data: %v", err)
	}
	return data, nil
}

func (tdl *TestDataLayer) tearDownPG() error {
	logger := log.New(ioutil.Discard, "", log.LstdFlags)
	migrator, err := gomigrate.NewMigratorWithLogger(tdl.pgdb.DB, gomigrate.Postgres{}, "./migrations", logger)
	if err != nil {
		return errors.Wrap(err, "error creating migrator")
	}
	if err := migrator.RollbackAll(); err != nil {
		return errors.Wrap(err, "error rolling back migrations")
	}
	return tdl.pgdb.Close()
}

func (tdl *TestDataLayer) TearDown() error {
	tdl.t.Helper()
	return tdl.tearDownPG()
}
