package persistence

import (
	"database/sql"
	"fmt"
	"net"
	"time"

	"github.com/pkg/errors"

	"github.com/bkeroack/acyl/pkg/models"
)

// CreateUISession creates a new unauthenticated UI session and returns the ID
func (pg *PGLayer) CreateUISession(targetRoute string, state []byte, clientIP net.IP, userAgent string, expires time.Time) (int, error) {
	if len(targetRoute) == 0 || len(state) == 0 {
		return 0, errors.New("targetRoute and state are required")
	}
	if expires.IsZero() || time.Now().UTC().After(expires) {
		return 0, fmt.Errorf("invalid expires time: %v", expires)
	}
	uis := models.UISession{
		TargetRoute:   targetRoute,
		State:         state,
		ClientIP:      clientIP.String(),
		UserAgent:     userAgent,
		Expires:       expires,
		Authenticated: false,
	}
	q := `INSERT INTO ui_sessions (` + uis.InsertColumns() + `) VALUES (` + uis.InsertParams() + `) RETURNING id;`
	var id int
	if err := pg.db.QueryRow(q, uis.InsertValues()...).Scan(&id); err != nil {
		return 0, errors.Wrap(err, "error inserting ui session")
	}
	return id, nil
}

// UpdateUISession updates the session with ID with githubUser and authenticated flag
func (pg *PGLayer) UpdateUISession(id int, githubUser string, encryptedtoken []byte, authenticated bool) error {
	q := `UPDATE ui_sessions SET github_user = $1, authenticated = $2, encrypted_user_token = $3 WHERE id = $4;`
	_, err := pg.db.Exec(q, githubUser, authenticated, encryptedtoken, id)
	return errors.Wrap(err, "error updating ui session")
}

// DeleteUISession unconditionally deletes the session with ID
func (pg *PGLayer) DeleteUISession(id int) error {
	q := `DELETE FROM ui_sessions WHERE id = $1;`
	_, err := pg.db.Exec(q, id)
	return errors.Wrap(err, "error deleting ui session")
}

// GetUISession returns the session with id or nil if not found
func (pg *PGLayer) GetUISession(id int) (*models.UISession, error) {
	out := &models.UISession{}
	q := `SELECT ` + out.Columns() + ` FROM ui_sessions WHERE id = $1;`
	if err := pg.db.QueryRow(q, id).Scan(out.ScanValues()...); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, errors.Wrap(err, "error getting ui session")
	}
	return out, nil
}

// DeleteExpiredUISessions deletes all expired sessions and returns the number deleted
func (pg *PGLayer) DeleteExpiredUISessions() (uint, error) {
	res, err := pg.db.Exec(`DELETE FROM ui_sessions WHERE expires < now();`)
	if err != nil {
		return 0, errors.Wrap(err, "error deleting expired ui sessions")
	}
	rows, err := res.RowsAffected()
	return uint(rows), errors.Wrap(err, "error getting rows affected")
}
