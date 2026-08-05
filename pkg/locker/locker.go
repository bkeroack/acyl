package locker

import (
	"context"
	"fmt"
	"time"

	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"

	"github.com/bkeroack/acyl/pkg/eventlogger"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// LockProviderOptions allows clients to configure different aspects for the LockProvider return from NewLockProvider
type LockProviderOption func(*LockProviderConfig)

var (
	defaultPreemptionTimeout = 10 * time.Second
	defaultLockTimeout       = 20 * time.Second
	defaultLockDelay         = 10 * time.Second
	defaultMaxLockDuration   = 30*time.Minute + 30*time.Second // global async timeout is 30 minutes
)

// WithMaxLockDuration sets the duration for which the lock will wait before automatically unlocking the lock.
// This can be used to protect against the holder of the lock getting blocked or not being able to unlock the lock.
func WithMaxLockDuration(duration time.Duration) LockProviderOption {
	return func(config *LockProviderConfig) {
		config.maxLockDuration = duration
	}
}

// WithPreemptionTimeout sets the duration for which the lock will wait for the holder to respect a notification and release the lock.
// If a notification is sent to the lock, the lock will be released after this duration no matter what the holder does.
// This provides a mechanism for releasing the lock quickly in scenarios where the holder of the lock may be blocked indefinitely.
func WithPreemptionTimeout(duration time.Duration) LockProviderOption {
	return func(config *LockProviderConfig) {
		config.preemptionTimeout = duration
	}
}

// WithLockTimeout sets the duration for which the lock will block while attempting to lock, before it gives up and returns an error.
func WithLockTimeout(lockWait time.Duration) LockProviderOption {
	return func(config *LockProviderConfig) {
		config.lockTimeout = lockWait
	}
}

// WithPostgresBackend allows for a distributed lock provider backed by Postgres.
// APM traces can also be enabled with the configured service name.
func WithPostgresBackend(postgresURI string, apmServiceName string) LockProviderOption {
	return func(config *LockProviderConfig) {
		config.postgresURI = postgresURI
		config.apmServiceName = apmServiceName
	}
}

// LockProviderConfig sets configuration values that can be shared between LockProvider implementations.
type LockProviderConfig struct {
	lockTimeout       time.Duration
	maxLockDuration   time.Duration
	preemptionTimeout time.Duration
	postgresURI       string
	apmServiceName    string
}

// LockProvider describes an object capable of creating distributed locks. You may provide your own int64 key or obtain one for a given Repo/PR.
type LockProvider interface {
	New(ctx context.Context, key int64, event string) (PreemptableLock, error)
	LockKey(ctx context.Context, repo string, pr uint) (int64, error)
}

// LockProviderKind enumerates the different LockProvider implementations
type LockProviderKind int

const (
	// UnknownLockProviderKind is the default kind and is unimplmeneted
	UnknownLockProviderKind = iota

	// PostgresLockProviderKind represents an implementation of the LockProvider interface that uses Postgres as the underlying store
	PostgresLockProviderKind

	// FakeLockProviderKind represents an implementation of the LockProvider interfaces that uses an in-memory store, suitable for tests
	FakeLockProviderKind
)

// NewLockProvider returns an implementation of the LockProvider interface.
// It accepts an arbitrary number of LockProviderOptions and sets some opinonated default values if the option is not explicitly set.
func NewLockProvider(kind LockProviderKind, options ...LockProviderOption) (LockProvider, error) {
	config := &LockProviderConfig{}
	for _, opt := range options {
		opt(config)
	}
	if config.lockTimeout == 0 {
		config.lockTimeout = defaultLockTimeout
	}
	if config.maxLockDuration == 0 {
		config.maxLockDuration = defaultMaxLockDuration
	}
	if config.preemptionTimeout == 0 {
		config.preemptionTimeout = defaultPreemptionTimeout
	}
	switch kind {
	case PostgresLockProviderKind:
		if config.postgresURI == "" {
			return nil, errors.New("must provide postgres uri for postgres lock provider")
		}
		return newPostgresLockProvider(*config)
	case FakeLockProviderKind:
		return newFakeLockProvider(*config), nil
	case UnknownLockProviderKind:
		fallthrough
	default:
		return nil, fmt.Errorf("lock provider kind not implemented: %v", kind)
	}
}

// NewFakePreemptiveLockerFactory will create a PreemptiveLockerFactory that makes use of a FakeLockProvider.
// This is suitable for tests, where postgres might not be available or wanted.
func NewFakePreemptiveLockerFactory(providerOpts []LockProviderOption, lockerOpts ...PreemptiveLockerOption) (PreemptiveLockerFactory, error) {
	lp, err := NewLockProvider(FakeLockProviderKind, providerOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "unable to create fake lock provider")
	}
	return NewPreemptiveLockerFactory(lp, lockerOpts...)
}

// NewPostgresPreemptiveLockerFactory will create a PreemptiveLockerFactory that makes use of a PostgresLockProvider.
func NewPostgresPreemptiveLockerFactory(postgresURI, apmServiceName string, providerOpts []LockProviderOption, lockerOpts ...PreemptiveLockerOption) (PreemptiveLockerFactory, error) {
	providerOpts = append([]LockProviderOption{WithPostgresBackend(postgresURI, apmServiceName)}, providerOpts...)
	lp, err := NewLockProvider(PostgresLockProviderKind, providerOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "unable to create postgres lock provider")
	}
	lockerOpts = append([]PreemptiveLockerOption{WithAPMServiceName(apmServiceName)}, lockerOpts...)
	return NewPreemptiveLockerFactory(lp, lockerOpts...)
}

// NotificationPayload represents the content of messages sent to the lock holder.
type NotificationPayload struct {
	// ID is required so that we can ensure the message came from another party.
	ID uuid.UUID `json:"id"`

	// Message provides some context as to why the notification was generated.
	// Useful for logging purposes.
	Message string `json:"event"`

	// The key that this Notification Payload pertains to
	LockKey int64 `json:"lockKey"`
}

// PreemptiveLocker represents a distributed lock where callers can be preempted while waiting for the lock to be released or while holding the lock. High level, the algorithm is as follows:
// - Client A calls Lock() which returns immediately, Client A now has the lock. Client A periodically ensures the session is still alive. If Client A dies, the session expires and the lock is automatically released.
// - Client B calls Lock() which blocks since the lock is held by Client A.
// - Client A receives a value on the channel returned from the Lock() call indicating Client A should release the lock ASAP
// - Client C calls Lock() which blocks
// - Client B's invocation of Lock() returns with an error indicating it was preempted while waiting for the lock to release
// - Client A calls Unlock()
// - Client C's invocation of Lock() returns successfully
//
type PreemptiveLocker struct {
	lp    LockProvider
	lock  PreemptableLock
	conf  PreemptiveLockerConfig
	repo  string
	pr    uint
	key   int64
	event string
}

// PreemptableLock describes an object that acts as a Lock that can signal to peers that they should unlock
// Preemptable locks are single use. Once you unlock the lock, underlying resources will be cleaned up
type PreemptableLock interface {
	// Lock locks the preemptable lock. If it fails to lock, or is preempted before locking, it should return an error
	Lock(ctx context.Context) (<-chan NotificationPayload, error)

	// Unlock unlocks the preemptable lock. It should clean up any underlying resources
	Unlock(ctx context.Context) error

	// Notify informs the current lock holder that they should unlock the lock
	Notify(ctx context.Context) error
}

// PreemptiveLockerConfig contains values for adjusting how the PreemptiveLocker behaves
type PreemptiveLockerConfig struct {

	// lockDelay is how long the locker should wait before returning the lock
	// This is an imperfect, but practical way of ensuring we don't perform operations before other lock holders have realized that their session has ended
	lockDelay time.Duration

	// apmServiceName is the service name used for APM.
	// An empty value disables APM.
	apmServiceName string
}

type PreemptiveLockerOption func(*PreemptiveLockerConfig)

func WithLockDelay(lockDelay time.Duration) PreemptiveLockerOption {
	return func(config *PreemptiveLockerConfig) {
		config.lockDelay = lockDelay
	}
}

func WithAPMServiceName(name string) PreemptiveLockerOption {
	return func(config *PreemptiveLockerConfig) {
		config.apmServiceName = name
	}
}

type PreemptiveLockerFactory func(repo string, pr uint, event string) *PreemptiveLocker

var ErrLockPreempted = errors.New("lock was preemptepd while waiting for the lock")

// NewPreemptiveLockerFactory
func NewPreemptiveLockerFactory(provider LockProvider, opts ...PreemptiveLockerOption) (PreemptiveLockerFactory, error) {
	if provider == nil {
		return nil, errors.New("must provide non-nil LockProvider")
	}
	config := PreemptiveLockerConfig{}
	for _, opt := range opts {
		opt(&config)
	}
	if config.lockDelay == 0 {
		config.lockDelay = defaultLockDelay
	}
	var plf PreemptiveLockerFactory
	plf = func(repo string, pr uint, event string) *PreemptiveLocker {
		return &PreemptiveLocker{
			lp:    provider,
			conf:  config,
			pr:    pr,
			repo:  repo,
			event: event,
		}
	}
	return plf, nil
}

func (p *PreemptiveLocker) log(ctx context.Context, msg string, args ...interface{}) {
	eventlogger.GetLogger(ctx).Printf("preemptive locker: "+msg, args...)
}

func (p *PreemptiveLocker) startSpanFromContext(ctx context.Context, operationName string) (tracer.Span, context.Context) {
	if p.conf.apmServiceName == "" {
		// return no-op span if tracing is disabled
		span, _ := tracer.SpanFromContext(context.Background())
		return span, ctx
	}

	span, ctx := tracer.StartSpanFromContext(ctx, operationName, tracer.ServiceName(p.conf.apmServiceName))
	span.SetTag("event", p.event)
	return span, ctx
}

// Lock locks the lock and returns a channel used to signal if the lock should be released ASAP. If the lock is currently in use, this method will block until the lock is released. If the caller is preempted while waiting for the lock to be released,
// an error is returned.
func (p *PreemptiveLocker) Lock(ctx context.Context) (ch <-chan NotificationPayload, err error) {
	span, ctx := p.startSpanFromContext(ctx, "lock")
	defer func() {
		span.Finish(tracer.WithError(err))
	}()

	// Ensure context hasn't been canceled before beginning an expensive operation
	select {
	case <-ctx.Done():
		return nil, errors.Wrap(ctx.Err(), "context was canceled before acquiring lock")
	default:
	}
	if p.lock != nil {
		return nil, errors.New("single use lock attempted to be reused")
	}

	key, err := p.lp.LockKey(ctx, p.repo, p.pr)
	if err != nil {
		return nil, errors.Wrap(err, "unable to obtain lock key")
	}

	p.key = key
	span.SetTag("lock_key", p.key)
	p.log(ctx, "locking key: %d for repo: %s, pr: %d", p.key, p.repo, p.pr)
	lock, err := p.lp.New(ctx, p.key, p.event)
	if err != nil {
		return nil, errors.Wrap(err, "unable to instantiate a preemptable lock")
	}

	p.lock = lock
	err = p.lock.Notify(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "unable to notify other locks")
	}

	ch, err = lock.Lock(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "unable to lock")
	}

	select {
	case np := <-ch:
		releaseErr := p.Release(context.Background())
		if releaseErr != nil {
			p.log(ctx, "error releasing lock: %v", releaseErr)
		}
		return nil, errors.Wrap(ErrLockPreempted, np.Message)
	// Wait for the specified LockDelay before returning
	case <-time.After(p.conf.lockDelay):
		return ch, nil
	}
}

// Release releases the lock. It is recommended to pass in a different context than the one provided for Lock, since that context could be canceled.
func (p *PreemptiveLocker) Release(ctx context.Context) (err error) {
	span, ctx := p.startSpanFromContext(ctx, "release")
	defer func() {
		span.Finish(tracer.WithError(err))
	}()
	if p.lock == nil {
		return errors.New("attempting to Release before the lock has been locked")
	}
	err = p.lock.Unlock(ctx)
	if err != nil {
		p.log(ctx, "error unlocking lock: %v", err)
	}
	return err
}
