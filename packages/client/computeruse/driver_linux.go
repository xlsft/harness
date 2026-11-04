//go:build linux

package computeruse

// NewDriver returns the driver of this platform: none yet on Linux. A real one would use AT-SPI over D-Bus for the
// accessibility tree and focused element, and split by session type: on X11, XTEST for input and XGetImage or XComposite
// for one window's image; on Wayland, the RemoteDesktop and ScreenCast portals, whose per-session prompt plays the part
// of macOS's permissions.
func NewDriver() Driver { return stubDriver{platform: "linux"} }
