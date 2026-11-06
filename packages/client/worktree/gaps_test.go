package worktree_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/worktree"
)

func TestCopyIncluded(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, ".gitignore", ".env\nsecrets/\nbuild/\n")
	gittest.Write(t, repo, worktree.IncludeFile, ".env\nsecrets/\nREADME.md\nnotes.txt\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "ignore")
	gittest.Write(t, repo, ".env", "A=1\n")
	gittest.Write(t, repo, "secrets/key", "k\n")
	gittest.Write(t, repo, "build/out", "x\n") // ignored, not named
	gittest.Write(t, repo, "notes.txt", "n\n") // named, not ignored
	if err := os.Symlink("/etc/hosts", filepath.Join(repo, "secrets", "link")); err != nil {
		t.Fatal(err)
	}
	r, err := worktree.Create(ctx, repo, worktree.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Included != 2 || r.IncludeProblem != "" {
		t.Fatalf("included %d, problem %q", r.Included, r.IncludeProblem)
	}
	if got, _ := os.ReadFile(filepath.Join(r.Path, ".env")); string(got) != "A=1\n" {
		t.Fatalf(".env is %q", got)
	}
	if _, err := os.Stat(filepath.Join(r.Path, "secrets", "key")); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"build/out", "notes.txt", "secrets/link"} {
		if _, err := os.Lstat(filepath.Join(r.Path, missing)); err == nil {
			t.Fatalf("%s came along", missing)
		}
	}
	if st := gittest.Run(t, r.Path, "status", "--porcelain"); st != "" {
		t.Fatalf("the worktree shows changes: %q", st)
	}
}

func TestParsePullRequest(t *testing.T) {
	good := map[string]worktree.PullRequestRef{
		"12":                                  {Number: 12},
		"#7":                                  {Number: 7},
		"https://github.com/o/r/pull/9":       {Number: 9, Repo: "o/r"},
		"https://github.com/o/r/pull/9/files": {Number: 9, Repo: "o/r"},
		"https://gl.example/g/s/r/-/merge_requests/3": {Number: 3, Repo: "g/s/r"},
	}
	for in, want := range good {
		if got, err := worktree.ParsePullRequest(in); err != nil || got != want {
			t.Errorf("%q: %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0", "-1", "main", "https://github.com/o/r/issues/3"} {
		if _, err := worktree.ParsePullRequest(bad); !errors.Is(err, worktree.ErrInvalidRef) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// prRemote gives repo a remote with a pull request head and returns the commit on it.
func prRemote(t *testing.T, repo, ref string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "o", "r.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, remote, "init", "-q", "--bare")
	gittest.Run(t, repo, "remote", "add", "origin", remote)
	gittest.Run(t, repo, "checkout", "-q", "-b", "feature")
	gittest.Write(t, repo, "feature.txt", "f\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "feature")
	sha := gittest.Run(t, repo, "rev-parse", "HEAD")
	gittest.Run(t, repo, "push", "-q", "origin", "HEAD:"+ref)
	gittest.Run(t, repo, "checkout", "-q", "main")
	gittest.Run(t, repo, "branch", "-qD", "feature")
	return sha
}

func TestCreateFromPullRequest(t *testing.T) {
	for _, ref := range []string{"refs/pull/5/head", "refs/merge-requests/5/head"} {
		t.Run(ref, func(t *testing.T) {
			repo := gittest.Init(t)
			sha := prRemote(t, repo, ref)
			r, err := worktree.Create(ctx, repo, worktree.Options{PullRequest: "#5"})
			if err != nil {
				t.Fatal(err)
			}
			if r.Name != "pr-5" || r.Branch != "harness/pr-5" || r.BaseRef != sha {
				t.Fatalf("result %+v", r)
			}
			if got := gittest.Run(t, r.Path, "rev-parse", "HEAD"); got != sha {
				t.Fatalf("HEAD %s, want %s", got, sha)
			}
		})
	}
	repo := gittest.Init(t)
	prRemote(t, repo, "refs/pull/5/head")
	if _, err := worktree.Create(ctx, repo, worktree.Options{PullRequest: "5", BaseRef: "main"}); !errors.Is(err, worktree.ErrInvalidRef) {
		t.Fatalf("with a base ref: %v", err)
	}
	if _, err := worktree.Create(ctx, repo, worktree.Options{PullRequest: "https://github.com/other/repo/pull/5"}); !errors.Is(err, worktree.ErrInvalidRef) {
		t.Fatalf("of another repository: %v", err)
	}
	if _, err := worktree.Create(ctx, repo, worktree.Options{PullRequest: "https://github.com/o/r/pull/5"}); err != nil {
		t.Fatalf("of this repository: %v", err)
	}
	if _, err := worktree.Create(ctx, repo, worktree.Options{PullRequest: "99"}); err == nil {
		t.Fatal("no such pull request, no error")
	}
}

func TestPrune(t *testing.T) {
	repo := gittest.Init(t)
	mk := func(name string) *worktree.Result {
		r, err := worktree.Create(ctx, repo, worktree.Options{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	clean, dirty, untracked, ahead, locked, used := mk("clean"), mk("dirty"), mk("untracked"), mk("ahead"), mk("locked"), mk("used")
	gittest.Write(t, dirty.Path, "README.md", "changed\n")
	gittest.Write(t, untracked.Path, "new.txt", "n\n")
	gittest.Write(t, ahead.Path, "a.txt", "a\n")
	gittest.Run(t, ahead.Path, "add", "-A")
	gittest.Run(t, ahead.Path, "commit", "-q", "-m", "a")
	gittest.Run(t, repo, "worktree", "lock", locked.Path)
	// Someone else's worktree outside Dir is none of its business.
	outside := filepath.Join(t.TempDir(), "outside")
	gittest.Run(t, repo, "worktree", "add", "-q", "-b", "outside", outside)

	inUse := func(p string) bool { return p == used.Path }
	future := time.Now().Add(48 * time.Hour)
	if got, err := worktree.Prune(ctx, repo, 72*time.Hour, future, inUse); err != nil || len(got) != 0 {
		t.Fatalf("not old enough yet: %v, %v", got, err)
	}
	got, err := worktree.Prune(ctx, repo, 24*time.Hour, future, inUse)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != clean.Path || got[0].Branch != clean.Branch {
		t.Fatalf("pruned %+v", got)
	}
	if _, err := os.Stat(clean.Path); err == nil {
		t.Fatal("still there")
	}
	if worktree.BranchExists(ctx, repo, clean.Branch) {
		t.Fatal("its branch stays")
	}
	for _, r := range []*worktree.Result{dirty, untracked, ahead, locked, used} {
		if _, err := os.Stat(r.Path); err != nil {
			t.Fatalf("%s is gone", r.Name)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("the outside worktree is gone")
	}
}
