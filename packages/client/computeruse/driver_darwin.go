//go:build darwin

package computeruse

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/config"
)

// The macOS driver. The client is built with CGO_ENABLED=0 for the desktop sidecar, so the native APIs (CGEvent,
// Accessibility, NSWorkspace, the window list) live in a small Swift program, helper/main.swift, compiled at build time
// by helper/build.sh and shipped next to the client. It speaks one JSON object per line over stdin and stdout and stays
// running between calls, which keeps the checks around each action fast. Cancelling an action kills the process, and
// that stops events still to be sent; the next call starts it again. osascript was the other option: it needs a third
// permission for System Events, takes 100 ms or more per call, and offers no check right before posting events.
//
// Screenshots use the system's screencapture -l, which takes one window, whatever covers it, so other apps stay out.
// CGWindowListCreateImage is gone from current SDKs, and ScreenCaptureKit would add much to the helper for no gain here.
//
// Permissions are macOS TCC ones, Screen Recording and Accessibility, attributed to the app that started the client
// (the Harness app, or the terminal in a dev run). They are only ever read, with the two calls that do not prompt
// (CGPreflightScreenCaptureAccess, AXIsProcessTrusted), and a capture is never tried without Screen Recording, since
// that would make macOS prompt. The user turns them on in System Settings.

const helperName = "harness-computer-helper"

type darwinDriver struct {
	// path is where the helper was found ("" when it was not).
	once sync.Once
	path string

	// sem serializes the calls to the helper, and can be given up on when ctx ends (unlike a mutex).
	sem chan struct{}
	// proc is the running helper (only touched while holding sem).
	proc *helperProc
}

// NewDriver returns the driver of this platform.
func NewDriver() Driver { return &darwinDriver{sem: make(chan struct{}, 1)} }

func (d *darwinDriver) Info() Info {
	return Info{
		Platform: "macos", Supported: true, Surface: SurfaceDesktop, Coordinates: "screen points",
		Capabilities: Capabilities{
			Screenshot: true, Windows: true, Click: true, DoubleClick: true, RightClick: true, Modifiers: true, Drag: true,
			Scroll: true, Type: true, Key: true, Focus: true, Find: true, Menu: true, SecureFields: true, HitTest: true,
		},
	}
}

// helperPath finds the helper: HARNESS_COMPUTER_HELPER, next to the executable (the sidecar's place, with or without the
// target triple the build leaves on it), or HARNESS_HOME/bin of a dev build.
func (d *darwinDriver) helperPath() string {
	d.once.Do(func() {
		if p := os.Getenv("HARNESS_COMPUTER_HELPER"); p != "" {
			d.path = p
			return
		}
		var dirs []string
		if exe, err := os.Executable(); err == nil {
			if r, err := filepath.EvalSymlinks(exe); err == nil {
				exe = r
			}
			dirs = append(dirs, filepath.Dir(exe))
		}
		dirs = append(dirs, filepath.Join(config.Home(), "bin"))
		for _, dir := range dirs {
			for _, n := range []string{helperName, helperName + "-aarch64-apple-darwin", helperName + "-x86_64-apple-darwin"} {
				if st, err := os.Stat(filepath.Join(dir, n)); err == nil && !st.IsDir() {
					d.path = filepath.Join(dir, n)
					return
				}
			}
		}
	})
	return d.path
}

var errNoHelper = errors.New("the computer use helper program is missing from this build (" + helperName + "). Build it with packages/client/computeruse/helper/build.sh, or use the desktop app, which ships it")

type helperProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader
}

func (p *helperProc) kill() {
	_ = p.stdin.Close()
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
}

func (d *darwinDriver) start() (*helperProc, error) {
	path := d.helperPath()
	if path == "" {
		return nil, errNoHelper
	}
	cmd := exec.Command(path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the computer use helper: %w", err)
	}
	return &helperProc{cmd: cmd, stdin: stdin, out: bufio.NewReaderSize(stdout, 1<<20)}, nil
}

type helperReply struct {
	OK     bool            `json:"ok"`
	Code   string          `json:"code"`
	Error  string          `json:"error"`
	Result json.RawMessage `json:"result"`
}

// helperTimeout bounds one call; typing long text is the slow case.
const helperTimeout = 60 * time.Second

// call sends one request and waits for its reply. When ctx ends first the helper is killed, so what it was sending stops.
func (d *darwinDriver) call(ctx context.Context, op string, params map[string]any, out any) error {
	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.proc == nil {
		p, err := d.start()
		if err != nil {
			return err
		}
		d.proc = p
	}
	msg := map[string]any{"op": op}
	for k, v := range params {
		msg[k] = v
	}
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := d.proc.stdin.Write(append(line, '\n')); err != nil {
		d.proc.kill()
		d.proc = nil
		return fmt.Errorf("computer use helper: %w", err)
	}
	type lineResult struct {
		b   []byte
		err error
	}
	reader := d.proc.out
	ch := make(chan lineResult, 1)
	go func() {
		b, err := reader.ReadBytes('\n')
		ch <- lineResult{b, err}
	}()
	timer := time.NewTimer(helperTimeout)
	defer timer.Stop()
	var res lineResult
	select {
	case res = <-ch:
	case <-ctx.Done():
		d.abort(op)
		return ctx.Err()
	case <-timer.C:
		d.abort(op)
		return errors.New("the computer use helper did not answer in time")
	}
	if res.err != nil {
		d.proc.kill()
		d.proc = nil
		return fmt.Errorf("the computer use helper stopped: %w", res.err)
	}
	var r helperReply
	if err := json.Unmarshal(res.b, &r); err != nil {
		return fmt.Errorf("computer use helper: bad reply: %w", err)
	}
	if !r.OK {
		switch r.Code {
		case "target_changed":
			return ErrTargetChanged
		case "secure_field":
			return ErrSecureField
		}
		return errors.New(r.Error)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// abort kills the helper in the middle of op. A click or drag may have left a mouse button down, which would make the
// user's next move a drag, so a fresh helper lets go of it.
func (d *darwinDriver) abort(op string) {
	d.proc.kill()
	d.proc = nil
	if op != "click" && op != "drag" {
		return
	}
	p, err := d.start()
	if err != nil {
		return
	}
	d.proc = p
	if _, err := p.stdin.Write([]byte(`{"op":"release_mouse"}` + "\n")); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = p.out.ReadBytes('\n')
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		p.kill()
		d.proc = nil
	}
}

func (d *darwinDriver) Permissions(ctx context.Context) (Permissions, error) {
	var r struct {
		ScreenRecording bool `json:"screen_recording"`
		Accessibility   bool `json:"accessibility"`
	}
	if err := d.call(ctx, "permissions", nil, &r); err != nil {
		if errors.Is(err, errNoHelper) {
			return Permissions{Reason: err.Error()}, nil
		}
		return Permissions{}, err
	}
	const howTo = "Turn it on for the Harness app (or the terminal that started the client) in System Settings > Privacy & Security > %s, then restart Harness."
	return Permissions{Items: []Permission{
		{ID: PermScreenRecording, Name: "Screen Recording", Granted: r.ScreenRecording, HowToGrant: fmt.Sprintf(howTo, "Screen & System Audio Recording")},
		{ID: PermAccessibility, Name: "Accessibility", Granted: r.Accessibility, HowToGrant: fmt.Sprintf(howTo, "Accessibility")},
	}}, nil
}

type helperApp struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	PID     int    `json:"pid"`
}

func (a helperApp) app() App { return App{ID: a.ID, Name: a.Name, Running: a.Running, PID: a.PID} }

func (d *darwinDriver) Apps(ctx context.Context, installed bool) ([]App, error) {
	var r []helperApp
	if err := d.call(ctx, "apps", map[string]any{"installed": installed}, &r); err != nil {
		return nil, err
	}
	out := make([]App, len(r))
	for i, a := range r {
		out[i] = a.app()
	}
	return out, nil
}

func (d *darwinDriver) Frontmost(ctx context.Context) (App, error) {
	var r helperApp
	err := d.call(ctx, "frontmost", nil, &r)
	return r.app(), err
}

func (d *darwinDriver) AppAt(ctx context.Context, p Point) (App, error) {
	var r helperApp
	err := d.call(ctx, "app_at", map[string]any{"x": p.X, "y": p.Y}, &r)
	return r.app(), err
}

type helperWindow struct {
	ID    int64   `json:"id"`
	Title string  `json:"title"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
}

func (d *darwinDriver) Windows(ctx context.Context, appID string) ([]Window, error) {
	var r []helperWindow
	if err := d.call(ctx, "windows", map[string]any{"app": appID}, &r); err != nil {
		return nil, err
	}
	out := make([]Window, len(r))
	for i, w := range r {
		out[i] = Window{ID: w.ID, AppID: appID, Title: w.Title, Bounds: Rect{w.X, w.Y, w.W, w.H}}
	}
	return out, nil
}

func (d *darwinDriver) FocusedElement(ctx context.Context) (FocusedElement, error) {
	var r struct {
		App    string `json:"app"`
		Role   string `json:"role"`
		Secure bool   `json:"secure"`
	}
	err := d.call(ctx, "focused", nil, &r)
	return FocusedElement{AppID: r.App, Role: r.Role, Secure: r.Secure}, err
}

// Screenshot captures one window with screencapture. The Controller only calls it after checking Screen Recording.
func (d *darwinDriver) Screenshot(ctx context.Context, req ScreenshotRequest) (Image, error) {
	wins, err := d.Windows(ctx, req.AppID)
	if err != nil {
		return Image{}, err
	}
	var win *Window
	for i := range wins {
		if req.WindowID == 0 || wins[i].ID == req.WindowID {
			win = &wins[i]
			break
		}
	}
	if win == nil {
		return Image{}, fmt.Errorf("no visible window of %s to capture", req.AppID)
	}
	dir, err := os.MkdirTemp("", "harness-capture-")
	if err != nil {
		return Image{}, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "window.png")
	// -x no sound, -o no shadow (so the image is exactly the window's bounds), -l that window only.
	out, err := exec.CommandContext(ctx, "/usr/sbin/screencapture", "-x", "-o", "-t", "png", "-l"+strconv.FormatInt(win.ID, 10), file).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return Image{}, ctx.Err()
		}
		return Image{}, fmt.Errorf("screencapture: %v: %s", err, out)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return Image{}, fmt.Errorf("screencapture left no image: %w", err)
	}
	return Image{Data: data, WindowID: win.ID, Bounds: win.Bounds, Title: win.Title}, nil
}

func (d *darwinDriver) Activate(ctx context.Context, appID string) error {
	return d.call(ctx, "activate", map[string]any{"app": appID}, nil)
}

func (d *darwinDriver) FocusWindow(ctx context.Context, appID string, windowID int64) error {
	return d.call(ctx, "focus_window", map[string]any{"app": appID, "window": windowID}, nil)
}

func (d *darwinDriver) Click(ctx context.Context, c Click) error {
	return d.call(ctx, "click", map[string]any{"x": c.Point.X, "y": c.Point.Y, "button": string(c.Button), "count": c.Count, "modifiers": c.Modifiers, "expect": c.Expect}, nil)
}

func (d *darwinDriver) Drag(ctx context.Context, g Drag) error {
	return d.call(ctx, "drag", map[string]any{"x": g.From.X, "y": g.From.Y, "x2": g.To.X, "y2": g.To.Y, "expect": g.Expect}, nil)
}

func (d *darwinDriver) Scroll(ctx context.Context, s Scroll) error {
	return d.call(ctx, "scroll", map[string]any{"x": s.Point.X, "y": s.Point.Y, "dx": s.DX, "dy": s.DY, "expect": s.Expect}, nil)
}

func (d *darwinDriver) Type(ctx context.Context, t TypeText) error {
	return d.call(ctx, "type", map[string]any{"text": t.Text, "expect": t.Expect, "refuse_secure": t.RefuseSecure}, nil)
}

func (d *darwinDriver) Key(ctx context.Context, k KeyPress) error {
	return d.call(ctx, "key", map[string]any{"combo": k.Combo, "repeat": k.Repeat, "expect": k.Expect, "refuse_secure": k.RefuseSecure}, nil)
}

func (d *darwinDriver) Find(ctx context.Context, q FindQuery) ([]Element, error) {
	var r []struct {
		Role        string  `json:"role"`
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Value       string  `json:"value"`
		X           float64 `json:"x"`
		Y           float64 `json:"y"`
		W           float64 `json:"w"`
		H           float64 `json:"h"`
		Enabled     bool    `json:"enabled"`
		Focused     bool    `json:"focused"`
		Secure      bool    `json:"secure"`
	}
	if err := d.call(ctx, "find", map[string]any{"app": q.AppID, "window": q.WindowID, "query": q.Query, "role": q.Role, "max": q.Max}, &r); err != nil {
		return nil, err
	}
	out := make([]Element, len(r))
	for i, e := range r {
		out[i] = Element{Role: e.Role, Title: e.Title, Description: e.Description, Value: e.Value, Frame: Rect{e.X, e.Y, e.W, e.H}, Enabled: e.Enabled, Focused: e.Focused, Secure: e.Secure}
	}
	return out, nil
}

func (d *darwinDriver) Menu(ctx context.Context, appID string, path []string) error {
	return d.call(ctx, "menu", map[string]any{"app": appID, "path": path}, nil)
}

// ShowIndicator runs the helper's indicator mode: a small always-on-top panel with a Stop button, in a process of its
// own, so that killing the action helper on cancel leaves it up.
func (d *darwinDriver) ShowIndicator(label string, onStop func()) (func(string), func(), error) {
	path := d.helperPath()
	if path == "" {
		return nil, nil, errNoHelper
	}
	cmd := exec.Command(path, "--indicator", label)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "stop" {
				onStop()
			}
		}
	}()
	go func() { _ = cmd.Wait() }()
	var mu sync.Mutex
	set := func(l string) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(l)
		_, _ = stdin.Write(append(b, '\n'))
	}
	hide := func() {
		mu.Lock()
		defer mu.Unlock()
		_ = stdin.Close() // the helper exits on end of input
		_ = cmd.Process.Kill()
	}
	return set, hide, nil
}
