//go:build linux

package daemon

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func setDeathSignal(attr *syscall.SysProcAttr) { attr.Pdeathsig = syscall.SIGTERM }

const prSetDumpable = 4

// Harden makes this process unreadable to others of its user: no ptrace, no /proc/<pid>/mem. An agent's command runs as the
// same user as its worker, and the worker holds the device token in memory.
func Harden() {
	_, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0)
}

func loadAverage() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.ParseFloat(fields[0], 64)
	return n
}
