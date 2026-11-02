package agent

import (
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
)

func TestRemoveQueued(t *testing.T) {
	a := New(Config{})
	msg := func(s string) []llm.ContentBlock { return []llm.ContentBlock{{Type: "text", Text: s}} }
	a.Enqueue("a", msg("one"))
	a.Enqueue("b", msg("two"))
	a.Enqueue("", msg("three"))
	if a.RemoveQueued("") || a.RemoveQueued("zzz") {
		t.Fatal("removed a message that is not queued by that id")
	}
	if !a.RemoveQueued("b") || a.RemoveQueued("b") {
		t.Fatal("b should be removed exactly once")
	}
	got := a.DrainQueue()
	if len(got) != 2 || got[0].Text != "one" || got[1].Text != "three" {
		t.Fatalf("drained %+v", got)
	}
	if a.RemoveQueued("a") {
		t.Fatal("a was already drained")
	}
}
