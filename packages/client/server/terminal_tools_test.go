//go:build unix

package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

func terminalRun(command string) func(*llm.Request) fg.Reply {
	return func(*llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("toolu_term", "TerminalRun", map[string]any{"command": command, "title": "agent", "description": "a command"}))}
	}
}

func say(text string) func(*llm.Request) fg.Reply {
	return func(*llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text(text))} }
}

// byMessage answers a user message that holds a key with its tool call, and everything else with text: a denial also
// sends the interrupt reply request at its own moment, so the requests cannot be scripted by their order.
func byMessage(calls map[string]fg.BlockSpec) fg.Responder {
	return func(n int, r *llm.Request) fg.Reply {
		last := r.Messages[len(r.Messages)-1]
		text := allText(&llm.Request{Messages: []llm.Message{last}})
		if last.Content[0].Type != llm.BlockToolResult && !strings.Contains(text, "The user stopped you") {
			for key, call := range calls {
				if strings.Contains(text, key) {
					return fg.Reply{Events: fg.Message("m", call)}
				}
			}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}
}

func anyRequestHolds(h *harness, text string) bool {
	for _, r := range h.gw.Requests() {
		if strings.Contains(allText(r), text) {
			return true
		}
	}
	return false
}

func terminalRunCall(command string) fg.BlockSpec {
	return fg.ToolUse("toolu_term", "TerminalRun", map[string]any{"command": command, "title": "agent", "description": "a command"})
}

// TerminalRun has Bash's permission check: the prompt shows the command as a Bash call, a refusal types nothing, a
// Bash rule applies, and what it runs comes back in the result. The pane is asked to show the tab.
func TestTerminalRunPermissions(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	h := startHarness(t, byMessage(map[string]fg.BlockSpec{
		"run-denied":   terminalRunCall("touch denied.txt"),
		"run-approved": terminalRunCall("touch approved.txt"),
		"run-ruled":    terminalRunCall("touch ruled.txt"),
	}), func(o *bootstrap.Options) { o.DisallowedTools = []string{"Bash(touch ruled.txt)"} })
	tabsOf := func(id string) []*agentv1.TerminalInfo {
		l, err := h.client.ListTerminals(h.ctx, connect.NewRequest(&agentv1.ListTerminalsRequest{SessionId: id}))
		if err != nil {
			t.Fatal(err)
		}
		return l.Msg.Terminals
	}

	// A denial in a session of its own: its interrupt reply must not meet the next message.
	denied, deniedCh := h.create(&agentv1.CreateSessionRequest{})
	if asked := h.turn(denied, deniedCh, "run-denied", agentv1.RespondToPermissionRequest_DECISION_DENY); len(asked) != 1 || asked[0] != "Bash" {
		t.Fatalf("asked: %v", asked)
	}
	if n := len(tabsOf(denied)); n != 0 {
		t.Fatalf("a denied call opened %d tabs", n)
	}

	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "run-approved")
	var shown *agentv1.ShowRequested
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId, Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}))
		}
		if s := ev.GetShowRequested(); s != nil {
			shown = s
		}
		return isTurnFinished(ev)
	})
	l := tabsOf(sid)
	if len(l) != 1 || l[0].Title != "agent" {
		t.Fatalf("tabs: %+v", l)
	}
	if shown == nil || shown.Kind != agentv1.ShowRequested_KIND_TERMINAL || shown.TerminalId != l[0].TerminalId || shown.ToolUseId != "toolu_term" {
		t.Fatalf("show request: %+v", shown)
	}
	if _, err := os.Stat(filepath.Join(h.proj, "approved.txt")); err != nil {
		t.Fatalf("the command ran in the session's folder: %v", err)
	}
	if !anyRequestHolds(h, l[0].TerminalId) {
		t.Fatal("no result names the tab")
	}

	// A Bash deny rule stops it before any prompt.
	if asked := h.turn(sid, ch, "run-ruled", agentv1.RespondToPermissionRequest_DECISION_ALLOW); len(asked) != 0 {
		t.Fatalf("asked: %v", asked)
	}
	if !anyRequestHolds(h, "denied by rule") {
		t.Fatal("no result says the rule denied it")
	}
	if _, err := os.Stat(filepath.Join(h.proj, "ruled.txt")); err == nil {
		t.Fatal("a denied command ran")
	}
}

// A command that outlives the tool call: its end starts a turn in the idle session, with a notice in the request.
func TestTerminalRunWakesTheSession(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	h := startHarness(t, fg.Sequence(terminalRun("sleep 3; echo finished-marker"), say("started"), say("it finished")), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.turn(sid, ch, "go", agentv1.RespondToPermissionRequest_DECISION_ALLOW)
	reqs := h.gw.Requests()
	if res := lastResultText(reqs[len(reqs)-1]); !strings.Contains(res, "still running") {
		t.Fatalf("result: %s", res)
	}
	// No message from anyone: the end of the command starts the next turn.
	h.until(ch, isTurnFinished)
	reqs = h.gw.Requests()
	all := allText(reqs[len(reqs)-1])
	if !strings.Contains(all, "finished-marker") || !strings.Contains(all, "sleep 3") || !strings.Contains(all, "untrusted-terminal-output") {
		t.Fatalf("the notice: %s", all)
	}
	// Once only.
	time.Sleep(time.Second)
	if n := len(h.gw.Requests()); n != len(reqs) {
		t.Fatalf("%d requests after the notice, had %d", n, len(reqs))
	}
}

func TestTerminalToolsRefusedForRemoteSessions(t *testing.T) {
	fake := synctest.New()
	ts := fake.Start()
	t.Cleanup(ts.Close)
	api := syncclient.NewClient(ts.URL, nil, nil)
	var srv *server.Server
	h := startHarnessSrv(t, fg.Sequence(), nil, nil, func(s *server.Server) {
		srv = s
		s.Sync, s.DeviceID = api, ownDevice
	})
	rh := &remoteHarness{harness: h, fake: fake, api: api}
	remote := rh.seedRemote(otherDevice, "/remote/proj", "hello", "hi")
	rh.listAll()
	if err := srv.RefuseTerminals(remote); err == nil || !strings.Contains(err.Error(), "only available for sessions of this machine") {
		t.Fatalf("remote session: %v", err)
	}
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	if err := srv.RefuseTerminals(sid); err != nil {
		t.Fatalf("a session of this machine: %v", err)
	}
}

// TerminalStop asks before it touches a tab of the user's, also in bypass mode; a refusal leaves the command running.
func TestTerminalStopAsksForTheUsersTab(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	var mu sync.Mutex
	tabs := map[string]string{}
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		mu.Lock()
		defer mu.Unlock()
		calls := map[string]fg.BlockSpec{}
		for key, tab := range tabs {
			calls[key] = fg.ToolUse("toolu_stop", "TerminalStop", map[string]any{"tab_id": tab, "description": "the dev server"})
		}
		return byMessage(calls)(n, r)
	}, nil)
	// Each answer in a session of its own: a denied turn's interrupt reply must not meet the next message.
	for key, d := range map[string]agentv1.RespondToPermissionRequest_Decision{
		"stop-denied": agentv1.RespondToPermissionRequest_DECISION_DENY, "stop-allowed": agentv1.RespondToPermissionRequest_DECISION_ALLOW,
	} {
		sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
		ct, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{SessionId: sid}))
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		tabs[key] = ct.Msg.Terminal.TerminalId
		mu.Unlock()
		if asked := h.turn(sid, ch, key, d); len(asked) != 1 || asked[0] != "TerminalStop" {
			t.Fatalf("%s: asked: %v", key, asked)
		}
	}
	// Nothing runs in the fresh tab, so the approved call reports that; it got past the check.
	if !anyRequestHolds(h, "nothing is running") {
		t.Fatal("the approved call did not run")
	}
}

// A resumed session gets the tools back. The tabs of before are gone with the old session, and the tool says what to do.
func TestTerminalToolsAfterResume(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	var old string
	read := func(*llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("toolu_read", "TerminalRead", map[string]any{"tab_id": old, "description": "the old tab"}))}
	}
	h := startHarness(t, fg.Sequence(terminalRun("echo hi"), say("ok"), read, say("ok"), terminalRun("echo again-$((1+1))"), say("ok")), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.turn(sid, ch, "go", agentv1.RespondToPermissionRequest_DECISION_ALLOW)
	l, err := h.client.ListTerminals(h.ctx, connect.NewRequest(&agentv1.ListTerminalsRequest{SessionId: sid}))
	if err != nil || len(l.Msg.Terminals) != 1 {
		t.Fatalf("tabs: %+v, %v", l, err)
	}
	old = l.Msg.Terminals[0].TerminalId
	h.closeSession(sid)

	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	ch = h.subscribe(sid)
	// Until that many model requests have been answered (the stream starts with a replay of the old turn's end).
	finish := func(requests int) string {
		h.until(ch, func(ev *agentv1.SessionEvent) bool {
			if p := ev.GetPermissionRequest(); p != nil {
				h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId, Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}))
			}
			return isTurnFinished(ev) && len(h.gw.Requests()) >= requests
		})
		reqs := h.gw.Requests()
		return lastResultText(reqs[len(reqs)-1])
	}
	h.send(sid, "read it")
	if res := finish(4); !strings.Contains(res, "TerminalList") {
		t.Fatalf("the old tab: %s", res)
	}
	h.send(sid, "run it")
	if res := finish(6); !strings.Contains(res, "again-2") {
		t.Fatalf("after the resume: %s", res)
	}
}
