package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/harness/packages/client/tools"
)

// fakeRunner is simctl for the tests: a device list, and a record of what was run.
type fakeRunner struct {
	mu    sync.Mutex
	list  string
	calls [][]string
	stdin [][]byte
	shot  []byte
}

func (f *fakeRunner) DeveloperDir(context.Context) string { return "/Applications/Xcode.app/Contents/Developer" }

func (f *fakeRunner) Simctl(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	f.stdin = append(f.stdin, stdin)
	switch args[0] {
	case "list":
		return []byte(f.list), nil
	case "io":
		return f.shot, nil
	case "launch":
		return []byte(args[len(args)-1] + ": 4242\n"), nil
	case "boot":
		if args[1] == bootedUDID {
			return nil, &SimctlError{Args: args, Message: "Unable to boot device in current state: Booted"}
		}
	}
	return nil, nil
}

func (f *fakeRunner) ran(sub string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if c[0] == sub {
			out = append(out, c)
		}
	}
	return out
}

type fakeInput struct {
	mu      sync.Mutex
	touches [][]Point
	presses [][]Press
	buttons []string
}

func (f *fakeInput) Touch(_ context.Context, _ string, points []Point, _, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches = append(f.touches, points)
	return nil
}

func (f *fakeInput) Keys(_ context.Context, _ string, presses []Press) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presses = append(f.presses, presses)
	return nil
}

func (f *fakeInput) Button(_ context.Context, _, b string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buttons = append(f.buttons, b)
	return nil
}

const (
	bootedUDID = "11111111-1111-1111-1111-111111111111"
	offUDID    = "22222222-2222-2222-2222-222222222222"
)

const listJSON = `{"devices":{
 "com.apple.CoreSimulator.SimRuntime.iOS-26-0":[
  {"udid":"22222222-2222-2222-2222-222222222222","name":"iPhone Air","state":"Shutdown","isAvailable":true,"deviceTypeIdentifier":"com.apple.CoreSimulator.SimDeviceType.iPhone-Air"},
  {"udid":"11111111-1111-1111-1111-111111111111","name":"iPhone 17 Pro","state":"Booted","isAvailable":true,"deviceTypeIdentifier":"com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro"}],
 "com.apple.CoreSimulator.SimRuntime.watchOS-26-0":[
  {"udid":"33333333-3333-3333-3333-333333333333","name":"Apple Watch","state":"Shutdown","isAvailable":true}]}}`

func jpegOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 80, 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func newTest() (*Manager, *fakeRunner, *fakeInput) {
	r := &fakeRunner{list: listJSON, shot: jpegOf(390, 844)}
	in := &fakeInput{}
	return &Manager{Runner: r, Input: in}, r, in
}

func run(t *testing.T, m *Manager, name string, input any) tools.Result {
	t.Helper()
	data, _ := json.Marshal(input)
	for _, tl := range m.Tools() {
		if tl.Name() == name {
			return tl.Run(context.Background(), &tools.Env{SessionID: "s1", Cwd: t.TempDir()}, tools.Call{ID: "c1", Input: data})
		}
	}
	t.Fatalf("no tool %s", name)
	return tools.Result{}
}

func text(r tools.Result) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestParseDevices(t *testing.T) {
	devs, err := parseDevices([]byte(listJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 3 || devs[0].UDID != bootedUDID || devs[0].RuntimeName != "iOS 26.0" || !devs[0].Booted() {
		t.Fatalf("booted first, runtime named: %+v", devs)
	}
	if devs[2].RuntimeName != "iOS 26.0" && devs[1].RuntimeName != "watchOS 26.0" {
		t.Fatalf("by runtime: %+v", devs)
	}
}

func TestResolve(t *testing.T) {
	m, _, _ := newTest()
	ctx := context.Background()
	for ref, want := range map[string]string{"": bootedUDID, "booted": bootedUDID, "iphone air": offUDID, offUDID: offUDID} {
		d, err := m.Resolve(ctx, ref)
		if err != nil || d.UDID != want {
			t.Errorf("%q: %v %v", ref, d.UDID, err)
		}
	}
	if _, err := m.Resolve(ctx, "Pixel"); err == nil {
		t.Error("an unknown name resolves")
	}
}

func TestSegments(t *testing.T) {
	segs := segments("Hi, é!")
	if len(segs) != 3 || segs[1].paste != "é" {
		t.Fatalf("keys, paste, keys: %+v", segs)
	}
	want := []Press{{Usage: 11, Mods: []int{usageShift}}, {Usage: 12}, {Usage: 54}, {Usage: 44}}
	if !reflect.DeepEqual(segs[0].presses, want) {
		t.Fatalf("H i , space: %+v", segs[0].presses)
	}
	if p, _ := keyFor(')'); p.Usage != 39 || len(p.Mods) != 1 {
		t.Fatalf("shift+0: %+v", p)
	}
	if p, _ := keyFor('@'); p.Usage != 31 {
		t.Fatalf("shift+2: %+v", p)
	}
	presses, err := parseKeys("cmd+a delete Return")
	if err != nil || len(presses) != 3 || presses[0].Usage != 4 || presses[0].Mods[0] != usageCmd || presses[1].Usage != 42 || presses[2].Usage != 40 {
		t.Fatalf("combos: %+v %v", presses, err)
	}
	if _, err := parseKeys("hyper+x"); err == nil {
		t.Fatal("an unknown modifier parses")
	}
}

func TestToolsDriveTheBootedDevice(t *testing.T) {
	m, r, in := newTest()
	if res := run(t, m, ToolTap, map[string]any{"x": 10, "y": 10}); !res.IsError || !strings.Contains(text(res), "no screenshot") {
		t.Fatalf("a tap before any screenshot: %s", text(res))
	}
	res := run(t, m, ToolScreenshot, map[string]any{})
	if res.IsError || len(res.Content) != 2 || !strings.Contains(text(res), "390×844") {
		t.Fatalf("screenshot: %s", text(res))
	}
	if s, _ := res.Structured.(map[string]any); s["computer_use"] != true {
		t.Fatal("the screenshot is not kept off the upload")
	}
	if res := run(t, m, ToolTap, map[string]any{"x": 195, "y": 422}); res.IsError {
		t.Fatal(text(res))
	}
	if got := in.touches[0]; len(got) != 1 || got[0] != (Point{0.5, 0.5}) {
		t.Fatalf("tap at the middle: %+v", got)
	}
	if res := run(t, m, ToolTap, map[string]any{"x": 400, "y": 10}); !res.IsError {
		t.Fatal("a tap outside the screenshot went through")
	}
	if res := run(t, m, ToolSwipe, map[string]any{"from_x": 195, "from_y": 700, "to_x": 195, "to_y": 100, "duration_ms": 200}); res.IsError {
		t.Fatal(text(res))
	}
	if sw := in.touches[1]; len(sw) < 3 || sw[0].Y <= sw[len(sw)-1].Y {
		t.Fatalf("swipe up: %+v", sw)
	}
	if res := run(t, m, ToolType, map[string]any{"text": "ok ü", "keys": "return"}); res.IsError {
		t.Fatal(text(res))
	}
	if pb := r.ran("pbcopy"); len(pb) != 1 {
		t.Fatalf("ü goes through the pasteboard: %v", r.calls)
	}
	if res := run(t, m, ToolButton, map[string]any{"button": "home"}); res.IsError || in.buttons[0] != "home" {
		t.Fatal(text(res))
	}
	if res := run(t, m, ToolButton, map[string]any{"button": "volume"}); !res.IsError {
		t.Fatal("an unknown button went through")
	}
	if res := run(t, m, ToolLaunch, map[string]any{"bundle_id": "com.example.app"}); res.IsError || !strings.Contains(text(res), "4242") {
		t.Fatal(text(res))
	}
	if l := r.ran("launch"); len(l) != 1 || l[0][1] != "--terminate-running-process" {
		t.Fatalf("launch restarts by default: %v", l)
	}
	if res := run(t, m, ToolInstall, map[string]any{"app_path": "Nope.app"}); !res.IsError {
		t.Fatal("a missing bundle installs")
	}
}

func TestBootAndShutdownDevices(t *testing.T) {
	m, r, _ := newTest()
	if res := run(t, m, ToolTap, map[string]any{"device": offUDID, "x": 1, "y": 1}); !res.IsError || !strings.Contains(text(res), "not booted") {
		t.Fatalf("input to a device that is off: %s", text(res))
	}
	if res := run(t, m, ToolBoot, map[string]any{"device": "iPhone Air"}); res.IsError {
		t.Fatal(text(res))
	}
	if b := r.ran("bootstatus"); len(b) != 1 || b[0][1] != offUDID {
		t.Fatalf("boot waits: %v", r.calls)
	}
	if err := m.Boot(context.Background(), bootedUDID); err != nil {
		t.Fatalf("booting a booted device is fine: %v", err)
	}
	if res := run(t, m, ToolList, map[string]any{}); res.IsError || strings.Contains(text(res), "Apple Watch") || !strings.Contains(text(res), "iPhone Air") {
		t.Fatalf("list: %s", text(res))
	}
}
