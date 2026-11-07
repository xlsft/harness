//go:build !darwin && !linux && !windows

package sandbox

import "runtime"

// NewDriver returns the driver of this platform: none yet.
func NewDriver() Driver { return stubDriver{platform: runtime.GOOS} }
