//go:build unix

package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/internal/gittest"
)

func sh(t *testing.T, dir, script string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func fileSum(t *testing.T, p string) string {
	t.Helper()
	s := sha256.Sum256([]byte(readFile(t, p)))
	return fmt.Sprintf("%x", s)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// gitHistory opens a history with git checkpoints over dirs.
func gitHistory(t *testing.T, id string, folders bool, dirs ...string) *FileHistory {
	t.Helper()
	h := NewFileHistory(filepath.Join(t.TempDir(), "history", id), nil, nil)
	h.EnableGit(GitOptions{SessionID: id, Dirs: func() []string { return dirs }, Folders: folders, Budget: 30 * time.Second})
	if h.git == nil {
		t.Skip("git is not installed")
	}
	return h
}

func checkpoint(t *testing.T, h *FileHistory, id string) {
	t.Helper()
	h.MakeCheckpoint(id)
	if n := h.WaitCheckpoint(context.Background()); len(n) > 0 {
		t.Fatalf("notices: %v", n)
	}
}

func TestGitCheckpointRewindRestoresShellEdits(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, ".gitignore", "*.log\n")
	gittest.Write(t, repo, "heredoc.txt", "one\n")
	gittest.Write(t, repo, "script.py.txt", "value = 1\n")
	gittest.Write(t, repo, "sed.txt", "colour\n")
	gittest.Write(t, repo, "gone.txt", "keep me\n")
	gittest.Write(t, repo, "tool.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod(filepath.Join(repo, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "files")
	gittest.Write(t, repo, "app.log", "log before\n")         // ignored, touched by Edit
	gittest.Write(t, repo, "notes.txt", "untracked before\n") // untracked, there before the turn
	gittest.Write(t, repo, "staged.txt", "staged\n")
	gittest.Run(t, repo, "add", "staged.txt") // the user's own staged change
	head := gittest.Run(t, repo, "rev-parse", "HEAD")
	index := fileSum(t, filepath.Join(repo, ".git", "index"))
	status := gittest.Run(t, repo, "status", "--porcelain")

	h := gitHistory(t, "s1", false, filepath.Join(repo, "src"))
	checkpoint(t, h, "m1")
	if h.HasChanges("m1") {
		t.Fatal("nothing changed yet")
	}

	// The turn: Edit backs up the ignored log, everything else goes through the shell.
	h.TrackEdit(filepath.Join(repo, "app.log"))
	gittest.Write(t, repo, "app.log", "log after\n")
	sh(t, repo, "cat > heredoc.txt <<'EOF'\ntwo\nthree\nEOF")
	if _, err := exec.LookPath("python3"); err == nil {
		sh(t, repo, `python3 -c "import pathlib; p=pathlib.Path('script.py.txt'); p.write_text(p.read_text().replace('1', '2'))"`)
	} else {
		sh(t, repo, "printf 'value = 2\\n' > script.py.txt")
	}
	sh(t, repo, "sed -i.bak 's/colour/color/' sed.txt && rm sed.txt.bak")
	sh(t, repo, "mkdir -p gen/deep && echo new > gen/deep/new.txt && echo top > new.txt")
	sh(t, repo, "rm gone.txt && chmod -x tool.sh && echo more >> notes.txt && echo changed >> README.md")
	big := filepath.Join(repo, "big.bin")
	if err := os.WriteFile(big, make([]byte, maxCheckpointFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if !h.HasChanges("m1") {
		t.Fatal("the shell edits are not seen")
	}

	res, err := h.Restore("m1")
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		"heredoc.txt": "one\n", "script.py.txt": "value = 1\n", "sed.txt": "colour\n", "gone.txt": "keep me\n",
		"notes.txt": "untracked before\n", "README.md": "hello\n", "app.log": "log before\n", "staged.txt": "staged\n",
	} {
		if got := readFile(t, filepath.Join(repo, p)); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	for _, p := range []string{"new.txt", "gen/deep/new.txt", "gen"} {
		if exists(filepath.Join(repo, p)) {
			t.Errorf("%s is still there", p)
		}
	}
	if st, err := os.Stat(filepath.Join(repo, "tool.sh")); err != nil || st.Mode()&0o100 == 0 {
		t.Errorf("tool.sh lost its executable bit: %v", err)
	}
	if !exists(big) {
		t.Error("an untracked file over the cap was deleted")
	}
	if len(res.Skipped) != 0 || len(res.Restored) < 9 {
		t.Errorf("result: %+v", res)
	}
	// The user's repository: same HEAD, same index, same status but the big file.
	if got := gittest.Run(t, repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved: %s", got)
	}
	if got := fileSum(t, filepath.Join(repo, ".git", "index")); got != index {
		t.Error("the user's index changed")
	}
	if got := strings.Replace(gittest.Run(t, repo, "status", "--porcelain"), "?? big.bin\n", "", 1); got != status {
		t.Errorf("status: %q, was %q", got, status)
	}
	if n := gittest.Run(t, repo, "rev-list", "--all", "--count"); n != "2" {
		t.Errorf("commits seen by --all: %s", n)
	}
	if out := gittest.Run(t, repo, "log", "--all", "--oneline"); strings.Count(out, "\n") != 1 {
		t.Errorf("log --all: %s", out)
	}
	if typ := gittest.Run(t, repo, "cat-file", "-t", CheckpointRefPrefix+"s1"); typ != "tree" {
		t.Errorf("ref type: %s", typ)
	}
	if h.HasChanges("m1") {
		t.Error("changes left after the rewind")
	}
}

func TestGitCheckpointKeepsTreesAcrossTurnsAndGC(t *testing.T) {
	repo := gittest.Init(t)
	h := gitHistory(t, "s2", false, repo)
	checkpoint(t, h, "m1")
	gittest.Write(t, repo, "a.txt", "turn one\n")
	checkpoint(t, h, "m2")
	gittest.Write(t, repo, "a.txt", "turn two\n")
	gittest.Run(t, repo, "gc", "-q", "--prune=now")

	cps := h.Checkpoints()
	if len(cps) != 2 || len(cps[0].GitTrees) != 1 || len(cps[1].GitTrees) != 1 {
		t.Fatalf("checkpoints: %+v", cps)
	}
	if out := gittest.Run(t, repo, "ls-tree", CheckpointRefPrefix+"s2"); !strings.Contains(out, "m1") || !strings.Contains(out, "m2") {
		t.Fatalf("ref tree: %s", out)
	}
	if _, err := h.Restore("m2"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(repo, "a.txt")); got != "turn one\n" {
		t.Fatalf("m2: %q", got)
	}
	if _, err := h.Restore("m1"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(repo, "a.txt")) {
		t.Fatal("m1 had no a.txt")
	}

	// A resumed session reads the trees back from the transcript records.
	again := NewFileHistory(h.dir, nil, h.Checkpoints())
	again.EnableGit(GitOptions{SessionID: "s2", Dirs: func() []string { return []string{repo} }})
	gittest.Write(t, repo, "a.txt", "after resume\n")
	if _, err := again.Restore("m2"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(repo, "a.txt")); got != "turn one\n" {
		t.Fatalf("resumed: %q", got)
	}

	// Deleting the session drops its ref; forking copies it.
	if err := ForkGitCheckpoints(h.dir, filepath.Join(filepath.Dir(h.dir), "s3"), "s2", "s3"); err != nil {
		t.Fatal(err)
	}
	DropGitCheckpoints(h.dir, "s2")
	refs := gittest.Run(t, repo, "for-each-ref", "--format=%(refname)", CheckpointRefPrefix)
	if refs != CheckpointRefPrefix+"s3" {
		t.Fatalf("refs: %q", refs)
	}
	dirOf := func(id string) string { return filepath.Join(filepath.Dir(h.dir), id) }
	if got := SweepGitCheckpoints(context.Background(), repo, dirOf, time.Hour, time.Now()); len(got) != 0 {
		t.Fatalf("a fresh fork was swept: %v", got)
	}
	if got := SweepGitCheckpoints(context.Background(), repo, dirOf, time.Hour, time.Now().Add(2*time.Hour)); len(got) != 1 || got[0] != "s3" {
		t.Fatalf("sweep: %v", got)
	}
}

func TestGitCheckpointInWorktreeAndSkipsSubmodules(t *testing.T) {
	repo := gittest.Init(t)
	sub := gittest.Init(t)
	gittest.Run(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	gittest.Run(t, repo, "commit", "-q", "-m", "submodule")
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, repo, "worktree", "add", "-q", wt)
	wt, _ = filepath.EvalSymlinks(wt)
	gittest.Run(t, wt, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")

	h := gitHistory(t, "s4", false, wt)
	checkpoint(t, h, "m1")
	gittest.Write(t, wt, "README.md", "edited in the worktree\n")
	gittest.Write(t, wt, "sub/README.md", "edited in the submodule\n")
	gittest.Write(t, wt, "fresh.txt", "new\n")
	if _, err := h.Restore("m1"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(wt, "README.md")); got != "hello\n" {
		t.Fatalf("worktree file: %q", got)
	}
	if exists(filepath.Join(wt, "fresh.txt")) {
		t.Fatal("new file in the worktree kept")
	}
	if got := readFile(t, filepath.Join(wt, "sub/README.md")); got != "edited in the submodule\n" {
		t.Fatalf("a submodule's file was touched: %q", got)
	}
	// The ref lives in the shared repository; the main checkout is untouched.
	if typ := gittest.Run(t, repo, "cat-file", "-t", CheckpointRefPrefix+"s4"); typ != "tree" {
		t.Fatalf("ref: %s", typ)
	}
	if st := gittest.Run(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("main checkout: %q", st)
	}
}

func TestGitCheckpointFolderOutsideGit(t *testing.T) {
	gittest.Isolate(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	gittest.Write(t, dir, "a.txt", "a\n")
	gittest.Write(t, dir, "node_modules/x/index.js", "dep\n")
	h := gitHistory(t, "s5", true, dir)
	checkpoint(t, h, "m1")
	sh(t, dir, "echo b > a.txt && echo c > c.txt && rm -rf node_modules/x && mkdir node_modules/y && echo y > node_modules/y/i.js")
	if _, err := h.Restore("m1"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "a\n" || exists(filepath.Join(dir, "c.txt")) {
		t.Fatalf("a.txt %q, c.txt kept: %v", got, exists(filepath.Join(dir, "c.txt")))
	}
	// node_modules is never captured, so never restored or deleted.
	if exists(filepath.Join(dir, "node_modules/x/index.js")) || !exists(filepath.Join(dir, "node_modules/y/i.js")) {
		t.Fatal("node_modules was touched")
	}
	if exists(filepath.Join(dir, ".git")) {
		t.Fatal("the folder got a .git")
	}
}

func TestGitCheckpointWithoutGitFallsBack(t *testing.T) {
	repo := gittest.Init(t)
	was := gitAvailable
	gitAvailable = func() bool { return false }
	defer func() { gitAvailable = was }()
	h := NewFileHistory(t.TempDir(), nil, nil)
	h.EnableGit(GitOptions{SessionID: "s6", Dirs: func() []string { return []string{repo} }})
	h.MakeCheckpoint("m1")
	if n := h.WaitCheckpoint(context.Background()); n != nil {
		t.Fatal(n)
	}
	p := filepath.Join(repo, "README.md")
	h.TrackEdit(p)
	gittest.Write(t, repo, "README.md", "edit\n")
	if _, err := h.Restore("m1"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); got != "hello\n" {
		t.Fatalf("per-file restore: %q", got)
	}
}

func TestGitCheckpointOverBudgetIsSkippedWithANotice(t *testing.T) {
	repo := gittest.Init(t)
	h := NewFileHistory(t.TempDir(), nil, nil)
	h.EnableGit(GitOptions{SessionID: "s7", Dirs: func() []string { return []string{repo} }, Budget: time.Nanosecond})
	if h.git == nil {
		t.Skip("git is not installed")
	}
	h.MakeCheckpoint("m1")
	n := h.WaitCheckpoint(context.Background())
	if len(n) != 1 || !strings.Contains(n[0], "longer than") {
		t.Fatalf("notices: %v", n)
	}
	if again := h.WaitCheckpoint(context.Background()); again != nil {
		t.Fatal("the notice came twice")
	}
	h.MakeCheckpoint("m2")
	if n := h.WaitCheckpoint(context.Background()); len(n) != 0 {
		t.Fatalf("a root that is off is reported again: %v", n)
	}
	if cps := h.Checkpoints(); len(cps[0].GitTrees) != 0 {
		t.Fatal("a tree was recorded")
	}
}

func TestGitCheckpointNewFileCap(t *testing.T) {
	repo := gittest.Init(t)
	was := maxCheckpointNewFiles
	maxCheckpointNewFiles = 300
	defer func() { maxCheckpointNewFiles = was }()
	h := gitHistory(t, "s8", false, repo)
	checkpoint(t, h, "m1")
	for i := 0; i <= maxCheckpointNewFiles; i++ {
		if i%100 == 0 {
			if err := os.MkdirAll(filepath.Join(repo, "many", fmt.Sprint(i/100)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(repo, "many", fmt.Sprint(i/100), fmt.Sprint(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.MakeCheckpoint("m2")
	if n := h.WaitCheckpoint(context.Background()); len(n) != 1 || !strings.Contains(n[0], "new files") {
		t.Fatalf("notices: %v", n)
	}
	// The rewind lifts the caps: everything the turns created goes.
	if _, err := h.Restore("m1"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(repo, "many")) {
		t.Fatal("the new files are still there")
	}
}

// TestGitCheckpointPerformance measures captures on a repository with many files. HARNESS_SNAPSHOT_PERF=<files> runs
// a larger one (20000, 100000).
func TestGitCheckpointPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	n := 5000
	if v, err := strconv.Atoi(os.Getenv("HARNESS_SNAPSHOT_PERF")); err == nil && v > 0 {
		n = v
	}
	setup := time.Now()
	repo := gittest.Init(t)
	for i := 0; i < n; i++ {
		d := filepath.Join(repo, "pkg", fmt.Sprint(i/500))
		if i%500 == 0 {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("f%d.go", i)), []byte(fmt.Sprintf("package p\n\nconst X%d = %d\n", i, i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "many")
	t.Logf("setup %v", time.Since(setup))
	h := gitHistory(t, "perf", false, repo)

	start := time.Now()
	checkpoint(t, h, "m1")
	first := time.Since(start)
	for i := 0; i < 20; i++ {
		gittest.Write(t, repo, fmt.Sprintf("pkg/%d/f%d.go", i, i*500), "package p // edited\n")
	}
	gittest.Write(t, repo, "pkg/new.go", "package p\n")
	start = time.Now()
	checkpoint(t, h, "m2")
	next := time.Since(start)
	start = time.Now()
	changed := h.ChangedCheckpoints()
	list := time.Since(start)
	start = time.Now()
	if _, err := h.Restore("m1"); err != nil {
		t.Fatal(err)
	}
	restore := time.Since(start)
	start = time.Now()
	gittest.Run(t, repo, "status", "--porcelain")
	status := time.Since(start)
	t.Logf("%d files: first capture %v, next turn %v, rewind list %v, restore %v; git status %v", n, first, next, list, restore, status)
	if !changed["m1"] || changed["m2"] {
		t.Fatalf("changed: %v", changed)
	}
	if next > GitCheckpointBudget {
		t.Fatalf("a turn's capture took %v", next)
	}
}
