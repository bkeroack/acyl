package persistence

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/dollarshaveclub/metahelm/pkg/metahelm"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/pkg/errors"
)

// GetEventLogByID returns a single EventLog by id, or nil or error
func (pg *PGLayer) GetEventLogByID(id uuid.UUID) (*models.EventLog, error) {
	out := &models.EventLog{}
	q := `SELECT ` + out.Columns() + ` FROM event_logs WHERE id = $1;`
	if err := pg.db.QueryRow(q, id).Scan(out.ScanValues()...); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrap(err, "error getting event log by id")
	}
	return out, nil
}

func (pg *PGLayer) GetEventLogByDeliveryID(deliveryID uuid.UUID) (*models.EventLog, error) {
	out := &models.EventLog{}
	q := `SELECT ` + out.Columns() + ` FROM event_logs WHERE github_delivery_id = $1;`
	if err := pg.db.QueryRow(q, deliveryID).Scan(out.ScanValues()...); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrap(err, "error getting event log by delivery id")
	}
	return out, nil
}

// GetEventLogsByEnvName gets all EventLogs associated with an environment
func (pg *PGLayer) GetEventLogsByEnvName(name string) ([]models.EventLog, error) {
	q := `SELECT qa_environment_event_ids.event_id, ` + models.EventLog{}.ColumnsWithoutID() + ` FROM (
		SELECT unnest(event_ids) AS event_id FROM qa_environments WHERE name = $1
	) AS qa_environment_event_ids
	JOIN event_logs ON qa_environment_event_ids.event_id = event_logs.id`
	return collectEventLogRows(pg.db.Query(q, name))
}

// GetEventLogsByRepoAndPR gets all EventLogs associated with a repo and PR, sorted in reverse created order (newest first)
func (pg *PGLayer) GetEventLogsByRepoAndPR(repo string, pr uint) ([]models.EventLog, error) {
	q := `SELECT ` + models.EventLog{}.Columns() + ` FROM event_logs WHERE repo = $1 AND pull_request = $2 ORDER BY created DESC;`
	return collectEventLogRows(pg.db.Query(q, repo, pr))
}

// CreateEventLog creates a new EventLog. If elog.EnvName is an empty string, null will be persisted in the database for that column.
func (pg *PGLayer) CreateEventLog(elog *models.EventLog) error {
	if elog.LogKey == uuid.Nil {
		lk, err := uuid.NewRandom()
		if err != nil {
			return errors.Wrap(err, "error generating log key")
		}
		elog.LogKey = lk
	}
	q := `INSERT INTO event_logs (` + elog.InsertColumns() + `) VALUES (` + elog.InsertParams() + `);`
	_, err := pg.db.Exec(q, elog.InsertValues()...)
	return errors.Wrap(err, "error inserting event log")
}

// AppendToEventLog appends a new log line to an EventLog
func (pg *PGLayer) AppendToEventLog(id uuid.UUID, msg string) error {
	q := `UPDATE event_logs SET log = array_append(log, $1) WHERE id = $2;`
	_, err := pg.db.Exec(q, msg, id)
	return errors.Wrap(err, "error appending to event log")
}

// SetEventLogEnvName sets the env name for an EventLog
// The name must be valid and exist in qa_environments or the foreign key will cause this method to return an error
func (pg *PGLayer) SetEventLogEnvName(id uuid.UUID, name string) error {
	tx, err := pg.db.Begin()
	if err != nil {
		return errors.Wrap(err, "error beginning transaction")
	}
	defer tx.Rollback()
	q := `UPDATE event_logs SET env_name = $1 WHERE id = $2;`
	if _, err := tx.Exec(q, name, id); err != nil {
		return errors.Wrap(err, "error setting eventlog env name")
	}
	q = `UPDATE qa_environments SET event_ids = event_ids || $1::uuid WHERE name = $2 AND NOT $1::uuid = ANY(event_ids);`
	if _, err := tx.Exec(q, id, name); err != nil {
		return errors.Wrap(err, "error setting environment event IDs")
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "error committing transaction")
	}
	return nil
}

// DeleteEventLog deletes an EventLog
func (pg *PGLayer) DeleteEventLog(id uuid.UUID) error {
	q := `DELETE FROM event_logs WHERE id = $1;`
	_, err := pg.db.Exec(q, id)
	return errors.Wrap(err, "error deleting event log")
}

// DeleteEventLogsByEnvName deletes all EventLogs associated with an environment and returns the number of EventLogs deleted, or error
func (pg *PGLayer) DeleteEventLogsByEnvName(name string) (uint, error) {
	q := `DELETE FROM event_logs WHERE env_name = $1;`
	res, err := pg.db.Exec(q, name)
	n, _ := res.RowsAffected()
	return uint(n), errors.Wrap(err, "error deleting event logs by env name")
}

// DeleteEventLogsByRepoAndPR deletes all EventLogs associated with a repo and PR and returns the number of EventLogs deleted, or error
func (pg *PGLayer) DeleteEventLogsByRepoAndPR(repo string, pr uint) (uint, error) {
	q := `DELETE FROM event_logs WHERE repo = $1 AND pull_request = $2;`
	res, err := pg.db.Exec(q, repo, pr)
	n, _ := res.RowsAffected()
	return uint(n), errors.Wrap(err, "error deleting event logs by repo and pr")
}

func collectEventLogRows(rows *sql.Rows, err error) ([]models.EventLog, error) {
	var logs []models.EventLog
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "error querying")
	}
	defer rows.Close()
	for rows.Next() {
		el := models.EventLog{}
		if err := rows.Scan(el.ScanValues()...); err != nil {
			return nil, errors.Wrap(err, "error scanning row")
		}
		logs = append(logs, el)
	}
	return logs, nil
}

func JSONTime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05Z07:00")
}

func (pg *PGLayer) SetEventStatus(id uuid.UUID, status models.EventStatusSummary) error {
	q := `UPDATE event_logs SET status = $1 WHERE id = $2;`
	_, err := pg.db.Exec(q, status, id)
	return errors.Wrap(err, "error setting event status")
}

func (pg *PGLayer) SetEventStatusConfig(id uuid.UUID, processingTime time.Duration, refmap map[string]string) error {
	if len(refmap) == 0 {
		return errors.New("refmap cannot be empty")
	}
	rj, err := json.Marshal(&refmap)
	if err != nil {
		return errors.Wrap(err, "error marshaling refmap")
	}
	ptj, err := json.Marshal(&models.ConfigProcessingDuration{Duration: processingTime})
	if err != nil {
		return errors.Wrap(err, "error marshaling processingTime")
	}
	q := `UPDATE event_logs SET
			status = jsonb_set(status, '{config}', status->'config' || json_build_object('processing_time', $1::text, 'ref_map', $2::jsonb)::jsonb)
		  WHERE id = $3;`
	_, err = pg.db.Exec(q, string(ptj), string(rj), id)
	return errors.Wrap(err, "error setting event status config")
}

func (pg *PGLayer) SetEventStatusConfigK8sNS(id uuid.UUID, ns string) error {
	q := `UPDATE event_logs SET
			status = jsonb_set(status, '{config}', status->'config' || json_build_object('k8s_ns', $1::text)::jsonb)
		  WHERE id = $2;`
	_, err := pg.db.Exec(q, ns, id)
	return errors.Wrap(err, "error setting event status config k8s namespace")
}

func (pg *PGLayer) SetEventStatusRenderedStatus(id uuid.UUID, rstatus models.RenderedEventStatus) error {
	j, err := json.Marshal(rstatus)
	if err != nil {
		return errors.Wrap(err, "error marshaling rendered event status")
	}
	q := `UPDATE event_logs SET
			status = jsonb_set(status, '{config,rendered_status}', $1::jsonb)
		  WHERE id = $2;`
	_, err = pg.db.Exec(q, string(j), id)
	return errors.Wrap(err, "error setting event status rendered status")
}

func (pg *PGLayer) SetEventStatusTree(id uuid.UUID, tree map[string]models.EventStatusTreeNode) error {
	if len(tree) == 0 {
		return errors.New("tree cannot be empty")
	}
	tj, err := json.Marshal(&tree)
	if err != nil {
		return errors.Wrap(err, "error marshaling tree")
	}
	q := `UPDATE event_logs SET
			status = jsonb_set(status, '{tree}', $1::jsonb)
		  WHERE id = $2;`
	_, err = pg.db.Exec(q, string(tj), id)
	return errors.Wrap(err, "error setting event status tree")
}

func (pg *PGLayer) SetEventStatusCompleted(id uuid.UUID, configStatus models.EventStatus) error {
	q := `UPDATE event_logs SET
			status = jsonb_set(status, '{config}', status->'config' || json_build_object('status', $1::int, 'completed', $2::text)::jsonb)
		  WHERE id = $3;`
	_, err := pg.db.Exec(q, configStatus, JSONTime(time.Now().UTC()), id)
	return errors.Wrap(err, "error setting event status config status to completed")
}

// SetEventStatusFailed will set the event status to indicate that it has failed and will persist the ChartError.
func (pg *PGLayer) SetEventStatusFailed(id uuid.UUID, ce metahelm.ChartError) error {
	encodedChartErr, err := json.Marshal(ce)
	if err != nil {
		return errors.Wrap(err, "error marshaling chart error")
	}
	status := models.FailedStatus
	q := `UPDATE event_logs SET
			status = jsonb_set(status, '{config}', status->'config' || json_build_object('status', $1::int, 'completed', $2::text, 'failed_resources', $4::jsonb)::jsonb)
		  WHERE id = $3;`
	_, err = pg.db.Exec(q, status, JSONTime(time.Now().UTC()), id, encodedChartErr)
	return errors.Wrap(err, "error setting event status config status to failed")
}

func (pg *PGLayer) SetEventStatusImageStarted(id uuid.UUID, name string) error {
	q := `UPDATE event_logs SET
			status = jsonb_set(status, ARRAY['tree',$1,'image'], status->'tree'->$1->'image' || json_build_object('started', $2::text)::jsonb)
		  WHERE id = $3;`
	_, err := pg.db.Exec(q, name, JSONTime(time.Now().UTC()), id)
	return errors.Wrap(err, "error setting event status image to started")
}

func (pg *PGLayer) SetEventStatusImageCompleted(id uuid.UUID, name string, err bool) error {
	q := `UPDATE event_logs SET
			status = jsonb_set(status, ARRAY['tree',$1,'image'], status->'tree'->$1->'image' || json_build_object('completed', $3::text, 'error', $2::boolean)::jsonb)
		  WHERE id = $4;`
	_, err2 := pg.db.Exec(q, name, err, JSONTime(time.Now().UTC()), id)
	return errors.Wrap(err2, "error setting event status image status to completed")
}

func (pg *PGLayer) SetEventStatusChartStarted(id uuid.UUID, name string, status models.NodeChartStatus) error {
	q := `UPDATE event_logs SET
			status = jsonb_set(status, ARRAY['tree',$1,'chart'], status->'tree'->$1->'chart' || json_build_object('status', $2::int, 'started', $3::text)::jsonb)
		  WHERE id = $4;`
	_, err := pg.db.Exec(q, name, status, JSONTime(time.Now().UTC()), id)
	return errors.Wrap(err, "error setting event status chart status to started")
}

func (pg *PGLayer) SetEventStatusChartCompleted(id uuid.UUID, name string, status models.NodeChartStatus) error {
	q := `UPDATE event_logs SET
			status = jsonb_set(status, ARRAY['tree',$1,'chart'], status->'tree'->$1->'chart' || json_build_object('status', $2::int, 'completed', $3::text)::jsonb)
		  WHERE id = $4;`
	_, err := pg.db.Exec(q, name, status, JSONTime(time.Now().UTC()), id)
	if err != nil {
		perr := err.(*pq.Error)
		fmt.Printf("perr: %v\n", perr.Detail)
	}
	return errors.Wrap(err, "error setting event status chart status to completed")
}

func (pg *PGLayer) GetEventStatus(id uuid.UUID) (*models.EventStatusSummary, error) {
	out := &models.EventStatusSummary{}
	q := `SELECT status FROM event_logs WHERE id = $1;`
	if err := pg.db.QueryRow(q, id).Scan(&out); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrap(err, "error getting event status by id")
	}
	return out, nil
}
