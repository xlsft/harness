package tools

import (
	"context"
	"strings"
	"time"
)

// UserShellDisplayChars bounds each stream of a user shell command's result
// as frontends and transcripts keep it (head and tail kept).
const UserShellDisplayChars = BashMaxInlineChars

// UserShellResult is the outcome of a command the user ran in the composer's
// shell mode.
type UserShellResult struct {
	// Stdout and Stderr are complete up to Bash's capture limit (Truncated).
	Stdout, Stderr string
	// ExitCode is -1 when the command was stopped or timed out.
	ExitCode  int
	Stopped   bool
	TimedOut  bool
	Truncated bool
	// Cwd is the directory it ran in.
	Cwd      string
	Duration time.Duration
	// Err is set when the shell could not be started.
	Err error
}

// UserShellTimeout is the time limit of a user shell command: the Bash
// maximum timeout (BASH_MAX_TIMEOUT_MS), since the user can stop it sooner.
func UserShellTimeout() time.Duration {
	maxT := envDuration("BASH_MAX_TIMEOUT_MS", BashMaxTimeout)
	if defT := envDuration("BASH_DEFAULT_TIMEOUT_MS", BashDefaultTimeout); defT > maxT {
		maxT = defT
	}
	return maxT
}

// RunUser runs a command the user typed in shell mode, the way the Bash tool
// runs the model's commands: in the shell's current directory with the
// session environment and the login-shell snapshot, and a `cd` moves the
// directory for later commands. It never moves to the background: cancelling
// ctx stops it. onOutput (optional, must not block) receives output chunks
// as they arrive.
func (s *Shell) RunUser(ctx context.Context, command string, onOutput func(stream int, p []byte)) UserShellResult {
	start := time.Now()
	if onOutput != nil {
		// Each stream has its own stripper: a sequence split between chunks is joined before it is removed.
		var strippers [2]terminalStripper
		raw := onOutput
		onOutput = func(stream int, p []byte) {
			if stream < 0 || stream > 1 {
				raw(stream, p)
				return
			}
			if b := strippers[stream].strip(p); len(b) > 0 {
				raw(stream, b)
			}
		}
	}
	res, err := s.Run(ctx, command, ExecOptions{Timeout: UserShellTimeout(), OnOutput: onOutput})
	r := UserShellResult{
		Stdout: StripTerminalCodes(res.Stdout), Stderr: StripTerminalCodes(res.Stderr), ExitCode: res.ExitCode, Stopped: res.Interrupted, TimedOut: res.TimedOut,
		Truncated: res.Truncated, Cwd: res.StartCwd, Duration: time.Since(start), Err: err,
	}
	if err != nil {
		r.ExitCode = -1
	}
	return r
}

// CombinedOutput is stdout followed by stderr, as the Bash tool shows them.
func (r UserShellResult) CombinedOutput() string {
	out := strings.TrimRight(r.Stdout, "\n")
	if e := strings.TrimRight(r.Stderr, "\n"); e != "" {
		if out != "" {
			out += "\n"
		}
		out += e
	}
	return out
}

// HeadTail keeps the first and last half of s when it exceeds n bytes, with a
// marker saying how much was cut in between.
func HeadTail(s string, n int) string { return headTail(s, n) }
