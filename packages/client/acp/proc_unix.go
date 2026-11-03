//go:build !windows

package acp

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup starts cmd in its own process group, so the agent and the processes it spawns are killed
// together.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills cmd's process group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}

// signalName returns the name of the signal that ended a process, or "".
func signalName(state *os.ProcessState) string {
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ws.Signal().String()
	}
	return ""
}
