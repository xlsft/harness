//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTerminal reports whether f is a terminal. A mode check won't do: /dev/null is a character device too.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ttyRequest)
	return err == nil
}
