package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/xlsft/harness/evals/task"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// copyTree copies src into dst (which must exist), preserving file modes.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// prepareWorkdir copies the task snapshot into a fresh directory and commits it
// so the agent starts from a clean git tree and checks can use `git diff`.
func prepareWorkdir(t *task.Task, parent string) (string, error) {
	dir, err := os.MkdirTemp(parent, "harness-eval-"+t.ID+"-")
	if err != nil {
		return "", err
	}
	if err := copyTree(filepath.Join(t.Dir, "repo"), dir); err != nil {
		return dir, err
	}
	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=harness-eval", "-c", "user.email=eval@harness.invalid", "-c", "commit.gpgsign=false",
			"commit", "-q", "-m", "Initial snapshot"},
	}
	for _, s := range steps {
		cmd := exec.Command("git", s...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return dir, fmt.Errorf("git %v: %v: %s", s, err, out)
		}
	}
	return dir, nil
}

type procResult struct {
	stdout, stderr []byte
	exitCode       int
	timedOut       bool
	duration       time.Duration
	err            error // failure to start, not a non-zero exit
}

// runProc runs a command with a hard timeout, killing its whole process group
// (where supported) so stray children such as test runners do not linger.
func runProc(ctx context.Context, timeout time.Duration, dir string, env []string, name string, args ...string) procResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	setProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	start := time.Now()
	err := cmd.Run()
	res := procResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), duration: time.Since(start)}
	if ctx.Err() == context.DeadlineExceeded {
		res.timedOut = true
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.exitCode = exitErr.ExitCode()
	case res.timedOut:
		res.exitCode = -1
	default:
		res.exitCode, res.err = -1, err
	}
	return res
}
