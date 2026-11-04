//go:build !windows

package tools

import "os/exec"

func setDesktopCommandLine(*exec.Cmd, []string) {}
