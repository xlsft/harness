//go:build unix

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/vcshooks"
)

// fakeVcs answers like a VcsStatus and a VcsDiff hook would, counting the diffs asked for.
type fakeVcs struct {
	mu     sync.Mutex
	root   string
	files  []vcshooks.File
	patch  string
	diffs  [][]string
	noDiff bool
}

func (f *fakeVcs) Status(_ context.Context, dir string) (*vcshooks.Status, error) {
	if _, ok := relWithin(f.root, dir); !ok && dir != f.root {
		return nil, nil
	}
	return &vcshooks.Status{Root: f.root, Base: "@-", Files: f.files}, nil
}

func (f *fakeVcs) Diff(_ context.Context, _ string, paths []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffs = append(f.diffs, paths)
	if f.noDiff {
		return nil, vcshooks.ErrNoAnswer
	}
	return []byte(f.patch), nil
}

func n32(n int32) *int32 { return &n }

const fakePatch = `diff --git a/a.txt b/a.txt
index 1..2 100644
--- a/a.txt
+++ b/a.txt
@@ -1,2 +1,2 @@
 keep
-old
+new
diff --git a/new.txt b/new.txt
new file mode 100644
--- /dev/null
+++ b/new.txt
@@ -0,0 +1 @@
+fresh
`

func TestSessionDiffThroughVcsHooks(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a.txt"), "keep\nnew\n")
	writeTree(t, filepath.Join(root, "new.txt"), "fresh\n")
	writeTree(t, filepath.Join(root, "untouched.txt"), "same\n")
	vcs := &fakeVcs{root: root, patch: fakePatch, files: []vcshooks.File{{Path: "a.txt", Status: "modified"}, {Path: "new.txt", Status: "added"}}}
	src := &diffSources{cwd: root, dirs: []string{root}, folderStore: t.TempDir(), vcs: vcs}

	r := diffOf(t, src)
	if len(r.Files) != 2 || r.BaseLabel != "@-" || r.HeadLabel != labelWorkingCopy || r.BaseKind != agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL {
		t.Fatalf("response %+v", r)
	}
	a := wantDiff(t, r, "a.txt")
	if a.Source != agentv1.FileDiffSource_FILE_DIFF_SOURCE_VCS || a.Additions != 1 || a.Deletions != 1 || !strings.Contains(hunkText(a), "+new") {
		t.Errorf("a.txt %+v", a)
	}
	if n := wantDiff(t, r, "new.txt"); n.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED || n.Additions != 1 || len(n.Hunks) != 1 {
		t.Errorf("new.txt %+v", n)
	}
	if len(vcs.diffs) != 1 {
		t.Errorf("one VcsDiff gives the counts and the hunks: %v", vcs.diffs)
	}

	// Nothing changed: the same version, and the snapshot of the folder is not what answers.
	again := diffOf(t, src)
	if again.Version != r.Version {
		t.Error("an unchanged state should keep its version")
	}
	writeTree(t, filepath.Join(root, "a.txt"), "keep\nnewer\n")
	if diffOf(t, src).Version == r.Version {
		t.Error("an edit of a listed file should change the version")
	}

	// Counts from the hook: VcsDiff only for the hunks, for the files that get them.
	vcs.files = []vcshooks.File{{Path: "a.txt", Status: "M", Additions: n32(1), Deletions: n32(1)}}
	vcs.diffs = nil
	r = diffOf(t, src)
	if len(vcs.diffs) != 1 || strings.Join(vcs.diffs[0], ",") != "a.txt" {
		t.Errorf("diffs asked for: %v", vcs.diffs)
	}
	if st := sessionDiffStats(context.Background(), src); st.FilesChanged != 1 || st.Additions != 1 {
		t.Errorf("stats %+v", st)
	}

	// One file of the pane.
	f, err := computeSessionFileDiff(context.Background(), src, nil, filepath.Join(root, "a.txt"))
	if err != nil || f.DisplayPath != "a.txt" || len(f.Hunks) != 1 {
		t.Fatalf("file %+v %v", f, err)
	}
	if _, err := computeSessionFileDiff(context.Background(), src, nil, filepath.Join(root, "untouched.txt")); err == nil {
		t.Error("a file the hook does not list should not be served")
	}

	// Without a VcsDiff hook the files are listed with what VcsStatus said.
	vcs.noDiff = true
	vcs.files = []vcshooks.File{{Path: "a.txt", Status: "M"}}
	if r := diffOf(t, src); len(r.Files) != 1 || len(r.Files[0].Hunks) != 0 {
		t.Errorf("without VcsDiff: %+v", r.Files)
	}
}

func TestSessionDiffVcsHookLeavesOtherFoldersToSnapshots(t *testing.T) {
	claimed, other := t.TempDir(), t.TempDir()
	writeTree(t, filepath.Join(other, "x.txt"), "one\n")
	vcs := &fakeVcs{root: claimed, patch: fakePatch, files: []vcshooks.File{{Path: "a.txt", Status: "modified"}}}
	writeTree(t, filepath.Join(claimed, "a.txt"), "keep\nnew\n")
	src := &diffSources{cwd: claimed, dirs: []string{claimed, other}, folderStore: t.TempDir(), vcs: vcs}
	_ = diffOf(t, src) // takes the snapshot of the other folder
	writeTree(t, filepath.Join(other, "x.txt"), "two\n")
	r := diffOf(t, src)
	wantDiff(t, r, "a.txt")
	x := wantDiff(t, r, filepath.Base(other)+"/x.txt")
	if x.Source != agentv1.FileDiffSource_FILE_DIFF_SOURCE_HISTORY {
		t.Errorf("x.txt %+v", x)
	}
}

func TestSessionDiffVcsHookFailureFails(t *testing.T) {
	root := t.TempDir()
	src := &diffSources{cwd: root, dirs: []string{root}, vcs: failingVcs{}}
	if _, err := computeSessionDiff(context.Background(), src, nil, ""); err == nil || !strings.Contains(err.Error(), "hook broke") {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
}

type failingVcs struct{}

func (failingVcs) Status(context.Context, string) (*vcshooks.Status, error) {
	return nil, errString("hook broke")
}
func (failingVcs) Diff(context.Context, string, []string) ([]byte, error) { return nil, nil }

type errString string

func (e errString) Error() string { return string(e) }
