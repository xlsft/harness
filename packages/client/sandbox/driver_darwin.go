//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// sandboxExec is deprecated by Apple (its man page has said so for years, and the profile language is private), but it
// works, the system's own daemons use the same mechanism, and nothing replaces it for confining a command line without an
// app bundle and entitlements. If a macOS release removes it, Info says so and the sandbox shows as unavailable.
const sandboxExec = "/usr/bin/sandbox-exec"

type seatbeltDriver struct{}

// NewDriver returns the driver of this platform.
func NewDriver() Driver { return seatbeltDriver{} }

func (seatbeltDriver) Info() Info {
	if _, err := os.Stat(sandboxExec); err != nil {
		return Info{Platform: "macos", Reason: fmt.Sprintf("%s is missing from this version of macOS", sandboxExec)}
	}
	return Info{Platform: "macos", Supported: true}
}

// Command passes the profile on the command line, not in a file: no path a command could rewrite for the next one. The
// program is an absolute path, never looked up on the command's PATH.
func (seatbeltDriver) Command(p Policy, argv []string) ([]string, error) {
	warmXcrun()
	return append([]string{sandboxExec, "-p", SeatbeltProfile(p)}, argv...), nil
}

// refusalRe is a line of the kernel's report: "Sandbox: touch(123) deny(1) file-write-create /tmp/x".
var refusalRe = regexp.MustCompile(`deny\(\d+\) (\S+) ?(.*)$`)

// reportWindow bounds how far back the log is read: time a failed command waits for its explanation.
const (
	reportWindow  = 5 * time.Minute
	reportTimeout = 3 * time.Second
)

// Refusals implements Reporter from the unified log, which any process of the user can read. It takes under a second, so it
// is for a command that failed without saying why, not for every command.
func (seatbeltDriver) Refusals(tag string, since time.Time) []string {
	window := min(time.Since(since)+2*time.Second, reportWindow)
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/log", "show", "--last", fmt.Sprintf("%ds", int(window.Seconds())+1), "--style", "compact",
		"--predicate", `eventMessage CONTAINS "`+tag+`"`).Output()
	if err != nil {
		return nil
	}
	var refused []string
	for _, line := range strings.Split(string(out), "\n") {
		if m := refusalRe.FindStringSubmatch(line); m != nil {
			if r := strings.TrimSpace(m[1] + " " + m[2]); !slices.Contains(refused, r) {
				refused = append(refused, r)
			}
		}
	}
	return refused
}

// xcrunTools are the shims in /usr/bin that sandboxed commands run most.
var xcrunTools = []string{"git", "cc", "c++", "clang", "clang++", "make", "ld", "ar", "strip", "nm", "python3", "swift", "swiftc"}

// warmXcrun fills the cache of the xcrun shims once, outside the sandbox. A shim that misses the cache rewrites it and
// prints an error on every call when it cannot, which a sandboxed one cannot and must not: unconfined commands read that
// cache to choose the program to run. xcrun alone would offer to install the developer tools, so xcode-select asks first.
var warmXcrun = sync.OnceFunc(func() {
	go func() {
		if exec.Command("/usr/bin/xcode-select", "-p").Run() != nil {
			return
		}
		for _, tool := range xcrunTools {
			_ = exec.Command("/usr/bin/xcrun", "--find", tool).Run()
		}
	}()
})
