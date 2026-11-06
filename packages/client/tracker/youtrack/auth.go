// Adapted from internal/youtrack of github.com/xlsft/ymcp (MIT). See LICENSE.ymcp in this directory.

package youtrack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNotAuthenticated means Hub rejected the login or the session expired.
var ErrNotAuthenticated = errors.New("youtrack: not authenticated")

// HubScope is the OAuth scope of the Hub service itself, needed for the Hub REST API.
const HubScope = "0-0-0-0-0"

// hubAuth obtains short-lived access tokens by logging in to Hub with a
// login and password, the same way the YouTrack web UI does: an interactive
// login sets a Hub session cookie, then the implicit OAuth flow of the
// YouTrack service returns a bearer token in the redirect fragment. Expired
// tokens are refreshed silently from the session; the password is resent
// only when the session itself is gone. Tokens are cached per scope.
type hubAuth struct {
	baseURL  *url.URL
	login    string
	password string
	http     *http.Client // no redirects, own cookie jar
	now      func() time.Time
	log      *slog.Logger
	// bypassProxy, when set, switches the environment's proxy off; nil when
	// the proxy was configured explicitly.
	bypassProxy *atomic.Bool

	mu        sync.Mutex
	hubURL    *url.URL
	serviceID string
	tokens    map[string]cachedToken // by scope

	// static, when set, is a permanent token: no sign-in at all.
	static string
	// oauth, when set, gives tokens from Authorization Code with a refresh token.
	oauth *oauthState
}

// oauthState is a connection authorized through OAuth. Hub issues one token for the client's scope.
type oauthState struct {
	clientID     string
	clientSecret string
	access       string
	refresh      string
	expires      time.Time
	onTokens     func(access, refresh string, expires time.Time)
}

type cachedToken struct {
	value   string
	expires time.Time
}

func newHubAuth(baseURL *url.URL, login, password string, transport http.RoundTripper, timeout time.Duration) *hubAuth {
	jar, _ := cookiejar.New(nil)
	return &hubAuth{
		baseURL:  baseURL,
		login:    login,
		password: password,
		now:      time.Now,
		log:      slog.New(slog.DiscardHandler),
		tokens:   map[string]cachedToken{},
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			Jar:       jar,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Token returns a valid access token for scope, logging in when needed.
// An empty scope means the YouTrack service.
func (a *hubAuth) Token(ctx context.Context, scope string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.static != "" {
		return a.static, nil
	}
	if a.oauth != nil {
		return a.oauthToken(ctx)
	}
	if t, ok := a.tokens[scope]; ok && a.now().Before(t.expires) {
		return t.value, nil
	}
	start := time.Now()
	if err := a.discover(ctx); err != nil {
		a.log.Warn("can't reach YouTrack", "err", err)
		return "", err
	}
	// The Hub session usually outlives the token, so try without the password first.
	if err := a.authorize(ctx, scope); err == nil {
		a.log.Debug("token renewed from session", "scope", scopeName(scope), "took", time.Since(start).Round(time.Millisecond))
		return a.tokens[scope].value, nil
	} else if !errors.Is(err, ErrNotAuthenticated) {
		a.log.Warn("sign-in failed", "err", err)
		return "", err
	}
	if err := a.passwordLogin(ctx, scope); err != nil {
		a.log.Warn("sign-in failed", "login", a.login, "err", err)
		return "", err
	}
	a.log.Info("signed in", "login", a.login, "scope", scopeName(scope), "took", time.Since(start).Round(time.Millisecond))
	return a.tokens[scope].value, nil
}

// youtrackAndHub asks for a token valid for both YouTrack and Hub, which
// some YouTrack endpoints, e.g. deleting users, insist on.
const youtrackAndHub = "\x00youtrack+hub"

func scopeName(scope string) string {
	switch scope {
	case HubScope:
		return "hub"
	case youtrackAndHub:
		return "youtrack+hub"
	}
	return "youtrack"
}

// Invalidate drops a cached token so the next Token call re-authenticates.
func (a *hubAuth) Invalidate(scope, token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.oauth != nil && a.oauth.access == token {
		a.oauth.access = ""
	}
	if a.tokens[scope].value == token {
		delete(a.tokens, scope)
	}
}

// HubURL returns the Hub root, e.g. https://youtrack.example.com/hub.
func (a *hubAuth) HubURL(ctx context.Context) (*url.URL, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.discover(ctx); err != nil {
		return nil, err
	}
	u := *a.hubURL
	return &u, nil
}

// discover reads the Hub location and the YouTrack service ID from the
// public frontend config.
//
// An HTTP(S)_PROXY meant for the internet often can't reach a YouTrack on
// the internal network: it resolves the name publicly and gets a parking
// page or an error instead. When that happens with the environment's proxy,
// discover retries directly and, if that works, stays direct.
func (a *hubAuth) discover(ctx context.Context) error {
	if a.serviceID != "" {
		return nil
	}
	err := a.discoverOnce(ctx)
	if err == nil || a.bypassProxy == nil || a.bypassProxy.Load() {
		return err
	}
	proxy, _ := envProxy(&http.Request{URL: a.baseURL})
	if proxy == nil {
		return err
	}
	a.bypassProxy.Store(true)
	a.http.CloseIdleConnections()
	if directErr := a.discoverOnce(ctx); directErr != nil {
		a.bypassProxy.Store(false)
		return fmt.Errorf("%w; directly, without the proxy %s: %v", err, proxy.Host, directErr)
	}
	a.log.Info("the proxy from HTTP(S)_PROXY doesn't reach YouTrack; connecting directly", "proxy", proxy.Host)
	return nil
}

func (a *hubAuth) discoverOnce(ctx context.Context) error {
	u := a.baseURL.JoinPath("api", "config")
	u.RawQuery = "fields=ring(url,serviceId)"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("youtrack: discover hub: %w", err)
	}
	a.log.Debug("youtrack request", "method", "GET", "path", "/"+strings.TrimPrefix(u.Path, "/"), "status", resp.StatusCode, "took", time.Since(start).Round(time.Millisecond))
	defer resp.Body.Close()
	var cfg struct {
		Ring struct {
			URL       string `json:"url"`
			ServiceID string `json:"serviceId"`
		} `json:"ring"`
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return fmt.Errorf("youtrack: discover hub: unexpected %s %q response from %s (check URL, VPN and proxy settings)",
			resp.Status, resp.Header.Get("Content-Type"), u.Host)
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return fmt.Errorf("youtrack: discover hub: %w", err)
	}
	if cfg.Ring.ServiceID == "" || cfg.Ring.URL == "" {
		return errors.New("youtrack: discover hub: config has no hub URL or service ID")
	}
	hub, err := a.baseURL.Parse(cfg.Ring.URL)
	if err != nil {
		return fmt.Errorf("youtrack: discover hub: %w", err)
	}
	a.hubURL = hub
	a.serviceID = cfg.Ring.ServiceID
	return nil
}

func (a *hubAuth) redirectURI() string {
	return a.baseURL.JoinPath("oauth").String()
}

func (a *hubAuth) oauthQuery(scope, credentials, state string) url.Values {
	switch scope {
	case "":
		scope = a.serviceID
	case youtrackAndHub:
		scope = a.serviceID + " " + HubScope
	}
	return url.Values{
		"response_type":       {"token"},
		"client_id":           {a.serviceID},
		"scope":               {scope},
		"redirect_uri":        {a.redirectURI()},
		"state":               {state},
		"request_credentials": {credentials},
	}
}

// authorize runs the implicit flow against the current Hub session.
func (a *hubAuth) authorize(ctx context.Context, scope string) error {
	state := newState()
	u := a.hubURL.JoinPath("api", "rest", "oauth2", "auth")
	u.RawQuery = a.oauthQuery(scope, "silent", state).Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	return a.follow(ctx, req, scope, state)
}

// passwordLogin submits the Hub login form and then completes the flow.
func (a *hubAuth) passwordLogin(ctx context.Context, scope string) error {
	state := newState()
	u := a.hubURL.JoinPath("api", "rest", "oauth2", "interactive", "login")
	u.RawQuery = a.oauthQuery(scope, "default", state).Encode()
	form := url.Values{"username": {a.login}, "password": {a.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return a.follow(ctx, req, scope, state)
}

// follow walks the redirect chain until Hub sends the browser back to the
// YouTrack redirect URI, then takes the token from the URL fragment.
func (a *hubAuth) follow(ctx context.Context, req *http.Request, scope, state string) error {
	redirectURI := a.redirectURI()
	for hop := 0; hop < 10; hop++ {
		start := time.Now()
		resp, err := a.http.Do(req)
		if err != nil {
			return fmt.Errorf("youtrack: hub login: %w", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		a.log.Debug("hub request", "method", req.Method, "path", req.URL.Path, "status", resp.StatusCode, "took", time.Since(start).Round(time.Millisecond))

		if resp.StatusCode/100 != 3 {
			return fmt.Errorf("youtrack: hub login: unexpected %s at %s", resp.Status, req.URL.Path)
		}
		next, err := req.URL.Parse(resp.Header.Get("Location"))
		if err != nil {
			return fmt.Errorf("youtrack: hub login: bad redirect: %w", err)
		}

		if strings.HasPrefix(next.String(), redirectURI) {
			return a.acceptFragment(next.Fragment, scope, state)
		}
		if strings.Contains(next.Path, "/login/totp") {
			return errors.New("youtrack: hub login: two-factor authentication is not supported")
		}
		if strings.HasSuffix(next.Path, "/auth/login") {
			// Hub sent us back to the login page: wrong credentials or no session.
			q := next.Query()
			msg := q.Get("developer_message")
			if msg == "" {
				msg = q.Get("message")
			}
			if msg == "" {
				msg = "login required"
			}
			return fmt.Errorf("%w: %s", ErrNotAuthenticated, msg)
		}

		req, err = http.NewRequestWithContext(ctx, http.MethodGet, next.String(), nil)
		if err != nil {
			return err
		}
	}
	return errors.New("youtrack: hub login: too many redirects")
}

func (a *hubAuth) acceptFragment(fragment, scope, state string) error {
	q, err := url.ParseQuery(fragment)
	if err != nil {
		return fmt.Errorf("youtrack: hub login: bad redirect fragment: %w", err)
	}
	if e := q.Get("error"); e != "" {
		// access_denied / login_required come back when there is no live session.
		return fmt.Errorf("%w: %s: %s", ErrNotAuthenticated, e, q.Get("error_description"))
	}
	if q.Get("state") != state {
		return errors.New("youtrack: hub login: state mismatch")
	}
	token := q.Get("access_token")
	if token == "" {
		return errors.New("youtrack: hub login: no access token in redirect")
	}
	ttl := time.Hour
	if s, err := strconv.Atoi(q.Get("expires_in")); err == nil && s > 0 {
		ttl = time.Duration(s) * time.Second
	}
	// Refresh a little early so in-flight requests don't race the expiry.
	a.tokens[scope] = cachedToken{value: token, expires: a.now().Add(ttl - min(ttl/10, time.Minute))}
	return nil
}

func newState() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// oauthToken gives the access token, refreshing it when it ran out.
func (a *hubAuth) oauthToken(ctx context.Context) (string, error) {
	o := a.oauth
	if o.access != "" && (o.expires.IsZero() || a.now().Before(o.expires)) {
		return o.access, nil
	}
	if o.refresh == "" {
		return "", fmt.Errorf("%w: authorize this connection first", ErrNotAuthenticated)
	}
	if err := a.discover(ctx); err != nil {
		return "", err
	}
	access, refresh, expires, err := a.tokenEndpoint(ctx, o.clientID, o.clientSecret, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {o.refresh}})
	if err != nil {
		return "", err
	}
	o.access, o.expires = access, expires
	// Hub may issue a new refresh token, and then the old one is done.
	if refresh != "" {
		o.refresh = refresh
	}
	if o.onTokens != nil {
		o.onTokens(o.access, o.refresh, o.expires)
	}
	return o.access, nil
}

// tokenEndpoint is Hub's token endpoint, for a refresh and for finishing Authorization Code.
func (a *hubAuth) tokenEndpoint(ctx context.Context, clientID, clientSecret string, form url.Values) (access, refresh string, expires time.Time, err error) {
	if clientSecret == "" {
		form.Set("client_id", clientID)
	}
	u := a.hubURL.JoinPath("api", "rest", "oauth2", "token")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if clientSecret != "" {
		req.SetBasicAuth(clientID, clientSecret)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("youtrack: token endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode >= 500 {
			return "", "", time.Time{}, fmt.Errorf("youtrack: token endpoint: %s", resp.Status)
		}
		return "", "", time.Time{}, fmt.Errorf("%w: the token endpoint answered %s: authorize this connection again", ErrNotAuthenticated, resp.Status)
	}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", "", time.Time{}, fmt.Errorf("youtrack: token endpoint: %w", err)
	}
	if t.AccessToken == "" {
		return "", "", time.Time{}, fmt.Errorf("%w: the token endpoint gave no access token", ErrNotAuthenticated)
	}
	ttl := time.Hour
	if t.ExpiresIn > 0 {
		ttl = time.Duration(t.ExpiresIn) * time.Second
	}
	return t.AccessToken, t.RefreshToken, a.now().Add(ttl - min(ttl/10, time.Minute)), nil
}
