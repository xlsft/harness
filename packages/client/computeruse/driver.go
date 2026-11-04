// Package computeruse lets the agent see and control apps of the machine the client runs on. The Driver is the only
// part that touches the platform; the Controller (controller.go) is where every safety decision is made, so it holds
// for every driver and is tested against a fake one: per-app grants, tiers, the check of the target app before each
// action, the refusal to type into a secure field, cancellation. Everything is device-local (manager.go).
package computeruse

import (
	"context"
	"errors"
)

// Driver talks to one platform. It only reports and performs: it decides nothing. Every method honors ctx, and
// cancelling it stops an action in flight.
//
// A target is either a desktop (macOS) or a device such as a simulator (Info.Surface); an app is addressed by the
// driver's stable id in both, and coordinates are in the driver's own space (Info.Coordinates): screen points on a
// desktop, device points on a device. A driver lists what it can do in Capabilities, and methods it lacks return
// ErrNotSupported. The Controller and the tools work from that alone, so a driver without windows or menus needs no
// changes outside its own file: its Windows is empty, and Screenshot captures the whole surface of the app.
type Driver interface {
	Info() Info
	// Permissions reports the state of what the OS must allow. It never prompts the user.
	Permissions(ctx context.Context) (Permissions, error)

	// Apps lists running apps and, with installed set, those that could be started.
	Apps(ctx context.Context, installed bool) ([]App, error)
	// Frontmost returns the app that receives keyboard input.
	Frontmost(ctx context.Context) (App, error)
	// AppAt returns the app whose window is on top at p, whatever the layer (the menu bar and Dock count, as apps of
	// their own). App.ID is empty when nothing identifiable is there.
	AppAt(ctx context.Context, p Point) (App, error)
	// Windows lists an app's visible windows, front to back.
	Windows(ctx context.Context, appID string) ([]Window, error)
	// FocusedElement describes what would receive typed text.
	FocusedElement(ctx context.Context) (FocusedElement, error)

	// Screenshot captures one window (req.WindowID) or the app's front window, and only that: other apps must not
	// show in the image.
	Screenshot(ctx context.Context, req ScreenshotRequest) (Image, error)

	// Activate brings an app to the front, starting it if needed.
	Activate(ctx context.Context, appID string) error
	// FocusWindow raises one window of an app.
	FocusWindow(ctx context.Context, appID string, windowID int64) error

	// The input methods take Expect, the app that must be frontmost at the moment events are posted. A driver checks
	// it as late as it can, on top of the Controller's check, and returns ErrTargetChanged when it does not hold.
	// Type and Key also return ErrSecureField when a secure field took focus in the meantime (RefuseSecure).
	Click(ctx context.Context, c Click) error
	Drag(ctx context.Context, d Drag) error
	Scroll(ctx context.Context, s Scroll) error
	Type(ctx context.Context, t TypeText) error
	Key(ctx context.Context, k KeyPress) error

	// Find searches the accessibility tree of an app. A secure field's Value is never returned.
	Find(ctx context.Context, q FindQuery) ([]Element, error)
	// Menu runs an item of the app's menu bar by its path ("File", "Save").
	Menu(ctx context.Context, appID string, path []string) error
}

// Indicator is implemented by drivers that can show something on the machine itself while the agent controls it,
// beyond the frontends: an agent working in another app hides the app's own window. Failing to show it is not fatal.
type Indicator interface {
	// ShowIndicator shows label until hide is called; set changes the label. onStop runs when the user asks to stop.
	ShowIndicator(label string, onStop func()) (set func(label string), hide func(), err error)
}

// Surface is what a driver controls.
type Surface string

// Surfaces.
const (
	SurfaceDesktop Surface = "desktop"
	SurfaceDevice  Surface = "device"
)

// Info describes a driver.
type Info struct {
	// Platform is "macos", "linux", "windows"...
	Platform string
	// Supported is false for the stubs of platforms without a driver yet; Reason says so.
	Supported bool
	Reason    string
	Surface   Surface
	// Coordinates names the unit of Point ("screen points", "device points").
	Coordinates  string
	Capabilities Capabilities
}

// Capabilities says what a driver can do. A tool whose capability is missing is not offered to the model.
type Capabilities struct {
	Screenshot  bool
	Windows     bool
	Click       bool
	DoubleClick bool
	RightClick  bool
	Modifiers   bool
	Drag        bool
	Scroll      bool
	Type        bool
	Key         bool
	Focus       bool
	Find        bool
	Menu        bool
	// SecureFields: FocusedElement can tell a secure field. Without it there is nothing to refuse on.
	SecureFields bool
	// HitTest: AppAt works.
	HitTest bool
}

// Permissions is what the OS allows this process. Items are empty on platforms with no such thing.
type Permissions struct {
	Items []Permission
	// Reason, when set, says why the driver cannot work at all right now (its helper program is missing).
	Reason string
}

// Permission IDs.
const (
	PermScreenRecording = "screen_recording"
	PermAccessibility   = "accessibility"
)

// Permission is one permission of the OS.
type Permission struct {
	ID      string
	Name    string
	Granted bool
	// HowToGrant tells the user where to turn it on.
	HowToGrant string
}

// Missing returns how to grant permission id, or "" when it is granted (or the platform has no such thing).
func (p Permissions) Missing(id string) string {
	for _, it := range p.Items {
		if it.ID == id && !it.Granted {
			return it.Name + " is not allowed. " + it.HowToGrant
		}
	}
	return ""
}

// App is an application.
type App struct {
	// ID is stable across runs: the bundle id on macOS.
	ID      string
	Name    string
	Running bool
	PID     int
}

// Point is a position in the driver's coordinates.
type Point struct{ X, Y float64 }

// Rect is a rectangle in the driver's coordinates.
type Rect struct{ X, Y, W, H float64 }

// Contains reports whether p is inside r.
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X && p.Y >= r.Y && p.X < r.X+r.W && p.Y < r.Y+r.H
}

// Window is a visible window.
type Window struct {
	ID     int64
	AppID  string
	Title  string
	Bounds Rect
}

// FocusedElement is the keyboard focus.
type FocusedElement struct {
	// AppID is the app that holds it ("" when the OS does not say).
	AppID string
	Role  string
	// Secure: a password field, or anything else in which the OS hides input from other programs.
	Secure bool
}

// ScreenshotRequest says what to capture.
type ScreenshotRequest struct {
	AppID string
	// WindowID is 0 for the app's front window (or a device's whole surface).
	WindowID int64
}

// Image is a captured window. Bounds is what it shows, in driver coordinates, which relates its pixels to points.
type Image struct {
	// Data is PNG or JPEG, whatever image.Decode reads.
	Data     []byte
	WindowID int64
	Bounds   Rect
	Title    string
}

// Button is a mouse button.
type Button string

// Mouse buttons.
const (
	ButtonLeft  Button = "left"
	ButtonRight Button = "right"
)

// Click is a click of one button, Count times (2 is a double click).
type Click struct {
	Point     Point
	Button    Button
	Count     int
	Modifiers []string
	Expect    string
}

// Drag presses at From, moves to To and releases.
type Drag struct {
	From, To Point
	Expect   string
}

// Scroll scrolls at a point by DX and DY lines (positive: down and right).
type Scroll struct {
	Point  Point
	DX, DY int
	Expect string
}

// TypeText types text as Unicode input.
type TypeText struct {
	Text         string
	Expect       string
	RefuseSecure bool
}

// KeyPress presses one combination ("cmd+s"), normalized by ParseCombo, Repeat times.
type KeyPress struct {
	Combo        string
	Repeat       int
	Expect       string
	RefuseSecure bool
}

// FindQuery searches an app's accessibility tree. Query matches the role, title, description and value; Role only
// the role. Both empty lists the elements up to Max.
type FindQuery struct {
	AppID    string
	WindowID int64
	Query    string
	Role     string
	Max      int
}

// Element is an accessibility element.
type Element struct {
	Role        string
	Title       string
	Description string
	Value       string
	Frame       Rect
	Enabled     bool
	Focused     bool
	Secure      bool
}

// Errors a driver reports. Controller and tool messages build on them.
var (
	// ErrNotSupported: this driver cannot do that.
	ErrNotSupported = errors.New("not supported by this platform's driver")
	// ErrTargetChanged: the expected app was no longer frontmost when the events were about to be posted.
	ErrTargetChanged = errors.New("another app came to the front; nothing was sent")
	// ErrSecureField: a secure text field has the keyboard focus.
	ErrSecureField = errors.New("a secure text field has the keyboard focus; only the user types there")
)
