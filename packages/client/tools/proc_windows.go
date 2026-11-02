//go:build windows

package tools

import "os/exec"

// setProcessGroup on Windows only kills the direct child.
// TODO(M1): use job objects to kill the whole process tree.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func setNewSession(cmd *exec.Cmd) { setProcessGroup(cmd) }
