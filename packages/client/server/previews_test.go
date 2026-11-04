package server_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/browser"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
)

func previewPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func writePreviewLaunch(t *testing.T, proj, command string, args []string, port int) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"servers": []map[string]any{{"name": "web", "command": command, "args": args, "port": port}}})
	if err := os.MkdirAll(filepath.Join(proj, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".harness", "launch.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPreviewServerAcrossTheSession runs the whole path with a real server: the model's PreviewStart is approved as
// the Bash command it is, the server answers, closing and resuming the session keeps it, a second start reuses it
// without asking, and deleting the session stops it.
func TestPreviewServerAcrossTheSession(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 for the test server")
	}
	port := previewPort(t)
	h := startHarness(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("tu_start", "PreviewStart", map[string]any{"name": "web"}))}
		},
		func(r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m2", fg.Text("up"))} },
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m3", fg.ToolUse("tu_again", "PreviewStart", map[string]any{"name": "web"}))}
		},
		func(r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m4", fg.Text("still up"))} },
	), nil)
	writePreviewLaunch(t, h.proj, "python3 -m http.server", []string{fmt.Sprint(port), "--bind", "127.0.0.1"}, port)
	wantCommand := fmt.Sprintf("python3 -m http.server %d --bind 127.0.0.1", port)

	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "start the dev server")
	var asked *agentv1.PermissionRequest
	var result string
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			asked = p
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId,
				Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}))
		}
		if f := ev.GetToolCallFinished(); f != nil && f.ToolUseId == "tu_start" {
			b, _ := json.Marshal(f)
			result = string(b)
		}
		return isTurnFinished(ev)
	})
	if asked == nil || asked.ToolName != "Bash" || !strings.Contains(asked.InputJson, wantCommand) || asked.ToolUseId != "tu_start" {
		t.Fatalf("permission request %+v", asked)
	}
	if !strings.Contains(result, "answers on port") {
		t.Fatalf("result %s", result)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	list := func() []*agentv1.PreviewServer {
		t.Helper()
		l, err := h.client.ListPreviewServers(h.ctx, connect.NewRequest(&agentv1.ListPreviewServersRequest{SessionId: sid}))
		if err != nil {
			t.Fatal(err)
		}
		return l.Msg.Servers
	}
	servers := list()
	if len(servers) != 1 || servers[0].Status != agentv1.PreviewServer_STATUS_RUNNING || servers[0].CommandLine != wantCommand || servers[0].Port != int32(port) {
		t.Fatalf("servers %+v", servers)
	}
	id := servers[0].ServerId

	// Closing the session leaves the server running and known.
	h.closeSession(sid)
	if s := list(); len(s) != 1 || s[0].ServerId != id || s[0].Status != agentv1.PreviewServer_STATUS_RUNNING {
		t.Fatalf("after close: %+v", s)
	}

	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if cs.Msg.Session.Id != sid {
		t.Fatalf("resumed as %s", cs.Msg.Session.Id)
	}
	ch = h.subscribe(sid)
	h.send(sid, "again")
	result, asked = "", nil
	h.until(ch, func(ev *agentv1.SessionEvent) bool { // the subscription replays the first turn first
		if p := ev.GetPermissionRequest(); p != nil && p.ToolUseId == "tu_again" {
			asked = p
		}
		if f := ev.GetToolCallFinished(); f != nil && f.ToolUseId == "tu_again" {
			b, _ := json.Marshal(f)
			result = string(b)
		}
		return isTurnFinished(ev) && result != ""
	})
	if asked != nil {
		t.Fatalf("reusing the server asked: %+v", asked)
	}
	if !strings.Contains(result, "already running") || !strings.Contains(result, id) {
		t.Fatalf("result %s", result)
	}

	logs, err := h.client.GetPreviewLogs(h.ctx, connect.NewRequest(&agentv1.GetPreviewLogsRequest{SessionId: sid, ServerId: id, Search: "GET /"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.Msg.Lines) == 0 {
		t.Fatal("no request in the logs")
	}
	if _, err := h.client.GetPreviewLogs(h.ctx, connect.NewRequest(&agentv1.GetPreviewLogsRequest{SessionId: sid, ServerId: "nope"})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("unknown server: %v", err)
	}

	if _, err := h.client.DeleteSession(h.ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		c.Close()
		t.Fatal("deleting the session left the server running")
	}
	if s := list(); len(s) != 0 {
		t.Fatalf("after delete: %+v", s)
	}
}

// The real browser opens at the server, and the site check needs no approval for its origin: only the command asks.
func TestPreviewStartOpensTheRealBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the live browser test in -short mode")
	}
	if _, ok := browser.Discover(t.TempDir()); !ok {
		t.Skip("no Chromium-based browser installed")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 for the test server")
	}
	port := previewPort(t)
	h := startHarness(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("tu_start", "PreviewStart", map[string]any{"name": "web"}))}
		},
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("tu_snap", "BrowserNavigate", map[string]any{"url": fmt.Sprintf("http://localhost:%d/index.html", port)}))}
		},
		func(r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m3", fg.Text("seen"))} },
	), nil)
	if err := os.WriteFile(filepath.Join(h.proj, "index.html"), []byte("<!doctype html><title>Preview page</title><h1>hello</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePreviewLaunch(t, h.proj, "python3 -m http.server", []string{fmt.Sprint(port), "--bind", "127.0.0.1"}, port)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	t.Cleanup(func() { // before the harness goes
		_, _ = h.client.DeleteSession(h.ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: sid}))
	})
	h.send(sid, "show the app")
	var asks []string
	var shown *agentv1.ShowRequested
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			asks = append(asks, p.ToolName)
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId,
				Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}))
		}
		if s := ev.GetShowRequested(); s != nil {
			shown = s
		}
		return isTurnFinished(ev)
	})
	if strings.Join(asks, ",") != "Bash" {
		t.Fatalf("asked for %v; the server's own origin must not need approval", asks)
	}
	if shown == nil || shown.Url != fmt.Sprintf("http://localhost:%d", port) {
		t.Fatalf("show %+v", shown)
	}
	tabs, err := h.client.ListBrowserTabs(h.ctx, connect.NewRequest(&agentv1.ListBrowserTabsRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs.Msg.State.Tabs) != 1 || !strings.Contains(tabs.Msg.State.Tabs[0].Url, fmt.Sprintf("localhost:%d/index.html", port)) || tabs.Msg.State.Tabs[0].Title != "Preview page" {
		t.Fatalf("tabs %+v", tabs.Msg.State.Tabs)
	}
	if _, err := h.client.CloseBrowser(h.ctx, connect.NewRequest(&agentv1.CloseBrowserRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
}

func TestClientExitStopsPreviewServers(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 for the test server")
	}
	port := previewPort(t)
	var srv *server.Server
	h := startHarnessSrv(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("tu_start", "PreviewStart", map[string]any{"name": "web"}))}
		},
		func(r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m2", fg.Text("up"))} },
	), nil, nil, func(s *server.Server) { srv = s })
	writePreviewLaunch(t, h.proj, "python3 -m http.server", []string{fmt.Sprint(port), "--bind", "127.0.0.1"}, port)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "start it")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId,
				Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}))
		}
		return isTurnFinished(ev)
	})
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("the server does not run: %v", err)
	}
	c.Close()
	srv.Close()
	if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		c.Close()
		t.Fatal("the client's exit left the server running")
	}
}

func TestPreviewRPCsRefuseSessionsOfOtherMachines(t *testing.T) {
	h := startRemoteHarness(t, nil)
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello", "hi")
	h.listAll()
	_, err := h.client.ListPreviewServers(h.ctx, connect.NewRequest(&agentv1.ListPreviewServersRequest{SessionId: remote}))
	if connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "not synced") {
		t.Fatalf("list: %v", err)
	}
	if _, err := h.client.StopPreviewServer(h.ctx, connect.NewRequest(&agentv1.StopPreviewServerRequest{SessionId: remote, ServerId: "x"})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("stop: %v", err)
	}
	if _, err := h.client.GetPreviewLogs(h.ctx, connect.NewRequest(&agentv1.GetPreviewLogsRequest{SessionId: remote, ServerId: "x"})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("logs: %v", err)
	}
	if _, err := h.client.ListPreviewServers(h.ctx, connect.NewRequest(&agentv1.ListPreviewServersRequest{})); connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("no id: %v", err)
	}
}
