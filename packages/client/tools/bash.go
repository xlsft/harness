package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bash limits (overridable with BASH_DEFAULT_TIMEOUT_MS / BASH_MAX_TIMEOUT_MS).
const (
	BashDefaultTimeout   = 2 * time.Minute
	BashMaxTimeout       = 10 * time.Minute
	BashMaxInlineChars   = 30000
	BashPreviewChars     = 2000
	BashMaxErrorChars    = 10000
	bashMaxCaptureBytes  = 64 << 20
	bashSnapshotDeadline = 10 * time.Second
)

// Shell keeps state that survives between Bash calls: the working directory
// and the login-shell snapshot of aliases/functions/PATH. Environment
// variables do not persist: every call runs in a fresh shell process.
type Shell struct {
	env  *Env
	mu   sync.Mutex
	cwd  string
	path string
	kind string // bash | zsh | sh

	snapOnce sync.Once
	snapPath string
}

// NewShell creates shell state rooted at env.Cwd.
func NewShell(env *Env) *Shell {
	s := &Shell{env: env, cwd: env.Cwd}
	s.path, s.kind = detectShell()
	return s
}

// Cwd returns the current shell directory.
func (s *Shell) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func detectShell() (string, string) {
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("bash"); err == nil {
			return p, "bash"
		}
		return "sh", "sh"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		switch filepath.Base(sh) {
		case "bash", "zsh":
			if _, err := os.Stat(sh); err == nil {
				return sh, filepath.Base(sh)
			}
		}
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p, "bash"
	}
	return "/bin/sh", "sh"
}

// snapshotScript dumps aliases, non-underscore functions and PATH.
func snapshotScript(kind, out string) string {
	q := shellQuote(out)
	switch kind {
	case "zsh":
		return `{ for __f in ${(k)functions}; do [[ $__f == _* ]] || typeset -f -- $__f; done; alias -L; print -r -- "setopt aliases"; print -r -- "export PATH=${(q)PATH}"; } >| ` + q + ` 2>/dev/null`
	case "bash":
		return `{ declare -F | awk '{print $3}' | grep -v '^_' | while read -r __f; do declare -f "$__f"; done; alias -p; echo "shopt -s expand_aliases"; printf 'export PATH=%q\n' "$PATH"; } > ` + q + ` 2>/dev/null`
	}
	return ""
}

func (s *Shell) snapshot() string {
	s.snapOnce.Do(func() {
		if s.kind == "sh" || s.env.ScratchDir == "" || os.Getenv("HARNESS_NO_SHELL_SNAPSHOT") == "1" {
			return
		}
		if err := os.MkdirAll(s.env.ScratchDir, 0o700); err != nil {
			return
		}
		path := filepath.Join(s.env.ScratchDir, "shell-snapshot."+s.kind)
		ctx, cancel := context.WithTimeout(context.Background(), bashSnapshotDeadline)
		defer cancel()
		cmd := exec.CommandContext(ctx, s.path, "-l", "-i", "-c", snapshotScript(s.kind, path))
		cmd.Dir = s.env.Cwd
		setNewSession(cmd)
		cmd.WaitDelay = time.Second
		if err := cmd.Run(); err != nil {
			if _, statErr := os.Stat(path); statErr != nil {
				return
			}
		}
		s.snapPath = path
	})
	return s.snapPath
}

// ExecResult is the raw result of one shell command.
type ExecResult struct {
	Stdout, Stderr string
	ExitCode       int
	TimedOut       bool
	Interrupted    bool
	Truncated      bool
	CwdReset       string
}

type capBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	cut bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := bashMaxCaptureBytes - c.buf.Len(); room < len(p) {
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.cut = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

// Exec runs command in the persistent cwd.
func (s *Shell) Exec(ctx context.Context, command string, timeout time.Duration) (ExecResult, error) {
	s.mu.Lock()
	cwd := s.cwd
	s.mu.Unlock()
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		cwd = s.env.Cwd
	}
	snap := s.snapshot()

	cwdFile := ""
	if s.env.ScratchDir != "" {
		_ = os.MkdirAll(s.env.ScratchDir, 0o700)
		f, err := os.CreateTemp(s.env.ScratchDir, "cwd-*")
		if err == nil {
			cwdFile = f.Name()
			f.Close()
			defer os.Remove(cwdFile)
		}
	}

	var script strings.Builder
	if snap != "" {
		script.WriteString(". " + shellQuote(snap) + " >/dev/null 2>&1\n")
	}
	if s.kind == "bash" {
		script.WriteString("shopt -s expand_aliases 2>/dev/null\n")
	}
	script.WriteString(`__harness_cmd=$__HARNESS_CMD; __harness_cwd=$__HARNESS_CWD_FILE; unset __HARNESS_CMD __HARNESS_CWD_FILE` + "\n")
	script.WriteString(`if [ -n "$__harness_cwd" ]; then trap 'pwd -P > "$__harness_cwd" 2>/dev/null' EXIT; fi` + "\n")
	script.WriteString(`eval "$__harness_cmd"` + "\n")

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, s.path, "-c", script.String())
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"__HARNESS_CMD="+command,
		"__HARNESS_CWD_FILE="+cwdFile,
		"HARNESS=1",
		"GIT_EDITOR=true",
	)
	var stdout, stderr capBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	res := ExecResult{Stdout: stdout.buf.String(), Stderr: stderr.buf.String(), Truncated: stdout.cut || stderr.cut}
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		res.Interrupted = true
		res.ExitCode = -1
	case errors.Is(tctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.ExitCode = -1
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case err != nil && !errors.Is(err, exec.ErrWaitDelay):
		return res, err
	}

	if cwdFile != "" {
		if data, err := os.ReadFile(cwdFile); err == nil {
			if nd := strings.TrimSpace(string(data)); nd != "" {
				if within(nd, s.env.WorkingDirs()) {
					s.mu.Lock()
					s.cwd = nd
					s.mu.Unlock()
				} else {
					s.mu.Lock()
					s.cwd = s.env.Cwd
					s.mu.Unlock()
					res.CwdReset = s.env.Cwd
				}
			}
		}
	}
	return res, nil
}

// shellQuote single-quotes s for POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// Bash runs shell commands.
type Bash struct{}

type bashInput struct {
	Command         string   `json:"command"`
	Description     string   `json:"description"`
	Timeout         *float64 `json:"timeout"`
	RunInBackground bool     `json:"run_in_background"`
}

func (*Bash) Name() string                  { return "Bash" }
func (*Bash) ReadOnly(json.RawMessage) bool { return false }

// BashCommand extracts the command from a Bash input (used by permissions).
func BashCommand(input json.RawMessage) string {
	var in bashInput
	_ = json.Unmarshal(input, &in)
	return in.Command
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return def
}

func (*Bash) Run(ctx context.Context, env *Env, call Call) Result {
	var in bashInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return ErrorResult("InputValidationError: command is required")
	}
	if in.RunInBackground {
		// TODO(M1): background tasks with output files and TaskStop.
		return ErrorResult("run_in_background is not supported by this client yet. Run the command in the foreground.")
	}
	maxT := envDuration("BASH_MAX_TIMEOUT_MS", BashMaxTimeout)
	timeout := envDuration("BASH_DEFAULT_TIMEOUT_MS", BashDefaultTimeout)
	if in.Timeout != nil && *in.Timeout > 0 {
		timeout = time.Duration(*in.Timeout) * time.Millisecond
	}
	if timeout > maxT {
		timeout = maxT
	}
	res, err := env.Shell.Exec(ctx, in.Command, timeout)
	if err != nil {
		return ErrorResult("failed to run command: %v", err)
	}
	structured := map[string]any{
		"stdout": res.Stdout, "stderr": res.Stderr, "interrupted": res.Interrupted,
		"exitCode": res.ExitCode,
	}

	out := strings.TrimRight(res.Stdout, "\n")
	if e := strings.TrimRight(res.Stderr, "\n"); e != "" {
		if out != "" {
			out += "\n"
		}
		out += e
	}
	isErr := false
	var notes []string
	switch {
	case res.Interrupted:
		isErr = true
		notes = append(notes, "[Command interrupted]")
	case res.TimedOut:
		isErr = true
		notes = append(notes, fmt.Sprintf("Command timed out after %s", timeout))
	case res.ExitCode != 0:
		if !(res.ExitCode == 1 && exitOneIsOK(in.Command)) {
			isErr = true
			notes = append(notes, fmt.Sprintf("Exit code %d", res.ExitCode))
		}
	}
	if res.Truncated {
		notes = append(notes, "[output exceeded capture limit and was cut]")
	}
	if res.CwdReset != "" {
		notes = append(notes, "Shell cwd was reset to "+res.CwdReset)
	}

	if isErr {
		out = headTail(out, BashMaxErrorChars)
	} else if len(out) > BashMaxInlineChars {
		saved, err := saveOutput(env, call.ID, out)
		if err == nil {
			structured["outputFile"] = saved
			out = fmt.Sprintf("Output too large (%.1fKB). Full output saved to: %s\n\nPreview (first 2KB):\n%s\n…", float64(len(out))/1024, saved, truncateUTF8(out, BashPreviewChars))
		} else {
			out = headTail(out, BashMaxInlineChars)
		}
	}
	if len(notes) > 0 {
		if out != "" {
			out += "\n"
		}
		out += strings.Join(notes, "\n")
	}
	if out == "" {
		out = "(no output)"
	}
	r := TextResult(out, structured)
	r.IsError = isErr
	return r
}

func saveOutput(env *Env, id, out string) (string, error) {
	if env.ScratchDir == "" {
		return "", errors.New("no scratch dir")
	}
	dir := filepath.Join(env.ScratchDir, "tool-results")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if id == "" {
		id = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	p := filepath.Join(dir, safeName(id)+".txt")
	return p, os.WriteFile(p, []byte(out), 0o600)
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '.' {
			return '_'
		}
		return r
	}, s)
}

// headTail keeps the first and last half of s when it exceeds n chars.
func headTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	half := n / 2
	head := truncateUTF8(s, half)
	tailStart := len(s) - half
	for tailStart < len(s) && (s[tailStart]&0xC0) == 0x80 {
		tailStart++
	}
	return fmt.Sprintf("%s\n\n… [%d characters truncated] …\n\n%s", head, len(s)-len(head)-(len(s)-tailStart), s[tailStart:])
}

// exitOneIsOK reports whether exit code 1 means "no match / differences"
// rather than failure for the last command of the pipeline.
func exitOneIsOK(command string) bool {
	seg := command
	for _, sep := range []string{"&&", "||", ";", "|", "\n"} {
		if i := strings.LastIndex(seg, sep); i >= 0 {
			seg = seg[i+len(sep):]
		}
	}
	fields := strings.Fields(seg)
	for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "=") {
		fields = fields[1:] // env assignments
	}
	if len(fields) == 0 {
		return false
	}
	name := filepath.Base(fields[0])
	switch name {
	case "grep", "rg", "egrep", "fgrep", "find", "diff", "test", "[":
		return true
	case "git":
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") {
				continue
			}
			return f == "diff" || f == "grep"
		}
	}
	return false
}
