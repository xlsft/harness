//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes the command the leader of a new process group so the
// whole tree can be killed on timeout or interrupt.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Negative pid targets the process group.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// setNewSession detaches the command from the controlling terminal so an
// interactive shell (used for the snapshot) cannot block on the tty.
func setNewSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
}
