//go:build windows

package sandbox

// NewDriver returns the driver of this platform: none yet. Restricted tokens and job objects could limit what a command
// writes, but that takes a helper and rights an installer has to set up (docs/design/platforms.md).
func NewDriver() Driver {
	return stubDriver{platform: "windows", reason: "Windows has no command sandbox yet, so commands rely on permission prompts"}
}
