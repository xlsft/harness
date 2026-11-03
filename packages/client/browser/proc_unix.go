//go:build !windows

package browser

import (
	"os"
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the browser and its helper processes.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// usePipe: the DevTools pipe (fds 3 and 4) is available on Unix.
const usePipe = true

func runningAsRoot() bool { return os.Geteuid() == 0 }
