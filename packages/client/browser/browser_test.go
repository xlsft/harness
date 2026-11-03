package browser

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/tools"
)

// These tests drive a real Chromium; they are skipped when none is installed (or with -short).

const testPage = `<!doctype html><html><head><title>Test Page</title></head><body>
<h1>Hello harness</h1>
<p id="status">idle</p>
<button id="btn" onclick="document.getElementById('status').textContent='clicked'">Press me</button>
<label for="name">Name</label><input id="name" type="text">
<label for="pw">Password</label><input id="pw" type="password">
<label for="color">Color</label><select id="color"><option>Red</option><option>Green</option></select>
<div id="pad" style="height:3000px">tall</div>
<a href="/second">Second page</a>
<script>console.error('boom from page'); fetch('/api/data');</script>
</body></html>`

const secondPage = `<!doctype html><html><head><title>Second</title></head><body>
<p id="count">0</p>
<button id="inc" style="position:absolute;left:20px;top:20px;width:200px;height:60px"
  onmousedown="var c=document.getElementById('count'); c.textContent=String(+c.textContent+1)">Increment</button>
</body></html>`

func testServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, testPage)
	})
	mux.HandleFunc("/second", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, secondPage)
	})
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type actionLog struct {
	mu      sync.Mutex
	actions []*agentv1.BrowserAction
}

func (l *actionLog) add(_ string, a *agentv1.BrowserAction) {
	l.mu.Lock()
	l.actions = append(l.actions, a)
	l.mu.Unlock()
}

func (l *actionLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, a := range l.actions {
		if a.Phase == agentv1.BrowserAction_PHASE_STARTED {
			out = append(out, a.Kind)
		}
	}
	return out
}

func liveManager(t *testing.T) (*Manager, *actionLog) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping the live browser test in -short mode")
	}
	dir := t.TempDir()
	if _, ok := Discover(dir); !ok {
		t.Skip("no Chromium-based browser installed")
	}
	log := &actionLog{}
	m := &Manager{Dir: dir, IdleTimeout: -1, OnAction: log.add}
	t.Cleanup(m.Close)
	return m, log
}

var refFor = func(snapshot, role, name string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(role+` "`+name+`"`) + ` \[(ref_\d+)\]`)
	if m := re.FindStringSubmatch(snapshot); m != nil {
		return m[1]
	}
	return ""
}

func TestLiveBrowser(t *testing.T) {
	m, log := liveManager(t)
	srv := testServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b, err := m.Session("sess-live")
	if err != nil {
		t.Fatal(err)
	}

	r, err := b.Navigate(ctx, srv.URL+"/", true)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Loaded || r.Title != "Test Page" {
		t.Fatalf("navigate result %+v", r)
	}

	snap, err := b.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`heading "Hello harness"`, `button "Press me" [ref_`, `textbox "Password" [ref_`, `[sensitive: password field`, `combobox "Color"`, `link "Second page"`} {
		if !strings.Contains(snap.Text, want) {
			t.Fatalf("snapshot lacks %q:\n%s", want, snap.Text)
		}
	}
	btn := refFor(snap.Text, "button", "Press me")
	name := refFor(snap.Text, "textbox", "Name")
	pw := refFor(snap.Text, "textbox", "Password")
	color := refFor(snap.Text, "combobox", "Color")
	if btn == "" || name == "" || pw == "" || color == "" {
		t.Fatalf("refs missing (btn %q name %q pw %q color %q):\n%s", btn, name, pw, color, snap.Text)
	}

	t.Run("refs are stable", func(t *testing.T) {
		again, err := b.Snapshot(ctx, SnapshotOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if refFor(again.Text, "button", "Press me") != btn {
			t.Fatalf("the button's ref changed:\n%s", again.Text)
		}
	})

	t.Run("click", func(t *testing.T) {
		res, err := b.Click(ctx, ClickOptions{Ref: btn, ToolUseID: "tu_click"})
		if err != nil {
			t.Fatal(err)
		}
		if res.X <= 0 || res.Y <= 0 {
			t.Fatalf("click point %+v", res)
		}
		ok, err := b.Wait(ctx, WaitOptions{Text: "clicked", Timeout: 5 * time.Second})
		if err != nil || !ok {
			t.Fatalf("the click did not change the text: %v %v", ok, err)
		}
	})

	t.Run("type", func(t *testing.T) {
		if _, err := b.Type(ctx, TypeOptions{Ref: name, Text: "Alice", Clear: true}); err != nil {
			t.Fatal(err)
		}
		v, err := b.Eval(ctx, `document.getElementById('name').value`, 5*time.Second, "")
		if err != nil || v.Value != `"Alice"` {
			t.Fatalf("value %+v %v", v, err)
		}
		if _, err := b.Type(ctx, TypeOptions{Ref: name, Text: " Smith"}); err != nil {
			t.Fatal(err)
		}
		v, _ = b.Eval(ctx, `document.getElementById('name').value`, 5*time.Second, "")
		if v.Value != `"Alice Smith"` {
			t.Fatalf("appended value %s", v.Value)
		}
		if _, err := b.Keys(ctx, "ctrl+a Backspace", 1, ""); err != nil {
			t.Fatal(err)
		}
		v, _ = b.Eval(ctx, `document.getElementById('name').value`, 5*time.Second, "")
		if v.Value != `""` {
			t.Fatalf("ctrl+a Backspace left %s", v.Value)
		}
	})

	t.Run("password field is refused", func(t *testing.T) {
		_, err := b.Type(ctx, TypeOptions{Ref: pw, Text: "hunter2", Clear: true})
		var se ErrSensitive
		if !errors.As(err, &se) {
			t.Fatalf("want ErrSensitive, got %v", err)
		}
		if why := b.FieldSensitivity(ctx, pw); why == "" {
			t.Fatal("FieldSensitivity did not flag the password field")
		}
		v, _ := b.Eval(ctx, `document.getElementById('pw').value`, 5*time.Second, "")
		if v.Value != `""` {
			t.Fatalf("the password field got %s", v.Value)
		}
	})

	t.Run("select", func(t *testing.T) {
		if _, err := b.Type(ctx, TypeOptions{Ref: color, Text: "green"}); err != nil {
			t.Fatal(err)
		}
		v, _ := b.Eval(ctx, `document.getElementById('color').value`, 5*time.Second, "")
		if v.Value != `"Green"` {
			t.Fatalf("select value %s", v.Value)
		}
	})

	t.Run("scroll", func(t *testing.T) {
		res, err := b.Scroll(ctx, ScrollOptions{Direction: "down", Amount: 600})
		if err != nil {
			t.Fatal(err)
		}
		if res.ScrollY <= 0 || res.Height < 3000 {
			t.Fatalf("scroll result %+v", res)
		}
	})

	t.Run("screenshot", func(t *testing.T) {
		img, err := b.Screenshot(ctx, ScreenshotOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(img.Data) < 1000 || !bytes.HasPrefix(img.Data, []byte("\x89PNG")) {
			t.Fatalf("screenshot %d bytes, %q", len(img.Data), img.Data[:min(8, len(img.Data))])
		}
		full, err := b.Screenshot(ctx, ScreenshotOptions{FullPage: true})
		if err != nil || full.MediaType != "image/jpeg" || full.Height < 3000 {
			t.Fatalf("full page %+v %v", full.Height, err)
		}
	})

	t.Run("eval", func(t *testing.T) {
		v, err := b.Eval(ctx, `await Promise.resolve(1 + 2)`, 5*time.Second, "")
		if err != nil || v.Value != "3" {
			t.Fatalf("eval %+v %v", v, err)
		}
		v, err = b.Eval(ctx, `({a: [1, "x"]})`, 5*time.Second, "")
		if err != nil || v.Value != `{"a":[1,"x"]}` {
			t.Fatalf("eval object %+v %v", v, err)
		}
		v, err = b.Eval(ctx, `throw new Error("nope")`, 5*time.Second, "")
		if err != nil || !strings.Contains(v.Exception, "nope") {
			t.Fatalf("eval exception %+v %v", v, err)
		}
	})

	t.Run("console and network", func(t *testing.T) {
		entries, _, err := b.Console(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range entries {
			if e.Level == "error" && strings.Contains(e.Text, "boom from page") {
				found = true
			}
		}
		if !found {
			t.Fatalf("console lacks the page error: %+v", entries)
		}
		var reqs []NetworkEntry
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			reqs, _, _ = b.Network(ctx, false)
			done := false
			for _, e := range reqs {
				if strings.HasSuffix(e.URL, "/api/data") && e.Status == 200 && e.Finished {
					done = true
				}
			}
			if done {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		ok := false
		for _, e := range reqs {
			if strings.HasSuffix(e.URL, "/api/data") && e.Status == 200 {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("network lacks /api/data: %+v", reqs)
		}
		if st := b.State(); st.Tabs[0].ConsoleErrors < 1 {
			t.Fatalf("state console errors %d", st.Tabs[0].ConsoleErrors)
		}
	})

	t.Run("tabs", func(t *testing.T) {
		first := b.State().ActiveTabId
		id, err := b.NewTab(ctx, srv.URL+"/second")
		if err != nil {
			t.Fatal(err)
		}
		st := b.State()
		if len(st.Tabs) != 2 || st.ActiveTabId != id {
			t.Fatalf("state after new tab %+v", st)
		}
		if err := b.SwitchTab(ctx, first); err != nil {
			t.Fatal(err)
		}
		if err := b.CloseTab(ctx, id); err != nil {
			t.Fatal(err)
		}
		wctx, c := context.WithTimeout(ctx, 5*time.Second)
		defer c()
		if err := b.waitFor(wctx, func() bool { return len(b.order) == 1 }); err != nil {
			t.Fatalf("the tab did not close: %+v", b.State())
		}
	})

	t.Run("history", func(t *testing.T) {
		if _, err := b.Navigate(ctx, srv.URL+"/second", true); err != nil {
			t.Fatal(err)
		}
		if b.HistoryURL(-1) == "" {
			wctx, c := context.WithTimeout(ctx, 3*time.Second)
			_ = b.waitFor(wctx, func() bool { t := b.tabs[b.active]; return t != nil && t.canBack })
			c()
		}
		r, err := b.History(ctx, -1, true)
		if err != nil {
			t.Fatal(err)
		}
		if r.Title != "Test Page" {
			t.Fatalf("back landed on %+v", r)
		}
	})

	t.Run("socket", func(t *testing.T) {
		if _, err := b.Navigate(ctx, srv.URL+"/second", true); err != nil {
			t.Fatal(err)
		}
		hs := httptest.NewServer(m.SocketHandler(nil))
		defer hs.Close()
		ticket, err := m.IssueTicket("sess-live")
		if err != nil {
			t.Fatal(err)
		}
		wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + SocketPath
		c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{Subprotocol, TicketProtocolPrefix + ticket}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.CloseNow()
		c.SetReadLimit(64 << 20)
		gotFrame, gotState := false, false
		rctx, rc := context.WithTimeout(ctx, 15*time.Second)
		defer rc()
		for !gotFrame || !gotState {
			typ, data, err := c.Read(rctx)
			if err != nil {
				t.Fatalf("frame %v state %v: %v", gotFrame, gotState, err)
			}
			if typ == websocket.MessageBinary {
				n := binary.BigEndian.Uint32(data)
				var h frameHeader
				if err := json.Unmarshal(data[4:4+n], &h); err != nil {
					t.Fatal(err)
				}
				img := data[4+n:]
				if h.Width != 1280 || len(img) < 100 || img[0] != 0xff || img[1] != 0xd8 {
					t.Fatalf("frame header %+v, %d image bytes", h, len(img))
				}
				gotFrame = true
				continue
			}
			var msg struct {
				Type  string          `json:"type"`
				State json.RawMessage `json:"state"`
			}
			_ = json.Unmarshal(data, &msg)
			if msg.Type == "state" && bytes.Contains(msg.State, []byte(`"running":true`)) {
				gotState = true
			}
		}
		// The ticket is single-use.
		if _, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{Subprotocol, TicketProtocolPrefix + ticket}}); err == nil {
			t.Fatal("a ticket worked twice")
		}
		// Input over the socket: press the button (onmousedown increments the counter).
		for _, ev := range []string{"down", "up"} {
			msg := fmt.Sprintf(`{"type":"mouse","event":%q,"x":100,"y":50,"button":"left","clickCount":1}`, ev)
			if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
				t.Fatal(err)
			}
		}
		ok, err := b.Wait(ctx, WaitOptions{Text: "1", Timeout: 5 * time.Second})
		if err != nil || !ok {
			v, _ := b.Eval(ctx, `document.getElementById('count').textContent`, time.Second, "")
			t.Fatalf("socket click did not reach the page (count %s): %v", v.Value, err)
		}
		// Keys and paste over the socket into a focused field.
		if _, err := b.Eval(ctx, `document.body.insertAdjacentHTML('beforeend', '<input id=k>'); document.getElementById('k').focus(); true`, time.Second, ""); err != nil {
			t.Fatal(err)
		}
		for _, msg := range []string{
			`{"type":"key","event":"down","key":"h","code":"KeyH","text":"h","keyCode":72}`,
			`{"type":"key","event":"up","key":"h","code":"KeyH","keyCode":72}`,
			`{"type":"paste","text":"ello"}`,
		} {
			if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
				t.Fatal(err)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			v, _ := b.Eval(ctx, `document.getElementById('k').value`, time.Second, "")
			if v.Value == `"hello"` {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("socket keys typed %s", v.Value)
			}
			time.Sleep(100 * time.Millisecond)
		}
		// Taking over blocks the agent's actions.
		if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"takeover","on":true}`)); err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(3 * time.Second)
		for !b.State().TakenOver && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if _, err := b.Click(ctx, ClickOptions{X: f64(1), Y: f64(1)}); !errors.Is(err, ErrTakenOver) {
			t.Fatalf("click while taken over: %v", err)
		}
		b.SetTakenOver(false)
	})

	t.Run("tools", func(t *testing.T) {
		env := &tools.Env{SessionID: "sess-live"}
		byName := map[string]tools.Tool{}
		for _, tl := range m.Tools() {
			byName[tl.Name()] = tl
		}
		run := func(name, input string) tools.Result {
			t.Helper()
			return byName[name].Run(ctx, env, tools.Call{ID: "tu_" + name, Input: json.RawMessage(input)})
		}
		text := func(r tools.Result) string {
			var s []string
			for _, b := range r.Content {
				s = append(s, b.Text)
			}
			return strings.Join(s, "\n")
		}
		r := run(ToolNavigate, fmt.Sprintf(`{"url":%q}`, srv.URL+"/"))
		if r.IsError || !strings.Contains(text(r), "(loaded)") {
			t.Fatalf("navigate: %s", text(r))
		}
		r = run(ToolSnapshot, `{}`)
		if r.IsError || !strings.Contains(text(r), untrustedOpen) || !strings.Contains(text(r), `heading "Hello harness"`) {
			t.Fatalf("snapshot: %s", text(r))
		}
		r = run(ToolScreenshot, `{}`)
		if r.IsError || len(r.Content) != 2 || r.Content[1].Type != llm.BlockImage || r.Content[1].Source.Data == "" {
			t.Fatalf("screenshot: %+v", r)
		}
		r = run(ToolTabs, `{"action":"list"}`)
		if r.IsError || !strings.Contains(text(r), "* t") {
			t.Fatalf("tabs: %s", text(r))
		}
		r = run(ToolConsole, `{"level":"error"}`)
		if r.IsError || !strings.Contains(text(r), "boom from page") {
			t.Fatalf("console: %s", text(r))
		}
		r = run(ToolNetwork, `{"filter":"/api/"}`)
		if r.IsError || !strings.Contains(text(r), "/api/data") {
			t.Fatalf("network: %s", text(r))
		}
		r = run(ToolEval, `{"expression":"document.title"}`)
		if r.IsError || !strings.Contains(text(r), `"Test Page"`) {
			t.Fatalf("eval: %s", text(r))
		}
		r = run(ToolWait, `{"selector":"#btn","timeout":2000}`)
		if r.IsError || !strings.Contains(text(r), "Done waiting") {
			t.Fatalf("wait: %s", text(r))
		}
		r = run(ToolWait, `{"text":"never-there","timeout":300}`)
		if r.IsError || !strings.Contains(text(r), "Timed out") {
			t.Fatalf("wait timeout: %s", text(r))
		}
		// Typing into the password field: refused unless the permission check flagged (and the user approved) it.
		snap := text(run(ToolSnapshot, `{}`))
		pwRef := refFor(snap, "textbox", "Password")
		r = run(ToolType, fmt.Sprintf(`{"ref":%q,"text":"secret"}`, pwRef))
		if !r.IsError || !strings.Contains(text(r), "password") {
			t.Fatalf("password type: %s", text(r))
		}
		h := byName[ToolType].(tools.PermissionHinter).PermissionHints(ctx, env, tools.Call{ID: "tu_pw", Input: json.RawMessage(fmt.Sprintf(`{"ref":%q,"text":"x"}`, pwRef))})
		if h.Sensitive == "" || !strings.HasPrefix(h.URL, srv.URL) {
			t.Fatalf("hints %+v", h)
		}
		r = byName[ToolType].Run(ctx, env, tools.Call{ID: "tu_pw", Input: json.RawMessage(fmt.Sprintf(`{"ref":%q,"text":"x"}`, pwRef))})
		if r.IsError {
			t.Fatalf("approved password type failed: %s", text(r))
		}
		// The field now has the focus: printable keys are refused too, Enter and shortcuts are not.
		r = run(ToolKey, `{"keys":"h i"}`)
		if !r.IsError || !strings.Contains(text(r), "password") {
			t.Fatalf("keys into the password field: %s", text(r))
		}
		if v, _ := b.Eval(ctx, `document.getElementById('pw').value`, 5*time.Second, ""); v.Value != `"x"` {
			t.Fatalf("the password field holds %s", v.Value)
		}
		if h := byName[ToolKey].(tools.PermissionHinter).PermissionHints(ctx, env, tools.Call{ID: "tu_k", Input: json.RawMessage(`{"keys":"a"}`)}); h.Sensitive == "" {
			t.Fatalf("key hints %+v", h)
		}
		if h := byName[ToolKey].(tools.PermissionHinter).PermissionHints(ctx, env, tools.Call{ID: "tu_k2", Input: json.RawMessage(`{"keys":"ctrl+a Tab"}`)}); h.Sensitive != "" {
			t.Fatalf("shortcut hints %+v", h)
		}
		if r = run(ToolKey, `{"keys":"Tab"}`); r.IsError {
			t.Fatalf("Tab: %s", text(r))
		}
	})

	kinds := strings.Join(log.kinds(), ",")
	for _, k := range []string{"click", "type", "navigate", "snapshot", "screenshot"} {
		if !strings.Contains(kinds, k) {
			t.Errorf("no %q action reported (got %s)", k, kinds)
		}
	}

	t.Run("stop and restore", func(t *testing.T) {
		b.Stop("test")
		if b.Running() {
			t.Fatal("still running")
		}
		if err := b.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		wctx, c := context.WithTimeout(ctx, 15*time.Second)
		defer c()
		if err := b.waitFor(wctx, func() bool {
			for _, t := range b.tabs {
				if strings.HasPrefix(t.url, srv.URL) {
					return true
				}
			}
			return false
		}); err != nil {
			t.Fatalf("tabs not restored: %+v", b.State())
		}
	})
}

func TestLiveViewportAndClear(t *testing.T) {
	m, _ := liveManager(t)
	srv := testServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b, _ := m.Session("sess-vp")
	if _, err := b.Navigate(ctx, srv.URL+"/", true); err != nil {
		t.Fatal(err)
	}
	vp, _ := PresetViewport("mobile", 0, 0)
	if err := b.SetViewport(ctx, vp); err != nil {
		t.Fatal(err)
	}
	v, err := b.Eval(ctx, `[screen.width, navigator.userAgent.includes('Mobile'), matchMedia('(pointer: coarse)').matches]`, 5*time.Second, "")
	if err != nil || !strings.HasPrefix(v.Value, "[375,true") {
		t.Fatalf("mobile emulation: %s %v", v.Value, err)
	}
	if _, err := b.Eval(ctx, `document.cookie = "k=v; max-age=3600"; localStorage.setItem("x", "1"); true`, 5*time.Second, ""); err != nil {
		t.Fatal(err)
	}
	if err := b.ClearData(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Wait(ctx, WaitOptions{Navigation: true, Timeout: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	v, _ = b.Eval(ctx, `document.cookie + "|" + localStorage.getItem("x")`, 5*time.Second, "")
	if v.Value != `"|null"` {
		t.Fatalf("after clear: %s", v.Value)
	}
}
