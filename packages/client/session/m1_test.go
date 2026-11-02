package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
)

func texts(c Conversation) []string {
	var out []string
	for _, m := range c.Messages {
		for _, b := range m.Content {
			out = append(out, m.Role[:1]+":"+b.Text)
		}
	}
	return out
}

func TestBranchingRewindAndCompaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	w, err := OpenWriter(path, WriterOptions{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	user := func(s, pid string) string {
		id, _ := w.Append(Entry{Type: TypeUser, Message: UserText(s), PromptID: pid})
		return id
	}
	asst := func(s string) string {
		id, _ := w.Append(Entry{Type: TypeAssistant, Message: AssistantMessage(llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentBlock{llm.TextBlock(s)}}})})
		return id
	}
	w.Append(Entry{Type: TypeUser, Message: UserText("ctx"), IsMeta: true, PromptID: "p1"})
	p1 := user("one", "p1")
	asst("a1")
	p2 := user("two", "p2")
	asst("a2")

	// Rewind to before p2.
	parent, ok := TurnParent(w.Entries(), p2)
	if !ok || parent == nil {
		t.Fatal("turn parent")
	}
	w.AppendBranch(Entry{Type: TypeSystem, Subtype: SubtypeRewind}, parent)
	c := Rebuild(w.Entries())
	if got := strings.Join(texts(c), "|"); got != "u:ctx|u:one|a:a1" {
		t.Fatalf("after rewind: %s", got)
	}
	user("two again", "p3")
	asst("a3")
	if got := strings.Join(texts(Rebuild(w.Entries())), "|"); got != "u:ctx|u:one|a:a1|u:two again|a:a3" {
		t.Fatalf("new branch: %s", got)
	}
	// Rewinding to the first prompt skips its context meta entry.
	if p, _ := TurnParent(w.Entries(), p1); p != nil {
		t.Fatalf("first turn parent %v", *p)
	}

	// TodoWrite input is restored and survives a compaction boundary.
	w.Append(Entry{Type: TypeAssistant, Message: AssistantMessage(llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentBlock{
		{Type: llm.BlockToolUse, ID: "t", Name: "TodoWrite", Input: []byte(`{"todos":[{"content":"x","activeForm":"X","status":"pending"}]}`)}}}})})
	leaf := w.LastUUID()
	w.AppendBranch(Entry{Type: TypeSystem, Subtype: SubtypeCompactBoundary, LogicalParentUUID: leaf, CompactMetadata: &CompactMetadata{Trigger: "manual", PreTokens: 100}}, nil)
	w.Append(Entry{Type: TypeUser, Message: UserText("summary"), IsCompactSummary: true})
	c = Rebuild(w.Entries())
	if got := strings.Join(texts(c), "|"); got != "u:summary" {
		t.Fatalf("after compaction: %s", got)
	}
	if !strings.Contains(string(c.Todos), `"content":"x"`) {
		t.Fatalf("todos %s", c.Todos)
	}
	found := false
	for _, id := range c.Chain {
		if id == p1 {
			found = true
		}
	}
	if !found {
		t.Fatal("the extended chain follows logical parents")
	}
	w.Close()
	// Reload from disk gives the same conversation.
	entries, _ := Load(path)
	if got := strings.Join(texts(Rebuild(entries)), "|"); got != "u:summary" {
		t.Fatalf("reloaded: %s", got)
	}
}

func TestFileHistoryCheckpointsAndRestore(t *testing.T) {
	proj := t.TempDir()
	fhDir := filepath.Join(t.TempDir(), "fh")
	path := filepath.Join(t.TempDir(), "s.jsonl")
	w, _ := OpenWriter(path, WriterOptions{SessionID: "s"})
	h := NewFileHistory(fhDir, w, nil)
	a := filepath.Join(proj, "a.txt")
	b := filepath.Join(proj, "b.txt")
	os.WriteFile(a, []byte("a0"), 0o644)

	h.MakeCheckpoint("m1")
	h.TrackEdit(a)
	os.WriteFile(a, []byte("a1"), 0o644)
	h.TrackEdit(a) // second edit in the same turn: no new backup
	os.WriteFile(a, []byte("a1b"), 0o644)

	h.MakeCheckpoint("m2")
	h.TrackEdit(b) // b does not exist yet
	os.WriteFile(b, []byte("b1"), 0o644)
	h.TrackEdit(a)
	os.WriteFile(a, []byte("a2"), 0o644)

	h.MakeCheckpoint("m3")
	if !h.HasChanges("m1") || !h.HasChanges("m2") || h.HasChanges("m3") {
		t.Fatal("HasChanges")
	}
	res, err := h.Restore("m2")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(a); string(got) != "a1b" {
		t.Fatalf("a at m2 = %q", got)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatal("b did not exist at m2")
	}
	if len(res.Restored) != 2 || len(res.Skipped) != 0 {
		t.Fatalf("restore result %+v", res)
	}
	h.Restore("m1")
	if got, _ := os.ReadFile(a); string(got) != "a0" {
		t.Fatalf("a at m1 = %q", got)
	}

	// Symlinks and hardlinks are skipped.
	h.Restore("m3")
	os.Remove(a)
	os.Symlink(b, a)
	res, _ = h.Restore("m1")
	if len(res.Skipped) != 1 || res.Reasons[a] != "symlink" {
		t.Fatalf("symlink skip %+v", res)
	}
	os.Remove(a)
	os.WriteFile(a, []byte("zz"), 0o644)
	os.Link(a, filepath.Join(proj, "hard"))
	res, _ = h.Restore("m1")
	if res.Reasons[a] != "hardlink" {
		t.Fatalf("hardlink skip %+v", res)
	}

	// Snapshots survive a resume through the transcript records.
	w.Close()
	entries, _ := Load(path)
	conv := Rebuild(entries)
	if len(conv.Snapshots) != 3 || conv.Snapshots[1].MessageID != "m2" || len(conv.Snapshots[1].TrackedFileBackups) != 2 {
		t.Fatalf("restored snapshots %+v", conv.Snapshots)
	}
	h2 := NewFileHistory(fhDir, nil, conv.Snapshots)
	os.Remove(filepath.Join(proj, "hard"))
	if _, err := h2.Restore("m2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(a); string(got) != "a1b" {
		t.Fatalf("a after resume restore = %q", got)
	}
}

func TestFileHistoryPruneKeepsFirstSnapshot(t *testing.T) {
	proj := t.TempDir()
	fhDir := filepath.Join(t.TempDir(), "fh")
	h := NewFileHistory(fhDir, nil, nil)
	h.max = 3
	a := filepath.Join(proj, "a.txt")
	os.WriteFile(a, []byte("v0"), 0o644)
	for i := 0; i < 6; i++ {
		h.MakeCheckpoint("m" + string(rune('0'+i)))
		h.TrackEdit(a)
		os.WriteFile(a, []byte("v"+string(rune('1'+i))), 0o644)
	}
	if n := len(h.Checkpoints()); n != 3 {
		t.Fatalf("kept %d checkpoints", n)
	}
	if _, err := os.Stat(filepath.Join(fhDir, pathHash(a)+"@v1")); err != nil {
		t.Fatal("first snapshot of each file is kept")
	}
	files, _ := os.ReadDir(fhDir)
	if len(files) > 5 {
		t.Fatalf("old snapshots not pruned: %d files", len(files))
	}
}

func TestFork(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jsonl")
	w, _ := OpenWriter(src, WriterOptions{SessionID: "a"})
	w.Append(Entry{Type: TypeMeta, PromptBundleVersion: "v1"})
	w.Append(Entry{Type: TypeUser, Message: UserText("one"), PromptID: "1"})
	w.Append(Entry{Type: TypeAssistant, Message: AssistantMessage(llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentBlock{llm.TextBlock("a1")}}})})
	p2, _ := w.Append(Entry{Type: TypeUser, Message: UserText("two"), PromptID: "2"})
	w.Append(Entry{Type: TypeAssistant, Message: AssistantMessage(llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentBlock{llm.TextBlock("a2")}}})})
	w.Close()

	dst := filepath.Join(dir, "b.jsonl")
	if err := Fork(src, dst, "a", "b", ""); err != nil {
		t.Fatal(err)
	}
	entries, _ := Load(dst)
	for _, e := range entries {
		if e.SessionID != "b" {
			t.Fatalf("sessionId not rewritten: %+v", e)
		}
	}
	c := Rebuild(entries)
	if got := strings.Join(texts(c), "|"); got != "u:one|a:a1|u:two|a:a2" || c.ForkedFrom != "a" || c.PromptBundleVersion != "v1" {
		t.Fatalf("fork: %s %+v", got, c)
	}
	at := filepath.Join(dir, "c.jsonl")
	if err := Fork(src, at, "a", "c", p2); err != nil {
		t.Fatal(err)
	}
	entries, _ = Load(at)
	if got := strings.Join(texts(Rebuild(entries)), "|"); got != "u:one|a:a1" {
		t.Fatalf("fork at checkpoint: %s", got)
	}
	if Fork(src, at, "a", "c", "") == nil {
		t.Fatal("existing destination")
	}
}
