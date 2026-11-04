//go:build !windows

package preview

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// The command is the leader of its own process group (tools.Shell.Command sets that up), so its pid names the group.

func termGroup(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }

func killGroup(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

// groupAlive reports whether any process of the group is left.
func groupAlive(cmd *exec.Cmd) bool {
	err := syscall.Kill(-cmd.Process.Pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// exitGuard goes in front of the command line: a process of the group waits on a pipe whose write end only the client
// holds, and ends the group when the pipe closes. So a client that dies without its cleanup (a crash, SIGKILL) takes
// its servers along. It only ever signals its own process group.
const exitGuard = "(read x <&3; kill -TERM 0) >/dev/null 2>&1 &\n"

// attachExitGuard gives the command the pipe as fd 3 and returns the end the client keeps open.
func attachExitGuard(cmd *exec.Cmd) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = []*os.File{r}
	return w, nil
}
