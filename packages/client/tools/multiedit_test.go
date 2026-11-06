package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestEnv(t *testing.T) (*Env, string) {
	t.Helper()
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewEnv(dir, t.TempDir()), dir
}

func inputOf(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mtext(r Result) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestMultiEdit(t *testing.T) {
	env, dir := newTestEnv(t)
	path := filepath.Join(dir, "a.txt")
	original := "one\ntwo\nthree\ntwo\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	run := func(edits ...map[string]any) Result {
		return (&MultiEdit{}).Run(context.Background(), env, Call{Input: inputOf(t, map[string]any{"file_path": path, "edits": edits})})
	}
	if r := run(map[string]any{"old_string": "one", "new_string": "1"}); !r.IsError {
		t.Fatalf("unread file edited: %s", mtext(r))
	}
	env.Files.MarkReadContent(path, []byte(original))

	// A failing edit leaves the file as it was, whichever edit it is.
	for name, edits := range map[string][]map[string]any{
		"not found":     {{"old_string": "one", "new_string": "1"}, {"old_string": "nope", "new_string": "x"}},
		"not unique":    {{"old_string": "one", "new_string": "1"}, {"old_string": "two", "new_string": "2"}},
		"gone already":  {{"old_string": "one", "new_string": "1"}, {"old_string": "one", "new_string": "x"}},
		"no change":     {{"old_string": "one", "new_string": "one"}},
		"empty old":     {{"old_string": "", "new_string": "x"}},
		"missing field": {{"old_string": "one"}},
	} {
		r := run(edits...)
		if !r.IsError {
			t.Errorf("%s: no error", name)
		}
		if got, _ := os.ReadFile(path); string(got) != original {
			t.Fatalf("%s: file changed to %q", name, got)
		}
	}

	r := run(map[string]any{"old_string": "one", "new_string": "1"}, map[string]any{"old_string": "two", "new_string": "2", "replace_all": true}, map[string]any{"old_string": "1\n2", "new_string": "1+2"})
	if r.IsError {
		t.Fatal(mtext(r))
	}
	if got, _ := os.ReadFile(path); string(got) != "1+2\nthree\n2\n" {
		t.Fatalf("got %q", got)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestLS(t *testing.T) {
	env, dir := newTestEnv(t)
	for _, f := range []string{"b.txt", "a.log", "sub/inner.txt", ".git/HEAD"} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("xx"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("b.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	r := (&LS{}).Run(context.Background(), env, Call{Input: inputOf(t, map[string]any{"path": dir, "ignore": []string{"*.log"}})})
	if r.IsError {
		t.Fatal(mtext(r))
	}
	want := dir + ":\nsub/\nb.txt  2\nlink@"
	if got := mtext(r); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if r := (&LS{}).Run(context.Background(), env, Call{Input: inputOf(t, map[string]any{"path": filepath.Join(dir, "none")})}); !r.IsError {
		t.Fatal("missing folder, no error")
	}
	// With no path, the current directory.
	if r := (&LS{}).Run(context.Background(), env, Call{Input: inputOf(t, map[string]any{})}); r.IsError || !strings.HasPrefix(mtext(r), dir+":") {
		t.Fatal(mtext(r))
	}
}

func TestMemoryLimitPrefix(t *testing.T) {
	if got := memoryLimitPrefix("linux", 2048); got != "ulimit -v 2097152 2>/dev/null\n" {
		t.Fatalf("%q", got)
	}
	for _, c := range []struct {
		goos string
		mb   int
	}{{"darwin", 2048}, {"windows", 2048}, {"linux", 0}} {
		if got := memoryLimitPrefix(c.goos, c.mb); got != "" {
			t.Fatalf("%v: %q", c, got)
		}
	}
}
