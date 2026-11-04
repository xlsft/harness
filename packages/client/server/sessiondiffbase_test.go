//go:build unix

package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/internal/gittest"
)

var (
	baseAll         = &agentv1.DiffBase{Kind: agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL}
	baseUncommitted = &agentv1.DiffBase{Kind: agentv1.DiffBaseKind_DIFF_BASE_KIND_UNCOMMITTED}
)

func baseRef(ref string) *agentv1.DiffBase {
	return &agentv1.DiffBase{Kind: agentv1.DiffBaseKind_DIFF_BASE_KIND_REF, Ref: ref}
}

func baseCommit(sha string) *agentv1.DiffBase {
	return &agentv1.DiffBase{Kind: agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT, Sha: sha}
}

func repoSrc(repo string) *diffSources { return &diffSources{cwd: repo, dirs: []string{repo}} }

func diffAt(t *testing.T, src *diffSources, base *agentv1.DiffBase) *agentv1.GetSessionDiffResponse {
	t.Helper()
	r, err := computeSessionDiff(context.Background(), src, base, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func listed(r *agentv1.GetSessionDiffResponse) string {
	names := []string{}
	for _, f := range r.Files {
		names = append(names, f.DisplayPath)
	}
	return strings.Join(names, " ")
}

func wantListed(t *testing.T, r *agentv1.GetSessionDiffResponse, want string) {
	t.Helper()
	if got := listed(r); got != want {
		t.Fatalf("files: %q, want %q", got, want)
	}
}

func wantLabels(t *testing.T, r *agentv1.GetSessionDiffResponse, kind agentv1.DiffBaseKind, base, head string) {
	t.Helper()
	if r.BaseKind != kind || r.BaseLabel != base || r.HeadLabel != head {
		t.Fatalf("base: %v %q %q, want %v %q %q", r.BaseKind, r.BaseLabel, r.HeadLabel, kind, base, head)
	}
}

func commitFile(t *testing.T, repo, rel, content string) string {
	t.Helper()
	gittest.Write(t, repo, rel, content)
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "add "+rel)
	return gittest.Run(t, repo, "rev-parse", "HEAD")
}

// A repository on branch work, one commit ahead of a main that moved on since, with an edit, a staged file and an
// untracked one.
func branchedRepo(t *testing.T) string {
	t.Helper()
	repo := gittest.Init(t)
	gittest.Run(t, repo, "checkout", "-q", "-b", "work")
	commitFile(t, repo, "committed.txt", "c\n")
	gittest.Run(t, repo, "checkout", "-q", "main")
	commitFile(t, repo, "on-main.txt", "m\n")
	gittest.Run(t, repo, "checkout", "-q", "work")
	gittest.Write(t, repo, "README.md", "hello\nedited\n")
	gittest.Write(t, repo, "staged.txt", "s\n")
	gittest.Run(t, repo, "add", "staged.txt")
	gittest.Write(t, repo, "untracked.txt", "u\n")
	return repo
}

func TestDiffBaseAll(t *testing.T) {
	const all = agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL

	t.Run("another branch: the merge base with the default one", func(t *testing.T) {
		repo := branchedRepo(t)
		for _, base := range []*agentv1.DiffBase{nil, baseAll, {}} {
			r := diffAt(t, repoSrc(repo), base)
			wantListed(t, r, "README.md committed.txt staged.txt untracked.txt")
			wantLabels(t, r, all, "main", "working tree")
		}
	})

	t.Run("on the default branch: HEAD", func(t *testing.T) {
		repo := gittest.Init(t)
		commitFile(t, repo, "second.txt", "x\n")
		gittest.Write(t, repo, "README.md", "hello\nedited\n")
		r := diffAt(t, repoSrc(repo), nil)
		wantListed(t, r, "README.md")
		wantLabels(t, r, all, "HEAD", "working tree")
	})

	t.Run("in a worktree of the client: its base ref", func(t *testing.T) {
		repo := branchedRepo(t)
		gittest.Run(t, repo, "branch", "release", "HEAD~1")
		src := repoSrc(repo)
		src.baseRef = "release"
		r := diffAt(t, src, nil)
		wantLabels(t, r, all, "release", "working tree")
		if diffFile(r, "committed.txt") == nil {
			t.Fatalf("committed since the base: %q", listed(r))
		}
		// The ref is gone: HEAD is left, not the default branch.
		src.baseRef = "no-such-ref"
		r = diffAt(t, src, nil)
		wantLabels(t, r, all, "HEAD", "working tree")
		if diffFile(r, "committed.txt") != nil {
			t.Fatalf("against HEAD: %q", listed(r))
		}
	})

	t.Run("no commits yet", func(t *testing.T) {
		gittest.Isolate(t)
		repo, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, repo, "init", "-q", "-b", "main")
		gittest.Write(t, repo, "a.txt", "a\n")
		r := diffAt(t, repoSrc(repo), nil)
		wantListed(t, r, "a.txt")
		wantLabels(t, r, all, labelNoCommits, "working tree")
	})
}

func TestDefaultBranchDetection(t *testing.T) {
	head := func(t *testing.T, repo string) string {
		t.Helper()
		return diffAt(t, repoSrc(repo), nil).BaseLabel
	}
	setOriginHead := func(t *testing.T, repo, name string) {
		t.Helper()
		gittest.Run(t, repo, "update-ref", "refs/remotes/origin/"+name, "HEAD")
		gittest.Run(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+name)
	}

	t.Run("origin/HEAD comes first", func(t *testing.T) {
		repo := gittest.Init(t)
		setOriginHead(t, repo, "trunk")
		gittest.Run(t, repo, "checkout", "-q", "-b", "work")
		if got := head(t, repo); got != "origin/trunk" {
			t.Fatalf("label %q", got)
		}
	})
	t.Run("on the branch origin/HEAD names", func(t *testing.T) {
		repo := gittest.Init(t)
		setOriginHead(t, repo, "main")
		commitFile(t, repo, "unpushed.txt", "x\n")
		r := diffAt(t, repoSrc(repo), nil)
		wantListed(t, r, "")
		wantLabels(t, r, agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL, "HEAD", "working tree")
	})
	t.Run("a stale origin/HEAD gives way to main", func(t *testing.T) {
		repo := gittest.Init(t)
		gittest.Run(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/gone")
		gittest.Run(t, repo, "checkout", "-q", "-b", "work")
		if got := head(t, repo); got != "main" {
			t.Fatalf("label %q", got)
		}
	})
	t.Run("master", func(t *testing.T) {
		repo := gittest.Init(t)
		gittest.Run(t, repo, "branch", "-m", "main", "master")
		gittest.Run(t, repo, "checkout", "-q", "-b", "work")
		if got := head(t, repo); got != "master" {
			t.Fatalf("label %q", got)
		}
	})
	t.Run("none", func(t *testing.T) {
		repo := gittest.Init(t)
		gittest.Run(t, repo, "branch", "-m", "main", "work")
		if got := head(t, repo); got != "HEAD" {
			t.Fatalf("label %q", got)
		}
	})
	t.Run("a tag named main is not the branch", func(t *testing.T) {
		repo := gittest.Init(t)
		gittest.Run(t, repo, "branch", "-m", "main", "work")
		gittest.Run(t, repo, "tag", "main")
		if got := head(t, repo); got != "HEAD" {
			t.Fatalf("label %q", got)
		}
	})
	t.Run("detached: compared with the default branch", func(t *testing.T) {
		repo := gittest.Init(t)
		commitFile(t, repo, "second.txt", "x\n")
		gittest.Run(t, repo, "branch", "work")
		gittest.Run(t, repo, "reset", "-q", "--hard", "HEAD~1")
		gittest.Run(t, repo, "checkout", "-q", "--detach", "work")
		r := diffAt(t, repoSrc(repo), nil)
		wantListed(t, r, "second.txt")
		wantLabels(t, r, agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL, "main", "working tree")
	})
}

func TestDiffBaseUncommitted(t *testing.T) {
	repo := branchedRepo(t)
	r := diffAt(t, repoSrc(repo), baseUncommitted)
	wantListed(t, r, "README.md staged.txt untracked.txt")
	wantLabels(t, r, agentv1.DiffBaseKind_DIFF_BASE_KIND_UNCOMMITTED, "HEAD", "working tree")
	if r.Version == diffAt(t, repoSrc(repo), nil).Version {
		t.Fatal("two bases, one version")
	}
	// In a worktree of the client, too.
	src := repoSrc(repo)
	src.baseRef = "main"
	wantListed(t, diffAt(t, src, baseUncommitted), "README.md staged.txt untracked.txt")
}

func TestDiffBaseRef(t *testing.T) {
	repo := branchedRepo(t)
	r := diffAt(t, repoSrc(repo), baseRef("main"))
	// The merge base, not main itself: what main gained since is not this branch's doing.
	wantListed(t, r, "README.md committed.txt staged.txt untracked.txt")
	wantLabels(t, r, agentv1.DiffBaseKind_DIFF_BASE_KIND_REF, "main", "working tree")

	// A ref the default rule would not pick.
	gittest.Run(t, repo, "branch", "release", "HEAD")
	wantListed(t, diffAt(t, repoSrc(repo), baseRef("release")), "README.md staged.txt untracked.txt")
	// A tag and a full name work as well.
	gittest.Run(t, repo, "tag", "v1", "HEAD~1")
	wantListed(t, diffAt(t, repoSrc(repo), baseRef("v1")), "README.md committed.txt staged.txt untracked.txt")
	wantListed(t, diffAt(t, repoSrc(repo), baseRef("refs/heads/release")), "README.md staged.txt untracked.txt")

	if _, err := computeSessionDiff(context.Background(), repoSrc(repo), baseRef("no-such"), ""); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing ref: %v", err)
	}

	// No history in common: nothing to compare with.
	gittest.Run(t, repo, "checkout", "-q", "--orphan", "other")
	gittest.Run(t, repo, "commit", "-q", "-m", "orphan")
	if _, err := computeSessionDiff(context.Background(), repoSrc(repo), baseRef("main"), ""); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unrelated: %v", err)
	}
}

func TestDiffBaseRefAcrossRepositories(t *testing.T) {
	one := branchedRepo(t)
	two := gittest.Init(t)
	gittest.Write(t, two, "README.md", "second repository\n")
	src := &diffSources{cwd: one, dirs: []string{one, two}}

	gittest.Run(t, one, "branch", "only-in-one", "HEAD~1")
	r := diffAt(t, src, baseRef("only-in-one"))
	wantLabels(t, r, agentv1.DiffBaseKind_DIFF_BASE_KIND_REF, "only-in-one", "working tree")
	if diffFile(r, "committed.txt") == nil || diffFile(r, filepath.Base(two)+"/README.md") == nil {
		t.Fatalf("both repositories: %q", listed(r))
	}

	// A commit is of one repository: nothing else shows.
	sha := gittest.Run(t, one, "rev-parse", "HEAD")
	wantListed(t, diffAt(t, src, baseCommit(sha)), "committed.txt")
}

func TestDiffRefValidation(t *testing.T) {
	for _, ref := range []string{"main", "origin/main", "feature/x-1", "v1.2.3", "release_2", "HEAD", "refs/heads/main", "задача"} {
		if !validDiffRef(ref) {
			t.Errorf("refused %q", ref)
		}
	}
	bad := []string{
		"", "-", "-x", "--output=/tmp/pwned", "--all", "--end-of-options", "main..work", "main...work", "main~1", "main^", "main^{tree}",
		"main:README.md", ":/fix", "HEAD@{1}", "@{-1}", "a b", "a\nb", "a\x00b", "a\tb", "a*", "a?", "a[", "a\\b", "\xff\xfe", strings.Repeat("a", maxDiffRefBytes+1),
	}
	repo := gittest.Init(t)
	for _, ref := range bad {
		if validDiffRef(ref) {
			t.Errorf("accepted %q", ref)
		}
		if _, err := computeSessionDiff(context.Background(), repoSrc(repo), baseRef(ref), ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("ref %q: %v", ref, err)
		}
		if _, err := computeSessionFileDiff(context.Background(), repoSrc(repo), baseRef(ref), filepath.Join(repo, "README.md")); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("file diff, ref %q: %v", ref, err)
		}
	}
	for _, sha := range []string{"", "abc", "-abc", "--all", "HEAD", "main", "abcd..ef01", "abcd^", strings.Repeat("a", 65), "abcg"} {
		if _, err := computeSessionDiff(context.Background(), repoSrc(repo), baseCommit(sha), ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("sha %q: %v", sha, err)
		}
	}
	if _, err := computeSessionDiff(context.Background(), repoSrc(repo), &agentv1.DiffBase{Kind: 99}, ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown kind: %v", err)
	}

	// An option in the ref's place never reaches git: git would write this file.
	out := filepath.Join(t.TempDir(), "pwned")
	if _, err := computeSessionDiff(context.Background(), repoSrc(repo), baseRef("--output="+out), ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("option as a ref: %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("git took the ref for an option")
	}
	// The name a request gives goes to `rev-parse --verify` alone, and diff gets the commit.
	gitLog := logGit(t)
	gittest.Run(t, repo, "branch", "named-ref")
	diffAt(t, repoSrc(repo), baseRef("named-ref"))
	for _, line := range gitLog() {
		if strings.Contains(line, "named-ref") && !strings.Contains(line, "rev-parse --verify -q named-ref^{commit}") && !strings.HasPrefix(line, "branch ") {
			t.Fatalf("the ref reached git as: %s", line)
		}
	}
}

func TestDiffBaseCommit(t *testing.T) {
	repo := gittest.Init(t)
	root := gittest.Run(t, repo, "rev-parse", "HEAD")
	first := commitFile(t, repo, "first.txt", "1\n2\n")
	gittest.Write(t, repo, "first.txt", "1\nchanged\n")
	gittest.Write(t, repo, "second.txt", "s\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "second")
	second := gittest.Run(t, repo, "rev-parse", "HEAD")
	// The working tree must not matter: it edits the same file, and adds another.
	gittest.Write(t, repo, "first.txt", "entirely\nother\ncontent\n")
	gittest.Write(t, repo, "untracked.txt", "u\n")
	outside := filepath.Join(t.TempDir(), "notes.txt")
	writeTree(t, outside, "now\n")
	baseline := filepath.Join(t.TempDir(), "backup")
	writeTree(t, baseline, "before\n")
	src := repoSrc(repo)
	src.baselines = map[string]string{outside: baseline}

	r := diffAt(t, src, baseCommit(second))
	wantListed(t, r, "first.txt second.txt")
	wantLabels(t, r, agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT, first[:7], second[:7])
	f := wantDiff(t, r, "first.txt")
	if f.Additions != 1 || f.Deletions != 1 || !strings.Contains(hunkText(f), "+changed") {
		t.Fatalf("the commit's change: %+v", f)
	}

	// Stable under edits of the working tree.
	v := r.Version
	gittest.Write(t, repo, "first.txt", "edited again\n")
	if again := diffAt(t, src, baseCommit(second)); again.Version != v {
		t.Fatal("the working tree moved a commit's version")
	}

	// An abbreviated name; the root commit, against nothing.
	wantListed(t, diffAt(t, src, baseCommit(second[:10])), "first.txt second.txt")
	r = diffAt(t, src, baseCommit(root))
	wantListed(t, r, "README.md src/app/main.go")
	wantLabels(t, r, agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT, labelEmptyTree, root[:7])

	if _, err := computeSessionDiff(context.Background(), src, baseCommit("deadbeefdeadbeef"), ""); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing commit: %v", err)
	}
}

func TestDiffBaseCommitMergeAndSizes(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Run(t, repo, "checkout", "-q", "-b", "side")
	commitFile(t, repo, "from-side.txt", "s\n")
	gittest.Run(t, repo, "checkout", "-q", "main")
	commitFile(t, repo, "from-main.txt", "m\n")
	gittest.Run(t, repo, "merge", "-q", "--no-edit", "--no-ff", "side")
	merge := gittest.Run(t, repo, "rev-parse", "HEAD")
	// A merge: what it brought to its first parent.
	wantListed(t, diffAt(t, repoSrc(repo), baseCommit(merge)), "from-side.txt")

	// The size that counts is the commit's, whatever is on disk now.
	big := commitFile(t, repo, "big.txt", strings.Repeat("x\n", maxDiffFileBytes))
	gittest.Write(t, repo, "big.txt", "small now\n")
	if f := wantDiff(t, diffAt(t, repoSrc(repo), baseCommit(big)), "big.txt"); !f.TooLarge || len(f.Hunks) != 0 {
		t.Fatalf("big in the commit: %+v", f)
	}
}

func TestDiffBaseOutsideGit(t *testing.T) {
	gittest.Isolate(t)
	dir := filepath.Join(t.TempDir(), "plain")
	writeTree(t, filepath.Join(dir, "a.txt"), "one\n")
	src := &diffSources{cwd: dir, dirs: []string{dir}, folderStore: t.TempDir()}
	diffOf(t, src) // the snapshot
	writeTree(t, filepath.Join(dir, "a.txt"), "one\ntwo\n")
	writeTree(t, filepath.Join(dir, "new.txt"), "n\n")

	for _, base := range []*agentv1.DiffBase{nil, baseAll, baseUncommitted, baseRef("main"), baseCommit("abcdef12")} {
		r := diffAt(t, src, base)
		wantListed(t, r, "a.txt new.txt")
		wantLabels(t, r, agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL, labelSessionStart, "working tree")
		if wantDiff(t, r, "a.txt").Additions != 1 {
			t.Fatalf("snapshot rules: %+v", r)
		}
	}
	// The name is still checked: what it would mean does not depend on where it is asked.
	if _, err := computeSessionDiff(context.Background(), src, baseRef("-x"), ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad ref outside git: %v", err)
	}

	// One file on demand, with the same rules.
	f, err := computeSessionFileDiff(context.Background(), src, nil, filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hunkText(f), "+two") || f.Source != agentv1.FileDiffSource_FILE_DIFF_SOURCE_HISTORY {
		t.Fatalf("file: %+v", f)
	}

	// Many files outside git make a large diff as well.
	for i := range largeDiffFiles {
		writeTree(t, filepath.Join(dir, "many", fmt.Sprintf("f%d.txt", i)), "x\n")
	}
	r := diffAt(t, src, nil)
	if !r.Large || r.FilesChanged != largeDiffFiles+2 || r.Additions != largeDiffFiles+2 {
		t.Fatalf("large outside git: %d files, %d additions, large %v", r.FilesChanged, r.Additions, r.Large)
	}
	for _, f := range r.Files {
		if len(f.Hunks) != 0 {
			t.Fatalf("hunks in a large diff: %s", f.Path)
		}
	}
}

func TestListDiffBases(t *testing.T) {
	ctx := context.Background()
	repo := branchedRepo(t)
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "Правка \"двух\" файлов")
	gittest.Run(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	gittest.Run(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	r, err := listDiffBases(ctx, repoSrc(repo))
	if err != nil {
		t.Fatal(err)
	}
	if !r.InGit || r.DefaultBaseRef != "origin/main" || r.DefaultBaseLabel != "origin/main" {
		t.Fatalf("default: %+v", r)
	}
	if mb := gittest.Run(t, repo, "merge-base", "main", "HEAD"); r.DefaultBaseSha != mb {
		t.Fatalf("default sha %s, want %s", r.DefaultBaseSha, mb)
	}
	var names []string
	for _, b := range r.Branches {
		names = append(names, b.Name)
		if b.Current != (b.Name == "work") || b.Remote != strings.HasPrefix(b.Name, "origin/") {
			t.Fatalf("branch: %+v", b)
		}
	}
	// Local ones first; those two have the same commit time here.
	if got := strings.Join(names, " "); got != "work main origin/main" && got != "main work origin/main" {
		t.Fatalf("branches: %s", got)
	}
	if len(r.Commits) != 2 || r.Commits[0].Subject != "Правка \"двух\" файлов" || r.Commits[1].Subject != "add committed.txt" {
		t.Fatalf("commits: %+v", r.Commits)
	}
	if r.Commits[0].Sha != gittest.Run(t, repo, "rev-parse", "HEAD") || !strings.HasSuffix(r.Commits[0].CommittedAt, "Z") || r.CommitsTruncated {
		t.Fatalf("commit: %+v", r.Commits[0])
	}

	// On the base itself: nothing since it.
	gittest.Run(t, repo, "checkout", "-q", "main")
	r, err = listDiffBases(ctx, repoSrc(repo))
	if err != nil {
		t.Fatal(err)
	}
	if !r.InGit || r.DefaultBaseRef != "HEAD" || r.DefaultBaseLabel != "HEAD" || r.DefaultBaseSha != gittest.Run(t, repo, "rev-parse", "HEAD") || len(r.Commits) != 0 {
		t.Fatalf("on main: %+v", r)
	}

	// In a worktree of the client: since its base.
	gittest.Run(t, repo, "checkout", "-q", "work")
	src := repoSrc(repo)
	src.baseRef = "HEAD~1"
	if r, err = listDiffBases(ctx, src); err != nil {
		t.Fatal(err)
	}
	if r.DefaultBaseLabel != "HEAD~1" || len(r.Commits) != 1 {
		t.Fatalf("base ref: %+v", r)
	}
}

func TestListDiffBasesEdges(t *testing.T) {
	ctx := context.Background()

	// Outside git.
	gittest.Isolate(t)
	plain := t.TempDir()
	r, err := listDiffBases(ctx, repoSrc(plain))
	if err != nil {
		t.Fatal(err)
	}
	if r.InGit || len(r.Branches) != 0 || len(r.Commits) != 0 || r.DefaultBaseRef != "" {
		t.Fatalf("outside git: %+v", r)
	}

	// No commits yet.
	fresh, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, fresh, "init", "-q", "-b", "main")
	r, err = listDiffBases(ctx, repoSrc(fresh))
	if err != nil {
		t.Fatal(err)
	}
	if !r.InGit || r.DefaultBaseRef != "" || r.DefaultBaseSha != "" || r.DefaultBaseLabel != labelNoCommits || len(r.Commits) != 0 {
		t.Fatalf("no commits: %+v", r)
	}

	// The cap on commits.
	repo := gittest.Init(t)
	gittest.Run(t, repo, "checkout", "-q", "-b", "work")
	var stream strings.Builder
	stream.WriteString("reset refs/heads/work\nfrom refs/heads/main\n\n")
	for i := range maxDiffCommits + 3 {
		msg := fmt.Sprintf("c%d", i)
		fmt.Fprintf(&stream, "commit refs/heads/work\ncommitter T <t@example.com> %d +0000\ndata %d\n%s\n\n", 1700000000+i, len(msg), msg)
	}
	cmd := exec.Command("git", "-C", repo, "fast-import", "--quiet", "--force")
	cmd.Stdin = strings.NewReader(stream.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, out)
	}
	r, err = listDiffBases(ctx, repoSrc(repo))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Commits) != maxDiffCommits || !r.CommitsTruncated || r.Commits[0].Subject != fmt.Sprintf("c%d", maxDiffCommits+2) {
		t.Fatalf("cap: %d commits, truncated %v, first %+v", len(r.Commits), r.CommitsTruncated, r.Commits[0])
	}
}

// A repository with more changed files than the threshold, of every kind: edits, a rename, a binary file, a too large
// one, a deletion, an untracked file.
func largeRepo(t *testing.T) string {
	t.Helper()
	repo := gittest.Init(t)
	for i := range largeDiffFiles + 5 {
		gittest.Write(t, repo, fmt.Sprintf("many/f%d.txt", i), "keep\nold\n")
	}
	gittest.Write(t, repo, "renamed-from.txt", strings.Repeat("the same line\n", 30))
	gittest.Write(t, repo, "image.bin", "\x00\x01old")
	gittest.Write(t, repo, "big.txt", "small\n")
	gittest.Write(t, repo, "gone.txt", "a\nb\nc\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "fill")
	for i := range largeDiffFiles + 5 {
		gittest.Write(t, repo, fmt.Sprintf("many/f%d.txt", i), "keep\nnew\nmore\n")
	}
	gittest.Run(t, repo, "mv", "renamed-from.txt", "renamed-to.txt")
	gittest.Write(t, repo, "renamed-to.txt", strings.Repeat("the same line\n", 30)+"one more\n")
	gittest.Write(t, repo, "image.bin", "\x00\x01new")
	gittest.Write(t, repo, "big.txt", strings.Repeat("x\n", maxDiffFileBytes))
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	gittest.Write(t, repo, "untracked.txt", "u\nv\n")
	return repo
}

func TestLargeDiff(t *testing.T) {
	repo := largeRepo(t)
	src := repoSrc(repo)
	r := diffOf(t, src)
	if !r.Large || r.RefreshAfterMs != largeDiffRefreshMs || r.Truncated {
		t.Fatalf("large %v, refresh %d, truncated %v", r.Large, r.RefreshAfterMs, r.Truncated)
	}
	for _, f := range r.Files {
		if len(f.Hunks) != 0 {
			t.Fatalf("hunks in a large diff: %s", f.Path)
		}
		if f.DisplayPath == "" || f.Source != agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT {
			t.Fatalf("file: %+v", f)
		}
	}
	// 45 edits (+2 -1 each), the rename (+1), the deletion (-3), the untracked file (+2); binary and too large count 0.
	wantFiles, wantAdd, wantDel := int32(largeDiffFiles+5+5), int32(2*(largeDiffFiles+5)+1+2), int32(largeDiffFiles+5+3)
	if r.FilesChanged != wantFiles || r.Additions != wantAdd || r.Deletions != wantDel {
		t.Fatalf("totals: %d +%d -%d, want %d +%d -%d", r.FilesChanged, r.Additions, r.Deletions, wantFiles, wantAdd, wantDel)
	}

	// The same counts a full patch gives.
	var add, del int32
	for _, f := range parseGitPatch([]byte(gittest.Run(t, repo, "diff", "-M", "--no-color", "HEAD", "--", ":(exclude)big.txt"))) {
		add += f.Additions
		del += f.Deletions
	}
	if add+2 != r.Additions || del != r.Deletions {
		t.Fatalf("against the patch: +%d -%d", add, del)
	}

	ren := wantDiff(t, r, "renamed-to.txt")
	if ren.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_RENAMED || ren.OldPath != filepath.Join(repo, "renamed-from.txt") || ren.Additions != 1 || ren.Deletions != 0 {
		t.Fatalf("rename: %+v", ren)
	}
	if diffFile(r, "renamed-from.txt") != nil {
		t.Fatal("the old name is listed as well")
	}
	if bin := wantDiff(t, r, "image.bin"); !bin.Binary || bin.TooLarge || bin.Additions != 0 {
		t.Fatalf("binary: %+v", bin)
	}
	if big := wantDiff(t, r, "big.txt"); !big.TooLarge || big.Binary || big.Additions != 0 {
		t.Fatalf("too large: %+v", big)
	}
	if gone := wantDiff(t, r, "gone.txt"); gone.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED || gone.Deletions != 3 {
		t.Fatalf("deleted: %+v", gone)
	}
	if un := wantDiff(t, r, "untracked.txt"); un.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED || un.Additions != 2 {
		t.Fatalf("untracked: %+v", un)
	}

	// Asking again answers the same, from what is remembered, and not_modified keeps all but the files.
	again := diffOf(t, src)
	if again.Version != r.Version || listed(again) != listed(r) || again.Additions != r.Additions {
		t.Fatalf("again: %+v", again)
	}
	same, err := computeSessionDiff(context.Background(), src, nil, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !same.NotModified || len(same.Files) != 0 || !same.Large || same.RefreshAfterMs != largeDiffRefreshMs || same.Additions != r.Additions || same.FilesChanged != r.FilesChanged || same.BaseLabel != r.BaseLabel || same.BaseKind != r.BaseKind {
		t.Fatalf("not modified: %+v", same)
	}
	// The stats agree.
	if ds := sessionDiffStats(context.Background(), src); ds.FilesChanged != r.FilesChanged || ds.Additions != r.Additions || ds.Deletions != r.Deletions {
		t.Fatalf("stats: %+v", ds)
	}
}

func TestLargeDiffThresholds(t *testing.T) {
	repo := gittest.Init(t)
	for i := range largeDiffFiles {
		gittest.Write(t, repo, fmt.Sprintf("f%d.txt", i), "x\n")
	}
	r := diffOf(t, repoSrc(repo))
	if r.Large || r.FilesChanged != largeDiffFiles || r.RefreshAfterMs != diffRefreshMs || len(r.Files[0].Hunks) != 1 {
		t.Fatalf("at the file limit: large %v, %d files, refresh %d", r.Large, r.FilesChanged, r.RefreshAfterMs)
	}
	gittest.Write(t, repo, "one-more.txt", "x\n")
	if r := diffOf(t, repoSrc(repo)); !r.Large || r.RefreshAfterMs != largeDiffRefreshMs {
		t.Fatalf("past the file limit: %v", r.Large)
	}

	lines := gittest.Init(t)
	gittest.Write(t, lines, "long.txt", strings.Repeat("l\n", largeDiffLines))
	gittest.Run(t, lines, "add", "-A") // tracked, so that git counts it
	r = diffOf(t, repoSrc(lines))
	if r.Large || r.Additions != largeDiffLines || len(wantDiff(t, r, "long.txt").Hunks) != 1 {
		t.Fatalf("at the line limit: large %v, %d lines", r.Large, r.Additions)
	}
	gittest.Write(t, lines, "long.txt", strings.Repeat("l\n", largeDiffLines+1))
	if r := diffOf(t, repoSrc(lines)); !r.Large || r.Additions != largeDiffLines+1 || len(wantDiff(t, r, "long.txt").Hunks) != 0 {
		t.Fatalf("past the line limit: large %v, %d lines", r.Large, r.Additions)
	}
}

// Past the file cap the list stops as it always did, and such a diff is large.
func TestLargeDiffTruncated(t *testing.T) {
	repo := gittest.Init(t)
	for i := range maxDiffFiles + 5 {
		gittest.Write(t, repo, fmt.Sprintf("tracked/f%d.txt", i), "x\n")
	}
	gittest.Run(t, repo, "add", "-A")
	r := diffOf(t, repoSrc(repo))
	if !r.Large || !r.Truncated || len(r.Files) != maxDiffFiles || r.Additions != maxDiffFiles {
		t.Fatalf("large %v, truncated %v, %d files, +%d", r.Large, r.Truncated, len(r.Files), r.Additions)
	}
}

func TestSessionFileDiff(t *testing.T) {
	ctx := context.Background()
	repo := largeRepo(t)
	src := repoSrc(repo)
	file := func(rel string) *agentv1.FileDiff {
		t.Helper()
		f, err := computeSessionFileDiff(ctx, src, nil, filepath.Join(repo, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if f.DisplayPath != rel {
			t.Fatalf("%s: display %q", rel, f.DisplayPath)
		}
		return f
	}

	if f := file("many/f3.txt"); f.Additions != 2 || f.Deletions != 1 || !strings.Contains(hunkText(f), "+new") || !strings.Contains(hunkText(f), "-old") || f.Source != agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT {
		t.Fatalf("edit: %+v", f)
	}
	if f := file("renamed-to.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_RENAMED || f.OldPath != filepath.Join(repo, "renamed-from.txt") || f.Additions != 1 || !strings.Contains(hunkText(f), "+one more") {
		t.Fatalf("rename: %+v", f)
	}
	if f := file("image.bin"); !f.Binary || len(f.Hunks) != 0 {
		t.Fatalf("binary: %+v", f)
	}
	if f := file("big.txt"); !f.TooLarge || len(f.Hunks) != 0 {
		t.Fatalf("too large: %+v", f)
	}
	if f := file("gone.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED || f.Deletions != 3 || len(f.Hunks) != 1 {
		t.Fatalf("deleted: %+v", f)
	}
	if f := file("untracked.txt"); f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED || !strings.Contains(hunkText(f), "+v") {
		t.Fatalf("untracked: %+v", f)
	}

	// What is not a change of this diff is not served, whatever it is.
	secret := filepath.Join(t.TempDir(), "secret.txt")
	writeTree(t, secret, "secret\n")
	for _, p := range []string{filepath.Join(repo, "README.md"), filepath.Join(repo, "renamed-from.txt"), filepath.Join(repo, ".git/config"), secret, "/etc/hosts", "", "--all", "many/f3.txt"} {
		if _, err := computeSessionFileDiff(ctx, src, nil, p); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestSessionFileDiffBases(t *testing.T) {
	ctx := context.Background()
	repo := branchedRepo(t)
	src := repoSrc(repo)
	committed := filepath.Join(repo, "committed.txt")
	if _, err := computeSessionFileDiff(ctx, src, nil, committed); err != nil {
		t.Fatalf("under ALL: %v", err)
	}
	if _, err := computeSessionFileDiff(ctx, src, baseRef("main"), committed); err != nil {
		t.Fatalf("under REF: %v", err)
	}
	if _, err := computeSessionFileDiff(ctx, src, baseUncommitted, committed); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("under UNCOMMITTED: %v", err)
	}
	sha := gittest.Run(t, repo, "rev-parse", "HEAD")
	f, err := computeSessionFileDiff(ctx, src, baseCommit(sha), committed)
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED || !strings.Contains(hunkText(f), "+c") {
		t.Fatalf("under COMMIT: %+v", f)
	}
	// A commit's file need not exist any more.
	if err := os.Remove(committed); err != nil {
		t.Fatal(err)
	}
	if _, err := computeSessionFileDiff(ctx, src, baseCommit(sha), committed); err != nil {
		t.Fatalf("removed since: %v", err)
	}
}

// logGit puts a recording git first on the PATH and returns what it has seen since the last call: a real large repo
// shows what a poll runs, not what we believe it runs.
func logGit(t *testing.T) func() []string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexec %q \"$@\"\n", log, real)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		raw, _ := os.ReadFile(log)
		_ = os.Remove(log)
		var lines []string
		for _, l := range strings.Split(string(raw), "\n") {
			if l != "" {
				_, l, _ = strings.Cut(l, "core.quotepath=off ") // diffGit's prefix
				lines = append(lines, l)
			}
		}
		return lines
	}
}

func patchRuns(lines []string) (patches, numstats []string) {
	for _, l := range lines {
		if !strings.HasPrefix(l, "diff ") {
			continue
		}
		switch {
		case strings.Contains(l, "--numstat"):
			numstats = append(numstats, l)
		case !strings.Contains(l, "--raw"):
			patches = append(patches, l)
		}
	}
	return patches, numstats
}

func TestLargeDiffPollRunsNoPatch(t *testing.T) {
	repo := largeRepo(t)
	src := repoSrc(repo)
	gitLog := logGit(t)
	ctx := context.Background()

	// The first answer needs the totals, and no patch.
	r := diffOf(t, src)
	patches, numstats := patchRuns(gitLog())
	if !r.Large || len(patches) != 0 || len(numstats) != 1 {
		t.Fatalf("first: large %v, patches %v, numstats %v", r.Large, patches, numstats)
	}
	// A file over the limit is not even counted: git would have to read it.
	if !strings.Contains(numstats[0], ":(top,literal,exclude)big.txt") {
		t.Fatalf("numstat: %s", numstats[0])
	}

	// An unchanged poll: neither.
	same, err := computeSessionDiff(ctx, src, nil, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	patches, numstats = patchRuns(gitLog())
	if !same.NotModified || len(patches) != 0 || len(numstats) != 0 {
		t.Fatalf("unchanged: not modified %v, patches %v, numstats %v", same.NotModified, patches, numstats)
	}

	// A measurement of diff_stats, the same.
	if ds := sessionDiffStats(ctx, src); ds == nil || ds.Additions != r.Additions {
		t.Fatalf("stats: %+v", ds)
	}
	patches, numstats = patchRuns(gitLog())
	if len(patches) != 0 || len(numstats) != 0 {
		t.Fatalf("stats: patches %v, numstats %v", patches, numstats)
	}

	// Another edit of a file already changed: `diff --raw` prints the same line, the poll must still see it.
	gittest.Write(t, repo, "many/f1.txt", "keep\nnew\nmore\nand more\n")
	later, err := computeSessionDiff(ctx, src, nil, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	patches, numstats = patchRuns(gitLog())
	if later.NotModified || later.Version == r.Version || later.Additions != r.Additions+1 || len(patches) != 0 || len(numstats) != 1 {
		t.Fatalf("edited: %v +%d, patches %v, numstats %v", later.NotModified, later.Additions, patches, numstats)
	}

	// So must an edit of an untracked file.
	gittest.Write(t, repo, "untracked.txt", "u\nv\nw\n")
	if again, err := computeSessionDiff(ctx, src, nil, later.Version); err != nil || again.NotModified || again.Additions != later.Additions+1 {
		t.Fatalf("untracked edited: %+v, %v", again, err)
	}
	gitLog()

	// The stats after a change count it without a patch, too.
	gittest.Write(t, repo, "many/f2.txt", "keep\nnew\n")
	if ds := sessionDiffStats(ctx, src); ds == nil {
		t.Fatal("stats failed")
	}
	if patches, _ = patchRuns(gitLog()); len(patches) != 0 {
		t.Fatalf("stats after a change: %v", patches)
	}

	// The one file asked for is the one patched.
	if _, err := computeSessionFileDiff(ctx, src, nil, filepath.Join(repo, "many/f3.txt")); err != nil {
		t.Fatal(err)
	}
	patches, _ = patchRuns(gitLog())
	if len(patches) != 1 || !strings.HasSuffix(patches[0], ":(top,literal)many/f3.txt") {
		t.Fatalf("file diff: %v", patches)
	}
}

func TestSmallDiffNotModifiedRunsNoPatch(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, "README.md", "hello\nedited\n")
	src := repoSrc(repo)
	gitLog := logGit(t)
	r := diffOf(t, src)
	if patches, _ := patchRuns(gitLog()); r.Large || len(patches) != 1 || len(r.Files[0].Hunks) != 1 {
		t.Fatalf("first: %v, %+v", patches, r)
	}
	same, err := computeSessionDiff(context.Background(), src, nil, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if patches, numstats := patchRuns(gitLog()); !same.NotModified || same.Additions != 1 || len(patches) != 0 || len(numstats) != 0 {
		t.Fatalf("not modified: %+v, patches %v, numstats %v", same, patches, numstats)
	}
	// A version that came from another client lifetime: no summary to reuse, and still no patch.
	diffSummaries.Lock()
	diffSummaries.entries = map[string]*diffSummary{}
	diffSummaries.Unlock()
	same, err = computeSessionDiff(context.Background(), src, nil, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if patches, _ := patchRuns(gitLog()); !same.NotModified || same.Additions != 1 || len(patches) != 0 {
		t.Fatalf("fresh not modified: %+v, patches %v", same, patches)
	}
	// Without if_none_match a small diff has its hunks, as ever, also when a summary is remembered.
	if again := diffOf(t, src); len(again.Files[0].Hunks) != 1 {
		t.Fatalf("hunks: %+v", again.Files[0])
	}
}

func TestParseNumstat(t *testing.T) {
	got := parseNumstat([]byte("3\t1\ta b.txt\x00-\t-\timg.bin\x000\t0\t\x00old\tname\x00new\tname\x005\t0\tlast\x00"))
	want := []numstatEntry{
		{path: "a b.txt", additions: 3, deletions: 1},
		{path: "img.bin", binary: true},
		{path: "new\tname", oldPath: "old\tname"},
		{path: "last", additions: 5},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

// What a poll of an unchanged large diff costs: compare with BenchmarkSessionDiffLargeMeasure. Run with -bench.
func BenchmarkSessionDiffLargePoll(b *testing.B) {
	src, version := benchLargeRepo(b)
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		r, err := computeSessionDiff(ctx, src, nil, version)
		if err != nil || !r.NotModified {
			b.Fatalf("%v %+v", err, r)
		}
	}
}

// The measurement behind it, when something did change.
func BenchmarkSessionDiffLargeMeasure(b *testing.B) {
	src, _ := benchLargeRepo(b)
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		diffSummaries.Lock()
		diffSummaries.entries = map[string]*diffSummary{}
		diffSummaries.Unlock()
		if _, err := computeSessionDiff(ctx, src, nil, ""); err != nil {
			b.Fatal(err)
		}
	}
}

func benchLargeRepo(b *testing.B) (*diffSources, string) {
	b.Helper()
	dir, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	text := strings.Repeat("a line of a generated file\n", 2000)
	for i := range maxDiffFiles {
		writeBench(b, filepath.Join(dir, "pkg", fmt.Sprintf("f%d.txt", i)), text)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "fill")
	for i := range maxDiffFiles {
		writeBench(b, filepath.Join(dir, "pkg", fmt.Sprintf("f%d.txt", i)), text+"changed\n")
	}
	src := repoSrc(dir)
	r, err := computeSessionDiff(context.Background(), src, nil, "")
	if err != nil {
		b.Fatal(err)
	}
	return src, r.Version
}

func writeBench(b *testing.B, path, text string) {
	b.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		b.Fatal(err)
	}
}
