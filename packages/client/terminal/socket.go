package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	// SocketPath is where the terminal WebSocket is served, next to AgentService.
	SocketPath = "/harness.agent.v1.Terminal/socket"
	// Subprotocol is the WebSocket subprotocol the server selects; clients offer it next to "ticket.<ticket>".
	Subprotocol = "harness-terminal.v1"
	// TicketProtocolPrefix marks the Sec-WebSocket-Protocol entry carrying the ticket (browsers cannot set
	// other headers on a WebSocket).
	TicketProtocolPrefix = "ticket."

	maxInputMessage = 64 << 10
	writeTimeout    = 30 * time.Second
	pingInterval    = 30 * time.Second
)

// control is a JSON control message (text frame), in either direction:
//
//	to the UI:   {"type":"exit","code":0}  {"type":"resize","cols":80,"rows":24}  {"type":"skipped","bytes":1234}
//	from the UI: {"type":"resize","cols":80,"rows":24}  {"type":"input","data":"ls\r"}
type control struct {
	Type  string `json:"type"`
	Cols  uint16 `json:"cols,omitempty"`
	Rows  uint16 `json:"rows,omitempty"`
	Code  *int   `json:"code,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
	Data  string `json:"data,omitempty"`
}

// SocketHandler serves the terminal WebSocket. The caller has checked nothing: a request with an Origin header
// must come from allowedOrigins (exact scheme://host[:port], as for CORS), and the ticket (one-time, from
// IssueTicket) authenticates it instead of the bearer token. Binary frames from the UI are input; binary frames
// to the UI are output, starting with the buffered output; text frames are control messages.
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
		t, err := m.Redeem(requestTicket(r))
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		// The Origin is checked above (the library's same-host check would refuse the UI's own origin).
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}, InsecureSkipVerify: true})
		if err != nil {
			return
		}
		serveSocket(conn, t)
	})
}

// requestTicket reads the ticket from the Sec-WebSocket-Protocol entries or the `ticket` query parameter.
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

func serveSocket(conn *websocket.Conn, t *Terminal) {
	defer conn.CloseNow()
	conn.SetReadLimit(maxInputMessage)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v := t.Attach()
	defer v.Close()

	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				_, _ = t.Write(data)
				continue
			}
			var c control
			if json.Unmarshal(data, &c) != nil {
				continue
			}
			switch c.Type {
			case "resize":
				_ = v.Resize(c.Cols, c.Rows)
			case "input":
				_, _ = t.Write([]byte(c.Data))
			}
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
				pctx, c := context.WithTimeout(ctx, writeTimeout)
				err := conn.Ping(pctx)
				c()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	write := func(typ websocket.MessageType, b []byte) error {
		wctx, c := context.WithTimeout(ctx, writeTimeout)
		defer c()
		return conn.Write(wctx, typ, b)
	}
	writeControl := func(c control) error {
		b, _ := json.Marshal(c)
		return write(websocket.MessageText, b)
	}
	for {
		ev, err := v.Next(ctx)
		if errors.Is(err, io.EOF) {
			_ = conn.Close(websocket.StatusNormalClosure, "exited")
			return
		}
		if err != nil {
			return
		}
		switch {
		case ev.Skipped > 0:
			err = writeControl(control{Type: "skipped", Bytes: ev.Skipped})
		case ev.Resized:
			err = writeControl(control{Type: "resize", Cols: ev.Cols, Rows: ev.Rows})
		case ev.Exited:
			code := ev.ExitCode
			err = writeControl(control{Type: "exit", Code: &code})
		}
		if err == nil && len(ev.Data) > 0 {
			err = write(websocket.MessageBinary, ev.Data)
		}
		if err != nil {
			return
		}
	}
}
