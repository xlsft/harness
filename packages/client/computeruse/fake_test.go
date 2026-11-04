package computeruse

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"sync"
)

// fakeDriver stands in for the machine in every test: nothing here touches a real desktop. It records input, and hooks
// let a test move the focus at the worst moment.
type fakeDriver struct {
	mu    sync.Mutex
	info  Info
	perms Permissions
	apps  []App
	front string
	// wins are the visible windows by app id.
	wins map[string][]Window
	// at decides what AppAt returns; nil means the front app.
	at      func(Point) App
	focused FocusedElement
	// frontAfterActivate, when set, is what Activate brings forward instead of the app asked for.
	frontAfterActivate string
	// beforeInput runs when an input method is reached, after the Controller's checks.
	beforeInput func()
	// block makes input methods wait for ctx: an action in flight.
	block chan struct{}
	// imgW, imgH size the captured image.
	imgW, imgH int

	clicks   []Click
	drags    []Drag
	scrolls  []Scroll
	typed    []TypeText
	keys     []KeyPress
	menus    [][]string
	activate []string
	shots    []ScreenshotRequest
	finds    []FindQuery
	elements []Element
	findErr  error
}

const (
	notes    = "com.apple.Notes"
	finder   = "com.apple.finder"
	safari   = "com.apple.Safari"
	terminal = "com.apple.Terminal"
	vault    = "com.1password.1password"
	settings = "com.apple.systempreferences"
)

func newFake() *fakeDriver {
	d := &fakeDriver{
		info: Info{
			Platform: "fake", Supported: true, Surface: SurfaceDesktop, Coordinates: "screen points",
			Capabilities: Capabilities{Screenshot: true, Windows: true, Click: true, DoubleClick: true, RightClick: true, Modifiers: true, Drag: true, Scroll: true, Type: true, Key: true, Focus: true, Find: true, Menu: true, SecureFields: true, HitTest: true},
		},
		perms: Permissions{Items: []Permission{
			{ID: PermScreenRecording, Name: "Screen Recording", Granted: true, HowToGrant: "Turn it on."},
			{ID: PermAccessibility, Name: "Accessibility", Granted: true, HowToGrant: "Turn it on."},
		}},
		apps: []App{
			{ID: notes, Name: "Notes", Running: true}, {ID: finder, Name: "Finder", Running: true},
			{ID: safari, Name: "Safari", Running: true}, {ID: terminal, Name: "Terminal", Running: true},
			{ID: vault, Name: "1Password", Running: true}, {ID: settings, Name: "System Settings", Running: true},
		},
		front: notes,
		wins: map[string][]Window{
			notes:    {{ID: 11, AppID: notes, Title: "A note", Bounds: Rect{100, 50, 800, 600}}, {ID: 12, AppID: notes, Title: "Other", Bounds: Rect{0, 0, 300, 200}}},
			finder:   {{ID: 21, AppID: finder, Title: "Home", Bounds: Rect{0, 0, 800, 600}}},
			safari:   {{ID: 31, AppID: safari, Title: "A page", Bounds: Rect{0, 0, 800, 600}}},
			terminal: {{ID: 41, AppID: terminal, Title: "zsh", Bounds: Rect{0, 0, 800, 600}}},
		},
		focused: FocusedElement{AppID: notes, Role: "AXTextArea"},
		imgW:    400, imgH: 300,
	}
	return d
}

func (d *fakeDriver) app(id string) App {
	for _, a := range d.apps {
		if a.ID == id {
			return a
		}
	}
	return App{ID: id, Name: id}
}

func (d *fakeDriver) Info() Info { return d.info }

func (d *fakeDriver) Permissions(context.Context) (Permissions, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.perms, nil
}

func (d *fakeDriver) Apps(context.Context, bool) ([]App, error) { return d.apps, nil }

func (d *fakeDriver) Frontmost(context.Context) (App, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.app(d.front), nil
}

func (d *fakeDriver) AppAt(_ context.Context, p Point) (App, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.at != nil {
		return d.at(p), nil
	}
	return d.app(d.front), nil
}

func (d *fakeDriver) Windows(_ context.Context, id string) ([]Window, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.wins[id], nil
}

func (d *fakeDriver) FocusedElement(context.Context) (FocusedElement, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.focused
	if f.AppID != "" && !f.Secure {
		f.AppID = d.front
	}
	return f, nil
}

func (d *fakeDriver) Screenshot(_ context.Context, r ScreenshotRequest) (Image, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.shots = append(d.shots, r)
	img := image.NewRGBA(image.Rect(0, 0, d.imgW, d.imgH))
	img.Set(0, 0, color.White)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return Image{}, err
	}
	out := Image{Data: buf.Bytes(), WindowID: r.WindowID, Bounds: Rect{0, 0, float64(d.imgW), float64(d.imgH)}}
	for _, w := range d.wins[r.AppID] {
		if w.ID == r.WindowID {
			out.Bounds, out.Title = w.Bounds, w.Title
		}
	}
	return out, nil
}

func (d *fakeDriver) Activate(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.activate = append(d.activate, id)
	d.front = id
	if d.frontAfterActivate != "" {
		d.front = d.frontAfterActivate
	}
	return nil
}

func (d *fakeDriver) FocusWindow(context.Context, string, int64) error { return nil }

// enter is what an input method does first. It gives the hook its moment, then checks Expect as the real driver does.
func (d *fakeDriver) enter(ctx context.Context, expect string) error {
	d.mu.Lock()
	hook, block := d.beforeInput, d.block
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if expect != "" && d.front != expect {
		return ErrTargetChanged
	}
	return nil
}

func (d *fakeDriver) Click(ctx context.Context, c Click) error {
	if err := d.enter(ctx, c.Expect); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clicks = append(d.clicks, c)
	return nil
}

func (d *fakeDriver) Drag(ctx context.Context, g Drag) error {
	if err := d.enter(ctx, g.Expect); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drags = append(d.drags, g)
	return nil
}

func (d *fakeDriver) Scroll(ctx context.Context, s Scroll) error {
	if err := d.enter(ctx, s.Expect); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scrolls = append(d.scrolls, s)
	return nil
}

func (d *fakeDriver) Type(ctx context.Context, t TypeText) error {
	if err := d.enter(ctx, t.Expect); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if t.RefuseSecure && d.focused.Secure {
		return ErrSecureField
	}
	d.typed = append(d.typed, t)
	return nil
}

func (d *fakeDriver) Key(ctx context.Context, k KeyPress) error {
	if err := d.enter(ctx, k.Expect); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if k.RefuseSecure && d.focused.Secure {
		return ErrSecureField
	}
	d.keys = append(d.keys, k)
	return nil
}

func (d *fakeDriver) Find(_ context.Context, q FindQuery) ([]Element, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.finds = append(d.finds, q)
	return d.elements, d.findErr
}

func (d *fakeDriver) Menu(_ context.Context, _ string, path []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.menus = append(d.menus, path)
	return nil
}

// inputCount is every input event the machine would have received.
func (d *fakeDriver) inputCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.clicks) + len(d.drags) + len(d.scrolls) + len(d.typed) + len(d.keys) + len(d.menus)
}

// answerer is the user at an access card.
type answerer struct {
	approve func(AccessRequest) []string
	asked   []AccessRequest
}

func (a *answerer) AskComputerAccess(_ context.Context, r AccessRequest) ([]string, error) {
	a.asked = append(a.asked, r)
	return a.approve(r), nil
}

func approveAll(r AccessRequest) []string {
	var out []string
	for _, x := range r.Apps {
		out = append(out, x.App.ID)
	}
	return out
}
