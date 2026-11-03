//go:build windows

package acp

import (
	"os"
	"os/exec"
)

func setProcessGroup(cmd *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func signalName(state *os.ProcessState) string { return "" }
