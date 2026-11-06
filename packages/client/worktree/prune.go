package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Pruned is a worktree Prune took away.
type Pruned struct {
	Path, Branch string
}

// Prune removes the client's worktrees of a repository that nobody needs any more: untouched for at least olderThan, with
// no uncommitted change, no untracked file, no commit that only they have, not locked, and not reported by inUse. Other
// people's worktrees (outside Dir) are not looked at. A branch goes with its worktree only if the client named it.
// repoMain is the main checkout. What couldn't be looked at or removed stays and comes back as an error.
func Prune(ctx context.Context, repoMain string, olderThan time.Duration, now time.Time, inUse func(path string) bool) ([]Pruned, error) {
	out, err := git(ctx, QueryTimeout, repoMain, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	root := filepath.Join(resolve(repoMain), filepath.FromSlash(Dir)) + string(filepath.Separator)
	var (
		pruned []Pruned
		errs   []error
	)
	for _, entry := range strings.Split(out, "\n\n") {
		var path, branch string
		locked := false
		for _, l := range strings.Split(entry, "\n") {
			switch {
			case strings.HasPrefix(l, "worktree "):
				path = strings.TrimPrefix(l, "worktree ")
			case strings.HasPrefix(l, "branch refs/heads/"):
				branch = strings.TrimPrefix(l, "branch refs/heads/")
			case l == "locked" || strings.HasPrefix(l, "locked "):
				locked = true
			}
		}
		if path == "" || locked || !strings.HasPrefix(resolve(path), root) || (inUse != nil && inUse(path)) {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue // gone already: `git worktree prune` is for the user to run
		}
		idle, err := idleSince(ctx, path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if now.Sub(idle) < olderThan {
			continue
		}
		dirty, err := Dirty(ctx, path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if len(dirty) > 0 {
			continue
		}
		own, err := ownCommits(ctx, path, branch)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if own {
			continue
		}
		r := Removal{RepositoryPath: repoMain, Path: path}
		if strings.HasPrefix(branch, BranchPrefix) {
			r.Branch = branch
		}
		if err := Remove(ctx, r); err != nil {
			errs = append(errs, err)
			if _, serr := os.Stat(path); serr == nil {
				continue
			}
		}
		pruned = append(pruned, Pruned{Path: path, Branch: branch})
	}
	return pruned, errors.Join(errs...)
}

// idleSince is the latest sign of work in a worktree: its folder, its index and its last commit.
func idleSince(ctx context.Context, path string) (time.Time, error) {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	latest := st.ModTime()
	if out, err := git(ctx, QueryTimeout, path, "rev-parse", "--git-path", "index"); err == nil {
		index := strings.TrimSpace(out)
		if !filepath.IsAbs(index) {
			index = filepath.Join(path, index)
		}
		if st, err := os.Stat(index); err == nil && st.ModTime().After(latest) {
			latest = st.ModTime()
		}
	}
	out, err := git(ctx, QueryTimeout, path, "log", "-1", "--format=%ct")
	if err != nil {
		return time.Time{}, err
	}
	if secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
		if t := time.Unix(secs, 0); t.After(latest) {
			latest = t
		}
	}
	return latest, nil
}

// ownCommits reports whether HEAD has commits that no other branch, remote-tracking branch or tag has.
func ownCommits(ctx context.Context, path, branch string) (bool, error) {
	args := []string{"rev-list", "--max-count=1", "HEAD", "--not"}
	if branch != "" {
		args = append(args, "--exclude="+branch) // relative to --branches: a pattern with refs/heads/ would match nothing
	}
	args = append(args, "--branches", "--remotes", "--tags")
	out, err := git(ctx, QueryTimeout, path, args...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}
