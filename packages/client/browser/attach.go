package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Attaching to the user's real Chrome: its sign-ins, in tabs this client opens itself, over the DevTools protocol, where
// Chrome allows it. Chrome writes DevToolsActivePort into its profile when remote debugging is on (chrome://inspect, or
// --remote-debugging-port on a non-default profile) and asks the user before letting a client in. Nothing here starts Chrome
// or reads its profile beyond that one file.
//
// What keeps this narrow:
//   - only tabs opened here are ever attached, so the user's own tabs stay out of reach; a popup of such a tab joins it;
//   - the agent reaches a site only with an explicit grant, per host (subdomains are separate sites). The main frame's
//     document requests are paused at the protocol level, so a click, a redirect or script cannot leave for a site without
//     one; a grant is asked of the user on the session's stream and waits for their answer;
//   - the endpoint must be on loopback: a real profile is never driven over the network;
//   - nothing that changes the profile as a whole is offered (ClearData, viewport emulation), and closing ends with this
//     client's tabs closed and Chrome running.

// HostEnv names a DevTools endpoint to use instead of looking for DevToolsActivePort: 127.0.0.1:9222.
const realChromeEnv = "HARNESS_CHROME_CDP"

// ErrRealChromeNotFound says what the user has to do.
var ErrRealChromeNotFound = errors.New("no Chrome with remote debugging found: open chrome://inspect/#remote-debugging in Chrome, turn it on, and try again")

// ErrSiteDenied is the user's no.
var ErrSiteDenied = errors.New("the user did not allow this site in their Chrome")

// RealChromeDirs are the profile folders of the browsers that can be attached to, most likely first.
func RealChromeDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var roots []string
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		roots = []string{filepath.Join(base, "Google", "Chrome"), filepath.Join(base, "Google", "Chrome Beta"), filepath.Join(base, "Google", "Chrome Canary"), filepath.Join(base, "Chromium"), filepath.Join(base, "BraveSoftware", "Brave-Browser"), filepath.Join(base, "Microsoft Edge")}
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		roots = []string{filepath.Join(base, "Google", "Chrome", "User Data"), filepath.Join(base, "Google", "Chrome Beta", "User Data"), filepath.Join(base, "Chromium", "User Data"), filepath.Join(base, "BraveSoftware", "Brave-Browser", "User Data"), filepath.Join(base, "Microsoft", "Edge", "User Data")}
	default:
		base := filepath.Join(home, ".config")
		roots = []string{filepath.Join(base, "google-chrome"), filepath.Join(base, "google-chrome-beta"), filepath.Join(base, "chromium"), filepath.Join(base, "BraveSoftware", "Brave-Browser"), filepath.Join(base, "microsoft-edge")}
	}
	return roots
}

// loopbackPort reports whether a port answers on loopback.
func loopbackPort(ctx context.Context, port string) bool {
	d := net.Dialer{Timeout: time.Second}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// findEndpoint returns the WebSocket URL of a debuggable Chrome, from the env override or the profile folders.
func findEndpoint(ctx context.Context, dirs []string) (string, error) {
	if explicit := strings.TrimSpace(os.Getenv(realChromeEnv)); explicit != "" {
		return versionEndpoint(ctx, explicit)
	}
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(dir, "DevToolsActivePort"))
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) < 2 {
			continue
		}
		port, path := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
		if _, err := strconv.Atoi(port); err != nil || !strings.HasPrefix(path, "/devtools/browser/") {
			continue
		}
		// Chrome leaves the file behind when it quits: only a port that answers counts.
		if loopbackPort(ctx, port) {
			return "ws://127.0.0.1:" + port + path, nil
		}
	}
	return "", ErrRealChromeNotFound
}

// versionEndpoint asks a host:port on loopback for its browser endpoint.
func versionEndpoint(ctx context.Context, hostPort string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(hostPort, "http://"), "ws://"))
	if err != nil {
		return "", fmt.Errorf("%s: want 127.0.0.1:<port>: %w", realChromeEnv, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("%s must be on this machine, not %s", realChromeEnv, host)
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/json/version", nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", ErrRealChromeNotFound
	}
	defer res.Body.Close()
	var v struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if json.NewDecoder(res.Body).Decode(&v) != nil || v.URL == "" {
		return "", ErrRealChromeNotFound
	}
	u, err := url.Parse(v.URL)
	if err != nil {
		return "", ErrRealChromeNotFound
	}
	if ip := net.ParseIP(u.Hostname()); u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("the DevTools endpoint points off this machine: %s", u.Host)
	}
	return v.URL, nil
}

// SiteOf returns the site of a URL for grants: the lowercase host without "www.", "" for pages that are no site at all
// (about:blank, chrome-error:).
func SiteOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// SiteGrants are the sites the user allowed for good on this device: device-local, never synced. They live in
// HARNESS_HOME/browser/real-chrome-sites.json.
type SiteGrants struct {
	path string
	mu   sync.Mutex
	// By site: RFC 3339 of when it was granted.
	sites map[string]string
}

// NewSiteGrants loads the grants at path; a missing or broken file is no grants.
func NewSiteGrants(path string) *SiteGrants {
	g := &SiteGrants{path: path, sites: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &g.sites)
	}
	if g.sites == nil {
		g.sites = map[string]string{}
	}
	return g
}

// Has reports whether site is granted.
func (g *SiteGrants) Has(site string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.sites[site]
	return ok
}

// List returns the granted sites, sorted.
func (g *SiteGrants) List() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.sites))
	for s := range g.sites {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// Add grants site and saves.
func (g *SiteGrants) Add(site string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sites[site] = time.Now().UTC().Format(time.RFC3339)
	return g.saveLocked()
}

// Remove takes a grant back and saves.
func (g *SiteGrants) Remove(site string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.sites, site)
	return g.saveLocked()
}

func (g *SiteGrants) saveLocked() error {
	data, err := json.MarshalIndent(g.sites, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.path), 0o700); err != nil {
		return err
	}
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, g.path)
}

// SiteRequest asks the user to allow a site in their Chrome.
type SiteRequest struct {
	ID   string
	Site string
	URL  string
}

// SiteDecision is the answer to a SiteRequest.
type SiteDecision int

// Answers.
const (
	SiteDeny SiteDecision = iota
	// SiteAllowSession: for this session.
	SiteAllowSession
	// SiteAllowAlways: for good, on this device.
	SiteAllowAlways
)

// Grants returns the device's site grants.
func (m *Manager) Grants() *SiteGrants {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.grants == nil {
		m.grants = NewSiteGrants(filepath.Join(m.Dir, "real-chrome-sites.json"))
	}
	return m.grants
}

func (m *Manager) chromeDirs() []string {
	if m.ChromeDirs != nil {
		return m.ChromeDirs
	}
	return RealChromeDirs()
}

// RealChromeAvailable reports whether a Chrome can be attached to right now.
func (m *Manager) RealChromeAvailable(ctx context.Context) bool {
	_, err := findEndpoint(ctx, m.chromeDirs())
	return err == nil
}

// UseRealChrome moves the session's browser to the user's Chrome (on: attached from now on, restarting whatever ran) or back
// to its own profile. Its tabs do not carry over: nothing the agent did in one profile moves into the other.
func (b *Browser) UseRealChrome(ctx context.Context, on bool) error {
	if on {
		// Say no before stopping anything.
		if _, err := findEndpoint(ctx, b.m.chromeDirs()); err != nil {
			return err
		}
	}
	b.mu.Lock()
	if b.attached == on {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	b.Stop("switched")
	b.mu.Lock()
	b.attached = on
	b.restore, b.sessionSites = nil, map[string]bool{}
	b.notifyLocked(false)
	b.mu.Unlock()
	if on {
		return b.Ensure(ctx)
	}
	return nil
}

// Attached reports whether the session's browser is the user's Chrome.
func (b *Browser) Attached() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attached
}

// siteAllowed reports whether the agent may be on raw in the user's Chrome. Pages that are no site are always fine.
func (b *Browser) siteAllowed(raw string) bool {
	site := SiteOf(raw)
	if site == "" {
		return true
	}
	b.mu.Lock()
	session := b.sessionSites[site]
	b.mu.Unlock()
	return session || b.m.Grants().Has(site)
}

// askSite asks the user for a site and waits. Only one question per site runs at a time.
func (b *Browser) askSite(ctx context.Context, raw string) error {
	site := SiteOf(raw)
	b.mu.Lock()
	if b.pendingSites == nil {
		b.pendingSites = map[string]*pendingSite{}
	}
	p := b.pendingSites[site]
	if p == nil {
		b.nextSiteReq++
		p = &pendingSite{id: fmt.Sprintf("site_%d", b.nextSiteReq), site: site, url: raw, done: make(chan struct{})}
		b.pendingSites[site] = p
		ask := b.m.OnSiteRequest
		b.mu.Unlock()
		if ask == nil {
			b.resolveSite(p.id, SiteDeny)
		} else {
			ask(b.sessionID, SiteRequest{ID: p.id, Site: site, URL: raw})
		}
	} else {
		b.mu.Unlock()
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.decision == SiteDeny {
		return fmt.Errorf("%w: %s", ErrSiteDenied, site)
	}
	return nil
}

// AnswerSite is the user's answer to a site request; false when nothing waits for it.
func (b *Browser) AnswerSite(id string, d SiteDecision) bool {
	return b.resolveSite(id, d)
}

func (b *Browser) resolveSite(id string, d SiteDecision) bool {
	b.mu.Lock()
	var p *pendingSite
	for _, x := range b.pendingSites {
		if x.id == id {
			p = x
			break
		}
	}
	if p == nil {
		b.mu.Unlock()
		return false
	}
	delete(b.pendingSites, p.site)
	if d == SiteAllowSession || d == SiteAllowAlways {
		b.sessionSites[p.site] = true
	}
	b.notifyLocked(true)
	b.mu.Unlock()
	if d == SiteAllowAlways {
		if err := b.m.Grants().Add(p.site); err != nil {
			b.m.log().Warn("browser: saving a site grant", "err", err)
		}
	}
	p.decision = d
	close(p.done)
	return true
}

type pendingSite struct {
	id, site, url string
	done          chan struct{}
	decision      SiteDecision
}

// grantForUser records a site the user went to themselves in the pane: their own act is the grant, for this session.
func (b *Browser) grantForUser(raw string) {
	site := SiteOf(raw)
	if site == "" {
		return
	}
	b.mu.Lock()
	if b.attached {
		b.sessionSites[site] = true
	}
	b.mu.Unlock()
}

// attach connects to the user's Chrome and opens this client's own first tab there.
func (b *Browser) attach(ctx context.Context) error {
	endpoint, err := findEndpoint(ctx, b.m.chromeDirs())
	if err != nil {
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	wt, err := dialWS(lctx, endpoint)
	if err != nil {
		return fmt.Errorf("could not reach Chrome (it may be waiting for you to allow the connection): %w", err)
	}
	var c *conn
	c = newConn(wt, func(ev event) { b.onEvent(c, ev) })
	b.mu.Lock()
	b.cmd, b.exited, b.conn, b.stderr = nil, nil, c, nil
	b.tabs, b.order, b.bySession, b.byTarget = map[string]*tab{}, nil, map[string]*tab{}, map[string]*tab{}
	b.active, b.running, b.lastFrame = "", true, nil
	b.sc.tab, b.sc.session = "", ""
	b.restore = nil
	b.mu.Unlock()
	go b.watch(c, nil)

	fail := func(err error) error {
		b.stopWith(c, "attach_failed", false)
		return err
	}
	var ver struct {
		UserAgent string `json:"userAgent"`
	}
	if err := c.Call(lctx, "", "Browser.getVersion", nil, &ver); err != nil {
		return fail(fmt.Errorf("Chrome did not answer (allow the connection in Chrome, then try again): %w", err))
	}
	b.mu.Lock()
	b.userAgent = ver.UserAgent
	b.mu.Unlock()
	// Discovery only tells about targets: nothing attaches until openAttachedTab asks, so the user's tabs stay theirs.
	if err := c.Call(lctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		return fail(err)
	}
	if _, err := b.openAttachedTab(lctx, c, "about:blank", true); err != nil {
		return fail(err)
	}
	if err := b.waitFor(lctx, func() bool {
		for _, t := range b.tabs {
			if t.ready {
				return true
			}
		}
		return false
	}); err != nil {
		return fail(errors.New("the tab opened in Chrome did not become ready"))
	}
	go b.idleLoop(c)
	b.syncScreencast()
	return nil
}

// openAttachedTab opens a tab of this client's in the user's Chrome, in the background so it does not take the user's focus.
// The first one gets a window of its own, which keeps the agent's pages apart from the user's.
func (b *Browser) openAttachedTab(ctx context.Context, c *conn, u string, newWindow bool) (string, error) {
	var res struct {
		TargetID string `json:"targetId"`
	}
	if err := c.Call(ctx, "", "Target.createTarget", map[string]any{"url": u, "background": true, "newWindow": newWindow}, &res); err != nil {
		return "", err
	}
	if err := b.attachTarget(ctx, c, res.TargetID); err != nil {
		return "", err
	}
	return res.TargetID, nil
}

func (b *Browser) attachTarget(ctx context.Context, c *conn, target string) error {
	return c.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true}, nil)
}

// onAttachedTargetCreated follows a popup of a tab of this client's. Any other new target is the user's.
func (b *Browser) onAttachedTargetCreated(c *conn, info targetInfo) {
	if info.Type != "page" || info.OpenerID == "" || b.byTarget[info.OpenerID] == nil || b.byTarget[info.TargetID] != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := b.attachTarget(ctx, c, info.TargetID); err != nil {
			b.m.log().Debug("browser: following a popup", "err", err)
		}
	}()
}

// onDocumentPaused decides about a main frame's document request of an attached tab. A subframe goes on: the page around it was
// allowed, and blocking what it embeds would only break it.
func (b *Browser) onDocumentPaused(c *conn, t *tab, params json.RawMessage) {
	var p struct {
		RequestID string `json:"requestId"`
		FrameID   string `json:"frameId"`
		Request   struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if p.FrameID != t.target || b.siteAllowed(p.Request.URL) {
		if err := c.Call(ctx, t.session, "Fetch.continueRequest", map[string]any{"requestId": p.RequestID}, nil); err != nil {
			b.m.log().Debug("browser: continuing a request", "err", err)
		}
		return
	}
	if err := c.Call(ctx, t.session, "Fetch.failRequest", map[string]any{"requestId": p.RequestID, "errorReason": "BlockedByClient"}, nil); err != nil {
		b.m.log().Debug("browser: blocking a request", "err", err)
	}
	// Ask in the background; a yes takes the tab where it was going.
	go func() {
		if err := b.askSite(context.Background(), p.Request.URL); err != nil {
			return
		}
		nctx, ncancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ncancel()
		_ = c.Call(nctx, t.session, "Page.navigate", map[string]any{"url": p.Request.URL}, nil)
	}()
}

// closeAttachedTabs closes this client's tabs in the user's Chrome. The browser itself keeps running.
func (b *Browser) closeAttachedTabs(c *conn, targets []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, target := range targets {
		_ = c.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": target}, nil)
	}
}
