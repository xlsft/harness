//go:build !linux && !windows

package daemon

import "syscall"

func setDeathSignal(*syscall.SysProcAttr) {}

// Harden has nothing to do here: shared hosts are Linux only.
func Harden() {}

func loadAverage() float64 { return 0 }
