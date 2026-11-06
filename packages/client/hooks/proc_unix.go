//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// shellCommand returns the command that runs a hook, and what kills it with everything it started.
func shellCommand(command string) (*exec.Cmd, func()) {
	shell := "bash"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "sh"
	}
	cmd := exec.Command(shell, "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}
