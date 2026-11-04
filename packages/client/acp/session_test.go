package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/acp/registry"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
)

var fakeAgentBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "acp-fake-")
	if err != nil {
		panic(err)
	}
	fakeAgentBin = filepath.Join(dir, "fakeagent")
	if out, err := exec.Command("go", "build", "-o", fakeAgentBin, "./internal/fakeagent").CombinedOutput(); err != nil {
		panic("build fake agent: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// FakeAgentBinary builds nothing; it returns the binary built in TestMain (for other tests of this package).
type recorder struct {
	mu      sync.Mutex
	events  []*agentv1.SessionEvent
	deltas  []*agentv1.AssistantDelta
	changed int
	files   int
	limits  *registry.Limits
	auth    []registry.AuthState
}

func (r *recorder) Publish(ev *agentv1.SessionEvent) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) PublishEphemeral(ev *agentv1.SessionEvent) {
	r.mu.Lock()
	r.deltas = append(r.deltas, ev.GetAssistantDelta())
	r.mu.Unlock()
}

func (r *recorder) Changed()      { r.mu.Lock(); r.changed++; r.mu.Unlock() }
func (r *recorder) FilesChanged() { r.mu.Lock(); r.files++; r.mu.Unlock() }

func (r *recorder) snapshot() []*agentv1.SessionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*agentv1.SessionEvent(nil), r.events...)
}

func (r *recorder) waitFor(t *testing.T, what string, ok func([]*agentv1.SessionEvent) bool) []*agentv1.SessionEvent {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if evs := r.snapshot(); ok(evs) {
			return evs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; events: %v", what, r.snapshot())
	return nil
}

func count(evs []*agentv1.SessionEvent, pred func(*agentv1.SessionEvent) bool) int {
	n := 0
	for _, e := range evs {
		if pred(e) {
			n++
		}
	}
	return n
}

func turnsFinished(n int) func([]*agentv1.SessionEvent) bool {
	return func(evs []*agentv1.SessionEvent) bool {
		return count(evs, func(e *agentv1.SessionEvent) bool { return e.GetTurnFinished() != nil }) >= n
	}
}

func text(s string) []*llmv1.ContentBlock {
	return []*llmv1.ContentBlock{{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: s}}}}
}

func newTestSession(t *testing.T, env ...string) (*Session, *recorder, string) {
	t.Helper()
	cwd := t.TempDir()
	rec := &recorder{}
	cfg := Config{AgentID: "fake", AgentName: "Fake", AgentVersion: "1.0.0", Launch: registry.Launch{Command: fakeAgentBin},
		Cwd: cwd, Env: env, Sink: rec,
		OnAuth: func(s registry.AuthState, _ []registry.AuthMethod) {
			rec.mu.Lock()
			rec.auth = append(rec.auth, s)
			rec.mu.Unlock()
		},
		OnLimits: func(l *registry.Limits) { rec.mu.Lock(); rec.limits = l; rec.mu.Unlock() }}
	s := New(cfg)
	t.Cleanup(s.Close)
	return s, rec, cwd
}

func TestPromptStreamsTextAndThinking(t *testing.T) {
	s, rec, _ := newTestSession(t)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.Status != agentv1.SessionStatus_SESSION_STATUS_IDLE || st.CurrentMode != "default" || len(st.Modes) != 3 {
		t.Fatalf("state after open: %+v", st)
	}
	if _, err := s.Submit("u1", text("hello"), false); err != nil {
		t.Fatal(err)
	}
	evs := rec.waitFor(t, "turn end", turnsFinished(1))
	var kinds []string
	for _, e := range evs {
		switch {
		case e.GetUserMessage() != nil:
			kinds = append(kinds, "user")
		case e.GetTurnStarted() != nil:
			kinds = append(kinds, "turn_started")
		case e.GetAssistantMessage() != nil:
			kinds = append(kinds, "assistant")
			m := e.GetAssistantMessage()
			if len(m.Content) != 2 || m.Content[0].GetThinking().GetThinking() != "Thinking it over." || m.Content[1].GetText().GetText() != "Hello world" {
				t.Fatalf("assistant message: %v", m)
			}
		case e.GetTurnFinished() != nil:
			kinds = append(kinds, "turn_finished")
			f := e.GetTurnFinished()
			if f.Result != agentv1.TurnFinished_RESULT_SUCCESS || f.Usage.GetInputTokens() != 100 || f.CostUsd != 0 {
				t.Fatalf("turn finished: %v", f)
			}
		}
	}
	if strings.Join(kinds, ",") != "user,turn_started,assistant,turn_finished" {
		t.Fatalf("event order: %v", kinds)
	}
	rec.mu.Lock()
	nd := len(rec.deltas)
	rec.mu.Unlock()
	if nd != 3 {
		t.Fatalf("deltas = %d, want 3", nd)
	}
	if st := s.State(); len(st.Commands) != 1 || st.Commands[0].Name != "review" {
		t.Fatalf("commands: %+v", st.Commands)
	}
}

func TestToolCallPermissionTerminalAndWrite(t *testing.T) {
	s, rec, cwd := newTestSession(t)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Submit("u1", text("tool please"), false)
	evs := rec.waitFor(t, "permission request", func(evs []*agentv1.SessionEvent) bool {
		return count(evs, func(e *agentv1.SessionEvent) bool { return e.GetPermissionRequest() != nil }) == 1
	})
	var req *agentv1.PermissionRequest
	var started *agentv1.ToolCallStarted
	for _, e := range evs {
		if p := e.GetPermissionRequest(); p != nil {
			req = p
		}
		if p := e.GetToolCallStarted(); p != nil && started == nil {
			started = p
		}
	}
	if started == nil || started.ToolUseId != "call_1" || started.Name != "Bash" || !strings.Contains(started.InputJson, `"command":"echo hi"`) {
		t.Fatalf("tool started: %v", started)
	}
	if req.ToolUseId != "call_1" || req.ToolName != "Bash" || req.OneTimeOnly {
		t.Fatalf("permission request: %v", req)
	}
	if st := s.State(); st.Status != agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT {
		t.Fatalf("status while asking: %v", st.Status)
	}
	if err := s.RespondPermission(req.RequestId, agentv1.RespondToPermissionRequest_DECISION_ALLOW, ""); err != nil {
		t.Fatal(err)
	}
	evs = rec.waitFor(t, "turn end", turnsFinished(1))
	finished := map[string]*agentv1.ToolCallFinished{}
	var todos *agentv1.TodosUpdated
	resolved := 0
	for _, e := range evs {
		if f := e.GetToolCallFinished(); f != nil {
			finished[f.ToolUseId] = f
		}
		if x := e.GetTodosUpdated(); x != nil {
			todos = x
		}
		if e.GetPermissionResolved() != nil {
			resolved++
		}
	}
	if resolved != 1 {
		t.Fatalf("permission resolved %d times", resolved)
	}
	bash := finished["call_1"]
	if bash == nil || bash.IsError || !strings.Contains(bash.Content[0].GetText().GetText(), "hi") || bash.Summary != "exit 0" {
		t.Fatalf("bash result: %v", bash)
	}
	edit := finished["call_2"]
	if edit == nil {
		t.Fatal("edit not finished")
	}
	var structured map[string]any
	if err := json.Unmarshal([]byte(edit.StructuredResultJson), &structured); err != nil || structured["type"] != "create" || structured["filePath"] != filepath.Join(cwd, "out.txt") {
		t.Fatalf("edit structured result: %s (%v)", edit.StructuredResultJson, err)
	}
	if b, err := os.ReadFile(filepath.Join(cwd, "out.txt")); err != nil || string(b) != "one\ntwo\n" {
		t.Fatalf("file: %q %v", b, err)
	}
	if todos == nil || len(todos.Todos) != 2 || todos.Todos[1].Status != agentv1.Todo_STATUS_IN_PROGRESS {
		t.Fatalf("todos: %v", todos)
	}
	rec.mu.Lock()
	files := rec.files
	rec.mu.Unlock()
	if files < 2 {
		t.Fatalf("files changed %d times", files)
	}
	// The tool calls are announced in assistant messages as tool_use blocks.
	toolUses := 0
	for _, e := range evs {
		for _, b := range e.GetAssistantMessage().GetContent() {
			if b.GetToolUse() != nil {
				toolUses++
			}
		}
	}
	if toolUses != 2 {
		t.Fatalf("tool_use blocks = %d", toolUses)
	}
}

func TestPermissionReject(t *testing.T) {
	s, rec, _ := newTestSession(t)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Submit("u1", text("tool"), false)
	evs := rec.waitFor(t, "permission request", func(evs []*agentv1.SessionEvent) bool {
		return count(evs, func(e *agentv1.SessionEvent) bool { return e.GetPermissionRequest() != nil }) == 1
	})
	var rid string
	for _, e := range evs {
		if p := e.GetPermissionRequest(); p != nil {
			rid = p.RequestId
		}
	}
	s.RespondPermission(rid, agentv1.RespondToPermissionRequest_DECISION_DENY, "")
	evs = rec.waitFor(t, "turn end", turnsFinished(1))
	for _, e := range evs {
		if f := e.GetToolCallFinished(); f != nil && (!f.IsError || f.ToolUseId != "call_1") {
			t.Fatalf("rejected tool: %v", f)
		}
	}
}

func TestCancelInterruptsTurn(t *testing.T) {
	s, rec, _ := newTestSession(t)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Submit("u1", text("slow"), false)
	rec.waitFor(t, "turn start", func(evs []*agentv1.SessionEvent) bool {
		return count(evs, func(e *agentv1.SessionEvent) bool { return e.GetTurnStarted() != nil }) == 1
	})
	// A message sent meanwhile is queued.
	if q, _ := s.Submit("u2", text("hello"), false); !q {
		t.Fatal("second message not queued")
	}
	time.Sleep(50 * time.Millisecond)
	if idle := s.Interrupt(); idle {
		t.Fatal("interrupt reported idle")
	}
	evs := rec.waitFor(t, "two turns", turnsFinished(2))
	var results []agentv1.TurnFinished_Result
	for _, e := range evs {
		if f := e.GetTurnFinished(); f != nil {
			results = append(results, f.Result)
		}
	}
	if results[0] != agentv1.TurnFinished_RESULT_INTERRUPTED || results[1] != agentv1.TurnFinished_RESULT_SUCCESS {
		t.Fatalf("results: %v", results)
	}
	if count(evs, func(e *agentv1.SessionEvent) bool { return e.GetQueueChanged() != nil }) < 2 {
		t.Fatal("queue changes missing")
	}
}

func TestAuthRequiredThenAgentAuth(t *testing.T) {
	s, rec, _ := newTestSession(t, "FAKE_AUTH=1")
	err := s.Open(context.Background())
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("open: %v", err)
	}
	st := s.State()
	if !st.NeedsAuth || len(st.AuthMethods) != 2 || st.AuthMethods[1].Kind != "terminal" || st.Status != agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT {
		t.Fatalf("state: %+v", st)
	}
	if _, err := s.Submit("u1", text("hello"), false); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("submit while signed out: %v", err)
	}
	if err := s.Authenticate(context.Background(), "login"); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.NeedsAuth {
		t.Fatal("still needs auth")
	}
	s.Submit("u1", text("hello"), false)
	rec.waitFor(t, "turn end", turnsFinished(1))
	rec.mu.Lock()
	auth := append([]registry.AuthState(nil), rec.auth...)
	rec.mu.Unlock()
	if len(auth) < 3 || auth[0] != registry.AuthRequired || auth[1] != registry.AuthAuthenticated {
		t.Fatalf("auth states: %v", auth)
	}
}

func TestTerminalAuthThenReopen(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "authed")
	s, _, _ := newTestSession(t, "FAKE_AUTH=1", "FAKE_AUTH_FILE="+flag)
	if err := s.Open(context.Background()); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("open: %v", err)
	}
	l, err := s.TerminalLogin("tlogin")
	if err != nil || l.Command != fakeAgentBin || strings.Join(l.Args, " ") != "--login" {
		t.Fatalf("terminal login: %+v %v", l, err)
	}
	// The user runs it in a terminal.
	cmd := exec.Command(l.Command, l.Args...)
	cmd.Env = append(os.Environ(), "FAKE_AUTH_FILE="+flag)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("login: %v %s", err, out)
	}
	if err := s.Reopen(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSessionSuppressesReplay(t *testing.T) {
	cwd := t.TempDir()
	rec := &recorder{}
	s := New(Config{AgentID: "fake", AgentName: "Fake", Launch: registry.Launch{Command: fakeAgentBin}, Cwd: cwd,
		Env: []string{"FAKE_LOAD=1"}, Sink: rec, ResumeAgentSessionID: "fake-7"})
	defer s.Close()
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.AgentSessionID != "fake-7" || st.Fresh {
		t.Fatalf("state: %+v", st)
	}
	time.Sleep(50 * time.Millisecond)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.deltas) != 0 || len(rec.events) != 0 {
		t.Fatalf("replay leaked: %v %v", rec.events, rec.deltas)
	}
}

func TestResumeWithoutLoadStartsFresh(t *testing.T) {
	rec := &recorder{}
	s := New(Config{AgentID: "fake", AgentName: "Fake", Launch: registry.Launch{Command: fakeAgentBin}, Cwd: t.TempDir(), Sink: rec, ResumeAgentSessionID: "fake-7"})
	defer s.Close()
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); !st.Fresh || st.AgentSessionID == "fake-7" {
		t.Fatalf("state: %+v", st)
	}
	if count(rec.snapshot(), func(e *agentv1.SessionEvent) bool { return e.GetNotice().GetKind() == "acp_fresh_session" }) != 1 {
		t.Fatal("no fresh-session notice")
	}
}

func TestModeUpdateAndSetMode(t *testing.T) {
	s, rec, _ := newTestSession(t)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Submit("u1", text("mode"), false)
	evs := rec.waitFor(t, "turn end", turnsFinished(1))
	if count(evs, func(e *agentv1.SessionEvent) bool {
		return e.GetPermissionModeChanged().GetMode() == agentv1.PermissionMode_PERMISSION_MODE_PLAN
	}) != 1 {
		t.Fatal("no plan mode change")
	}
	if err := s.SetPermissionMode(context.Background(), agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.CurrentMode != "acceptEdits" {
		t.Fatalf("mode: %s", st.CurrentMode)
	}
	if err := s.SetPermissionMode(context.Background(), agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS); err == nil {
		t.Fatal("bypass should not map")
	}
}

func TestLimitsFromExtensionNotification(t *testing.T) {
	s, rec, _ := newTestSession(t)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Submit("u1", text("limits"), false)
	rec.waitFor(t, "turn end", turnsFinished(1))
	st := s.State()
	if st.Limits == nil || len(st.Limits.Windows) != 2 || st.Limits.Windows[0].Label != "5h" || st.Limits.Windows[0].UsedPercent != 42 {
		t.Fatalf("limits: %+v", st.Limits)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.limits == nil || rec.limits.Windows[1].Label != "weekly" {
		t.Fatalf("recorded limits: %+v", rec.limits)
	}
}

func TestStartFailureReportsError(t *testing.T) {
	rec := &recorder{}
	s := New(Config{AgentID: "x", AgentName: "X", Launch: registry.Launch{Command: "/bin/false"}, Cwd: t.TempDir(), Sink: rec})
	defer s.Close()
	if err := s.Open(context.Background()); err == nil {
		t.Fatal("open succeeded")
	}
	if st := s.State(); st.Status != agentv1.SessionStatus_SESSION_STATUS_ERROR || st.StartError == "" {
		t.Fatalf("state: %+v", st)
	}
	if _, err := s.Submit("u", text("hi"), false); err == nil {
		t.Fatal("submit to a dead agent succeeded")
	}
}

func TestUsageLimitPassthrough(t *testing.T) {
	s, rec, _ := newTestSession(t)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	usage := func(evs []*agentv1.SessionEvent) []*agentv1.UsageLimit {
		var out []*agentv1.UsageLimit
		for _, e := range evs {
			if u := e.GetUsageLimit(); u != nil {
				out = append(out, u)
			}
		}
		return out
	}
	// Under the thresholds: nothing.
	s.Submit("u1", text("limits"), false)
	if got := usage(rec.waitFor(t, "turn 1", turnsFinished(1))); len(got) != 0 {
		t.Fatalf("usage events under the thresholds: %v", got)
	}
	// The agent reports 98% of its 5h window: WRAP_UP with the agent's numbers, and no reminder is sent to it.
	s.Submit("u2", text("nearlimit"), false)
	got := usage(rec.waitFor(t, "turn 2", turnsFinished(2)))
	if len(got) != 1 || got[0].GetState() != agentv1.UsageLimit_STATE_WRAP_UP || got[0].GetProviderId() != "fake" || got[0].GetProviderName() != "Fake" ||
		got[0].GetWindowLabel() != "5h" || got[0].GetUsedPercent() != 98 || got[0].GetResetsAt() == "" || got[0].GetWrappedUp() {
		t.Fatalf("usage events: %v", got)
	}
	if st := s.State(); st.UsageLimit.GetState() != agentv1.UsageLimit_STATE_WRAP_UP {
		t.Fatalf("state: %v", st.UsageLimit)
	}
	// The agent refuses the prompt for its usage limit: REACHED, before the turn's TurnFinished.
	s.Submit("u3", text("ratelimited"), false)
	evs := rec.waitFor(t, "turn 3", turnsFinished(3))
	got = usage(evs)
	if len(got) != 2 || got[1].GetState() != agentv1.UsageLimit_STATE_REACHED || got[1].GetWindowLabel() != "5h" {
		t.Fatalf("usage events: %v", got)
	}
	if last := evs[len(evs)-1]; last.GetTurnFinished() == nil {
		t.Fatalf("REACHED must come before TurnFinished: %v", last)
	}
}

func TestEffortOption(t *testing.T) {
	file := filepath.Join(t.TempDir(), "efforts")
	if err := os.WriteFile(file, []byte("think=Think\nultra=Ultrathink\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _, _ := newTestSession(t, "FAKE_EFFORTS_FILE="+file)
	if err := s.SetEffort(context.Background(), "ultra"); err != nil {
		t.Fatal(err) // Not open yet: waits for the agent.
	}
	if st := s.State(); st.CurrentEffort != "ultra" {
		t.Fatalf("state: %+v", st)
	}
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.CurrentEffort != "ultra" || len(st.Efforts) != 2 || st.Efforts[1] != (Effort{ID: "ultra", Name: "Ultrathink"}) {
		t.Fatalf("state: %+v", st)
	}
	// Ids are the agent's: the names harness has for its levels are not levels here.
	if err := s.SetEffort(context.Background(), "medium"); err == nil || !strings.Contains(err.Error(), `no effort level "medium"`) {
		t.Fatalf("set: %v", err)
	}
	if err := s.SetEffort(context.Background(), "think"); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.CurrentEffort != "think" {
		t.Fatalf("state: %+v", st)
	}
	// A report without any options says nothing about them.
	s.mu.Lock()
	s.applyConfigLocked(nil)
	kept := s.effortOpt != ""
	s.mu.Unlock()
	if !kept {
		t.Fatal("no report took the option away")
	}
}

func TestEffortNoLongerOfferedFallsBackWithANotice(t *testing.T) {
	file := filepath.Join(t.TempDir(), "efforts")
	if err := os.WriteFile(file, []byte("think=Think\ndeep=Deep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	s := New(Config{AgentID: "fake", AgentName: "Fake", Launch: registry.Launch{Command: fakeAgentBin}, Cwd: t.TempDir(),
		Env: []string{"FAKE_EFFORTS_FILE=" + file}, Sink: rec, InitialEffort: "ultra"})
	defer s.Close()
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.CurrentEffort != "think" {
		t.Fatalf("state: %+v", st)
	}
	rec.waitFor(t, "the notice", func(evs []*agentv1.SessionEvent) bool {
		return count(evs, func(e *agentv1.SessionEvent) bool { return e.GetNotice().GetKind() == "acp_effort_unavailable" }) == 1
	})
}

func TestAgentWithoutEffortOptionRefuses(t *testing.T) {
	s, _, _ := newTestSession(t)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); len(st.Efforts) != 0 || st.CurrentEffort != "" {
		t.Fatalf("state: %+v", st)
	}
	if err := s.SetEffort(context.Background(), "think"); err == nil || !strings.Contains(err.Error(), "does not offer effort levels") {
		t.Fatalf("set: %v", err)
	}
	if !slices.Contains(UnsupportedFeatures(false), agentv1.SessionFeature_SESSION_FEATURE_EFFORT) || slices.Contains(UnsupportedFeatures(true), agentv1.SessionFeature_SESSION_FEATURE_EFFORT) {
		t.Fatal("features")
	}
}
