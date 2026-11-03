package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// ReminderUserShell is the bundle reminder that tells the model about a
// command the user ran in the composer's shell mode ({{cwd}}, {{command}},
// {{exit_code}}, {{output}}, {{status}}).
const ReminderUserShell = "user_shell_command"

// UserShellOutputChars bounds the output the model sees of one user shell
// command (head and tail kept).
const UserShellOutputChars = 10000

// UserShellNote is a finished user shell command as the model is told about it.
type UserShellNote struct {
	Command  string
	Cwd      string
	ExitCode int
	// Reason is exited, stopped, time_limit or failed.
	Reason string
	// Output is stdout and stderr, already bounded by the caller.
	Output string
	// Error is set when the shell could not run the command.
	Error string
}

// NoteUserShell queues the user_shell_command reminder: it rides on the next
// model request, the next request of a running turn or the first request of
// the next turn. It never starts a turn.
func (a *Agent) NoteUserShell(n UserShellNote) {
	a.Remind(ReminderUserShell, userShellVars(n))
}

func userShellVars(n UserShellNote) map[string]string {
	var status string
	switch n.Reason {
	case "stopped":
		status = "The user stopped it before it finished."
	case "time_limit":
		status = "It was killed when it reached the shell time limit."
	case "failed":
		status = "The shell could not run it: " + n.Error
	default:
		status = fmt.Sprintf("It exited with code %d.", n.ExitCode)
	}
	out := strings.TrimRight(n.Output, "\n")
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	return map[string]string{
		"cwd":       escapeShellTag(strings.ReplaceAll(n.Cwd, `"`, "&quot;")),
		"command":   escapeShellTag(n.Command),
		"exit_code": strconv.Itoa(n.ExitCode),
		"output":    "\n" + escapeShellTag(out) + "\n",
		"status":    status,
	}
}

// escapeShellTag keeps command text and output from closing the
// <user-shell-...> tags or opening a system reminder of their own.
func escapeShellTag(s string) string {
	r := strings.NewReplacer("</user-shell-", "&lt;/user-shell-", "<system-reminder>", "&lt;system-reminder>", "</system-reminder>", "&lt;/system-reminder>")
	return r.Replace(s)
}
