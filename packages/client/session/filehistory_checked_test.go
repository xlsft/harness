package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrackEditCheckedReportsAFailedSnapshot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.docx")
	if err := os.WriteFile(file, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The history folder cannot be made: a file is in the way.
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewFileHistory(filepath.Join(blocker, "history"), nil, nil)
	h.MakeCheckpoint("m1")
	if err := h.TrackEditChecked(file); err == nil {
		t.Fatal("no snapshot was taken, and the caller must hear it")
	}
	// The plain TrackEdit stays as it was for the other tools.
	h.TrackEdit(file)
}

func TestTrackEditCheckedWithoutACheckpointIsNotAFailure(t *testing.T) {
	h := NewFileHistory(t.TempDir(), nil, nil)
	if err := h.TrackEditChecked("/nowhere"); err != nil {
		t.Fatal(err)
	}
}

func TestTrackEditCheckedNoticesALostSnapshot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.docx")
	if err := os.WriteFile(file, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "history")
	h := NewFileHistory(store, nil, nil)
	h.MakeCheckpoint("m1")
	if err := h.TrackEditChecked(file); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(store)
	for _, e := range entries {
		os.Remove(filepath.Join(store, e.Name()))
	}
	if err := h.TrackEditChecked(file); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("%v", err)
	}
}

func TestCheckpointFailureMakesTheNextCheckedEditBackUpAgain(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.docx")
	if err := os.WriteFile(file, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "history")
	h := NewFileHistory(store, nil, nil)
	h.MakeCheckpoint("m1")
	if err := h.TrackEditChecked(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The next turn's checkpoint cannot read the file, so it carries the old backup forward.
	if err := os.Rename(file, file+".away"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o755); err != nil {
		t.Fatal(err)
	}
	h.MakeCheckpoint("m2")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file+".away", file); err != nil {
		t.Fatal(err)
	}
	if err := h.TrackEditChecked(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("three"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Restore("m2"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file)
	if string(got) != "two" {
		t.Errorf("restored %q, want the state before the edit", got)
	}
}

func TestRestoreIsAtomicAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.xlsx")
	if err := os.WriteFile(file, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "history")
	h := NewFileHistory(store, nil, nil)
	h.MakeCheckpoint("m1")
	if err := h.TrackEditChecked(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(file)
	res, err := h.Restore("m1")
	if err != nil || len(res.Restored) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	after, _ := os.Stat(file)
	got, _ := os.ReadFile(file)
	if string(got) != "original" || after.Mode().Perm() != 0o640 {
		t.Errorf("%q %o", got, after.Mode().Perm())
	}
	if os.SameFile(before, after) {
		t.Error("the restore wrote in place: a crash could have left half a file")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".harness") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}
