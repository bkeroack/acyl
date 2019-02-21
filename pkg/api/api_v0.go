package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	stdlog "log"
	"net/http"
	"strconv"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/config"
	"github.com/dollarshaveclub/acyl/pkg/eventlogger"
	"github.com/dollarshaveclub/acyl/pkg/ghevent"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/dollarshaveclub/acyl/pkg/spawner"
	"github.com/gorilla/mux"
	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

// API output schema
type v0QAEnvironment struct {
	Name                     string                      `json:"name"`
	Created                  time.Time                   `json:"created"`
	RawEvents                []string                    `json:"-"`
	Events                   []models.QAEnvironmentEvent `json:"events"`
	Hostname                 string                      `json:"hostname"`
	QAType                   string                      `json:"qa_type"`
	User                     string                      `json:"user"`
	Repo                     string                      `json:"repo"`
	PullRequest              uint                        `json:"pull_request"`
	SourceSHA                string                      `json:"source_sha"`
	BaseSHA                  string                      `json:"base_sha"`
	SourceBranch             string                      `json:"source_branch"`
	BaseBranch               string                      `json:"base_branch"`
	RawStatus                string                      `json:"status"`
	Status                   models.EnvironmentStatus    `json:"status_int"`
	RefMap                   models.RefMap               `json:"ref_map"`
	AminoServiceToPort       map[string]int64            `json:"amino_service_to_port"`
	AminoKubernetesNamespace string                      `json:"amino_kubernetes_namespace"`
	AminoEnvironmentID       int                         `json:"amino_environment_id"`
}

func v0QAEnvironmentFromQAEnvironment(qae *models.QAEnvironment) *v0QAEnvironment {
	return &v0QAEnvironment{
		Name:                     qae.Name,
		Created:                  qae.Created,
		RawEvents:                qae.RawEvents,
		Events:                   qae.Events,
		Hostname:                 qae.Hostname,
		QAType:                   qae.QAType,
		User:                     qae.User,
		Repo:                     qae.Repo,
		PullRequest:              qae.PullRequest,
		SourceSHA:                qae.SourceSHA,
		BaseSHA:                  qae.BaseSHA,
		SourceBranch:             qae.SourceBranch,
		BaseBranch:               qae.BaseBranch,
		RawStatus:                qae.RawStatus,
		Status:                   qae.Status,
		RefMap:                   qae.RefMap,
		AminoServiceToPort:       qae.AminoServiceToPort,
		AminoKubernetesNamespace: qae.AminoKubernetesNamespace,
		AminoEnvironmentID:       qae.AminoEnvironmentID,
	}
}

type v0api struct {
	apiBase
	dl persistence.DataLayer
	ge *ghevent.GitHubEventWebhook
	es spawner.EnvironmentSpawner
	sc config.ServerConfig
}

func newV0API(dl persistence.DataLayer, ge *ghevent.GitHubEventWebhook, es spawner.EnvironmentSpawner, sc config.ServerConfig, logger *stdlog.Logger) (*v0api, error) {
	return &v0api{
		apiBase: apiBase{
			logger: logger,
		},
		dl: dl,
		ge: ge,
		es: es,
		sc: sc,
	}, nil
}

func (api *v0api) register(r *muxtrace.Router) error {
	if r == nil {
		return fmt.Errorf("router is nil")
	}
	// backwards-compatible routes
	r.HandleFunc("/spawn", middlewareChain(api.githubWebhookHandler, waitMiddleware.waitOnRequest)).Methods("POST")
	r.HandleFunc("/webhook", middlewareChain(api.githubWebhookHandler, waitMiddleware.waitOnRequest)).Methods("POST")
	r.HandleFunc("/envs", middlewareChain(api.envListHandler, authMiddleware.authRequest)).Methods("GET")
	r.HandleFunc("/envs/", middlewareChain(api.envListHandler, authMiddleware.authRequest)).Methods("GET")
	r.HandleFunc("/envs/_search", middlewareChain(api.envSearchHandler, authMiddleware.authRequest)).Methods("GET")
	r.HandleFunc("/envs/{name}", middlewareChain(api.envDetailHandler, authMiddleware.authRequest)).Methods("GET")
	r.HandleFunc("/envs/{name}", middlewareChain(api.envDestroyHandler, authMiddleware.authRequest, waitMiddleware.waitOnRequest)).Methods("DELETE")
	r.HandleFunc("/envs/{name}/success", middlewareChain(api.envSuccessHandler, authMiddleware.authRequest)).Methods("POST")
	r.HandleFunc("/envs/{name}/failure", middlewareChain(api.envFailureHandler, authMiddleware.authRequest)).Methods("POST")
	r.HandleFunc("/envs/{name}/event", middlewareChain(api.envEventHandler, authMiddleware.authRequest)).Methods("POST")

	// v0 routes
	r.HandleFunc("/v0/spawn", middlewareChain(api.githubWebhookHandler, waitMiddleware.waitOnRequest)).Methods("POST")
	r.HandleFunc("/v0/webhook", middlewareChain(api.githubWebhookHandler, waitMiddleware.waitOnRequest)).Methods("POST")
	r.HandleFunc("/v0/envs", middlewareChain(api.envListHandler, authMiddleware.authRequest)).Methods("GET")
	r.HandleFunc("/v0/envs/", middlewareChain(api.envListHandler, authMiddleware.authRequest)).Methods("GET")
	r.HandleFunc("/v0/envs/_search", middlewareChain(api.envSearchHandler, authMiddleware.authRequest)).Methods("GET")
	r.HandleFunc("/v0/envs/{name}", middlewareChain(api.envDetailHandler, authMiddleware.authRequest)).Methods("GET")
	r.HandleFunc("/v0/envs/{name}", middlewareChain(api.envDestroyHandler, authMiddleware.authRequest, waitMiddleware.waitOnRequest)).Methods("DELETE")
	r.HandleFunc("/v0/envs/{name}/success", middlewareChain(api.envSuccessHandler, authMiddleware.authRequest)).Methods("POST")
	r.HandleFunc("/v0/envs/{name}/failure", middlewareChain(api.envFailureHandler, authMiddleware.authRequest)).Methods("POST")
	r.HandleFunc("/v0/envs/{name}/event", middlewareChain(api.envEventHandler, authMiddleware.authRequest)).Methods("POST")
	return nil
}

// MaxAsyncActionTimeout is the maximum amount of time an asynchronous action can take before it's forcibly cancelled
var MaxAsyncActionTimeout = 30 * time.Minute

func (api *v0api) githubWebhookHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var err error
	rootSpan := tracer.StartSpan("github_webhook_handler")
	defer rootSpan.Finish(tracer.WithError(err))
	defer func() {
		if err != nil {
			api.logger.Printf("webhook handler error: %v", err)
		}
	}()

	span := tracer.StartSpan("github_webhook_handler.validation", tracer.ChildOf(rootSpan.Context()))
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.badRequestError(w, err, withSpan(span))
		return
	}
	sig := r.Header.Get("X-Hub-Signature")
	if sig == "" {
		api.forbiddenError(w, "X-Hub-Signature missing", withSpan(span))
		return
	}
	out, err := api.ge.New(b, sig)
	if err != nil {
		_, bsok := err.(ghevent.BadSignature)
		if bsok {
			api.forbiddenError(w, "invalid hub signature", withSpan(span))
		} else {
			api.badRequestError(w, err, withSpan(span))
		}
		return
	}

	log := out.Logger.Printf
	ctx := eventlogger.NewEventLoggerContext(context.Background(), out.Logger)

	if out.Action == ghevent.NotRelevant {
		log("event not relevant: %v", out.Action)
		goto Accepted
	}
	if out.RRD == nil {
		log("repo data is nil")
		api.internalError(w, fmt.Errorf("RepoData is nil (event_log_id: %v)", out.Logger.ID.String()), withSpan(span))
		return
	}
	if out.RRD.SourceBranch == "" && out.Action != ghevent.NotRelevant {
		api.internalError(w, fmt.Errorf("RepoData.SourceBranch has no value (event_log_id: %v)", out.Logger.ID.String()), withSpan(span))
		return
	}

	span.Finish()
	span = tracer.StartSpan("github_webhook_handler.async_action", tracer.ChildOf(rootSpan.Context()))

	log("starting async processing for %v", out.Action)

	switch out.Action {
	case ghevent.CreateNew:
		api.wg.Add(1)
		go func() {
			defer api.wg.Done()
			ctx, cf := context.WithTimeout(ctx, MaxAsyncActionTimeout)
			defer cf() // guarantee that any goroutines created with the ctx are cancelled
			name, err := api.es.Create(ctx, *out.RRD)
			if err != nil {
				log("finished processing create with error: %v", err)
				return
			}
			log("success processing create event (env: %q); done", name)
		}()
	case ghevent.Update:
		api.wg.Add(1)
		go func() {
			var updateError error
			defer api.wg.Done()
			defer span.Finish(tracer.WithError(updateError))
			ctx, cf := context.WithTimeout(ctx, MaxAsyncActionTimeout)
			defer cf() // guarantee that any goroutines created with the ctx are cancelled
			name, updateError := api.es.Update(ctx, *out.RRD)
			if err != nil {
				log("finished processing update with error: %v", err)
				return
			}
			log("success processing update event (env: %q); done", name)
		}()
	case ghevent.Destroy:
		api.wg.Add(1)
		go func() {
			var destroyErr error
			defer api.wg.Done()
			defer span.Finish(tracer.WithError(destroyErr))
			ctx, cf := context.WithTimeout(ctx, MaxAsyncActionTimeout)
			defer cf() // guarantee that any goroutines created with the ctx are cancelled
			destroyErr = api.es.Destroy(ctx, *out.RRD, models.DestroyApiRequest)
			if err != nil {
				log("finished processing destroy with error: %v", err)
				return
			}
			log("success processing destroy event; done")
		}()
	default:
		log("unknown action type: %v", out.Action)
		api.internalError(w, fmt.Errorf("unknown action type: %v (event_log_id: %v)", out.Action, out.Logger.ID.String()), withSpan(span))
		return
	}

Accepted:

	w.WriteHeader(http.StatusAccepted)
	w.Header().Add("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"event_log_id": "%v"}`, out.Logger.ID.String())))
}

func (api *v0api) envListHandler(w http.ResponseWriter, r *http.Request) {
	var fullDetails bool
	envs, err := api.dl.GetQAEnvironments()
	if err != nil {
		api.internalError(w, fmt.Errorf("error getting environments: %v", err))
		return
	}
	if val, ok := r.URL.Query()["full_details"]; ok {
		for _, v := range val {
			if v == "true" {
				fullDetails = true
			}
		}
	}
	var j []byte
	if fullDetails {
		output := []v0QAEnvironment{}
		for _, e := range envs {
			output = append(output, *v0QAEnvironmentFromQAEnvironment(&e))
		}
		j, err = json.Marshal(output)
		if err != nil {
			api.internalError(w, fmt.Errorf("error marshaling environments: %v", err))
			return
		}
	} else {
		nl := []string{}
		for _, e := range envs {
			nl = append(nl, e.Name)
		}
		j, err = json.Marshal(nl)
		if err != nil {
			api.internalError(w, fmt.Errorf("error marshaling environments: %v", err))
			return
		}
	}
	w.Header().Add("Content-Type", "application/json")
	w.Write(j)
}

func (api *v0api) envDetailHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	qa, err := api.dl.GetQAEnvironmentConsistently(name)
	if err != nil {
		api.internalError(w, fmt.Errorf("error getting environment: %v", err))
		return
	}
	if qa == nil {
		api.notfoundError(w)
		return
	}

	output := v0QAEnvironmentFromQAEnvironment(qa)
	j, err := json.Marshal(output)
	if err != nil {
		api.internalError(w, fmt.Errorf("error marshaling environment: %v", err))
		return
	}
	w.Header().Add("Content-Type", "application/json")
	w.Write(j)
}

func (api *v0api) envDestroyHandler(w http.ResponseWriter, r *http.Request) {
	rootSpan := tracer.StartSpan("env_destroy_handler")
	getEnvSpan := tracer.StartSpan("env_destroy_handler.get_env", tracer.ChildOf(rootSpan.Context()))
	name := mux.Vars(r)["name"]
	qa, err := api.dl.GetQAEnvironmentConsistently(name)
	if err != nil {
		api.internalError(w, err, withSpan(getEnvSpan))
		rootSpan.Finish(tracer.WithError(err))
		return
	}
	if qa == nil {
		api.notfoundError(w, withSpan(getEnvSpan))
		rootSpan.Finish(tracer.WithError(fmt.Errorf("not found")))
		return
	}
	getEnvSpan.Finish()
	go func() {
		err := api.es.DestroyExplicitly(context.Background(), qa, models.DestroyApiRequest)
		if err != nil {
			api.logger.Printf("error destroying QA: %v: %v", name, err)
		}
		rootSpan.Finish(tracer.WithError(err))
	}()
	w.WriteHeader(http.StatusNoContent)
}

func (api *v0api) envSuccessHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	err := api.es.Success(context.Background(), name)
	if err != nil {
		api.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (api *v0api) envFailureHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	err := api.es.Failure(context.Background(), name, "")
	if err != nil {
		api.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (api *v0api) envEventHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	event := models.QAEnvironmentEvent{}
	err := d.Decode(&event)
	if err != nil {
		api.badRequestError(w, err)
		return
	}
	err = api.dl.AddEvent(name, event.Message)
	if err != nil {
		api.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (api *v0api) envSearchHandler(w http.ResponseWriter, r *http.Request) {
	qvars := r.URL.Query()
	if _, ok := qvars["pr"]; ok {
		if _, ok := qvars["repo"]; !ok {
			api.badRequestError(w, fmt.Errorf("search by PR requires repo name"))
			return
		}
	}
	if status, ok := qvars["status"]; ok {
		if status[0] == "destroyed" && len(qvars) == 1 {
			api.badRequestError(w, fmt.Errorf("'destroyed' status searches require at least one other search parameter"))
			return
		}
	}
	if len(qvars) == 0 {
		api.badRequestError(w, fmt.Errorf("at least one search parameter is required"))
		return
	}

	ops := models.EnvSearchParameters{}

	for k, vs := range qvars {
		if len(vs) != 1 {
			api.badRequestError(w, fmt.Errorf("unexpected value for %v: %v", k, vs))
			return
		}
		v := vs[0]
		switch k {
		case "repo":
			ops.Repo = v
		case "pr":
			pr, err := strconv.Atoi(v)
			if err != nil || pr < 1 {
				api.badRequestError(w, fmt.Errorf("bad PR number"))
				return
			}
			ops.Pr = uint(pr)
		case "source_sha":
			ops.SourceSHA = v
		case "source_branch":
			ops.SourceBranch = v
		case "user":
			ops.User = v
		case "status":
			s, err := models.EnvironmentStatusFromString(v)
			if err != nil {
				api.badRequestError(w, fmt.Errorf("unknown status"))
				return
			}
			ops.Status = s
		}
	}
	qas, err := api.dl.Search(ops)
	if err != nil {
		api.internalError(w, fmt.Errorf("error searching in DB: %v", err))
	}
	w.Header().Add("Content-Type", "application/json")
	if len(qas) == 0 {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
		return
	}
	output := []v0QAEnvironment{}
	for _, e := range qas {
		output = append(output, *v0QAEnvironmentFromQAEnvironment(&e))
	}
	j, err := json.Marshal(output)
	if err != nil {
		api.internalError(w, fmt.Errorf("error marshaling environments: %v", err))
		return
	}
	w.Write(j)
}
