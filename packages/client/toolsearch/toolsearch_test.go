package toolsearch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/tools"
)

// TestEveryBundleToolIsClassified keeps the classification complete: a tool added to the prompt bundle must be put in
// one tier or the other on purpose.
func TestEveryBundleToolIsClassified(t *testing.T) {
	dir := filepath.Join("..", "..", "core", "prompts", "tools")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skip("packages/core/prompts is not next to this module")
	}
	var missing []string
	have := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		have[e.Name()] = true
		if !Classified(e.Name()) {
			missing = append(missing, e.Name())
		}
	}
	if len(missing) > 0 {
		t.Fatalf("classify these tools in builtinTiers (classify.go): %v", missing)
	}
	for n := range builtinTiers {
		if !have[n] {
			t.Errorf("builtinTiers names %s, which the bundle does not have", n)
		}
	}
}

// TestSystemPromptToolsStayReachable: every tool the static system prompt names is either always loaded or deferred
// (and then reachable by a direct call, which loads it); the core coding set is never deferred.
func TestCoreToolsAreAlwaysLoaded(t *testing.T) {
	for _, n := range []string{"Read", "Write", "Edit", "Glob", "Grep", "Bash", "TodoWrite", "AskUserQuestion", "Agent", "WrapUp", "ToolSearch", "Skill"} {
		if Classify(n) != Always {
			t.Errorf("%s must be always loaded", n)
		}
	}
	if Classify("SomeNewTool") != Always {
		t.Error("an unknown tool must be always loaded")
	}
}

func TestSummary(t *testing.T) {
	cases := map[string]string{
		"Reads a file. More text here.":              "Reads a file.",
		"Opens a page, e.g. a docs site. Then more.": "Opens a page, e.g. a docs site.",
		"First line without period\nsecond line":     "First line without period",
		"":                                           "",
		strings.Repeat("word ", 60):                  strings.TrimSpace(strings.Repeat("word ", 32)) + "...",
	}
	for in, want := range cases {
		if got := Summary(in); got != want {
			t.Errorf("Summary(%q) = %q, want %q", in, got, want)
		}
	}
}

func defs(names ...string) []llm.ToolDef {
	var out []llm.ToolDef
	for _, n := range names {
		out = append(out, llm.ToolDef{Name: n, Description: "Does " + n + ". Details.", InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return out
}

func TestLoaderLoadsInOrderAndRecords(t *testing.T) {
	l := NewLoader(true)
	l.AddSource(SourceFunc(func() []llm.ToolDef { return defs("PrStatus", "BrowserNavigate") }))
	l.AddSource(SourceFunc(func() []llm.ToolDef { return defs("mcp__gh__create_issue") }))
	var recorded [][]string
	l.OnLoad(func(n []string) { recorded = append(recorded, n) })

	if got := l.Load("BrowserNavigate", "Read", "BrowserNavigate"); !slices.Equal(got, []string{"BrowserNavigate"}) {
		t.Fatalf("Load = %v", got)
	}
	l.Load("mcp__gh__create_issue", "PrStatus")
	if got := l.Order(); !slices.Equal(got, []string{"BrowserNavigate", "mcp__gh__create_issue", "PrStatus"}) {
		t.Fatalf("Order = %v", got)
	}
	if len(recorded) != 2 || !slices.Equal(recorded[1], []string{"mcp__gh__create_issue", "PrStatus"}) {
		t.Fatalf("recorded = %v", recorded)
	}
	if l.Reminder() != nil {
		t.Fatalf("nothing pending, reminder = %v", l.Reminder())
	}
	l.Forget("mcp__gh__create_issue")
	if got := l.Order(); !slices.Equal(got, []string{"BrowserNavigate", "PrStatus"}) {
		t.Fatalf("Order after Forget = %v", got)
	}
	if v := l.Reminder()["tools"]; v != "mcp__gh__create_issue: Does mcp__gh__create_issue." {
		t.Fatalf("reminder = %q", v)
	}
}

func TestLoaderRestore(t *testing.T) {
	l := NewLoader(true)
	l.AddSource(SourceFunc(func() []llm.ToolDef { return defs("PrStatus", "PrBind", "mcp__slack__send_message") }))
	l.Restore([]string{"PrBind"})
	l.RestoreFromHistory([]llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockToolUse, ID: "1", Name: Name, Input: json.RawMessage(`{"query":"select:send_message"}`)},
		{Type: llm.BlockToolUse, ID: "2", Name: "PrStatus", Input: json.RawMessage(`{}`)},
		{Type: llm.BlockToolUse, ID: "3", Name: "Read", Input: json.RawMessage(`{}`)},
	}}})
	if got := l.Order(); !slices.Equal(got, []string{"PrBind", "mcp__slack__send_message", "PrStatus"}) {
		t.Fatalf("Order = %v", got)
	}
}

func TestToolSearchQueries(t *testing.T) {
	l := NewLoader(true)
	l.AddSource(SourceFunc(func() []llm.ToolDef {
		return defs("BrowserNavigate", "BrowserClick", "PrStatus", "mcp__gh__list_issues", "mcp__slack__send_message")
	}))
	s := &Tool{L: l}
	run := func(q string) tools.Result {
		in, _ := json.Marshal(map[string]any{"query": q})
		return s.Run(context.Background(), nil, tools.Call{Input: in})
	}
	r := run("select:prstatus,BrowserClick,nope")
	if r.IsError || !strings.Contains(r.Content[0].Text, `"name":"PrStatus"`) || !strings.Contains(r.Content[0].Text, `"name":"BrowserClick"`) {
		t.Fatalf("select = %+v", r)
	}
	if !l.Loaded("PrStatus") || !l.Loaded("BrowserClick") {
		t.Fatal("select did not load")
	}
	// Keyword search splits built-in names at case changes.
	r = run("browser navigate")
	if got := r.Structured.(map[string]any)["matches"].([]string); len(got) == 0 || got[0] != "BrowserNavigate" {
		t.Fatalf("keyword matches = %v", got)
	}
	r = run("+slack send")
	if got := r.Structured.(map[string]any)["matches"].([]string); !slices.Equal(got, []string{"mcp__slack__send_message"}) {
		t.Fatalf("required word matches = %v", got)
	}
	if r := run("zzz"); r.IsError || r.Summary != "No matches" {
		t.Fatalf("no match = %+v", r)
	}
	if r := run(" "); !r.IsError {
		t.Fatal("empty query must fail")
	}
}

func TestNilLoader(t *testing.T) {
	var l *Loader
	if l.DefersBuiltin("PrStatus") || l.Loaded("x") || l.Load("x") != nil || l.Reminder() != nil || l.Order() != nil {
		t.Fatal("a nil loader defers nothing")
	}
	if NewLoader(false).DefersBuiltin("PrStatus") {
		t.Fatal("built-in deferral off")
	}
}
