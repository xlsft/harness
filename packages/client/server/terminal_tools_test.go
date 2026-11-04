//go:build unix

package server_test

import (
	"os"
	"path/filepath"
	"strings"
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

// TerminalRun has Bash's permission check: the prompt shows the command as a Bash call, a refusal types nothing, a
// Bash rule applies, and what it runs comes back in the result. The pane is asked to show the tab.
func TestTerminalRunPermissions(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	h := startHarness(t, fg.Sequence(
		terminalRun("touch denied.txt"), say("ok"),
		terminalRun("touch approved.txt"), say("ok"),
		terminalRun("touch ruled.txt"), say("ok"),
	), func(o *bootstrap.Options) { o.DisallowedTools = []string{"Bash(touch ruled.txt)"} })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	tabs := func() []*agentv1.TerminalInfo {
		l, err := h.client.ListTerminals(h.ctx, connect.NewRequest(&agentv1.ListTerminalsRequest{SessionId: sid}))
		if err != nil {
			t.Fatal(err)
		}
		return l.Msg.Terminals
	}

	if asked := h.turn(sid, ch, "one", agentv1.RespondToPermissionRequest_DECISION_DENY); len(asked) != 1 || asked[0] != "Bash" {
		t.Fatalf("asked: %v", asked)
	}
	if n := len(tabs()); n != 0 {
		t.Fatalf("a denied call opened %d tabs", n)
	}

	h.send(sid, "two")
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
	l := tabs()
	if len(l) != 1 || l[0].Title != "agent" {
		t.Fatalf("tabs: %+v", l)
	}
	if shown == nil || shown.Kind != agentv1.ShowRequested_KIND_TERMINAL || shown.TerminalId != l[0].TerminalId || shown.ToolUseId != "toolu_term" {
		t.Fatalf("show request: %+v", shown)
	}
	if _, err := os.Stat(filepath.Join(h.proj, "approved.txt")); err != nil {
		t.Fatalf("the command ran in the session's folder: %v", err)
	}
	reqs := h.gw.Requests()
	if res := lastResultText(reqs[len(reqs)-1]); !strings.Contains(res, l[0].TerminalId) {
		t.Fatalf("the result names the tab: %s", res)
	}

	// A Bash deny rule stops it before any prompt.
	if asked := h.turn(sid, ch, "three", agentv1.RespondToPermissionRequest_DECISION_ALLOW); len(asked) != 0 {
		t.Fatalf("asked: %v", asked)
	}
	reqs = h.gw.Requests()
	if res := lastResultText(reqs[len(reqs)-1]); !strings.Contains(res, "denied by rule") {
		t.Fatalf("result: %s", res)
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
	var tab string
	stop := func(*llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("toolu_stop", "TerminalStop", map[string]any{"tab_id": tab, "description": "the dev server"}))}
	}
	h := startHarness(t, fg.Sequence(stop, say("ok"), stop, say("ok")), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	ct, err := h.client.CreateTerminal(h.ctx, connect.NewRequest(&agentv1.CreateTerminalRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	tab = ct.Msg.Terminal.TerminalId
	if asked := h.turn(sid, ch, "stop it", agentv1.RespondToPermissionRequest_DECISION_DENY); len(asked) != 1 || asked[0] != "TerminalStop" {
		t.Fatalf("asked: %v", asked)
	}
	if asked := h.turn(sid, ch, "stop it", agentv1.RespondToPermissionRequest_DECISION_ALLOW); len(asked) != 1 {
		t.Fatalf("asked: %v", asked)
	}
	reqs := h.gw.Requests()
	// Nothing runs in the fresh tab, so the approved call reports that; it got past the check.
	if res := lastResultText(reqs[len(reqs)-1]); !strings.Contains(res, "nothing is running") {
		t.Fatalf("result: %s", res)
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
