package api

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/securecookie"
	"github.com/pkg/errors"

	"github.com/bkeroack/acyl/pkg/config"
	"github.com/bkeroack/acyl/pkg/ghclient"
	"github.com/bkeroack/acyl/pkg/persistence"
)

func randomCookieKey() [32]byte {
	rand.Seed(time.Now().UTC().UnixNano())
	out := [32]byte{}
	rand.Read(out[:])
	return out
}

func TestUI_Authenticate(t *testing.T) {
	type fields struct {
		cookiekeys [2][32]byte
	}
	var sessID int64
	authURL := "https://github.com/some/path"
	clientID := "asdf"
	authKey := randomCookieKey()
	encKey := randomCookieKey()
	expectStatus := func(rc *httptest.ResponseRecorder, status int) error {
		if rc.Result().StatusCode != status {
			return fmt.Errorf("unexpected status code: %v (wanted %v)", rc.Result().StatusCode, status)
		}
		return nil
	}
	expectOK := func(rc *httptest.ResponseRecorder, dl persistence.UISessionsDataLayer) error {
		return expectStatus(rc, 200)
	}
	expectAuthRedirect := func(rc *httptest.ResponseRecorder, dl persistence.UISessionsDataLayer) error {
		aurl, err := url.Parse(rc.Result().Header.Get("Location"))
		if err != nil {
			return errors.Wrap(err, "error parsing location header")
		}
		if state := aurl.Query().Get("state"); state == "" {
			return errors.New("empty or missing state")
		}
		if cid := aurl.Query().Get("client_id"); cid != clientID {
			return fmt.Errorf("empty or incorrect client ID: %v", cid)
		}
		return expectStatus(rc, 302)
	}
	tests := []struct {
		name        string
		fields      fields
		wantErr     bool
		dbSetupFunc func(dl persistence.UISessionsDataLayer)
		requestFunc func() *http.Request
		verifyfunc  func(*httptest.ResponseRecorder, persistence.UISessionsDataLayer) error
	}{
		{
			name: "authenticated",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: func(dl persistence.UISessionsDataLayer) {
				id, _ := dl.CreateUISession("/some/other/path", []byte("asdf"), net.ParseIP("10.0.0.0"), "some agent", time.Now().UTC().Add(24*time.Hour))
				dl.UpdateUISession(id, "johndoe", []byte(`asdf`), true)
				atomic.StoreInt64(&sessID, int64(id))
			},
			requestFunc: func() *http.Request {
				s := securecookie.New(authKey[:], encKey[:])
				enc, _ := s.Encode(uiSessionName, map[interface{}]interface{}{cookieIDkey: int(atomic.LoadInt64(&sessID))})
				req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
				req.AddCookie(&http.Cookie{
					Name:  uiSessionName,
					Value: enc,
					Path:  "/",
				})
				return req
			},
			verifyfunc: expectOK,
		},
		{
			name: "unauthenticated",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: func(dl persistence.UISessionsDataLayer) {},
			requestFunc: func() *http.Request {
				req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
				return req
			},
			verifyfunc: expectAuthRedirect,
		},
		{
			name: "bad keys",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: func(dl persistence.UISessionsDataLayer) {},
			requestFunc: func() *http.Request {
				badAuthKey, badEncKey := randomCookieKey(), randomCookieKey()
				s := securecookie.New(badAuthKey[:], badEncKey[:])
				enc, _ := s.Encode(uiSessionName, map[interface{}]interface{}{cookieIDkey: int(atomic.LoadInt64(&sessID))})
				req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
				req.AddCookie(&http.Cookie{
					Name:  uiSessionName,
					Value: enc,
					Path:  "/",
				})
				return req
			},
			verifyfunc: expectAuthRedirect,
		},
		{
			name: "missing id",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: func(dl persistence.UISessionsDataLayer) {},
			requestFunc: func() *http.Request {
				s := securecookie.New(authKey[:], encKey[:])
				cookieKey := "invalid"
				enc, _ := s.Encode(uiSessionName, map[interface{}]interface{}{cookieKey: 0})
				req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
				req.AddCookie(&http.Cookie{
					Name:  uiSessionName,
					Value: enc,
					Path:  "/",
				})
				return req
			},
			verifyfunc: expectAuthRedirect,
		},
		{
			name: "missing session in db",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: func(dl persistence.UISessionsDataLayer) {},
			requestFunc: func() *http.Request {
				s := securecookie.New(authKey[:], encKey[:])
				enc, _ := s.Encode(uiSessionName, map[interface{}]interface{}{cookieIDkey: int(atomic.LoadInt64(&sessID))})
				req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
				req.AddCookie(&http.Cookie{
					Name:  uiSessionName,
					Value: enc,
					Path:  "/",
				})
				return req
			},
			verifyfunc: expectAuthRedirect,
		},
		{
			name: "expired session",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: func(dl persistence.UISessionsDataLayer) {
				id, _ := dl.CreateUISession("/some/other/path", []byte("asdf"), net.ParseIP("10.0.0.0"), "some agent", time.Now().UTC().Add(1*time.Millisecond))
				dl.UpdateUISession(id, "johndoe", []byte(`asdf`), true)
				atomic.StoreInt64(&sessID, int64(id))
			},
			requestFunc: func() *http.Request {
				s := securecookie.New(authKey[:], encKey[:])
				enc, _ := s.Encode(uiSessionName, map[interface{}]interface{}{cookieIDkey: int(atomic.LoadInt64(&sessID))})
				req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
				req.AddCookie(&http.Cookie{
					Name:  uiSessionName,
					Value: enc,
					Path:  "/",
				})
				time.Sleep(2 * time.Millisecond)
				return req
			},
			verifyfunc: expectAuthRedirect,
		},
		{
			// verifies that the redirect is correct and proper session state is set up
			name: "redirect",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: func(dl persistence.UISessionsDataLayer) {},
			requestFunc: func() *http.Request {
				req, _ := http.NewRequest("GET", "https://example.com/some/protected/path?somekey=someval", nil)
				req.RemoteAddr = "10.0.0.0:2000"
				req.Header.Add("User-Agent", "some useragent")
				return req
			},
			verifyfunc: func(rc *httptest.ResponseRecorder, dl persistence.UISessionsDataLayer) error {
				if err := expectStatus(rc, 302); err != nil {
					return err
				}
				uis, err := dl.GetUISession(1)
				if err != nil {
					return errors.Wrap(err, "error getting session")
				}
				if uis == nil {
					return errors.New("session missing from db")
				}
				if uis.Authenticated {
					return errors.New("session should not be authenticated yet")
				}
				if time.Now().UTC().After(uis.Expires) {
					return fmt.Errorf("session should not be expired: %v", uis.Expires)
				}
				if uis.GitHubUser != "" {
					return fmt.Errorf("github user shouldn't be set: %v", uis.GitHubUser)
				}
				if uis.ClientIP != "10.0.0.0" {
					return fmt.Errorf("bad client ip: %v", uis.ClientIP)
				}
				if uis.UserAgent != "some useragent" {
					return fmt.Errorf("bad user agent: %v", uis.UserAgent)
				}
				if uis.TargetRoute != "/some/protected/path?somekey=someval" {
					return fmt.Errorf("bad target route: %v", uis.TargetRoute)
				}
				rurl, err := url.Parse(rc.Result().Header.Get("Location"))
				if err != nil {
					return errors.Wrap(err, "error parsing redirect location")
				}
				if rurl.Scheme != "https" {
					return fmt.Errorf("bad redirect scheme: %v", rurl.Scheme)
				}
				if rurl.Host != "github.com" {
					return fmt.Errorf("bad redirect host: %v", rurl.Host)
				}
				if rurl.Path != "/some/path" {
					return fmt.Errorf("bad redirect path: %v", rurl.Path)
				}
				if rs := rurl.Query().Get("state"); rs != string(uis.State) {
					return fmt.Errorf("bad redirect state: %v", rs)
				}
				return nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dl := persistence.NewFakeDataLayer()
			tt.dbSetupFunc(dl)
			oauthcfg := OAuthConfig{
				Enforce:                true,
				AppInstallationID:      1,
				ClientID:               clientID,
				ClientSecret:           "asdf",
				AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient { return &ghclient.FakeRepoClient{} },
				CookieAuthKey:          tt.fields.cookiekeys[0],
				CookieEncKey:           tt.fields.cookiekeys[1],
			}
			oauthcfg.SetAuthURL(authURL)
			oauthcfg.SetValidateURL("https://github.com/oauth/login")
			uapi, err := newUIAPI(
				"https://something.com",
				"../../ui",
				"ui/",
				false,
				config.DefaultUIBranding,
				dl,
				oauthcfg,
				log.New(os.Stderr, "", log.LstdFlags))
			if err != nil {
				t.Fatalf("error creating ui API: %v", err)
			}
			rc := httptest.NewRecorder()
			h := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
			mh := middlewareChain(h, uapi.authenticate)
			mh(rc, tt.requestFunc())
			if err := tt.verifyfunc(rc, dl); err != nil {
				if !tt.wantErr {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestUI_AuthCallbackHandler(t *testing.T) {
	type fields struct {
		cookiekeys [2][32]byte
	}
	var sessID int64
	authKey := randomCookieKey()
	encKey := randomCookieKey()
	defaultdbSetupFunc := func(dl persistence.UISessionsDataLayer) {
		id, _ := dl.CreateUISession("/some/other/path", []byte("asdf"), net.ParseIP("10.0.0.0"), "some agent", time.Now().UTC().Add(24*time.Hour))
		atomic.StoreInt64(&sessID, int64(id))
	}
	defaultrequestFunc := func() *http.Request {
		s := securecookie.New(authKey[:], encKey[:])
		enc, _ := s.Encode(uiSessionName, map[interface{}]interface{}{cookieIDkey: int(atomic.LoadInt64(&sessID))})
		req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
		req.AddCookie(&http.Cookie{
			Name:  uiSessionName,
			Value: enc,
			Path:  "/",
		})
		vals := url.Values{}
		vals.Add("code", "something")
		vals.Add("state", "asdf")
		req.URL.RawQuery = vals.Encode()
		return req
	}
	defaultvalidateHandler := func(w http.ResponseWriter, r *http.Request) {
		b, _ := ioutil.ReadAll(r.Body)
		r.Body.Close()
		reqform, err := url.ParseQuery(string(b))
		if err != nil {
			t.Logf("could not parse req form: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, k := range []string{"client_id", "client_secret", "code", "state"} {
			if reqform.Get(k) == "" {
				t.Logf("missing req form key: %v", k)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		vals := url.Values{}
		vals.Add("access_token", "1234")
		vals.Add("token_type", "bearer")
		w.Write([]byte(vals.Encode()))
	}
	expectSuccess := func(rc *httptest.ResponseRecorder, dl persistence.UISessionsDataLayer) error {
		if rc.Result().StatusCode != 302 {
			return fmt.Errorf("unexpected status: %v (wanted 302)", rc.Result().StatusCode)
		}
		if loc := rc.Result().Header.Get("Location"); loc != "https://something.com/some/other/path" {
			return fmt.Errorf("unexpected redirect location: %v", loc)
		}
		s, err := dl.GetUISession(int(atomic.LoadInt64(&sessID)))
		if err != nil || s == nil {
			return fmt.Errorf("error getting session from db or missing: %v: %v", err, s)
		}
		if !s.Authenticated {
			return fmt.Errorf("expected authenticated session")
		}
		if s.GitHubUser != "johndoe" {
			return fmt.Errorf("unexpected github user: %v", s.GitHubUser)
		}
		return nil
	}
	expectUnauthorized := func(rc *httptest.ResponseRecorder, dl persistence.UISessionsDataLayer) error {
		if rc.Result().StatusCode != 302 {
			return fmt.Errorf("unexpected status: %v (wanted 302)", rc.Result().StatusCode)
		}
		if loc := rc.Result().Header.Get("Location"); loc != "https://something.com/ui"+baseErrorRoute {
			return fmt.Errorf("unexpected redirect location: %v", loc)
		}
		s, err := dl.GetUISession(int(atomic.LoadInt64(&sessID)))
		if err != nil || s == nil {
			return fmt.Errorf("error getting session from db or missing: %v: %v", err, s)
		}
		if s.Authenticated {
			return fmt.Errorf("expected unauthenticated session")
		}
		return nil
	}
	tests := []struct {
		name                        string
		fields                      fields
		wantErr                     bool
		dbSetupFunc                 func(dl persistence.UISessionsDataLayer)
		requestFunc                 func() *http.Request
		validateHandler             http.HandlerFunc
		getUserAppInstallationsFunc func(ctx context.Context) (ghclient.AppInstallations, error)
		getUserAppReposFunc         func(ctx context.Context, appID int64) ([]string, error)
		getUserFunc                 func(ctx context.Context) (string, error)
		verifyfunc                  func(*httptest.ResponseRecorder, persistence.UISessionsDataLayer) error
	}{
		{
			name: "authorized",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc:     defaultdbSetupFunc,
			requestFunc:     defaultrequestFunc,
			validateHandler: defaultvalidateHandler,
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 1}}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: expectSuccess,
		},
		{
			name: "unauthorized-badstate",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: defaultdbSetupFunc,
			requestFunc: func() *http.Request {
				s := securecookie.New(authKey[:], encKey[:])
				enc, _ := s.Encode(uiSessionName, map[interface{}]interface{}{cookieIDkey: int(atomic.LoadInt64(&sessID))})
				req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
				req.AddCookie(&http.Cookie{
					Name:  uiSessionName,
					Value: enc,
					Path:  "/",
				})
				vals := url.Values{}
				vals.Add("code", "something")
				vals.Add("state", "zzzzzzzz") // unexpected/bad state simulates replay attack or spoofing
				req.URL.RawQuery = vals.Encode()
				return req
			},
			validateHandler: defaultvalidateHandler,
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 1}}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: expectUnauthorized,
		},
		{
			name: "unauthorized-missingcode",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: defaultdbSetupFunc,
			requestFunc: func() *http.Request {
				s := securecookie.New(authKey[:], encKey[:])
				enc, _ := s.Encode(uiSessionName, map[interface{}]interface{}{cookieIDkey: int(atomic.LoadInt64(&sessID))})
				req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
				req.AddCookie(&http.Cookie{
					Name:  uiSessionName,
					Value: enc,
					Path:  "/",
				})
				vals := url.Values{}
				vals.Add("invalid", "random thing") // missing code simulates a bad/invalid callback request
				vals.Add("state", "asdf")
				req.URL.RawQuery = vals.Encode()
				return req
			},
			validateHandler: defaultvalidateHandler,
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 1}}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: expectUnauthorized,
		},
		{
			name: "unauthorized-missingcookie",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: defaultdbSetupFunc,
			requestFunc: func() *http.Request {
				req, _ := http.NewRequest("GET", "https://example.com/some/path", nil)
				vals := url.Values{}
				vals.Add("code", "something")
				vals.Add("state", "asdf")
				req.URL.RawQuery = vals.Encode()
				return req
			},
			validateHandler: defaultvalidateHandler,
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 1}}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: expectUnauthorized,
		},
		{
			name: "unauthorized-missingsession",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc:     func(dl persistence.UISessionsDataLayer) {},
			requestFunc:     defaultrequestFunc,
			validateHandler: defaultvalidateHandler,
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 1}}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: func(rc *httptest.ResponseRecorder, dl persistence.UISessionsDataLayer) error {
				if rc.Result().StatusCode != 302 {
					return fmt.Errorf("unexpected status: %v (wanted 302)", rc.Result().StatusCode)
				}
				if loc := rc.Result().Header.Get("Location"); loc != "https://something.com/ui"+baseErrorRoute {
					return fmt.Errorf("unexpected redirect location: %v", loc)
				}
				return nil
			},
		},
		{
			name: "unauthorized-no-installations",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc:     defaultdbSetupFunc,
			requestFunc:     defaultrequestFunc,
			validateHandler: defaultvalidateHandler,
			// no app installations
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: expectUnauthorized,
		},
		{
			name: "unauthorized-different-installations",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc:     defaultdbSetupFunc,
			requestFunc:     defaultrequestFunc,
			validateHandler: defaultvalidateHandler,
			// different app installation ids
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 2}, ghclient.AppInstallation{ID: 3}}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: expectUnauthorized,
		},
		{
			name: "unauthorized-no-repos",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc:     defaultdbSetupFunc,
			requestFunc:     defaultrequestFunc,
			validateHandler: defaultvalidateHandler,
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 1}}, nil
			},
			// no repos
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: expectUnauthorized,
		},
		{
			name: "unauthorized-validatefailure",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: defaultdbSetupFunc,
			requestFunc: defaultrequestFunc,
			validateHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 1}}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: expectUnauthorized,
		},
		{
			name: "unauthorized-validatejunkresponse",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc: defaultdbSetupFunc,
			requestFunc: defaultrequestFunc,
			validateHandler: func(w http.ResponseWriter, r *http.Request) {
				// return some random unexpected junk
				w.Header().Add("Content-Type", "application/json")
				w.Write([]byte(`{"foo":"bar","asdf": 123, "qwerty": null}`))
			},
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 1}}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				return "johndoe", nil
			},
			verifyfunc: expectUnauthorized,
		},
		{
			name: "unauthorized-baduser",
			fields: fields{
				cookiekeys: [2][32]byte{authKey, encKey},
			},
			dbSetupFunc:     defaultdbSetupFunc,
			requestFunc:     defaultrequestFunc,
			validateHandler: defaultvalidateHandler,
			getUserAppInstallationsFunc: func(ctx context.Context) (ghclient.AppInstallations, error) {
				return ghclient.AppInstallations{ghclient.AppInstallation{ID: 1}}, nil
			},
			getUserAppReposFunc: func(ctx context.Context, appID int64) ([]string, error) {
				return []string{"foo/bar"}, nil
			},
			getUserFunc: func(ctx context.Context) (string, error) {
				// GitHub returns an unknown user due to a bad token or spoofed callback request
				return "", fmt.Errorf("bad response: 404: Not Found")
			},
			verifyfunc: expectUnauthorized,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dl := persistence.NewFakeDataLayer()
			tt.dbSetupFunc(dl)
			oauthcfg := OAuthConfig{
				Enforce:           true,
				AppInstallationID: 1,
				ClientID:          "asdf",
				ClientSecret:      "asdf",
				AppGHClientFactoryFunc: func(_ string) ghclient.GitHubAppInstallationClient {
					return &ghclient.FakeRepoClient{
						GetUserAppInstallationsFunc: tt.getUserAppInstallationsFunc,
						GetUserAppReposFunc:         tt.getUserAppReposFunc,
						GetUserFunc:                 tt.getUserFunc,
					}
				},
				CookieAuthKey: tt.fields.cookiekeys[0],
				CookieEncKey:  tt.fields.cookiekeys[1],
			}
			oauthcfg.SetAuthURL("https://github.com/some/path")
			vs := httptest.NewServer(tt.validateHandler)
			defer vs.Close()
			oauthcfg.SetValidateURL(vs.URL)
			uapi, err := newUIAPI(
				"https://something.com",
				"../../ui",
				"/ui",
				false,
				config.DefaultUIBranding,
				dl,
				oauthcfg,
				log.New(os.Stderr, "", log.LstdFlags))
			if err != nil {
				t.Fatalf("error creating ui API: %v", err)
			}
			rc := httptest.NewRecorder()
			uapi.authCallbackHandler(rc, tt.requestFunc())
			if err := tt.verifyfunc(rc, dl); err != nil {
				if !tt.wantErr {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
