//go:build !darwin

package computeruse

// NewHelper returns nil: there is no helper program on this platform.
func NewHelper() Helper { return nil }
