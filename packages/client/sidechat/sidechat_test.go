package sidechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

// conv builds a chained transcript from (type, content JSON) pairs.
func conv(t *testing.T, items ...[2]string) []session.Entry {
	t.Helper()
	var out []session.Entry
	var parent *string
	for i, it := range items {
		id := fmt.Sprintf("u%d", i)
		role := it[0]
		e := session.Entry{Type: role, UUID: id, ParentUUID: parent, Message: &session.Message{Role: role, Content: json.RawMessage(it[1])}}
		out = append(out, e)
		p := id
		parent = &p
	}
	return out
}

func q(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestRenderMain(t *testing.T) {
	entries := conv(t,
		[2]string{"user", q("Fix the flaky login test <system-reminder>secret reminder</system-reminder>")},
		[2]string{"assistant", `[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"Looking at it."},{"type":"tool_use","id":"t1","name":"Bash","input":{"description":"test","command":"go test ./auth/...   -run Login","timeout":5}}]`},
		[2]string{"user", `[{"type":"tool_result","tool_use_id":"t1","content":"FAIL"}]`},
		[2]string{"assistant", `[{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"/p/auth/login_test.go","old_string":"a","new_string":"b"}},{"type":"tool_use","id":"t3","name":"TodoWrite","input":{"todos":[]}}]`},
		[2]string{"user", q("<command-name>/clear</command-name>")},
		[2]string{"assistant", `[{"type":"text","text":"Fixed: the test raced on the clock."}]`},
	)
	got, omitted := RenderMain(entries, 10_000)
	want := "User: Fix the flaky login test\n\n" +
		"Assistant: Looking at it.\n[tool Bash: go test ./auth/... -run Login]\n\n" +
		"Assistant: [tool Edit: /p/auth/login_test.go]\n[tool TodoWrite]\n\n" +
		"Assistant: Fixed: the test raced on the clock."
	if got != want || omitted != 0 {
		t.Fatalf("rendering (%d omitted):\n%s\n--- want:\n%s", omitted, got, want)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "hmm") || strings.Contains(got, "FAIL") {
		t.Fatalf("reminders, thinking and tool results must not be rendered: %s", got)
	}
	if got, _ := RenderMain(nil, 100); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestRenderMainTruncatesFromTheOldest(t *testing.T) {
	var items [][2]string
	for i := 0; i < 20; i++ {
		items = append(items, [2]string{"user", q(fmt.Sprintf("question %02d %s", i, strings.Repeat("x", 400)))})
		items = append(items, [2]string{"assistant", fmt.Sprintf(`[{"type":"text","text":"answer %02d"}]`, i)})
	}
	entries := conv(t, items...)
	full, _ := RenderMain(entries, 1_000_000)
	got, omitted := RenderMain(entries, 1_000)
	if omitted == 0 || EstimateTokens(got) > 1_050 || len(got) >= len(full) {
		t.Fatalf("not truncated: %d omitted, %d tokens", omitted, EstimateTokens(got))
	}
	if !strings.HasPrefix(got, fmt.Sprintf("[%d earlier messages omitted]", omitted)) {
		t.Fatalf("no omission note: %.80s", got)
	}
	// The newest messages are kept, the oldest dropped.
	if !strings.HasSuffix(got, "Assistant: answer 19") || strings.Contains(got, "question 00") {
		t.Fatalf("kept the wrong end: …%s", got[len(got)-80:])
	}

	// After a compaction only the summary and what follows are rendered.
	compacted := append(conv(t, [2]string{"user", q("old question")}),
		session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeCompactBoundary, UUID: "b", LogicalParentUUID: "u0"},
		session.Entry{Type: "user", UUID: "s", ParentUUID: ptr("b"), IsCompactSummary: true, Message: &session.Message{Role: "user", Content: json.RawMessage(q("## User intent\nfix login"))}},
		session.Entry{Type: "user", UUID: "n", ParentUUID: ptr("s"), Message: &session.Message{Role: "user", Content: json.RawMessage(q("and now?"))}},
	)
	got, _ = RenderMain(compacted, 10_000)
	if got != "Summary of the earlier conversation:\n## User intent\nfix login\n\nUser: and now?" {
		t.Fatalf("compacted: %q", got)
	}
}

func ptr(s string) *string { return &s }

func TestRequest(t *testing.T) {
	main := conv(t, [2]string{"user", q("Why ConnectRPC?")}, [2]string{"assistant", `[{"type":"text","text":"It works over HTTP/1.1."}]`})
	history := []Message{
		{Role: "user", Text: "first?", Quote: "a quote"},
		{Role: "assistant", Text: "first answer"},
		{Role: "user", Text: "failed one"},
		{Role: "assistant", Text: "", Error: "boom"},
		{Role: "user", Text: "never answered"},
	}
	req := Request(RequestOptions{
		SessionID: "sess", Prompt: "SIDE PROMPT", BundleVersion: "v1", Model: "m", ContextWindow: 200_000, MaxOutputTokens: 4_000,
		Effort: "high", Thinking: true, Main: main, History: history, Question: "and gRPC?", Quote: "line one\nline two",
	})
	if len(req.Tools) != 0 || req.ToolChoice != nil {
		t.Fatalf("tools offered: %+v %+v", req.Tools, req.ToolChoice)
	}
	if req.Metadata == nil || req.Metadata.RequestClass != "auxiliary" || req.Metadata.SessionID != "sess" || req.Metadata.PromptBundleVersion != "v1" {
		t.Fatalf("metadata: %+v", req.Metadata)
	}
	if req.Model != "m" || req.MaxTokens != 4_000 || req.Effort != "high" || req.Thinking == nil || !req.Stream {
		t.Fatalf("request: %+v", req)
	}
	if len(req.System) != 2 || req.System[0].Text != "SIDE PROMPT" || !strings.Contains(req.System[1].Text, "<main_session>\nUser: Why ConnectRPC?\n\nAssistant: It works over HTTP/1.1.\n</main_session>") || !req.System[1].Cache {
		t.Fatalf("system: %+v", req.System)
	}
	var roles []string
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Fatalf("messages must alternate and skip unanswered questions: %v", roles)
	}
	if got := req.Messages[0].Text(); got != "About this excerpt from the main session:\n> a quote\n\nfirst?" {
		t.Fatalf("history question: %q", got)
	}
	if got := req.Messages[2].Text(); got != "About this excerpt from the main session:\n> line one\n> line two\n\nand gRPC?" {
		t.Fatalf("question: %q", got)
	}

	// Defaults: fallback prompt, default window and answer room, no thinking; empty main session.
	req = Request(RequestOptions{Model: "m", Question: "hi"})
	if req.System[0].Text != FallbackPrompt || req.MaxTokens != DefaultMaxTokens || req.Thinking != nil || !strings.Contains(req.System[1].Text, "no messages yet") || req.Messages[0].Text() != "hi" {
		t.Fatalf("defaults: %+v", req)
	}
}

func TestRequestBudgetFollowsTheContextWindow(t *testing.T) {
	var items [][2]string
	for i := 0; i < 200; i++ {
		items = append(items, [2]string{"user", q(fmt.Sprintf("q%03d %s", i, strings.Repeat("y", 800)))})
	}
	main := conv(t, items...)
	small := Request(RequestOptions{Model: "m", ContextWindow: 32_000, MaxOutputTokens: 2_000, Main: main, Question: "?"})
	large := Request(RequestOptions{Model: "m", ContextWindow: 1_000_000, Main: main, Question: "?"})
	ts, tl := EstimateTokens(small.System[1].Text), EstimateTokens(large.System[1].Text)
	if ts > 16_000 || ts < 8_000 {
		t.Fatalf("small window: %d tokens of main session", ts)
	}
	if !strings.Contains(small.System[1].Text, "earlier messages omitted") || strings.Contains(large.System[1].Text, "omitted") || tl <= ts {
		t.Fatalf("large window must keep everything: small %d, large %d", ts, tl)
	}
}

func TestStore(t *testing.T) {
	dir := DirFor(filepath.Join(t.TempDir(), "proj", "sess.jsonl"))
	if filepath.Base(dir) != "side-chats" || filepath.Base(filepath.Dir(dir)) != "sess" {
		t.Fatalf("dir: %s", dir)
	}
	s := Store{Dir: dir}
	if l, err := s.List(); err != nil || len(l) != 0 {
		t.Fatalf("empty list: %v %v", l, err)
	}
	t0 := time.Now().UTC()
	a := &Chat{ID: NewID("sc_"), SessionID: "sess", Title: DefaultTitle, Model: "m1", Effort: "high", CreatedAt: t0, UpdatedAt: t0}
	b := &Chat{ID: NewID("sc_"), SessionID: "sess", Title: "B", Model: "m2", CreatedAt: t0.Add(time.Second), UpdatedAt: t0}
	for _, c := range []*Chat{b, a} {
		if err := s.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Update(a.ID, func(c *Chat) error {
		c.Messages = append(c.Messages, Message{ID: "m", Role: "user", Text: "hi", Quote: "q", CreatedAt: t0}, Message{ID: "a", Role: "assistant", Text: "hello", Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}, Stopped: true})
		return nil
	})
	if err != nil || len(got.Messages) != 2 {
		t.Fatalf("update: %v %+v", err, got)
	}
	// A fresh store (a reload) reads the same.
	l, err := Store{Dir: dir}.List()
	if err != nil || len(l) != 2 || l[0].ID != a.ID || l[1].ID != b.ID {
		t.Fatalf("list oldest first: %v %+v", err, l)
	}
	if m := l[0].Messages[1]; m.Text != "hello" || m.Usage.OutputTokens != 2 || !m.Stopped || l[0].Effort != "high" {
		t.Fatalf("persisted: %+v", l[0])
	}
	if _, err := s.Get("../escape"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("path escape: %v", err)
	}
	if err := s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(a.ID, func(*Chat) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update after delete: %v", err)
	}
	if err := s.Delete(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	// A damaged file does not hide the others.
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(); len(l) != 1 || l[0].ID != b.ID {
		t.Fatalf("with a damaged file: %+v", l)
	}
	if TitleFrom("  How does the relay work?\nmore") != "How does the relay work?" || TitleFrom("") != DefaultTitle || len([]rune(TitleFrom(strings.Repeat("w", 100)))) != 61 {
		t.Fatal("titles")
	}
}

// fakeGW streams scripted raw events; hold keeps the stream open until the context ends.
type fakeGW struct {
	events []string
	hold   bool
	reqs   []*llm.Request
}

type fakeStream struct {
	ctx    context.Context
	events []string
	hold   bool
}

func (s *fakeStream) Next() (llm.StreamEvent, error) {
	if len(s.events) == 0 {
		if s.hold {
			<-s.ctx.Done()
			return llm.StreamEvent{}, s.ctx.Err()
		}
		return llm.StreamEvent{}, io.EOF
	}
	raw := s.events[0]
	s.events = s.events[1:]
	var t struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(raw), &t)
	return llm.StreamEvent{Type: t.Type, Raw: json.RawMessage(raw)}, nil
}

func (s *fakeStream) Close() error { return nil }

func (g *fakeGW) Stream(ctx context.Context, req *llm.Request) (gateway.EventStream, error) {
	g.reqs = append(g.reqs, req)
	return &fakeStream{ctx: ctx, events: append([]string(nil), g.events...), hold: g.hold}, nil
}
func (g *fakeGW) Models(context.Context) (*llm.Catalog, error) { return &llm.Catalog{}, nil }
func (g *fakeGW) CountTokens(context.Context, *llm.Request) (*llm.TokenCount, error) {
	return &llm.TokenCount{}, nil
}

func textEvents(parts ...string) []string {
	ev := []string{
		`{"type":"message_start","message":{"id":"msg","model":"m","usage":{"input_tokens":120,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	}
	for _, p := range parts {
		ev = append(ev, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+q(p)+`}}`)
	}
	return ev
}

func TestAskStreams(t *testing.T) {
	gw := &fakeGW{events: append(textEvents("Because ", "it works ", "in browsers."),
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`)}
	var deltas []string
	ans, err := Ask(context.Background(), gw, Request(RequestOptions{Model: "m", Question: "why?"}), func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "Because |it works |in browsers." || ans.Text != "Because it works in browsers." {
		t.Fatalf("deltas %q, text %q", deltas, ans.Text)
	}
	if ans.Usage == nil || ans.Usage.InputTokens != 120 || ans.Usage.OutputTokens != 9 {
		t.Fatalf("usage: %+v", ans.Usage)
	}
	if len(gw.reqs) != 1 || len(gw.reqs[0].Tools) != 0 {
		t.Fatalf("requests: %+v", gw.reqs)
	}
}

func TestAskStopKeepsThePartialText(t *testing.T) {
	gw := &fakeGW{events: textEvents("partial ", "answer"), hold: true}
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	ans, err := Ask(ctx, gw, Request(RequestOptions{Model: "m", Question: "?"}), func(string) {
		if n++; n == 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || ans.Text != "partial answer" {
		t.Fatalf("stopped: %v %q", err, ans.Text)
	}
	if ans.Usage == nil || ans.Usage.InputTokens != 120 {
		t.Fatalf("usage so far: %+v", ans.Usage)
	}
}
