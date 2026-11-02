package worktree_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/worktree"
)

var ctx = context.Background()

func TestCreateDefault(t *testing.T) {
	repo := gittest.Init(t)
	r, err := worktree.Create(ctx, repo, worktree.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z]+-[a-z]+-[a-z0-9]{4}$`).MatchString(r.Name) {
		t.Fatalf("derived name %q", r.Name)
	}
	if want := filepath.Join(repo, ".harness", "worktrees", r.Name); r.Path != want || r.Cwd != want {
		t.Fatalf("path %q cwd %q, want %q", r.Path, r.Cwd, want)
	}
	if r.Branch != "harness/"+r.Name || !r.NewBranch || r.BaseRef != "main" || r.RepositoryPath != repo {
		t.Fatalf("result %+v", r)
	}
	if got := gittest.Run(t, r.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != r.Branch {
		t.Fatalf("worktree HEAD %q", got)
	}
	if _, err := os.Stat(filepath.Join(r.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	// The worktree does not show up in the main checkout.
	if st := gittest.Run(t, repo, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("main checkout is dirty: %q", st)
	}
	// No upstream is configured for the new branch.
	if cfg, _ := os.ReadFile(filepath.Join(repo, ".git", "config")); strings.Contains(string(cfg), r.Branch) {
		t.Fatalf("branch config:\n%s", cfg)
	}
}

func TestCreateCustomNameBranchAndBase(t *testing.T) {
	repo := gittest.Init(t)
	first := gittest.Run(t, repo, "rev-parse", "HEAD")
	gittest.Write(t, repo, "second.txt", "2\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "second")

	r, err := worktree.Create(ctx, repo, worktree.Options{Name: "fix-login", Branch: "feature/login", BaseRef: first})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "fix-login" || r.Branch != "feature/login" || r.BaseRef != first || !r.NewBranch {
		t.Fatalf("result %+v", r)
	}
	if got := gittest.Run(t, r.Path, "rev-parse", "HEAD"); got != first {
		t.Fatalf("worktree HEAD %s, want %s", got, first)
	}
	if _, err := os.Stat(filepath.Join(r.Path, "second.txt")); !os.IsNotExist(err) {
		t.Fatalf("second.txt exists in a worktree based on the first commit: %v", err)
	}
}

func TestCreateExistingBranch(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Run(t, repo, "branch", "topic")
	sha := gittest.Run(t, repo, "rev-parse", "topic")
	r, err := worktree.Create(ctx, repo, worktree.Options{Name: "t", Branch: "topic"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Branch != "topic" || r.NewBranch || r.BaseRef != sha {
		t.Fatalf("result %+v", r)
	}
	// topic is now checked out in that worktree; main is checked out in the repo.
	for _, b := range []string{"topic", "main"} {
		_, err := worktree.Create(ctx, repo, worktree.Options{Branch: b})
		if !errors.Is(err, worktree.ErrBranchCheckedOut) {
			t.Fatalf("%s: err %v", b, err)
		}
	}
}

func TestCreateCollisions(t *testing.T) {
	repo := gittest.Init(t)
	a, err := worktree.Create(ctx, repo, worktree.Options{Name: "dup"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := worktree.Create(ctx, repo, worktree.Options{Name: "dup"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "dup" || b.Name != "dup-2" || b.Branch != "harness/dup-2" {
		t.Fatalf("names %q %q (%s)", a.Name, b.Name, b.Branch)
	}
	// A leftover branch without a directory also counts as taken.
	gittest.Run(t, repo, "branch", "harness/other")
	c, err := worktree.Create(ctx, repo, worktree.Options{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "other-2" {
		t.Fatalf("name %q", c.Name)
	}
	// A stray directory, too.
	os.MkdirAll(filepath.Join(repo, ".harness", "worktrees", "stray"), 0o755)
	d, err := worktree.Create(ctx, repo, worktree.Options{Name: "stray"})
	if err != nil || d.Name != "stray-2" {
		t.Fatalf("%v %+v", err, d)
	}
}

func TestCreateValidation(t *testing.T) {
	repo := gittest.Init(t)
	for _, name := range []string{"a/b", "..", ".hidden", "-x", "a..b", "x.lock", "has space", strings.Repeat("x", 65)} {
		if _, err := worktree.Create(ctx, repo, worktree.Options{Name: name}); !errors.Is(err, worktree.ErrInvalidName) {
			t.Errorf("name %q: err %v", name, err)
		}
	}
	if _, err := worktree.Create(ctx, repo, worktree.Options{Branch: "bad..branch"}); !errors.Is(err, worktree.ErrInvalidRef) {
		t.Errorf("branch: %v", err)
	}
	if _, err := worktree.Create(ctx, repo, worktree.Options{BaseRef: "nope"}); !errors.Is(err, worktree.ErrInvalidRef) {
		t.Errorf("base: %v", err)
	}
	if _, err := worktree.Create(ctx, repo, worktree.Options{BaseRef: "--orphan"}); !errors.Is(err, worktree.ErrInvalidRef) {
		t.Errorf("base option: %v", err)
	}
}

func TestCreateOutsideRepository(t *testing.T) {
	gittest.Isolate(t)
	if _, err := worktree.Create(ctx, t.TempDir(), worktree.Options{}); !errors.Is(err, worktree.ErrNotRepository) {
		t.Fatalf("err %v", err)
	}
	if _, err := worktree.Create(ctx, filepath.Join(t.TempDir(), "missing"), worktree.Options{}); !errors.Is(err, worktree.ErrNotRepository) {
		t.Fatalf("missing dir: err %v", err)
	}
	empty := t.TempDir()
	gittest.Run(t, empty, "init", "-q")
	if _, err := worktree.Create(ctx, empty, worktree.Options{}); !errors.Is(err, worktree.ErrNoCommits) {
		t.Fatalf("empty repo: err %v", err)
	}
}

func TestCreateSubdirectoryCwd(t *testing.T) {
	repo := gittest.Init(t)
	r, err := worktree.Create(ctx, filepath.Join(repo, "src", "app"), worktree.Options{Name: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(r.Path, "src", "app"); r.Cwd != want {
		t.Fatalf("cwd %q, want %q", r.Cwd, want)
	}
	// An untracked directory is created in the worktree.
	os.MkdirAll(filepath.Join(repo, "scratch", "deep"), 0o755)
	r2, err := worktree.Create(ctx, filepath.Join(repo, "scratch", "deep"), worktree.Options{Name: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(r2.Cwd); err != nil || !st.IsDir() || r2.Cwd != filepath.Join(r2.Path, "scratch", "deep") {
		t.Fatalf("cwd %q: %v", r2.Cwd, err)
	}
	// From inside a worktree, new worktrees still go under the main checkout.
	r3, err := worktree.Create(ctx, r.Cwd, worktree.Options{Name: "nested"})
	if err != nil {
		t.Fatal(err)
	}
	if r3.Path != filepath.Join(repo, ".harness", "worktrees", "nested") || r3.RepositoryPath != repo || r3.BaseRef != "harness/sub" ||
		r3.Cwd != filepath.Join(r3.Path, "src", "app") {
		t.Fatalf("nested %+v", r3)
	}
}

func TestExcludeEntry(t *testing.T) {
	repo := gittest.Init(t)
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	os.WriteFile(exclude, []byte("*.log"), 0o644) // no trailing newline
	gittest.Write(t, repo, ".gitignore", "node_modules/\n")
	gittest.Run(t, repo, "add", ".gitignore")
	gittest.Run(t, repo, "commit", "-q", "-m", "ignore")
	for i := 0; i < 2; i++ {
		if _, err := worktree.Create(ctx, repo, worktree.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(exclude)
	if got := string(data); !strings.HasPrefix(got, "*.log\n") || strings.Count(got, "/.harness/worktrees/") != 1 {
		t.Fatalf("exclude:\n%s", got)
	}
	if gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore")); string(gi) != "node_modules/\n" {
		t.Fatalf(".gitignore changed: %q", gi)
	}
	// An existing broader entry is respected.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "info"), 0o755)
	os.WriteFile(filepath.Join(dir, "info", "exclude"), []byte(".harness/\n"), 0o644)
	if err := worktree.EnsureExcluded(dir); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "info", "exclude")); string(data) != ".harness/\n" {
		t.Fatalf("exclude %q", data)
	}
}

func TestRollback(t *testing.T) {
	repo := gittest.Init(t)
	r, err := worktree.Create(ctx, repo, worktree.Options{Name: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Rollback(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
	if out := gittest.Run(t, repo, "branch", "--list", r.Branch); out != "" {
		t.Fatalf("branch still there: %q", out)
	}
}

func TestBranches(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Run(t, repo, "branch", "old")
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T03:04:05Z")
	gittest.Run(t, repo, "checkout", "-q", "-b", "newer")
	gittest.Write(t, repo, "n.txt", "n\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "newer")
	gittest.Run(t, repo, "checkout", "-q", "main")
	if _, err := worktree.Create(ctx, repo, worktree.Options{Name: "wt", Branch: "old"}); err != nil {
		t.Fatal(err)
	}
	bl, err := worktree.Branches(ctx, filepath.Join(repo, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if bl.Repository != repo || bl.Current != "main" || len(bl.Branches) != 3 {
		t.Fatalf("branches %+v", bl)
	}
	if b := bl.Branches[0]; b.Name != "newer" || b.CheckedOut || b.CommittedAt.Format("2006-01-02T15:04:05Z07:00") != "2030-01-02T03:04:05Z" {
		t.Fatalf("first %+v", b)
	}
	co := map[string]bool{}
	for _, b := range bl.Branches[1:] {
		co[b.Name] = b.CheckedOut
	}
	if !co["main"] || !co["old"] {
		t.Fatalf("checked out %+v", bl.Branches)
	}
	gittest.Isolate(t)
	if _, err := worktree.Branches(ctx, t.TempDir()); !errors.Is(err, worktree.ErrNotRepository) {
		t.Fatalf("outside: %v", err)
	}
}
