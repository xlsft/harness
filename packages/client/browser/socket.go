package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

const (
	// SocketPath is where the browser WebSocket is served, next to AgentService.
	SocketPath = "/harness.agent.v1.Browser/socket"
	// Subprotocol is the WebSocket subprotocol the server selects; clients offer it next to "ticket.<ticket>".
	Subprotocol = "harness-browser.v1"
	// TicketProtocolPrefix marks the Sec-WebSocket-Protocol entry carrying the ticket.
	TicketProtocolPrefix = "ticket."

	maxCommandMessage = 1 << 20
	writeTimeout      = 30 * time.Second
	pingInterval      = 30 * time.Second
)

// viewer is one socket watching a browser.
type viewer struct {
	frame   chan []byte
	msgs    chan []byte
	state   chan struct{}
	done    chan struct{}
	once    sync.Once
	reason  string
	buttons int
}

func newViewer() *viewer {
	return &viewer{frame: make(chan []byte, 1), msgs: make(chan []byte, 64), state: make(chan struct{}, 1), done: make(chan struct{})}
}

// offerFrame replaces a frame the viewer has not sent yet; it reports whether one was dropped.
func (v *viewer) offerFrame(f []byte) (dropped bool) {
	select {
	case v.frame <- f:
		return false
	default:
	}
	select {
	case <-v.frame:
		dropped = true
	default:
	}
	select {
	case v.frame <- f:
	default:
	}
	return dropped
}

func (v *viewer) signalState() {
	select {
	case v.state <- struct{}{}:
	default:
	}
}

func (v *viewer) send(msg any) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case v.msgs <- b:
	default: // a viewer this far behind misses messages; the next state catches it up
	}
}

func (v *viewer) close(reason string) {
	v.once.Do(func() {
		v.reason = reason
		close(v.done)
	})
}

func protoJSON(m proto.Message) json.RawMessage {
	b, err := protojson.MarshalOptions{UseProtoNames: false}.Marshal(m)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// broadcastAction sends an action to the viewers. Caller holds b.mu.
func (b *Browser) broadcastActionLocked(a *agentv1.BrowserAction) {
	msg := map[string]any{"type": "action", "action": protoJSON(a)}
	for v := range b.viewers {
		v.send(msg)
	}
}

// SocketHandler serves the browser WebSocket. A request with an Origin header must come from allowedOrigins;
// the one-time ticket (IssueTicket) authenticates it instead of the bearer token.
func (m *Manager) SocketHandler(allowedOrigins []string) http.Handler {
	origins := map[string]bool{}
	for _, o := range allowedOrigins {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			origins[strings.ToLower(o)] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !origins[strings.ToLower(o)] {
			http.Error(w, "forbidden: Origin is not allowed", http.StatusForbidden)
			return
		}
		b, err := m.redeem(requestTicket(r))
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}, InsecureSkipVerify: true})
		if err != nil {
			return
		}
		b.serveSocket(c)
	})
}

func requestTicket(r *http.Request) string {
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); strings.HasPrefix(p, TicketProtocolPrefix) {
				return p[len(TicketProtocolPrefix):]
			}
		}
	}
	return r.URL.Query().Get("ticket")
}

// addViewer registers a viewer; the browser must run.
func (b *Browser) addViewer(ctx context.Context) (*viewer, error) {
	if err := b.Ensure(ctx); err != nil {
		return nil, err
	}
	v := newViewer()
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return nil, errors.New("the browser stopped")
	}
	b.viewers[v] = struct{}{}
	if b.lastFrame != nil {
		v.offerFrame(b.lastFrame)
	}
	b.mu.Unlock()
	v.signalState()
	b.syncScreencast()
	return v, nil
}

func (b *Browser) removeViewer(v *viewer) {
	b.mu.Lock()
	delete(b.viewers, v)
	b.lastUse = time.Now()
	b.mu.Unlock()
	b.syncScreencast()
}

// command is a message from the UI.
type command struct {
	Type       string  `json:"type"`
	URL        string  `json:"url"`
	Tab        string  `json:"tab"`
	Preset     string  `json:"preset"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Event      string  `json:"event"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Button     string  `json:"button"`
	ClickCount int     `json:"clickCount"`
	DeltaX     float64 `json:"deltaX"`
	DeltaY     float64 `json:"deltaY"`
	Modifiers  int     `json:"modifiers"`
	Key        string  `json:"key"`
	Code       string  `json:"code"`
	Text       string  `json:"text"`
	KeyCode    int     `json:"keyCode"`
	On         bool    `json:"on"`
}

func (b *Browser) serveSocket(c *websocket.Conn) {
	defer c.CloseNow()
	c.SetReadLimit(maxCommandMessage)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v, err := b.addViewer(ctx)
	if err != nil {
		_ = c.Close(websocket.StatusInternalError, truncate(err.Error(), 120))
		return
	}
	defer b.removeViewer(v)

	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var cmd command
			if json.Unmarshal(data, &cmd) != nil {
				continue
			}
			b.mu.Lock()
			b.lastUse = time.Now()
			b.mu.Unlock()
			cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
			if err := b.userCommand(cctx, v, cmd); err != nil && ctx.Err() == nil {
				v.send(map[string]any{"type": "error", "message": err.Error()})
			}
			ccancel()
		}
	}()
	go func() {
		tick := time.NewTicker(pingInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				pctx, pc := context.WithTimeout(ctx, writeTimeout)
				err := c.Ping(pctx)
				pc()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	write := func(typ websocket.MessageType, p []byte) error {
		wctx, wc := context.WithTimeout(ctx, writeTimeout)
		defer wc()
		return c.Write(wctx, typ, p)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-v.done:
			msg, _ := json.Marshal(map[string]any{"type": "closed", "reason": v.reason})
			_ = write(websocket.MessageText, msg)
			_ = c.Close(websocket.StatusNormalClosure, "browser closed")
			return
		case <-v.state:
			msg, _ := json.Marshal(map[string]any{"type": "state", "state": protoJSON(b.State())})
			if write(websocket.MessageText, msg) != nil {
				return
			}
		case m := <-v.msgs:
			if write(websocket.MessageText, m) != nil {
				return
			}
		case f := <-v.frame:
			if write(websocket.MessageBinary, f) != nil {
				return
			}
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// userCommand runs a command of the Browser pane.
func (b *Browser) userCommand(ctx context.Context, v *viewer, cmd command) error {
	switch cmd.Type {
	case "navigate":
		u, err := NormalizeURL(cmd.URL)
		if err != nil {
			return err
		}
		// Going there in the pane is the user's own act: it needs no question.
		b.grantForUser(u)
		_, err = b.Navigate(ctx, u, false)
		return err
	case "back":
		_, err := b.History(ctx, -1, false)
		return err
	case "forward":
		_, err := b.History(ctx, 1, false)
		return err
	case "reload":
		_, err := b.Reload(ctx, false)
		return err
	case "stop":
		t, c, err := b.activeTab(ctx)
		if err != nil {
			return err
		}
		return c.Call(ctx, t.session, "Page.stopLoading", nil, nil)
	case "newTab":
		u := strings.TrimSpace(cmd.URL)
		if u == "" {
			u = "about:blank"
		} else {
			var err error
			if u, err = NormalizeURL(u); err != nil {
				return err
			}
			b.grantForUser(u)
		}
		_, err := b.NewTab(ctx, u)
		return err
	case "closeTab":
		return b.CloseTab(ctx, cmd.Tab)
	case "switchTab":
		return b.SwitchTab(ctx, cmd.Tab)
	case "viewport":
		vp, err := PresetViewport(cmd.Preset, cmd.Width, cmd.Height)
		if err != nil {
			return err
		}
		return b.SetViewport(ctx, vp)
	case "mouse":
		return b.userMouse(ctx, v, cmd)
	case "key":
		return b.userKey(ctx, cmd)
	case "paste":
		t, c, err := b.activeTab(ctx)
		if err != nil {
			return err
		}
		if len(cmd.Text) > 1<<16 {
			return errors.New("the pasted text is longer than 64 KB")
		}
		return c.Call(ctx, t.session, "Input.insertText", map[string]any{"text": cmd.Text}, nil)
	case "takeover":
		b.SetTakenOver(cmd.On)
		return nil
	}
	return nil
}

// SetTakenOver marks that the user drives the browser (the agent's action tools fail meanwhile).
func (b *Browser) SetTakenOver(on bool) {
	b.mu.Lock()
	if b.takenOver != on {
		b.takenOver = on
		b.notifyLocked(true)
	}
	b.mu.Unlock()
}

var mouseButtons = map[string]int{"left": 1, "right": 2, "middle": 4}

func (b *Browser) userMouse(ctx context.Context, v *viewer, cmd command) error {
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return err
	}
	button := cmd.Button
	if button == "" {
		button = "none"
	}
	if _, ok := mouseButtons[button]; !ok {
		button = "none"
	}
	p := map[string]any{"x": cmd.X, "y": cmd.Y, "modifiers": cmd.Modifiers}
	switch cmd.Event {
	case "move":
		p["type"], p["button"] = "mouseMoved", "none"
		for name, bit := range mouseButtons {
			if v.buttons&bit != 0 {
				p["button"] = name
				break
			}
		}
	case "down":
		v.buttons |= mouseButtons[button]
		p["type"], p["button"], p["clickCount"] = "mousePressed", button, max(cmd.ClickCount, 1)
	case "up":
		v.buttons &^= mouseButtons[button]
		p["type"], p["button"], p["clickCount"] = "mouseReleased", button, max(cmd.ClickCount, 1)
	case "wheel":
		p["type"], p["button"], p["deltaX"], p["deltaY"] = "mouseWheel", "none", cmd.DeltaX, cmd.DeltaY
	default:
		return nil
	}
	p["buttons"] = v.buttons
	return c.Call(ctx, t.session, "Input.dispatchMouseEvent", p, nil)
}

func (b *Browser) userKey(ctx context.Context, cmd command) error {
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return err
	}
	p := map[string]any{"key": cmd.Key, "code": cmd.Code, "modifiers": cmd.Modifiers}
	if cmd.KeyCode > 0 {
		p["windowsVirtualKeyCode"], p["nativeVirtualKeyCode"] = cmd.KeyCode, cmd.KeyCode
	}
	switch cmd.Event {
	case "down":
		p["type"] = "rawKeyDown"
		if cmd.Text != "" && cmd.Modifiers&(modCtrl|modMeta|modAlt) == 0 {
			p["type"], p["text"], p["unmodifiedText"] = "keyDown", cmd.Text, cmd.Text
		}
		if cmds := editingCommands(cmd.Key, cmd.Modifiers); len(cmds) > 0 {
			p["commands"] = cmds
		}
	case "up":
		p["type"] = "keyUp"
	case "char":
		if cmd.Text == "" {
			return nil
		}
		p["type"], p["text"], p["unmodifiedText"] = "char", cmd.Text, cmd.Text
	default:
		return nil
	}
	return c.Call(ctx, t.session, "Input.dispatchKeyEvent", p, nil)
}
