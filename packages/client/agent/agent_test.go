package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/tools"
)

type env struct {
	srv  *fg.Server
	url  string
	proj string
}

func setup(t *testing.T, respond fg.Responder) *env {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	srv, url := fg.Start(t, respond)
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte("Always be brief."), 0o644)
	return &env{srv: srv, url: url, proj: proj}
}

func (e *env) open(t *testing.T, mod func(*bootstrap.Options)) *bootstrap.Session {
	t.Helper()
	tok := auth.StaticToken("hat_test")
	o := bootstrap.Options{Cwd: e.proj, Gateway: connectgw.New(e.url, nil, tok), Prompts: prompts.NewConnectSource(e.url, nil, tok)}
	if mod != nil {
		mod(&o)
	}
	s, err := bootstrap.Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func prompt(s string) []llm.ContentBlock { return []llm.ContentBlock{llm.TextBlock(s)} }

// checkPairing asserts every tool_use is answered in the next user message.
func checkPairing(t *testing.T, msgs []llm.Message) {
	t.Helper()
	for i, m := range msgs {
		for _, tu := range m.ToolUses() {
			if i+1 >= len(msgs) {
				t.Fatalf("tool_use %s has no following message", tu.ID)
			}
			found := false
			for _, b := range msgs[i+1].Content {
				if b.Type == llm.BlockToolResult && b.ToolUseID == tu.ID {
					found = true
				}
			}
			if !found {
				t.Fatalf("tool_use %s unpaired", tu.ID)
			}
		}
	}
}

func TestTextTurnAndCacheMarkers(t *testing.T) {
	e := setup(t, nil)
	s := e.open(t, nil)
	res := s.Agent.RunTurn(context.Background(), prompt("hello"))
	if res.Subtype != agent.SubtypeSuccess || res.Result != "ok" || res.CostUSD != 0.001 {
		t.Fatalf("result %+v", res)
	}
	r := e.srv.Requests()[0]
	if len(r.Tools) != len(e.srv.Bundle.Tools) || r.Metadata.SessionID != s.ID || r.Metadata.PromptBundleVersion != e.srv.Bundle.Version {
		t.Fatalf("request tools/metadata: %d %+v", len(r.Tools), r.Metadata)
	}
	if !r.System[1].Cache || r.System[2].Cache || !strings.Contains(r.System[2].Text, "Working directory: "+e.proj) {
		t.Fatalf("system: %+v", r.System)
	}
	c := r.Messages[0].Content
	if len(c) != 2 || !c[0].Cache || !strings.Contains(c[0].Text, "Always be brief.") || !strings.Contains(c[0].Text, "Project context") || !c[1].Cache || c[1].Text != "hello" {
		t.Fatalf("first message: %+v", c)
	}
}

func TestToolRoundTripAndPermissions(t *testing.T) {
	var target string
	e := setup(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			target = filepath.Join(fg.Cwd(r), "out.txt")
			return fg.Reply{Events: fg.Message("m1",
				fg.ToolUse("t_write", "Write", map[string]any{"file_path": target, "content": "hi\n"}),
				fg.ToolUse("t_bash", "Bash", map[string]any{"description": "test", "command": "rm -rf /tmp/never"}),
			)}
		},
	))
	s := e.open(t, func(o *bootstrap.Options) { o.AllowedTools = []string{"Write"} })
	res := s.Agent.RunTurn(context.Background(), prompt("write it"))
	if res.Subtype != agent.SubtypeSuccess || res.NumTurns != 2 {
		t.Fatalf("result %+v", res)
	}
	if data, _ := os.ReadFile(target); string(data) != "hi\n" {
		t.Fatalf("file %q", data)
	}
	if len(res.PermissionDenials) != 1 || res.PermissionDenials[0].ToolName != "Bash" {
		t.Fatalf("denials %+v", res.PermissionDenials)
	}
	r2 := e.srv.Requests()[1]
	trs := fg.LastToolResults(r2)
	if len(trs) != 2 || trs[0].ToolUseID != "t_write" || trs[0].IsError || trs[1].ToolUseID != "t_bash" || !trs[1].IsError {
		t.Fatalf("tool results %+v", trs)
	}
	checkPairing(t, s.Agent.Messages())
}

type slowTool struct {
	name    string
	cur     atomic.Int32
	maxSeen atomic.Int32
}

func (s *slowTool) Name() string                  { return s.name }
func (s *slowTool) ReadOnly(json.RawMessage) bool { return true }
func (s *slowTool) Run(ctx context.Context, _ *tools.Env, c tools.Call) tools.Result {
	n := s.cur.Add(1)
	for {
		m := s.maxSeen.Load()
		if n <= m || s.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
	}
	s.cur.Add(-1)
	return tools.TextResult("done "+c.ID, nil)
}

func TestParallelReadOnlyCalls(t *testing.T) {
	e := setup(t, fg.Sequence(func(r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m1",
			fg.ToolUse("g1", "Glob", map[string]any{"pattern": "a"}),
			fg.ToolUse("g2", "Glob", map[string]any{"pattern": "b"}),
			fg.ToolUse("g3", "Glob", map[string]any{"pattern": "c"}))}
	}))
	s := e.open(t, nil)
	slow := &slowTool{name: "Glob"}
	s.Tools.Register(slow)
	start := time.Now()
	res := s.Agent.RunTurn(context.Background(), prompt("search"))
	if res.Subtype != agent.SubtypeSuccess {
		t.Fatalf("result %+v", res)
	}
	if slow.maxSeen.Load() != 3 || time.Since(start) > 800*time.Millisecond {
		t.Fatalf("read-only calls not parallel: max=%d elapsed=%s", slow.maxSeen.Load(), time.Since(start))
	}
	trs := fg.LastToolResults(e.srv.Requests()[1])
	for i, id := range []string{"g1", "g2", "g3"} {
		if trs[i].ToolUseID != id || fg.ResultText(trs[i]) != "done "+id {
			t.Fatalf("results out of order: %+v", trs)
		}
	}
}

func TestInterruptMidTool(t *testing.T) {
	e := setup(t, fg.Sequence(func(r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m1", fg.Text("running"), fg.ToolUse("b1", "Bash", map[string]any{"description": "test", "command": "sleep 30"}))}
	}))
	var s *bootstrap.Session
	s = e.open(t, func(o *bootstrap.Options) {
		o.PermissionMode = permissions.ModeBypassPermissions
		o.OnEvent = func(ev agent.Event) {
			if ev.Kind == agent.EventToolStart {
				go func() { time.Sleep(200 * time.Millisecond); s.Agent.Interrupt() }()
			}
		}
	})
	start := time.Now()
	res := s.Agent.RunTurn(context.Background(), prompt("go"))
	if !res.Interrupted || res.Subtype != agent.SubtypeErrorDuringExecution || time.Since(start) > 5*time.Second {
		t.Fatalf("result %+v after %s", res, time.Since(start))
	}
	msgs := s.Agent.Messages()
	last := msgs[len(msgs)-1]
	if last.Content[0].Type != llm.BlockToolResult || !last.Content[0].IsError || fg.ResultText(last.Content[0]) != "[Request interrupted by user for tool use]" {
		t.Fatalf("synthetic result %+v", last.Content)
	}
	if last.Content[1].Text != "[Request interrupted by user]" {
		t.Fatalf("marker %+v", last.Content)
	}
	checkPairing(t, msgs)
	// The next turn sends a valid history (pairing kept, new prompt merged).
	res = s.Agent.RunTurn(context.Background(), prompt("continue"))
	if res.Subtype != agent.SubtypeSuccess {
		t.Fatalf("second turn %+v", res)
	}
	r := e.srv.Requests()[1]
	lm := r.Messages[len(r.Messages)-1]
	if lm.Role != "user" || lm.Content[len(lm.Content)-1].Text != "continue" {
		t.Fatalf("history %+v", lm)
	}
	checkPairing(t, r.Messages)
}

func TestInterruptDuringStream(t *testing.T) {
	e := setup(t, fg.Sequence(func(r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Partial("m1", false, fg.Text("half done"), fg.ToolUse("x", "Bash", map[string]any{"description": "test", "command": "ls"})), Hold: true}
	}))
	s := e.open(t, nil)
	go func() { time.Sleep(300 * time.Millisecond); s.Agent.Interrupt() }()
	res := s.Agent.RunTurn(context.Background(), prompt("go"))
	if !res.Interrupted {
		t.Fatalf("result %+v", res)
	}
	msgs := s.Agent.Messages()
	if len(msgs) != 3 || msgs[1].Role != "assistant" || len(msgs[1].Content) != 1 || msgs[1].Content[0].Text != "half done" {
		t.Fatalf("partial assistant kept without tool_use: %+v", msgs)
	}
	if msgs[2].Content[0].Text != "[Request interrupted by user]" {
		t.Fatalf("marker %+v", msgs[2])
	}
}

func TestQueuedMessageInjectedAfterTools(t *testing.T) {
	e := setup(t, fg.Sequence(func(r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m1", fg.ToolUse("b1", "Bash", map[string]any{"description": "test", "command": "sleep 0.5"}))}
	}))
	var drv *agent.Driver
	var mu sync.Mutex
	var results []agent.TurnResult
	s := e.open(t, func(o *bootstrap.Options) {
		o.PermissionMode = permissions.ModeBypassPermissions
		o.OnEvent = func(ev agent.Event) {
			if ev.Kind == agent.EventToolStart {
				if !drv.Submit("", prompt("also check the README")) {
					t.Error("message should be queued while a turn runs")
				}
			}
		}
	})
	drv = agent.NewDriver(context.Background(), s.Agent, func(r agent.TurnResult) { mu.Lock(); results = append(results, r); mu.Unlock() })
	drv.Submit("", prompt("run it"))
	drv.Wait()
	if len(results) != 1 || results[0].Subtype != agent.SubtypeSuccess {
		t.Fatalf("expected one turn, got %+v", results)
	}
	r := e.srv.Requests()[1]
	lm := r.Messages[len(r.Messages)-1]
	if len(lm.Content) != 2 || lm.Content[0].Type != llm.BlockToolResult || lm.Content[1].Text != "also check the README" {
		t.Fatalf("queued message not injected after tool result: %+v", lm.Content)
	}
}

func TestMaxTurns(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("g"+string(rune('a'+n)), "Glob", map[string]any{"pattern": "*"}))}
	})
	s := e.open(t, func(o *bootstrap.Options) { o.MaxTurns = 2 })
	res := s.Agent.RunTurn(context.Background(), prompt("loop"))
	if res.Subtype != agent.SubtypeErrorMaxTurns || res.NumTurns != 2 || len(e.srv.Requests()) != 2 {
		t.Fatalf("result %+v requests=%d", res, len(e.srv.Requests()))
	}
	checkPairing(t, s.Agent.Messages())
}

func TestResumeKeepsHistoryAndFrozenBundle(t *testing.T) {
	e := setup(t, nil)
	s := e.open(t, nil)
	s.Agent.RunTurn(context.Background(), prompt("first"))
	id, version := s.ID, s.Bundle.Version
	s.Close()
	e.srv.Bundle.Version = "test-new-version"

	s2 := e.open(t, func(o *bootstrap.Options) { o.Resume = id })
	if s2.ID != id || s2.Bundle.Version != version {
		t.Fatalf("resume id=%s bundle=%s (want frozen %s)", s2.ID, s2.Bundle.Version, version)
	}
	s2.Agent.RunTurn(context.Background(), prompt("second"))
	r := e.srv.Requests()[1]
	if len(r.Messages) != 3 || r.Messages[0].Content[1].Text != "first" || r.Messages[2].Content[0].Text != "second" {
		t.Fatalf("resumed history %+v", r.Messages)
	}
	if !r.Messages[0].Content[0].Cache {
		t.Fatal("project context cache marker lost on resume")
	}
	s2.Close()
	s3 := e.open(t, func(o *bootstrap.Options) { o.Continue = true })
	if s3.ID != id || len(s3.Agent.Messages()) != 4 {
		t.Fatalf("continue: id=%s msgs=%d", s3.ID, len(s3.Agent.Messages()))
	}
}
