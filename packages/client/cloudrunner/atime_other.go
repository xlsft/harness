//go:build !linux && !darwin

package cloudrunner

import (
	"io/fs"
	"time"
)

// usedAt is when a file was last written: access times are not read on this platform.
func usedAt(info fs.FileInfo) time.Time { return info.ModTime() }
