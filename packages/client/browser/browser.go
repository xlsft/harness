package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// Ring sizes.
const (
	maxConsoleEntries = 500
	maxNetworkEntries = 500
	launchTimeout     = 30 * time.Second
)

// Browser is the browser of one session. It starts lazily and can stop and start again; its profile persists.
type Browser struct {
	m         *Manager
	sessionID string
	scMu      sync.Mutex // orders screencast start/stop

	mu        sync.Mutex
	cmd       *exec.Cmd
	exited    chan struct{}
	conn      *conn
	running   bool
	starting  chan struct{}
	userAgent string
	tabs      map[string]*tab
	order     []string
	bySession map[string]*tab
	byTarget  map[string]*tab
	active    string
	nextTab   int
	viewport  Viewport
	viewers   map[*viewer]struct{}
	takenOver bool
	restore   []string
	visited   map[string]bool
	lastUse   time.Time
	changed   chan struct{}
	sites     func() []string
	closed    bool
	sc        screencast
	lastFrame []byte
	stderr    *tailBuffer

	// The user's real Chrome instead of a profile of this session's own (attach.go).
	attached     bool
	sessionSites map[string]bool
	pendingSites map[string]*pendingSite
	nextSiteReq  int
	// A recording running, and the last one saved (recording.go).
	rec           *recorder
	lastRecording string
}

type screencast struct {
	tab        string
	session    string
	quality    int
	seq        int
	drops      int
	window     time.Time
	lastDrop   time.Time
	lastRaise  time.Time
	needsStart bool
}

// tab is one page target.
type tab struct {
	id, target, session string
	url, title, favicon string
	loading             bool
	ready               bool
	crashed             bool
	consoleErrors       int
	canBack, canForward bool
	history             []historyEntry
	historyIndex        int
	console             []ConsoleEntry
	network             []*NetworkEntry
	netByID             map[string]*NetworkEntry
	world               int64
	refNext             int
	navSeq, loadSeq     int
	stopSeq             int
}

type historyEntry struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
}

// ConsoleEntry is a console message, an uncaught exception or a browser log entry of a tab.
type ConsoleEntry struct {
	Time   time.Time
	Level  string // error, warning, info, log, debug
	Text   string
	Source string // url:line, when known
}

// NetworkEntry is a request of a tab.
type NetworkEntry struct {
	ID         string
	Method     string
	URL        string
	Type       string
	Status     int
	StatusText string
	MimeType   string
	Failed     string
	Size       int64
	Started    time.Time
	startTS    float64
	Duration   time.Duration
	Finished   bool
}

func newBrowser(m *Manager, sessionID string) *Browser {
	return &Browser{m: m, sessionID: sessionID, viewport: DesktopViewport, viewers: map[*viewer]struct{}{},
		changed: make(chan struct{}), visited: map[string]bool{}, sc: screencast{quality: 70}, sessionSites: map[string]bool{}}
}

// SetSites sets the source of the sites the agent may use without asking (the permission engine's grants).
func (b *Browser) SetSites(f func() []string) {
	b.mu.Lock()
	b.sites = f
	b.mu.Unlock()
}

// SessionID returns the session the browser belongs to.
func (b *Browser) SessionID() string { return b.sessionID }

// Running reports whether the browser process runs.
func (b *Browser) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}

// notify wakes waiters; with state, viewers get a fresh state message. Caller holds b.mu.
func (b *Browser) notifyLocked(state bool) {
	close(b.changed)
	b.changed = make(chan struct{})
	if state {
		for v := range b.viewers {
			v.signalState()
		}
	}
}

// waitFor blocks until cond (evaluated with b.mu held) is true.
func (b *Browser) waitFor(ctx context.Context, cond func() bool) error {
	for {
		b.mu.Lock()
		if cond() {
			b.mu.Unlock()
			return nil
		}
		ch := b.changed
		b.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Ensure starts the browser if it does not run.
func (b *Browser) Ensure(ctx context.Context) error {
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return ErrClosed
		}
		b.lastUse = time.Now()
		if b.running {
			b.mu.Unlock()
			return nil
		}
		if ch := b.starting; ch != nil {
			b.mu.Unlock()
			select {
			case <-ch:
				b.mu.Lock()
				ok := b.running
				b.mu.Unlock()
				if !ok {
					return errors.New("the browser failed to start")
				}
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		ch := make(chan struct{})
		b.starting = ch
		b.mu.Unlock()
		err := b.launch(ctx)
		b.mu.Lock()
		b.starting = nil
		close(ch)
		b.mu.Unlock()
		return err
	}
}

// tailBuffer keeps the last bytes written (the browser's stderr, for error messages).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 8192 {
		t.buf = t.buf[len(t.buf)-8192:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func (b *Browser) launchArgs(profile string) []string {
	args := []string{
		"--headless=new",
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--disable-component-update",
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
		"--disable-features=Translate,MediaRouter,OptimizationHints,DialMediaRouteProvider",
		"--password-store=basic",
		"--use-mock-keychain",
		"--mute-audio",
		"--force-color-profile=srgb",
		"--window-size=" + strconv.Itoa(b.viewport.Width) + "," + strconv.Itoa(b.viewport.Height),
	}
	if usePipe {
		args = append(args, "--remote-debugging-pipe")
	} else {
		args = append(args, "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1")
	}
	if runningAsRoot() {
		args = append(args, "--no-sandbox")
	}
	return append(args, "about:blank")
}

func (b *Browser) launch(ctx context.Context) error {
	b.mu.Lock()
	attached := b.attached
	b.mu.Unlock()
	if attached {
		return b.attach(ctx)
	}
	exe, err := b.m.executable()
	if err != nil {
		return err
	}
	profile := b.m.profileDir(b.sessionID)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(profile, "DevToolsActivePort"))
	// A crashed earlier run leaves its singleton lock behind; this profile belongs to this session only.
	for _, f := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		_ = os.Remove(filepath.Join(profile, f))
	}
	cmd := exec.Command(exe.Path, b.launchArgs(profile)...)
	setProcessGroup(cmd)
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	var inW, outR *os.File
	if usePipe {
		inR, w, err := os.Pipe()
		if err != nil {
			return err
		}
		r, outW, err := os.Pipe()
		if err != nil {
			inR.Close()
			w.Close()
			return err
		}
		cmd.ExtraFiles = []*os.File{inR, outW}
		inW, outR = w, r
		defer inR.Close()
		defer outW.Close()
	}
	if err := cmd.Start(); err != nil {
		if inW != nil {
			inW.Close()
			outR.Close()
		}
		return fmt.Errorf("starting %s: %w", exe.Product, err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	fail := func(err error) error {
		killProcessGroup(cmd)
		if s := stderr.String(); s != "" {
			if len(s) > 600 {
				s = s[len(s)-600:]
			}
			return fmt.Errorf("%w (browser output: %s)", err, s)
		}
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	var t transport
	if usePipe {
		t = newPipeTransport(inW, outR)
	} else {
		wsURL, err := waitDevToolsPort(lctx, profile, exited)
		if err != nil {
			return fail(err)
		}
		wt, err := dialWS(lctx, wsURL)
		if err != nil {
			return fail(err)
		}
		t = wt
	}
	var c *conn
	c = newConn(t, func(ev event) { b.onEvent(c, ev) })
	b.mu.Lock()
	b.cmd, b.exited, b.conn, b.stderr = cmd, exited, c, stderr
	b.tabs, b.order, b.bySession, b.byTarget = map[string]*tab{}, nil, map[string]*tab{}, map[string]*tab{}
	b.active, b.running, b.lastFrame = "", true, nil
	b.sc.tab, b.sc.session = "", ""
	restore := b.restore
	b.restore = nil
	b.mu.Unlock()
	go b.watch(c, exited)

	var ver struct {
		UserAgent string `json:"userAgent"`
	}
	if err := c.Call(lctx, "", "Browser.getVersion", nil, &ver); err != nil {
		b.stopWith(c, "launch_failed", false)
		return fail(err)
	}
	b.mu.Lock()
	b.userAgent = ver.UserAgent
	b.mu.Unlock()
	if err := c.Call(lctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		b.stopWith(c, "launch_failed", false)
		return fail(err)
	}
	if err := c.Call(lctx, "", "Target.setAutoAttach", map[string]any{
		"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true,
		"filter": []map[string]any{{"type": "page", "exclude": false}, {"exclude": true}},
	}, nil); err != nil {
		b.stopWith(c, "launch_failed", false)
		return fail(err)
	}
	err = b.waitFor(lctx, func() bool {
		for _, t := range b.tabs {
			if t.ready {
				return true
			}
		}
		return false
	})
	if err != nil {
		// No initial page (unusual): open one.
		var res struct {
			TargetID string `json:"targetId"`
		}
		if cerr := c.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &res); cerr != nil {
			b.stopWith(c, "launch_failed", false)
			return fail(fmt.Errorf("the browser opened no page: %w", cerr))
		}
	}
	go b.idleLoop(c)
	if len(restore) > 0 {
		go b.restoreTabs(c, restore)
	}
	b.syncScreencast()
	return nil
}

// waitDevToolsPort reads the DevTools WebSocket URL Chrome writes into the profile (--remote-debugging-port=0).
func waitDevToolsPort(ctx context.Context, profile string, exited <-chan struct{}) (string, error) {
	p := filepath.Join(profile, "DevToolsActivePort")
	for {
		if b, err := os.ReadFile(p); err == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) >= 2 {
				return "ws://127.0.0.1:" + strings.TrimSpace(lines[0]) + strings.TrimSpace(lines[1]), nil
			}
		}
		select {
		case <-exited:
			return "", errors.New("the browser exited while starting")
		case <-ctx.Done():
			return "", errors.New("the browser did not open its DevTools port in time")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// restoreTabs opens the tabs of the previous run (after an idle stop).
func (b *Browser) restoreTabs(c *conn, urls []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.mu.Lock()
	var first *tab
	if len(b.order) > 0 {
		first = b.tabs[b.order[0]]
	}
	b.mu.Unlock()
	for i, u := range urls {
		if i == 0 && first != nil {
			_ = c.Call(ctx, first.session, "Page.navigate", map[string]any{"url": u}, nil)
			continue
		}
		_ = c.Call(ctx, "", "Target.createTarget", map[string]any{"url": u}, nil)
	}
}

func (b *Browser) watch(c *conn, exited <-chan struct{}) {
	select {
	case <-exited:
	case <-c.Done():
	}
	b.stopWith(c, "exited", false)
}

func (b *Browser) idleLoop(c *conn) {
	idle := b.m.idle()
	if idle < 0 {
		return
	}
	tick := time.NewTicker(min(idle/4, 30*time.Second) + time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-c.Done():
			return
		case <-tick.C:
		}
		b.mu.Lock()
		stale := b.conn == c && len(b.viewers) == 0 && time.Since(b.lastUse) > idle
		b.mu.Unlock()
		if stale {
			b.stopWith(c, "idle", true)
			return
		}
	}
}

// Stop stops the browser process (it starts again on the next use; the open tabs come back).
func (b *Browser) Stop(reason string) {
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c != nil {
		b.stopWith(c, reason, true)
	}
}

func (b *Browser) shutdown(reason string) {
	b.mu.Lock()
	b.closed = true
	c := b.conn
	b.mu.Unlock()
	if c != nil {
		b.stopWith(c, reason, true)
	}
}

// stopWith stops the run of connection c (a no-op when another run is current). graceful asks the browser to
// close first; the open tabs are remembered for the next start.
func (b *Browser) stopWith(c *conn, reason string, graceful bool) {
	b.mu.Lock()
	if b.conn != c {
		b.mu.Unlock()
		return
	}
	var restore, ownTargets []string
	for _, id := range b.order {
		if t := b.tabs[id]; t != nil {
			ownTargets = append(ownTargets, t.target)
			if restorable(t.url) {
				restore = append(restore, t.url)
			}
		}
	}
	attached := b.attached
	if !b.closed && !attached {
		b.restore = restore
	}
	cmd, exited := b.cmd, b.exited
	b.rec = nil
	b.conn, b.cmd, b.running = nil, nil, false
	b.tabs, b.order, b.bySession, b.byTarget, b.active = map[string]*tab{}, nil, map[string]*tab{}, map[string]*tab{}, ""
	b.sc.tab, b.sc.session, b.lastFrame = "", "", nil
	b.takenOver = false
	viewers := b.viewers
	b.viewers = map[*viewer]struct{}{}
	b.notifyLocked(false)
	b.mu.Unlock()
	for v := range viewers {
		v.close(reason)
	}
	if attached {
		// The user's Chrome stays up: only this client's tabs go, and only if the connection still is there.
		if graceful {
			b.closeAttachedTabs(c, ownTargets)
		}
		c.Close()
		return
	}
	if graceful {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.Call(ctx, "", "Browser.close", nil, nil)
		cancel()
	}
	if exited != nil {
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			killProcessGroup(cmd)
		}
	}
	c.Close()
}

func restorable(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "file://")
}

// ── events ──────────────────────────────────────────────────────────────────

func (b *Browser) onEvent(c *conn, ev event) {
	b.mu.Lock()
	if b.conn != c {
		b.mu.Unlock()
		return
	}
	defer b.mu.Unlock()
	if ev.Session == "" {
		b.onBrowserEvent(c, ev)
		return
	}
	t := b.bySession[ev.Session]
	if t == nil {
		return
	}
	b.onTabEvent(c, t, ev)
}

type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	OpenerID string `json:"openerId"`
}

// onBrowserEvent handles browser-level events. Caller holds b.mu.
func (b *Browser) onBrowserEvent(c *conn, ev event) {
	switch ev.Method {
	case "Target.attachedToTarget":
		var p struct {
			SessionID          string     `json:"sessionId"`
			TargetInfo         targetInfo `json:"targetInfo"`
			WaitingForDebugger bool       `json:"waitingForDebugger"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		if p.TargetInfo.Type != "page" {
			if p.WaitingForDebugger {
				go c.Call(context.Background(), p.SessionID, "Runtime.runIfWaitingForDebugger", nil, nil)
			}
			return
		}
		b.nextTab++
		t := &tab{id: "t" + strconv.Itoa(b.nextTab), target: p.TargetInfo.TargetID, session: p.SessionID,
			url: p.TargetInfo.URL, title: p.TargetInfo.Title, netByID: map[string]*NetworkEntry{}, refNext: 1}
		b.tabs[t.id] = t
		b.order = append(b.order, t.id)
		b.bySession[t.session] = t
		b.byTarget[t.target] = t
		if b.active == "" || p.TargetInfo.OpenerID != "" {
			b.active = t.id
		}
		b.notifyLocked(true)
		go b.setupTab(c, t)
	case "Target.targetCreated":
		if !b.attached {
			return
		}
		var p struct {
			TargetInfo targetInfo `json:"targetInfo"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			b.onAttachedTargetCreated(c, p.TargetInfo)
		}
	case "Target.detachedFromTarget":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			if t := b.bySession[p.SessionID]; t != nil {
				b.removeTabLocked(c, t)
			}
		}
	case "Target.targetDestroyed":
		var p struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			if t := b.byTarget[p.TargetID]; t != nil {
				b.removeTabLocked(c, t)
			}
		}
	case "Target.targetInfoChanged":
		var p struct {
			TargetInfo targetInfo `json:"targetInfo"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			if t := b.byTarget[p.TargetInfo.TargetID]; t != nil {
				if t.url != p.TargetInfo.URL || t.title != p.TargetInfo.Title {
					t.url, t.title = p.TargetInfo.URL, p.TargetInfo.Title
					b.notifyLocked(true)
				}
			}
		}
	}
}

func (b *Browser) removeTabLocked(c *conn, t *tab) {
	if b.tabs[t.id] == nil {
		return
	}
	delete(b.tabs, t.id)
	delete(b.bySession, t.session)
	delete(b.byTarget, t.target)
	for i, id := range b.order {
		if id == t.id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
	if b.active == t.id {
		b.active = ""
		if n := len(b.order); n > 0 {
			b.active = b.order[n-1]
		}
	}
	if b.sc.tab == t.id {
		b.sc.tab, b.sc.session = "", ""
	}
	b.notifyLocked(true)
	if len(b.order) == 0 && b.running && !b.closed {
		// A browser always has a tab: closing the last one opens a blank one.
		if b.attached {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, _ = b.openAttachedTab(ctx, c, "about:blank", true)
			}()
		} else {
			go c.Call(context.Background(), "", "Target.createTarget", map[string]any{"url": "about:blank"}, nil)
		}
	}
	go b.syncScreencast()
}

// setupTab enables the domains the browser uses, applies the viewport and lets the page run.
func (b *Browser) setupTab(c *conn, t *tab) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b.mu.Lock()
	vp, ua, attached := b.viewport, b.userAgent, b.attached
	b.mu.Unlock()
	if attached {
		// Before anything else: a tab of the user's Chrome reaches no site without a grant.
		patterns := []map[string]any{{"resourceType": "Document", "requestStage": "Request"}}
		if err := c.Call(ctx, t.session, "Fetch.enable", map[string]any{"patterns": patterns}, nil); err != nil {
			b.m.log().Warn("browser: could not guard a tab of Chrome; closing it", "err", err)
			_ = c.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": t.target}, nil)
			return
		}
	}
	for _, step := range []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Runtime.enable", nil},
		{"Network.enable", map[string]any{"maxPostDataSize": 0}},
		{"Log.enable", nil},
		{"Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}},
	} {
		if err := c.Call(ctx, t.session, step.method, step.params, nil); err != nil && !errors.Is(err, ErrConnClosed) {
			b.m.log().Debug("browser: tab setup", "method", step.method, "err", err)
		}
	}
	if !attached {
		_ = applyViewport(ctx, c, t.session, vp, ua)
	}
	_ = c.Call(ctx, t.session, "Runtime.runIfWaitingForDebugger", nil, nil)
	if attached && !b.siteAllowed(b.TabURL(t.id)) {
		// A popup can start loading before its guard is up.
		_ = c.Call(ctx, t.session, "Page.navigate", map[string]any{"url": "about:blank"}, nil)
	}
	b.mu.Lock()
	t.ready = true
	b.notifyLocked(true)
	b.mu.Unlock()
	b.refreshHistory(c, t)
	b.syncScreencast()
}

func applyViewport(ctx context.Context, c *conn, session string, vp Viewport, ua string) error {
	err := c.Call(ctx, session, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": vp.Width, "height": vp.Height, "deviceScaleFactor": vp.Scale, "mobile": vp.Mobile,
		"screenWidth": vp.Width, "screenHeight": vp.Height,
	}, nil)
	touch := map[string]any{"enabled": vp.Touch}
	if vp.Touch {
		touch["maxTouchPoints"] = 5
	}
	_ = c.Call(ctx, session, "Emulation.setTouchEmulationEnabled", touch, nil)
	if ua != "" {
		_ = c.Call(ctx, session, "Emulation.setUserAgentOverride", map[string]any{"userAgent": userAgentFor(vp, ua)}, nil)
	}
	return err
}

type remoteObject struct {
	Type                string          `json:"type"`
	Subtype             string          `json:"subtype"`
	Value               json.RawMessage `json:"value"`
	UnserializableValue string          `json:"unserializableValue"`
	Description         string          `json:"description"`
}

func (o remoteObject) String() string {
	switch {
	case o.Type == "string":
		var s string
		_ = json.Unmarshal(o.Value, &s)
		return s
	case o.UnserializableValue != "":
		return o.UnserializableValue
	case o.Type == "undefined":
		return "undefined"
	case len(o.Value) > 0 && (o.Type == "number" || o.Type == "boolean"):
		return string(o.Value)
	case o.Description != "":
		return o.Description
	case len(o.Value) > 0:
		return string(o.Value)
	}
	return o.Type
}

func (t *tab) addConsole(e ConsoleEntry) {
	if len([]rune(e.Text)) > 2000 {
		e.Text = string([]rune(e.Text)[:2000]) + "…"
	}
	if e.Level == "error" {
		t.consoleErrors++
	}
	t.console = append(t.console, e)
	if len(t.console) > maxConsoleEntries {
		t.console = t.console[len(t.console)-maxConsoleEntries:]
	}
}

func consoleLevel(l string) string {
	switch l {
	case "error", "assert":
		return "error"
	case "warning", "warn":
		return "warning"
	case "info":
		return "info"
	case "debug", "verbose", "trace":
		return "debug"
	}
	return "log"
}

func epochMillis(ms float64) time.Time {
	if ms <= 0 {
		return time.Now()
	}
	return time.UnixMilli(int64(ms))
}

// onTabEvent handles a tab's events. Caller holds b.mu.
func (b *Browser) onTabEvent(c *conn, t *tab, ev event) {
	switch ev.Method {
	case "Fetch.requestPaused":
		go b.onDocumentPaused(c, t, ev.Params)
	case "Page.frameNavigated":
		var p struct {
			Frame struct {
				ID          string `json:"id"`
				ParentID    string `json:"parentId"`
				URL         string `json:"url"`
				URLFragment string `json:"urlFragment"`
			} `json:"frame"`
		}
		if json.Unmarshal(ev.Params, &p) != nil || p.Frame.ParentID != "" {
			return
		}
		t.url = p.Frame.URL + p.Frame.URLFragment
		t.navSeq++
		t.world = 0
		t.consoleErrors = 0
		t.favicon = ""
		t.crashed = false
		if o := origin(t.url); o != "" {
			b.visited[o] = true
		}
		b.notifyLocked(true)
		go b.refreshHistory(c, t)
	case "Page.navigatedWithinDocument":
		var p struct {
			FrameID string `json:"frameId"`
			URL     string `json:"url"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && p.FrameID == t.target {
			t.url = p.URL
			b.notifyLocked(true)
			go b.refreshHistory(c, t)
		}
	case "Page.frameStartedLoading", "Page.frameStoppedLoading":
		var p struct {
			FrameID string `json:"frameId"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && p.FrameID == t.target {
			t.loading = ev.Method == "Page.frameStartedLoading"
			if !t.loading {
				t.stopSeq++
			}
			b.notifyLocked(true)
		}
	case "Page.loadEventFired":
		t.loadSeq++
		b.notifyLocked(false)
		go b.fetchFavicon(c, t)
	case "Page.javascriptDialogOpening":
		var p struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		_ = json.Unmarshal(ev.Params, &p)
		// Dialogs block the page in a headless browser: alerts, confirms and beforeunload are accepted,
		// prompts dismissed, and the agent sees it in the console.
		accept := p.Type != "prompt"
		verb := "accepted"
		if !accept {
			verb = "dismissed"
		}
		t.addConsole(ConsoleEntry{Time: time.Now(), Level: "warning", Text: fmt.Sprintf("[dialog] %s(%q) was %s automatically", p.Type, p.Message, verb)})
		go c.Call(context.Background(), t.session, "Page.handleJavaScriptDialog", map[string]any{"accept": accept}, nil)
	case "Page.screencastFrame":
		b.onFrameLocked(c, t, ev.Params)
	case "Runtime.executionContextsCleared":
		t.world = 0
	case "Runtime.executionContextDestroyed":
		var p struct {
			ID int64 `json:"executionContextId"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && p.ID == t.world {
			t.world = 0
		}
	case "Runtime.consoleAPICalled":
		var p struct {
			Type       string         `json:"type"`
			Args       []remoteObject `json:"args"`
			Timestamp  float64        `json:"timestamp"`
			StackTrace *struct {
				CallFrames []struct {
					URL        string `json:"url"`
					LineNumber int    `json:"lineNumber"`
				} `json:"callFrames"`
			} `json:"stackTrace"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		parts := make([]string, 0, len(p.Args))
		for _, a := range p.Args {
			parts = append(parts, a.String())
		}
		e := ConsoleEntry{Time: epochMillis(p.Timestamp), Level: consoleLevel(p.Type), Text: strings.Join(parts, " ")}
		if p.StackTrace != nil && len(p.StackTrace.CallFrames) > 0 && p.StackTrace.CallFrames[0].URL != "" {
			f := p.StackTrace.CallFrames[0]
			e.Source = fmt.Sprintf("%s:%d", f.URL, f.LineNumber+1)
		}
		b.addConsoleLocked(t, e)
	case "Runtime.exceptionThrown":
		var p struct {
			Timestamp        float64 `json:"timestamp"`
			ExceptionDetails struct {
				Text       string        `json:"text"`
				URL        string        `json:"url"`
				LineNumber int           `json:"lineNumber"`
				Exception  *remoteObject `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		d := p.ExceptionDetails
		text := d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			text = strings.TrimSpace(strings.TrimSuffix(text, ":")) + " " + d.Exception.Description
		}
		e := ConsoleEntry{Time: epochMillis(p.Timestamp), Level: "error", Text: text}
		if d.URL != "" {
			e.Source = fmt.Sprintf("%s:%d", d.URL, d.LineNumber+1)
		}
		b.addConsoleLocked(t, e)
	case "Log.entryAdded":
		var p struct {
			Entry struct {
				Level     string  `json:"level"`
				Text      string  `json:"text"`
				URL       string  `json:"url"`
				Timestamp float64 `json:"timestamp"`
			} `json:"entry"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			b.addConsoleLocked(t, ConsoleEntry{Time: epochMillis(p.Entry.Timestamp), Level: consoleLevel(p.Entry.Level), Text: p.Entry.Text, Source: p.Entry.URL})
		}
	case "Network.requestWillBeSent":
		var p struct {
			RequestID string  `json:"requestId"`
			Timestamp float64 `json:"timestamp"`
			WallTime  float64 `json:"wallTime"`
			Type      string  `json:"type"`
			Request   struct {
				URL    string `json:"url"`
				Method string `json:"method"`
			} `json:"request"`
			RedirectResponse *struct {
				Status     int    `json:"status"`
				StatusText string `json:"statusText"`
			} `json:"redirectResponse"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		if prev := t.netByID[p.RequestID]; prev != nil && p.RedirectResponse != nil {
			prev.Status, prev.StatusText, prev.Finished = p.RedirectResponse.Status, p.RedirectResponse.StatusText, true
			prev.Duration = time.Duration((p.Timestamp - prev.startTS) * float64(time.Second))
		}
		e := &NetworkEntry{ID: p.RequestID, Method: p.Request.Method, URL: p.Request.URL, Type: p.Type,
			Started: time.UnixMilli(int64(p.WallTime * 1000)), startTS: p.Timestamp}
		t.netByID[p.RequestID] = e
		t.network = append(t.network, e)
		if len(t.network) > maxNetworkEntries {
			old := t.network[0]
			if t.netByID[old.ID] == old {
				delete(t.netByID, old.ID)
			}
			t.network = t.network[1:]
		}
		b.notifyLocked(false)
	case "Network.responseReceived":
		var p struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Response  struct {
				Status     int    `json:"status"`
				StatusText string `json:"statusText"`
				MimeType   string `json:"mimeType"`
			} `json:"response"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			if e := t.netByID[p.RequestID]; e != nil {
				e.Status, e.StatusText, e.MimeType = p.Response.Status, p.Response.StatusText, p.Response.MimeType
				if p.Type != "" {
					e.Type = p.Type
				}
			}
		}
	case "Network.loadingFinished":
		var p struct {
			RequestID         string  `json:"requestId"`
			Timestamp         float64 `json:"timestamp"`
			EncodedDataLength float64 `json:"encodedDataLength"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			if e := t.netByID[p.RequestID]; e != nil {
				e.Finished, e.Size = true, int64(p.EncodedDataLength)
				e.Duration = time.Duration((p.Timestamp - e.startTS) * float64(time.Second))
			}
			b.notifyLocked(false)
		}
	case "Network.loadingFailed":
		var p struct {
			RequestID     string  `json:"requestId"`
			Timestamp     float64 `json:"timestamp"`
			ErrorText     string  `json:"errorText"`
			Canceled      bool    `json:"canceled"`
			BlockedReason string  `json:"blockedReason"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			if e := t.netByID[p.RequestID]; e != nil {
				e.Finished, e.Failed = true, p.ErrorText
				if p.Canceled {
					e.Failed = "canceled"
				}
				if p.BlockedReason != "" {
					e.Failed += " (blocked: " + p.BlockedReason + ")"
				}
				e.Duration = time.Duration((p.Timestamp - e.startTS) * float64(time.Second))
			}
			b.notifyLocked(false)
		}
	case "Inspector.targetCrashed":
		t.crashed = true
		t.loading = false
		b.addConsoleLocked(t, ConsoleEntry{Time: time.Now(), Level: "error", Text: "The page crashed (reload it to continue)."})
	}
}

func (b *Browser) addConsoleLocked(t *tab, e ConsoleEntry) {
	before := t.consoleErrors
	t.addConsole(e)
	b.notifyLocked(t.consoleErrors != before)
}

func (b *Browser) refreshHistory(c *conn, t *tab) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var h struct {
		CurrentIndex int            `json:"currentIndex"`
		Entries      []historyEntry `json:"entries"`
	}
	if c.Call(ctx, t.session, "Page.getNavigationHistory", nil, &h) != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	t.history, t.historyIndex = h.Entries, h.CurrentIndex
	back, fwd := h.CurrentIndex > 0, h.CurrentIndex < len(h.Entries)-1
	if back != t.canBack || fwd != t.canForward {
		t.canBack, t.canForward = back, fwd
		b.notifyLocked(true)
	}
}

const faviconScript = `(() => {
  const l = document.querySelector('link[rel~="icon"], link[rel="shortcut icon"], link[rel="apple-touch-icon"]');
  if (l && l.href) return l.href;
  return location.protocol === 'http:' || location.protocol === 'https:' ? location.origin + '/favicon.ico' : '';
})()`

func (b *Browser) fetchFavicon(c *conn, t *tab) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var href string
	if b.evalIsolated(ctx, c, t, faviconScript, &href) != nil {
		return
	}
	b.mu.Lock()
	if t.favicon != href {
		t.favicon = href
		b.notifyLocked(true)
	}
	b.mu.Unlock()
}

// ── state ───────────────────────────────────────────────────────────────────

// State returns the browser's state for the UI.
func (b *Browser) State() *agentv1.BrowserState {
	b.mu.Lock()
	sites := b.sites
	s := b.stateLocked()
	b.mu.Unlock()
	if sites != nil {
		s.AllowedSites = sites()
	}
	return s
}

func (b *Browser) stateLocked() *agentv1.BrowserState {
	s := &agentv1.BrowserState{SessionId: b.sessionID, Running: b.running, ActiveTabId: b.active, Viewport: b.viewport.proto(), TakenOver: b.takenOver,
		RealChrome: b.attached, Recording: b.rec != nil, LastRecording: b.lastRecording}
	for _, id := range b.order {
		t := b.tabs[id]
		s.Tabs = append(s.Tabs, &agentv1.BrowserTab{TabId: t.id, Url: t.url, Title: t.title, Loading: t.loading, FaviconUrl: t.favicon,
			ConsoleErrors: int32(t.consoleErrors), CanGoBack: t.canBack, CanGoForward: t.canForward, Active: t.id == b.active})
	}
	return s
}

// ── screencast ──────────────────────────────────────────────────────────────

const (
	minQuality = 30
	maxQuality = 80
)

// syncScreencast runs the screencast on the active tab while someone watches.
func (b *Browser) syncScreencast() {
	b.scMu.Lock()
	defer b.scMu.Unlock()
	b.mu.Lock()
	c := b.conn
	want, wantSess := "", ""
	if b.running && (len(b.viewers) > 0 || b.rec != nil) {
		if t := b.tabs[b.active]; t != nil && t.ready {
			want, wantSess = t.id, t.session
		}
	}
	cur, curSess := b.sc.tab, b.sc.session
	restart := b.sc.needsStart
	b.sc.needsStart = false
	quality, vp := b.sc.quality, b.viewport
	b.mu.Unlock()
	if c == nil || (cur == want && !restart) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if curSess != "" {
		_ = c.Call(ctx, curSess, "Page.stopScreencast", nil, nil)
	}
	if want != "" {
		maxW := min(int(float64(vp.Width)*vp.Scale), 1600)
		maxH := min(int(float64(vp.Height)*vp.Scale), 1600)
		err := c.Call(ctx, wantSess, "Page.startScreencast", map[string]any{"format": "jpeg", "quality": quality, "maxWidth": maxW, "maxHeight": maxH, "everyNthFrame": 1}, nil)
		if err != nil {
			want, wantSess = "", ""
		}
	}
	b.mu.Lock()
	if b.conn == c {
		b.sc.tab, b.sc.session = want, wantSess
	}
	b.mu.Unlock()
}

type frameHeader struct {
	Tab     string  `json:"tab"`
	Seq     int     `json:"seq"`
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
	Scale   float64 `json:"scale"`
	ScrollX float64 `json:"scrollX"`
	ScrollY float64 `json:"scrollY"`
	TS      float64 `json:"ts"`
}

// encodeFrame builds a binary socket frame: [uint32 BE header length][header JSON][JPEG].
func encodeFrame(h frameHeader, img []byte) []byte {
	hj, _ := json.Marshal(h)
	out := make([]byte, 4+len(hj)+len(img))
	binary.BigEndian.PutUint32(out, uint32(len(hj)))
	copy(out[4:], hj)
	copy(out[4+len(hj):], img)
	return out
}

// onFrameLocked handles a screencast frame. Caller holds b.mu.
func (b *Browser) onFrameLocked(c *conn, t *tab, params json.RawMessage) {
	var p struct {
		Data     string `json:"data"`
		Metadata struct {
			DeviceWidth   float64 `json:"deviceWidth"`
			DeviceHeight  float64 `json:"deviceHeight"`
			ScrollOffsetX float64 `json:"scrollOffsetX"`
			ScrollOffsetY float64 `json:"scrollOffsetY"`
			Timestamp     float64 `json:"timestamp"`
		} `json:"metadata"`
		SessionID int `json:"sessionId"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	go c.Call(context.Background(), t.session, "Page.screencastFrameAck", map[string]any{"sessionId": p.SessionID}, nil)
	if t.id != b.sc.tab {
		return
	}
	img, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return
	}
	if b.rec != nil {
		b.rec.offer(img, time.Now())
	}
	scale := 1.0
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(img)); err == nil && p.Metadata.DeviceWidth > 0 {
		scale = float64(cfg.Width) / p.Metadata.DeviceWidth
	}
	b.sc.seq++
	frame := encodeFrame(frameHeader{Tab: t.id, Seq: b.sc.seq, Width: p.Metadata.DeviceWidth, Height: p.Metadata.DeviceHeight, Scale: scale,
		ScrollX: p.Metadata.ScrollOffsetX, ScrollY: p.Metadata.ScrollOffsetY, TS: p.Metadata.Timestamp}, img)
	b.lastFrame = frame
	dropped := 0
	for v := range b.viewers {
		if v.offerFrame(frame) {
			dropped++
		}
	}
	b.adaptQualityLocked(dropped > 0)
}

// adaptQualityLocked lowers the JPEG quality when viewers fall behind and raises it again after a calm while.
func (b *Browser) adaptQualityLocked(dropped bool) {
	now := time.Now()
	sc := &b.sc
	if now.Sub(sc.window) > 2*time.Second {
		sc.window, sc.drops = now, 0
	}
	if dropped {
		sc.drops++
		sc.lastDrop = now
	}
	switch {
	case sc.drops >= 3 && sc.quality > minQuality:
		sc.quality = max(minQuality, sc.quality-15)
		sc.drops, sc.needsStart = 0, true
		go b.restartScreencast()
	case !dropped && sc.quality < maxQuality && now.Sub(sc.lastDrop) > 10*time.Second && now.Sub(sc.lastRaise) > 10*time.Second:
		sc.quality = min(maxQuality, sc.quality+10)
		sc.lastRaise, sc.needsStart = now, true
		go b.restartScreencast()
	}
}

func (b *Browser) restartScreencast() {
	b.mu.Lock()
	b.sc.needsStart = true
	b.mu.Unlock()
	b.syncScreencast()
}

// ── helpers ─────────────────────────────────────────────────────────────────

// origin returns scheme://host[:port] of an http(s) URL, "" otherwise.
func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
