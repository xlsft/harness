//go:build windows

package preview

import (
	"os"
	"os/exec"
)

// Windows has no process groups here: only the direct child is stopped (as tools.killProcessGroup does).

func termGroup(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

func killGroup(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

func groupAlive(*exec.Cmd) bool { return false }

// No exit guard: a client that dies without its cleanup leaves the server running.
const exitGuard = ""

func attachExitGuard(*exec.Cmd) (*os.File, error) { return nil, nil }
