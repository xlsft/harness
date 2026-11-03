//go:build windows

package browser

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// usePipe: Windows cannot pass extra file descriptors, so the DevTools port is used.
const usePipe = false

func runningAsRoot() bool { return false }
