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

func TestAutoModeReminder(t *testing.T) {
	mode := "auto"
	a := New(Config{PermissionMode: func() string { return mode }})
	pending := func() int {
		n := 0
		for _, r := range a.reminders {
			if r.id == "auto_mode" {
				n++
			}
		}
		a.reminders = nil // delivered with the next request
		return n
	}
	a.remindAutoMode()
	if n := pending(); n != 1 {
		t.Fatalf("entering auto: %d reminders", n)
	}
	a.remindAutoMode()
	if n := pending(); n != 0 {
		t.Fatalf("still auto: %d reminders", n)
	}
	mode = "default"
	a.remindAutoMode()
	mode = "auto"
	a.remindAutoMode()
	if n := pending(); n != 1 {
		t.Fatalf("auto again after default: %d reminders", n)
	}
	a.autoReminded = false // what a compaction does
	a.remindAutoMode()
	if n := pending(); n != 1 {
		t.Fatalf("after compaction: %d reminders", n)
	}
}
