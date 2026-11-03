package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// Page operations used by the agent tools and the Browser pane. They act on the active tab.

// ErrTakenOver is returned by the agent's actions while the user drives the browser.
var ErrTakenOver = errors.New("the user has taken over the browser; wait until they hand it back (ask them in your reply) before acting in it")

// NormalizeURL makes a typed address navigable: http(s), file and about:blank pass; "localhost:3000/x" and
// IP addresses get http://, other bare hosts https://; absolute paths become file URLs. Other schemes
// (javascript:, data:, chrome:, ...) are refused.
func NormalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("url is empty")
	}
	if s == "about:blank" {
		return s, nil
	}
	if filepath.IsAbs(s) && !strings.Contains(s, "://") {
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(s)}).String(), nil
	}
	if i := strings.Index(s, "://"); i > 0 {
		u, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("invalid url %q: %v", raw, err)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			if u.Host == "" {
				return "", fmt.Errorf("url %q has no host", raw)
			}
			return u.String(), nil
		case "file":
			return u.String(), nil
		}
		return "", fmt.Errorf("url scheme %q is not allowed (use http, https or file)", u.Scheme)
	}
	if m := schemeLike.FindStringSubmatch(s); m != nil && !hostPortLike.MatchString(s) {
		return "", fmt.Errorf("url scheme %q is not allowed (use http, https or file)", m[1])
	}
	host := s
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	scheme := "https://"
	if IsLocalHost(host) || net.ParseIP(strings.Trim(host, "[]")) != nil {
		scheme = "http://"
	}
	u, err := url.Parse(scheme + s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid url %q", raw)
	}
	return u.String(), nil
}

var (
	schemeLike   = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*):`)
	hostPortLike = regexp.MustCompile(`^[A-Za-z0-9.-]+:\d+(/|$|\?|#)`)
)

// IsLocalHost reports whether host is this machine: localhost, *.localhost, 127.0.0.0/8, ::1, 0.0.0.0.
func IsLocalHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// activeTab starts the browser if needed and returns its active tab once it is set up.
func (b *Browser) activeTab(ctx context.Context) (*tab, *conn, error) {
	if err := b.Ensure(ctx); err != nil {
		return nil, nil, err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var t *tab
	var c *conn
	err := b.waitFor(wctx, func() bool {
		if !b.running {
			return true
		}
		t, c = b.tabs[b.active], b.conn
		if t == nil && len(b.order) > 0 {
			b.active = b.order[len(b.order)-1]
			t = b.tabs[b.active]
		}
		return t != nil && t.ready
	})
	if err != nil {
		return nil, nil, errors.New("the browser has no open tab")
	}
	if t == nil || c == nil {
		return nil, nil, errors.New("the browser stopped")
	}
	return t, c, nil
}

// world returns the tab's isolated world (created once per document).
func (b *Browser) world(ctx context.Context, c *conn, t *tab) (int64, error) {
	b.mu.Lock()
	id := t.world
	b.mu.Unlock()
	if id != 0 {
		return id, nil
	}
	var res struct {
		ExecutionContextID int64 `json:"executionContextId"`
	}
	if err := c.Call(ctx, t.session, "Page.createIsolatedWorld", map[string]any{"frameId": t.target, "worldName": "harness"}, &res); err != nil {
		return 0, err
	}
	b.mu.Lock()
	t.world = res.ExecutionContextID
	b.mu.Unlock()
	return res.ExecutionContextID, nil
}

type evalResult struct {
	Result           remoteObject `json:"result"`
	ExceptionDetails *struct {
		Text      string        `json:"text"`
		Exception *remoteObject `json:"exception"`
	} `json:"exceptionDetails"`
}

func (r evalResult) err() error {
	if r.ExceptionDetails == nil {
		return nil
	}
	msg := r.ExceptionDetails.Text
	if e := r.ExceptionDetails.Exception; e != nil && e.Description != "" {
		msg = e.Description
	}
	return errors.New(msg)
}

func contextGone(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "context") || strings.Contains(err.Error(), "Cannot find"))
}

// callIsolated calls a function declaration in the tab's isolated world with JSON arguments and decodes its
// (JSON-serializable) result into out.
func (b *Browser) callIsolated(ctx context.Context, c *conn, t *tab, fn string, args []any, out any) error {
	for attempt := 0; ; attempt++ {
		id, err := b.world(ctx, c, t)
		if err != nil {
			return err
		}
		jargs := make([]map[string]any, len(args))
		for i, a := range args {
			jargs[i] = map[string]any{"value": a}
		}
		var res evalResult
		err = c.Call(ctx, t.session, "Runtime.callFunctionOn", map[string]any{
			"functionDeclaration": fn, "executionContextId": id, "arguments": jargs, "returnByValue": true, "awaitPromise": true,
		}, &res)
		if err != nil && attempt == 0 && contextGone(err) {
			b.mu.Lock()
			if t.world == id {
				t.world = 0
			}
			b.mu.Unlock()
			continue
		}
		if err != nil {
			return err
		}
		if err := res.err(); err != nil {
			return err
		}
		if out != nil && len(res.Result.Value) > 0 {
			return json.Unmarshal(res.Result.Value, out)
		}
		return nil
	}
}

// evalIsolated evaluates an expression in the tab's isolated world.
func (b *Browser) evalIsolated(ctx context.Context, c *conn, t *tab, expr string, out any) error {
	return b.callIsolated(ctx, c, t, "function () { return ("+expr+"); }", nil, out)
}

// checkControl fails while the user has taken the browser over.
func (b *Browser) checkControl() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.takenOver {
		return ErrTakenOver
	}
	return nil
}

// report publishes an agent action to the viewers and the session.
func (b *Browser) report(a *agentv1.BrowserAction) {
	b.mu.Lock()
	b.broadcastActionLocked(a)
	b.mu.Unlock()
	b.m.reportAction(b.sessionID, a)
}

// ── navigation ──────────────────────────────────────────────────────────────

// NavResult is where a navigation ended.
type NavResult struct {
	TabID  string
	URL    string
	Title  string
	Loaded bool
}

func (b *Browser) result(t *tab, loaded bool) NavResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	return NavResult{TabID: t.id, URL: t.url, Title: t.title, Loaded: loaded}
}

// loadedResult reads the document's title (the target's title lags behind) and returns the result.
func (b *Browser) loadedResult(ctx context.Context, c *conn, t *tab, loaded bool) NavResult {
	var title string
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err := b.evalIsolated(tctx, c, t, "document.title", &title)
	cancel()
	if err == nil {
		b.mu.Lock()
		if t.title != title && title != "" {
			t.title = title
			b.notifyLocked(true)
		}
		b.mu.Unlock()
	}
	return b.result(t, loaded)
}

type seqs struct{ nav, load, stop int }

func (b *Browser) seqsOf(t *tab) seqs {
	b.mu.Lock()
	defer b.mu.Unlock()
	return seqs{t.navSeq, t.loadSeq, t.stopSeq}
}

// waitLoad waits for the tab to finish the navigation that started after s.
func (b *Browser) waitLoad(ctx context.Context, t *tab, s seqs, timeout time.Duration) bool {
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return b.waitFor(wctx, func() bool {
		return !b.running || b.tabs[t.id] == nil || t.loadSeq > s.load || t.stopSeq > s.stop || (t.navSeq > s.nav && !t.loading)
	}) == nil
}

// navigationTimeout bounds waiting for a page load.
const navigationTimeout = 30 * time.Second

// Navigate loads u in the active tab; with wait it returns once the page loaded (or the timeout passed).
func (b *Browser) Navigate(ctx context.Context, u string, wait bool) (NavResult, error) {
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return NavResult{}, err
	}
	s := b.seqsOf(t)
	var res struct {
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	if err := c.Call(ctx, t.session, "Page.navigate", map[string]any{"url": u}, &res); err != nil {
		return NavResult{}, err
	}
	if res.ErrorText != "" {
		return b.result(t, false), fmt.Errorf("navigation to %s failed: %s", u, res.ErrorText)
	}
	if !wait {
		return b.result(t, false), nil
	}
	if res.LoaderID == "" { // same-document navigation
		return b.loadedResult(ctx, c, t, true), nil
	}
	loaded := b.waitLoad(ctx, t, s, navigationTimeout)
	return b.loadedResult(ctx, c, t, loaded), nil
}

// History goes back (delta < 0) or forward in the active tab.
func (b *Browser) History(ctx context.Context, delta int, wait bool) (NavResult, error) {
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return NavResult{}, err
	}
	var h struct {
		CurrentIndex int            `json:"currentIndex"`
		Entries      []historyEntry `json:"entries"`
	}
	if err := c.Call(ctx, t.session, "Page.getNavigationHistory", nil, &h); err != nil {
		return NavResult{}, err
	}
	i := h.CurrentIndex + delta
	if i < 0 || i >= len(h.Entries) {
		if delta < 0 {
			return b.result(t, true), errors.New("there is no page to go back to")
		}
		return b.result(t, true), errors.New("there is no page to go forward to")
	}
	s := b.seqsOf(t)
	if err := c.Call(ctx, t.session, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}, nil); err != nil {
		return NavResult{}, err
	}
	if !wait {
		return b.result(t, false), nil
	}
	loaded := b.waitLoad(ctx, t, s, navigationTimeout)
	return b.loadedResult(ctx, c, t, loaded), nil
}

// Reload reloads the active tab.
func (b *Browser) Reload(ctx context.Context, wait bool) (NavResult, error) {
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return NavResult{}, err
	}
	s := b.seqsOf(t)
	if err := c.Call(ctx, t.session, "Page.reload", map[string]any{"ignoreCache": false}, nil); err != nil {
		return NavResult{}, err
	}
	if !wait {
		return b.result(t, false), nil
	}
	loaded := b.waitLoad(ctx, t, s, navigationTimeout)
	return b.loadedResult(ctx, c, t, loaded), nil
}

// CurrentURL is the active tab's URL ("" when the browser does not run).
func (b *Browser) CurrentURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t := b.tabs[b.active]; t != nil {
		return t.url
	}
	return ""
}

// TabURL is a tab's URL ("" when unknown).
func (b *Browser) TabURL(id string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t := b.tabs[id]; t != nil {
		return t.url
	}
	return ""
}

// HistoryURL is the URL back (delta -1) or forward would load, from the last known history ("" when unknown).
func (b *Browser) HistoryURL(delta int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.tabs[b.active]
	if t == nil {
		return ""
	}
	if i := t.historyIndex + delta; i >= 0 && i < len(t.history) {
		return t.history[i].URL
	}
	return ""
}

// ── tabs ────────────────────────────────────────────────────────────────────

// NewTab opens u in a new tab, makes it active and returns its id.
func (b *Browser) NewTab(ctx context.Context, u string) (string, error) {
	if err := b.Ensure(ctx); err != nil {
		return "", err
	}
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c == nil {
		return "", errors.New("the browser stopped")
	}
	var res struct {
		TargetID string `json:"targetId"`
	}
	if err := c.Call(ctx, "", "Target.createTarget", map[string]any{"url": u}, &res); err != nil {
		return "", err
	}
	var id string
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := b.waitFor(wctx, func() bool {
		if t := b.byTarget[res.TargetID]; t != nil && t.ready {
			id = t.id
			return true
		}
		return !b.running
	})
	if err != nil || id == "" {
		return "", errors.New("the new tab did not open")
	}
	return id, b.SwitchTab(ctx, id)
}

// CloseTab closes a tab ("" closes the active one).
func (b *Browser) CloseTab(ctx context.Context, id string) error {
	b.mu.Lock()
	if id == "" {
		id = b.active
	}
	t, c := b.tabs[id], b.conn
	b.mu.Unlock()
	if t == nil || c == nil {
		return fmt.Errorf("no tab %q", id)
	}
	return c.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": t.target}, nil)
}

// SwitchTab makes a tab the active one (the pane shows it and the agent acts in it).
func (b *Browser) SwitchTab(ctx context.Context, id string) error {
	b.mu.Lock()
	t, c := b.tabs[id], b.conn
	if t == nil || c == nil {
		b.mu.Unlock()
		return fmt.Errorf("no tab %q", id)
	}
	changed := b.active != id
	b.active = id
	if changed {
		b.notifyLocked(true)
	}
	b.mu.Unlock()
	_ = c.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": t.target}, nil)
	b.syncScreencast()
	return nil
}

// SetViewport changes the emulated screen of every tab.
func (b *Browser) SetViewport(ctx context.Context, vp Viewport) error {
	b.mu.Lock()
	b.viewport = vp
	c, ua := b.conn, b.userAgent
	var sessions []string
	for _, id := range b.order {
		sessions = append(sessions, b.tabs[id].session)
	}
	b.sc.needsStart = true
	b.notifyLocked(true)
	b.mu.Unlock()
	if c != nil {
		for _, s := range sessions {
			if err := applyViewport(ctx, c, s, vp, ua); err != nil {
				return err
			}
		}
	}
	b.syncScreencast()
	return nil
}

// ClearData deletes cookies, cache and site storage of the session's profile and reloads the open pages.
func (b *Browser) ClearData(ctx context.Context) error {
	b.mu.Lock()
	c := b.conn
	var session string
	if t := b.tabs[b.active]; t != nil {
		session = t.session
	}
	origins := make([]string, 0, len(b.visited))
	for o := range b.visited {
		origins = append(origins, o)
	}
	for _, id := range b.order {
		if o := origin(b.tabs[id].url); o != "" {
			origins = append(origins, o)
		}
	}
	var reload []string
	for _, id := range b.order {
		if origin(b.tabs[id].url) != "" {
			reload = append(reload, b.tabs[id].session)
		}
	}
	b.visited = map[string]bool{}
	running := b.running
	b.mu.Unlock()
	if !running || c == nil {
		return removeProfileData(b.m.profileDir(b.sessionID))
	}
	if err := c.Call(ctx, "", "Storage.clearCookies", map[string]any{}, nil); err != nil {
		return err
	}
	if session != "" {
		_ = c.Call(ctx, session, "Network.clearBrowserCache", nil, nil)
	}
	seen := map[string]bool{}
	for _, o := range origins {
		if seen[o] {
			continue
		}
		seen[o] = true
		if err := c.Call(ctx, session, "Storage.clearDataForOrigin", map[string]any{"origin": o, "storageTypes": "all"}, nil); err != nil {
			b.m.log().Debug("browser: clearing site data", "origin", o, "err", err)
		}
	}
	// Open pages still hold the old storage in memory: reload them.
	for _, s := range reload {
		_ = c.Call(ctx, s, "Page.reload", nil, nil)
	}
	return nil
}

// ── snapshot ────────────────────────────────────────────────────────────────

// SnapshotOptions narrow a snapshot.
type SnapshotOptions struct {
	Ref      string
	Selector string
	MaxChars int
}

// Snapshot is a rendered page outline.
type Snapshot struct {
	TabID     string
	URL       string
	Title     string
	Text      string
	Truncated bool
	Nodes     int
	ScrollY   int
	Height    int
	Viewport  int
}

// Snapshot builds the outline of the active tab.
func (b *Browser) Snapshot(ctx context.Context, o SnapshotOptions) (Snapshot, error) {
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	b.mu.Lock()
	start := t.refNext
	b.mu.Unlock()
	maxChars := o.MaxChars
	if maxChars <= 0 {
		maxChars = defaultSnapshotChars
	}
	maxChars = min(maxChars, maxSnapshotChars)
	var res snapResult
	opts := map[string]any{"start": start, "root": o.Ref, "selector": o.Selector, "maxNodes": maxSnapshotNodes}
	if err := b.callIsolated(ctx, c, t, snapshotScript, []any{opts}, &res); err != nil {
		return Snapshot{}, fmt.Errorf("reading the page: %w", err)
	}
	switch res.Error {
	case "not_found":
		return Snapshot{}, fmt.Errorf("%s is not on the page any more; take a snapshot without ref", o.Ref)
	case "no_match":
		return Snapshot{}, fmt.Errorf("no element matches the selector %q", o.Selector)
	}
	b.mu.Lock()
	if res.Next > t.refNext {
		t.refNext = res.Next
	}
	b.mu.Unlock()
	text, truncated := renderSnapshot(res.Nodes, maxChars)
	return Snapshot{TabID: t.id, URL: res.URL, Title: res.Title, Text: text, Truncated: truncated || res.Cut, Nodes: res.Count,
		ScrollY: res.Scroll.Y, Height: res.Scroll.Height, Viewport: res.Scroll.Viewport}, nil
}

// ── element actions ─────────────────────────────────────────────────────────

const pointScript = `function (ref, scroll) {
  const H = window.__harness; const w = H && H.byRef.get(ref); const el = w && w.deref();
  if (!el || !el.isConnected) return { error: 'not_found' };
  if (scroll) el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
  const rects = el.getClientRects();
  let r = el.getBoundingClientRect();
  if (!rects.length || r.width === 0 || r.height === 0) return { error: 'not_visible' };
  if (rects.length > 1) r = rects[0];
  const x = r.left + r.width / 2, y = r.top + r.height / 2;
  const desc = (e) => {
    const t = (e.getAttribute('aria-label') || e.innerText || e.value || e.getAttribute('alt') || e.getAttribute('title') || '').replace(/\s+/g, ' ').trim();
    return t.length > 80 ? t.slice(0, 79) + '…' : t;
  };
  let covered = '';
  const top = document.elementFromPoint(x, y);
  if (top && top !== el && !el.contains(top) && !(top.control && top.control === el) && !(el.labels && Array.from(el.labels).includes(top))) {
    covered = '<' + top.tagName.toLowerCase() + '>' + (desc(top) ? ' "' + desc(top) + '"' : '');
  }
  return { x, y, rect: [r.left, r.top, r.width, r.height], name: desc(el), tag: el.tagName.toLowerCase(), covered,
    scrollX: scrollX, scrollY: scrollY };
}`

type elementPoint struct {
	Error   string    `json:"error"`
	X       float64   `json:"x"`
	Y       float64   `json:"y"`
	Rect    []float64 `json:"rect"`
	Name    string    `json:"name"`
	Tag     string    `json:"tag"`
	Covered string    `json:"covered"`
	ScrollX float64   `json:"scrollX"`
	ScrollY float64   `json:"scrollY"`
}

func refError(ref, code string) error {
	switch code {
	case "not_found":
		return fmt.Errorf("%s is not on the page (it changed or navigated); take a new BrowserSnapshot", ref)
	case "not_visible":
		return fmt.Errorf("%s has no visible box (hidden or zero-size); take a new BrowserSnapshot or scroll", ref)
	}
	return fmt.Errorf("%s: %s", ref, code)
}

func (b *Browser) point(ctx context.Context, c *conn, t *tab, ref string, scroll bool) (elementPoint, error) {
	var p elementPoint
	if err := b.callIsolated(ctx, c, t, pointScript, []any{ref, scroll}, &p); err != nil {
		return p, err
	}
	if p.Error != "" {
		return p, refError(ref, p.Error)
	}
	return p, nil
}

// ClickOptions describe a click: on an element (Ref) or at a point (X, Y in CSS px of the viewport).
type ClickOptions struct {
	Ref       string
	X, Y      *float64
	Button    string // left, right, middle
	Double    bool
	Modifiers int
	ToolUseID string
}

// ActionResult describes what an action changed.
type ActionResult struct {
	Target    string // "button "Save" (ref_7)"
	X, Y      float64
	Covered   string
	Navigated bool
	URL       string
	NewTab    string
	NewTabURL string
}

func (b *Browser) tabCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.order)
}

// settle waits briefly for what an action set off: a navigation (then its load) or a new tab.
func (b *Browser) settle(ctx context.Context, t *tab, s seqs, tabsBefore int, r *ActionResult) {
	wctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	_ = b.waitFor(wctx, func() bool { return t.navSeq > s.nav || t.loading || len(b.order) > tabsBefore })
	cancel()
	b.mu.Lock()
	navigated := t.navSeq > s.nav || t.loading
	var nt *tab
	if len(b.order) > tabsBefore {
		nt = b.tabs[b.order[len(b.order)-1]]
	}
	b.mu.Unlock()
	if navigated {
		b.waitLoad(ctx, t, s, 10*time.Second)
		r.Navigated = true
	}
	b.mu.Lock()
	r.URL = t.url
	if nt != nil {
		r.NewTab, r.NewTabURL = nt.id, nt.url
	}
	b.mu.Unlock()
}

func (b *Browser) mouseClick(ctx context.Context, c *conn, session string, x, y float64, button string, double bool, mods int) error {
	bit := mouseButtons[button]
	if err := c.Call(ctx, session, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y, "modifiers": mods}, nil); err != nil {
		return err
	}
	clicks := 1
	if double {
		clicks = 2
	}
	for n := 1; n <= clicks; n++ {
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			buttons := bit
			if typ == "mouseReleased" {
				buttons = 0
			}
			if err := c.Call(ctx, session, "Input.dispatchMouseEvent", map[string]any{"type": typ, "x": x, "y": y, "button": button,
				"buttons": buttons, "clickCount": n, "modifiers": mods}, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func actionBase(toolUseID, kind string, t *tab, url string) *agentv1.BrowserAction {
	return &agentv1.BrowserAction{ToolUseId: toolUseID, Phase: agentv1.BrowserAction_PHASE_STARTED, Kind: kind, TabId: t.id, Url: url}
}

func (b *Browser) finish(a *agentv1.BrowserAction, err error) {
	f := &agentv1.BrowserAction{ToolUseId: a.ToolUseId, Phase: agentv1.BrowserAction_PHASE_FINISHED, Kind: a.Kind, TabId: a.TabId,
		Ref: a.Ref, X: a.X, Y: a.Y, Rect: a.Rect, Label: a.Label, Url: a.Url}
	if err != nil {
		f.Error = err.Error()
	}
	b.report(f)
}

func f64(v float64) *float64 { return &v }

// Click clicks an element or a point of the active tab.
func (b *Browser) Click(ctx context.Context, o ClickOptions) (ActionResult, error) {
	if err := b.checkControl(); err != nil {
		return ActionResult{}, err
	}
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	button := o.Button
	if button == "" {
		button = "left"
	}
	if _, ok := mouseButtons[button]; !ok {
		return ActionResult{}, fmt.Errorf("button must be left, right or middle")
	}
	var r ActionResult
	a := actionBase(o.ToolUseID, "click", t, b.TabURL(t.id))
	switch {
	case o.Ref != "":
		p, err := b.point(ctx, c, t, o.Ref, true)
		if err != nil {
			return r, err
		}
		r.X, r.Y, r.Covered = p.X, p.Y, p.Covered
		r.Target = p.Tag
		if p.Name != "" {
			r.Target += " " + quoted(p.Name)
		}
		r.Target += " (" + o.Ref + ")"
		a.Ref, a.Rect, a.Label = o.Ref, p.Rect, p.Name
	case o.X != nil && o.Y != nil:
		r.X, r.Y = *o.X, *o.Y
		r.Target = fmt.Sprintf("the point (%.0f, %.0f)", r.X, r.Y)
		a.Label = fmt.Sprintf("%.0f, %.0f", r.X, r.Y)
	default:
		return r, errors.New("give ref, or x and y")
	}
	a.X, a.Y = f64(r.X), f64(r.Y)
	if o.Double {
		a.Kind = "double_click"
	}
	b.report(a)
	s, before := b.seqsOf(t), b.tabCount()
	err = b.mouseClick(ctx, c, t.session, r.X, r.Y, button, o.Double, o.Modifiers)
	if err == nil {
		b.settle(ctx, t, s, before, &r)
	}
	b.finish(a, err)
	return r, err
}

const fieldScript = `function (ref) {
  const H = window.__harness; const w = H && H.byRef.get(ref); const el = w && w.deref();
  if (!el || !el.isConnected) return { error: 'not_found' };
  const tag = el.tagName;
  const type = (el.getAttribute('type') || '').toLowerCase();
  const noText = ['checkbox', 'radio', 'button', 'submit', 'reset', 'image', 'file', 'range', 'color', 'hidden'];
  const editable = el.isContentEditable || tag === 'TEXTAREA' || tag === 'SELECT' || (tag === 'INPUT' && !noText.includes(type));
  const label = ((el.labels && el.labels[0] && el.labels[0].innerText) || el.getAttribute('aria-label') || '').trim().slice(0, 120);
  return { tag: tag.toLowerCase(), editable, disabled: !!el.disabled, readonly: !!el.readOnly, select: tag === 'SELECT',
    options: tag === 'SELECT' ? Array.from(el.options).slice(0, 60).map((o) => o.text.trim()) : [],
    f: { type, ac: el.getAttribute('autocomplete') || '', name: el.getAttribute('name') || '', id: el.id || '',
      ph: el.getAttribute('placeholder') || '', label } };
}`

type fieldDesc struct {
	Error    string    `json:"error"`
	Tag      string    `json:"tag"`
	Editable bool      `json:"editable"`
	Disabled bool      `json:"disabled"`
	ReadOnly bool      `json:"readonly"`
	Select   bool      `json:"select"`
	Options  []string  `json:"options"`
	Field    fieldInfo `json:"f"`
}

// FieldSensitivity says why the agent must not type into the element ("" when it may, or when unknown).
func (b *Browser) FieldSensitivity(ctx context.Context, ref string) string {
	b.mu.Lock()
	running := b.running
	b.mu.Unlock()
	if !running {
		return ""
	}
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return ""
	}
	var d fieldDesc
	if ref == "" {
		if b.callIsolated(ctx, c, t, focusedFieldScript, nil, &d) != nil {
			return ""
		}
	} else if b.callIsolated(ctx, c, t, fieldScript, []any{ref}, &d) != nil || d.Error != "" {
		return ""
	}
	return sensitiveField(d.Field)
}

const focusedFieldScript = `function () {
  const el = document.activeElement;
  if (!el || el === document.body) return { error: 'none' };
  return { tag: el.tagName.toLowerCase(), f: { type: (el.getAttribute('type') || '').toLowerCase(), ac: el.getAttribute('autocomplete') || '',
    name: el.getAttribute('name') || '', id: el.id || '', ph: el.getAttribute('placeholder') || '',
    label: ((el.labels && el.labels[0] && el.labels[0].innerText) || el.getAttribute('aria-label') || '').trim().slice(0, 120) } };
}`

const focusScript = `function (ref, clear) {
  const H = window.__harness; const w = H && H.byRef.get(ref); const el = w && w.deref();
  if (!el || !el.isConnected) return 'not_found';
  el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
  el.focus();
  if (el.isContentEditable) {
    const r = document.createRange(); r.selectNodeContents(el);
    if (!clear) r.collapse(false);
    const s = getSelection(); s.removeAllRanges(); s.addRange(r);
  } else if (typeof el.select === 'function' && clear) {
    el.select();
  } else if (typeof el.setSelectionRange === 'function') {
    try { const n = (el.value || '').length; el.setSelectionRange(n, n); } catch (e) {}
  }
  return document.activeElement === el || el.contains(document.activeElement) ? '' : 'not_focusable';
}`

const selectScript = `function (ref, want) {
  const H = window.__harness; const w = H && H.byRef.get(ref); const el = w && w.deref();
  if (!el || !el.isConnected) return { error: 'not_found' };
  const norm = (s) => (s || '').replace(/\s+/g, ' ').trim().toLowerCase();
  const target = norm(want);
  const opts = Array.from(el.options);
  const opt = opts.find((o) => norm(o.text) === target) || opts.find((o) => norm(o.value) === target) || opts.find((o) => norm(o.text).includes(target));
  if (!opt) return { error: 'no_option', options: opts.slice(0, 60).map((o) => o.text.trim()) };
  el.focus();
  opt.selected = true;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
  return { chosen: opt.text.trim() };
}`

// TypeOptions describe typing into a field.
type TypeOptions struct {
	Ref    string
	Text   string
	Clear  bool
	Submit bool
	// AllowSensitive is set when the user approved typing into a sensitive field for this call.
	AllowSensitive bool
	ToolUseID      string
}

// ErrSensitive is returned when the agent tries to type into a password or payment field without approval.
type ErrSensitive struct{ Why string }

func (e ErrSensitive) Error() string {
	return "refused: the target is a " + e.Why + ". The agent never types into password, payment or identity fields unless the user approves that call; ask the user to fill it in themselves"
}

// Type types text into a field of the active tab (or chooses an option of a select).
func (b *Browser) Type(ctx context.Context, o TypeOptions) (ActionResult, error) {
	if err := b.checkControl(); err != nil {
		return ActionResult{}, err
	}
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	var d fieldDesc
	if o.Ref != "" {
		if err := b.callIsolated(ctx, c, t, fieldScript, []any{o.Ref}, &d); err != nil {
			return ActionResult{}, err
		}
		if d.Error != "" {
			return ActionResult{}, refError(o.Ref, d.Error)
		}
		if !d.Editable {
			return ActionResult{}, fmt.Errorf("%s is a <%s>, not a text field or select; use BrowserClick or BrowserKey", o.Ref, d.Tag)
		}
		if d.Disabled || d.ReadOnly {
			return ActionResult{}, fmt.Errorf("%s is disabled or read-only", o.Ref)
		}
	} else if err := b.callIsolated(ctx, c, t, focusedFieldScript, nil, &d); err != nil || d.Error != "" {
		return ActionResult{}, errors.New("no element has the focus; give ref")
	}
	if why := sensitiveField(d.Field); why != "" && !o.AllowSensitive {
		return ActionResult{}, ErrSensitive{Why: why}
	}
	r := ActionResult{Target: "the focused element"}
	a := actionBase(o.ToolUseID, "type", t, b.TabURL(t.id))
	if o.Ref != "" {
		r.Target = d.Tag
		if d.Field.Label != "" {
			r.Target += " " + quoted(d.Field.Label)
		}
		r.Target += " (" + o.Ref + ")"
		a.Ref, a.Label = o.Ref, d.Field.Label
		if p, err := b.point(ctx, c, t, o.Ref, true); err == nil {
			a.X, a.Y, a.Rect = f64(p.X), f64(p.Y), p.Rect
		}
	}
	b.report(a)
	err = b.typeInto(ctx, c, t, o, d, &r)
	b.finish(a, err)
	return r, err
}

func (b *Browser) typeInto(ctx context.Context, c *conn, t *tab, o TypeOptions, d fieldDesc, r *ActionResult) error {
	if d.Select {
		var res struct {
			Error   string   `json:"error"`
			Options []string `json:"options"`
			Chosen  string   `json:"chosen"`
		}
		if err := b.callIsolated(ctx, c, t, selectScript, []any{o.Ref, o.Text}, &res); err != nil {
			return err
		}
		if res.Error == "no_option" {
			return fmt.Errorf("%s has no option %q; options: %s", o.Ref, o.Text, strings.Join(quoteAll(res.Options), ", "))
		}
		if res.Error != "" {
			return refError(o.Ref, res.Error)
		}
		r.Target += " → option " + quoted(res.Chosen)
		return nil
	}
	if o.Ref != "" {
		var status string
		if err := b.callIsolated(ctx, c, t, focusScript, []any{o.Ref, o.Clear}, &status); err != nil {
			return err
		}
		if status == "not_found" {
			return refError(o.Ref, status)
		}
		if status == "not_focusable" {
			return fmt.Errorf("%s did not take the focus; click it first", o.Ref)
		}
	} else if o.Clear {
		if err := press(ctx, c, t.session, keyPress{def: keyTable["a"], mods: modCtrl}); err != nil {
			return err
		}
	}
	switch {
	case o.Text != "":
		if err := c.Call(ctx, t.session, "Input.insertText", map[string]any{"text": o.Text}, nil); err != nil {
			return err
		}
	case o.Clear:
		if err := press(ctx, c, t.session, keyPress{def: keyTable["backspace"]}); err != nil {
			return err
		}
	}
	if o.Submit {
		s, before := b.seqsOf(t), b.tabCount()
		if err := press(ctx, c, t.session, keyPress{def: keyTable["enter"]}); err != nil {
			return err
		}
		b.settle(ctx, t, s, before, r)
	}
	return nil
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = quoted(s)
	}
	return out
}

// Keys presses key combinations in the active tab, each repeat times.
func (b *Browser) Keys(ctx context.Context, keys string, repeat int, toolUseID string) (ActionResult, error) {
	if err := b.checkControl(); err != nil {
		return ActionResult{}, err
	}
	presses, err := parseKeys(keys)
	if err != nil {
		return ActionResult{}, err
	}
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	repeat = min(max(repeat, 1), 100)
	a := actionBase(toolUseID, "key", t, b.TabURL(t.id))
	a.Label = keys
	b.report(a)
	s, before := b.seqsOf(t), b.tabCount()
	r := ActionResult{Target: keys}
	for i := 0; i < repeat && err == nil; i++ {
		for _, p := range presses {
			if err = press(ctx, c, t.session, p); err != nil {
				break
			}
		}
	}
	if err == nil {
		b.settle(ctx, t, s, before, &r)
	}
	b.finish(a, err)
	return r, err
}

// ScrollOptions describe a scroll: Ref alone scrolls it into view; Direction scrolls the page (or the element
// under Ref / X,Y) by Amount CSS px (default: most of the viewport).
type ScrollOptions struct {
	Direction string
	Amount    int
	Ref       string
	X, Y      *float64
	ToolUseID string
}

// ScrollResult is where the page is after a scroll.
type ScrollResult struct {
	Target   string
	ScrollY  int
	ScrollX  int
	Height   int
	Viewport int
}

// Scroll scrolls the active tab.
func (b *Browser) Scroll(ctx context.Context, o ScrollOptions) (ScrollResult, error) {
	if err := b.checkControl(); err != nil {
		return ScrollResult{}, err
	}
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return ScrollResult{}, err
	}
	a := actionBase(o.ToolUseID, "scroll", t, b.TabURL(t.id))
	var r ScrollResult
	if o.Direction == "" {
		if o.Ref == "" {
			return r, errors.New("give direction, or ref to scroll an element into view")
		}
		p, err := b.point(ctx, c, t, o.Ref, false)
		if err != nil {
			return r, err
		}
		a.Ref, a.Label, a.Rect = o.Ref, p.Name, p.Rect
		b.report(a)
		_, err = b.point(ctx, c, t, o.Ref, true)
		r.Target = o.Ref + " into view"
		if err == nil {
			err = b.scrollPos(ctx, c, t, &r)
		}
		b.finish(a, err)
		return r, err
	}
	b.mu.Lock()
	vp := b.viewport
	b.mu.Unlock()
	x, y := float64(vp.Width)/2, float64(vp.Height)/2
	switch {
	case o.Ref != "":
		p, err := b.point(ctx, c, t, o.Ref, false)
		if err != nil {
			return r, err
		}
		x, y = p.X, p.Y
		a.Ref, a.Rect = o.Ref, p.Rect
	case o.X != nil && o.Y != nil:
		x, y = *o.X, *o.Y
	}
	amount := o.Amount
	if amount <= 0 {
		size := vp.Height
		if o.Direction == "left" || o.Direction == "right" {
			size = vp.Width
		}
		amount = size * 4 / 5
	}
	var dx, dy float64
	switch o.Direction {
	case "down":
		dy = float64(amount)
	case "up":
		dy = -float64(amount)
	case "right":
		dx = float64(amount)
	case "left":
		dx = -float64(amount)
	default:
		return r, errors.New("direction must be up, down, left or right")
	}
	a.X, a.Y = f64(x), f64(y)
	a.Label = fmt.Sprintf("%s %d px", o.Direction, amount)
	r.Target = a.Label
	b.report(a)
	err = c.Call(ctx, t.session, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": x, "y": y, "deltaX": dx, "deltaY": dy}, nil)
	if err == nil {
		select {
		case <-time.After(350 * time.Millisecond):
		case <-ctx.Done():
		}
		err = b.scrollPos(ctx, c, t, &r)
	}
	b.finish(a, err)
	return r, err
}

func (b *Browser) scrollPos(ctx context.Context, c *conn, t *tab, r *ScrollResult) error {
	var p struct {
		X, Y, H, V float64
	}
	err := b.evalIsolated(ctx, c, t, `(() => { const se = document.scrollingElement || document.documentElement; return { X: scrollX, Y: scrollY, H: se ? se.scrollHeight : 0, V: innerHeight }; })()`, &p)
	r.ScrollX, r.ScrollY, r.Height, r.Viewport = int(p.X), int(p.Y), int(p.H), int(p.V)
	return err
}

// ── screenshot, eval ────────────────────────────────────────────────────────

// Screenshot limits.
const (
	maxScreenshotEdge   = 1568
	maxFullPageHeight   = 12000
	screenshotJPEGLevel = 80
)

// ScreenshotOptions choose what to capture.
type ScreenshotOptions struct {
	FullPage bool
	Ref      string
}

// Image is a captured screenshot.
type Image struct {
	Data      []byte
	MediaType string
	// Width and Height are the captured area in CSS px; Scale is image px per CSS px.
	Width, Height float64
	Scale         float64
	Cut           bool
	URL           string
	TabID         string
}

// Screenshot captures the active tab's viewport, whole page or one element.
func (b *Browser) Screenshot(ctx context.Context, o ScreenshotOptions) (Image, error) {
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return Image{}, err
	}
	var lm struct {
		CSSVisualViewport struct {
			PageX        float64 `json:"pageX"`
			PageY        float64 `json:"pageY"`
			ClientWidth  float64 `json:"clientWidth"`
			ClientHeight float64 `json:"clientHeight"`
		} `json:"cssVisualViewport"`
		CSSContentSize struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"cssContentSize"`
	}
	if err := c.Call(ctx, t.session, "Page.getLayoutMetrics", nil, &lm); err != nil {
		return Image{}, err
	}
	vv := lm.CSSVisualViewport
	x, y, w, h := vv.PageX, vv.PageY, vv.ClientWidth, vv.ClientHeight
	img := Image{MediaType: "image/png", TabID: t.id, URL: b.TabURL(t.id)}
	params := map[string]any{"format": "png"}
	switch {
	case o.Ref != "":
		p, err := b.point(ctx, c, t, o.Ref, true)
		if err != nil {
			return Image{}, err
		}
		x, y, w, h = p.Rect[0]+p.ScrollX, p.Rect[1]+p.ScrollY, p.Rect[2], p.Rect[3]
	case o.FullPage:
		x, y, w, h = 0, 0, max(lm.CSSContentSize.Width, 1), max(lm.CSSContentSize.Height, 1)
		if h > maxFullPageHeight {
			h, img.Cut = maxFullPageHeight, true
		}
		params["format"], params["quality"], params["captureBeyondViewport"] = "jpeg", screenshotJPEGLevel, true
		img.MediaType = "image/jpeg"
	}
	b.mu.Lock()
	dpr := b.viewport.Scale
	b.mu.Unlock()
	if dpr <= 0 {
		dpr = 1
	}
	scale := 1.0
	if edge := max(w, h); edge > maxScreenshotEdge {
		scale = maxScreenshotEdge / edge
	}
	params["clip"] = map[string]any{"x": x, "y": y, "width": w, "height": h, "scale": scale / dpr}
	var res struct {
		Data []byte `json:"data"`
	}
	if err := c.Call(ctx, t.session, "Page.captureScreenshot", params, &res); err != nil {
		return Image{}, err
	}
	if len(res.Data) == 0 {
		return Image{}, errors.New("the screenshot is empty")
	}
	img.Data, img.Width, img.Height, img.Scale = res.Data, w, h, scale
	return img, nil
}

// EvalResult is the outcome of BrowserEval.
type EvalResult struct {
	Value     string // JSON, or "undefined"
	Exception string
	URL       string
}

// Eval runs JavaScript in the page's own context (with top-level await) and returns its JSON value.
func (b *Browser) Eval(ctx context.Context, expr string, timeout time.Duration, toolUseID string) (EvalResult, error) {
	if err := b.checkControl(); err != nil {
		return EvalResult{}, err
	}
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return EvalResult{}, err
	}
	a := actionBase(toolUseID, "eval", t, b.TabURL(t.id))
	a.Label = truncate(strings.TrimSpace(expr), 80)
	b.report(a)
	ectx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	var res evalResult
	err = c.Call(ectx, t.session, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true,
		"replMode": true, "userGesture": true, "timeout": timeout.Milliseconds()}, &res)
	b.finish(a, err)
	if err != nil {
		return EvalResult{}, err
	}
	out := EvalResult{URL: b.TabURL(t.id)}
	if e := res.err(); e != nil {
		out.Exception = e.Error()
		return out, nil
	}
	switch {
	case res.Result.Type == "undefined":
		out.Value = "undefined"
	case res.Result.UnserializableValue != "":
		out.Value = res.Result.UnserializableValue
	case len(res.Result.Value) > 0:
		out.Value = string(res.Result.Value)
	case res.Result.Description != "":
		out.Value = res.Result.Description
	default:
		out.Value = "null"
	}
	return out, nil
}

// ── console, network, wait ──────────────────────────────────────────────────

// Console returns the active tab's console entries (oldest first) and clears them when asked.
func (b *Browser) Console(ctx context.Context, clear bool) ([]ConsoleEntry, string, error) {
	t, _, err := b.activeTab(ctx)
	if err != nil {
		return nil, "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]ConsoleEntry(nil), t.console...)
	if clear {
		t.console = nil
		t.consoleErrors = 0
		b.notifyLocked(true)
	}
	return out, t.url, nil
}

// Network returns the active tab's requests (oldest first) and clears them when asked.
func (b *Browser) Network(ctx context.Context, clear bool) ([]NetworkEntry, string, error) {
	t, _, err := b.activeTab(ctx)
	if err != nil {
		return nil, "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]NetworkEntry, len(t.network))
	for i, e := range t.network {
		out[i] = *e
	}
	if clear {
		t.network = nil
		t.netByID = map[string]*NetworkEntry{}
	}
	return out, t.url, nil
}

// WaitOptions name one condition.
type WaitOptions struct {
	Text       string
	TextGone   string
	Selector   string
	URL        string
	Navigation bool
	Timeout    time.Duration
}

const waitScript = `function (o) {
  if (o.url) return location.href.includes(o.url);
  if (o.selector) {
    const el = document.querySelector(o.selector);
    if (!el) return false;
    const r = el.getClientRects();
    return r.length > 0 && getComputedStyle(el).visibility !== 'hidden';
  }
  const t = document.body ? document.body.innerText : '';
  if (o.text) return t.includes(o.text);
  if (o.textGone) return !t.includes(o.textGone);
  return true;
}`

// Wait waits for a condition in the active tab; false means it timed out.
func (b *Browser) Wait(ctx context.Context, o WaitOptions) (bool, error) {
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return false, err
	}
	wctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if o.Navigation {
		s := b.seqsOf(t)
		err := b.waitFor(wctx, func() bool {
			return !b.running || t.loadSeq > s.load || (t.navSeq > s.nav && !t.loading)
		})
		return err == nil, nil
	}
	if o.Selector != "" {
		// Validate the selector once so a typo fails fast.
		var ok bool
		if err := b.callIsolated(ctx, c, t, `function (s) { document.querySelector(s); return true; }`, []any{o.Selector}, &ok); err != nil {
			return false, fmt.Errorf("invalid selector %q: %v", o.Selector, err)
		}
	}
	arg := map[string]any{"text": o.Text, "textGone": o.TextGone, "selector": o.Selector, "url": o.URL}
	for {
		var ok bool
		err := b.callIsolated(wctx, c, t, waitScript, []any{arg}, &ok)
		if err == nil && ok {
			return true, nil
		}
		select {
		case <-wctx.Done():
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}
