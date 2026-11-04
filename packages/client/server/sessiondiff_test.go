//go:build unix

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/session"
)

func diffOf(t *testing.T, src *diffSources) *agentv1.GetSessionDiffResponse {
	t.Helper()
	r, err := computeSessionDiff(context.Background(), src, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func diffFile(r *agentv1.GetSessionDiffResponse, display string) *agentv1.FileDiff {
	for _, f := range r.Files {
		if f.DisplayPath == display {
			return f
		}
	}
	return nil
}

func wantDiff(t *testing.T, r *agentv1.GetSessionDiffResponse, display string) *agentv1.FileDiff {
	t.Helper()
	f := diffFile(r, display)
	if f == nil {
		names := []string{}
		for _, x := range r.Files {
			names = append(names, x.DisplayPath)
		}
		t.Fatalf("%s is not listed; have %v", display, names)
	}
	return f
}

func hunkText(f *agentv1.FileDiff) string {
	var lines []string
	for _, h := range f.Hunks {
		lines = append(lines, h.Lines...)
	}
	return strings.Join(lines, "\n")
}

func writeTree(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionDiffGitRepository(t *testing.T) {
	repo := gittest.Init(t)
	src := &diffSources{cwd: repo, dirs: []string{repo}}

	clean := diffOf(t, src)
	if len(clean.Files) != 0 || clean.Version == "" {
		t.Fatalf("clean repository: %+v", clean)
	}

	// A shell command changes a tracked file: no tool call involved.
	writeTree(t, filepath.Join(repo, "README.md"), "hello\nfrom sed\n")
	r := diffOf(t, src)
	f := wantDiff(t, r, "README.md")
	if f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED || f.Additions != 1 || f.Deletions != 0 || f.Source != agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT {
		t.Fatalf("modified: %+v", f)
	}
	if f.Path != filepath.Join(repo, "README.md") {
		t.Fatalf("path: %s", f.Path)
	}
	if h := f.Hunks[0]; h.OldStart != 1 || h.OldLines != 1 || h.NewStart != 1 || h.NewLines != 2 || !strings.Contains(hunkText(f), "+from sed") {
		t.Fatalf("hunk: %+v", h)
	}

	// Unchanged: the same version.
	if again := diffOf(t, src); again.Version != r.Version {
		t.Fatalf("version moved without a change: %s, %s", r.Version, again.Version)
	}

	// A later change updates the hunks and the version.
	writeTree(t, filepath.Join(repo, "README.md"), "hello\nfrom awk\n")
	later := diffOf(t, src)
	if later.Version == r.Version || !strings.Contains(hunkText(wantDiff(t, later, "README.md")), "+from awk") {
		t.Fatalf("later edit: %+v", later)
	}

	// Staged, untracked, ignored and deleted files.
	writeTree(t, filepath.Join(repo, ".gitignore"), "*.log\n")
	writeTree(t, filepath.Join(repo, "debug.log"), "noise\n")
	writeTree(t, filepath.Join(repo, "notes dir/untracked file.txt"), "a\nb\n")
	writeTree(t, filepath.Join(repo, "staged.txt"), "s\n")
	gittest.Run(t, repo, "add", "staged.txt")
	if err := os.Remove(filepath.Join(repo, "src/app/main.go")); err != nil {
		t.Fatal(err)
	}
	r = diffOf(t, src)
	if f := wantDiff(t, r, "notes dir/untracked file.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED || f.Additions != 2 || f.Hunks[0].OldLines != 0 {
		t.Fatalf("untracked: %+v", f)
	}
	if f := wantDiff(t, r, "staged.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED || f.Additions != 1 {
		t.Fatalf("staged: %+v", f)
	}
	if f := wantDiff(t, r, "src/app/main.go"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED || f.Deletions != 1 {
		t.Fatalf("deleted: %+v", f)
	}
	if diffFile(r, "debug.log") != nil {
		t.Fatal("an ignored file is listed")
	}
	if r.FilesChanged != int32(len(r.Files)) || r.Additions < 4 || r.Deletions < 1 {
		t.Fatalf("totals: %d %d %d", r.FilesChanged, r.Additions, r.Deletions)
	}
}

func TestSessionDiffGitFlags(t *testing.T) {
	repo := gittest.Init(t)
	big := strings.Repeat("line of text\n", maxDiffFileBytes/10)
	writeTree(t, filepath.Join(repo, "big-old.txt"), big)
	writeTree(t, filepath.Join(repo, "moved.txt"), "one\ntwo\nthree\nfour\nfive\n")
	writeTree(t, filepath.Join(repo, "tracked.bin"), "a\x00b")
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "commit", "-q", "-m", "more")
	src := &diffSources{cwd: repo, dirs: []string{repo}}

	// Too large on the old side only; a rename; binary files, tracked and not; a large new file.
	writeTree(t, filepath.Join(repo, "big-old.txt"), "small now\n")
	writeTree(t, filepath.Join(repo, "tracked.bin"), "a\x00c")
	writeTree(t, filepath.Join(repo, "big-new.txt"), big)
	writeTree(t, filepath.Join(repo, "new.bin"), "x\x00y")
	writeTree(t, filepath.Join(repo, "é file.txt"), "accent\n")
	gittest.Run(t, repo, "mv", "moved.txt", "renamed.txt")
	gittest.Write(t, repo, "README.md", "changed\n")

	r := diffOf(t, src)
	if f := wantDiff(t, r, "big-old.txt"); !f.TooLarge || len(f.Hunks) != 0 {
		t.Fatalf("large old side: %+v", f)
	}
	if f := wantDiff(t, r, "big-new.txt"); !f.TooLarge || len(f.Hunks) != 0 {
		t.Fatalf("large new file: %+v", f)
	}
	if f := wantDiff(t, r, "tracked.bin"); !f.Binary || len(f.Hunks) != 0 {
		t.Fatalf("tracked binary: %+v", f)
	}
	if f := wantDiff(t, r, "new.bin"); !f.Binary || len(f.Hunks) != 0 {
		t.Fatalf("new binary: %+v", f)
	}
	if f := wantDiff(t, r, "renamed.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_RENAMED || f.OldPath != filepath.Join(repo, "moved.txt") {
		t.Fatalf("rename: %+v", f)
	}
	if diffFile(r, "moved.txt") != nil {
		t.Fatal("a rename is listed as a deletion too")
	}
	if f := wantDiff(t, r, "é file.txt"); f.Additions != 1 {
		t.Fatalf("unusual name: %+v", f)
	}
	if f := wantDiff(t, r, "README.md"); f.Additions != 1 || f.Deletions != 1 {
		t.Fatalf("a small file next to skipped ones: %+v", f)
	}
}

func TestSessionDiffUnbornHeadAndWorktreeBase(t *testing.T) {
	gittest.Isolate(t)
	fresh, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, fresh, "init", "-q", "-b", "main")
	writeTree(t, filepath.Join(fresh, "a.txt"), "a\n")
	writeTree(t, filepath.Join(fresh, "b.txt"), "b\n")
	gittest.Run(t, fresh, "add", "a.txt")
	r := diffOf(t, &diffSources{cwd: fresh, dirs: []string{fresh}})
	if wantDiff(t, r, "a.txt").Additions != 1 || wantDiff(t, r, "b.txt").Additions != 1 {
		t.Fatalf("no commit yet: %+v", r)
	}

	// In the client's worktree, what the session committed since its base still counts.
	repo := gittest.Init(t)
	gittest.Run(t, repo, "checkout", "-q", "-b", "work")
	writeTree(t, filepath.Join(repo, "committed.txt"), "c\n")
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "commit", "-q", "-m", "work")
	r = diffOf(t, &diffSources{cwd: repo, dirs: []string{repo}, baseRef: "main"})
	wantDiff(t, r, "committed.txt")
}

func TestSessionDiffOutsideGit(t *testing.T) {
	gittest.Isolate(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd, scratch := filepath.Join(base, "project"), filepath.Join(base, "scratchpad")
	for _, d := range []string{cwd, scratch} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hist := session.NewFileHistory(filepath.Join(base, "history"), nil, nil)
	hist.MakeCheckpoint("m1")

	touched := filepath.Join(cwd, "touched.txt")
	writeTree(t, touched, "original\n")
	hist.TrackEdit(touched) // Edit tool, about to write
	gone := filepath.Join(cwd, "gone.txt")
	writeTree(t, gone, "bye\n")
	hist.TrackEdit(gone)
	created := filepath.Join(cwd, "created.txt")
	hist.TrackEdit(created) // does not exist yet
	same := filepath.Join(cwd, "same.txt")
	writeTree(t, same, "same\n")
	hist.TrackEdit(same)
	writeTree(t, filepath.Join(cwd, "unknown.txt"), "no baseline\n")

	src := func() *diffSources {
		return &diffSources{cwd: cwd, dirs: []string{cwd, scratch}, scratch: scratch, baselines: hist.Baselines()}
	}
	// Nothing changed yet (created.txt still does not exist), and a file without a baseline cannot be listed.
	if r := diffOf(t, src()); len(r.Files) != 0 {
		t.Fatalf("nothing changed yet: %+v", r.Files)
	}

	// Bash-style changes on disk, after the snapshots.
	writeTree(t, touched, "original\nappended by echo\n")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	writeTree(t, created, "fresh\n")
	r := diffOf(t, src())
	if f := wantDiff(t, r, "touched.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED || f.Additions != 1 || f.Source != agentv1.FileDiffSource_FILE_DIFF_SOURCE_HISTORY {
		t.Fatalf("modified: %+v", f)
	}
	if f := wantDiff(t, r, "gone.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED || f.Deletions != 1 {
		t.Fatalf("deleted: %+v", f)
	}
	if f := wantDiff(t, r, "created.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED || f.Additions != 1 {
		t.Fatalf("created: %+v", f)
	}
	if diffFile(r, "same.txt") != nil || diffFile(r, "unknown.txt") != nil {
		t.Fatalf("unchanged or baseline-less files listed: %+v", r.Files)
	}
	if again := diffOf(t, src()); again.Version != r.Version {
		t.Fatal("version moved without a change")
	}

	// A later turn snapshots again: the baseline stays the first one.
	hist.MakeCheckpoint("m2")
	writeTree(t, touched, "original\nappended by echo\nand again\n")
	r2 := diffOf(t, src())
	if f := wantDiff(t, r2, "touched.txt"); f.Additions != 2 || r2.Version == r.Version {
		t.Fatalf("later edit: %+v", f)
	}

	// The cache notices a change of the same size.
	writeTree(t, touched, "original\nappended by ECHO\nand again\n")
	if f := wantDiff(t, diffOf(t, src()), "touched.txt"); !strings.Contains(hunkText(f), "+appended by ECHO") {
		t.Fatalf("stale: %s", hunkText(f))
	}

	// The scratch dir: the agent's files are new, the client's own are not listed.
	writeTree(t, filepath.Join(scratch, "report.md"), "# report\n")
	writeTree(t, filepath.Join(scratch, "sub/data.json"), "{}\n")
	writeTree(t, filepath.Join(scratch, "tasks/b1.output"), "task output\n")
	writeTree(t, filepath.Join(scratch, "tool-results/r1.txt"), "saved\n")
	writeTree(t, filepath.Join(scratch, "shell-snapshot.zsh"), "state\n")
	writeTree(t, filepath.Join(scratch, "cwd-123"), "/x\n")
	writeTree(t, filepath.Join(scratch, "pkg/node_modules/x/i.js"), "x\n")
	r = diffOf(t, src())
	if f := wantDiff(t, r, "scratchpad/report.md"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED || f.Source != agentv1.FileDiffSource_FILE_DIFF_SOURCE_NEW_FILE {
		t.Fatalf("scratch file: %+v", f)
	}
	wantDiff(t, r, "scratchpad/sub/data.json")
	for _, f := range r.Files {
		if strings.Contains(f.Path, "tasks") || strings.Contains(f.Path, "tool-results") || strings.Contains(f.Path, "shell-snapshot") || strings.Contains(f.Path, "cwd-") || strings.Contains(f.Path, "node_modules") {
			t.Fatalf("internal file listed: %s", f.Path)
		}
	}

	// Flags outside git.
	writeTree(t, filepath.Join(scratch, "big.txt"), strings.Repeat("x\n", maxDiffFileBytes))
	writeTree(t, filepath.Join(scratch, "blob.bin"), "a\x00b")
	r = diffOf(t, src())
	if f := wantDiff(t, r, "scratchpad/big.txt"); !f.TooLarge {
		t.Fatalf("too large: %+v", f)
	}
	if f := wantDiff(t, r, "scratchpad/blob.bin"); !f.Binary {
		t.Fatalf("binary: %+v", f)
	}
}

func TestSessionDiffNoFolderPrivateDir(t *testing.T) {
	gittest.Isolate(t)
	private, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := &diffSources{cwd: private, dirs: []string{private}, private: private}
	if r := diffOf(t, src); len(r.Files) != 0 {
		t.Fatalf("empty: %+v", r.Files)
	}
	writeTree(t, filepath.Join(private, "out.txt"), "1\n2\n")
	writeTree(t, filepath.Join(private, "tasks/notes.txt"), "mine\n") // only the scratch dir's tasks/ are internal
	r := diffOf(t, src)
	if f := wantDiff(t, r, "out.txt"); f.Additions != 2 {
		t.Fatalf("new file: %+v", f)
	}
	wantDiff(t, r, "tasks/notes.txt")
	if ds := diffStatsOf(r); ds.FilesChanged != 2 || ds.Additions != 3 || ds.Deletions != 0 {
		t.Fatalf("diff stats: %+v", ds)
	}

	// The agent makes it a repository: git decides from then on.
	gittest.Run(t, private, "init", "-q", "-b", "main")
	writeTree(t, filepath.Join(private, ".gitignore"), "out.txt\n")
	r = diffOf(t, src)
	if diffFile(r, "out.txt") != nil {
		t.Fatalf("ignored by the new repository: %+v", r.Files)
	}
}

func TestSessionDiffFileCap(t *testing.T) {
	repo := gittest.Init(t)
	for i := range maxDiffFiles + 5 {
		writeTree(t, filepath.Join(repo, "many", fmt.Sprintf("f%d.txt", i)), "x\n")
	}
	r := diffOf(t, &diffSources{cwd: repo, dirs: []string{repo}})
	if len(r.Files) != maxDiffFiles || !r.Truncated {
		t.Fatalf("cap: %d files, truncated %v", len(r.Files), r.Truncated)
	}
}

func TestParseGitPatchQuotedAndCounts(t *testing.T) {
	patch := "diff --git \"a/q\\303\\251.txt\" \"b/q\\303\\251.txt\"\n" +
		"index 1..2 100644\n--- \"a/q\\303\\251.txt\"\n+++ \"b/q\\303\\251.txt\"\n" +
		"@@ -1,2 +1,2 @@ func x() {\n a\n--- looks like a header\n+b\n\\ No newline at end of file\n" +
		"diff --git a/with space.txt b/with space.txt\nnew file mode 100644\nindex 0..e69de29\n"
	fs := parseGitPatch([]byte(patch))
	if len(fs) != 2 {
		t.Fatalf("files: %+v", fs)
	}
	if fs[0].Path != "qé.txt" || fs[0].Additions != 1 || fs[0].Deletions != 1 || fs[0].Hunks[0].Header != "func x() {" || len(fs[0].Hunks[0].Lines) != 3 {
		t.Fatalf("first: %+v", fs[0])
	}
	if fs[1].Path != "with space.txt" || fs[1].Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED {
		t.Fatalf("second: %+v", fs[1])
	}
}

func TestSessionDiffFolderSnapshot(t *testing.T) {
	cwd := t.TempDir()
	store := t.TempDir()
	writeTree(t, filepath.Join(cwd, "kept.txt"), "kept\n")
	writeTree(t, filepath.Join(cwd, "edited.txt"), "one\ntwo\n")
	writeTree(t, filepath.Join(cwd, "gone.txt"), "bye\n")
	writeTree(t, filepath.Join(cwd, "big.bin"), strings.Repeat("x", maxDiffFileBytes+1))
	writeTree(t, filepath.Join(cwd, "node_modules/dep.js"), "dep\n")
	writeTree(t, filepath.Join(cwd, "nested/.git/HEAD"), "ref\n")
	writeTree(t, filepath.Join(cwd, "nested/inner.txt"), "inner\n")
	src := &diffSources{cwd: cwd, dirs: []string{cwd}, folderStore: store}

	if r := diffOf(t, src); len(r.Files) != 0 {
		t.Fatalf("the first measurement takes the snapshot, nothing changed: %+v", r.Files)
	}
	// Changes no file tool saw.
	writeTree(t, filepath.Join(cwd, "edited.txt"), "one\nTWO\n")
	if err := os.Remove(filepath.Join(cwd, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, filepath.Join(cwd, "fresh.txt"), "fresh\n")
	writeTree(t, filepath.Join(cwd, "big.bin"), strings.Repeat("y", maxDiffFileBytes+2))
	writeTree(t, filepath.Join(cwd, "node_modules/dep.js"), "changed\n")
	writeTree(t, filepath.Join(cwd, "nested/inner.txt"), "changed\n")
	check := func(r *agentv1.GetSessionDiffResponse) {
		t.Helper()
		if f := wantDiff(t, r, "edited.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED || f.Additions != 1 || f.Deletions != 1 {
			t.Fatalf("edited: %+v", f)
		}
		if f := wantDiff(t, r, "gone.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED || f.Deletions != 1 {
			t.Fatalf("gone: %+v", f)
		}
		if f := wantDiff(t, r, "fresh.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED || f.Additions != 1 {
			t.Fatalf("fresh: %+v", f)
		}
		if f := wantDiff(t, r, "big.bin"); !f.TooLarge || len(f.Hunks) != 0 {
			t.Fatalf("big: %+v", f)
		}
		if len(r.Files) != 4 {
			t.Fatalf("only these four changed: %+v", r.Files)
		}
	}
	check(diffOf(t, src))

	// A client restart: the snapshot comes back from disk, not from a new walk.
	folderBaselines.Lock()
	folderBaselines.held = map[string]*folderBaseline{}
	folderBaselines.Unlock()
	check(diffOf(t, src))

	// Put back as it was (a new mtime): no change after all.
	writeTree(t, filepath.Join(cwd, "edited.txt"), "one\ntwo\n")
	if r := diffOf(t, src); diffFile(r, "edited.txt") != nil {
		t.Fatalf("restored file listed: %+v", r.Files)
	}
}

func TestSessionDiffFolderSnapshotPartial(t *testing.T) {
	cwd := t.TempDir()
	store := t.TempDir()
	writeTree(t, filepath.Join(cwd, "a.txt"), "a\n")
	src := &diffSources{cwd: cwd, dirs: []string{cwd}, folderStore: store}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	// Out of time before reaching anything: nothing is known, so nothing reads as new.
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if fb := folderBaselineOf(done, store, real, nil); !fb.Partial || len(fb.Files) != 0 {
		t.Fatalf("partial snapshot: %+v", fb)
	}
	writeTree(t, filepath.Join(cwd, "b.txt"), "b\n")
	if r := diffOf(t, src); len(r.Files) != 0 {
		t.Fatalf("a partial snapshot finds no new files: %+v", r.Files)
	}
}

func TestSessionDiffFolderTooBroad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if !folderTooBroad(resolvedPath(home)) || !folderTooBroad("/") {
		t.Fatal("the home directory and the root get no snapshot")
	}
	if folderTooBroad(filepath.Join(resolvedPath(home), "project")) {
		t.Fatal("a project does")
	}
}

func TestSweepFolderBaselines(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	writeTree(t, filepath.Join(bootstrap.FolderBaselineDir("11111111-1111-1111-1111-111111111111"), "x.json"), "{}")
	kept := "22222222-2222-2222-2222-222222222222"
	writeTree(t, filepath.Join(bootstrap.FolderBaselineDir(kept), "x.json"), "{}")
	writeTree(t, session.Store{Root: config.ProjectsDir()}.Path("/some/project", kept), "{}\n")
	sweepFolderBaselines()
	if _, err := os.Stat(bootstrap.FolderBaselineDir("11111111-1111-1111-1111-111111111111")); err == nil {
		t.Fatal("a session without a transcript keeps its snapshot")
	}
	if _, err := os.Stat(filepath.Join(bootstrap.FolderBaselineDir(kept), "x.json")); err != nil {
		t.Fatalf("a session with a transcript lost its snapshot: %v", err)
	}
}
