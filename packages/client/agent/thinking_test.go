package agent

import (
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
)

func TestThinkingRequest(t *testing.T) {
	if th := New(Config{}).thinking(); th != nil {
		t.Fatalf("model without thinking: %+v", th)
	}
	a := New(Config{ThinkingCapable: true})
	if th := a.thinking(); th == nil || th.Type != "adaptive" {
		t.Fatalf("thinking-capable model: %+v", th)
	}
	a.SetModelThinking(false)
	if th := a.thinking(); th != nil {
		t.Fatalf("after switching to a model without thinking: %+v", th)
	}
	explicit := &llm.Thinking{Type: "enabled", BudgetTokens: 4000}
	if th := New(Config{ThinkingCapable: true, Thinking: explicit}).thinking(); th != explicit {
		t.Fatalf("explicit config ignored: %+v", th)
	}
}
