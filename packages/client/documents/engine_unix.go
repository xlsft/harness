//go:build !windows

package documents

import (
	"os/exec"
	"syscall"
)

// setEngineProcess makes the engine a process group, so a timeout also ends the helpers it starts.
func setEngineProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func fileLinkCount(st interface{ Sys() any }) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Nlink)
	}
	return 1
}
