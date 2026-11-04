//go:build !windows

package keepawake

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
)

// Inhibit runs the system's inhibitor as a child process in a process group of its own, tied to this process so that
// it ends with the client: `caffeinate` on macOS, `systemd-inhibit` on Linux.
func Inhibit() (func(), error) {
	pid := strconv.Itoa(os.Getpid())
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		argv = []string{"caffeinate", "-i", "-w", pid}
	case "linux":
		argv = []string{"systemd-inhibit", "--what=idle:sleep", "--who=harness", "--why=A session is working", "tail", "--pid=" + pid, "-f", "/dev/null"}
	default:
		return nil, fmt.Errorf("keeping the computer awake is not supported on %s", runtime.GOOS)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("cannot keep the computer awake: %s is not installed", argv[0])
		}
		return nil, fmt.Errorf("cannot keep the computer awake: %w", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		<-done
	}, nil
}
