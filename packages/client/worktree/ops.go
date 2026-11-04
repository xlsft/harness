package worktree

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// What a running session does with its worktree and branch: leaving (Inspect, Remove), switching branches
// (SwitchBranch) and catching up with the base (Sync). None of it forces anything except Remove on the caller's
// explicit word.

// Errors of Sync and SwitchBranch. The tools report them as they are; the model acts on the text.
var (
	ErrUncommitted     = errors.New("the worktree has uncommitted changes")
	ErrMergeInProgress = errors.New("a merge is already in progress")
)

// Dirty lists the paths with uncommitted changes in dir, untracked files included (ignored ones are not).
func Dirty(ctx context.Context, dir string) ([]string, error) {
	return porcelain(ctx, dir, "status", "--porcelain")
}

func porcelain(ctx context.Context, dir string, args ...string) ([]string, error) {
	out, err := git(ctx, QueryTimeout, dir, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, l := range strings.Split(out, "\n") {
		if len(l) < 4 {
			continue
		}
		p := l[3:]
		if _, to, ok := strings.Cut(p, " -> "); ok {
			p = to
		}
		paths = append(paths, strings.Trim(p, `"`))
	}
	return paths, nil
}

// CommitsAhead counts the commits of dir's HEAD that base does not have.
func CommitsAhead(ctx context.Context, dir, base string) (int, error) {
	if err := verifyCommit(ctx, dir, base); err != nil {
		return 0, err
	}
	return countRange(ctx, dir, base+"..HEAD")
}

func countRange(ctx context.Context, dir, rng string) (int, error) {
	out, err := git(ctx, QueryTimeout, dir, "rev-list", "--count", rng)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// Removal says which worktree Remove takes away.
type Removal struct {
	RepositoryPath, Path string
	// Force discards uncommitted changes; without it git refuses a worktree that has any.
	Force bool
	// Branch, when set, is deleted after the worktree: the caller checked it has no commits of its own.
	Branch string
}

// Remove removes a worktree and optionally its branch. An error after the worktree is gone concerns the branch only:
// the branch stays.
func Remove(ctx context.Context, r Removal) error {
	args := []string{"worktree", "remove"}
	if r.Force {
		args = append(args, "--force")
	}
	if _, err := git(ctx, AddTimeout, r.RepositoryPath, append(args, r.Path)...); err != nil {
		return err
	}
	if r.Branch != "" {
		if _, err := git(ctx, QueryTimeout, r.RepositoryPath, "branch", "-D", "--", r.Branch); err != nil {
			return fmt.Errorf("the worktree is gone but its branch %s stays: %w", r.Branch, err)
		}
	}
	return nil
}

// CurrentBranch returns the branch checked out in dir ("" when detached).
func CurrentBranch(ctx context.Context, dir string) string {
	out, err := git(ctx, QueryTimeout, dir, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// CheckBranchName reports whether name can be a branch of the repository containing dir.
func CheckBranchName(ctx context.Context, dir, name string) error {
	return checkBranchName(ctx, dir, name)
}

// BranchExists reports whether the local branch exists.
func BranchExists(ctx context.Context, dir, name string) bool { return branchExists(ctx, dir, name) }

// SwitchBranch checks out branch in dir, creating it from HEAD when it does not exist; created says which. Git refuses
// when the switch would overwrite uncommitted changes, and a branch checked out elsewhere is refused here.
func SwitchBranch(ctx context.Context, dir, branch string) (created bool, err error) {
	if err := checkBranchName(ctx, dir, branch); err != nil {
		return false, err
	}
	if !branchExists(ctx, dir, branch) {
		_, err := git(ctx, QueryTimeout, dir, "switch", "-c", branch)
		return err == nil, err
	}
	checkedOut, err := checkedOutBranches(ctx, dir)
	if err != nil {
		return false, err
	}
	if where, ok := checkedOut[branch]; ok {
		return false, fmt.Errorf("%w: %s is checked out at %s", ErrBranchCheckedOut, branch, where)
	}
	_, err = git(ctx, AddTimeout, dir, "switch", branch)
	return false, err
}

// Outcomes of Sync.
const (
	SyncUpToDate  = "up_to_date"
	SyncMerged    = "merged"
	SyncConflicts = "conflicts"
)

// SyncResult is what Sync did.
type SyncResult struct {
	Status string
	// Target is what was merged (or would have been): the base ref, or its upstream when that is ahead of it.
	Target string
	// Commits counts the commits that came in.
	Commits int
	// Conflicts are the files git could not merge; the merge is left in progress for someone to finish.
	Conflicts []string
	// FetchError says the fetch failed, so the merge used what the repository already had.
	FetchError string
}

var commitRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// IsCommit reports whether ref looks like a commit id rather than a name.
func IsCommit(ref string) bool { return commitRe.MatchString(ref) }

// Sync brings the branch checked out in dir up to date with base: it fetches base's remote when it has one, then
// merges. It refuses with uncommitted changes to tracked files or a merge in progress. Conflicts are a result, not an
// error: the merge stays in progress. Nothing is forced and nothing but the checked out branch moves.
func Sync(ctx context.Context, dir, base string) (*SyncResult, error) {
	if _, err := git(ctx, QueryTimeout, dir, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil {
		return nil, ErrMergeInProgress
	}
	dirty, err := porcelain(ctx, dir, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, err
	}
	if len(dirty) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrUncommitted, strings.Join(dirty, ", "))
	}
	res := &SyncResult{Target: base}
	res.Target, res.FetchError = syncTarget(ctx, dir, base)
	if err := verifyCommit(ctx, dir, res.Target); err != nil {
		return nil, err
	}
	if res.Commits, err = countRange(ctx, dir, "HEAD.."+res.Target); err != nil {
		return nil, err
	}
	if res.Commits == 0 {
		res.Status = SyncUpToDate
		return res, nil
	}
	if _, err := git(ctx, AddTimeout, dir, "merge", "--no-edit", res.Target); err != nil {
		res.Conflicts, _ = lines(ctx, dir, "diff", "--name-only", "--diff-filter=U")
		if len(res.Conflicts) == 0 {
			return nil, err
		}
		res.Status = SyncConflicts
		return res, nil
	}
	res.Status = SyncMerged
	return res, nil
}

func lines(ctx context.Context, dir string, args ...string) ([]string, error) {
	out, err := git(ctx, QueryTimeout, dir, args...)
	if err != nil {
		return nil, err
	}
	var ls []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ls = append(ls, strings.Trim(l, `"`))
		}
	}
	return ls, nil
}

// syncTarget fetches base's remote and returns what to merge: a local branch's upstream when the branch has nothing
// of its own beyond it, else base. A failed fetch does not stop the sync; its error comes back.
func syncTarget(ctx context.Context, dir, base string) (target, fetchErr string) {
	if IsCommit(base) && !branchExists(ctx, dir, base) {
		return base, ""
	}
	fetch := func(remote, branch string) {
		if _, err := git(ctx, AddTimeout, dir, "fetch", "--no-tags", remote, "refs/heads/"+branch+":refs/remotes/"+remote+"/"+branch); err != nil {
			fetchErr = err.Error()
		}
	}
	if branchExists(ctx, dir, base) {
		remote := configValue(ctx, dir, "branch."+base+".remote")
		merge := strings.TrimPrefix(configValue(ctx, dir, "branch."+base+".merge"), "refs/heads/")
		if remote == "" || remote == "." || merge == "" {
			return base, ""
		}
		fetch(remote, merge)
		upstream := remote + "/" + merge
		if verifyCommit(ctx, dir, upstream) != nil {
			return base, fetchErr
		}
		if _, err := git(ctx, QueryTimeout, dir, "merge-base", "--is-ancestor", base, upstream); err != nil {
			return base, fetchErr
		}
		return upstream, fetchErr
	}
	if remote, branch, ok := strings.Cut(base, "/"); ok {
		if _, err := git(ctx, QueryTimeout, dir, "show-ref", "--verify", "--quiet", "refs/remotes/"+base); err == nil {
			fetch(remote, branch)
		}
	}
	return base, fetchErr
}

func configValue(ctx context.Context, dir, key string) string {
	out, err := git(ctx, QueryTimeout, dir, "config", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
