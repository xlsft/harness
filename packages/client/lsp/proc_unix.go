//go:build !windows

package lsp

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts cmd in its own process group: servers spawn helpers (tsserver, a type checker), and they stop
// together.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
