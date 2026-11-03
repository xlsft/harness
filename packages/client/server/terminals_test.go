//go:build unix

package server_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/terminal"
)

func TestSessionTerminals(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	h := startHarness(t, fg.Sequence(), nil)
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	sub := filepath.Join(h.proj, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// The session's cwd by default, a directory inside it, nothing outside.
	ct, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{SessionId: sid, Cols: 90, Rows: 20}))
	if err != nil {
		t.Fatal(err)
	}
	info := ct.Msg.Terminal
	if info.Cwd != h.proj || info.SessionId != sid || !info.Running || info.Pid <= 0 || info.Shell != "/bin/sh" || info.Cols != 90 || info.ExitCode != nil || ct.Msg.Ticket == "" {
		t.Fatalf("terminal: %+v ticket %q", info, ct.Msg.Ticket)
	}
	if _, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{SessionId: sid, Cwd: sub, Title: "build"})); err != nil {
		t.Fatalf("subdirectory: %v", err)
	}
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	if _, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{SessionId: sid, Cwd: outside})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("outside the working directories: %v", err)
	}
	if _, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{SessionId: sid, Cwd: sub + "/../.."})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("escaping with ..: %v", err)
	}
	if _, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{SessionId: "00000000-0000-0000-0000-000000000000"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
	if _, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{Cwd: "relative/dir"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("relative cwd: %v", err)
	}
	if _, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{Env: map[string]string{"A=B": "x"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad env: %v", err)
	}
	// Standalone: any directory.
	alone, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{Cwd: outside}))
	if err != nil || alone.Msg.Terminal.SessionId != "" || alone.Msg.Terminal.Title != filepath.Base(outside) {
		t.Fatalf("standalone: %v %+v", err, alone)
	}

	list := func(session string) []*agentv1.TerminalInfo {
		resp, err := h.client.ListTerminals(h.ctx, connect.NewRequest(&agentv1.ListTerminalsRequest{SessionId: session}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Terminals
	}
	if l := list(sid); len(l) != 2 || l[1].Title != "build" || len(list("")) != 3 {
		t.Fatalf("list: %+v", l)
	}

	if _, err := h.client.ResizeTerminal(h.ctx, connect.NewRequest(&agentv1.ResizeTerminalRequest{TerminalId: info.TerminalId, Cols: 120, Rows: 40})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.ResizeTerminal(h.ctx, connect.NewRequest(&agentv1.ResizeTerminalRequest{TerminalId: info.TerminalId})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("zero size: %v", err)
	}
	at, err := h.client.AttachTerminal(h.ctx, connect.NewRequest(&agentv1.AttachTerminalRequest{TerminalId: info.TerminalId}))
	if err != nil || at.Msg.Ticket == "" || at.Msg.Ticket == ct.Msg.Ticket || at.Msg.Terminal.Cols != 120 {
		t.Fatalf("attach: %v %+v", err, at)
	}
	if _, err := h.client.AttachTerminal(h.ctx, connect.NewRequest(&agentv1.AttachTerminalRequest{TerminalId: "term_nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("attach unknown: %v", err)
	}

	if _, err := h.client.KillTerminal(h.ctx, connect.NewRequest(&agentv1.KillTerminalRequest{TerminalId: info.TerminalId})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.KillTerminal(h.ctx, connect.NewRequest(&agentv1.KillTerminalRequest{TerminalId: info.TerminalId})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("second kill: %v", err)
	}
	// Closing the session ends its terminals; standalone ones stay.
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if l := list(""); len(l) != 1 || l[0].TerminalId != alone.Msg.Terminal.TerminalId {
		t.Fatalf("after closing the session: %+v", l)
	}
}

func TestTerminalsRefusedForRemoteSessions(t *testing.T) {
	h := startRemoteHarness(t, nil)
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello", "hi")
	h.listAll() // the client learns about the remote session from the server's listing
	_, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{SessionId: remote}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "only available for sessions of this machine") {
		t.Fatalf("remote session: %v", err)
	}
}

// The socket over the TCP listener: the bearer token guards the RPCs, the ticket the socket; Origin is checked
// for both.
func TestTerminalSocketOverTCP(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	srv := server.New(bootstrap.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addrCh := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- srv.ServeTCP(ctx, server.TCPOptions{
			Addr: "127.0.0.1:0", Token: testToken, AllowedOrigins: append(server.DefaultCORSOrigins, "tauri://localhost"),
			OnListen: func(a net.Addr) { addrCh <- a },
		})
	}()
	var addr string
	select {
	case a := <-addrCh:
		addr = a.String()
	case err := <-done:
		t.Fatalf("serve: %v", err)
	}
	anon := agentv1connect.NewAgentServiceClient(http.DefaultClient, "http://"+addr, connect.WithProtoJSON())
	if _, err := anon.CreateTerminal(ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous CreateTerminal: %v", err)
	}
	client := browserClient("http://"+addr, func(p *http.Protocols) { p.SetHTTP1(true) })
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	ct, err := client.CreateTerminal(ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{Cwd: dir}))
	if err != nil {
		t.Fatal(err)
	}

	url := "ws://" + addr + terminal.SocketPath
	dial := func(origin, ticket string) (*websocket.Conn, int) {
		h := http.Header{"Origin": {origin}}
		c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{terminal.Subprotocol, terminal.TicketProtocolPrefix + ticket}})
		if err != nil {
			if resp == nil {
				t.Fatalf("dial: %v", err)
			}
			return nil, resp.StatusCode
		}
		return c, http.StatusSwitchingProtocols
	}
	if _, code := dial("https://evil.example", ct.Msg.Ticket); code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", code)
	}
	if _, code := dial(testOrigin, "forged"); code != http.StatusUnauthorized {
		t.Fatalf("forged ticket: %d", code)
	}
	c, code := dial("tauri://localhost", ct.Msg.Ticket)
	if c == nil {
		t.Fatalf("tauri origin with a ticket: %d", code)
	}
	defer c.CloseNow()
	_ = c.Write(ctx, websocket.MessageBinary, []byte("echo over-$((20+3))\n"))
	var out strings.Builder
	for !strings.Contains(out.String(), "over-23") {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (output %q)", err, out.String())
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
		}
	}
	if _, code := dial(testOrigin, ct.Msg.Ticket); code != http.StatusUnauthorized {
		t.Fatalf("reused ticket: %d", code)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
