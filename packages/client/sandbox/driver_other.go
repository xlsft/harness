//go:build !darwin && !linux

package sandbox

import "runtime"

// NewDriver returns the driver of this platform: none yet.
func NewDriver() Driver { return stubDriver{platform: runtime.GOOS} }
