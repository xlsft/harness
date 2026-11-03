//go:build unix

package terminal

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// DefaultShell is the user's login shell: $SHELL, or the first of bash, zsh and sh that exists.
func DefaultShell() (shell string, args []string) {
	if s := os.Getenv("SHELL"); s != "" {
		if st, err := os.Stat(s); err == nil && !st.IsDir() {
			return s, []string{"-l"}
		}
	}
	for _, s := range []string{"/bin/bash", "/bin/zsh", "/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s, []string{"-l"}
		}
	}
	return "/bin/sh", nil
}

type unixProc struct {
	f   *os.File
	cmd *exec.Cmd
}

// startProc runs the shell as the leader of a new session with the PTY as its controlling terminal.
func startProc(s startSpec) (proc, error) {
	cmd := exec.Command(s.Shell, s.Args...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: s.Cols, Rows: s.Rows})
	if err != nil {
		return nil, err
	}
	return &unixProc{f: f, cmd: cmd}, nil
}

func (p *unixProc) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixProc) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *unixProc) Pid() int                    { return p.cmd.Process.Pid }
func (p *unixProc) Close() error                { return p.f.Close() }

func (p *unixProc) Resize(cols, rows uint16) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: cols, Rows: rows})
}

func (p *unixProc) Wait() (int, error) {
	err := p.cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// Hangup and Kill signal the shell's whole process group (the shell leads it), so its jobs end too.
func (p *unixProc) Hangup() error { return p.signal(syscall.SIGHUP) }
func (p *unixProc) Kill() error   { return p.signal(syscall.SIGKILL) }

func (p *unixProc) signal(sig syscall.Signal) error {
	pid := p.cmd.Process.Pid
	if err := syscall.Kill(-pid, sig); err != nil {
		return syscall.Kill(pid, sig)
	}
	return nil
}
