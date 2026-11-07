package insights

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

func writeTranscript(t *testing.T, path string, cwd string, cost float64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := session.OpenWriter(path, session.WriterOptions{SessionID: "s", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	must := func(e session.Entry) {
		if _, err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	must(session.Entry{Type: session.TypeUser, Message: session.UserBlocks([]llm.ContentBlock{llm.TextBlock("do it")})})
	content, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "ok"},
		{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "git status --short"}},
		{"type": "tool_use", "id": "t2", "name": "Read", "input": map[string]any{"file_path": "/x"}},
	})
	must(session.Entry{Type: session.TypeAssistant, Message: &session.Message{ID: "m1", Role: "assistant", Model: "fake-model", Content: content, CostUSD: cost,
		Usage: &llm.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 300}}})
	must(session.Entry{Type: session.TypeUser, Message: session.UserBlocks([]llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1", IsError: true}, {Type: llm.BlockToolResult, ToolUseID: "t2"}})})
}

func TestBuild(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, filepath.Join(dir, "p1", "a.jsonl"), "/work/one", 0.5)
	writeTranscript(t, filepath.Join(dir, "p1", "a", "subagents", "agent-1.jsonl"), "/work/one", 0.25)
	writeTranscript(t, filepath.Join(dir, "p2", "b.jsonl"), "/work/two", 1)
	old := filepath.Join(dir, "p3", "old.jsonl")
	writeTranscript(t, old, "/work/old", 100)
	long := time.Now().AddDate(0, 0, -90)
	if err := os.Chtimes(old, long, long); err != nil {
		t.Fatal(err)
	}

	r := Build(Options{Dir: dir})
	if r.Sessions != 2 || r.Agents != 1 || r.Prompts != 3 || r.ModelRequests != 3 {
		t.Fatalf("%+v", r)
	}
	if r.CostUSD != 1.75 || r.InputTokens != 300 || r.OutputTokens != 150 || r.CacheReadTokens != 900 {
		t.Fatalf("usage %+v", r)
	}
	if r.ToolCalls != 6 || r.ToolErrors != 3 {
		t.Fatalf("tools %+v", r)
	}
	if len(r.Commands) != 1 || r.Commands[0] != (Count{"git status", 3}) {
		t.Fatalf("commands %+v", r.Commands)
	}
	if len(r.Folders) != 2 || r.Folders[0].Name != "/work/one" {
		t.Fatalf("folders %+v", r.Folders)
	}
	if len(r.Models) != 1 || r.Models[0].Tokens != 1350 {
		t.Fatalf("models %+v", r.Models)
	}
	text := r.Text()
	for _, want := range []string{"2 sessions", "$1.75", "git status", "/work/two", "75% of the input"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
	if got := Build(Options{Dir: dir, Cwd: "/work/two"}); got.Sessions != 1 || got.CostUSD != 1 {
		t.Fatalf("one folder %+v", got)
	}
	if !strings.Contains(Build(Options{Dir: t.TempDir()}).Text(), "0 sessions") {
		t.Fatal("empty report")
	}
}
