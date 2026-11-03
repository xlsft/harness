package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/llm"
)

func newEnv(t *testing.T) *Env {
	t.Helper()
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	return NewEnv(dir, filepath.Join(t.TempDir(), "scratch"))
}

func call(t *testing.T, env *Env, tool Tool, input any) Result {
	t.Helper()
	raw, _ := json.Marshal(input)
	return tool.Run(context.Background(), env, Call{ID: "toolu_test", Input: raw})
}

func text(r Result) string {
	var sb strings.Builder
	for _, c := range r.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRead(t *testing.T) {
	env := newEnv(t)
	p := filepath.Join(env.Cwd, "a.txt")
	write(t, p, "one\ntwo\nthree\n"+strings.Repeat("x", 2500)+"\n")
	r := call(t, env, &Read{}, map[string]any{"file_path": p})
	if r.IsError || !strings.HasPrefix(text(r), "     1\tone\n     2\ttwo\n") || !strings.Contains(text(r), "[line truncated]") {
		t.Fatalf("read: %q", text(r))
	}
	r = call(t, env, &Read{}, map[string]any{"file_path": p, "offset": 2, "limit": 1})
	if text(r) != "     2\ttwo\n" {
		t.Fatalf("offset/limit: %q", text(r))
	}
	if r := call(t, env, &Read{}, map[string]any{"file_path": "a.txt"}); !r.IsError {
		t.Fatal("relative path must be rejected")
	}
	if r := call(t, env, &Read{}, map[string]any{"file_path": env.Cwd}); !r.IsError || !strings.Contains(text(r), "directory") {
		t.Fatalf("directory: %q", text(r))
	}
	if r := call(t, env, &Read{}, map[string]any{"file_path": filepath.Join(env.Cwd, "nope")}); !r.IsError {
		t.Fatal("missing file")
	}
	empty := filepath.Join(env.Cwd, "empty.txt")
	write(t, empty, "")
	env.Reminders = func(k string) string { return map[string]string{"empty_file": "EMPTY"}[k] }
	if r := call(t, env, &Read{}, map[string]any{"file_path": empty}); text(r) != "EMPTY" {
		t.Fatalf("empty: %q", text(r))
	}
	png := filepath.Join(env.Cwd, "i.png")
	write(t, png, "\x89PNG\r\n\x1a\nfake")
	r = call(t, env, &Read{}, map[string]any{"file_path": png})
	if len(r.Content) != 1 || r.Content[0].Type != llm.BlockImage || r.Content[0].Source.MediaType != "image/png" {
		t.Fatalf("image: %+v", r.Content)
	}
	if d, _ := base64.StdEncoding.DecodeString(r.Content[0].Source.Data); string(d) != "\x89PNG\r\n\x1a\nfake" {
		t.Fatal("image data")
	}
	pdf := filepath.Join(env.Cwd, "d.pdf")
	write(t, pdf, "%PDF-1.4")
	if r := call(t, env, &Read{}, map[string]any{"file_path": pdf}); r.Content[0].Type != llm.BlockDocument {
		t.Fatal("pdf document block")
	}
}

func TestWriteAndEdit(t *testing.T) {
	env := newEnv(t)
	p := filepath.Join(env.Cwd, "sub", "f.go")
	r := call(t, env, &Write{}, map[string]any{"file_path": p, "content": "a b a\n"})
	if r.IsError || !strings.Contains(text(r), "created") {
		t.Fatalf("create: %q", text(r))
	}
	other := filepath.Join(env.Cwd, "other.txt")
	write(t, other, "x")
	if r := call(t, env, &Write{}, map[string]any{"file_path": other, "content": "y"}); !r.IsError || !strings.Contains(text(r), "has not been read") {
		t.Fatalf("overwrite without read: %q", text(r))
	}
	if r := call(t, env, &Edit{}, map[string]any{"file_path": other, "old_string": "x", "new_string": "y"}); !r.IsError {
		t.Fatal("edit without read must fail")
	}
	// p was written by us, so it counts as read.
	if r := call(t, env, &Edit{}, map[string]any{"file_path": p, "old_string": "a", "new_string": "c"}); !r.IsError || !strings.Contains(text(r), "Found 2 matches") {
		t.Fatalf("uniqueness: %q", text(r))
	}
	if r := call(t, env, &Edit{}, map[string]any{"file_path": p, "old_string": "zzz", "new_string": "c"}); !r.IsError || !strings.Contains(text(r), "not found") {
		t.Fatalf("not found: %q", text(r))
	}
	if r := call(t, env, &Edit{}, map[string]any{"file_path": p, "old_string": "b", "new_string": "B"}); r.IsError {
		t.Fatalf("edit: %q", text(r))
	}
	if r := call(t, env, &Edit{}, map[string]any{"file_path": p, "old_string": "a", "new_string": "c", "replace_all": true}); r.IsError {
		t.Fatalf("replace_all: %q", text(r))
	}
	if data, _ := os.ReadFile(p); string(data) != "c B c\n" {
		t.Fatalf("content %q", data)
	}
	// External modification after read is detected.
	time.Sleep(10 * time.Millisecond)
	write(t, p, "changed elsewhere\n")
	if r := call(t, env, &Edit{}, map[string]any{"file_path": p, "old_string": "changed", "new_string": "x"}); !r.IsError || !strings.Contains(text(r), "modified since read") {
		t.Fatalf("stale: %q", text(r))
	}
	call(t, env, &Read{}, map[string]any{"file_path": p})
	if r := call(t, env, &Write{}, map[string]any{"file_path": p, "content": "new"}); r.IsError || !strings.Contains(text(r), "updated") {
		t.Fatalf("overwrite after read: %q", text(r))
	}
}

func TestBash(t *testing.T) {
	env := newEnv(t)
	sub := filepath.Join(env.Cwd, "sub")
	os.Mkdir(sub, 0o755)
	if r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": "cd sub && export FOO=bar"}); r.IsError {
		t.Fatalf("cd: %q", text(r))
	}
	r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": "pwd; echo \"foo=$FOO\""})
	if !strings.Contains(text(r), sub) || !strings.Contains(text(r), "foo=\n") && !strings.HasSuffix(text(r), "foo=") {
		t.Fatalf("cwd persists, env does not: %q", text(r))
	}
	if r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": "cd / "}); !strings.Contains(text(r), "reset") {
		t.Fatalf("cwd outside project must reset: %q", text(r))
	}
	if env.Shell.Cwd() != env.Cwd {
		t.Fatal("cwd not reset")
	}
	if r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": "grep nothing-here /dev/null"}); r.IsError {
		t.Fatalf("grep exit 1 is not an error: %q", text(r))
	}
	if r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": "echo oops >&2; exit 3"}); !r.IsError || !strings.Contains(text(r), "oops") || !strings.Contains(text(r), "Exit code 3") {
		t.Fatalf("failure: %q", text(r))
	}
	r = call(t, env, &Bash{}, map[string]any{"description": "test", "command": "head -c 40000 /dev/zero | tr '\\0' 'a'"})
	if r.IsError || !strings.Contains(text(r), "Full output saved to:") {
		t.Fatalf("large output: %q", text(r)[:200])
	}
	saved := r.Structured.(map[string]any)["outputFile"].(string)
	if data, _ := os.ReadFile(saved); len(data) != 40000 {
		t.Fatalf("saved output length %d", len(data))
	}
	start := time.Now()
	r = call(t, env, &Bash{}, map[string]any{"description": "test", "command": "sleep 5 & sleep 5; echo never", "timeout": 300})
	if !r.IsError || !strings.Contains(text(r), "timed out") || time.Since(start) > 4*time.Second {
		t.Fatalf("timeout: %q after %s", text(r), time.Since(start))
	}
	if r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": "true", "run_in_background": true}); !r.IsError {
		t.Fatal("background needs a task manager")
	}
}

func TestBashInterruptKillsProcessGroup(t *testing.T) {
	env := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	raw, _ := json.Marshal(map[string]any{"description": "test", "command": "sleep 30 | cat"})
	r := (&Bash{}).Run(ctx, env, Call{ID: "x", Input: raw})
	if time.Since(start) > 5*time.Second {
		t.Fatalf("interrupt took %s", time.Since(start))
	}
	if !r.Structured.(map[string]any)["interrupted"].(bool) {
		t.Fatalf("not marked interrupted: %+v", r.Structured)
	}
}

func TestExitOneIsOK(t *testing.T) {
	for cmd, want := range map[string]bool{
		"grep x f": true, "cat f | rg x": true, "FOO=1 diff a b": true, "git diff --stat": true,
		"git grep foo": true, "test -f x": true, "[ -f x ]": true, "make": false, "git status": false, "grep x f && false": false,
	} {
		if got := exitOneIsOK(cmd); got != want {
			t.Errorf("%q = %v, want %v", cmd, got, want)
		}
	}
}

func TestGlob(t *testing.T) {
	env := newEnv(t)
	old := filepath.Join(env.Cwd, "old.go")
	newer := filepath.Join(env.Cwd, "pkg", "new.go")
	write(t, old, "")
	write(t, newer, "")
	write(t, filepath.Join(env.Cwd, ".git", "x.go"), "")
	write(t, filepath.Join(env.Cwd, "readme.md"), "")
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)
	r := call(t, env, &Glob{}, map[string]any{"pattern": "**/*.go"})
	if text(r) != newer+"\n"+old {
		t.Fatalf("glob: %q", text(r))
	}
	for i := 0; i < 105; i++ {
		write(t, filepath.Join(env.Cwd, "many", strings.Repeat("f", 1)+string(rune('a'+i%26))+strings.Repeat("x", i/26)+".txt"), "")
	}
	r = call(t, env, &Glob{}, map[string]any{"pattern": "*.txt", "path": "many"})
	if !strings.Contains(text(r), "truncated") || r.Structured.(map[string]any)["numFiles"].(int) != 100 {
		t.Fatalf("cap: %v", r.Structured)
	}
	if r := call(t, env, &Glob{}, map[string]any{"pattern": "*.nothing"}); text(r) != "No files found" {
		t.Fatal(text(r))
	}
}

func grepFixture(t *testing.T) *Env {
	env := newEnv(t)
	write(t, filepath.Join(env.Cwd, ".gitignore"), "ignored/\n")
	write(t, filepath.Join(env.Cwd, "a.go"), "package a\nfunc Hello() {}\n// hello world\n")
	write(t, filepath.Join(env.Cwd, "b.txt"), "nothing\nHELLO again\nend\n")
	write(t, filepath.Join(env.Cwd, "ignored", "c.go"), "func Hello() {}\n")
	os.MkdirAll(filepath.Join(env.Cwd, ".git"), 0o755) // make rg treat .gitignore as a repo ignore
	return env
}

func TestGrep(t *testing.T) {
	modes := map[string]string{"go": "-"}
	if p := ripgrepPath(&Env{}); p != "" {
		modes["rg"] = p
	}
	for name, rg := range modes {
		t.Run(name, func(t *testing.T) {
			env := grepFixture(t)
			env.RipgrepPath = rg
			a, b := filepath.Join(env.Cwd, "a.go"), filepath.Join(env.Cwd, "b.txt")
			r := call(t, env, &Grep{}, map[string]any{"pattern": "hello", "-i": true})
			if !strings.HasPrefix(text(r), "Found 2 files") || strings.Contains(text(r), "ignored") {
				t.Fatalf("files_with_matches: %q", text(r))
			}
			r = call(t, env, &Grep{}, map[string]any{"pattern": "Hello", "output_mode": "content", "glob": "*.go"})
			if text(r) != a+":2:func Hello() {}" {
				t.Fatalf("content: %q", text(r))
			}
			r = call(t, env, &Grep{}, map[string]any{"pattern": "HELLO", "output_mode": "content", "-C": 1, "path": b})
			if !strings.Contains(text(r), "1-nothing") || !strings.Contains(text(r), "2:HELLO again") || !strings.Contains(text(r), "3-end") {
				t.Fatalf("context: %q", text(r))
			}
			r = call(t, env, &Grep{}, map[string]any{"pattern": "(?i)hello", "output_mode": "count", "type": "go"})
			if !strings.HasPrefix(text(r), a+":2") {
				t.Fatalf("count: %q", text(r))
			}
			r = call(t, env, &Grep{}, map[string]any{"pattern": "e", "output_mode": "content", "head_limit": 2})
			if strings.Count(text(r), "\n") > 4 || !strings.Contains(text(r), "pagination") {
				t.Fatalf("head_limit: %q", text(r))
			}
			r = call(t, env, &Grep{}, map[string]any{"pattern": "Hello\\(\\) \\{\\}\n// hello", "multiline": true})
			if !strings.Contains(text(r), a) {
				t.Fatalf("multiline: %q", text(r))
			}
			if r := call(t, env, &Grep{}, map[string]any{"pattern": "zzzz"}); text(r) != "No files found" {
				t.Fatalf("none: %q", text(r))
			}
		})
	}
}

func TestBashShellSnapshot(t *testing.T) {
	bash := "/bin/bash"
	if _, err := os.Stat(bash); err != nil {
		t.Skip("no /bin/bash")
	}
	home := t.TempDir()
	write(t, filepath.Join(home, ".bash_profile"), "alias hello='echo hello-from-alias'\ngreet() { echo \"hi $1\"; }\n")
	t.Setenv("HOME", home)
	t.Setenv("SHELL", bash)
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	env := NewEnv(dir, filepath.Join(t.TempDir(), "scratch"))
	r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": "hello; greet bob"})
	if r.IsError || !strings.Contains(text(r), "hello-from-alias") || !strings.Contains(text(r), "hi bob") {
		t.Fatalf("snapshot aliases/functions not applied: %q", text(r))
	}
}
