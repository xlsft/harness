package worktree

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Branch is a local branch.
type Branch struct {
	Name string
	// CheckedOut reports that some worktree (the main checkout included) has
	// the branch checked out.
	CheckedOut bool
	// CommittedAt is the committer date of the branch tip.
	CommittedAt time.Time
}

// BranchList is the result of Branches.
type BranchList struct {
	// Repository is the root of the work tree containing the directory.
	Repository string
	// Current is the branch checked out there ("" when detached).
	Current  string
	Branches []Branch
}

// Branches lists the local branches of the repository containing dir, most
// recently committed first. Outside a repository it returns ErrNotRepository.
func Branches(ctx context.Context, dir string) (BranchList, error) {
	repo, err := Find(ctx, dir)
	if err != nil {
		return BranchList{}, err
	}
	out := BranchList{Repository: repo.Toplevel}
	if cur, err := git(ctx, QueryTimeout, repo.Toplevel, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		out.Current = strings.TrimSpace(cur)
	}
	checked, err := checkedOutBranches(ctx, repo.Toplevel)
	if err != nil {
		return out, err
	}
	refs, err := git(ctx, QueryTimeout, repo.Toplevel, "for-each-ref", "--format=%(refname:short)%00%(committerdate:unix)", "refs/heads")
	if err != nil {
		return out, err
	}
	for _, l := range strings.Split(refs, "\n") {
		name, ts, ok := strings.Cut(l, "\x00")
		if !ok || name == "" {
			continue
		}
		b := Branch{Name: name}
		_, b.CheckedOut = checked[name]
		if sec, err := parseUnix(ts); err == nil {
			b.CommittedAt = sec
		}
		out.Branches = append(out.Branches, b)
	}
	sort.SliceStable(out.Branches, func(i, j int) bool {
		a, b := out.Branches[i], out.Branches[j]
		if !a.CommittedAt.Equal(b.CommittedAt) {
			return a.CommittedAt.After(b.CommittedAt)
		}
		return a.Name < b.Name
	})
	return out, nil
}

func parseUnix(s string) (time.Time, error) {
	sec, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, 0).UTC(), nil
}
