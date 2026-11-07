package worktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// IncludeFile, at the root of a repository, names the git-ignored files a new worktree gets a copy of, in gitignore
// syntax: secrets and local config a checkout doesn't bring along.
const IncludeFile = ".worktreeinclude"

// Limits of what one worktree gets copied, so that a pattern that catches node_modules stays a small mistake.
const (
	maxIncludeFiles = 2000
	maxIncludeBytes = 512 << 20
)

// CopyIncluded copies the files of src that IncludeFile names and git ignores into the same paths under dst. A tracked
// file comes with the checkout anyway; a file git doesn't ignore isn't copied either, as the worktree would then show it
// as a change. It never follows or copies a symbolic link, and never replaces a file that exists. It returns how many
// files it copied; an error says what it didn't copy, and what was copied stays.
func CopyIncluded(ctx context.Context, src, dst string) (int, error) {
	list := filepath.Join(src, IncludeFile)
	if st, err := os.Lstat(list); err != nil || !st.Mode().IsRegular() {
		return 0, nil
	}
	named, err := git(ctx, QueryTimeout, src, "ls-files", "--others", "--ignored", "-z", "--exclude-from="+list)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", IncludeFile, err)
	}
	ignored, err := git(ctx, QueryTimeout, src, "ls-files", "--others", "--ignored", "-z", "--exclude-standard")
	if err != nil {
		return 0, fmt.Errorf("list ignored files: %w", err)
	}
	isIgnored := map[string]bool{}
	for _, p := range strings.Split(ignored, "\x00") {
		isIgnored[p] = true
	}
	var (
		count int
		total int64
		errs  []error
	)
	for _, rel := range strings.Split(named, "\x00") {
		if rel == "" || !isIgnored[rel] || underWorktrees(rel) {
			continue
		}
		if count >= maxIncludeFiles {
			errs = append(errs, fmt.Errorf("stopped after %d files", maxIncludeFiles))
			break
		}
		from, to := filepath.Join(src, filepath.FromSlash(rel)), filepath.Join(dst, filepath.FromSlash(rel))
		st, err := os.Lstat(from)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if total+st.Size() > maxIncludeBytes {
			errs = append(errs, fmt.Errorf("stopped at %s: more than %d MB", rel, maxIncludeBytes>>20))
			break
		}
		if err := copyNew(from, to, st.Mode().Perm()); err != nil {
			if !errors.Is(err, os.ErrExist) {
				errs = append(errs, fmt.Errorf("%s: %w", rel, err))
			}
			continue
		}
		count++
		total += st.Size()
	}
	return count, errors.Join(errs...)
}

// underWorktrees: the worktrees themselves are ignored files of the main checkout.
func underWorktrees(rel string) bool { return strings.HasPrefix(rel, Dir+"/") }

func copyNew(from, to string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	if err := errors.Join(cerr, out.Close()); err != nil {
		_ = os.Remove(to)
		return err
	}
	return nil
}
