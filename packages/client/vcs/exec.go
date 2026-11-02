package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds one CLI invocation.
const DefaultTimeout = 30 * time.Second

// ErrNotInstalled is returned when the CLI binary is not on PATH.
var ErrNotInstalled = errors.New("not installed")

// ErrTimeout is returned when a CLI invocation exceeds its timeout.
var ErrTimeout = errors.New("timed out")

// nonInteractiveEnv disables prompts, pagers, colors, spinners and update checks of gh and glab.
var nonInteractiveEnv = []string{
	"GH_PROMPT_DISABLED=1",
	"GH_NO_UPDATE_NOTIFIER=1",
	"GH_NO_EXTENSION_UPDATE_NOTIFIER=1",
	"GH_SPINNER_DISABLED=1",
	"GH_PAGER=cat",
	"GLAB_PAGER=cat",
	"PAGER=cat",
	"NO_PROMPT=1",
	"GLAB_NO_PROMPT=1",
	"GLAB_CHECK_UPDATE=false",
	"GLAB_SEND_TELEMETRY=false",
	"NO_COLOR=1",
	"CLICOLOR=0",
	"CLICOLOR_FORCE=0",
	"TERM=dumb",
	"LANG=C",
	"LC_ALL=C",
}

// Runner runs a CLI without a shell, with a timeout, empty stdin and a non-interactive environment.
type Runner struct {
	// Timeout per invocation; default DefaultTimeout.
	Timeout time.Duration
	// Env is the base environment; default os.Environ(). The non-interactive variables are appended
	// and win over it.
	Env []string
	// LookPath resolves a binary; default exec.LookPath (honors PATH in Env when set).
	LookPath func(name string) (string, error)
}

// Result is the outcome of one invocation that started.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Run executes name with args. It returns ErrNotInstalled when the binary is missing and ErrTimeout
// when it ran out of time; a non-zero exit is not an error (see Result.ExitCode).
func (r *Runner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	path, err := r.lookPath(name)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", name, ErrNotInstalled)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(append([]string{}, r.env()...), nonInteractiveEnv...)
	cmd.Stdin = nil // /dev/null: nothing can be answered interactively
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitWriter{w: &stdout, n: 8 << 20}, &limitWriter{w: &stderr, n: 1 << 20}
	// Children that keep the pipes open (a wrapper script's subprocess) must not hang Wait.
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("%s %s: %w after %s", name, strings.Join(args, " "), ErrTimeout, timeout)
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			return res, nil
		}
		return res, fmt.Errorf("%s: %w", name, err)
	}
	return res, nil
}

func (r *Runner) env() []string {
	if r.Env != nil {
		return r.Env
	}
	return os.Environ()
}

func (r *Runner) lookPath(name string) (string, error) {
	if r.LookPath != nil {
		return r.LookPath(name)
	}
	if r.Env != nil {
		for _, kv := range r.Env {
			if p, ok := strings.CutPrefix(kv, "PATH="); ok {
				return lookPathIn(name, p)
			}
		}
	}
	return exec.LookPath(name)
}

// lookPathIn resolves name against an explicit PATH value.
func lookPathIn(name, path string) (string, error) {
	for _, dir := range strings.Split(path, string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		p := dir + string(os.PathSeparator) + name
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

// limitWriter keeps at most n bytes and discards the rest.
type limitWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		if len(p) <= room {
			l.w.Write(p)
		} else {
			l.w.Write(p[:room])
		}
	}
	return len(p), nil
}

// firstLine returns the first non-empty trimmed line of b.
func firstLine(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// cliError formats a failed invocation for logs, keeping stderr short.
func cliError(name string, res Result) error {
	msg := firstLine(res.Stderr)
	if msg == "" {
		msg = firstLine(res.Stdout)
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return fmt.Errorf("%s exited with %d: %s", name, res.ExitCode, msg)
}
