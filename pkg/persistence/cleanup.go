package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
)

// Cleaner is an object that performs data model clean up operations
type Cleaner struct {
	// DB must be an initialized SQL client
	DB *sqlx.DB
	// Maximum ages for old DB records
	DestroyedEnvRecordsMaxAge, EventLogsMaxAge time.Duration
	// LogFunc is a function that logs a formatted string somewhere
	LogFunc func(string, ...interface{})
}

func (c *Cleaner) log(msg string, args ...interface{}) {
	if c.LogFunc != nil {
		c.LogFunc(msg, args...)
	}
}

// PruneDestroyedEnvRecords deletes all QAEnvironment records with status Destroyed or Failed with a Created timestamp earlier than time.Now() - olderThan.
// olderThan must be > 0
func (c *Cleaner) PruneDestroyedEnvRecords(olderThan time.Duration) (err error) {
	if olderThan == 0 {
		return errors.New("olderThan must be greater than zero")
	}
	if c.DB == nil {
		return errors.New("database client is nil")
	}
	temptable := `CREATE TEMP TABLE old_envs
		ON COMMIT DROP
		AS
		SELECT name 
		FROM qa_environments 
		WHERE (status = $1 OR status = $2)
		AND created < (now() - interval '%v %v');`
	if s := olderThan.Seconds(); s < 1 {
		temptable = fmt.Sprintf(temptable, int64(s*1000), "milliseconds")
	} else {
		temptable = fmt.Sprintf(temptable, int64(s), "seconds")
	}
	txn, err := c.DB.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return errors.Wrap(err, "error beginning txn")
	}
	defer func() {
		if err != nil {
			txn.Rollback()
		}
	}()
	if _, err := txn.Exec(temptable, models.Destroyed, models.Failure); err != nil {
		return errors.Wrap(err, "error creating temp table")
	}
	q := "DELETE FROM kubernetes_environments WHERE env_name IN (SELECT name FROM old_envs);"
	res, err := txn.Exec(q)
	if err != nil {
		return errors.Wrap(err, "error deleting from kubernetes_environments")
	}
	n, _ := res.RowsAffected()
	c.log("pruned %v rows from kubernetes_environment", n)

	q = "DELETE FROM helm_releases WHERE env_name IN (SELECT name FROM old_envs);"
	res, err = txn.Exec(q)
	if err != nil {
		return errors.Wrap(err, "error deleting from helm_releases")
	}
	n, _ = res.RowsAffected()
	c.log("pruned %v rows from helm_releases", n)

	q = "DELETE FROM qa_environments WHERE name IN (SELECT name FROM old_envs);"
	res, err = txn.Exec(q)
	if err != nil {
		return errors.Wrap(err, "error deleting from qa_environments")
	}
	n, _ = res.RowsAffected()
	c.log("pruned %v rows from qa_environments", n)

	return txn.Commit()
}

// PruneEventLogs deletes all EventLog records with a Created timestamp earlier than time.Now() - olderThan.
// olderThan must be > 0
func (c *Cleaner) PruneEventLogs(olderThan time.Duration) error {
	if olderThan == 0 {
		return errors.New("olderThan must be greater than zero")
	}
	if c.DB == nil {
		return errors.New("database client is nil")
	}
	q := `DELETE FROM event_logs WHERE created < (now() - interval '%v %v');`
	if s := olderThan.Seconds(); s < 1 {
		q = fmt.Sprintf(q, int64(s*1000), "milliseconds")
	} else {
		q = fmt.Sprintf(q, int64(s), "seconds")
	}
	res, err := c.DB.Exec(q)
	if err != nil {
		return errors.Wrap(err, "error deleting from event_logs")
	}
	n, _ := res.RowsAffected()
	c.log("pruned %v rows from event_logs", n)
	return nil
}

// Clean runs all data cleanup operations
func (c *Cleaner) Clean() {
	if err := c.PruneDestroyedEnvRecords(c.DestroyedEnvRecordsMaxAge); err != nil {
		c.log("error pruning destroyed env records: %v", err)
	}
	if err := c.PruneEventLogs(c.EventLogsMaxAge); err != nil {
		c.log("error pruning event logs: %v", err)
	}
}
