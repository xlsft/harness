//go:build !windows

package worktree

import (
	"os/exec"
	"syscall"
)

func prepareGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if !cmd.SysProcAttr.Setsid {
		cmd.SysProcAttr.Setpgid = true
	}
}

// killGroup kills a step and what it started: the whole process group when the command leads one.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if a := cmd.SysProcAttr; a != nil && (a.Setsid || (a.Setpgid && a.Pgid == 0)) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
