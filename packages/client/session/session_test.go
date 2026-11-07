package session

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
)

func TestSlug(t *testing.T) {
	if got := Slug("/Users/me/my.project"); got != "-Users-me-my-project" {
		t.Fatalf("slug %q", got)
	}
	long := "/" + strings.Repeat("a", 300)
	if s := Slug(long); len(s) != 200+1+16 {
		t.Fatalf("long slug length %d", len(s))
	}
}

func TestJSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := Store{Root: dir}
	path := store.Path("/proj", "sid")
	w, err := OpenWriter(path, WriterOptions{SessionID: "sid", Cwd: "/proj", GitBranch: "main", Version: "1", Entrypoint: "sdk-cli"})
	if err != nil {
		t.Fatal(err)
	}
	ctxBlock := llm.TextBlock("<system-reminder>ctx</system-reminder>")
	tu := llm.ContentBlock{Type: llm.BlockToolUse, ID: "t1", Name: "Read", Input: json.RawMessage(`{"file_path":"/a"}`)}
	tr := llm.ContentBlock{Type: llm.BlockToolResult, ToolUseID: "t1", Content: []llm.ContentBlock{llm.TextBlock("1\ta")}}
	mustAppend := func(e Entry) string {
		id, err := w.Append(e)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mustAppend(Entry{Type: TypeMeta, PromptBundleVersion: "v1", Model: "m"})
	first := mustAppend(Entry{Type: TypeUser, Message: UserBlocks([]llm.ContentBlock{ctxBlock}), IsMeta: true})
	mustAppend(Entry{Type: TypeUser, Message: UserText("read /a")})
	asst := mustAppend(Entry{Type: TypeAssistant, Message: AssistantMessage(llm.Response{ID: "msg_1", Model: "m", StopReason: "tool_use", Message: llm.Message{Role: "assistant", Content: []llm.ContentBlock{llm.TextBlock("ok"), tu}}, Usage: llm.Usage{InputTokens: 5}})})
	mustAppend(Entry{Type: TypeUser, Message: UserBlocks([]llm.ContentBlock{tr}), ToolUseResult: map[string]any{"type": "text"}, SourceToolAssistantUUID: asst})
	w.Close()

	entries, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("entries %d", len(entries))
	}
	if entries[1].ParentUUID != nil || entries[2].ParentUUID == nil || *entries[2].ParentUUID != first {
		t.Fatal("parentUuid chain broken")
	}
	if entries[3].Message.Usage.InputTokens != 5 || *entries[3].Message.StopReason != "tool_use" || entries[3].GitBranch != "main" {
		t.Fatalf("assistant entry %+v", entries[3])
	}
	// user prompt stored as a plain string, like Claude Code
	if string(entries[2].Message.Content) != `"read /a"` {
		t.Fatalf("prompt content %s", entries[2].Message.Content)
	}
	conv := Rebuild(entries)
	want := []llm.Message{
		{Role: "user", Content: []llm.ContentBlock{ctxBlock, llm.TextBlock("read /a")}},
		{Role: "assistant", Content: []llm.ContentBlock{llm.TextBlock("ok"), tu}},
		{Role: "user", Content: []llm.ContentBlock{tr}},
	}
	if !reflect.DeepEqual(conv.Messages, want) {
		t.Fatalf("rebuild mismatch:\n%+v\n%+v", conv.Messages, want)
	}
	if !conv.ContextInjected || conv.PromptBundleVersion != "v1" || conv.Model != "m" || conv.LastUUID == "" {
		t.Fatalf("conv meta %+v", conv)
	}
	if id, err := store.Latest("/proj"); err != nil || id != "sid" {
		t.Fatalf("latest %q %v", id, err)
	}
	if p, err := store.Find("/elsewhere", "sid"); err != nil || p != filepath.Clean(path) {
		t.Fatalf("find %q %v", p, err)
	}
}

func TestParentOf(t *testing.T) {
	meta := func(from string, detached bool) Entry {
		return Entry{Type: TypeMeta, ForkedFrom: from, DetachedFromParent: detached}
	}
	cases := []struct {
		name    string
		entries []Entry
		want    string
	}{
		{"never forked", []Entry{{Type: TypeUser, UUID: "u"}}, ""},
		{"fork", []Entry{meta("a", false)}, "a"},
		{"fork of a fork: the last record wins", []Entry{meta("a", false), {Type: TypeUser}, meta("b", false)}, "b"},
		{"detached", []Entry{meta("a", false), meta("", true)}, ""},
		{"fork of a detached session", []Entry{meta("a", false), meta("", true), meta("c", false)}, "c"},
	}
	for _, c := range cases {
		if got := ParentOf(c.entries); got != c.want {
			t.Errorf("%s: ParentOf = %q, want %q", c.name, got, c.want)
		}
		if got := Rebuild(c.entries).ForkedFrom; got != c.want {
			t.Errorf("%s: Rebuild.ForkedFrom = %q, want %q", c.name, got, c.want)
		}
	}
}

// Claude Code parents each result of parallel calls on the entry with its own call; only the last one is on the path
// from the leaf.
func TestRebuildKeepsSideResultsOfParallelCalls(t *testing.T) {
	var raw []string
	line := func(uuid, parent, typ, content string) {
		p := "null"
		if parent != "" {
			p = `"` + parent + `"`
		}
		raw = append(raw, `{"type":"`+typ+`","uuid":"`+uuid+`","parentUuid":`+p+`,"sessionId":"s","timestamp":"","message":{"role":"`+typ+`","content":`+content+`}}`)
	}
	use := func(id string) string { return `[{"type":"tool_use","id":"` + id + `","name":"Read","input":{}}]` }
	result := func(id string) string {
		return `[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok"}]`
	}
	line("u0", "", "user", `"go"`)
	line("a1", "u0", "assistant", use("A"))
	line("a2", "a1", "assistant", use("B"))
	line("a3", "a2", "assistant", use("C"))
	line("rA", "a1", "user", result("A"))
	line("rB", "a2", "user", result("B"))
	line("rC", "a3", "user", result("C"))
	line("a4", "rC", "assistant", `[{"type":"text","text":"done"}]`)
	var entries []Entry
	for _, l := range raw {
		var e Entry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	c := Rebuild(entries)
	if len(c.Messages) != 4 {
		t.Fatalf("messages %+v", c.Messages)
	}
	var got []string
	for _, b := range c.Messages[2].Content {
		got = append(got, b.ToolUseID)
	}
	if !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Fatalf("results %v", got)
	}
	if want := []string{"u0", "a1", "a2", "a3", "rA", "rB", "rC", "a4"}; !reflect.DeepEqual(c.Chain, want) {
		t.Fatalf("chain %v", c.Chain)
	}
}
