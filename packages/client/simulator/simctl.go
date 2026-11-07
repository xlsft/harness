// Package simulator drives the iOS Simulators of this Mac: the Simulator pane and the agent's Simulator* tools. Devices,
// apps and screenshots go through `xcrun simctl`; touches, keys and hardware buttons through the computer use helper,
// which talks to SimulatorKit's HID client the way Simulator.app does (docs/design/demo-recording.md). Nothing here is
// synced: frames and screenshots stay on this machine.
package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Runner runs `xcrun simctl`. The default runs the real one; tests give a fake.
type Runner interface {
	// Simctl runs `xcrun simctl args...` with stdin (may be nil) and returns its standard output.
	Simctl(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
	// DeveloperDir is the active Xcode's Developer folder ("" without Xcode).
	DeveloperDir(ctx context.Context) string
}

// execRunner runs the real tools.
type execRunner struct {
	once sync.Once
	dir  string
}

func (r *execRunner) DeveloperDir(ctx context.Context) string {
	r.once.Do(func() {
		if runtime.GOOS != "darwin" {
			return
		}
		out, err := exec.CommandContext(ctx, "/usr/bin/xcode-select", "-p").Output()
		if err != nil {
			return
		}
		dir := strings.TrimSpace(string(out))
		// The command line tools alone have no simctl.
		if strings.Contains(dir, "CommandLineTools") {
			return
		}
		if err := exec.CommandContext(ctx, "/usr/bin/xcrun", "--find", "simctl").Run(); err != nil {
			return
		}
		r.dir = dir
	})
	return r.dir
}

func (r *execRunner) Simctl(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/xcrun", append([]string{"simctl"}, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, &SimctlError{Args: args, Message: msg}
	}
	return out, nil
}

// SimctlError is a failed simctl command, with what it printed.
type SimctlError struct {
	Args    []string
	Message string
}

func (e *SimctlError) Error() string {
	sub := ""
	if len(e.Args) > 0 {
		sub = " " + e.Args[0]
	}
	return "simctl" + sub + ": " + e.Message
}

// Device is one simulator.
type Device struct {
	UDID string
	Name string
	// State is "Booted", "Shutdown", "Booting"...
	State string
	// Runtime is the runtime's id; RuntimeName how people say it ("iOS 26.0").
	Runtime, RuntimeName string
	DeviceType           string
	Available            bool
}

// Booted reports whether the device runs.
func (d Device) Booted() bool { return d.State == "Booted" }

// ErrNoXcode: simctl is not there.
var ErrNoXcode = errors.New("the iOS Simulator needs Xcode on this Mac (xcode-select -p must point at Xcode, not the command line tools)")

var runtimeRe = regexp.MustCompile(`SimRuntime\.([A-Za-z]+)-([0-9-]+)$`)

// runtimeName turns com.apple.CoreSimulator.SimRuntime.iOS-26-0 into "iOS 26.0".
func runtimeName(id string) string {
	m := runtimeRe.FindStringSubmatch(id)
	if m == nil {
		return id
	}
	return m[1] + " " + strings.ReplaceAll(m[2], "-", ".")
}

// parseDevices reads `simctl list -j devices`. Booted devices come first, then by runtime (newest first) and name.
func parseDevices(data []byte) ([]Device, error) {
	var raw struct {
		Devices map[string][]struct {
			UDID        string `json:"udid"`
			Name        string `json:"name"`
			State       string `json:"state"`
			IsAvailable bool   `json:"isAvailable"`
			DeviceType  string `json:"deviceTypeIdentifier"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("simctl list: %w", err)
	}
	var out []Device
	for rt, list := range raw.Devices {
		for _, d := range list {
			out = append(out, Device{UDID: d.UDID, Name: d.Name, State: d.State, Runtime: rt, RuntimeName: runtimeName(rt), DeviceType: d.DeviceType, Available: d.IsAvailable})
		}
	}
	slices.SortFunc(out, func(a, b Device) int {
		if a.Booted() != b.Booted() {
			if a.Booted() {
				return -1
			}
			return 1
		}
		if c := strings.Compare(b.Runtime, a.Runtime); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.UDID, b.UDID)
	})
	return out, nil
}

// parseLaunch reads the pid from `simctl launch` ("com.example.app: 4242").
func parseLaunch(out []byte) int {
	s := strings.TrimSpace(string(out))
	if i := strings.LastIndex(s, ":"); i >= 0 {
		n, _ := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		return n
	}
	return 0
}
