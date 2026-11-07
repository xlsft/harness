//go:build windows

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// Windows has no daemon workers: CheckShared refuses before anything gets here.

func workerAttr(int, int) *syscall.SysProcAttr { return &syscall.SysProcAttr{} }

func terminate(p *os.Process) { _ = p.Kill() }

func prepareHome(string, int, int) error {
	return errors.New("a shared host is not available on Windows")
}

func diskFree(string) int64 { return 0 }

// LoadAverage is unknown here.
func loadAverage() float64 { return 0 }

// Harden has nothing to do here.
func Harden() {}
