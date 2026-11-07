// Package worktree creates git worktrees for sessions (AgentService
// CreateSession with worktree options) and answers the repository questions
// of the new-session pickers (repository root, local branches).
//
// Worktrees live in <repository>/.harness/worktrees/<name>, on a branch
// harness/<name> by default; .harness/worktrees/ is ignored through
// .git/info/exclude so the user's .gitignore is never touched. A session's worktree stays while it holds work of its
// own; Prune takes away the ones that hold none after the cleanup period.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Timeouts for git invocations.
const (
	// QueryTimeout bounds read-only git commands.
	QueryTimeout = 10 * time.Second
	// AddTimeout bounds `git worktree add` (a full checkout of the tree).
	AddTimeout = 2 * time.Minute
)

// ErrNotRepository is returned for directories outside a git work tree.
var ErrNotRepository = errors.New("not inside a git repository")

// git runs git in dir without a shell, without prompts and with a timeout.
// It returns stdout; on failure the error carries git's stderr.
func git(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git %s: timed out after %s", args[0], timeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), &GitError{Args: args, Stderr: msg, Err: err}
	}
	return stdout.String(), nil
}

// GitError is a failed git command.
type GitError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), firstLine(e.Stderr))
}

func (e *GitError) Unwrap() error { return e.Err }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Repository locates a git work tree.
type Repository struct {
	// Toplevel is the root of the work tree containing the directory (a
	// linked worktree's own root when inside one).
	Toplevel string
	// Main is the repository's main checkout.
	Main string
	// CommonDir is the shared git directory (<Main>/.git).
	CommonDir string
}

// Find returns the repository containing dir, or ErrNotRepository.
// Paths are absolute with symlinks resolved.
func Find(ctx context.Context, dir string) (Repository, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return Repository{}, ErrNotRepository
	}
	out, err := git(ctx, QueryTimeout, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return Repository{}, ErrNotRepository
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] == "" {
		// Bare repositories and .git directories have no work tree.
		return Repository{}, ErrNotRepository
	}
	r := Repository{Toplevel: resolve(lines[0]), CommonDir: resolve(lines[1])}
	if filepath.Base(r.CommonDir) == ".git" {
		r.Main = filepath.Dir(r.CommonDir)
	} else {
		// A separate git dir (--separate-git-dir): the main checkout is the
		// first entry of `git worktree list`.
		r.Main = r.Toplevel
		if list, err := git(ctx, QueryTimeout, dir, "worktree", "list", "--porcelain"); err == nil {
			for _, l := range strings.Split(list, "\n") {
				if p, ok := strings.CutPrefix(l, "worktree "); ok {
					r.Main = resolve(p)
					break
				}
			}
		}
	}
	return r, nil
}

func resolve(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
