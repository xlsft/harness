package simulator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // screenshots are JPEG
	_ "image/png"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/computeruse"
)

// Manager is the iOS Simulator on this client, shared by the pane and the tools.
type Manager struct {
	// Runner and Input override the real simctl and helper (tests).
	Runner Runner
	Input  Input
	// OnActivity receives what the agent does to a device, for the pane (sessionID, udid, what).
	OnActivity func(sessionID, udid, action string)
	Logger     *slog.Logger

	once  sync.Once
	r     Runner
	in    Input
	mu    sync.Mutex
	shots map[string]size // last screenshot of a device in a session: what tool coordinates refer to
	// input serializes input to each device.
	locks map[string]*sync.Mutex
}

type size struct{ w, h int }

func (m *Manager) init() {
	m.once.Do(func() {
		m.r = m.Runner
		if m.r == nil {
			m.r = &execRunner{}
		}
		m.in = m.Input
		if m.in == nil {
			m.in = &helperInput{h: computeruse.NewHelper(), dir: m.r.DeveloperDir}
		}
		m.shots = map[string]size{}
		m.locks = map[string]*sync.Mutex{}
	})
}

// Available reports whether simctl is there (macOS with Xcode). HARNESS_NO_SIMULATOR=1 turns it off.
func (m *Manager) Available(ctx context.Context) bool {
	m.init()
	return os.Getenv("HARNESS_NO_SIMULATOR") != "1" && m.r.DeveloperDir(ctx) != ""
}

func (m *Manager) ready(ctx context.Context) error {
	if !m.Available(ctx) {
		return ErrNoXcode
	}
	return nil
}

// List lists the simulators, booted ones first.
func (m *Manager) List(ctx context.Context) ([]Device, error) {
	if err := m.ready(ctx); err != nil {
		return nil, err
	}
	out, err := m.r.Simctl(ctx, nil, "list", "-j", "devices")
	if err != nil {
		return nil, err
	}
	return parseDevices(out)
}

// Resolve finds a device by UDID or name; "" or "booted" is the one booted device.
func (m *Manager) Resolve(ctx context.Context, ref string) (Device, error) {
	devs, err := m.List(ctx)
	if err != nil {
		return Device{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.EqualFold(ref, "booted") {
		var booted []Device
		for _, d := range devs {
			if d.Booted() {
				booted = append(booted, d)
			}
		}
		switch len(booted) {
		case 0:
			return Device{}, errors.New("no simulator is booted. SimulatorList shows them; SimulatorBoot starts one")
		case 1:
			return booted[0], nil
		}
		return Device{}, fmt.Errorf("%d simulators are booted: name one by its UDID", len(booted))
	}
	for _, d := range devs {
		if strings.EqualFold(d.UDID, ref) {
			return d, nil
		}
	}
	var named []Device
	for _, d := range devs {
		if strings.EqualFold(d.Name, ref) && d.Available {
			named = append(named, d)
		}
	}
	for _, d := range named {
		if d.Booted() {
			return d, nil
		}
	}
	switch len(named) {
	case 0:
		return Device{}, fmt.Errorf("no simulator %q. SimulatorList shows them", ref)
	case 1:
		return named[0], nil
	}
	return Device{}, fmt.Errorf("%d simulators are called %q: name one by its UDID", len(named), ref)
}

func (m *Manager) booted(ctx context.Context, ref string) (Device, error) {
	d, err := m.Resolve(ctx, ref)
	if err != nil {
		return Device{}, err
	}
	if !d.Booted() {
		return Device{}, fmt.Errorf("%s (%s) is %s, not booted. SimulatorBoot starts it", d.Name, d.UDID, strings.ToLower(d.State))
	}
	return d, nil
}

// Boot starts a device and waits until it has finished booting.
func (m *Manager) Boot(ctx context.Context, udid string) error {
	if err := m.ready(ctx); err != nil {
		return err
	}
	if _, err := m.r.Simctl(ctx, nil, "boot", udid); err != nil {
		var se *SimctlError
		if !errors.As(err, &se) || !strings.Contains(se.Message, "current state: Booted") {
			return err
		}
	}
	_, err := m.r.Simctl(ctx, nil, "bootstatus", udid)
	return err
}

// Shutdown stops a device.
func (m *Manager) Shutdown(ctx context.Context, udid string) error {
	if err := m.ready(ctx); err != nil {
		return err
	}
	_, err := m.r.Simctl(ctx, nil, "shutdown", udid)
	var se *SimctlError
	if errors.As(err, &se) && strings.Contains(se.Message, "current state: Shutdown") {
		return nil
	}
	return err
}

// Install installs an app bundle (a .app built for the simulator).
func (m *Manager) Install(ctx context.Context, udid, appPath string) error {
	if err := m.ready(ctx); err != nil {
		return err
	}
	_, err := m.r.Simctl(ctx, nil, "install", udid, appPath)
	return err
}

// Launch starts an installed app and returns its pid.
func (m *Manager) Launch(ctx context.Context, udid, bundleID string, args []string, restart bool) (int, error) {
	if err := m.ready(ctx); err != nil {
		return 0, err
	}
	a := []string{"launch"}
	if restart {
		a = append(a, "--terminate-running-process")
	}
	a = append(a, udid, bundleID)
	out, err := m.r.Simctl(ctx, nil, append(a, args...)...)
	if err != nil {
		return 0, err
	}
	return parseLaunch(out), nil
}

// Create makes a new device (tests make throwaway ones) and returns its UDID.
func (m *Manager) Create(ctx context.Context, name, deviceType, runtimeID string) (string, error) {
	if err := m.ready(ctx); err != nil {
		return "", err
	}
	args := []string{"create", name, deviceType}
	if runtimeID != "" {
		args = append(args, runtimeID)
	}
	out, err := m.r.Simctl(ctx, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// Delete deletes a device.
func (m *Manager) Delete(ctx context.Context, udid string) error {
	if err := m.ready(ctx); err != nil {
		return err
	}
	_, err := m.r.Simctl(ctx, nil, "delete", udid)
	return err
}

// Frame is one screenshot of a device.
type Frame struct {
	JPEG []byte
	// W, H are the image's pixels.
	W, H int
}

// Capture takes a screenshot of a booted device as JPEG, at the device's own resolution.
func (m *Manager) Capture(ctx context.Context, udid string) (Frame, error) {
	if err := m.ready(ctx); err != nil {
		return Frame{}, err
	}
	out, err := m.r.Simctl(ctx, nil, "io", udid, "screenshot", "--type=jpeg", "-")
	if err != nil {
		return Frame{}, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		return Frame{}, fmt.Errorf("simctl screenshot: %w", err)
	}
	return Frame{JPEG: out, W: cfg.Width, H: cfg.Height}, nil
}

// Watch sends frames of a device to fn until ctx ends, about every interval, skipping frames that did not change. It
// returns the first failed capture (the device shut down) or fn's error.
func (m *Manager) Watch(ctx context.Context, udid string, interval time.Duration, fn func(Frame) error) error {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	var last [32]byte
	for {
		start := time.Now()
		f, err := m.Capture(ctx, udid)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if sum := sha256.Sum256(f.JPEG); sum != last {
			last = sum
			if err := fn(f); err != nil {
				return err
			}
		}
		wait := interval - time.Since(start)
		if wait < 30*time.Millisecond {
			wait = 30 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (m *Manager) lock(udid string) func() {
	m.mu.Lock()
	l := m.locks[udid]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[udid] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func clamp01(v float64) float64 { return min(max(v, 0), 1) }

// Tap touches a point (fractions of the screen) and lifts after holdMS (0: a tap).
func (m *Manager) Tap(ctx context.Context, udid string, p Point, holdMS int) error {
	if err := m.ready(ctx); err != nil {
		return err
	}
	defer m.lock(udid)()
	return m.in.Touch(ctx, udid, []Point{{clamp01(p.X), clamp01(p.Y)}}, holdMS, 0)
}

// Swipe moves a finger from one point to another over about durationMS.
func (m *Manager) Swipe(ctx context.Context, udid string, from, to Point, durationMS int) error {
	if err := m.ready(ctx); err != nil {
		return err
	}
	if durationMS <= 0 {
		durationMS = 300
	}
	durationMS = min(durationMS, 5000)
	const step = 20
	n := max(durationMS/step, 2)
	pts := make([]Point, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		pts = append(pts, Point{clamp01(from.X + (to.X-from.X)*t), clamp01(from.Y + (to.Y-from.Y)*t)})
	}
	defer m.lock(udid)()
	return m.in.Touch(ctx, udid, pts, 0, step)
}

// Type types text: key presses for what a US layout has keys for, the pasteboard and ⌘V for the rest.
func (m *Manager) Type(ctx context.Context, udid, text string) error {
	if err := m.ready(ctx); err != nil {
		return err
	}
	defer m.lock(udid)()
	for _, s := range segments(text) {
		if s.paste != "" {
			if _, err := m.r.Simctl(ctx, []byte(s.paste), "pbcopy", udid); err != nil {
				return err
			}
			if err := m.in.Keys(ctx, udid, []Press{{Usage: usageV, Mods: []int{usageCmd}}}); err != nil {
				return err
			}
			continue
		}
		// In short runs, so cancelling stops what is left.
		for i := 0; i < len(s.presses); i += 32 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := m.in.Keys(ctx, udid, s.presses[i:min(i+32, len(s.presses))]); err != nil {
				return err
			}
		}
	}
	return nil
}

// Keys presses combinations ("cmd+a delete return").
func (m *Manager) Keys(ctx context.Context, udid, keys string) error {
	if err := m.ready(ctx); err != nil {
		return err
	}
	presses, err := parseKeys(keys)
	if err != nil {
		return err
	}
	defer m.lock(udid)()
	return m.in.Keys(ctx, udid, presses)
}

// Buttons the device has.
var Buttons = []string{"home", "lock", "side_button", "siri", "apple_pay"}

// Button presses a hardware button.
func (m *Manager) Button(ctx context.Context, udid, button string, holdMS int) error {
	if err := m.ready(ctx); err != nil {
		return err
	}
	ok := false
	for _, b := range Buttons {
		ok = ok || b == button
	}
	if !ok {
		return fmt.Errorf("unknown button %q (one of %s)", button, strings.Join(Buttons, ", "))
	}
	defer m.lock(udid)()
	return m.in.Button(ctx, udid, button, holdMS)
}

func shotKey(sessionID, udid string) string { return sessionID + "\x00" + udid }

func (m *Manager) remember(sessionID, udid string, w, h int) {
	m.init()
	m.mu.Lock()
	m.shots[shotKey(sessionID, udid)] = size{w, h}
	m.mu.Unlock()
}

// fraction turns pixels of the session's last screenshot of a device into fractions of its screen.
func (m *Manager) fraction(sessionID, udid string, x, y float64) (Point, error) {
	m.init()
	m.mu.Lock()
	s, ok := m.shots[shotKey(sessionID, udid)]
	m.mu.Unlock()
	if !ok {
		return Point{}, errors.New("no screenshot of this device yet: coordinates are pixels of the last SimulatorScreenshot")
	}
	if x < 0 || y < 0 || x >= float64(s.w) || y >= float64(s.h) {
		return Point{}, fmt.Errorf("(%.0f, %.0f) is outside the last screenshot, which is %d×%d", x, y, s.w, s.h)
	}
	return Point{x / float64(s.w), y / float64(s.h)}, nil
}

func (m *Manager) activity(sessionID, udid, action string) {
	if m.OnActivity != nil {
		m.OnActivity(sessionID, udid, action)
	}
}
