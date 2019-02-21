package api

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/ghevent"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/dollarshaveclub/acyl/pkg/spawner"
	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

type apiBase struct {
	logger *log.Logger
	wg     sync.WaitGroup
}

type ErrorResponseConfig struct {
	Span tracer.Span
	Code int
}

type ErrorResponseOption func(*ErrorResponseConfig)

func withSpan(span tracer.Span) ErrorResponseOption {
	return func(cfg *ErrorResponseConfig) {
		cfg.Span = span
	}
}

func withCode(code int) ErrorResponseOption {
	return func(cfg *ErrorResponseConfig) {
		cfg.Code = code
	}
}

func (api *apiBase) httpError(w http.ResponseWriter, err error, options ...ErrorResponseOption) {
	var config ErrorResponseConfig
	for _, fn := range options {
		fn(&config)
	}

	if config.Code == 0 {
		config.Code = http.StatusInternalServerError
	}
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(config.Code)
	msg := fmt.Sprintf(`{"error_details":"%v"}`, err)
	w.Write([]byte(msg))
	api.logger.Println(msg)
	if config.Span != nil {
		config.Span.Finish(tracer.WithError(err))
	}
}

func (api *apiBase) badRequestError(w http.ResponseWriter, err error, options ...ErrorResponseOption) {
	opts := []ErrorResponseOption{withCode(http.StatusBadRequest)}
	opts = append(opts, options...)
	api.httpError(w, err, opts...)
}

func (api *apiBase) internalError(w http.ResponseWriter, err error, options ...ErrorResponseOption) {
	opts := []ErrorResponseOption{withCode(http.StatusInternalServerError)}
	opts = append(opts, options...)
	api.httpError(w, err, opts...)
}

func (api *apiBase) notfoundError(w http.ResponseWriter, options ...ErrorResponseOption) {
	opts := []ErrorResponseOption{withCode(http.StatusNotFound)}
	opts = append(opts, options...)
	api.httpError(w, fmt.Errorf("not found"), opts...)
}

func (api *apiBase) forbiddenError(w http.ResponseWriter, msg string, options ...ErrorResponseOption) {
	opts := []ErrorResponseOption{withCode(http.StatusForbidden)}
	opts = append(opts, options...)
	api.httpError(w, fmt.Errorf("forbidden: %v", msg), opts...)
}

// Dependencies are the dependencies required for the API
type Dependencies struct {
	DataLayer          persistence.DataLayer
	GitHubEventWebhook *ghevent.GitHubEventWebhook
	EnvironmentSpawner spawner.EnvironmentSpawner
	ServerConfig       config.ServerConfig
	DatadogServiceName string
	Logger             *log.Logger
}

// Manager describes an object capable of registering API versions and waiting on requests
type Manager interface {
	RegisterVersions(deps *Dependencies)
	Wait()
}

type registerOptions struct {
	debugEndpoints bool
	apiKeys        []string
	ipWhitelist    []*net.IPNet
}

// RegisterOption is an option for RegisterVersions()
type RegisterOption func(*registerOptions)

// WithDebugEndpoints causes RegisterVersions to register debug pprof endpoints
func WithDebugEndpoints() RegisterOption {
	return func(ropts *registerOptions) {
		ropts.debugEndpoints = true
	}
}

// WithAPIKeys supplies API keys for protected endpoints
func WithAPIKeys(keys []string) RegisterOption {
	if keys == nil {
		keys = []string{}
	}
	return func(ropts *registerOptions) {
		ropts.apiKeys = keys
	}
}

// WithIPWhitelist supplies IP CIDRs for whitelisted endpoints. Invalid CIDRs are ignored.
func WithIPWhitelist(ips []string) RegisterOption {
	ipwl := []*net.IPNet{}
	for _, cidr := range ips {
		_, cn, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		ipwl = append(ipwl, cn)
	}
	return func(ropts *registerOptions) {
		ropts.ipWhitelist = ipwl
	}
}

// Dispatcher is the concrete implementation of Manager
type Dispatcher struct {
	s          *http.Server
	waitgroups []*sync.WaitGroup
}

// NewDispatcher returns an initialized Dispatcher.
// s should be preconfigured to be able to run ListenAndServeTLS()
func NewDispatcher(s *http.Server) *Dispatcher {
	return &Dispatcher{s: s}
}

// RegisterVersions registers all API versions with the supplied http.Server
func (d *Dispatcher) RegisterVersions(deps *Dependencies, ro ...RegisterOption) error {
	if d.s == nil || deps == nil {
		return fmt.Errorf("one of s (%v) or deps (%v) is nil", d.s, deps)
	}
	ropts := &registerOptions{}
	for _, opt := range ro {
		opt(ropts)
	}

	authMiddleware.apiKeys = ropts.apiKeys
	ipWhitelistMiddleware.ipwl = ropts.ipWhitelist

	r := muxtrace.NewRouter(muxtrace.WithServiceName(deps.DatadogServiceName))
	r.HandleFunc("/health", d.healthHandler).Methods("GET")

	apiv0, err := newV0API(deps.DataLayer, deps.GitHubEventWebhook, deps.EnvironmentSpawner, deps.ServerConfig, deps.Logger)
	if err != nil {
		return fmt.Errorf("error creating api v0: %v", err)
	}
	err = apiv0.register(r)
	if err != nil {
		return fmt.Errorf("error registering api v0: %v", err)
	}
	d.waitgroups = append(d.waitgroups, &apiv0.wg)

	apiv1, err := newV1API(deps.DataLayer, deps.GitHubEventWebhook, deps.EnvironmentSpawner, deps.ServerConfig, deps.Logger)
	if err != nil {
		return fmt.Errorf("error creating api v1: %v", err)
	}
	err = apiv1.register(r)
	if err != nil {
		return fmt.Errorf("error registering api v1: %v", err)
	}
	d.waitgroups = append(d.waitgroups, &apiv1.wg)

	apiv2, err := newV2API(deps.DataLayer, deps.GitHubEventWebhook, deps.EnvironmentSpawner, deps.ServerConfig, deps.Logger)
	if err != nil {
		return fmt.Errorf("error creating api v2: %v", err)
	}
	err = apiv2.register(r)
	if err != nil {
		return fmt.Errorf("error registering api v2: %v", err)
	}
	d.waitgroups = append(d.waitgroups, &apiv2.wg)

	if ropts.debugEndpoints {
		dbg := newDebugEndpoints()
		err = dbg.register(r)
		if err != nil {
			return fmt.Errorf("error registering debug endpoints: %v", err)
		}
	}
	d.s.Handler = r
	return nil
}

// WaitHandlers waits for any handlers that have used waitMiddleware to finish
func (d *Dispatcher) WaitForHandlers() {
	waitMiddleware.wg.Wait()
}

// WaitAsync waits for any async goroutines to finish
func (d *Dispatcher) WaitForAsync() {
	for i := range d.waitgroups {
		d.waitgroups[i].Wait()
	}
}

func (d *Dispatcher) healthHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	w.WriteHeader(http.StatusOK)
}
