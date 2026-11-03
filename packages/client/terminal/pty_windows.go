//go:build windows

package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/UserExistsError/conpty"
)

// DefaultShell is PowerShell 7 (pwsh) when installed, else Windows PowerShell, else %COMSPEC%.
func DefaultShell() (shell string, args []string) {
	for _, s := range []string{"pwsh.exe", "powershell.exe"} {
		if p, err := exec.LookPath(s); err == nil {
			return p, []string{"-NoLogo"}
		}
	}
	if c := os.Getenv("COMSPEC"); c != "" {
		return c, nil
	}
	return `C:\Windows\System32\cmd.exe`, nil
}

// conProc runs the shell in a ConPTY pseudo console (Windows 10 1809 and later).
type conProc struct {
	c *conpty.ConPty
}

func startProc(s startSpec) (proc, error) {
	if !conpty.IsConPtyAvailable() {
		return nil, fmt.Errorf("%w: ConPTY needs Windows 10 1809 or later", ErrUnsupported)
	}
	line := syscall.EscapeArg(s.Shell)
	for _, a := range s.Args {
		line += " " + syscall.EscapeArg(a)
	}
	c, err := conpty.Start(line, conpty.ConPtyDimensions(int(s.Cols), int(s.Rows)), conpty.ConPtyWorkDir(s.Dir), conpty.ConPtyEnv(s.Env))
	if err != nil {
		return nil, err
	}
	return &conProc{c: c}, nil
}

func (p *conProc) Read(b []byte) (int, error)  { return p.c.Read(b) }
func (p *conProc) Write(b []byte) (int, error) { return p.c.Write(b) }
func (p *conProc) Pid() int                    { return p.c.Pid() }
func (p *conProc) Close() error                { return p.c.Close() }

func (p *conProc) Resize(cols, rows uint16) error { return p.c.Resize(int(cols), int(rows)) }

func (p *conProc) Wait() (int, error) {
	code, err := p.c.Wait(context.Background())
	if err != nil {
		return -1, err
	}
	return int(code), nil
}

// Windows has no hangup signal: both end the process (closing the pseudo console ends what it still runs).
func (p *conProc) Hangup() error { return p.Kill() }

func (p *conProc) Kill() error {
	pr, err := os.FindProcess(p.c.Pid())
	if err != nil {
		return err
	}
	if err := pr.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
