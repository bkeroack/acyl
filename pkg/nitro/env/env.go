package env

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/eventlogger"
	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/locker"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/namegen"
	nitroerrors "github.com/dollarshaveclub/acyl/pkg/nitro/errors"
	"github.com/dollarshaveclub/acyl/pkg/nitro/meta"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metahelm"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metrics"
	"github.com/dollarshaveclub/acyl/pkg/nitro/notifier"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/dollarshaveclub/acyl/pkg/s3"
	metahelmlib "github.com/dollarshaveclub/metahelm/pkg/metahelm"
	"github.com/google/uuid"
	"github.com/imdario/mergo"
	"github.com/pkg/errors"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	billy "gopkg.in/src-d/go-billy.v4"
	billyutil "gopkg.in/src-d/go-billy.v4/util"
)

// LogFunc is a function that logs a formatted string somewhere
type LogFunc func(string, ...interface{})

type s3Pusher interface {
	Push(contentType string, in io.Reader, opts s3.Options) (string, error)
}

// metrics name prefix
var mpfx = "env."

// NotificationsFactoryFunc is a function that takes a notifications config from the triggering repo, processes it according to any global defaults, and returns a Router suitable to push notifications
type NotificationsFactoryFunc func(lf func(string, ...interface{}), notifications models.Notifications, user string) notifier.Router

func enrichSpan(span tracer.Span, rd *models.RepoRevisionData) {
	span.SetTag("base_branch", rd.BaseBranch)
	span.SetTag("base_sha", rd.BaseSHA)
	span.SetTag("pull_request", rd.PullRequest)
	span.SetTag("repo", rd.Repo)
	span.SetTag("source_branch", rd.SourceBranch)
	span.SetTag("source_ref", rd.SourceRef)
	span.SetTag("source_sha", rd.SourceSHA)
	span.SetTag("user", rd.User)
}

// Manager is an object that creates/updates/deletes environments in k8s
type Manager struct {
	NF                   NotificationsFactoryFunc
	DefaultNotifications models.Notifications
	DL                   persistence.DataLayer
	RC                   ghclient.RepoClient
	MC                   metrics.Collector
	NG                   namegen.NameGenerator
	LP                   locker.PreemptiveLockProvider
	FS                   billy.Filesystem
	MG                   meta.Getter
	CI                   metahelm.Installer
	AWSCreds             config.AWSCreds
	S3Config             config.S3Config
	failureTemplate      *template.Template
	s3p                  s3Pusher
}

func (m *Manager) log(ctx context.Context, msg string, args ...interface{}) {
	eventlogger.GetLogger(ctx).Printf(msg, args...)
}

func (m *Manager) setloggername(ctx context.Context, name string) {
	l := eventlogger.GetLogger(ctx)
	l.SetEnvName(name)
	if l.ID != uuid.UUID([16]byte{}) {
		m.DL.AddEvent(name, "webhook event id: "+l.ID.String())
	}
}

// validContext returns ctx2 if ctx1 is cancelled, or ctx1 otherwise
func validContext(ctx1, ctx2 context.Context) context.Context {
	select {
	case <-ctx1.Done():
		return ctx2
	default:
		return ctx1
	}
}

func (m *Manager) pushNotification(ctx context.Context, env *newEnv, event notifier.NotificationEvent, errmsg string) {
	var err error
	var cmsg string
	if env == nil {
		m.log(ctx, "pushNotification: %v: newenv is nil", event.Key())
		return
	}
	if env.env == nil {
		m.log(ctx, "pushNotification: %v: newenv.env is nil", event.Key())
		return
	}
	span, _ := tracer.SpanFromContext(context.Background())
	parentSpan, ok := getSpanFromContext(ctx)
	if !ok {
		m.log(ctx, "pushNotification: error getting span from context")
	} else {
		span = tracer.StartSpan("process_env_config", tracer.ChildOf(parentSpan.Context()))
	}
	defer span.Finish(tracer.WithError(err))
	// if ctx is cancelled, we don't want to use it to fetch the commit status
	cmsg, err = m.RC.GetCommitMessage(validContext(ctx, context.Background()), env.env.Repo, env.env.SourceSHA)
	if err != nil {
		m.log(ctx, "error getting commit message: %v", err)
		cmsg = "<error getting commit message: " + err.Error() + ">"
	}
	var k8sns string
	k8senv, err := m.DL.GetK8sEnv(env.env.Name)
	switch {
	case err != nil:
		k8sns = fmt.Sprintf("<error getting namespace: %v>", err)
	case k8senv == nil:
		k8sns = "<k8s environment not found>"
	default:
		k8sns = k8senv.Namespace
	}
	if env.rc == nil {
		env.rc = &models.RepoConfig{}
	}
	if err := mergo.Merge(&env.rc.Notifications, m.DefaultNotifications); err != nil {
		msg := "error merging notifications defaults: " + err.Error()
		m.log(ctx, msg)
		m.DL.AddEvent(env.env.Name, msg)
	}
	n := notifier.Notification{
		Data: models.NotificationData{
			EnvName:       env.env.Name,
			Repo:          env.env.Repo,
			SourceBranch:  env.env.SourceBranch,
			SourceSHA:     env.env.SourceSHA,
			BaseBranch:    env.env.BaseBranch,
			BaseSHA:       env.env.BaseSHA,
			User:          env.env.User,
			PullRequest:   env.env.PullRequest,
			K8sNamespace:  k8sns,
			CommitMessage: cmsg,
			ErrorMessage:  errmsg,
		},
		Event:    event,
		Template: env.rc.Notifications.Templates[event.Key()],
	}
	if m.NF == nil {
		m.log(ctx, "notifier factory is uninitialized")
		return
	}
	if err := m.NF(func(msg string, args ...interface{}) { m.log(ctx, msg, args...) }, env.rc.Notifications, env.env.User).FanOut(n); err != nil {
		msg := "error sending " + event.Key() + " notification: " + err.Error()
		m.log(ctx, msg)
		m.DL.AddEvent(env.env.Name, msg)
	}
}

func (m *Manager) createPendingGithubStatus(ctx context.Context, rd *models.RepoRevisionData) (err error) {
	parentSpan, ok := getSpanFromContext(ctx)
	if !ok {
		return errors.New("error generating new env: unable to get span from context")
	}
	span := tracer.StartSpan("create_pending_github_status", tracer.ChildOf(parentSpan.Context()))
	defer span.Finish(tracer.WithError(err))
	cs := &ghclient.CommitStatus{
		Context:     "Acyl",
		Status:      "pending",
		Description: "Environment is being created",
		TargetURL:   "https://media.giphy.com/media/oiymhxu13VYEo/giphy.gif",
	}
	err = m.RC.SetStatus(ctx, rd.Repo, rd.SourceSHA, cs)
	if err != nil {
		m.log(ctx, "error setting pending github commit status: %v", err)
	}
	return err
}

func (m *Manager) createErrorGithubStatus(ctx context.Context, rd *models.RepoRevisionData) error {
	cs := &ghclient.CommitStatus{
		Context:     "Acyl",
		Status:      "failure",
		Description: "Error creating environment",
		TargetURL:   "https://media.giphy.com/media/pyFsc5uv5WPXN9Ocki/giphy.gif",
	}
	err := m.RC.SetStatus(validContext(ctx, context.Background()), rd.Repo, rd.SourceSHA, cs)
	if err != nil {
		m.log(ctx, "error setting failed github commit status: %v", err)
	}
	return err
}

func (m *Manager) createSuccessGithubStatus(ctx context.Context, rd *models.RepoRevisionData) error {
	cs := &ghclient.CommitStatus{
		Context:     "Acyl",
		Status:      "success",
		Description: "Successfully created environment",
		TargetURL:   "https://media.giphy.com/media/SRO0ZwmImic0/giphy.gif",
	}
	err := m.RC.SetStatus(ctx, rd.Repo, rd.SourceSHA, cs)
	if err != nil {
		m.log(ctx, "error setting success github commit status: %v", err)
	}
	return err
}

// lockingOperation sets up the lock and if successful executes f, releasing the lock afterward
func (m *Manager) lockingOperation(ctx context.Context, operationName, repo, pr string, f func(ctx context.Context) error) (err error) {
	rootSpan := tracer.StartSpan(operationName)
	defer rootSpan.Finish(tracer.WithError(err))

	ctx, cf := context.WithCancel(newSpanContext(ctx, rootSpan))
	defer cf()

	end := m.MC.Timing(mpfx+"lock_wait", "triggering_repo:"+repo, "operation_name:"+operationName)
	lock := m.LP.NewPreemptiveLocker(repo, pr, locker.PreemptiveLockerOpts{})
	preempt, err := lock.Lock(ctx)
	if err != nil {
		end("success:false")
		return errors.Wrap(err, "error getting lock")
	}
	end("success:true")
	defer lock.Release()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-preempt: // Lock got preempted, cancel action
			m.MC.Increment(mpfx+"lock_preempt", "triggering_repo:"+repo, "operation_name:"+operationName)
			m.log(ctx, "operation preempted: %v: %v: %v", operationName, repo, pr)
		case <-stop:
		}
		cf()
	}()
	endop := m.MC.Timing(mpfx+"operation", "triggering_repo:"+repo, "operation_name:"+operationName)
	err = f(ctx)
	if err != nil {
		m.log(ctx, "operation error (user: %v, sys: %v): %v: %v: %v: %v", nitroerrors.IsUserError(err), nitroerrors.IsSystemError(err), operationName, repo, pr, err)
	}
	endop(fmt.Sprintf("success:%v", err == nil), fmt.Sprintf("user_error:%v", nitroerrors.IsUserError(err)), fmt.Sprintf("system_error:%v", nitroerrors.IsSystemError(err)))
	return err
}

// Create creates a new k8s environment, persists the information to the DB and returns the environment name or error
func (m *Manager) Create(ctx context.Context, rd models.RepoRevisionData) (string, error) {
	var err error
	var name string
	err = m.lockingOperation(ctx, "nitro_create", rd.Repo, strconv.Itoa(int(rd.PullRequest)), func(ctx context.Context) error {
		// note (mk): is this nonsense?
		rootSpan, ok := getSpanFromContext(ctx)
		if !ok {
			return errors.New("create: unable to get span from context")
		}
		enrichSpan(rootSpan, &rd)
		ctx = newSpanContext(ctx, rootSpan)

		name, err = m.create(ctx, &rd)
		return err
	})
	return name, err
}

// newEnv contains all the information required for construction of a new environment
type newEnv struct {
	env *models.QAEnvironment
	rc  *models.RepoConfig
}

func (m *Manager) getRepoConfig(ctx context.Context, rd *models.RepoRevisionData) (rc *models.RepoConfig, err error) {
	if rd == nil {
		return nil, nitroerrors.SystemError(errors.New("rd is nil"))
	}
	m.log(ctx, "fetching and processing environment config")
	end := m.MC.Timing(mpfx+"process_config", "triggering_repo:"+rd.Repo)
	defer func() {
		end(fmt.Sprintf("success:%v", err == nil))
	}()
	rc, err = m.MG.Get(ctx, *rd)
	if err != nil {
		return nil, errors.Wrap(nitroerrors.UserError(err), "error getting metadata")
	}
	if rc == nil {
		return nil, nitroerrors.SystemError(errors.New("rc is nil"))
	}
	m.MC.Gauge(mpfx+"dependencies", float64(len(rc.Dependencies.All())), "triggering_repo:"+rd.Repo)
	return rc, nil
}

// generateNewEnv calculates the metadata for a new environment and either creates a new environment DB record or modifies an existing one
func (m *Manager) generateNewEnv(ctx context.Context, rd *models.RepoRevisionData) (env *models.QAEnvironment, err error) {
	defer func() {
		if err != nil {
			err = nitroerrors.SystemError(err)
		}
	}()
	parentSpan, ok := getSpanFromContext(ctx)
	if !ok {
		return nil, errors.New("error generating new env: unable to get span from context")
	}
	envSpan := tracer.StartSpan("generate_new_env", tracer.ChildOf(parentSpan.Context()))
	defer envSpan.Finish(tracer.WithError(err))
	envs, err := m.DL.GetQAEnvironmentsByRepoAndPR(rd.Repo, rd.PullRequest)
	if err != nil {
		return nil, errors.Wrap(err, "error checking for existing environment record")
	}
	if len(envs) > 0 {
		// environment record exists, reuse the latest one
		sort.Slice(envs, func(i, j int) bool { return envs[i].Created.Before(envs[j].Created) })
		env = &envs[len(envs)-1]
		m.log(ctx, "reusing environment db record: %v", env.Name)
		// update relevant fields
		if err := m.DL.SetQAEnvironmentStatus(env.Name, models.Spawned); err != nil {
			return nil, errors.Wrap(err, "error setting environment status")
		}
		m.DL.AddEvent(env.Name, fmt.Sprintf("reusing environment record for webhook event %v", eventlogger.GetLogger(ctx).ID.String()))
		if err := m.DL.SetQAEnvironmentRepoData(env.Name, rd); err != nil {
			return nil, errors.Wrap(err, "error setting environment repo data")
		}
		if err := m.DL.SetQAEnvironmentCreated(env.Name, time.Now().UTC()); err != nil {
			return nil, errors.Wrap(err, "error setting environment created timestamp")
		}
		env, err = m.DL.GetQAEnvironment(env.Name)
		if err != nil {
			return nil, errors.Wrap(err, "error getting updated, reused environment record")
		}
	} else {
		// no record exists, create a new one
		name, err := m.NG.New()
		if err != nil {
			return nil, errors.Wrap(err, "error generating name")
		}
		m.log(ctx, "generating new environment record: %v", name)
		env = &models.QAEnvironment{
			Name:         name,
			Created:      time.Now().UTC(),
			Status:       models.Spawned,
			User:         rd.User,
			Repo:         rd.Repo,
			PullRequest:  rd.PullRequest,
			SourceSHA:    rd.SourceSHA,
			BaseSHA:      rd.BaseSHA,
			SourceBranch: rd.SourceBranch,
			BaseBranch:   rd.BaseBranch,
			SourceRef:    rd.SourceRef,
		}
		if err = m.DL.CreateQAEnvironment(env); err != nil {
			return nil, errors.Wrap(err, "error writing environment to db")
		}
	}
	return env, nil
}

// processEnvConfig fetches, parses and validates the top-level acyl.yml and all dependencies, calculates refs and writes them to the env db record. It always returns a valid *newEnv regardless of error.
func (m *Manager) processEnvConfig(ctx context.Context, env *models.QAEnvironment, rd *models.RepoRevisionData) (ne *newEnv, err error) {
	defer func() {
		if err != nil && !nitroerrors.IsUserError(err) {
			err = nitroerrors.SystemError(err)
		}
	}()
	parentSpan, ok := getSpanFromContext(ctx)
	if !ok {
		return nil, errors.New("error generating new env: unable to get span from context")
	}
	span := tracer.StartSpan("process_env_config", tracer.ChildOf(parentSpan.Context()))
	defer span.Finish(tracer.WithError(err))
	ne = &newEnv{env: env}
	rc, err := m.getRepoConfig(ctx, rd)
	if err != nil {
		return ne, errors.Wrap(err, "error validating environment config")
	}
	ne.rc = rc
	rm, err := rc.RefMap()
	if err != nil {
		return ne, errors.Wrap(err, "error generating ref map")
	}
	csm, err := rc.CommitSHAMap()
	if err != nil {
		return ne, errors.Wrap(err, "error generating commit SHA map")
	}
	if err := m.DL.SetQAEnvironmentRefMap(env.Name, rm); err != nil {
		return ne, errors.Wrap(err, "error setting environment ref map")
	}
	if err := m.DL.SetQAEnvironmentCommitSHAMap(env.Name, csm); err != nil {
		return ne, errors.Wrap(err, "error setting environment commit sha map")
	}
	if err := m.DL.SetQAEnvironmentRepoData(env.Name, rd); err != nil {
		return ne, errors.Wrap(err, "error setting environment repo data")
	}
	env, err = m.DL.GetQAEnvironment(env.Name)
	if err != nil {
		return ne, errors.Wrap(err, "error getting updated environment record")
	}
	ne.env = env
	return ne, nil
}

func (m *Manager) fetchCharts(ctx context.Context, name string, rc *models.RepoConfig) (_ string, _ meta.ChartLocations, err error) {
	parentSpan, ok := getSpanFromContext(ctx)
	if !ok {
		return "", nil, errors.New("error generating new env: unable to get span from context")
	}
	span := tracer.StartSpan("fetch_charts", tracer.ChildOf(parentSpan.Context()))
	defer span.Finish(tracer.WithError(err))
	td, err := tempDir(m.FS, "", name)
	if err != nil {
		return "", nil, errors.Wrap(err, "error generating temp dir")
	}
	end := m.MC.Timing(mpfx+"fetch_helm_charts", "triggering_repo:"+rc.Application.Repo)
	cloc, err := m.MG.FetchCharts(ctx, rc, td)
	if err != nil {
		end("success:false")
		if !nitroerrors.IsSystemError(err) {
			err = nitroerrors.UserError(err)
		}
		return "", nil, errors.Wrap(err, "error fetching charts")
	}
	end("success:true")
	return td, cloc, nil
}

// create creates a new environment and returns the environment name, or error
func (m *Manager) create(ctx context.Context, rd *models.RepoRevisionData) (envname string, err error) {
	parentSpan, ok := getSpanFromContext(ctx)
	if !ok {
		return "", errors.New("create: unable to get span from context")
	}
	end := m.MC.Timing(mpfx+"create", "triggering_repo:"+rd.Repo)
	defer func() {
		end(fmt.Sprintf("success:%v", err == nil))
	}()
	env, err := m.generateNewEnv(ctx, rd)
	if err != nil {
		return "", errors.Wrap(err, "error generating environment data")
	}
	m.setloggername(ctx, env.Name)
	newenv := &newEnv{env: env}
	defer func() {
		if err != nil {
			if err := m.DL.SetQAEnvironmentStatus(env.Name, models.Failure); err != nil {
				m.log(ctx, "error setting environment status to failed: %v", err)
			}
			m.pushNotification(ctx, newenv, notifier.Failure, "error creating: "+err.Error())
			m.createErrorGithubStatus(ctx, rd)
			m.MC.Increment(mpfx+"create_errors", "triggering_repo:"+rd.Repo)
			return
		}
		// metahelm.Manager sets the success status on QAEnvironment
		m.pushNotification(ctx, newenv, notifier.Success, "")
		m.createSuccessGithubStatus(ctx, rd)
	}()
	newenv, err = m.processEnvConfig(ctx, env, rd)
	if err != nil {
		return "", errors.Wrap(err, "error processing environment config")
	}
	select {
	case <-ctx.Done():
		return "", nitroerrors.UserError(fmt.Errorf("context was cancelled in create"))
	default:
		break
	}
	m.pushNotification(ctx, newenv, notifier.CreateEnvironment, "")
	m.createPendingGithubStatus(ctx, rd)
	td, cloc, err := m.fetchCharts(ctx, env.Name, newenv.rc)
	if err != nil {
		return "", errors.Wrap(err, "error fetching charts")
	}
	defer billyutil.RemoveAll(m.FS, td)
	mcloc := metahelm.ChartLocations{}
	for k, v := range cloc {
		mcloc[k] = metahelm.ChartLocation{
			ChartPath:   v.ChartPath,
			VarFilePath: v.VarFilePath,
		}
	}
	// Note (mk): Either we manage the build and install span here or we put some duplicate code in
	// build and install charts to avoid cyclic dependency.
	chartSpan := tracer.StartSpan("build_and_install_chart", tracer.ChildOf(parentSpan.Context()))
	defer chartSpan.Finish(tracer.WithError(err))
	if err = m.CI.BuildAndInstallCharts(ctx, &metahelm.EnvInfo{Env: newenv.env, RC: newenv.rc}, mcloc); err != nil {
		return "", m.handleMetahelmError(ctx, newenv, err, "error installing charts")
	}
	return newenv.env.Name, nil
}

// Delete destroys an environment in k8s and marks it as such in the DB
func (m *Manager) Delete(ctx context.Context, rd *models.RepoRevisionData, reason models.QADestroyReason) error {
	var err error
	err = m.lockingOperation(ctx, "nitro_delete", rd.Repo, strconv.Itoa(int(rd.PullRequest)), func(ctx context.Context) error {
		// note (mk): is this nonsense?
		rootSpan, ok := getSpanFromContext(ctx)
		if !ok {
			return errors.New("create: unable to get span from context")
		}
		enrichSpan(rootSpan, rd)
		ctx = newSpanContext(ctx, rootSpan)

		return m.delete(ctx, rd, reason)
	})
	return err
}

var extantEnvsErr = errors.New("did not find exactly one extant environment")

// getenv returns the extant environment for rd or error
func (m *Manager) getenv(ctx context.Context, rd *models.RepoRevisionData) (*models.QAEnvironment, error) {
	envs, err := m.DL.GetExtantQAEnvironments(rd.Repo, rd.PullRequest)
	if err != nil {
		return nil, errors.Wrap(err, "error getting extant environments")
	}
	if len(envs) != 1 {
		m.log(ctx, "expected exactly one extant environment but there are %v", len(envs))
		return nil, extantEnvsErr
	}
	return &envs[0], nil
}

func (m *Manager) delete(ctx context.Context, rd *models.RepoRevisionData, reason models.QADestroyReason) (err error) {
	end := m.MC.Timing(mpfx+"delete", "triggering_repo:"+rd.Repo)
	defer func() {
		end(fmt.Sprintf("success:%v", err == nil))
	}()
	env, err := m.getenv(ctx, rd)
	if err != nil {
		if err == extantEnvsErr {
			// if there's no extant envs, set all associated with the repo & PR to status destroyed
			m.log(ctx, "no extant envs for destroy request")
			envs, err := m.DL.GetQAEnvironmentsByRepoAndPR(rd.Repo, rd.PullRequest)
			if err != nil {
				return errors.Wrapf(nitroerrors.SystemError(err), "error getting environments associated with the repo (%v) and PR (%v)", rd.Repo, rd.PullRequest)
			}
			if len(envs) > 0 {
				for _, e := range envs {
					m.log(ctx, "setting %v to status destroyed", e.Name)
					if err := m.DL.SetQAEnvironmentStatus(e.Name, models.Destroyed); err != nil {
						m.log(ctx, "error setting status to destroyed for environment: %v: %v", e.Name, err)
					}
				}
			}
			return nil
		}
		return errors.Wrap(nitroerrors.SystemError(err), "error getting extant environment")
	}
	m.setloggername(ctx, env.Name)
	ne, err := m.processEnvConfig(ctx, env, rd)
	if err != nil {
		// if there's an error getting or processing the config, continue on with default notifications
		// processEnvConfig() always returns a valid newenv
		m.log(ctx, "error processing environment config: %v", err)
	}
	defer func() {
		if err != nil {
			m.pushNotification(ctx, ne, notifier.Failure, "error destroying: "+err.Error())
		}
	}()
	select {
	case <-ctx.Done():
		return nitroerrors.UserError(fmt.Errorf("context was cancelled in delete"))
	default:
		break
	}
	m.pushNotification(ctx, ne, notifier.DestroyEnvironment, "")
	k8senv, err := m.DL.GetK8sEnv(env.Name)
	if err != nil {
		return errors.Wrap(nitroerrors.SystemError(err), "error getting k8s environment")
	}
	if k8senv == nil {
		return errors.New("missing k8s environment")
	}
	dnend := m.MC.Timing(mpfx+"delete_namespace_duration", "triggering_repo:"+rd.Repo)
	if err = m.CI.DeleteReleases(ctx, k8senv); err != nil {
		return errors.Wrap(err, "error deleting helm releases")
	}
	if err = m.CI.DeleteNamespace(ctx, k8senv); err != nil {
		return errors.Wrap(err, "error deleting namespace")
	}
	dnend()
	err = m.DL.SetQAEnvironmentStatus(env.Name, models.Destroyed)
	return errors.Wrap(nitroerrors.SystemError(err), "error setting environment status")
}

// Update changes an existing environment
func (m *Manager) Update(ctx context.Context, rd models.RepoRevisionData) (string, error) {
	var err error
	var name string
	err = m.lockingOperation(ctx, "nitro_update", rd.Repo, strconv.Itoa(int(rd.PullRequest)), func(ctx context.Context) error {
		// note (mk): is this nonsense?
		rootSpan, ok := getSpanFromContext(ctx)
		if !ok {
			return errors.New("create: unable to get span from context")
		}
		enrichSpan(rootSpan, &rd)
		ctx = newSpanContext(ctx, rootSpan)

		name, err = m.update(ctx, &rd)
		return err
	})
	return name, err
}

func (m *Manager) update(ctx context.Context, rd *models.RepoRevisionData) (envname string, err error) {
	end := m.MC.Timing(mpfx+"update", "triggering_repo:"+rd.Repo)
	defer func() {
		end(fmt.Sprintf("success:%v", err == nil))
	}()
	// check config signatures, if match then we can do chart upgrades
	// if mismatch, then tear down existing env and rebuild from scratch
	env, err := m.getenv(ctx, rd)
	if err != nil {
		if err == extantEnvsErr {
			// if there's no extant envs, go through the create flow (which will reuse the previous name, if a record exists)
			m.log(ctx, "could not find an extant environment so creating new env from scratch")
			m.MC.Increment(mpfx+"update_create", "triggering_repo:"+rd.Repo)
			return m.create(ctx, rd)
		}
		return "", errors.Wrap(nitroerrors.SystemError(err), "error getting extant environment")
	}
	m.setloggername(ctx, env.Name)
	ne := &newEnv{env: env}
	defer func() {
		if err != nil {
			if err := m.DL.SetQAEnvironmentStatus(env.Name, models.Failure); err != nil {
				m.log(ctx, "error setting environment status to failed: %v", err)
			}
			m.pushNotification(ctx, ne, notifier.Failure, err.Error())
			m.createErrorGithubStatus(ctx, rd)
			return
		}
		// metahelm.Manager sets the success status on QAEnvironment
		m.pushNotification(ctx, ne, notifier.Success, "")
		m.createSuccessGithubStatus(ctx, rd)
	}()
	ne, err = m.processEnvConfig(ctx, env, rd)
	if err != nil {
		return "", errors.Wrap(err, "error processing environment config for update")
	}
	k8senv, err := m.DL.GetK8sEnv(env.Name)
	if err != nil {
		return "", errors.Wrap(nitroerrors.SystemError(err), "error getting k8s environment")
	}
	if k8senv == nil {
		return "", nitroerrors.SystemError(errors.New("missing k8s environment"))
	}
	select {
	case <-ctx.Done():
		return "", nitroerrors.UserError(fmt.Errorf("context was cancelled in update"))
	default:
		break
	}
	m.pushNotification(ctx, ne, notifier.UpdateEnvironment, "")
	m.createPendingGithubStatus(ctx, rd)
	td, cloc, err := m.fetchCharts(ctx, env.Name, ne.rc)
	if err != nil {
		return "", errors.Wrap(err, "error fetching charts")
	}
	defer billyutil.RemoveAll(m.FS, td)
	mcloc := metahelm.ChartLocations{}
	for k, v := range cloc {
		mcloc[k] = metahelm.ChartLocation{
			ChartPath:   v.ChartPath,
			VarFilePath: v.VarFilePath,
		}
	}
	envinfo := &metahelm.EnvInfo{Env: env, RC: ne.rc}
	var sig [32]byte
	copy(sig[:], k8senv.ConfigSignature)
	if ne.rc.ConfigSignature() == sig && env.Status == models.Success {
		m.log(ctx, "config signature matches previous successful environment: performing helm release upgrades")
		m.MC.Increment(mpfx+"update_in_place", "triggering_repo:"+rd.Repo)
		releases, err := m.DL.GetHelmReleasesForEnv(env.Name)
		if err != nil {
			return "", errors.Wrap(nitroerrors.SystemError(err), "error getting helm releases for env")
		}
		rsls := map[string]string{}
		for _, r := range releases {
			rsls[r.Name] = r.Release // chart title (dependency name) to release name
		}
		envinfo.Releases = rsls
		if err := m.CI.BuildAndUpgradeCharts(ctx, envinfo, k8senv, mcloc); err != nil {
			return envinfo.Env.Name, m.handleMetahelmError(ctx, ne, err, "error upgrading charts")
		}
		return envinfo.Env.Name, nil
	}
	m.log(ctx, "config signature mismatch or previous environment failed: deleting all helm releases and building environment into existing namespace")
	m.MC.Increment(mpfx+"update_tear_down", "triggering_repo:"+rd.Repo)
	err = errors.Wrap(m.CI.DeleteReleases(ctx, k8senv), "error deleting helm releases for environment")
	if err != nil {
		return "", err
	}
	if err := m.CI.BuildAndInstallChartsIntoExisting(ctx, envinfo, k8senv, mcloc); err != nil {
		return envinfo.Env.Name, m.handleMetahelmError(ctx, ne, err, "error installing charts into existing namespace")
	}
	return envinfo.Env.Name, nil
}

// handleMetahelmError detects if the error returned by metahelm is a ChartError and, if so, generates a failure report and writes it to S3
func (m *Manager) handleMetahelmError(ctx context.Context, env *newEnv, err error, msg string) error {
	ce, ok := err.(metahelmlib.ChartError)
	if !ok {
		return errors.Wrap(nitroerrors.UserError(err), msg)
	}
	if len(ce.FailedDeployments) == 0 && len(ce.FailedJobs) == 0 && len(ce.FailedDaemonSets) == 0 {
		// if there's no failed resources, just return the inner helm error
		return nitroerrors.UserError(ce.HelmError)
	}
	m.MC.Increment(mpfx+"failure_reports", "triggering_repo:"+env.env.Repo)
	// only push to S3 if bucket and region are defined
	if m.S3Config.Bucket != "" && m.S3Config.Region != "" {
		ftd := failureTemplateData{
			EnvName:        env.env.Name,
			PullRequestURL: fmt.Sprintf("https://github.com/%v/pull/%v", env.env.Repo, env.env.PullRequest),
			StartedTime:    env.env.Created,
			FailedTime:     time.Now().UTC(),
			CError:         ce,
		}
		html, err2 := m.chartErrorRenderHTML(ftd)
		if err2 != nil {
			m.log(ctx, "error rendering failure template HTML: %v", err2)
			return errors.Wrap(nitroerrors.SystemError(err), msg)
		}
		sm := &s3.StorageManager{
			LogFunc: eventlogger.GetLogger(ctx).Printf,
		}
		sm.SetCredentials(m.AWSCreds.AccessKeyID, m.AWSCreds.SecretAccessKey)
		m.log(ctx, "pushing environment failure report to S3")
		end := m.MC.Timing(mpfx+"s3_failure_report_push", "triggering_repo:"+env.env.Repo)
		link, err3 := sm.Push("text/html", bytes.NewBuffer(html), s3.Options{
			Region:            m.S3Config.Region,
			Bucket:            m.S3Config.Bucket,
			Key:               m.S3Config.KeyPrefix + "envfailures/" + time.Now().UTC().Round(time.Minute).Format(time.RFC3339) + "/" + env.env.Name + ".html",
			Concurrency:       10,
			MaxRetries:        3,
			PresignTTLMinutes: 60 * 24,
		})
		end()
		if err3 != nil {
			m.log(ctx, "error writing failure HTML to S3: %v", err3)
			return errors.Wrap(nitroerrors.SystemError(err), msg)
		}
		m.pushNotification(context.Background(), env, notifier.Failure, "Environment Failure Log: "+link)
	}
	return errors.Wrap(nitroerrors.UserError(err), msg)
}

// InitFailureTemplate parses the raw temlate data from tmpldata and initializes the S3 client for later use
func (m *Manager) InitFailureTemplate(tmpldata []byte) error {
	if len(tmpldata) == 0 {
		return errors.New("template data is empty or nil")
	}
	t, err := template.New("failure").Parse(string(tmpldata))
	if err != nil {
		return errors.Wrap(err, "error parsing template")
	}
	m.failureTemplate = t
	return nil
}

type failureTemplateData struct {
	EnvName, PullRequestURL string
	StartedTime, FailedTime time.Time
	CError                  metahelmlib.ChartError
}

func (m *Manager) chartErrorRenderHTML(data failureTemplateData) ([]byte, error) {
	if m.failureTemplate == nil {
		return nil, errors.New("failure template is uninitialized")
	}
	b := bytes.NewBuffer(nil)
	if err := m.failureTemplate.Execute(b, data); err != nil {
		return nil, errors.Wrap(err, "error executing template")
	}
	return b.Bytes(), nil
}
