package reap

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"

	"github.com/bkeroack/acyl/pkg/ghclient"
	"github.com/bkeroack/acyl/pkg/locker"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/bkeroack/acyl/pkg/spawner"
)

const (
	lockWait                  = 5 * time.Second
	deleteMaxCount            = 100
	destroyedMaxDurationHours = 730 // one month
	failedMaxDurationSecs     = 3600
	spawnedMaxDurationSecs    = 3600
	registeredMaxDurationSecs = 7200
)

type ReaperMetricsCollector interface {
	Pruned(int)
	Reaped(string, string, models.QADestroyReason, error)
	EnvironmentCount(string, models.EnvironmentStatus, uint)
}

// Reaper is an object that does periodic cleanup
type Reaper struct {
	lp          locker.LockProvider
	dl          persistence.DataLayer
	es          spawner.EnvironmentSpawner
	rc          ghclient.RepoClient
	mc          ReaperMetricsCollector
	globalLimit uint
	logger      *log.Logger
	lockKey     int64
}

// NewReaper returns a Reaper object using the supplied dependencies
func NewReaper(lp locker.LockProvider, dl persistence.DataLayer, es spawner.EnvironmentSpawner, rc ghclient.RepoClient, mc ReaperMetricsCollector, globalLimit uint, logger *log.Logger, lockKey int64) *Reaper {
	return &Reaper{
		lp:          lp,
		dl:          dl,
		es:          es,
		rc:          rc,
		mc:          mc,
		globalLimit: globalLimit,
		lockKey:     lockKey,
		logger:      logger,
	}
}

// Reap is called periodically to do various cleanup tasks
func (r *Reaper) Reap() {
	var err error
	reapSpan, ctx := tracer.StartSpanFromContext(context.Background(), "reap")
	defer func() {
		reapSpan.Finish(tracer.WithError(err))
	}()
	lock, err := r.lp.New(ctx, r.lockKey, "reap")
	if err != nil || lock == nil {
		r.logger.Printf("error trying to acquire lock: %v", err)
		return
	}
	_, err = lock.Lock(ctx)
	if err != nil {
		r.logger.Printf("error locking: %v", err)
		return
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), lockWait)
		lock.Unlock(unlockCtx)
		unlockCancel()
	}()

	err = r.pruneDestroyedRecords(ctx)
	if err != nil {
		r.logger.Printf("error pruning destroyed records: %v", err)
	}
	err = r.destroyFailedOrStuckEnvironments(ctx)
	if err != nil {
		r.logger.Printf("error destroying failed/stuck environments: %v", err)
	}
	err = r.destroyClosedPRs(ctx)
	if err != nil {
		r.logger.Printf("error destroying environments associated with closed PRs: %v", err)
	}
	err = r.enforceGlobalLimit(ctx)
	if err != nil {
		r.logger.Printf("error enforcing global limit (%v): %v", r.globalLimit, err)
	}
	if err = r.auditQaEnvs(ctx); err != nil {
		r.logger.Printf("reaper: audit qa envs: %v", err)
	}
}

func (r *Reaper) auditQaEnvs(ctx context.Context) error {
	qas, err := r.dl.GetQAEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("error getting QA environments: %v", err)
	}

	repoMap := make(map[string]map[models.EnvironmentStatus]uint)

	for _, qa := range qas {
		if _, found := repoMap[qa.Repo]; !found {
			repoMap[qa.Repo] = make(map[models.EnvironmentStatus]uint)
		}
		repoMap[qa.Repo][qa.Status]++
	}

	for repoName, repoStatuses := range repoMap {
		for status, num := range repoStatuses {
			r.mc.EnvironmentCount(repoName, status, num)
		}
	}

	return nil
}

func (r *Reaper) destroyClosedPRs(ctx context.Context) error {
	qas, err := r.dl.GetQAEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("error getting QA environments: %v", err)
	}
	var prs string
	for _, qa := range qas {
		if qa.Status != models.Destroyed {
			prs, err = r.rc.GetPRStatus(context.Background(), qa.Repo, qa.PullRequest)
			if err != nil {
				return err
			}
			if prs == "closed" {
				r.logger.Printf("destroying environment because PR is now closed: %v", qa.Name)
				err = r.es.DestroyExplicitly(context.Background(), &qa, models.ReapPrClosed)
				if err != nil {
					r.logger.Printf("error destroying %v: %v", qa.Name, err)
				}
			}
		}
	}
	return nil
}

func (r *Reaper) pruneDestroyedRecords(ctx context.Context) error {
	qas, err := r.dl.GetQAEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("error getting QA environments: %v", err)
	}
	var found bool
	var ts time.Time
	var i int

	defer func() { r.mc.Pruned(i) }()

	for _, qa := range qas {
		if qa.Status == models.Destroyed && i < deleteMaxCount {
			found = false
			for _, e := range qa.Events { // get timestamp of destroyed event
				if strings.Contains(strings.ToLower(e.Message), strings.ToLower("marked as "+models.Destroyed.String())) {
					found = true
					ts = e.Timestamp
				}
			}
			if !found {
				r.logger.Printf("could not find destroyed event for %v", qa.Name)
				err = r.dl.DeleteQAEnvironment(ctx, qa.Name)
				if err != nil {
					r.logger.Printf("error deleting destroyed environment: %v", err)
				}
				continue
			}
			if time.Since(ts) > (destroyedMaxDurationHours * time.Hour) {
				r.logger.Printf("deleting destroyed environment: %v", qa.Name)
				err = r.dl.DeleteQAEnvironment(ctx, qa.Name)
				if err != nil {
					r.logger.Printf("error deleting destroyed environment: %v", err)
				}
				i++
			}
		}
	}
	return nil
}

func (r *Reaper) destroyIfOlderThan(ctx context.Context, qa *models.QAEnvironment, d time.Duration, reason models.QADestroyReason) (err error) {
	if time.Since(qa.Created) > d {
		r.logger.Printf("destroying environment %v: in state %v greater than %v secs", qa.Name, qa.Status.String(), d.Seconds())
		defer func() { r.mc.Reaped(qa.Name, qa.Repo, reason, err) }()
		return r.es.DestroyExplicitly(ctx, qa, reason)
	}
	return nil
}

func (r *Reaper) destroyFailedOrStuckEnvironments(ctx context.Context) error {
	qas, err := r.dl.GetQAEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("error getting QA environments: %v", err)
	}
	for _, qa := range qas {
		switch qa.Status {
		case models.Spawned:
			err = r.destroyIfOlderThan(ctx, &qa, spawnedMaxDurationSecs*time.Second, models.ReapAgeSpawned)
		case models.Failure:
			err = r.destroyIfOlderThan(ctx, &qa, failedMaxDurationSecs*time.Second, models.ReapAgeFailure)
		default:
			continue
		}
		if err != nil {
			r.logger.Printf("error destroying if older than: %v", err)
		}
	}
	return nil
}

func (r *Reaper) enforceGlobalLimit(ctx context.Context) error {
	if r.globalLimit == 0 {
		return nil
	}
	qae, err := r.dl.GetRunningQAEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("error getting running environments: %v", err)
	}
	if len(qae) > int(r.globalLimit) {
		kc := len(qae) - int(r.globalLimit)
		sort.Slice(qae, func(i int, j int) bool { return qae[i].Created.Before(qae[j].Created) })
		kenvs := qae[0:kc]
		r.logger.Printf("reaper: enforcing global limit: extant: %v, limit: %v, destroying: %v", len(qae), r.globalLimit, kc)
		for _, e := range kenvs {
			env := e
			r.logger.Printf("reaper: destroying: %v (created %v)", env.Name, env.Created)
			err := r.es.DestroyExplicitly(context.Background(), &env, models.ReapEnvironmentLimitExceeded)
			if err != nil {
				r.logger.Printf("error destroying environment for exceeding limit: %v", err)
			}
		}
	} else {
		r.logger.Printf("global limit not exceeded: running: %v, limit: %v", len(qae), r.globalLimit)
	}
	return nil
}
