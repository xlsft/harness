//go:build !darwin && !linux && !windows

package computeruse

import "runtime"

// NewDriver returns the driver of this platform: none.
func NewDriver() Driver { return stubDriver{platform: runtime.GOOS} }
