//go:build windows

package keepawake

import (
	"fmt"
	"runtime"
	"syscall"
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

// Inhibit asks Windows to stay up. The request belongs to the calling thread, so one thread is kept for it until the
// release (or the end of the process).
func Inhibit() (func(), error) {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")
	stop, stopped, started := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(stopped)
		if r, _, err := proc.Call(uintptr(esContinuous | esSystemRequired)); r == 0 {
			started <- fmt.Errorf("cannot keep the computer awake: %w", err)
			return
		}
		started <- nil
		<-stop
		_, _, _ = proc.Call(uintptr(esContinuous))
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return func() {
		close(stop)
		<-stopped
	}, nil
}
