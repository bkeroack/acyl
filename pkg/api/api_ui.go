package api

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/bkeroack/acyl/pkg/config"
	"github.com/bkeroack/acyl/pkg/ghclient"
	"github.com/bkeroack/acyl/pkg/models"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"

	"github.com/google/uuid"
	"github.com/pkg/errors"

	"github.com/bkeroack/acyl/pkg/persistence"

	mh "github.com/dollarshaveclub/metahelm/pkg/metahelm"
	muxtrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/gorilla/mux"
)

type uiBranding struct {
	config.UIBrandingConfig
	FaviconType string
}

// OAuthConfig models the configuration needed to support a GitHub OAuth authn/authz flow
type OAuthConfig struct {
	Enforce                     bool                                                    // Enforce OAuth authn/authz for protected routes
	DummySessionUser            string                                                  // Create a new authenticated dummy session with username if no session exists (for testing)
	AppInstallationID           int64                                                   // GitHub App installation ID
	ClientID, ClientSecret      string                                                  // GitHub App ClientID/secret
	ValidateURL                 url.URL                                                 // URL to validate callback code and exchange for bearer token
	AuthURL                     url.URL                                                 // URL to initiate OAuth flow
	AppGHClientFactoryFunc      func(token string) ghclient.GitHubAppInstallationClient // Function that produces a token-scoped GitHubAppInstallationClient
	CookieAuthKey, CookieEncKey [32]byte                                                // Cookie authentication & encryption keys (AES-256)
	UserTokenEncKey             [32]byte                                                // Secretbox encryption key for user token
	cookiestore                 sessions.Store
	errorPage                   []byte
	hc                          http.Client
}

func (oac *OAuthConfig) SetValidateURL(vurl string) error {
	pvurl, err := url.Parse(vurl)
	if err != nil || pvurl == nil {
		return errors.Wrap(err, "error parsing validate url")
	}
	oac.ValidateURL = *pvurl
	return nil
}

func (oac *OAuthConfig) SetAuthURL(aurl string) error {
	paurl, err := url.Parse(aurl)
	if err != nil || paurl == nil {
		return errors.Wrap(err, "error parsing auth url")
	}
	oac.AuthURL = *paurl
	return nil
}

type uiapi struct {
	apiBase
	dl          persistence.DataLayer
	apiBaseURL  string
	assetsPath  string
	routePrefix string
	reload      bool
	views       map[string]*template.Template
	viewmtx     sync.RWMutex
	branding    uiBranding
	oauth       OAuthConfig
	stop        chan struct{}
}

var partials = map[string]string{
	"header": path.Join("views", "partials", "header.html"),
	"footer": path.Join("views", "partials", "footer.html"),
}

var viewPaths = map[string]string{
	"status":         path.Join("views", "status.html"),
	"auth_error":     path.Join("views", "autherror.html"),
	"home":           path.Join("views", "home.html"),
	"env":            path.Join("views", "env.html"),
	"denied":         path.Join("views", "denied.html"),
	"help":           path.Join("views", "help.html"),
	"failure_report": path.Join("views", "failurereport.html"),
	"user_settings":  path.Join("views", "usersettings.html"),
}

func newSessionsCookieStore(oauthCfg OAuthConfig) sessions.Store {
	cstore := sessions.NewCookieStore(oauthCfg.CookieAuthKey[:], oauthCfg.CookieEncKey[:])
	cstore.Options.SameSite = http.SameSiteLaxMode // Lax mode is required so callback request contains the session cookie
	cstore.Options.Secure = true
	if oauthCfg.DummySessionUser != "" {
		cstore.Options.Secure = false
	}
	cstore.MaxAge(int(cookieMaxAge.Seconds()))
	return cstore
}

func newUIAPI(baseURL, assetsPath, routePrefix string, reload bool, branding config.UIBrandingConfig, dl persistence.DataLayer, oauthCfg OAuthConfig, logger *log.Logger) (*uiapi, error) {
	if assetsPath == "" || routePrefix == "" ||
		dl == nil {
		return nil, errors.New("all dependencies required")
	}
	oauthCfg.cookiestore = newSessionsCookieStore(oauthCfg)
	api := &uiapi{
		apiBase: apiBase{
			logger: logger,
		},
		apiBaseURL:  baseURL,
		assetsPath:  assetsPath,
		routePrefix: routePrefix,
		dl:          dl,
		oauth:       oauthCfg,
		reload:      reload,
		views:       make(map[string]*template.Template, len(viewPaths)),
		stop:        make(chan struct{}),
	}
	for k := range viewPaths {
		if err := api.loadTemplate(k); err != nil {
			return nil, errors.Wrap(err, "error reading view template")
		}
	}
	if branding.LogoURL == "" && branding.Title == "" {
		branding = config.DefaultUIBranding
	}
	err := api.processBranding(branding)
	if err != nil {
		return nil, errors.Wrap(err, "error processing branding")
	}
	api.cacheRenderedAuthErrorPage()
	return api, nil
}

// StartSessionsCleanup begins periodic async cleanup of expired UI sessions
func (api *uiapi) StartSessionsCleanup() {
	rand.Seed(time.Now().UTC().UnixNano())
	d := time.Duration(18+rand.Intn(7)) * time.Hour // random interval between 18-24 hours
	log.Printf("ui api: starting periodic ui sessions cleanup: interval: %v", d)
	ticker := time.NewTicker(d)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n, err := api.dl.DeleteExpiredUISessions()
				if err != nil {
					log.Printf("ui api: error deleting expired sessions: %v", err)
					continue
				}
				log.Printf("ui api: deleted %v expired ui sessions", n)
			case <-api.stop:
				log.Printf("ui api: close signalled, stopping sessions cleanup")
				return
			}
		}
	}()
}

// Close stops any async processes such as sessions cleanup
func (api *uiapi) Close() {
	close(api.stop)
}

func (api *uiapi) processBranding(b config.UIBrandingConfig) error {
	api.branding.UIBrandingConfig = b
	fiurl, err := url.Parse(b.FaviconURL)
	if err != nil {
		return errors.Wrap(err, "error in favicon url")
	}
	switch filepath.Ext(fiurl.Path) {
	case ".ico":
		api.branding.FaviconType = "image/x-icon"
	case ".gif":
		api.branding.FaviconType = "image/gif"
	case ".png":
		api.branding.FaviconType = "image/png"
	}
	if _, err := url.Parse(b.LogoURL); err != nil {
		return errors.Wrap(err, "error in logo url")
	}
	return nil
}

func (api *uiapi) loadTemplate(name string) error {
	v := viewPaths[name]
	if v == "" {
		return fmt.Errorf("view not found: %v", name)
	}
	var tmpl *template.Template
	var err error
	var b []byte
	// partials are parsed & available to every view
	for k, v := range partials {
		b, err = ioutil.ReadFile(path.Join(api.assetsPath, v))
		if err != nil {
			return errors.Wrap(err, "error reading partial")
		}
		if tmpl == nil {
			tmpl, err = template.New(k).Parse(string(b))
		} else {
			tmpl, err = tmpl.New(k).Parse(string(b))
		}
		if err != nil {
			return errors.Wrapf(err, "error parsing template: %v", v)
		}
	}

	b, err = ioutil.ReadFile(path.Join(api.assetsPath, v))
	if err != nil {
		return errors.Wrap(err, "error reading asset template")
	}
	tmpl, err = tmpl.New(name).Parse(string(b))
	if err != nil {
		return errors.Wrapf(err, "error parsing asset template: %v", name)
	}
	api.viewmtx.Lock()
	api.views[name] = tmpl
	api.viewmtx.Unlock()
	return nil
}

const (
	baseErrorRoute  = "/oauth/accessdenied"
	baseDeniedRoute = "/denied"
)

func (api *uiapi) register(r *muxtrace.Router) error {
	if r == nil {
		return fmt.Errorf("router is nil")
	}
	urlPath := func(route string) string {
		return api.routePrefix + route
	}

	// UI routes
	r.HandleFunc(urlPath("/event/status"), middlewareChain(api.statusHandler, api.authenticate)).Methods("GET")
	r.HandleFunc(urlPath("/home"), middlewareChain(api.homeHandler, api.authenticate)).Methods("GET")
	r.HandleFunc(urlPath("/env/{envname}"), middlewareChain(api.envHandler, api.authenticate)).Methods("GET")
	r.HandleFunc(urlPath("/help"), middlewareChain(api.helpHandler, api.authenticate)).Methods("GET")
	r.HandleFunc(urlPath("/event/status/failure_report"), middlewareChain(api.failureReportHandler, api.authenticate)).Methods("GET")
	r.HandleFunc(urlPath("/user/settings"), middlewareChain(api.userSettingsHandler, api.authenticate)).Methods("GET")

	// unauthenticated OAuth callback
	r.HandleFunc(urlPath("/oauth/callback"), middlewareChain(api.authCallbackHandler)).Methods("GET")

	// unauthenticated error pages
	r.HandleFunc(urlPath(baseErrorRoute), middlewareChain(api.authErrorHandler)).Methods("GET")
	r.HandleFunc(urlPath(baseDeniedRoute), middlewareChain(api.deniedHandler)).Methods("GET")

	// static assets
	r.PathPrefix(urlPath("/static/")).Handler(http.StripPrefix(urlPath("/static/"), http.FileServer(http.Dir(path.Join(api.assetsPath, "assets")))))

	// api documentation
	r.PathPrefix(urlPath("/apidocs/")).Handler(http.StripPrefix(urlPath("/apidocs/"), http.FileServer(http.Dir(path.Join(api.assetsPath, "apidocs")))))

	return nil
}

func (api *uiapi) render(w http.ResponseWriter, name string, td interface{}) {
	w.Header().Add("Content-Type", "text/html")
	if api.reload {
		if err := api.loadTemplate(name); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			api.logger.Printf("error serving ui template: %v", err)
			return
		}
	}
	api.viewmtx.RLock()
	defer api.viewmtx.RUnlock()
	if _, ok := api.views[name]; !ok {
		w.WriteHeader(http.StatusInternalServerError)
		api.logger.Printf("view not found: %v", name)
		return
	}
	if err := api.views[name].Execute(w, td); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		api.logger.Printf("error serving ui template: %v: %v", name, err)
	}
}

type BaseTemplateData struct {
	APIBaseURL, GitHubUser              string
	Branding                            uiBranding
	RenderEventLink, RenderUserSettings bool
}

func (api *uiapi) defaultBaseTemplateData(session *models.UISession) BaseTemplateData {
	btd := BaseTemplateData{
		APIBaseURL:         api.apiBaseURL,
		Branding:           api.branding,
		RenderEventLink:    false,
		RenderUserSettings: false,
	}
	if session != nil {
		if session.GitHubUser != "" {
			btd.GitHubUser = session.GitHubUser
			btd.RenderUserSettings = true
		}
	}
	return btd
}

type StatusTemplateData struct {
	BaseTemplateData
	LogKey string
}

func (api *uiapi) getEventFromIDString(idstr string) (*models.EventLog, error) {
	id, err := uuid.Parse(idstr)
	if err != nil {
		return nil, errors.Wrap(err, "invalid event id")
	}
	elog, err := api.dl.GetEventLogByID(id)
	if err != nil {
		return nil, errors.Wrap(err, "error getting event log")
	}
	return elog, nil
}

func (api *uiapi) statusHandler(w http.ResponseWriter, r *http.Request) {
	uis, err := getSessionFromContext(r.Context())
	if err != nil {
		api.rlogger(r).Logf("error getting ui session: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	elog, err := api.getEventFromIDString(r.URL.Query().Get("id"))
	if err != nil {
		api.logger.Printf("error serving status page: %v", err)
		if strings.Contains(err.Error(), "invalid") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if elog == nil {
		api.logger.Printf("error serving status page: event log not found: %v", r.URL.Query().Get("id"))
		w.WriteHeader(http.StatusNotFound)
		return
	}
	td := StatusTemplateData{
		BaseTemplateData: api.defaultBaseTemplateData(&uis),
		LogKey:           elog.LogKey.String(),
	}
	td.RenderEventLink = true
	td.BaseTemplateData.RenderUserSettings = true
	api.render(w, "status", &td)
}

/*
UI authn/authz flow:

client requests a protected UI route without session cookie or invalid/expired session:

- server detects missing/invalid session, goes into auth flow
- creates new session:
	* authenticated = false
	* target_route = the route they attempted to access
	* state = random data
- session cookie is the session ID (encrypted)
- server response is 302 to github auth endpoint w/ state param

client successfully auths w/ github and is redirected to acyl callback URL:

- request contains session cookie set above (session ID)
- server validates code and state parameters supplied by GitHub in the callback request
	* this authenticates client as a valid GitHub user
- server then validates that the user is authorized:
	* GitHub user must have permissions to this installation of the GitHub app
	* GitHub user must have permissions to at least one repo that this installation has access to
- if these conditions are true, server updates session:
	* authenticated = true
	* github_user is set
- server returns a 302 redirect to target_route, with same session cookie
- client requests the protected UI route again with valid session cookie
- server decrypts and validates the session id in the database, response is the content for the protected route

Error flow:

- callback URL detects invalid state or code, or the authenticated user fails authz:
	* server returns 302 to the unauthenticated error page (/ui/oauth/accessdenied)
*/

const (
	uiSessionName  = "user-session"
	cookieIDkey    = "id"
	stateSizeBytes = 32
	cookieMaxAge   = 30 * 24 * time.Hour
)

// authenticate is a middleware that ensures a request to a protected UI path has an authenticated session
// authenticate performs authentication but does not perform specific authorization
// if a user has a valid session, they are authenticated and have at least one visible repo
// if not, it creates a new session and redirects to the provided oauth URL
func (ui *uiapi) authenticate(f http.HandlerFunc) http.HandlerFunc {
	rr := rand.New(rand.NewSource(time.Now().UTC().UnixNano()))
	// for convenience we use only alphanumerics for the state, so we don't have to base64-encode
	rset := []rune(`abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789`)
	// randstate generates a random state for a session.
	randstate := func(n uint) []byte {
		out := make([]rune, n)
		for i := range out {
			out[i] = rset[rr.Intn(len(rset))]
		}
		return []byte(string(out))
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !ui.oauth.Enforce {
			// create a dummy session for this request
			ui.rlogger(r).Logf("dummy session user set: %v, setting session to authenticated", ui.oauth.DummySessionUser)
			uis := models.UISession{}
			uis.EncryptandSetUserToken([]byte(`dummy token`), ui.oauth.UserTokenEncKey)
			uis.Authenticated = true
			uis.GitHubUser = ui.oauth.DummySessionUser
			f(w, r.Clone(withSession(r.Context(), uis)))
			return
		}
		redirectToAuth := func(s *sessions.Session) {
			if s == nil {
				var err error
				s, err = ui.oauth.cookiestore.New(r, uiSessionName)
				if err != nil {
					ui.rlogger(r).Logf("error creating new cookie session: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
			state := randstate(stateSizeBytes)
			ip, _, _ := net.SplitHostPort(r.RemoteAddr) // ignore the error if the remote addr can't be parsed
			troute := r.URL.Path
			if r.URL.RawQuery != "" {
				troute += "?" + r.URL.RawQuery
			}
			id, err := ui.dl.CreateUISession(troute, state, net.ParseIP(ip), r.UserAgent(), time.Now().UTC().Add(cookieMaxAge))
			if err != nil {
				ui.rlogger(r).Logf("error creating session in db: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			s.Values[cookieIDkey] = id
			if err := s.Save(r, w); err != nil {
				ui.rlogger(r).Logf("error saving session in cookie store: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// if dummy session, don't redirect to auth
			// set session as authenticated w/ username, return route content
			if ui.oauth.DummySessionUser != "" {
				ui.rlogger(r).Logf("dummy session user set: %v, setting session to authenticated", ui.oauth.DummySessionUser)
				uis := models.UISession{}
				uis.EncryptandSetUserToken([]byte(`dummy token`), ui.oauth.UserTokenEncKey)
				if err := ui.dl.UpdateUISession(id, ui.oauth.DummySessionUser, uis.EncryptedUserToken, true); err != nil {
					ui.rlogger(r).Logf("error updating dummy ui session: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				uis.Authenticated = true
				uis.GitHubUser = ui.oauth.DummySessionUser
				f(w, r.Clone(withSession(r.Context(), uis)))
				return
			}
			aurl := ui.oauth.AuthURL
			qvals := make(url.Values, 2)
			qvals.Add("state", string(state))
			qvals.Add("client_id", ui.oauth.ClientID)
			aurl.RawQuery = qvals.Encode()
			w.Header().Add("Location", aurl.String())
			w.WriteHeader(http.StatusFound)
		}
		// Get returns the session from the cookie or creates a new session if missing
		sess, err := ui.oauth.cookiestore.Get(r, uiSessionName)
		if err != nil {
			// invalid cookie or failure to authenticate/decrypt
			ui.rlogger(r).Logf("error getting session, redirecting to auth: %v", err)
			redirectToAuth(sess) // Get returns a new session on error
			return
		}
		if sess.IsNew {
			ui.rlogger(r).Logf("session missing from request, redirecting to auth")
			redirectToAuth(sess)
			return
		}
		id, ok := sess.Values[cookieIDkey].(int)
		if !ok {
			// missing id
			ui.rlogger(r).Logf("session id is missing from cookie")
			redirectToAuth(nil)
			return
		}
		uis, err := ui.dl.GetUISession(id)
		if err != nil {
			ui.rlogger(r).Logf("error getting session by id: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if uis == nil {
			// not found in db
			ui.rlogger(r).Logf("session %v not found in db, redirecting to auth", id)
			redirectToAuth(nil)
			return
		}
		if !uis.IsValid() {
			if err := ui.dl.DeleteUISession(id); err != nil {
				ui.rlogger(r).Logf("error deleting session: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			ui.rlogger(r).Logf("session %v isn't valid, redirecting to auth", id)
			redirectToAuth(nil)
			return
		}
		// we have a valid, authenticated session
		if err := sess.Save(r, w); err != nil {
			ui.rlogger(r).Logf("error saving valid session in cookie store: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f(w, r.Clone(withSession(r.Context(), *uis)))
	}
}

// authCallbackHandler handles the OAuth GitHub callback, verifies that the authenticated GitHub
// user account is authorized to access UI resources and updates the session accordingly
func (api *uiapi) authCallbackHandler(w http.ResponseWriter, r *http.Request) {
	log := func(msg string, args ...interface{}) {
		log.Printf("ui auth callback: "+msg, args...)
	}
	redirectToError := func() {
		w.Header().Add("Location", api.apiBaseURL+api.routePrefix+baseErrorRoute)
		w.WriteHeader(http.StatusFound)
	}
	sess, err := api.oauth.cookiestore.Get(r, uiSessionName)
	if err != nil || sess.IsNew {
		// invalid cookie or failure to authenticate/decrypt
		log("error getting session or new session, redirecting to error: %v", err)
		redirectToError() // Get returns a new session on error
		return
	}
	id, ok := sess.Values[cookieIDkey].(int)
	if !ok {
		// missing id
		log("session id is missing from cookie")
		redirectToError()
		return
	}
	uis, err := api.dl.GetUISession(id)
	if err != nil {
		log("error getting session by id: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if uis == nil {
		// not found in db
		log("session %v not found in db, redirecting to auth", id)
		redirectToError()
		return
	}
	state := r.URL.Query().Get("state")
	if state != string(uis.State) {
		log("missing or invalid state: %v", state)
		redirectToError()
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		log("missing code: %v", code)
		redirectToError()
		return
	}
	ghuser, tkn, err := api.getAndAuthorizeGitHubUser(r.Context(), code, state)
	if err != nil {
		log("error authorizing github user: %v", err)
		redirectToError()
		return
	}
	if err := uis.EncryptandSetUserToken([]byte(tkn), api.oauth.UserTokenEncKey); err != nil {
		log("error encrypting user token: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := api.dl.UpdateUISession(id, ghuser, uis.EncryptedUserToken, true); err != nil {
		log("error updating session: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := sess.Save(r, w); err != nil {
		log("error saving session to cookie store: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Add("Location", api.apiBaseURL+uis.TargetRoute)
	w.WriteHeader(http.StatusFound)
}

// getAndAuthorizeGitHubUser verifies that the code supplied to the redirect is valid, and that the GitHub user is authorized to view UI resources
// A GitHub user is considered authorized if and only if all the following are true:
// - the user has explicit permissions for this installation of this GitHub app
// - the user has at least one repository accessible via this installation
func (api *uiapi) getAndAuthorizeGitHubUser(ctx context.Context, code, state string) (string, string, error) {
	form := make(url.Values, 4)
	form.Add("client_id", api.oauth.ClientID)
	form.Add("client_secret", api.oauth.ClientSecret)
	form.Add("code", code)
	form.Add("state", state)
	req, err := http.NewRequest("POST", api.oauth.ValidateURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", errors.Wrap(err, "error creating validate request")
	}
	resp, err := api.oauth.hc.Do(req)
	if err != nil {
		return "", "", errors.Wrap(err, "error validating code")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("validation response indicates error: %v", resp.StatusCode)
	}
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", "", errors.Wrap(err, "error reading response body")
	}
	vals, err := url.ParseQuery(string(b))
	if err != nil {
		return "", "", errors.Wrap(err, "error parsing response")
	}
	tkn := vals.Get("access_token")
	if tkn == "" || vals.Get("token_type") != "bearer" {
		return "", "", fmt.Errorf("invalid validation reponse or missing values: %v", string(b))
	}
	rc := api.oauth.AppGHClientFactoryFunc(tkn)
	ghuser, err := rc.GetUser(ctx)
	if err != nil {
		return "", "", errors.Wrap(err, "error getting github user from token")
	}
	insts, err := rc.GetUserAppInstallations(ctx)
	if err != nil {
		return "", "", errors.Wrap(err, "error getting user app installations")
	}
	if !insts.IDPresent(api.oauth.AppInstallationID) {
		return "", "", fmt.Errorf("app installation id missing from user installations")
	}
	repos, err := rc.GetUserAppRepos(ctx, api.oauth.AppInstallationID)
	if err != nil {
		return "", "", fmt.Errorf("error getting user app installation repos")
	}
	if len(repos) == 0 {
		return "", "", fmt.Errorf("user %v has zero app installation repos", ghuser)
	}
	return ghuser, tkn, nil
}

// cacheRenderedAuthErrorPage renders the auth error page and saves the output for future use
// it must be called via uiapi constructor only as no locking is performed
func (api *uiapi) cacheRenderedAuthErrorPage() {
	if err := api.loadTemplate("auth_error"); err != nil {
		api.logger.Printf("error loading auth_error template: %v", err)
		return
	}
	b := bytes.Buffer{}
	if err := api.views["auth_error"].Execute(&b, api.defaultBaseTemplateData(nil)); err != nil {
		api.logger.Printf("error rendering auth_error template: %v", err)
		return
	}
	api.oauth.errorPage = b.Bytes()
}

func (api *uiapi) authErrorHandler(w http.ResponseWriter, r *http.Request) {
	if api.reload || len(api.oauth.errorPage) == 0 {
		api.render(w, "auth_error", api.defaultBaseTemplateData(nil))
		return
	}
	w.Header().Add("Content-Type", "text/html")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write(api.oauth.errorPage)
}

type homeTmplData struct {
	BaseTemplateData
}

func (api *uiapi) homeHandler(w http.ResponseWriter, r *http.Request) {
	uis, err := getSessionFromContext(r.Context())
	if err != nil {
		api.rlogger(r).Logf("error getting ui session: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	td := homeTmplData{
		BaseTemplateData: api.defaultBaseTemplateData(&uis),
	}
	api.render(w, "home", &td)
}

type envTmplData struct {
	BaseTemplateData
	EnvName       string
	RenderActions bool
}

func repoInRepos(repos []string, repo string) bool {
	for _, r := range repos {
		if r == repo {
			return true
		}
	}
	return false
}

func (api *uiapi) envHandler(w http.ResponseWriter, r *http.Request) {
	uis, err := getSessionFromContext(r.Context())
	if err != nil {
		api.rlogger(r).Logf("error getting ui session: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	td := envTmplData{
		BaseTemplateData: api.defaultBaseTemplateData(&uis),
		EnvName:          mux.Vars(r)["envname"],
		RenderActions:    false,
	}
	if td.EnvName == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	env, err := api.dl.GetQAEnvironment(r.Context(), td.EnvName)
	if err != nil {
		api.rlogger(r).Logf("error getting env from db: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if env == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	repos, err := userPermissionsClient(api.oauth, env.Repo).GetUserVisibleRepos(r.Context(), uis)
	if err != nil {
		api.rlogger(r).Logf("error getting user visible repos: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !repoInRepos(repos, env.Repo) {
		w.Header().Add("Location", api.apiBaseURL+api.routePrefix+baseDeniedRoute)
		w.WriteHeader(http.StatusFound)
		return
	}
	reposWritable, err := userPermissionsClient(api.oauth, env.Repo).GetUserWritableRepos(r.Context(), uis)
	if err != nil {
		api.rlogger(r).Logf("error getting user writable repos: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if _, ok := reposWritable[env.Repo]; ok {
		td.RenderActions = true
	}
	api.render(w, "env", &td)
}

func (api *uiapi) helpHandler(w http.ResponseWriter, r *http.Request) {
	uis, err := getSessionFromContext(r.Context())
	if err != nil {
		api.rlogger(r).Logf("error getting ui session: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	api.render(w, "help", api.defaultBaseTemplateData(&uis))
}

type failureReportTmplData struct {
	BaseTemplateData
	EnvName, EventID, PullRequestURL string
	StartedTime, FailedTime          time.Time
	FailedResources                  mh.ChartError
}

func (api *uiapi) failureReportHandler(w http.ResponseWriter, r *http.Request) {
	uis, err := getSessionFromContext(r.Context())
	if err != nil {
		api.rlogger(r).Logf("error getting ui session: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		api.rlogger(r).Logf("error getting id from url query")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	elog, err := api.getEventFromIDString(id)
	if err != nil {
		api.logger.Printf("error serving chart error page: %v", err)
		if strings.Contains(err.Error(), "invalid") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if elog == nil {
		api.logger.Printf("error serving chart error page: event log not found: %v", id)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	td := failureReportTmplData{
		BaseTemplateData: api.defaultBaseTemplateData(&uis),
		EnvName:          elog.EnvName,
		EventID:          id,
		PullRequestURL:   fmt.Sprintf("https://github.com/%v/pull/%v", elog.Repo, elog.PullRequest),
		StartedTime:      elog.Status.Config.Started,
		FailedTime:       elog.Status.Config.Completed,
		FailedResources:  elog.Status.Config.FailedResources,
	}
	api.render(w, "failure_report", &td)
}

func (api *uiapi) userSettingsHandler(w http.ResponseWriter, r *http.Request) {
	uis, err := getSessionFromContext(r.Context())
	if err != nil {
		api.rlogger(r).Logf("error getting ui session: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	api.render(w, "user_settings", api.defaultBaseTemplateData(&uis))
}

func (api *uiapi) deniedHandler(w http.ResponseWriter, r *http.Request) {
	api.render(w, "denied", api.defaultBaseTemplateData(nil))
}
