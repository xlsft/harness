package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/xlsft/harness/packages/client/browser"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
)

func TestBrowserRPCsNeedALiveSession(t *testing.T) {
	h := startHarness(t, fg.Sequence(), nil)
	if _, err := h.client.OpenBrowser(h.ctx, connect.NewRequest(&agentv1.OpenBrowserRequest{SessionId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
	if _, err := h.client.OpenBrowser(h.ctx, connect.NewRequest(&agentv1.OpenBrowserRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no session id: %v", err)
	}
	a, err := h.client.GetBrowserAvailability(h.ctx, connect.NewRequest(&agentv1.GetBrowserAvailabilityRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Msg.Availability.DownloadVersion != browser.ChromeForTestingVersion {
		t.Fatalf("availability %+v", a.Msg.Availability)
	}
}

// TestBrowserAgentToolInTurn runs a turn whose model calls browser tools: the tools come from the fallback
// definitions (the fake bundle has none), localhost needs no approval, and BrowserAction events reach subscribers.
func TestBrowserAgentToolInTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the live browser test in -short mode")
	}
	if _, ok := browser.Discover(t.TempDir()); !ok {
		t.Skip("no Chromium-based browser installed")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Agent page</title><h1>Hello agent</h1><button>Go</button>`)
	}))
	defer page.Close()

	var srv *server.Server
	var mu sync.Mutex
	var seen []string
	note := func(r *llm.Request) {
		mu.Lock()
		defer mu.Unlock()
		for _, d := range r.Tools {
			seen = append(seen, d.Name)
		}
	}
	h := startHarnessSrv(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			note(r)
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("tu_nav", "BrowserNavigate", map[string]any{"url": page.URL}))}
		},
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("tu_snap", "BrowserSnapshot", map[string]any{}))}
		},
		func(r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m3", fg.Text("done"))} },
	), nil, nil, func(s *server.Server) { srv = s })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "open the page")
	var started, finished int
	var snapshot string
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if a := ev.GetBrowserAction(); a != nil {
			switch a.Phase {
			case agentv1.BrowserAction_PHASE_STARTED:
				started++
			case agentv1.BrowserAction_PHASE_FINISHED:
				finished++
			}
		}
		if f := ev.GetToolCallFinished(); f != nil && f.ToolUseId == "tu_snap" {
			b, _ := json.Marshal(f)
			snapshot = string(b)
		}
		return isTurnFinished(ev)
	})
	if started != 2 || finished != 2 {
		t.Fatalf("actions: %d started, %d finished", started, finished)
	}
	if !strings.Contains(snapshot, "Hello agent") || !strings.Contains(snapshot, "untrusted-page-content") {
		t.Fatalf("snapshot result: %s", snapshot)
	}
	mu.Lock()
	offered := strings.Join(seen, ",")
	mu.Unlock()
	if !strings.Contains(offered, "BrowserClick") {
		t.Fatalf("the model was not offered the browser tools: %v", offered)
	}

	// The live view of the same browser, over the socket with a one-time ticket.
	open, err := h.client.OpenBrowser(h.ctx, connect.NewRequest(&agentv1.OpenBrowserRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if !open.Msg.State.Running || len(open.Msg.State.Tabs) != 1 || !strings.HasPrefix(open.Msg.State.Tabs[0].Url, page.URL) || open.Msg.Ticket == "" {
		t.Fatalf("open: %+v", open.Msg)
	}
	mux := http.NewServeMux()
	mux.Handle(browser.SocketPath, srv.Browsers.SocketHandler(nil))
	sock := httptest.NewServer(mux)
	defer sock.Close()
	ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(sock.URL, "http")+browser.SocketPath, &websocket.DialOptions{Subprotocols: []string{browser.Subprotocol, browser.TicketProtocolPrefix + open.Msg.Ticket}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 20)
	for {
		typ, _, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ == websocket.MessageBinary {
			break
		}
	}

	if _, err := h.client.CloseBrowser(h.ctx, connect.NewRequest(&agentv1.CloseBrowserRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	list, err := h.client.ListBrowserTabs(h.ctx, connect.NewRequest(&agentv1.ListBrowserTabsRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if list.Msg.State.Running {
		t.Fatalf("still running: %+v", list.Msg.State)
	}
}
