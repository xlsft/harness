//go:build !windows

package session

import (
	"os"
	"syscall"
)

// linkCount returns the number of hard links of a file (1 when unknown).
func linkCount(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
