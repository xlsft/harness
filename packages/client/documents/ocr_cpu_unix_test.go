//go:build !windows

package documents

import (
	"syscall"
	"time"
)

// processCPU is the user and system time the test process has used: on a busy machine it measures OCR better than
// the wall clock does.
func processCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
