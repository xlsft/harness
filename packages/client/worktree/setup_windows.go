package worktree

import "os/exec"

func prepareGroup(cmd *exec.Cmd) {}

// killGroup kills a step.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
