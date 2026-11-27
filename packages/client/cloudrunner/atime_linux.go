//go:build linux

package cloudrunner

import (
	"io/fs"
	"syscall"
	"time"
)

// usedAt is when a file was last read, or written when the file system keeps no access times.
func usedAt(info fs.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if a := time.Unix(st.Atim.Sec, st.Atim.Nsec); a.After(info.ModTime()) {
			return a
		}
	}
	return info.ModTime()
}
