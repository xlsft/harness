package server_test

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

// runShell runs a user shell command and returns its events up to UserShellFinished.
func (h *harness) runShell(sid string, ch <-chan *agentv1.SessionEvent, command string) (*agentv1.UserShellFinished, []*agentv1.SessionEvent) {
	h.t.Helper()
	res, err := h.client.RunShellCommand(h.ctx, connect.NewRequest(&agentv1.RunShellCommandRequest{SessionId: sid, Command: command}))
	if err != nil {
		h.t.Fatal(err)
	}
	id := res.Msg.CommandId
	evs := h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetUserShellFinished().GetCommandId() == id })
	return evs[len(evs)-1].GetUserShellFinished(), evs
}

func TestUserShellCommands(t *testing.T) {
	h := startHarness(t, nil, nil)
	if _, err := h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{Environment: &agentv1.Environment{
		Folder: h.proj, Env: "SHELL_TEST_VAR=from-env\n"}})); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	// Runs in the session cwd with the session environment.
	fin, evs := h.runShell(sid, ch, `echo "var=$SHELL_TEST_VAR"; pwd`)
	started := evs[0].GetUserShellStarted()
	for _, ev := range evs {
		if s := ev.GetUserShellStarted(); s != nil {
			started = s
		}
		if ev.GetTurnStarted() != nil || ev.GetUserMessage() != nil {
			t.Fatalf("a shell command started a turn: %v", ev)
		}
	}
	if started == nil || started.Command != `echo "var=$SHELL_TEST_VAR"; pwd` || started.Cwd != h.proj {
		t.Fatalf("started: %+v", started)
	}
	if fin.Reason != agentv1.UserShellFinished_REASON_EXITED || fin.ExitCode != 0 || fin.Stdout != "var=from-env\n"+h.proj+"\n" || fin.Cwd != h.proj {
		t.Fatalf("finished: %+v", fin)
	}

	// cd moves the session shell for later commands (the agent's Bash included).
	if fin, _ = h.runShell(sid, ch, "mkdir -p sub && cd sub"); fin.ExitCode != 0 {
		t.Fatalf("cd: %+v", fin)
	}
	if fin, _ = h.runShell(sid, ch, "pwd"); fin.Stdout != filepath.Join(h.proj, "sub")+"\n" || fin.Cwd != filepath.Join(h.proj, "sub") {
		t.Fatalf("pwd after cd: %+v", fin)
	}

	// A failing command: exit code and stderr.
	fin, _ = h.runShell(sid, ch, "echo oops >&2; exit 3")
	if fin.ExitCode != 3 || fin.Stderr != "oops\n" || fin.Reason != agentv1.UserShellFinished_REASON_EXITED {
		t.Fatalf("failing: %+v", fin)
	}

	// Output streams while it runs.
	_, evs = h.runShell(sid, ch, "echo first; sleep 0.5; echo second")
	var chunks []string
	for _, ev := range evs {
		if o := ev.GetUserShellOutput(); o != nil {
			chunks = append(chunks, o.Text)
		}
	}
	if len(chunks) < 2 || chunks[0] != "first\n" || strings.Join(chunks, "") != "first\nsecond\n" {
		t.Fatalf("streamed chunks: %q", chunks)
	}

	// Large output: bounded for the transcript and for the model, head and tail kept.
	fin, _ = h.runShell(sid, ch, `echo HEAD; i=0; while [ $i -lt 4000 ]; do echo "line $i of filler text"; i=$((i+1)); done; echo TAIL`)
	if !fin.Truncated || len(fin.Stdout) > 31000 || !strings.HasPrefix(fin.Stdout, "HEAD\n") || !strings.HasSuffix(fin.Stdout, "TAIL\n") {
		t.Fatalf("large output: truncated=%v len=%d", fin.Truncated, len(fin.Stdout))
	}

	// Stop.
	res, err := h.client.RunShellCommand(h.ctx, connect.NewRequest(&agentv1.RunShellCommandRequest{SessionId: sid, Command: "sleep 30", Uuid: "sh-sleep"}))
	if err != nil || res.Msg.CommandId != "sh-sleep" {
		t.Fatalf("run: %v %+v", err, res)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetUserShellStarted().GetCommandId() == "sh-sleep" })
	// The same uuid again is a duplicate: no second command.
	if again, err := h.client.RunShellCommand(h.ctx, connect.NewRequest(&agentv1.RunShellCommandRequest{SessionId: sid, Command: "sleep 30", Uuid: "sh-sleep"})); err != nil || again.Msg.CommandId != "sh-sleep" {
		t.Fatalf("duplicate: %v", err)
	}
	start := time.Now()
	if _, err := h.client.StopShellCommand(h.ctx, connect.NewRequest(&agentv1.StopShellCommandRequest{SessionId: sid, CommandId: "sh-sleep"})); err != nil {
		t.Fatal(err)
	}
	evs = h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetUserShellFinished().GetCommandId() == "sh-sleep" })
	if fin = evs[len(evs)-1].GetUserShellFinished(); fin.Reason != agentv1.UserShellFinished_REASON_STOPPED || fin.ExitCode != -1 || time.Since(start) > 5*time.Second {
		t.Fatalf("stopped: %+v", fin)
	}
	for _, ev := range evs {
		if ev.GetUserShellStarted() != nil {
			t.Fatalf("duplicate started a second command")
		}
	}
	if _, err := h.client.StopShellCommand(h.ctx, connect.NewRequest(&agentv1.StopShellCommandRequest{SessionId: sid, CommandId: "sh-sleep"})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("stop finished command: %v", err)
	}
	if _, err := h.client.RunShellCommand(h.ctx, connect.NewRequest(&agentv1.RunShellCommandRequest{SessionId: sid, Command: "  "})); connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty command: %v", err)
	}

	// Older UIs send "!command" as a message: it runs as a shell command too.
	h.send(sid, "!echo legacy")
	evs = h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetUserShellFinished() != nil })
	if fin = evs[len(evs)-1].GetUserShellFinished(); fin.Command != "echo legacy" || fin.Stdout != "legacy\n" {
		t.Fatalf("legacy: %+v", fin)
	}
	// A command that ended by itself starts a turn when the session is idle, so the model answers it.
	if n := len(h.gw.Requests()); n == 0 {
		t.Fatal("no model request after the shell commands")
	}

	// The conversation carries every command as context, marked as the user's.
	h.send(sid, "what happened?")
	// (The message may queue behind a turn a command started.)
	reqs := h.gw.Requests()
	for !strings.Contains(allText(reqs[len(reqs)-1]), "what happened?") {
		h.until(ch, isTurnFinished)
		reqs = h.gw.Requests()
	}
	all := allText(reqs[len(reqs)-1])
	for _, want := range []string{
		`<user-shell-command cwd="` + h.proj + `">echo "var=$SHELL_TEST_VAR"; pwd</user-shell-command>`,
		"<user-shell-output exit=\"0\">\nvar=from-env\n" + h.proj + "\n</user-shell-output>",
		`<user-shell-command cwd="` + filepath.Join(h.proj, "sub") + `">echo oops >&2; exit 3</user-shell-command>`,
		"<user-shell-output exit=\"3\">\noops\n</user-shell-output>",
		"exited with code 3", "stopped it", "characters truncated", "line 3999 of filler text\nTAIL", "echo legacy",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("request lacks %q:\n%s", want, all)
		}
	}
	if strings.Index(all, "what happened?") < 0 || strings.Count(all, "<user-shell-command") != 8 {
		t.Fatalf("request:\n%s", all)
	}

	// Persisted in the transcript and replayed on resume.
	h.closeSession(sid)
	path, err := session.Store{Root: config.ProjectsDir()}.Find("", sid)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Subtype == session.SubtypeUserShell && e.UserShell != nil {
			n++
		}
	}
	if n != 8 {
		t.Fatalf("user shell entries: %d", n)
	}
	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	ch = h.subscribe(sid)
	var replayed []*agentv1.UserShellFinished
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if f := ev.GetUserShellFinished(); f != nil {
			replayed = append(replayed, f)
		}
		return isTurnFinished(ev)
	})
	if len(replayed) < 3 || replayed[0].Stdout != "var=from-env\n"+h.proj+"\n" || replayed[0].Cwd != h.proj || replayed[0].Command != `echo "var=$SHELL_TEST_VAR"; pwd` {
		t.Fatalf("replayed: %+v", replayed)
	}
	var stopped *agentv1.UserShellFinished
	for _, f := range replayed {
		if f.CommandId == "sh-sleep" {
			stopped = f
		}
	}
	if stopped == nil || stopped.Reason != agentv1.UserShellFinished_REASON_STOPPED {
		t.Fatalf("replayed stop: %+v", stopped)
	}
}

// A user shell command runs at once while a turn runs; the model sees it on the turn's next request.
func TestUserShellWhileTurnRuns(t *testing.T) {
	var mu sync.Mutex
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		mu.Lock()
		defer mu.Unlock()
		if n == 0 {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("b1", "Bash", map[string]any{"description": "test", "command": "sleep 1.5"}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("done"))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "work")
	var shellDone, toolDone bool
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetToolCallStarted() != nil:
			if _, err := h.client.RunShellCommand(h.ctx, connect.NewRequest(&agentv1.RunShellCommandRequest{SessionId: sid, Command: "echo during-turn"})); err != nil {
				t.Error(err)
			}
		case ev.GetUserShellFinished() != nil:
			shellDone = true
			if toolDone {
				t.Error("the shell command waited for the tool call")
			}
		case ev.GetToolCallFinished() != nil:
			toolDone = true
		}
		return isTurnFinished(ev)
	})
	if !shellDone {
		t.Fatal("shell command did not finish")
	}
	reqs := h.gw.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d", len(reqs))
	}
	if strings.Contains(allText(reqs[0]), "during-turn") || !strings.Contains(allText(reqs[1]), "<user-shell-output exit=\"0\">\nduring-turn\n</user-shell-output>") {
		t.Fatalf("second request:\n%s", allText(reqs[1]))
	}
	// The turn kept its tool_use / tool_result pairing.
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Content[0].Type != llm.BlockToolResult {
		t.Fatalf("last message: %+v", last.Content)
	}
}

func TestUserShellRefusedForRemoteSessions(t *testing.T) {
	h := startRemoteHarness(t, nil)
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello", "hi")
	h.listAll()
	if _, err := h.client.RunShellCommand(h.ctx, connect.NewRequest(&agentv1.RunShellCommandRequest{SessionId: remote, Command: "echo hi"})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("remote: %v", err)
	}
	if _, err := h.client.RunShellCommand(h.ctx, connect.NewRequest(&agentv1.RunShellCommandRequest{SessionId: "nope", Command: "echo hi"})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
}
