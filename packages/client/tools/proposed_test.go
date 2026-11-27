package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func proposeInput(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProposeEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("one two two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "new.txt")

	for _, c := range []struct {
		name   string
		tool   string
		in     map[string]any
		after  string
		exists bool
	}{
		{"write over", "Write", map[string]any{"file_path": path, "content": "x"}, "x", true},
		{"write new", "Write", map[string]any{"file_path": missing, "content": "x"}, "x", false},
		{"edit once", "Edit", map[string]any{"file_path": path, "old_string": "one", "new_string": "1"}, "1 two two\n", true},
		{"edit all", "Edit", map[string]any{"file_path": path, "old_string": "two", "new_string": "2", "replace_all": true}, "one 2 2\n", true},
		{"edit creates", "Edit", map[string]any{"file_path": missing, "old_string": "", "new_string": "fresh"}, "fresh", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := ProposeEdit(c.tool, proposeInput(t, c.in))
			if err != nil {
				t.Fatal(err)
			}
			if p.After != c.after || p.Exists != c.exists {
				t.Errorf("%+v", p)
			}
			if c.exists && p.Before != "one two two\n" {
				t.Errorf("before: %q", p.Before)
			}
		})
	}
	if b, _ := os.ReadFile(path); string(b) != "one two two\n" {
		t.Errorf("proposing changed the file: %q", b)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("proposing created a file")
	}
}

func TestProposeEditHasNothingToShow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("two two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(binary, []byte{0xff, 0xfe, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", maxProposedBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		tool string
		in   map[string]any
	}{
		"relative":       {"Write", map[string]any{"file_path": "a.txt", "content": "x"}},
		"a directory":    {"Write", map[string]any{"file_path": dir, "content": "x"}},
		"no content":     {"Write", map[string]any{"file_path": path}},
		"not unique":     {"Edit", map[string]any{"file_path": path, "old_string": "two", "new_string": "2"}},
		"no match":       {"Edit", map[string]any{"file_path": path, "old_string": "nine", "new_string": "9"}},
		"no such file":   {"Edit", map[string]any{"file_path": filepath.Join(dir, "gone"), "old_string": "a", "new_string": "b"}},
		"not empty":      {"Edit", map[string]any{"file_path": path, "old_string": "", "new_string": "b"}},
		"not text":       {"Write", map[string]any{"file_path": binary, "content": "x"}},
		"too large":      {"Write", map[string]any{"file_path": large, "content": "x"}},
		"result too big": {"Write", map[string]any{"file_path": path, "content": strings.Repeat("y", maxProposedBytes+1)}},
		"another tool":   {"NotebookEdit", map[string]any{"file_path": path}},
	} {
		if p, err := ProposeEdit(c.tool, proposeInput(t, c.in)); err == nil {
			t.Errorf("%s: proposed %+v", name, p)
		}
	}
}
