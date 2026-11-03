package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
)

func TestEstimateTokens(t *testing.T) {
	for chars, want := range map[int]int{0: 0, -3: 0, 1: 1, 4: 1, 5: 2, 400: 100} {
		if got := EstimateTextTokens(chars); got != want {
			t.Fatalf("EstimateTextTokens(%d) = %d, want %d", chars, got, want)
		}
	}
	d := llm.ToolDef{Name: "Read", Description: strings.Repeat("d", 36), InputSchema: json.RawMessage(`{"a":1}`)}
	if got := EstimateToolTokens(d); got != EstimateTextTokens(4+36+7) {
		t.Fatalf("EstimateToolTokens = %d", got)
	}
}

func TestContextUsageBreakdown(t *testing.T) {
	bundle := &prompts.Bundle{
		System: []prompts.Section{{ID: "intro", Text: strings.Repeat("s", 4000)}},
		Tools: map[string]prompts.ToolPrompt{
			"Read": {Description: strings.Repeat("r", 794), InputSchema: json.RawMessage(`{}`)},
		},
	}
	mcpTool := llm.ToolDef{Name: "mcp__docs__search", Description: strings.Repeat("m", 381), InputSchema: json.RawMessage(`{}`)}
	history := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(strings.Repeat("u", 2000))}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(strings.Repeat("a", 2000))}},
	}
	a := New(Config{
		Bundle: bundle, Tools: tools.NewRegistry(&tools.Read{}), ContextWindow: 10_000, AutoCompactPct: 0.9,
		ExtraTools: func() []llm.ToolDef { return []llm.ToolDef{mcpTool} },
		History:    session.Conversation{Messages: history},
	})

	// No provider count yet: everything is estimated at ~4 characters per token.
	u := a.ContextUsage()
	if !u.Estimated || u.SystemPrompt != 1000 || u.SystemTools != 200 || u.MCPTools != 100 || u.Messages != 1000 {
		t.Fatalf("estimated breakdown %+v", u)
	}
	if u.Used() != 2300 || u.Threshold != 9000 || u.Buffer() != 1000 || u.Free() != 10_000-2300-1000 || u.UntilAutoCompact() != 9000-2300 {
		t.Fatalf("totals used=%d threshold=%d buffer=%d free=%d until=%d", u.Used(), u.Threshold, u.Buffer(), u.Free(), u.UntilAutoCompact())
	}
	if f := u.Fraction(); f != 0.23 {
		t.Fatalf("fraction %v", f)
	}

	// With the provider's count, messages are what it counted minus the estimated system prompt and tools, plus
	// the estimate of the messages added since.
	a.noteUsage(llm.Usage{InputTokens: 3000, CacheReadInputTokens: 1500, OutputTokens: 500})
	a.appendMessage(llm.RoleUser, []llm.ContentBlock{llm.TextBlock(strings.Repeat("n", 400))})
	u = a.ContextUsage()
	if u.Estimated || u.Messages != 5000+100-1300 || u.Used() != 5100 {
		t.Fatalf("provider breakdown %+v used=%d", u, u.Used())
	}
	if u.Used() != a.EstimateTokens() {
		t.Fatalf("used %d differs from the auto-compaction estimate %d", u.Used(), a.EstimateTokens())
	}

	// A provider count below the estimated overhead never yields negative messages.
	a.noteUsage(llm.Usage{InputTokens: 10})
	if u = a.ContextUsage(); u.Messages != 0 {
		t.Fatalf("messages %d", u.Messages)
	}

	// Auto-compaction off: no threshold, no buffer, everything else is free.
	a.mu.Lock()
	a.autoCompactDisabled = true
	a.mu.Unlock()
	u = a.ContextUsage()
	if u.Threshold != 0 || u.Buffer() != 0 || u.UntilAutoCompact() != 0 || u.Free() != 10_000-u.Used() {
		t.Fatalf("disabled %+v", u)
	}

	// Unknown window.
	if (ContextUsage{SystemPrompt: 5}).Fraction() != 0 || (ContextUsage{SystemPrompt: 5}).Free() != 0 {
		t.Fatal("unknown window")
	}
}

func TestCompactCommand(t *testing.T) {
	for in, want := range map[string]string{"/compact": "", "  /compact  ": "", "/compact keep the API notes": "keep the API notes", "/compact\nfocus": "focus"} {
		if got, ok := CompactCommand(in); !ok || got != want {
			t.Fatalf("CompactCommand(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"/compactor", "please /compact", ""} {
		if _, ok := CompactCommand(in); ok {
			t.Fatalf("CompactCommand(%q) matched", in)
		}
	}
}
