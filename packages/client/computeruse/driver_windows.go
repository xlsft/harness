//go:build windows

package computeruse

// NewDriver returns the driver of this platform: none yet on Windows. A real one would use UI Automation for the
// accessibility tree (UIA's IsPassword for secure fields), SendInput for input, GetForegroundWindow and WindowFromPoint
// for the checks, and Windows.Graphics.Capture for one window's image. Input does not reach elevated windows (UIPI),
// which is a limit to keep.
func NewDriver() Driver { return stubDriver{platform: "windows"} }
