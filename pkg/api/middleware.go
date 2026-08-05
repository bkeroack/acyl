package api

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/pkg/errors"

	"github.com/bkeroack/acyl/pkg/ghclient"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/persistence"
)

type middleware func(http.HandlerFunc) http.HandlerFunc

// middlewareChain is used to chain middlewares on a request handler.
// Usage: router.HandleFunc("/foo", middlewareChain(myhandler, authMiddleware.authRequest, waitMiddleware.waitOnRequest))
func middlewareChain(f http.HandlerFunc, m ...middleware) http.HandlerFunc {
	if len(m) < 1 {
		return f
	}
	return m[0](middlewareChain(f, m[1:cap(m)]...))
}

const (
	apiKeyHeader   = "API-Key"   // string
	apiKeyCtxKey   = "api_key"   // models.APIKey
	qaEnvCtxKey    = "qa_env"    // models.QAEnvironment
	eventLogCtxKey = "event_log" // models.EventLog
)

// authMiddleware checks for correct API key header or aborts with Unauthorized
var authMiddleware = reqAuthorizor{}

type reqAuthorizor struct {
	apiKeys []string
	DL      persistence.DataLayer
}

func (ra reqAuthorizor) tokenAuth(f http.HandlerFunc, minPermission models.PermissionLevel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := func(msg string, args ...interface{}) {
			log.Printf("tokenAuth: "+msg, args...)
		}
		key := r.Header.Get(apiKeyHeader)
		if key != "" {
			for _, k := range ra.apiKeys {
				if key == k {
					f(w, r.Clone(context.WithValue(r.Context(), apiKeyCtxKey, models.APIKey{PermissionLevel: models.AdminPermission})))
					return
				}
			}
			keyID, err := uuid.Parse(key)
			if err != nil {
				log("error parsing api key id: %v", err)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			apikey, err := authMiddleware.DL.GetAPIKeyByToken(r.Context(), keyID)
			if err != nil {
				log("error getting api key: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if apikey != nil {
				if apikey.PermissionLevel >= minPermission {
					err = authMiddleware.DL.UpdateAPIKeyLastUsed(r.Context(), keyID)
					if err != nil {
						log("error updating api key: %v", err)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					f(w, r.Clone(context.WithValue(r.Context(), apiKeyCtxKey, *apikey)))
					return
				}
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
	}
}

// authorizeEnv() requires tokenAuth to be called first or will fail
func (ra reqAuthorizor) authorizeEnv(f http.HandlerFunc) http.HandlerFunc {
	log := func(msg string, args ...interface{}) {
		log.Printf("authorizeEnv: "+msg, args...)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// abort if the API Key not found in context
		apikey, ok := r.Context().Value(apiKeyCtxKey).(models.APIKey)
		if !ok {
			log("request not authorized")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		name := mux.Vars(r)["name"]
		qa, err := ra.DL.GetQAEnvironment(r.Context(), name)
		if err != nil {
			log("error getting environment: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if qa == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if apikey.PermissionLevel != models.AdminPermission {
			if apikey.GitHubUser != qa.User {
				log("request not authorized")
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		f(w, r.Clone(context.WithValue(r.Context(), qaEnvCtxKey, *qa)))
	}
}

// authorizeEventLog() requires tokenAuth to be called first or will fail
func (ra reqAuthorizor) authorizeEventLog(f http.HandlerFunc) http.HandlerFunc {
	log := func(msg string, args ...interface{}) {
		log.Printf("authorizeEventLog: "+msg, args...)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// abort if the API Key not found in context
		apikey, ok := r.Context().Value(apiKeyCtxKey).(models.APIKey)
		if !ok {
			log("request not authorized")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		idstr := mux.Vars(r)["id"]
		id, err := uuid.Parse(idstr)
		if err != nil {
			log(errors.Wrap(err, "error parsing id").Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		el, err := ra.DL.GetEventLogByID(id)
		if err != nil {
			log(errors.Wrap(err, "error fetching event logs").Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if el == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		qa, err := ra.DL.GetQAEnvironment(r.Context(), el.EnvName)
		if err != nil {
			log("error getting environment: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if qa == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if apikey.PermissionLevel != models.AdminPermission {
			if apikey.GitHubUser != qa.User {
				log("request not authorized")
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		f(w, r.Clone(context.WithValue(r.Context(), eventLogCtxKey, *el)))
	}
}

// authorize() requires tokenAuth to be called first or will fail
// does not validate the user name, just that its present or admin token
func (ra reqAuthorizor) authorize(f http.HandlerFunc) http.HandlerFunc {
	log := func(msg string, args ...interface{}) {
		log.Printf("authorize: "+msg, args...)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// abort if the API Key not found in context
		apikey, ok := r.Context().Value(apiKeyCtxKey).(models.APIKey)
		if !ok {
			log("request not authorized")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if apikey.PermissionLevel != models.AdminPermission {
			if apikey.GitHubUser == "" {
				log("request not authorized")
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		f(w, r)
		return
	}
}

// waitMiddleware increments a waitgroup for the duration of the request
var waitMiddleware = waitOnRequests{}

type waitOnRequests struct {
	wg sync.WaitGroup
}

func (wr *waitOnRequests) waitOnRequest(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wr.wg.Add(1)
		f(w, r)
		wr.wg.Done()
	}
}

var ipWhitelistMiddleware = ipWhitelistChecker{}

type ipWhitelistChecker struct {
	ipwl []*net.IPNet
}

func (iwc *ipWhitelistChecker) checkIPWhitelist(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		addr := net.ParseIP(host)
		for _, cidr := range iwc.ipwl {
			if cidr.Contains(addr) {
				f(w, r)
				return
			}
		}
		w.WriteHeader(http.StatusForbidden)
	}
}

// globals are a bad idea but this is how all the other middlewares work, so...
var sessionAuthMiddleware = &sessionAuthenticator{}

// these are used to store the UI session in the request context
const sessionContextKeyVal = "ctx_acyl_session"

type sessionContextKey string

func withSession(ctx context.Context, uis models.UISession) context.Context {
	return context.WithValue(ctx, sessionContextKey(sessionContextKeyVal), uis)
}

func getSessionFromContext(ctx context.Context) (models.UISession, error) {
	uis, ok := ctx.Value(sessionContextKey(sessionContextKeyVal)).(models.UISession)
	if !ok {
		return models.UISession{}, fmt.Errorf("session missing from context")
	}
	if !uis.Authenticated || uis.GitHubUser == "" {
		return models.UISession{}, fmt.Errorf("unauthenticated session or empty GitHub user")
	}
	return uis, nil
}

// sessionAuthenticator is a middleware that authenticates UI API calls with session cookies
type sessionAuthenticator struct {
	OAuth       OAuthConfig
	CookieStore sessions.Store
	DL          persistence.UISessionsDataLayer
}

func (sa *sessionAuthenticator) sessionAuth(f http.HandlerFunc) http.HandlerFunc {
	if sa.CookieStore == nil || sa.DL == nil {
		return f
	}
	if !sa.OAuth.Enforce {
		return func(w http.ResponseWriter, r *http.Request) {
			// create a dummy session for this request
			uis := models.UISession{}
			uis.EncryptandSetUserToken([]byte(`dummy token`), sa.OAuth.UserTokenEncKey)
			uis.Authenticated = true
			uis.GitHubUser = sa.OAuth.DummySessionUser
			f(w, r.Clone(withSession(r.Context(), uis)))
			return
		}
	}
	log := func(msg string, args ...interface{}) {
		log.Printf("sessionAuth: "+msg, args...)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		accessDenied := func() {
			w.WriteHeader(http.StatusForbidden)
		}
		// Get returns the session from the cookie or creates a new session if missing
		sess, err := sa.CookieStore.Get(r, uiSessionName)
		if err != nil {
			// invalid cookie or failure to authenticate/decrypt
			log("error getting session, access denied: %v", err)
			accessDenied()
			return
		}
		if sess.IsNew {
			log("session missing from request, access denied")
			accessDenied()
			return
		}
		id, ok := sess.Values[cookieIDkey].(int)
		if !ok {
			// missing id
			log("session id is missing from cookie")
			accessDenied()
			return
		}
		uis, err := sa.DL.GetUISession(id)
		if err != nil {
			log("error getting session by id: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if uis == nil {
			// not found in db
			log("session %v not found in db, access denied", id)
			accessDenied()
			return
		}
		if !uis.IsValid() {
			if err := sa.DL.DeleteUISession(id); err != nil {
				log("error deleting session: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			log("session %v isn't valid, access denied", id)
			accessDenied()
			return
		}
		f(w, r.Clone(withSession(r.Context(), *uis)))
	}
}

type userPermissions struct {
	enforce    bool
	targetRepo string
	instID     int64
	deckey     [32]byte
	gcfunc     func(tkn string) ghclient.GitHubAppInstallationClient
}

func userPermissionsClient(oauth OAuthConfig, repo string) *userPermissions {
	return &userPermissions{
		enforce:    oauth.Enforce,
		targetRepo: repo,
		instID:     oauth.AppInstallationID,
		deckey:     oauth.UserTokenEncKey,
		gcfunc:     oauth.AppGHClientFactoryFunc,
	}
}

// GetUserVisibleRepos returns the names of all repos (owner/repo) for which the authenticated user has "pull" permissions
func (up *userPermissions) GetUserVisibleRepos(ctx context.Context, uis models.UISession) ([]string, error) {
	if !up.enforce {
		// if we're not enforcing oauth, we want to short-circuit the auth check
		// and simply return whatever repo the request is targeting
		return []string{up.targetRepo}, nil
	}
	tkn, err := uis.GetUserToken(up.deckey)
	if err != nil {
		return nil, errors.Wrap(err, "error decrypting user token")
	}

	ghc := up.gcfunc(tkn)

	rps, err := ghc.GetUserAppRepoPermissions(ctx, up.instID)
	if err != nil {
		return nil, errors.Wrap(err, "error getting user visible repos")
	}
	out := []string{}
	for _, r := range rps {
		if r.Pull {
			out = append(out, r.Repo)
		}
	}
	return out, nil
}

// GetUserWritableRepos returns the names of all repos (owner/repo) for which the authenticated user has "admin" or "push" permissions
func (up *userPermissions) GetUserWritableRepos(ctx context.Context, uis models.UISession) (map[string]ghclient.AppRepoPermissions, error) {
	if !up.enforce {
		// if we're not enforcing oauth, we want to short-circuit the auth check
		// and simply return whatever repo the request is targeting
		return map[string]ghclient.AppRepoPermissions{
			up.targetRepo: ghclient.AppRepoPermissions{
				Repo:  up.targetRepo,
				Admin: true,
				Push:  true,
				Pull:  true,
			},
		}, nil
	}
	tkn, err := uis.GetUserToken(up.deckey)
	if err != nil {
		return nil, errors.Wrap(err, "error decrypting user token")
	}

	ghc := up.gcfunc(tkn)

	rps, err := ghc.GetUserAppRepoPermissions(ctx, up.instID)
	if err != nil {
		return nil, errors.Wrap(err, "error getting user repos")
	}
	out := make(map[string]ghclient.AppRepoPermissions, len(rps))
	for _, r := range rps {
		if r.Admin || r.Push {
			out[r.Repo] = r
		}
	}
	return out, nil
}
