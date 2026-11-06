//go:build !windows

package lsp

import "syscall"

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
