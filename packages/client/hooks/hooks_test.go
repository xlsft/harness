package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xlsft/harness/packages/client/settings"
)

func layer(t *testing.T, src settings.Source, gate settings.HookGate, cfg string) settings.HookLayer {
	t.Helper()
	return settings.HookLayer{Source: src, Raw: json.RawMessage(cfg), Hash: settings.ContentHash([]byte(cfg)), Gate: gate}
}

func project() settings.Source {
	return settings.Source{Level: settings.LevelProject, Path: "/p/.claude/settings.json"}
}

func TestMatchers(t *testing.T) {
	for _, tc := range []struct {
		matcher, value string
		want           bool
	}{
		{"", "Bash", true},
		{"*", "Bash", true},
		{"Bash", "Bash", true},
		{"Bash", "BashOutput", false},
		{"Edit|Write", "Write", true},
		{"Edit|Write", "Read", false},
		{"Notebook.*", "NotebookEdit", true},
		{"mcp__memory__.*", "mcp__memory__store", true},
		{"mcp__memory__.*", "mcp__other__store", false},
	} {
		match, err := compileMatcher(tc.matcher)
		if err != nil {
			t.Fatal(err)
		}
		if got := match(tc.value); got != tc.want {
			t.Errorf("%q on %q: %v", tc.matcher, tc.value, got)
		}
	}
	if _, err := compileMatcher("(?!x)"); err == nil {
		t.Error("an expression RE2 cannot read should be reported")
	}
}

func TestBuildKeepsWhatCannotRunWithAReason(t *testing.T) {
	st := &settings.Settings{Hooks: []settings.HookLayer{
		layer(t, project(), settings.HookGateNone, `{"PreToolUse":[{"matcher":"(?!x)","hooks":[{"type":"command","command":"a"}]},{"hooks":[{"type":"mcp_tool"},{"type":"command"},{"type":"http","url":"ftp://x"},{"type":"command","command":"b","timeout":-1}]}],"Nope":[{"hooks":[{"type":"command","command":"c"}]}],"Stop":"wrong"}`),
		layer(t, project(), settings.HookGateNone, `[]`),
	}}
	set := Build(st, nil)
	for _, h := range set.All {
		if h.Status != StatusUnsupported || h.Reason == "" {
			t.Errorf("%+v", h)
		}
	}
	if n := len(set.All); n != 8 {
		t.Fatalf("hooks: %d", n)
	}
	if got := set.Matching(PreToolUse, "Bash"); len(got) != 0 {
		t.Fatalf("matching: %+v", got)
	}
}

func TestGates(t *testing.T) {
	cfg := `{"Stop":[{"hooks":[{"type":"command","command":"a"}]}]}`
	raw := settings.ContentHash([]byte(cfg))
	st := &settings.Settings{Hooks: []settings.HookLayer{
		layer(t, project(), settings.HookGateWorkspace, cfg),
		layer(t, settings.Source{Level: settings.LevelUser, DocID: "doc_1"}, settings.HookGateDevice, cfg),
		layer(t, settings.Source{Level: settings.LevelUser, Path: "/h"}, settings.HookGateManaged, cfg),
	}}
	set := Build(st, func(string) bool { return false })
	want := []Status{StatusNeedsWorkspaceTrust, StatusNeedsDeviceTrust, StatusBlockedByPolicy}
	for i, h := range set.All {
		if h.Status != want[i] {
			t.Errorf("%d: %+v", i, h)
		}
	}
	if set.All[1].TrustHash != raw {
		t.Errorf("trust hash: %q", set.All[1].TrustHash)
	}
	if set.Has(Stop) {
		t.Error("nothing should run")
	}
	set = Build(st, func(h string) bool { return h == raw })
	if set.All[1].Status != StatusActive {
		t.Errorf("trusted: %+v", set.All[1])
	}
}

func TestTrustStore(t *testing.T) {
	s := &TrustStore{Path: filepath.Join(t.TempDir(), "sub", "hook-trust.json")}
	if s.Trusted("a") {
		t.Fatal("nothing is trusted at first")
	}
	if err := s.Set("a", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("b", true); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted("a") || !s.Trusted("b") {
		t.Fatal("trust did not stick")
	}
	if err := s.Set("a", false); err != nil {
		t.Fatal(err)
	}
	if s.Trusted("a") || !s.Trusted("b") {
		t.Fatal("trust was not taken back")
	}
	if err := os.WriteFile(s.Path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.Trusted("b") {
		t.Fatal("a broken file trusts nothing")
	}
	if err := s.Set("", true); err == nil {
		t.Fatal("an empty hash must be refused")
	}
}

// engineFor runs handlers of one event as a project file would give them.
func engineFor(t *testing.T, ev Event, commands ...string) (*Engine, *runLog) {
	t.Helper()
	groups := []map[string]any{}
	for _, c := range commands {
		groups = append(groups, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": c}}})
	}
	raw, err := json.Marshal(map[string]any{string(ev): groups})
	if err != nil {
		t.Fatal(err)
	}
	log := &runLog{}
	e := New(Build(&settings.Settings{Hooks: []settings.HookLayer{{Source: project(), Raw: raw}}}, nil), Options{OnRun: log.add, Cwd: func() string { return t.TempDir() }})
	t.Cleanup(e.Close)
	return e, log
}

type runLog struct {
	mu   sync.Mutex
	runs []Run
}

func (l *runLog) add(r Run) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.Status != RunRunning {
		l.runs = append(l.runs, r)
	}
}

func (l *runLog) finished() []Run {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Run(nil), l.runs...)
}

func TestExitCodes(t *testing.T) {
	ctx := context.Background()
	t.Run("two blocks where it can", func(t *testing.T) {
		e, log := engineFor(t, UserPromptSubmit, "echo nope >&2; exit 2")
		out := e.Dispatch(ctx, UserPromptSubmit, "", Input{})
		if !out.Blocked || out.Reason != "nope" {
			t.Fatalf("%+v", out)
		}
		if r := log.finished()[0]; r.Status != RunBlocked || r.Decision != "block" || *r.ExitCode != 2 || r.Output != "nope" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("two is a failure where it cannot", func(t *testing.T) {
		e, log := engineFor(t, SessionStart, "echo nope >&2; exit 2")
		out := e.Dispatch(ctx, SessionStart, "startup", Input{})
		if out.Blocked {
			t.Fatalf("%+v", out)
		}
		if r := log.finished()[0]; r.Status != RunError || !strings.Contains(r.Output, "nope") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("another exit code is shown and otherwise ignored", func(t *testing.T) {
		e, log := engineFor(t, PreToolUse, "echo oops >&2; exit 7")
		out := e.Dispatch(ctx, PreToolUse, "Bash", Input{})
		if out.Permission != "" || out.Blocked {
			t.Fatalf("%+v", out)
		}
		if r := log.finished()[0]; r.Status != RunError || *r.ExitCode != 7 || !strings.Contains(r.Output, "oops") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("an exit code above 200 is no success", func(t *testing.T) {
		e, log := engineFor(t, PreToolUse, "exit 201")
		e.Dispatch(ctx, PreToolUse, "Bash", Input{})
		if r := log.finished()[0]; r.Status != RunError {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("output that looks like JSON and is not", func(t *testing.T) {
		e, log := engineFor(t, UserPromptSubmit, "echo '{broken'")
		out := e.Dispatch(ctx, UserPromptSubmit, "", Input{})
		if out.Blocked || len(out.Context) != 0 {
			t.Fatalf("%+v", out)
		}
		if r := log.finished()[0]; r.Status != RunError || !strings.Contains(r.Output, "JSON") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("plain output is context only where the format says so", func(t *testing.T) {
		e, _ := engineFor(t, PostToolUse, "echo fine")
		if out := e.Dispatch(ctx, PostToolUse, "Bash", Input{}); len(out.Context) != 0 {
			t.Fatalf("%+v", out)
		}
		e, _ = engineFor(t, UserPromptSubmit, "echo fine")
		if out := e.Dispatch(ctx, UserPromptSubmit, "", Input{}); len(out.Context) != 1 || out.Context[0] != "fine" {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("output for another event is not obeyed", func(t *testing.T) {
		e, log := engineFor(t, PreToolUse, `echo '{"hookSpecificOutput":{"hookEventName":"Stop","permissionDecision":"allow"}}'`)
		if out := e.Dispatch(ctx, PreToolUse, "Bash", Input{}); out.Permission != "" {
			t.Fatalf("%+v", out)
		}
		if r := log.finished()[0]; r.Status != RunError {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("invalid UTF-8 is safe to carry", func(t *testing.T) {
		e, log := engineFor(t, PostToolUse, `printf '\377\376broken'`)
		e.Dispatch(ctx, PostToolUse, "Bash", Input{})
		if r := log.finished()[0]; !strings.Contains(r.Output, "broken") || !utf8.ValidString(r.Output) {
			t.Fatalf("%q", r.Output)
		}
	})
}

// However the handlers finish, the strictest answer wins, and the last handler's rewrite is the one that holds.
func TestCombiningIsDeterministic(t *testing.T) {
	answer := func(decision, command string) string {
		return `echo '{"hookSpecificOutput":{"permissionDecision":"` + decision + `","permissionDecisionReason":"` + decision + `","updatedInput":{"command":"` + command + `"}}}'`
	}
	ctx := context.Background()
	for range 5 {
		e, _ := engineFor(t, PreToolUse, "sleep 0.05; "+answer("allow", "first"), answer("ask", "second"), "sleep 0.02; "+answer("allow", "third"))
		out := e.Dispatch(ctx, PreToolUse, "Bash", Input{})
		if out.Permission != Ask || out.PermissionReason != "ask" || !strings.Contains(string(out.UpdatedInput), "third") {
			t.Fatalf("%+v", out)
		}
	}
	e, _ := engineFor(t, PreToolUse, answer("allow", "a"), answer("deny", "b"), answer("ask", "c"))
	if out := e.Dispatch(ctx, PreToolUse, "Bash", Input{}); out.Permission != Deny {
		t.Fatalf("%+v", out)
	}
	e, _ = engineFor(t, PreToolUse, answer("allow", "a"), "echo refused >&2; exit 2")
	if out := e.Dispatch(ctx, PreToolUse, "Bash", Input{}); out.Permission != Deny || out.PermissionReason != "refused" {
		t.Fatalf("%+v", out)
	}
	e, _ = engineFor(t, PreToolUse, `echo '{"decision":"approve","reason":"old form"}'`)
	if out := e.Dispatch(ctx, PreToolUse, "Bash", Input{}); out.Permission != Allow || out.PermissionReason != "old form" {
		t.Fatalf("%+v", out)
	}
	e, _ = engineFor(t, PreToolUse, `echo '{"hookSpecificOutput":{"updatedInput":["not an object"]}}'`)
	if out := e.Dispatch(ctx, PreToolUse, "Bash", Input{}); len(out.UpdatedInput) != 0 {
		t.Fatalf("%+v", out)
	}
	e, _ = engineFor(t, PermissionRequest, `echo '{"hookSpecificOutput":{"decision":{"behavior":"allow"}}}'`, `echo '{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"no"}}}'`)
	if out := e.Dispatch(ctx, PermissionRequest, "Bash", Input{}); out.Answer == nil || out.Answer.Allow || out.Answer.Message != "no" {
		t.Fatalf("%+v", out)
	}
}

func TestSwitches(t *testing.T) {
	raw := json.RawMessage(`{"Stop":[{"hooks":[{"type":"command","command":"echo a"}]}]}`)
	policy := settings.Source{Level: settings.LevelPolicy, DocID: "doc"}
	set := Build(&settings.Settings{Hooks: []settings.HookLayer{
		{Source: project(), Raw: raw},
		{Source: policy, Raw: json.RawMessage(`{"Stop":[{"hooks":[{"type":"command","command":"echo b"}]}]}`)},
	}}, nil)
	var off, policyOff bool
	log := &runLog{}
	e := New(set, Options{OnRun: log.add, Switches: func() (bool, bool) { return off, policyOff }})
	defer e.Close()
	ctx := context.Background()
	count := func() int {
		log.mu.Lock()
		defer log.mu.Unlock()
		n := len(log.runs)
		log.runs = nil
		return n
	}
	e.Dispatch(ctx, Stop, "", Input{})
	if n := count(); n != 2 {
		t.Fatalf("on: %d", n)
	}
	off = true
	e.Dispatch(ctx, Stop, "", Input{})
	if n := count(); n != 1 {
		t.Fatalf("user off keeps the policy's hooks: %d", n)
	}
	policyOff = true
	e.Dispatch(ctx, Stop, "", Input{})
	if n := count(); n != 0 {
		t.Fatalf("policy off stops everything: %d", n)
	}
	off, policyOff = false, false
	t.Setenv("HARNESS_NO_HOOKS", "1")
	e.Dispatch(ctx, Stop, "", Input{})
	if n := count(); n != 0 {
		t.Fatalf("HARNESS_NO_HOOKS: %d", n)
	}
}

func TestDedupeKeepsTheHighestLevel(t *testing.T) {
	raw := json.RawMessage(`{"Stop":[{"hooks":[{"type":"command","command":"same"}]}]}`)
	set := Build(&settings.Settings{Hooks: []settings.HookLayer{{Source: settings.Source{Level: settings.LevelUser}, Raw: raw}, {Source: project(), Raw: raw}}}, nil)
	got := set.Matching(Stop, "")
	if len(got) != 1 || got[0].Source.Level != settings.LevelProject {
		t.Fatalf("%+v", got)
	}
}

func TestNilEngineRunsNothing(t *testing.T) {
	var e *Engine
	if out := e.Dispatch(context.Background(), Stop, "", Input{}); out.Blocked {
		t.Fatalf("%+v", out)
	}
	e.Fire(Stop, "", Input{})
	e.End("x")
	e.Close()
	e.Replace(nil)
	if e.Set().Has(Stop) {
		t.Fatal("nothing is configured")
	}
}

func TestCloseStopsWhatRunsOnItsOwn(t *testing.T) {
	log := &runLog{}
	e := New(Build(&settings.Settings{Hooks: []settings.HookLayer{{Source: project(), Raw: json.RawMessage(`{"Notification":[{"hooks":[{"type":"command","command":"sleep 60"}]}]}`)}}}, nil), Options{OnRun: log.add})
	e.Fire(Notification, "x", Input{})
	done := make(chan struct{})
	go func() {
		e.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not end the hook")
	}
	e.Fire(Notification, "x", Input{}) // after Close: nothing starts
	if runs := log.finished(); len(runs) > 1 || (len(runs) == 1 && runs[0].Status != RunCanceled) {
		t.Fatalf("%+v", runs)
	}
}

func TestSessionEndHasAShortDefaultTimeout(t *testing.T) {
	if defaultEndTimeout >= defaultCommandTimeout {
		t.Fatal("something is closing and waits")
	}
}
