//go:build !windows

package worktree

import (
	"os/exec"
	"syscall"
)

// killGroup kills a step and what it started: the whole process group when the command leads one.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if a := cmd.SysProcAttr; a != nil && (a.Setpgid || a.Setsid) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
