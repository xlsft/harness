package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

// tb builds a chained transcript like the agent writes it.
type tb struct {
	entries []session.Entry
	last    string
	n       int
	t0      time.Time
}

func newTB() *tb { return &tb{t0: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)} }

func (b *tb) add(e session.Entry) string {
	b.n++
	if e.UUID == "" {
		e.UUID = fmt.Sprintf("e%02d", b.n)
	}
	if e.ParentUUID == nil && b.last != "" && e.Subtype != session.SubtypeCompactBoundary {
		p := b.last
		e.ParentUUID = &p
	}
	e.Timestamp = b.t0.Add(time.Duration(b.n) * time.Second).Format("2006-01-02T15:04:05.000Z")
	b.entries = append(b.entries, e)
	b.last = e.UUID
	return e.UUID
}

func (b *tb) prompt(text, promptID, mode string) string {
	return b.add(session.Entry{Type: session.TypeUser, Message: session.UserText(text), PromptID: promptID, PermissionMode: mode})
}

func (b *tb) assistant(id string, usage llm.Usage, blocks ...llm.ContentBlock) string {
	raw, _ := json.Marshal(blocks)
	stop := "end_turn"
	return b.add(session.Entry{Type: session.TypeAssistant, Message: &session.Message{ID: id, Type: "message", Role: "assistant", Model: "m1", Content: raw, StopReason: &stop, Usage: &usage}})
}

func (b *tb) result(promptID, toolUseID, text string, isError bool, structured any) string {
	blk := llm.ContentBlock{Type: llm.BlockToolResult, ToolUseID: toolUseID, IsError: isError, Content: []llm.ContentBlock{llm.TextBlock(text)}}
	var st any
	if structured != nil {
		// Round-trip like a loaded transcript.
		j, _ := json.Marshal(structured)
		_ = json.Unmarshal(j, &st)
	}
	return b.add(session.Entry{Type: session.TypeUser, Message: session.UserBlocks([]llm.ContentBlock{blk}), PromptID: promptID, ToolUseResult: st})
}

func toolUse(id, name string, input any) llm.ContentBlock {
	j, _ := json.Marshal(input)
	return llm.ContentBlock{Type: llm.BlockToolUse, ID: id, Name: name, Input: j}
}

// kinds renders events as short names for order assertions.
func kinds(evs []*agentv1.SessionEvent) string {
	var out []string
	for _, ev := range evs {
		switch e := ev.Event.(type) {
		case *agentv1.SessionEvent_UserMessage:
			out = append(out, "user")
		case *agentv1.SessionEvent_TurnStarted:
			out = append(out, "turn")
		case *agentv1.SessionEvent_AssistantMessage:
			out = append(out, "asst:"+e.AssistantMessage.MessageId)
		case *agentv1.SessionEvent_ToolCallStarted:
			out = append(out, "start:"+e.ToolCallStarted.ToolUseId)
		case *agentv1.SessionEvent_ToolCallFinished:
			out = append(out, "end:"+e.ToolCallFinished.ToolUseId)
		case *agentv1.SessionEvent_TurnFinished:
			out = append(out, "done:"+strings.TrimPrefix(e.TurnFinished.Result.String(), "RESULT_"))
		case *agentv1.SessionEvent_TodosUpdated:
			out = append(out, "todos")
		case *agentv1.SessionEvent_Compacted:
			out = append(out, "compacted")
		case *agentv1.SessionEvent_PermissionModeChanged:
			out = append(out, "mode:"+strings.TrimPrefix(e.PermissionModeChanged.Mode.String(), "PERMISSION_MODE_"))
		case *agentv1.SessionEvent_Notice:
			out = append(out, "notice:"+e.Notice.Kind)
		case *agentv1.SessionEvent_Question:
			out = append(out, "question")
		case *agentv1.SessionEvent_QuestionResolved:
			out = append(out, "answered")
		case *agentv1.SessionEvent_PlanProposed:
			out = append(out, "plan")
		case *agentv1.SessionEvent_PlanResolved:
			out = append(out, "planned")
		default:
			out = append(out, fmt.Sprintf("%T", e))
		}
	}
	return strings.Join(out, " ")
}

const (
	testInterrupt     = "[Request interrupted by user]"
	testToolInterrupt = "[Tool use interrupted]"
)

func TestReplayHistory(t *testing.T) {
	b := newTB()
	b.entries = append(b.entries, session.Entry{Type: session.TypeMeta, PromptBundleVersion: "v1", Model: "m1"})
	b.add(session.Entry{Type: session.TypeUser, Message: session.UserText("<context>project</context>"), IsMeta: true, PromptID: "p1"})
	u1 := b.prompt("fix the parser bug", "p1", "default")
	b.assistant("msg_1", llm.Usage{InputTokens: 100, OutputTokens: 10},
		llm.ContentBlock{Type: llm.BlockThinking, Thinking: "hmm", Signature: "sig"}, llm.TextBlock("Looking."),
		toolUse("t1", "Read", map[string]string{"file_path": "/w/a.go"}),
		toolUse("t2", "TodoWrite", map[string]any{"todos": []map[string]string{{"content": "Fix", "activeForm": "Fixing", "status": "in_progress"}}}))
	b.result("p1", "t1", "package a", false, map[string]any{"type": "text", "file": map[string]any{"filePath": "/w/a.go"}})
	b.result("p1", "t2", "ok", false, nil)
	b.add(session.Entry{Type: session.TypeUser, Message: session.UserText("<system-reminder>x</system-reminder>"), IsMeta: true, PromptID: "p1"})
	b.assistant("msg_2", llm.Usage{InputTokens: 200, OutputTokens: 20}, llm.TextBlock("Fixed."))
	leaf := b.last
	b.add(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeCompactBoundary, Content: "Conversation compacted", LogicalParentUUID: leaf,
		CompactMetadata: &session.CompactMetadata{Trigger: "manual", PreTokens: 5000}})
	b.add(session.Entry{Type: session.TypeUser, Message: session.UserText("<context>project</context>"), IsMeta: true})
	b.add(session.Entry{Type: session.TypeUser, Message: session.UserText(strings.Repeat("summary ", 100)), IsCompactSummary: true, IsVisibleInTranscriptOnly: true})
	b.prompt("now add tests", "p2", "acceptEdits")
	b.assistant("msg_3", llm.Usage{InputTokens: 300, OutputTokens: 5}, toolUse("t3", "Bash", map[string]string{"description": "test", "command": "go test ./..."}))
	b.result("p2", "t3", testToolInterrupt, true, testToolInterrupt)
	b.add(session.Entry{Type: session.TypeUser, Message: session.UserText(testInterrupt), PromptID: "p2"})
	b.entries = append(b.entries, session.Entry{Type: session.TypeAITitle, AITitle: "Parser bug"})

	evs := replayHistory(b.entries, replayOptions{Cwd: "/w", Interrupts: []string{testInterrupt, testToolInterrupt}})
	want := "mode:DEFAULT user turn asst:msg_1 start:t1 start:t2 end:t1 end:t2 todos asst:msg_2 done:SUCCESS " +
		"compacted mode:ACCEPT_EDITS user turn asst:msg_3 start:t3 end:t3 user done:INTERRUPTED"
	if got := kinds(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if u := evs[1].GetUserMessage(); u.Uuid != u1 || u.Content[0].GetText().GetText() != "fix the parser bug" {
		t.Fatalf("user message: %+v", u)
	}
	if ts := evs[2].GetTurnStarted(); ts.UserMessageUuid != u1 || ts.Model != "m1" {
		t.Fatalf("turn started: %+v", ts)
	}
	am := evs[3].GetAssistantMessage()
	if len(am.Content) != 4 || am.Content[0].GetThinking().GetThinking() != "hmm" || am.Content[2].GetToolUse().GetName() != "Read" || am.Usage.GetInputTokens() != 100 {
		t.Fatalf("assistant message: %+v", am)
	}
	if st := evs[4].GetToolCallStarted(); st.Name != "Read" || st.Summary != "Read a.go" || !strings.Contains(st.InputJson, "/w/a.go") {
		t.Fatalf("tool started: %+v", st)
	}
	if f := evs[6].GetToolCallFinished(); f.IsError || f.Interrupted || f.Content[0].GetText().GetText() != "package a" ||
		!strings.Contains(f.StructuredResultJson, `"filePath":"/w/a.go"`) || f.DurationMs != 1000 {
		t.Fatalf("tool finished: %+v", f)
	}
	if td := evs[8].GetTodosUpdated(); len(td.Todos) != 1 || td.Todos[0].Status != agentv1.Todo_STATUS_IN_PROGRESS {
		t.Fatalf("todos: %+v", td)
	}
	if tf := evs[10].GetTurnFinished(); tf.NumTurns != 2 || tf.Usage.GetInputTokens() != 300 || tf.Usage.GetOutputTokens() != 30 || tf.DurationMs <= 0 {
		t.Fatalf("turn finished: %+v", tf)
	}
	if c := evs[11].GetCompacted(); c.Trigger != agentv1.Compacted_TRIGGER_MANUAL || c.TokensBefore != 5000 || c.TokensAfter <= 0 {
		t.Fatalf("compacted: %+v", c)
	}
	if f := evs[17].GetToolCallFinished(); !f.Interrupted || !f.IsError || f.Summary != "Interrupted" {
		t.Fatalf("interrupted tool: %+v", f)
	}
	if u := evs[18].GetUserMessage(); u.Content[0].GetText().GetText() != testInterrupt {
		t.Fatalf("interrupt marker: %+v", u)
	}
	for _, ev := range evs {
		if ev.Timestamp == "" || ev.Seq != 0 {
			t.Fatalf("timestamp/seq: %+v", ev)
		}
	}
}

func TestReplayHistoryOrphanToolsAndRewind(t *testing.T) {
	b := newTB()
	b.prompt("first", "p1", "")
	b.assistant("msg_1", llm.Usage{}, llm.TextBlock("one"))
	b.add(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeRewind, Content: "Rewound to checkpoint x"})
	b.prompt("second", "p2", "")
	b.assistant("msg_2", llm.Usage{}, toolUse("t1", "Bash", map[string]string{"description": "test", "command": "sleep 100"}), toolUse("t2", "Glob", map[string]string{"pattern": "*.go"}))
	b.result("p2", "t2", "a.go", false, nil)
	// The process stopped here: t1 never got a result.
	evs := replayHistory(b.entries, replayOptions{})
	want := "user turn asst:msg_1 done:SUCCESS notice:rewind user turn asst:msg_2 start:t1 start:t2 end:t2 end:t1 done:INTERRUPTED"
	if got := kinds(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if f := evs[11].GetToolCallFinished(); f.ToolUseId != "t1" || !f.Interrupted {
		t.Fatalf("orphan: %+v", f)
	}
}

func TestReplayHistoryCards(t *testing.T) {
	b := newTB()
	b.prompt("plan it", "p1", "plan")
	qs := map[string]any{"questions": []map[string]any{{"question": "Which?", "header": "Pick", "options": []map[string]string{{"label": "A"}, {"label": "B"}}}}}
	b.assistant("msg_1", llm.Usage{}, toolUse("q1", "AskUserQuestion", qs))
	b.result("p1", "q1", "answered", false, map[string]any{"questions": qs["questions"], "answers": map[string]string{"Which?": "A"}})
	b.assistant("msg_2", llm.Usage{}, toolUse("x1", "ExitPlanMode", map[string]string{"plan": "do it"}))
	b.result("p1", "x1", "approved", false, map[string]any{"plan": "do it", "filePath": "/p.md", "approved": true})
	b.assistant("msg_3", llm.Usage{}, llm.TextBlock("ok"))
	evs := replayHistory(b.entries, replayOptions{})
	want := "mode:PLAN user turn asst:msg_1 start:q1 question answered end:q1 asst:msg_2 start:x1 plan planned end:x1 asst:msg_3 done:SUCCESS"
	if got := kinds(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if q := evs[5].GetQuestion(); q.ToolUseId != "q1" || len(q.Items) != 1 || len(q.Items[0].Options) != 2 {
		t.Fatalf("question: %+v", q)
	}
	if p := evs[11].GetPlanResolved(); p.Decision != agentv1.RespondToPlanRequest_DECISION_APPROVE_MANUAL {
		t.Fatalf("plan: %+v", p)
	}
}

func TestReplayHistoryCap(t *testing.T) {
	b := newTB()
	for i := range 10 {
		b.prompt(fmt.Sprintf("prompt %d", i), fmt.Sprintf("p%d", i), "")
		b.assistant(fmt.Sprintf("msg_%d", i), llm.Usage{}, llm.TextBlock("answer"))
	}
	evs := replayHistory(b.entries, replayOptions{MaxTurns: 3})
	if n := evs[0].GetNotice(); n == nil || n.Kind != "history_truncated" || !strings.Contains(n.Text, "last 3 of 10 turns") {
		t.Fatalf("notice: %+v", evs[0])
	}
	if got := kinds(evs[1:]); got != "user turn asst:msg_7 done:SUCCESS user turn asst:msg_8 done:SUCCESS user turn asst:msg_9 done:SUCCESS" {
		t.Fatalf("kept: %s", got)
	}
	// The event cap drops whole turns too.
	evs = replayHistory(b.entries, replayOptions{MaxEvents: 9})
	if n := evs[0].GetNotice(); n == nil || !strings.Contains(n.Text, "last 2 of 10 turns") || len(evs) != 9 {
		t.Fatalf("event cap: %d %s", len(evs), kinds(evs))
	}
	// Under the caps nothing is added.
	if evs = replayHistory(b.entries, replayOptions{}); len(evs) != 40 || evs[0].GetUserMessage() == nil {
		t.Fatalf("uncapped: %d", len(evs))
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Parser bug fix":                   "Parser bug fix",
		"\n\n\"Flaky login test.\"\nextra": "Flaky login test",
		"Title: CSV export for reports":    "CSV export for reports",
		"## **Caching layer**":             "Caching layer",
		"Fix f(x)":                         "Fix f(x)",
		"   ":                              "",
		strings.Repeat("word ", 30):        strings.TrimSpace(strings.Repeat("word ", 16)) + "…",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if got := provisionalTitle(strings.Repeat("é", 100) + "\nmore"); got != strings.Repeat("é", 80)+"…" {
		t.Errorf("provisional: %q", got)
	}
}

func TestTitleModel(t *testing.T) {
	cat := func(slots map[string]string) *llm.Catalog { return &llm.Catalog{Slots: slots} }
	for _, c := range []struct {
		name string
		cat  *llm.Catalog
		want string
	}{
		{"title slot", cat(map[string]string{"main": "m", "small": "s", "title": "t"}), "t"},
		{"small slot when no title slot", cat(map[string]string{"main": "m", "small": "s"}), "s"},
		{"small slot when the title slot is empty", cat(map[string]string{"small": "s", "title": ""}), "s"},
		{"session model without slots", cat(map[string]string{"main": "m"}), "session"},
		{"session model without a catalog", nil, "session"},
	} {
		if got := titleModel(c.cat, "session"); got != c.want {
			t.Errorf("%s: titleModel = %q, want %q", c.name, got, c.want)
		}
	}
}
