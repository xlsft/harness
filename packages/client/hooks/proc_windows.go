//go:build windows

package hooks

import (
	"os/exec"
)

// shellCommand returns the command that runs a hook, and what kills it: Git Bash when it is there, as the format's commands are
// written for a POSIX shell, else cmd.
func shellCommand(command string) (*exec.Cmd, func()) {
	var cmd *exec.Cmd
	if _, err := exec.LookPath("bash"); err == nil {
		cmd = exec.Command("bash", "-c", command)
	} else {
		cmd = exec.Command("cmd", "/C", command)
	}
	return cmd, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}
