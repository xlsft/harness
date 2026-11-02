package session

import (
	"path/filepath"
	"testing"
)

func TestWorktreeMetaRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	w, err := OpenWriter(p, WriterOptions{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	first := &Worktree{Path: "/r/.harness/worktrees/a", Branch: "harness/a", BaseRef: "main", RepositoryPath: "/r"}
	second := &Worktree{Path: "/r/.harness/worktrees/b", Branch: "harness/b", RepositoryPath: "/r"}
	w.Append(Entry{Type: TypeMeta, Worktree: first})
	w.Append(Entry{Type: TypeUser, Message: UserText("hi")})
	w.Append(Entry{Type: TypeMeta, Worktree: second})
	w.Close()
	entries, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c := Rebuild(entries)
	if c.Worktree == nil || *c.Worktree != *second {
		t.Fatalf("worktree %+v", c.Worktree)
	}
	if c := Rebuild(entries[:1]); c.Worktree == nil || *c.Worktree != *first {
		t.Fatalf("first worktree %+v", c.Worktree)
	}
	if c := Rebuild(entries[1:2]); c.Worktree != nil {
		t.Fatalf("no worktree expected: %+v", c.Worktree)
	}
}
