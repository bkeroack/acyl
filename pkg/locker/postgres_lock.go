package locker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bkeroack/acyl/pkg/eventlogger"
	"github.com/google/uuid"
	sqltrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/database/sql"
	sqlxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/jmoiron/sqlx"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/pkg/errors"
)

const (
	defaultKeepAlivePeriod  = 3 * time.Second
	defaultFailureThreshold = 2
)

type postgresSessionController struct {
	// failureThreshold is the number of consecutive failures before marking connection as failed
	failureThreshold uint

	// failreCount is the current count of consecutive failures
	failureCount uint

	// keepAlivePeriod determines how often we will ping Postgres to ensure the connection is still working
	keepAlivePeriod time.Duration

	// sessionErr allows us to propagate session errors to the user of this struct
	sessionErr chan error

	// conn is the underlying sql connection
	conn *sql.Conn
}

func newPostgresSessionController(ctx context.Context, keepAlivePeriod time.Duration, conn *sql.Conn, failureThreshold uint) (*postgresSessionController, error) {
	if keepAlivePeriod == time.Duration(0) {
		keepAlivePeriod = defaultKeepAlivePeriod
	}
	if failureThreshold == 0 {
		failureThreshold = defaultFailureThreshold
	}
	if conn == nil {
		return nil, errors.New("must provide non-nil connection")
	}
	return &postgresSessionController{
		keepAlivePeriod: keepAlivePeriod,
		conn:            conn,
		sessionErr:      make(chan error, 1),
	}, nil
}

// run is a blocking function that periodically checks the health of the connection
func (psc *postgresSessionController) run(ctx context.Context) {
	t := time.NewTicker(psc.keepAlivePeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := psc.sendKeepAlive(ctx)
			if err != nil {
				psc.failureCount++
				if psc.failureCount > psc.failureThreshold {
					psc.sessionErr <- err
				}
				return
			}
			psc.failureCount = 0
		}
	}
}

func (psc *postgresSessionController) sendKeepAlive(ctx context.Context) error {
	return psc.conn.PingContext(ctx)
}

var _ PreemptableLock = &PostgresLock{}

type PostgresLock struct {
	// id is a unique identifier for this lock. This way, we can determine if the notifications we receive are from other locks
	id uuid.UUID

	// psc allows the lock to determine if there are any issues with the underlying postgres connection
	psc *postgresSessionController

	// key used to obtain the lock
	key int64

	// We want to use a single connection as the Advisory Lock we will be using will be a session-level lock
	// This means that the lock is released in 2 conditions:
	// 1. The lock is explicitly released (e.g. via pg_advisory_unlock(key bigint))
	// 2. The session ended (i.e. the tcp connection terminated)
	// Using a single connection will allow us to detect if the session has ended more reliably than using a connection pool
	// conn is the single connection to postgres that will be used for the lock
	conn *sql.Conn

	// postgresURI stores the postgres connection string.
	postgresURI string

	// listener represents a Postgres listener, which watches for Notifications over a defined channel.
	listener *pq.Listener

	// preempted is a channel which contains payloads from the Postgres Listener.
	preempted chan NotificationPayload

	// message represents a descriptive reason to communicate to other lock holders why their operation was preempted, optional
	message string

	conf LockProviderConfig
}

func NewPostgresLock(ctx context.Context, db *sqlx.DB, key int64, connInfo, message string, conf LockProviderConfig) (pl *PostgresLock, err error) {
	id, err := uuid.NewUUID()
	if err != nil {
		return nil, errors.Wrap(err, "unable to create new UUID")
	}
	if db == nil {
		return nil, errors.New("db must not be nil")
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "unable to obtain connection from pool")
	}
	defer func() {
		if err != nil && conn != nil {
			conn.Close()
		}
	}()

	psc, err := newPostgresSessionController(ctx, defaultKeepAlivePeriod, conn, defaultFailureThreshold)
	if err != nil {
		return nil, errors.Wrap(err, "unable to create postgres session controller")
	}
	go psc.run(ctx)
	pl = &PostgresLock{
		id:          id,
		psc:         psc,
		key:         key,
		conn:        conn,
		postgresURI: connInfo,
		preempted:   make(chan NotificationPayload),
		message:     message,
		conf:        conf,
	}
	return pl, nil
}

// handleEvents is a blocking function.
// It checks the multiple different channels to determine how to proceed
func (pl *PostgresLock) handleEvents(ctx context.Context, listener *pq.Listener) {
	ctx, cancel := context.WithTimeout(ctx, pl.conf.maxLockDuration)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			pl.handleNotification(NotificationPayload{
				ID:      pl.id,
				Message: ctx.Err().Error(),
				LockKey: pl.key,
			})
			return
		case err := <-pl.psc.sessionErr:
			if err != nil {
				pl.handleNotification(NotificationPayload{
					ID:      pl.id,
					Message: err.Error(),
					LockKey: pl.key,
				})
			}
			return
		case notification := <-listener.Notify:
			if notification == nil {
				pl.log(ctx, "received nil notification")
				return
			}
			payload := notification.Extra
			np := NotificationPayload{}
			if err := json.Unmarshal([]byte(payload), &np); err != nil {
				pl.log(ctx, "could not unmarshal notification payload: %v", err)
				// In the event that we receive an unknown notification payload, we will give up the lock.
				// This could help for debugging since we can execute a Notify query to force the app to release the lock.
				// It could also help if we accidentally make a breaking change to the Notification payload.
				pl.handleNotification(NotificationPayload{
					ID:      pl.id,
					Message: "received unknown notification payload",
					LockKey: pl.key,
				})
				return
			}

			// If we have received a notification that is not our own, send it over the channel and return
			if np.ID != pl.id {
				pl.handleNotification(np)
				return
			}
		}
	}
}

func (pl *PostgresLock) handleNotification(np NotificationPayload) {
	select {
	case pl.preempted <- np:
		return
	case <-time.After(pl.conf.preemptionTimeout):
		pl.log(context.Background(), "lock (%v): preemption timeout reached (%v), unlocking", pl.key, pl.conf.preemptionTimeout)
		pl.Unlock(context.Background())
	}
}

// Notify lets other processes know that they should release the lock.
// In this case, we use the Postgres NOTIFY command to let the other processes know.
// It is up to the other locks to LISTEN and release the lock accordingly.
func (pl *PostgresLock) Notify(ctx context.Context) error {
	np := NotificationPayload{
		ID:      pl.id,
		Message: pl.message,
		LockKey: pl.key,
	}
	b, err := json.Marshal(np)
	if err != nil {
		return errors.Wrap(err, "unable to encode NotificationPayload")
	}
	q := fmt.Sprintf("NOTIFY  %s, %s", pq.QuoteIdentifier(notificationChannel(pl.key)), pq.QuoteLiteral(string(b)))
	_, err = pl.conn.ExecContext(ctx, q)
	if err != nil {
		return errors.Wrap(err, "unable to execute postgres notify command")
	}
	return nil
}

// Unlock is an idempotent method that unlocks the lock if it is still held.
func (pl *PostgresLock) Unlock(ctx context.Context) error {
	defer func() {
		// Even if we fail to unlock via Postgres properly, destroying the lock should close the underlying sql.Conn.
		// At that point, Postgres should clean up the connection.
		destroyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pl.destroy(destroyCtx)
		cancel()
	}()

	q := `SELECT pg_advisory_unlock($1)`
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pl.conn.ExecContext(releaseCtx, q, pl.key)
	if err != nil {
		return errors.Wrap(err, "unable to unlock advisory lock")
	}
	return nil
}

func (pl *PostgresLock) Lock(ctx context.Context) (<-chan NotificationPayload, error) {
	handleEvents := func(event pq.ListenerEventType, err error) {
		if err != nil {
			pl.log(ctx, "received error when handling postgres listener event: %v", err)
		}
	}

	listener := pq.NewListener(pl.postgresURI, 10*time.Second, time.Minute, handleEvents)
	err := listener.Listen(notificationChannel(pl.key))
	if err != nil {
		return nil, errors.Wrap(err, "unable to establish listener")
	}
	pl.listener = listener
	go func() {
		pl.handleEvents(ctx, pl.listener)
	}()

	query := `SELECT pg_advisory_lock($1)`
	advLockContext, cancel := context.WithTimeout(ctx, pl.conf.lockTimeout)
	defer cancel()
	_, err = pl.conn.ExecContext(advLockContext, query, pl.key)
	if err != nil {
		return nil, errors.Wrap(err, "unable to create pg advisory lock")
	}

	return pl.preempted, nil
}

func (pl *PostgresLock) destroy(ctx context.Context) {
	if pl.listener != nil {
		err := pl.listener.Close()
		if err != nil {
			pl.log(ctx, "unable to close the listener: %v", err)
		}
	}
	if pl.conn != nil {
		err := pl.conn.Close()
		if err != nil {
			pl.log(ctx, "unable to close the connection %v", err)
		}
	}
}

// notificationChannel returns the channel to listen/notify on given a key
func notificationChannel(key int64) string {
	return fmt.Sprintf("%d", key)
}

func (pl *PostgresLock) log(ctx context.Context, msg string, args ...interface{}) {
	eventlogger.GetLogger(ctx).Printf("postgres lock: "+msg, args...)
}

type PostgresLockProvider struct {
	db       *sqlx.DB
	connInfo string
	conf     LockProviderConfig
}

// newPostgresLock returns a PostgresLockProvider, which implements the LockProvider interface
// It utilizes advisory locks and Notify / Listen in order to provide PreemptableLocks
func newPostgresLockProvider(conf LockProviderConfig) (*PostgresLockProvider, error) {
	var db *sqlx.DB
	var err error
	if conf.apmServiceName != "" {
		sqltrace.Register("postgres", &pq.Driver{}, sqltrace.WithServiceName(conf.apmServiceName))
		db, err = sqlxtrace.Open("postgres", conf.postgresURI)
	} else {
		db, err = sqlx.Open("postgres", conf.postgresURI)
	}
	if err != nil {
		return nil, errors.Wrap(err, "error opening db")
	}
	return &PostgresLockProvider{db: db, connInfo: conf.postgresURI, conf: conf}, nil
}

func (plp *PostgresLockProvider) New(ctx context.Context, key int64, event string) (PreemptableLock, error) {
	return NewPostgresLock(ctx, plp.db, key, plp.connInfo, event, plp.conf)
}

// PostgresEnvLock models a distributed lock associated with a unique repo/PR combination
type PostgresEnvLock struct {
	LockKey     int64
	Repo        string
	PullRequest uint
}

func (el PostgresEnvLock) Columns() string {
	return "lock_key, repo, pull_request"
}

func (el PostgresEnvLock) ScanValues() []interface{} {
	return []interface{}{&el.LockKey, &el.Repo, &el.PullRequest}
}

func (plp *PostgresLockProvider) LockKey(ctx context.Context, repo string, pullRequest uint) (int64, error) {
	txn, err := plp.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, errors.Wrap(err, "unable to begin transaction")
	}

	defer func() {
		if err != nil {
			rerr := txn.Rollback()
			if rerr != nil {
				plp.log(ctx, "error rolling back: %v", rerr)
			}
		}
	}()

	el := &PostgresEnvLock{}
	var lockKey int64
	// We do a pseudo update on conflict in order to get a row to be returned from INSERT
	query := `INSERT INTO env_locks(` + el.Columns() + `) VALUES (random_bigint(), $1, $2) ON CONFLICT ON CONSTRAINT env_locks_pkey DO UPDATE set repo = $1 RETURNING (lock_key);`
	err = txn.QueryRowContext(ctx, query, repo, pullRequest).Scan(&lockKey)
	if err != nil {
		return 0, errors.Wrap(err, "unable to get env lock")
	}

	err = txn.Commit()
	if err != nil {
		return 0, errors.Wrap(err, "unable to commit transaction")
	}
	return lockKey, nil
}

func (plp *PostgresLockProvider) log(ctx context.Context, msg string, args ...interface{}) {
	eventlogger.GetLogger(ctx).Printf("postgres lock provider: "+msg, args...)
}
