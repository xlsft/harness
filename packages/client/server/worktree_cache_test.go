package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLastRecordedReadsOnlyWhatWasAppended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	write := func(s string, flag int) {
		f, err := os.OpenFile(path, flag|os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	write(`{"type":"user","cwd":"/a"}`+"\n"+`{"type":"assistant","message":{"content":"x"}}`+"\n", os.O_TRUNC)
	if cwd, _ := lastRecorded(path); cwd != "/a" {
		t.Fatalf("first read: %q", cwd)
	}
	// An entry being written: half a line, then the rest.
	write(`{"type":"user","cwd":"/b"`, os.O_APPEND)
	if cwd, _ := lastRecorded(path); cwd != "/a" {
		t.Fatalf("half a line: %q", cwd)
	}
	write(`}`+"\n"+`{"type":"harness-meta","worktree":{"path":"/w","branch":"b"}}`+"\n", os.O_APPEND)
	cwd, wt := lastRecorded(path)
	if cwd != "/b" || wt == nil || wt.Path != "/w" {
		t.Fatalf("after the rest: %q %+v", cwd, wt)
	}
	// A replaced, shorter file is read from the start.
	write(`{"type":"user","cwd":"/c"}`+"\n", os.O_TRUNC)
	if cwd, wt := lastRecorded(path); cwd != "/c" || wt != nil {
		t.Fatalf("replaced: %q %+v", cwd, wt)
	}
}

func TestTranscriptHeadFollowsChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","cwd":"/a"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cwd, _ := transcriptHead(path, false); cwd != "/a" {
		t.Fatalf("first: %q", cwd)
	}
	if err := os.WriteFile(path, []byte(`{"type":"harness-meta","worktree":{"path":"/w"}}`+"\n"+`{"type":"user","cwd":"/z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cwd, wt := transcriptHead(path, false); cwd != "/z" || wt == nil || wt.Path != "/w" {
		t.Fatalf("changed: %q %+v", cwd, wt)
	}
}
