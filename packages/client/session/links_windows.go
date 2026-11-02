//go:build windows

package session

import "os"

// linkCount is not tracked on Windows.
func linkCount(os.FileInfo) uint64 { return 1 }
