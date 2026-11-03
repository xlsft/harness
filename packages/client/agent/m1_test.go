package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/session"
)

// reqText concatenates all text of a request's messages (including tool results).
func reqText(r *llm.Request) string {
	var sb strings.Builder
	for _, m := range r.Messages {
		for _, b := range m.Content {
			sb.WriteString(b.Text)
			for _, c := range b.Content {
				sb.WriteString(c.Text)
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func mainRequests(srv *fg.Server) []*llm.Request {
	var out []*llm.Request
	for _, r := range srv.Requests() {
		if r.Metadata == nil || r.Metadata.RequestClass != "compaction" {
			out = append(out, r)
		}
	}
	return out
}

func compactionRequests(srv *fg.Server) []*llm.Request {
	var out []*llm.Request
	for _, r := range srv.Requests() {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" {
			out = append(out, r)
		}
	}
	return out
}

type recorder struct {
	mu  sync.Mutex
	evs []agent.Event
}

func (r *recorder) on(e agent.Event) {
	if e.Kind == agent.EventStream {
		return
	}
	r.mu.Lock()
	r.evs = append(r.evs, e)
	r.mu.Unlock()
}

func (r *recorder) find(k agent.EventKind) []agent.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []agent.Event
	for _, e := range r.evs {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackgroundTaskReminder(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("b1", "Bash", map[string]any{"command": "echo bg-out; sleep 0.2", "run_in_background": true, "description": "bg"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	rec := &recorder{}
	s := e.open(t, func(o *bootstrap.Options) {
		o.AllowedTools = []string{"Bash(echo *)", "Bash(sleep *)"}
		o.OnEvent = rec.on
	})
	res := s.Agent.RunTurn(context.Background(), prompt("start it"))
	if res.Subtype != agent.SubtypeSuccess {
		t.Fatalf("result %+v", res)
	}
	waitUntil(t, 5*time.Second, func() bool {
		for _, ev := range rec.find(agent.EventTask) {
			if ev.Task.Kind == 3 {
				return true
			}
		}
		return false
	})
	trs := fg.LastToolResults(mainRequests(e.srv)[1])
	if !strings.Contains(fg.ResultText(trs[0]), "running in background with ID: b") {
		t.Fatalf("tool result %q", fg.ResultText(trs[0]))
	}
	s.Agent.RunTurn(context.Background(), prompt("next"))
	last := mainRequests(e.srv)[2]
	if txt := reqText(last); !strings.Contains(txt, "Background task b") || !strings.Contains(txt, "exited with code 0") ||
		!strings.Contains(txt, "Last lines:\nbg-out\n") || !strings.Contains(txt, "(echo bg-out; sleep 0.2)") {
		t.Fatalf("background_task_done reminder missing:\n%s", reqText(last))
	}
	tasks := s.Tasks.List()
	if len(tasks) != 1 || tasks[0].Running {
		t.Fatalf("tasks %+v", tasks)
	}
	if data, _ := os.ReadFile(tasks[0].OutputPath); string(data) != "bg-out\n" {
		t.Fatalf("output %q", data)
	}
}

// A task whose finish a TaskOutput result already reported gets no
// completion reminder.
func TestBackgroundTaskReminderSkippedWhenReported(t *testing.T) {
	idRe := regexp.MustCompile(`ID: (b[0-9a-f]+)`)
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("b1", "Bash", map[string]any{"command": "echo bg-out", "run_in_background": true}))}
		case 1:
			id := idRe.FindStringSubmatch(fg.ResultText(fg.LastToolResults(r)[0]))[1]
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("t1", "TaskOutput", map[string]any{"task_id": id, "block": true, "timeout": 5000}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	s := e.open(t, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash(echo *)"} })
	if res := s.Agent.RunTurn(context.Background(), prompt("start it")); res.Subtype != agent.SubtypeSuccess {
		t.Fatalf("result %+v", res)
	}
	if tr := fg.ResultText(fg.LastToolResults(mainRequests(e.srv)[2])[0]); !strings.Contains(tr, "exited with code 0") || !strings.Contains(tr, "bg-out") {
		t.Fatalf("TaskOutput result %q", tr)
	}
	s.Agent.RunTurn(context.Background(), prompt("next"))
	for i, r := range mainRequests(e.srv) {
		if strings.Contains(reqText(r), "Background task b") {
			t.Fatalf("request %d carries a completion reminder for a reported task:\n%s", i, reqText(r))
		}
	}
}

func TestAutoBackgroundInTurn(t *testing.T) {
	t.Setenv("BASH_DEFAULT_TIMEOUT_MS", "300")
	e := setup(t, fg.Sequence(func(r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m1", fg.ToolUse("b1", "Bash", map[string]any{"command": "echo a; sleep 2"}))}
	}))
	s := e.open(t, func(o *bootstrap.Options) { o.PermissionMode = permissions.ModeBypassPermissions })
	start := time.Now()
	res := s.Agent.RunTurn(context.Background(), prompt("go"))
	if res.Subtype != agent.SubtypeSuccess || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("result %+v after %s", res, time.Since(start))
	}
	tr := fg.ResultText(fg.LastToolResults(e.srv.Requests()[1])[0])
	if !strings.Contains(tr, "did not complete within its") || s.Tasks.Running() != 1 {
		t.Fatalf("auto-background: %q running=%d", tr, s.Tasks.Running())
	}
	s.Close()
	if s.Tasks.Running() != 0 {
		t.Fatal("background tasks survive Close")
	}
}

func TestCheckpointsAndRewind(t *testing.T) {
	var file string
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata.RequestClass == "compaction" {
			return fg.Reply{Events: fg.Message("sum", fg.Text("SUMMARY-"+strings.Fields(reqText(r))[0]))}
		}
		last := r.Messages[len(r.Messages)-1]
		if last.Content[len(last.Content)-1].Type == llm.BlockToolResult {
			return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
		}
		file = filepath.Join(fg.Cwd(r), "a.txt")
		switch txt := last.Content[len(last.Content)-1].Text; txt {
		case "create":
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("w1", "Write", map[string]any{"file_path": file, "content": "v1\n"}))}
		case "update":
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("e1", "Edit", map[string]any{"file_path": file, "old_string": "v1", "new_string": "v2"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("chat"))}
	})
	rec := &recorder{}
	s := e.open(t, func(o *bootstrap.Options) {
		o.PermissionMode = permissions.ModeAcceptEdits
		o.OnEvent = rec.on
	})
	ctx := context.Background()
	s.Agent.RunTurn(ctx, prompt("create"))
	s.Agent.RunTurn(ctx, prompt("update"))
	s.Agent.RunTurn(ctx, prompt("talk"))
	cps := s.Agent.Checkpoints()
	if len(cps) != 3 || cps[1].PromptPreview != "update" || !cps[0].HasCodeChanges || !cps[1].HasCodeChanges || cps[2].HasCodeChanges {
		t.Fatalf("checkpoints %+v", cps)
	}
	if n := len(rec.find(agent.EventCheckpoint)); n != 3 {
		t.Fatalf("checkpoint events %d", n)
	}
	if ts := rec.find(agent.EventTurnStarted); len(ts) != 3 || ts[0].UUID != cps[0].ID {
		t.Fatalf("turn started events %+v", ts)
	}

	// Code only: back to v1, conversation unchanged.
	res, err := s.Agent.Rewind(ctx, cps[1].ID, agent.RewindCode, "")
	if err != nil || res.FilesRestored != 1 || res.RestoredPrompt != "" {
		t.Fatalf("rewind code: %+v %v", res, err)
	}
	if data, _ := os.ReadFile(file); string(data) != "v1\n" {
		t.Fatalf("file after code rewind %q", data)
	}
	if n := len(s.Agent.Messages()); n != 10 {
		t.Fatalf("conversation changed by code rewind: %d messages", n)
	}
	// Code and conversation to the first checkpoint: file gone, history empty.
	res, err = s.Agent.Rewind(ctx, cps[0].ID, agent.RewindCodeAndConversation, "")
	if err != nil || res.RestoredPrompt != "create" {
		t.Fatalf("rewind all: %+v %v", res, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("file should not exist at the first checkpoint")
	}
	if n := len(s.Agent.Messages()); n != 0 {
		t.Fatalf("messages after full rewind: %d", n)
	}
	if n := len(s.Agent.Checkpoints()); n != 0 {
		t.Fatalf("rewound-away checkpoints still listed: %d", n)
	}
	if len(rec.find(agent.EventRewound)) != 2 {
		t.Fatal("rewound events")
	}

	// New history: three turns again, then conversation-only rewind.
	s.Agent.RunTurn(ctx, prompt("create"))
	s.Agent.RunTurn(ctx, prompt("talk"))
	s.Agent.RunTurn(ctx, prompt("talk more"))
	cps = s.Agent.Checkpoints()
	if len(cps) != 3 {
		t.Fatalf("checkpoints after new turns %+v", cps)
	}
	res, _ = s.Agent.Rewind(ctx, cps[2].ID, agent.RewindConversation, "")
	if res.RestoredPrompt != "talk more" || len(s.Agent.Messages()) != 6 {
		t.Fatalf("conversation rewind: %+v msgs=%d", res, len(s.Agent.Messages()))
	}
	if data, _ := os.ReadFile(file); string(data) != "v1\n" {
		t.Fatal("conversation rewind must not touch files")
	}
	s.Agent.RunTurn(ctx, prompt("talk again"))

	// Summarize from the second checkpoint: earlier turn kept, rest summarized.
	cps = s.Agent.Checkpoints()
	res, err = s.Agent.Rewind(ctx, cps[1].ID, agent.RewindSummarizeFrom, "keep it short")
	if err != nil || res.RestoredPrompt != "talk" {
		t.Fatalf("summarize from: %+v %v", res, err)
	}
	cr := compactionRequests(e.srv)
	if len(cr) != 1 || !strings.Contains(reqText(cr[0]), "keep it short") || !strings.HasPrefix(reqText(cr[0]), "talk") {
		t.Fatalf("summarize-from request:\n%s", reqText(cr[0]))
	}
	msgs := s.Agent.Messages()
	if txt := reqText(&llm.Request{Messages: msgs}); !strings.Contains(txt, "create") || !strings.Contains(txt, "SUMMARY-talk") || strings.Contains(txt, "talk again") {
		t.Fatalf("after summarize from:\n%s", txt)
	}
	s.Agent.RunTurn(ctx, prompt("after summary"))

	// Summarize up to the last checkpoint: older turns replaced by a summary.
	cps = s.Agent.Checkpoints()
	lastCP := cps[len(cps)-1]
	res, err = s.Agent.Rewind(ctx, lastCP.ID, agent.RewindSummarizeUpTo, "")
	if err != nil || res.RestoredPrompt != "" {
		t.Fatalf("summarize up to: %+v %v", res, err)
	}
	msgs = s.Agent.Messages()
	txt := reqText(&llm.Request{Messages: msgs})
	if !strings.Contains(txt, "SUMMARY-") || !strings.Contains(txt, "after summary") || strings.Contains(txt, "\ncreate\n") {
		t.Fatalf("after summarize up to:\n%s", txt)
	}
	if cps2 := s.Agent.Checkpoints(); len(cps2) == 0 || cps2[len(cps2)-1].ID != lastCP.ID {
		t.Fatalf("kept turn's checkpoint must survive: %+v", cps2)
	}
	if len(rec.find(agent.EventCompacted)) != 2 {
		t.Fatal("summarize modes emit Compacted")
	}
	// Resume rebuilds the same conversation from the transcript.
	id := s.ID
	s.Close()
	s2 := e.open(t, func(o *bootstrap.Options) { o.Resume = id })
	if got := reqText(&llm.Request{Messages: s2.Agent.Messages()}); got != txt {
		t.Fatalf("resumed conversation differs:\n%s\nvs\n%s", got, txt)
	}
	if len(s2.Agent.Checkpoints()) == 0 {
		t.Fatal("checkpoints survive resume")
	}
}

func TestAutoCompactionAndAntiThrashing(t *testing.T) {
	var compactions atomic.Int32
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata.RequestClass == "compaction" {
			compactions.Add(1)
			return fg.Reply{Events: fg.Message("sum", fg.Text("SUMMARY"))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	rec := &recorder{}
	// A threshold of 0.0001 of the 200k window (20 tokens) is always exceeded:
	// every compaction is ineffective, so auto-compaction stops after 3 tries.
	s := e.open(t, func(o *bootstrap.Options) { o.AutoCompactPct = 0.0001; o.OnEvent = rec.on })
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if res := s.Agent.RunTurn(ctx, prompt("hello")); res.Subtype != agent.SubtypeSuccess {
			t.Fatalf("turn %d: %+v", i, res)
		}
	}
	if n := compactions.Load(); n != agent.MaxCompactFailures {
		t.Fatalf("compactions %d, want %d", n, agent.MaxCompactFailures)
	}
	cr := compactionRequests(e.srv)[0]
	if cr.ToolChoice == nil || cr.ToolChoice.Type != "none" || !strings.Contains(reqText(cr), "Summarize the conversation.") {
		t.Fatalf("compaction request %+v", cr.ToolChoice)
	}
	ev := rec.find(agent.EventCompacted)
	if len(ev) != 3 || ev[0].Compaction.Trigger != "auto" {
		t.Fatalf("compacted events %+v", ev)
	}
	disabled := false
	for _, n := range rec.find(agent.EventNotice) {
		if n.Notice.Kind == "compaction_disabled" {
			disabled = true
		}
	}
	if !disabled {
		t.Fatal("no compaction_disabled notice")
	}
	// The main request after a compaction starts from the summary, with the
	// project context re-injected.
	m := mainRequests(e.srv)[0]
	first := m.Messages[0].Content
	if !strings.Contains(first[0].Text, "Always be brief.") || first[1].Text != "SUMMARY" || !strings.Contains(first[2].Text, "compacted") {
		t.Fatalf("post-compaction request %+v", first)
	}
	// Transcript has the boundary and the summary entry.
	var boundary, summary bool
	for _, en := range s.Transcript.Entries() {
		if en.Subtype == session.SubtypeCompactBoundary && en.CompactMetadata != nil && en.CompactMetadata.Trigger == "auto" && en.ParentUUID == nil {
			boundary = true
		}
		if en.IsCompactSummary {
			summary = true
		}
	}
	if !boundary || !summary {
		t.Fatalf("transcript boundary=%v summary=%v", boundary, summary)
	}
	// A manual compaction still works and re-enables auto-compaction.
	if _, err := s.Agent.Compact(ctx, "focus on tests"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reqText(compactionRequests(e.srv)[3]), "focus on tests") {
		t.Fatal("manual focus instructions missing")
	}
}

func TestTrimToolOutputsBeforeSummarizing(t *testing.T) {
	big := "head -c 4000 /dev/zero | tr '\\0' x"
	var compactions atomic.Int32
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata.RequestClass == "compaction" {
			compactions.Add(1)
			return fg.Reply{Events: fg.Message("sum", fg.Text("SUMMARY"))}
		}
		if n < 6 {
			return fg.Reply{Events: fg.WithInputTokens(fg.Message("m", fg.ToolUse("t"+string(rune('a'+n)), "Bash", map[string]any{"command": big})), len(reqText(r))/4)}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	})
	// Window 200k * 0.03 = 6000 tokens: the big outputs (~1000 tokens each)
	// cross it, trimming old ones brings it back under.
	s := e.open(t, func(o *bootstrap.Options) {
		o.AutoCompactPct = 0.03
		o.PermissionMode = permissions.ModeBypassPermissions
	})
	s.Agent.RunTurn(context.Background(), prompt("go"))
	if compactions.Load() != 0 {
		t.Fatalf("summarized instead of trimming (%d)", compactions.Load())
	}
	last := mainRequests(e.srv)[6]
	if !strings.Contains(reqText(last), "[Old tool result content cleared") {
		t.Fatal("old tool outputs not trimmed")
	}
	checkPairing(t, last.Messages)
}

func TestTodosPersistAcrossResumeAndNudge(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("td", "TodoWrite", map[string]any{"todos": []any{
				map[string]any{"content": "Write tests", "activeForm": "Writing tests", "status": "in_progress"}}}))}
		}
		if n >= 2 && n < 13 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("g"+string(rune('a'+n)), "Glob", map[string]any{"pattern": "*"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	rec := &recorder{}
	s := e.open(t, func(o *bootstrap.Options) { o.OnEvent = rec.on })
	s.Agent.RunTurn(context.Background(), prompt("plan"))
	if td := rec.find(agent.EventTodos); len(td) != 1 || td[0].Todos[0].Status != "in_progress" {
		t.Fatalf("todos event %+v", td)
	}
	s.Agent.RunTurn(context.Background(), prompt("work"))
	nudged := false
	for _, r := range mainRequests(e.srv) {
		if strings.Contains(reqText(r), "Consider updating the todo list") {
			nudged = true
		}
	}
	if !nudged {
		t.Fatal("todo_nudge not injected after 10 rounds without TodoWrite")
	}
	id := s.ID
	s.Close()
	s2 := e.open(t, func(o *bootstrap.Options) { o.Resume = id })
	if td := s2.Agent.Todos(); len(td) != 1 || td[0].Content != "Write tests" {
		t.Fatalf("todos after resume %+v", td)
	}
}

func TestFileChangedReminder(t *testing.T) {
	var file string
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			file = filepath.Join(fg.Cwd(r), "f.txt")
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("r", "Read", map[string]any{"file_path": file}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	os.WriteFile(filepath.Join(e.proj, "f.txt"), []byte("alpha\nbeta\n"), 0o644)
	s := e.open(t, nil)
	s.Agent.RunTurn(context.Background(), prompt("read"))
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(file, []byte("alpha\ngamma\n"), 0o644)
	s.Agent.RunTurn(context.Background(), prompt("again"))
	txt := reqText(mainRequests(e.srv)[2])
	if !strings.Contains(txt, file+" was modified outside of your tools") || !strings.Contains(txt, "-beta") || !strings.Contains(txt, "+gamma") {
		t.Fatalf("file_changed reminder missing:\n%s", txt)
	}
}

func TestPlanModeDeniesWritesAndInjectsReminder(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("w", "Write", map[string]any{"file_path": filepath.Join(fg.Cwd(r), "x"), "content": "x"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	s := e.open(t, func(o *bootstrap.Options) { o.PermissionMode = permissions.ModePlan })
	res := s.Agent.RunTurn(context.Background(), prompt("plan it"))
	if res.Subtype != agent.SubtypeSuccess || len(res.PermissionDenials) != 1 {
		t.Fatalf("result %+v", res)
	}
	first := reqText(e.srv.Requests()[0])
	if !strings.Contains(first, "Plan mode is active") || !strings.Contains(first, filepath.Join("plans", s.ID, "1.md")) {
		t.Fatalf("plan_mode reminder missing:\n%s", first)
	}
	if !strings.Contains(fg.ResultText(fg.LastToolResults(e.srv.Requests()[1])[0]), "Plan mode is active") {
		t.Fatal("write must be denied in plan mode")
	}
}

func TestDisallowedToolRemovedFromToolList(t *testing.T) {
	e := setup(t, nil)
	s := e.open(t, func(o *bootstrap.Options) { o.DisallowedTools = []string{"Bash", "Edit(./secret/**)"} })
	s.Agent.RunTurn(context.Background(), prompt("hi"))
	for _, tl := range e.srv.Requests()[0].Tools {
		if tl.Name == "Bash" {
			t.Fatal("bare deny must remove Bash from the tool list")
		}
	}
	found := false
	for _, n := range s.ToolNames() {
		if n == "Edit" {
			found = true
		}
	}
	if !found {
		t.Fatal("a scoped deny keeps the tool")
	}
}

func TestForkSession(t *testing.T) {
	e := setup(t, nil)
	s := e.open(t, nil)
	s.Agent.RunTurn(context.Background(), prompt("one"))
	s.Agent.RunTurn(context.Background(), prompt("two"))
	cps := s.Agent.Checkpoints()
	id := s.ID
	s.Close()
	f := e.open(t, func(o *bootstrap.Options) { o.Resume = id; o.Fork = true })
	if f.ID == id || f.ParentSessionID != id || len(f.Agent.Messages()) != 4 {
		t.Fatalf("fork id=%s parent=%s msgs=%d", f.ID, f.ParentSessionID, len(f.Agent.Messages()))
	}
	f.Close()
	at := e.open(t, func(o *bootstrap.Options) { o.Resume = id; o.Fork = true; o.ForkAtCheckpoint = cps[1].ID })
	if len(at.Agent.Messages()) != 2 {
		t.Fatalf("fork at checkpoint msgs=%d", len(at.Agent.Messages()))
	}
	// The original is untouched.
	orig := e.open(t, func(o *bootstrap.Options) { o.Resume = id })
	if len(orig.Agent.Messages()) != 4 {
		t.Fatal("original session changed by fork")
	}
}
