//go:build windows

package tools

import (
	"os/exec"
	"strings"
	"syscall"
)

// setDesktopCommandLine writes the command line of `explorer /select,<path>` by hand: explorer parses it itself and
// does not accept the argument quoted as a whole, which is what the default quoting does for a path with spaces.
func setDesktopCommandLine(cmd *exec.Cmd, argv []string) {
	if len(argv) == 2 && strings.HasPrefix(argv[1], "/select,") {
		path := strings.TrimPrefix(argv[1], "/select,")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	}
}
