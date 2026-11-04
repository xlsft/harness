package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/internal/gittest"
)

// clone returns a clone of a new repository (origin) whose main tracks origin/main.
func clone(t *testing.T) (origin, repo string) {
	t.Helper()
	origin = gittest.Init(t)
	repo = t.TempDir()
	gittest.Run(t, repo, "clone", "-q", origin, ".")
	return origin, repo
}

func commit(t *testing.T, dir, file, content string) {
	t.Helper()
	gittest.Write(t, dir, file, content)
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-q", "-m", "change "+file)
}

func TestSyncFetchesUpstream(t *testing.T) {
	ctx := context.Background()
	origin, repo := clone(t)
	wt, err := Create(ctx, repo, Options{Name: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, origin, "fresh.txt", "x\n")

	res, err := Sync(ctx, wt.Path, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != SyncMerged || res.Target != "origin/main" || res.Commits != 1 || res.FetchError != "" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "fresh.txt")); err != nil {
		t.Fatal(err)
	}
	// Local main did not move: only the checked out branch does.
	if out := gittest.Run(t, repo, "log", "--oneline", "main"); strings.Contains(out, "fresh.txt") {
		t.Fatalf("main moved: %s", out)
	}
	if res, err := Sync(ctx, wt.Path, "main"); err != nil || res.Status != SyncUpToDate {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSyncPrefersLocalBaseWithItsOwnCommits(t *testing.T) {
	ctx := context.Background()
	origin, repo := clone(t)
	wt, err := Create(ctx, repo, Options{Name: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, repo, "local.txt", "x\n")
	commit(t, origin, "remote.txt", "x\n")
	res, err := Sync(ctx, wt.Path, "main")
	if err != nil || res.Target != "main" || res.Status != SyncMerged {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSyncFetchFailureStillMerges(t *testing.T) {
	ctx := context.Background()
	_, repo := clone(t)
	gittest.Run(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))
	wt, err := Create(ctx, repo, Options{Name: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, repo, "local.txt", "x\n")
	res, err := Sync(ctx, wt.Path, "main")
	if err != nil || res.FetchError == "" || res.Status != SyncMerged {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSyncRefusesAndReportsConflicts(t *testing.T) {
	ctx := context.Background()
	repo := gittest.Init(t)
	wt, err := Create(ctx, repo, Options{Name: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, repo, "README.md", "base\n")
	gittest.Write(t, wt.Path, "README.md", "dirty\n")
	if _, err := Sync(ctx, wt.Path, "main"); !errors.Is(err, ErrUncommitted) || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("dirty: %v", err)
	}
	commit(t, wt.Path, "README.md", "branch\n")
	res, err := Sync(ctx, wt.Path, "main")
	if err != nil || res.Status != SyncConflicts || len(res.Conflicts) != 1 || res.Conflicts[0] != "README.md" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := Sync(ctx, wt.Path, "main"); !errors.Is(err, ErrMergeInProgress) {
		t.Fatalf("in progress: %v", err)
	}
	if _, err := Sync(ctx, wt.Path, "-bad"); !errors.Is(err, ErrMergeInProgress) {
		t.Fatalf("still in progress: %v", err)
	}
}

func TestSyncRefusesAnOptionAsBase(t *testing.T) {
	ctx := context.Background()
	repo := gittest.Init(t)
	wt, err := Create(ctx, repo, Options{Name: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(ctx, wt.Path, "--abort"); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("%v", err)
	}
}

func TestRemoveRefusesDirtyWithoutForce(t *testing.T) {
	ctx := context.Background()
	repo := gittest.Init(t)
	wt, err := Create(ctx, repo, Options{Name: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	gittest.Write(t, wt.Path, "work.txt", "x\n")
	if d, _ := Dirty(ctx, wt.Path); len(d) != 1 || d[0] != "work.txt" {
		t.Fatalf("dirty %v", d)
	}
	if err := Remove(ctx, Removal{RepositoryPath: repo, Path: wt.Path}); err == nil {
		t.Fatal("removed uncommitted work without being told to")
	}
	if err := Remove(ctx, Removal{RepositoryPath: repo, Path: wt.Path, Force: true, Branch: wt.Branch}); err != nil {
		t.Fatal(err)
	}
	if gittest.Run(t, repo, "branch", "--list", wt.Branch) != "" {
		t.Fatal("branch stayed")
	}
}

func TestSwitchBranch(t *testing.T) {
	ctx := context.Background()
	repo := gittest.Init(t)
	wt, err := Create(ctx, repo, Options{Name: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	if created, err := SwitchBranch(ctx, repo, "new/one"); err != nil || !created {
		t.Fatalf("%v %v", created, err)
	}
	if CurrentBranch(ctx, repo) != "new/one" {
		t.Fatal("not switched")
	}
	if _, err := SwitchBranch(ctx, repo, wt.Branch); !errors.Is(err, ErrBranchCheckedOut) {
		t.Fatalf("branch of a worktree: %v", err)
	}
	if created, err := SwitchBranch(ctx, repo, "main"); err != nil || created {
		t.Fatalf("%v %v", created, err)
	}
	if _, err := SwitchBranch(ctx, repo, "-x"); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("%v", err)
	}
}
