//go:build linux

package sandbox

// NewDriver returns the driver of this platform: none yet on Linux. bubblewrap covers the files (a read-only root with the
// writable roots bound over it), but a private network namespace has no way out to the proxy on the host's loopback without
// a bridge process inside it (socat over a unix socket), and the deny lists have to be tmpfs mounts over paths that exist.
// That is a driver with its own tests, not a small wrapper.
func NewDriver() Driver { return stubDriver{platform: "linux"} }
