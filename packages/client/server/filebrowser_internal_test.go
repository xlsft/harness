package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

func TestFileScopeResolve(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	proj := filepath.Join(base, "proj")
	extra := filepath.Join(base, "extra")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(proj, "src"), extra, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fbWrite(t, filepath.Join(proj, "src", "a.go"), "package a\n")
	fbWrite(t, filepath.Join(extra, "notes.md"), "# notes\n")
	fbWrite(t, filepath.Join(outside, "secret"), "secret\n")
	fbMust(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(proj, "leak")))
	fbMust(t, os.Symlink(outside, filepath.Join(proj, "leakdir")))
	fbMust(t, os.Symlink(filepath.Join(proj, "src", "a.go"), filepath.Join(proj, "alias.go")))
	sc := &fileScope{cwd: proj, dirs: []string{proj, extra}}

	ok := map[string]string{
		"":                               proj,
		"src/a.go":                       filepath.Join(proj, "src", "a.go"),
		filepath.Join(proj, "src"):       filepath.Join(proj, "src"),
		"src/../src/a.go":                filepath.Join(proj, "src", "a.go"),
		filepath.Join(extra, "notes.md"): filepath.Join(extra, "notes.md"),
		"alias.go":                       filepath.Join(proj, "alias.go"),
	}
	for in, want := range ok {
		got, _, err := sc.resolve(in)
		if err != nil || got != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	denied := []string{
		"../outside/secret",
		filepath.Join(outside, "secret"),
		"src/../../outside",
		"leak",            // a symlink to a file outside
		"leakdir/secret",  // through a symlinked directory
		"leakdir",         // the symlinked directory itself
		"../outside/none", // missing, and outside anyway
		"/etc/passwd",
	}
	for _, in := range denied {
		if _, _, err := sc.resolve(in); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("resolve(%q): %v; want permission denied", in, err)
		}
	}
	if _, _, err := sc.resolve("src/missing.go"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("missing file: %v", err)
	}
	if _, _, err := sc.resolve("~/x"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("home path: %v", err)
	}
}

func TestGitStatusCode(t *testing.T) {
	cases := map[string]agentv1.GitFileStatus{
		"??": agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED,
		"A ": agentv1.GitFileStatus_GIT_FILE_STATUS_ADDED,
		"AM": agentv1.GitFileStatus_GIT_FILE_STATUS_ADDED,
		"M ": agentv1.GitFileStatus_GIT_FILE_STATUS_MODIFIED,
		" M": agentv1.GitFileStatus_GIT_FILE_STATUS_MODIFIED,
		"MM": agentv1.GitFileStatus_GIT_FILE_STATUS_MODIFIED,
		" T": agentv1.GitFileStatus_GIT_FILE_STATUS_MODIFIED,
		"D ": agentv1.GitFileStatus_GIT_FILE_STATUS_DELETED,
		" D": agentv1.GitFileStatus_GIT_FILE_STATUS_DELETED,
		"MD": agentv1.GitFileStatus_GIT_FILE_STATUS_DELETED,
		"R ": agentv1.GitFileStatus_GIT_FILE_STATUS_RENAMED,
		"C ": agentv1.GitFileStatus_GIT_FILE_STATUS_ADDED,
		"UU": agentv1.GitFileStatus_GIT_FILE_STATUS_CONFLICTED,
		"AA": agentv1.GitFileStatus_GIT_FILE_STATUS_CONFLICTED,
		"DD": agentv1.GitFileStatus_GIT_FILE_STATUS_CONFLICTED,
		"UD": agentv1.GitFileStatus_GIT_FILE_STATUS_CONFLICTED,
	}
	for code, want := range cases {
		if got := gitStatusCode(code[0], code[1]); got != want {
			t.Errorf("%q = %v; want %v", code, got, want)
		}
	}
}

func TestParseGitStatus(t *testing.T) {
	out := " M src/a.go\x00A  new.go\x00R  b.go\x00old-b.go\x00?? scratch/\x00?? notes.txt\x00 D gone.go\x00UU conflict.go\x00"
	files, untracked := parseGitStatus([]byte(out))
	want := map[string]agentv1.GitFileStatus{
		"src/a.go":    agentv1.GitFileStatus_GIT_FILE_STATUS_MODIFIED,
		"new.go":      agentv1.GitFileStatus_GIT_FILE_STATUS_ADDED,
		"b.go":        agentv1.GitFileStatus_GIT_FILE_STATUS_RENAMED,
		"notes.txt":   agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED,
		"gone.go":     agentv1.GitFileStatus_GIT_FILE_STATUS_DELETED,
		"conflict.go": agentv1.GitFileStatus_GIT_FILE_STATUS_CONFLICTED,
	}
	if len(files) != len(want) {
		t.Fatalf("files: %v", files)
	}
	for p, st := range want {
		if files[p] != st {
			t.Errorf("%s = %v; want %v", p, files[p], st)
		}
	}
	if _, ok := files["old-b.go"]; ok {
		t.Error("the original path of a rename is not a file")
	}
	if len(untracked) != 1 || untracked[0] != "scratch" {
		t.Fatalf("untracked dirs: %v", untracked)
	}
	r := &gitRepoStatus{files: files, untrackedDirs: untracked}
	if r.status("scratch/deep/x.txt") != agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED || r.status("scratch") != agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED {
		t.Error("files inside an untracked directory are untracked")
	}
	if r.status("src") != agentv1.GitFileStatus_GIT_FILE_STATUS_UNSPECIFIED {
		t.Error("directories have no status of their own")
	}
	if d := r.deletedIn(""); len(d) != 1 || d[0] != "gone.go" {
		t.Errorf("deleted in root: %v", d)
	}
}

func TestGitignoreMatchesOutsideRepository(t *testing.T) {
	root := t.TempDir()
	fbMust(t, os.MkdirAll(filepath.Join(root, "pkg", "build"), 0o755))
	fbWrite(t, filepath.Join(root, ".gitignore"), "*.log\nbuild/\n")
	fbWrite(t, filepath.Join(root, "pkg", ".gitignore"), "secret.txt\n")
	got := gitignoreMatches(root, filepath.Join(root, "pkg"), []string{"build/", "a.log", "secret.txt", "main.go", "build.go"})
	for _, n := range []string{"build/", "a.log", "secret.txt"} {
		if !got[n] {
			t.Errorf("%s should be ignored", n)
		}
	}
	for _, n := range []string{"main.go", "build.go"} {
		if got[n] {
			t.Errorf("%s should not be ignored", n)
		}
	}
	// Rules of a parent above the working directory do not apply.
	if gitignoreMatches(filepath.Join(root, "pkg"), filepath.Join(root, "pkg"), []string{"a.log"})["a.log"] {
		t.Error("a .gitignore above the base applied")
	}
}

func TestReadFileLimits(t *testing.T) {
	dir := t.TempDir()
	path := func(n string) string { return filepath.Join(dir, n) }

	fbWrite(t, path("README.md"), "# Hello\n\nworld\n")
	r, err := readFile(path("README.md"), path("README.md"), 0, "")
	if err != nil || r.IsBinary || r.Text != "# Hello\n\nworld\n" || r.Mime != "text/markdown" || r.Truncated || r.Etag == "" {
		t.Fatalf("markdown: %v %+v", err, r)
	}
	if again, _ := readFile(path("README.md"), path("README.md"), 0, r.Etag); !again.NotModified || again.Text != "" {
		t.Fatalf("etag: %+v", again)
	}

	// Text over the limit is cut, never inside a rune.
	fbWrite(t, path("big.txt"), strings.Repeat("é", 10))
	r, _ = readFile(path("big.txt"), path("big.txt"), 5, "")
	if !r.Truncated || r.Text != "éé" || r.Size != 20 {
		t.Fatalf("truncated: %+v", r)
	}

	// Binary: a NUL byte, no content.
	fbMust(t, os.WriteFile(path("app.bin"), []byte{0x7f, 'E', 'L', 'F', 0, 1, 2}, 0o644))
	r, _ = readFile(path("app.bin"), path("app.bin"), 0, "")
	if !r.IsBinary || r.Text != "" || r.Data != nil || r.Mime != "application/octet-stream" {
		t.Fatalf("binary: %+v", r)
	}
	// Invalid UTF-8 counts as binary.
	fbMust(t, os.WriteFile(path("latin1.txt"), []byte{'c', 'a', 'f', 0xe9}, 0o644))
	if r, _ = readFile(path("latin1.txt"), path("latin1.txt"), 0, ""); !r.IsBinary {
		t.Fatalf("latin-1: %+v", r)
	}

	// Images come back as bytes within the image limit, nothing beyond it.
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 100)...)
	fbMust(t, os.WriteFile(path("logo.png"), png, 0o644))
	r, _ = readFile(path("logo.png"), path("logo.png"), 0, "")
	if r.Mime != "image/png" || !bytes.Equal(r.Data, png) || r.TooLarge {
		t.Fatalf("image: %+v", r)
	}
	r, _ = readFile(path("logo.png"), path("logo.png"), 50, "")
	if !r.TooLarge || r.Data != nil {
		t.Fatalf("large image: %+v", r)
	}
	// SVG is text.
	fbWrite(t, path("icon.svg"), `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	if r, _ = readFile(path("icon.svg"), path("icon.svg"), 0, ""); r.Mime != "image/svg+xml" || r.IsBinary || r.Text == "" {
		t.Fatalf("svg: %+v", r)
	}

	if _, err := readFile(dir, dir, 0, ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("directory: %v", err)
	}
}

func TestTrimPartialRune(t *testing.T) {
	b := []byte("aé")
	if got := string(trimPartialRune(b[:2])); got != "a" {
		t.Errorf("cut rune: %q", got)
	}
	if got := string(trimPartialRune(b)); got != "aé" {
		t.Errorf("whole rune: %q", got)
	}
}

func fbWrite(t *testing.T, path, content string) {
	t.Helper()
	fbMust(t, os.WriteFile(path, []byte(content), 0o644))
}

func fbMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
