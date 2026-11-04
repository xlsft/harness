package computeruse

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// Controller is one session's computer use: its grants and the checks made on every action. Nothing reaches the driver
// except through it. The rules:
//
//   - Off unless the user turned the setting on, and refused when the session's last message came from another device.
//   - Every action names an app, which needs a grant from this session, given by the user in the access card.
//   - The tier is the app's own (Classify) and is looked up again for each action.
//   - Input goes to the app it names, which must be frontmost at that moment: the Controller asks the driver, brings the
//     app forward if it is not, checks again and refuses if something else stays in front; the driver checks a last
//     time when it posts the events (Expect). A pointer action also needs the point to be on that app's window. So a
//     focus switch nobody saw, between two actions or during one, cannot send input to the wrong app.
//   - No typing into a secure field: refused when the focused element reports one, and when the OS says so otherwise.
//   - Stop cancels what is in flight and revokes the grants; the session's Stop cancels through ctx.
type Controller struct {
	m         *Manager
	d         Driver
	sessionID string

	mu     sync.Mutex
	grants map[string]Grant
	// frames are the last screenshot per app id: what the model's coordinates refer to.
	frames map[string]frame
	// remote: the session's last message came from another device.
	remote      bool
	controlling bool
	action      string
	cancels     map[int]context.CancelFunc
	nextCancel  int
}

// Grant is an app the session may use.
type Grant struct {
	App  App
	Tier Tier
}

// State is the session's computer use as frontends show it.
type State struct {
	Controlling bool
	Action      string
	Grants      []Grant
}

// frame relates image pixels to driver points.
type frame struct {
	windowID int64
	bounds   Rect
	w, h     int
}

// Refusals the tools report as they are. Each says what the model can do about it.
var (
	ErrDisabled = errors.New("computer use is turned off on this device; the user can turn it on in Settings > Computer use")
	ErrRemote   = errors.New("computer use only works for someone at this machine, and this session's last message came from another device")
	ErrStopped  = errors.New("stopped by the user")
)

func newController(m *Manager, sessionID string) *Controller {
	return &Controller{m: m, d: m.driver(), sessionID: sessionID, grants: map[string]Grant{}, frames: map[string]frame{}, cancels: map[int]context.CancelFunc{}}
}

// SetRemoteOrigin records where the session's last message came from.
func (c *Controller) SetRemoteOrigin(remote bool) {
	c.mu.Lock()
	c.remote = remote
	c.mu.Unlock()
}

// State returns the current state.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateLocked()
}

func (c *Controller) stateLocked() State {
	keys := slices.Sorted(maps.Keys(c.grants))
	st := State{Controlling: c.controlling, Action: c.action}
	for _, k := range keys {
		st.Grants = append(st.Grants, c.grants[k])
	}
	return st
}

func (c *Controller) notify() {
	c.mu.Lock()
	st := c.stateLocked()
	c.mu.Unlock()
	c.m.stateChanged(c.sessionID, st)
}

// allowed makes the checks every call starts with, reads included.
func (c *Controller) allowed() error {
	if !c.d.Info().Supported {
		return errors.New(c.d.Info().Reason)
	}
	if !c.m.Enabled() {
		return ErrDisabled
	}
	c.mu.Lock()
	remote := c.remote
	c.mu.Unlock()
	if remote {
		return ErrRemote
	}
	return nil
}

// begin registers an action in flight: Stop can cancel it. label is what the indicator shows; an input action also
// turns the indicator on, for the rest of the turn. end must be called.
func (c *Controller) begin(ctx context.Context, label string, input bool) (context.Context, func(), error) {
	if err := c.allowed(); err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.nextCancel++
	id := c.nextCancel
	c.cancels[id] = cancel
	if input {
		c.controlling = true
	}
	c.action = label
	c.mu.Unlock()
	c.notify()
	return ctx, func() {
		c.mu.Lock()
		delete(c.cancels, id)
		if len(c.cancels) == 0 {
			c.action = ""
		}
		c.mu.Unlock()
		cancel()
		c.notify()
	}, nil
}

// Stop is the user's Stop: it cancels what is in flight and revokes every grant. The model has to ask again.
func (c *Controller) Stop() {
	c.mu.Lock()
	for _, cancel := range c.cancels {
		cancel()
	}
	c.grants = map[string]Grant{}
	c.frames = map[string]frame{}
	c.controlling, c.action = false, ""
	c.mu.Unlock()
	c.notify()
}

// EndTurn turns the indicator off: the agent is no longer acting. Grants stay for the session.
func (c *Controller) EndTurn() {
	c.mu.Lock()
	was := c.controlling || c.action != ""
	c.controlling, c.action = false, ""
	c.mu.Unlock()
	if was {
		c.notify()
	}
}

// grantFor finds the grant an action's `app` names, by id or name. The tier comes from Classify, not from the grant's
// record, so a change to the tables or a blocklisted app can never keep an older answer.
func (c *Controller) grantFor(ref string) (Grant, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Grant{}, errors.New("InputValidationError: app is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.grants[ref]
	if !ok {
		for _, x := range c.grants {
			if strings.EqualFold(x.App.Name, ref) || strings.EqualFold(x.App.ID, ref) {
				g, ok = x, true
				break
			}
		}
	}
	if !ok {
		return Grant{}, fmt.Errorf("%s has no access in this session. Ask the user with ComputerAccess first", ref)
	}
	g.Tier = Classify(g.App)
	if g.Tier == TierBlocked {
		return Grant{}, fmt.Errorf("%s is never controllable", g.App.Name)
	}
	return g, nil
}

// tierCheck refuses an action the app's tier does not allow.
func tierCheck(g Grant, a Action) error {
	if g.Tier.Allows(a) {
		return nil
	}
	switch g.Tier {
	case TierRead:
		return fmt.Errorf("%s has read-only access, so %s is refused: it is a browser. You can take screenshots of it; use the Browser tools to work on a web page", g.App.Name, a)
	case TierClick:
		return fmt.Errorf("%s has click-only access, so %s is refused: typing there could run commands. Use Bash or the file tools for that work", g.App.Name, a)
	}
	return fmt.Errorf("%s: %s is refused", g.App.Name, a)
}

// ensureFront makes the granted app frontmost or fails: input is only sent to the app in front.
func (c *Controller) ensureFront(ctx context.Context, g Grant) error {
	f, err := c.d.Frontmost(ctx)
	if err != nil {
		return err
	}
	if f.ID == g.App.ID {
		return nil
	}
	if err := c.d.Activate(ctx, g.App.ID); err != nil {
		return fmt.Errorf("bring %s to the front: %w", g.App.Name, err)
	}
	if f, err = c.d.Frontmost(ctx); err != nil {
		return err
	}
	if f.ID != g.App.ID {
		return fmt.Errorf("%s stayed in front of %s, so nothing was sent", appLabel(f), g.App.Name)
	}
	return nil
}

func appLabel(a App) string {
	if a.Name != "" {
		return a.Name
	}
	if a.ID != "" {
		return a.ID
	}
	return "an unknown app"
}

// input makes the checks an input action starts with: grant, tier, permission, front app.
func (c *Controller) input(ctx context.Context, ref string, a Action, doing string) (context.Context, Grant, func(), error) {
	if err := c.allowed(); err != nil {
		return nil, Grant{}, nil, err
	}
	g, err := c.grantFor(ref)
	if err != nil {
		return nil, Grant{}, nil, err
	}
	if err := tierCheck(g, a); err != nil {
		return nil, Grant{}, nil, err
	}
	ctx, end, err := c.begin(ctx, doing+" "+g.App.Name, true)
	if err != nil {
		return nil, Grant{}, nil, err
	}
	release, err := c.m.acquire(ctx)
	if err != nil {
		end()
		return nil, Grant{}, nil, err
	}
	done := func() { release(); end() }
	// A Stop may have come since the first look, while waiting for the input.
	if g, err = c.grantFor(ref); err != nil {
		done()
		return nil, Grant{}, nil, err
	}
	perms, err := c.d.Permissions(ctx)
	if err == nil {
		if msg := perms.Missing(PermAccessibility); msg != "" {
			err = errors.New(msg)
		}
	}
	if err == nil {
		err = c.ensureFront(ctx, g)
	}
	if err != nil {
		done()
		return nil, Grant{}, nil, err
	}
	return ctx, g, done, nil
}

// after reports where the focus went: the model should know, and the next action is checked against the app it names.
func (c *Controller) after(ctx context.Context, g Grant) string {
	f, err := c.d.Frontmost(ctx)
	if err != nil || f.ID == g.App.ID || f.ID == "" {
		return ""
	}
	return fmt.Sprintf(" %s is now in front.", appLabel(f))
}

// pointFor turns image coordinates of the app's last screenshot into driver points, and checks that the point is on
// that app, not on what covers it now.
func (c *Controller) pointFor(ctx context.Context, g Grant, x, y float64) (Point, error) {
	c.mu.Lock()
	fr, ok := c.frames[g.App.ID]
	c.mu.Unlock()
	if !ok {
		return Point{}, fmt.Errorf("no screenshot of %s yet: coordinates refer to the last ComputerScreenshot of the app", g.App.Name)
	}
	if x < 0 || y < 0 || x >= float64(fr.w) || y >= float64(fr.h) {
		return Point{}, fmt.Errorf("(%.0f, %.0f) is outside the last screenshot of %s, which is %d×%d", x, y, g.App.Name, fr.w, fr.h)
	}
	bounds := fr.bounds
	if c.d.Info().Capabilities.Windows {
		wins, err := c.d.Windows(ctx, g.App.ID)
		if err != nil {
			return Point{}, err
		}
		i := slices.IndexFunc(wins, func(w Window) bool { return w.ID == fr.windowID })
		if i < 0 {
			return Point{}, fmt.Errorf("the window of the last screenshot of %s is gone. Take a new screenshot", g.App.Name)
		}
		if b := wins[i].Bounds; b.W != fr.bounds.W || b.H != fr.bounds.H {
			return Point{}, fmt.Errorf("the window of %s changed size since the last screenshot. Take a new screenshot", g.App.Name)
		} else {
			bounds = b
		}
	}
	p := Point{X: bounds.X + x*bounds.W/float64(fr.w), Y: bounds.Y + y*bounds.H/float64(fr.h)}
	if c.d.Info().Capabilities.HitTest {
		a, err := c.d.AppAt(ctx, p)
		if err != nil {
			return Point{}, err
		}
		if a.ID != g.App.ID {
			return Point{}, fmt.Errorf("%s covers that point, not %s, so nothing was sent", appLabel(a), g.App.Name)
		}
	}
	return p, nil
}

// ClickRequest is a ComputerClick in image coordinates.
type ClickRequest struct {
	App       string
	X, Y      float64
	Button    Button
	Count     int
	Modifiers []string
}

// Click clicks in a granted app and returns a note for the model.
func (c *Controller) Click(ctx context.Context, r ClickRequest) (string, error) {
	if r.Button == "" {
		r.Button = ButtonLeft
	}
	if r.Count == 0 {
		r.Count = 1
	}
	caps := c.d.Info().Capabilities
	action := ActionClick
	switch {
	case r.Button != ButtonLeft && r.Button != ButtonRight:
		return "", fmt.Errorf("InputValidationError: button must be left or right")
	case r.Count < 1 || r.Count > 3:
		return "", fmt.Errorf("InputValidationError: count must be 1, 2 or 3")
	case r.Button == ButtonRight && !caps.RightClick, r.Count > 1 && !caps.DoubleClick, len(r.Modifiers) > 0 && !caps.Modifiers:
		return "", ErrNotSupported
	case r.Button == ButtonRight:
		action = ActionRightClick
	case len(r.Modifiers) > 0:
		action = ActionModClick
	}
	mods := make([]string, 0, len(r.Modifiers))
	for _, m := range r.Modifiers {
		n, ok := modifierNames[strings.ToLower(m)]
		if !ok {
			return "", fmt.Errorf("InputValidationError: unknown modifier %q", m)
		}
		mods = append(mods, n)
	}
	ctx, g, done, err := c.input(ctx, r.App, action, "Clicking in")
	if err != nil {
		return "", err
	}
	defer done()
	p, err := c.pointFor(ctx, g, r.X, r.Y)
	if err != nil {
		return "", err
	}
	if err := c.d.Click(ctx, Click{Point: p, Button: r.Button, Count: r.Count, Modifiers: mods, Expect: g.App.ID}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Clicked in %s at (%.0f, %.0f).%s", g.App.Name, r.X, r.Y, c.after(ctx, g)), nil
}

// DragRequest is a ComputerDrag in image coordinates.
type DragRequest struct {
	App                string
	FromX, FromY, X, Y float64
}

// Drag drags within a granted app; both ends must be on it.
func (c *Controller) Drag(ctx context.Context, r DragRequest) (string, error) {
	if !c.d.Info().Capabilities.Drag {
		return "", ErrNotSupported
	}
	ctx, g, done, err := c.input(ctx, r.App, ActionDrag, "Dragging in")
	if err != nil {
		return "", err
	}
	defer done()
	from, err := c.pointFor(ctx, g, r.FromX, r.FromY)
	if err != nil {
		return "", err
	}
	to, err := c.pointFor(ctx, g, r.X, r.Y)
	if err != nil {
		return "", err
	}
	if err := c.d.Drag(ctx, Drag{From: from, To: to, Expect: g.App.ID}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Dragged in %s from (%.0f, %.0f) to (%.0f, %.0f).%s", g.App.Name, r.FromX, r.FromY, r.X, r.Y, c.after(ctx, g)), nil
}

// ScrollRequest is a ComputerScroll: at a point, or the middle of the last screenshot.
type ScrollRequest struct {
	App    string
	X, Y   *float64
	DX, DY int
}

// Scroll scrolls in a granted app.
func (c *Controller) Scroll(ctx context.Context, r ScrollRequest) (string, error) {
	if !c.d.Info().Capabilities.Scroll {
		return "", ErrNotSupported
	}
	if r.DX == 0 && r.DY == 0 {
		return "", errors.New("InputValidationError: give dx or dy")
	}
	ctx, g, done, err := c.input(ctx, r.App, ActionScroll, "Scrolling in")
	if err != nil {
		return "", err
	}
	defer done()
	c.mu.Lock()
	fr, ok := c.frames[g.App.ID]
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("no screenshot of %s yet: coordinates refer to the last ComputerScreenshot of the app", g.App.Name)
	}
	x, y := float64(fr.w)/2, float64(fr.h)/2
	if r.X != nil && r.Y != nil {
		x, y = *r.X, *r.Y
	}
	p, err := c.pointFor(ctx, g, x, y)
	if err != nil {
		return "", err
	}
	if err := c.d.Scroll(ctx, Scroll{Point: p, DX: r.DX, DY: r.DY, Expect: g.App.ID}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Scrolled in %s.%s", g.App.Name, c.after(ctx, g)), nil
}

// checkFocus refuses when the keyboard focus is in a secure field, or in another app than the one named: the frontmost
// app can differ from the one holding the focus (a system search panel takes it without coming forward).
func (c *Controller) checkFocus(ctx context.Context, g Grant) error {
	if !c.d.Info().Capabilities.SecureFields {
		return nil
	}
	f, err := c.d.FocusedElement(ctx)
	if err != nil {
		return fmt.Errorf("cannot tell what has the keyboard focus, so nothing was sent: %w", err)
	}
	if f.Secure {
		return ErrSecureField
	}
	if f.AppID != "" && f.AppID != g.App.ID {
		return fmt.Errorf("the keyboard focus is in %s, not %s, so nothing was sent", f.AppID, g.App.Name)
	}
	return nil
}

// maxTypeLen bounds one ComputerType.
const maxTypeLen = 10000

// Type types into the focused element of a granted app.
func (c *Controller) Type(ctx context.Context, app, text string) (string, error) {
	if !c.d.Info().Capabilities.Type {
		return "", ErrNotSupported
	}
	if text == "" {
		return "", errors.New("InputValidationError: text is required")
	}
	if len(text) > maxTypeLen {
		return "", fmt.Errorf("InputValidationError: text is longer than %d bytes", maxTypeLen)
	}
	ctx, g, done, err := c.input(ctx, app, ActionType, "Typing in")
	if err != nil {
		return "", err
	}
	defer done()
	if err := c.checkFocus(ctx, g); err != nil {
		return "", err
	}
	if err := c.d.Type(ctx, TypeText{Text: text, Expect: g.App.ID, RefuseSecure: true}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Typed %d characters in %s.%s", len([]rune(text)), g.App.Name, c.after(ctx, g)), nil
}

// Key presses key combinations, space separated, in a granted app.
func (c *Controller) Key(ctx context.Context, app, keys string, repeat int) (string, error) {
	if !c.d.Info().Capabilities.Key {
		return "", ErrNotSupported
	}
	if repeat == 0 {
		repeat = 1
	}
	if repeat < 1 || repeat > 100 {
		return "", errors.New("InputValidationError: repeat must be between 1 and 100")
	}
	fields := strings.Fields(keys)
	if len(fields) == 0 {
		return "", errors.New("InputValidationError: keys is required")
	}
	combos := make([]string, len(fields))
	for i, f := range fields {
		combo, err := ParseCombo(f)
		if err != nil {
			return "", fmt.Errorf("InputValidationError: %v", err)
		}
		if why := BlockedCombo(combo); why != "" {
			return "", fmt.Errorf("%s is refused: it %s, outside the app. Use ComputerActivate to change app", combo, why)
		}
		combos[i] = combo
	}
	ctx, g, done, err := c.input(ctx, app, ActionKey, "Pressing keys in")
	if err != nil {
		return "", err
	}
	defer done()
	for range repeat {
		for _, combo := range combos {
			// Each press is checked: the one before may have moved the focus.
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if err := c.ensureFront(ctx, g); err != nil {
				return "", err
			}
			secureOK := harmlessInSecureField[combo]
			if err := c.checkFocus(ctx, g); err != nil && !(secureOK && errors.Is(err, ErrSecureField)) {
				return "", err
			}
			if err := c.d.Key(ctx, KeyPress{Combo: combo, Repeat: 1, Expect: g.App.ID, RefuseSecure: !secureOK}); err != nil {
				return "", err
			}
		}
	}
	return fmt.Sprintf("Pressed %s in %s.%s", strings.Join(combos, " "), g.App.Name, c.after(ctx, g)), nil
}

// Menu runs an item of a granted app's menu bar.
func (c *Controller) Menu(ctx context.Context, app string, path []string) (string, error) {
	if !c.d.Info().Capabilities.Menu {
		return "", ErrNotSupported
	}
	if len(path) < 2 {
		return "", errors.New("InputValidationError: path needs a menu and an item")
	}
	// The first menu of the bar belongs to the system, with its settings and log out.
	if p := strings.TrimSpace(path[0]); p == "" || strings.EqualFold(p, "Apple") || p == "" {
		return "", errors.New("the system menu is refused")
	}
	ctx, g, done, err := c.input(ctx, app, ActionMenu, "Using the menu of")
	if err != nil {
		return "", err
	}
	defer done()
	if err := c.d.Menu(ctx, g.App.ID, path); err != nil {
		return "", err
	}
	return fmt.Sprintf("Chose %s in %s.%s", strings.Join(path, " > "), g.App.Name, c.after(ctx, g)), nil
}

// Activate brings a granted app, or one of its windows, to the front. Any tier can: it is how the model gets to look.
func (c *Controller) Activate(ctx context.Context, app string, windowID int64) (string, error) {
	if err := c.allowed(); err != nil {
		return "", err
	}
	g, err := c.grantFor(app)
	if err != nil {
		return "", err
	}
	ctx, end, err := c.begin(ctx, "Opening "+g.App.Name, true)
	if err != nil {
		return "", err
	}
	defer end()
	release, err := c.m.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if g, err = c.grantFor(app); err != nil {
		return "", err
	}
	if err := c.d.Activate(ctx, g.App.ID); err != nil {
		return "", err
	}
	if windowID != 0 {
		if !c.d.Info().Capabilities.Focus {
			return "", ErrNotSupported
		}
		if err := c.ownsWindow(ctx, g, windowID); err != nil {
			return "", err
		}
		if err := c.d.FocusWindow(ctx, g.App.ID, windowID); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s is in front.", g.App.Name), nil
}

func (c *Controller) ownsWindow(ctx context.Context, g Grant, windowID int64) error {
	wins, err := c.d.Windows(ctx, g.App.ID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(wins, func(w Window) bool { return w.ID == windowID }) {
		return fmt.Errorf("window %d is not a visible window of %s", windowID, g.App.Name)
	}
	return nil
}

// Shot is a screenshot ready for the model.
type Shot struct {
	App      App
	JPEG     []byte
	W, H     int
	WindowID int64
	Title    string
	// Others are the app's other visible windows.
	Others []Window
}

// Screenshot captures one window of a granted app. The image holds only that window, and coordinates of later actions
// refer to it. A driver without windows captures the app's whole surface.
func (c *Controller) Screenshot(ctx context.Context, app string, windowID int64) (Shot, error) {
	if !c.d.Info().Capabilities.Screenshot {
		return Shot{}, ErrNotSupported
	}
	if err := c.allowed(); err != nil {
		return Shot{}, err
	}
	g, err := c.grantFor(app)
	if err != nil {
		return Shot{}, err
	}
	ctx, end, err := c.begin(ctx, "Looking at "+g.App.Name, false)
	if err != nil {
		return Shot{}, err
	}
	defer end()
	perms, err := c.d.Permissions(ctx)
	if err != nil {
		return Shot{}, err
	}
	if msg := perms.Missing(PermScreenRecording); msg != "" {
		return Shot{}, errors.New(msg)
	}
	shot := Shot{App: g.App}
	if c.d.Info().Capabilities.Windows {
		wins, err := c.d.Windows(ctx, g.App.ID)
		if err != nil {
			return Shot{}, err
		}
		if len(wins) == 0 {
			return Shot{}, fmt.Errorf("%s has no visible window. ComputerActivate brings it forward", g.App.Name)
		}
		if windowID == 0 {
			windowID = wins[0].ID
		}
		if !slices.ContainsFunc(wins, func(w Window) bool { return w.ID == windowID }) {
			return Shot{}, fmt.Errorf("window %d is not a visible window of %s", windowID, g.App.Name)
		}
		for _, w := range wins {
			if w.ID != windowID {
				shot.Others = append(shot.Others, w)
			}
		}
	}
	img, err := c.d.Screenshot(ctx, ScreenshotRequest{AppID: g.App.ID, WindowID: windowID})
	if err != nil {
		return Shot{}, err
	}
	data, w, h, err := encodeFrame(img.Data, maxImageSide)
	if err != nil {
		return Shot{}, err
	}
	shot.JPEG, shot.W, shot.H, shot.WindowID, shot.Title = data, w, h, img.WindowID, img.Title
	c.mu.Lock()
	// A Stop that came during the capture revoked the grant; the frame must not outlive it.
	_, still := c.grants[g.App.ID]
	if still {
		c.frames[g.App.ID] = frame{windowID: img.WindowID, bounds: img.Bounds, w: w, h: h}
	}
	c.mu.Unlock()
	if !still {
		return Shot{}, ErrStopped
	}
	return shot, nil
}

// FoundElement is an accessibility element, located in the app's last screenshot.
type FoundElement struct {
	Element
	// X, Y, W, H are image coordinates; Located is false for one outside the screenshot.
	X, Y, W, H float64
	Located    bool
}

// Find searches the accessibility tree of a granted app, in the window of its last screenshot.
func (c *Controller) Find(ctx context.Context, app, query, role string, limit int) ([]FoundElement, error) {
	if !c.d.Info().Capabilities.Find {
		return nil, ErrNotSupported
	}
	if err := c.allowed(); err != nil {
		return nil, err
	}
	g, err := c.grantFor(app)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	fr, ok := c.frames[g.App.ID]
	c.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no screenshot of %s yet: positions refer to the last ComputerScreenshot of the app", g.App.Name)
	}
	ctx, end, err := c.begin(ctx, "Reading "+g.App.Name, false)
	if err != nil {
		return nil, err
	}
	defer end()
	perms, err := c.d.Permissions(ctx)
	if err != nil {
		return nil, err
	}
	if msg := perms.Missing(PermAccessibility); msg != "" {
		return nil, errors.New(msg)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	els, err := c.d.Find(ctx, FindQuery{AppID: g.App.ID, WindowID: fr.windowID, Query: query, Role: role, Max: limit})
	if err != nil {
		return nil, err
	}
	out := make([]FoundElement, 0, len(els))
	for _, e := range els {
		if e.Secure {
			e.Value = "" // whatever the driver returned
		}
		f := FoundElement{Element: e}
		if fr.bounds.W > 0 && fr.bounds.H > 0 && fr.bounds.Contains(Point{e.Frame.X + e.Frame.W/2, e.Frame.Y + e.Frame.H/2}) {
			sx, sy := float64(fr.w)/fr.bounds.W, float64(fr.h)/fr.bounds.H
			f.X, f.Y, f.W, f.H = (e.Frame.X-fr.bounds.X)*sx, (e.Frame.Y-fr.bounds.Y)*sy, e.Frame.W*sx, e.Frame.H*sy
			f.Located = true
		}
		out = append(out, f)
	}
	return out, nil
}

// ListedApp is an app as ComputerApps reports it.
type ListedApp struct {
	App
	Tier    Tier
	Granted bool
	Windows []Window
}

// Apps lists the apps of the machine with their tier. Windows are only listed for apps with access.
func (c *Controller) Apps(ctx context.Context, installed bool) ([]ListedApp, Permissions, error) {
	if err := c.allowed(); err != nil {
		return nil, Permissions{}, err
	}
	perms, err := c.d.Permissions(ctx)
	if err != nil {
		return nil, Permissions{}, err
	}
	if perms.Reason != "" {
		return nil, perms, errors.New(perms.Reason)
	}
	apps, err := c.d.Apps(ctx, installed)
	if err != nil {
		return nil, perms, err
	}
	out := make([]ListedApp, 0, len(apps))
	for _, a := range apps {
		l := ListedApp{App: a, Tier: Classify(a)}
		c.mu.Lock()
		_, l.Granted = c.grants[a.ID]
		c.mu.Unlock()
		if l.Granted && a.Running && c.d.Info().Capabilities.Windows {
			if wins, err := c.d.Windows(ctx, a.ID); err == nil {
				l.Windows = wins
			}
		}
		out = append(out, l)
	}
	return out, perms, nil
}

// AccessApp is one app of an access request.
type AccessApp struct {
	App  App
	Tier Tier
}

// AccessRequest is what the user sees in the access card.
type AccessRequest struct {
	ToolUseID string
	Reason    string
	Apps      []AccessApp
	// Missing are permissions of the OS still to grant.
	Missing []Permission
}

// AccessAsker is the frontend side of the access card, implemented by the session's Interactor.
type AccessAsker interface {
	// AskComputerAccess blocks until the user answers and returns the ids of the apps approved.
	AskComputerAccess(ctx context.Context, req AccessRequest) ([]string, error)
}

// AccessResult says how a ComputerAccess went.
type AccessResult struct {
	Granted []Grant
	Denied  []App
	// Blocked apps are never controllable and were not offered.
	Blocked []App
	// Unknown are names that match no app.
	Unknown []string
	Missing []Permission
}

// RequestAccess asks the user about apps by id or name. Apps already granted are not asked about again.
func (c *Controller) RequestAccess(ctx context.Context, asker AccessAsker, toolUseID, reason string, refs []string) (AccessResult, error) {
	var res AccessResult
	if err := c.allowed(); err != nil {
		return res, err
	}
	if asker == nil {
		return res, errors.New("no one can answer an access request in this session, so computer use is refused")
	}
	perms, err := c.d.Permissions(ctx)
	if err != nil {
		return res, err
	}
	if perms.Reason != "" {
		return res, errors.New(perms.Reason)
	}
	apps, err := c.d.Apps(ctx, true)
	if err != nil {
		return res, err
	}
	var ask []AccessApp
	for _, ref := range refs {
		a, ok := resolveApp(apps, ref)
		if !ok {
			res.Unknown = append(res.Unknown, ref)
			continue
		}
		if Classify(a) == TierBlocked {
			res.Blocked = append(res.Blocked, a)
			continue
		}
		c.mu.Lock()
		g, has := c.grants[a.ID]
		c.mu.Unlock()
		if has {
			res.Granted = append(res.Granted, g)
			continue
		}
		if !slices.ContainsFunc(ask, func(x AccessApp) bool { return x.App.ID == a.ID }) {
			ask = append(ask, AccessApp{App: a, Tier: Classify(a)})
		}
	}
	for _, p := range perms.Items {
		if !p.Granted {
			res.Missing = append(res.Missing, p)
		}
	}
	if len(ask) == 0 {
		return res, nil
	}
	approved, err := asker.AskComputerAccess(ctx, AccessRequest{ToolUseID: toolUseID, Reason: reason, Apps: ask, Missing: res.Missing})
	if err != nil {
		return res, err
	}
	// Only what was asked can be approved, and at its own tier, whatever the frontend sent.
	c.mu.Lock()
	for _, x := range ask {
		if slices.Contains(approved, x.App.ID) {
			g := Grant{App: x.App, Tier: x.Tier}
			c.grants[x.App.ID] = g
			res.Granted = append(res.Granted, g)
		} else {
			res.Denied = append(res.Denied, x.App)
		}
	}
	c.mu.Unlock()
	c.notify()
	return res, nil
}

func resolveApp(apps []App, ref string) (App, bool) {
	ref = strings.TrimSpace(ref)
	for _, a := range apps {
		if a.ID == ref {
			return a, true
		}
	}
	for _, a := range apps {
		if strings.EqualFold(a.Name, ref) || strings.EqualFold(a.ID, ref) {
			return a, true
		}
	}
	return App{}, false
}
