//go:build unix

package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const uiOrigin = "http://localhost:3210"

func socketServer(t *testing.T, m *Manager) string {
	ts := httptest.NewServer(m.SocketHandler([]string{uiOrigin, "tauri://localhost"}))
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http")
}

func dial(ctx context.Context, url, origin string, protocols ...string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h, Subprotocols: protocols})
}

// readSocket reads frames until the output contains want or a control message of type stop arrives.
func readSocket(t *testing.T, ctx context.Context, c *websocket.Conn, want, stop string) (string, []control) {
	t.Helper()
	var out strings.Builder
	var ctl []control
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read (want %q): %v; output so far %q", want, err, out.String())
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
		} else {
			var m control
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("bad control message %q", data)
			}
			ctl = append(ctl, m)
			if m.Type == stop {
				return out.String(), ctl
			}
		}
		if want != "" && strings.Contains(out.String(), want) {
			return out.String(), ctl
		}
	}
}

func TestSocketAuthAndOrigin(t *testing.T) {
	m := newManager(t)
	term := shell(t, m, Options{})
	url := socketServer(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status := func(origin string, protocols ...string) int {
		c, resp, err := dial(ctx, url, origin, protocols...)
		if err == nil {
			c.CloseNow()
			return http.StatusSwitchingProtocols
		}
		if resp == nil {
			t.Fatalf("dial: %v", err)
		}
		return resp.StatusCode
	}
	if s := status(uiOrigin, Subprotocol); s != http.StatusUnauthorized {
		t.Fatalf("no ticket: %d", s)
	}
	if s := status(uiOrigin, Subprotocol, TicketProtocolPrefix+"bogus"); s != http.StatusUnauthorized {
		t.Fatalf("bogus ticket: %d", s)
	}
	tk, _ := m.IssueTicket(term.ID())
	if s := status("https://evil.example", Subprotocol, TicketProtocolPrefix+tk); s != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", s)
	}
	// The refused request did not consume the ticket.
	if s := status("tauri://localhost", Subprotocol, TicketProtocolPrefix+tk); s != http.StatusSwitchingProtocols {
		t.Fatalf("tauri origin: %d", s)
	}
	if s := status(uiOrigin, Subprotocol, TicketProtocolPrefix+tk); s != http.StatusUnauthorized {
		t.Fatalf("reused ticket: %d", s)
	}
	// The query parameter works too, and clients without an Origin (not browsers) are allowed.
	tk, _ = m.IssueTicket(term.ID())
	c, _, err := dial(ctx, url+"?ticket="+tk, "")
	if err != nil {
		t.Fatalf("ticket in the query: %v", err)
	}
	c.CloseNow()
}

func TestSocketIO(t *testing.T) {
	m := newManager(t)
	term := shell(t, m, Options{Cols: 80, Rows: 24})
	url := socketServer(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tk, _ := m.IssueTicket(term.ID())
	c, _, err := dial(ctx, url, uiOrigin, Subprotocol, TicketProtocolPrefix+tk)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subprotocol() != Subprotocol {
		t.Fatalf("subprotocol %q", c.Subprotocol())
	}
	// Input as binary, a resize and input as control messages.
	_ = c.Write(ctx, websocket.MessageBinary, []byte("echo bin-$((6*7))\n"))
	readSocket(t, ctx, c, "bin-42", "")
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":91,"rows":33}`))
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"stty size\n"}`))
	_, ctl := readSocket(t, ctx, c, "33 91", "")
	for _, m := range ctl {
		if m.Type == "resize" {
			t.Fatalf("the resizing viewer got its own resize back: %+v", m)
		}
	}
	if i := term.Info(); i.Cols != 91 || i.Rows != 33 {
		t.Fatalf("size: %+v", i)
	}
	c.CloseNow()

	// Reattach: the output so far is replayed; a resize by someone else is announced; the exit closes the socket.
	tk, _ = m.IssueTicket(term.ID())
	c, _, err = dial(ctx, url, uiOrigin, Subprotocol, TicketProtocolPrefix+tk)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	readSocket(t, ctx, c, "bin-42", "")
	_ = term.Resize(100, 50)
	_, ctl = readSocket(t, ctx, c, "", "resize")
	if r := ctl[len(ctl)-1]; r.Cols != 100 || r.Rows != 50 {
		t.Fatalf("resize message: %+v", r)
	}
	_ = c.Write(ctx, websocket.MessageBinary, []byte("exit 7\n"))
	_, ctl = readSocket(t, ctx, c, "", "exit")
	if e := ctl[len(ctl)-1]; e.Code == nil || *e.Code != 7 {
		t.Fatalf("exit message: %+v", e)
	}
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("after the exit: %v", err)
	}
}
