package computeruse

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Recording a demo: the user shows a workflow once, the client keeps it as steps with screenshots, and the agent drafts
// a skill from it (docs/design/demo-recording.md). Only the user starts and stops it; it needs computer use turned on and
// watches only the apps the user picked, which the active profile must not block. Keystrokes in a password field are
// dropped by the helper, and the secure fields of every screenshot are painted black before it is stored. Recordings
// live under Dir/../demo-recordings, on this machine only.

// Watcher is implemented by drivers that can watch the user's input for a recording.
type Watcher interface {
	// Watch starts watching; label is what the indicator on the machine says. The feed ends when Stop is called or
	// the user stops it there (a RawEvent of type "stop").
	Watch(label string) (Feed, error)
}

// Feed is a running watch.
type Feed interface {
	Events() <-chan RawEvent
	Stop()
}

// SecureFieldLister is implemented by drivers that can find the secure text fields of a window, in driver points.
type SecureFieldLister interface {
	SecureFields(ctx context.Context, appID string, windowID int64) ([]Rect, error)
}

// RawEvent is one thing the user did, as the driver reports it.
type RawEvent struct {
	// Type is click, scroll, key, secure_key, activate, started, stop or error.
	Type      string
	X, Y      float64
	Button    string
	Count     int
	Modifiers []string
	App       App
	Element   RawElement
	Window    *Window
	// Key is the key's name ("a", "enter"); Text what a plain key types ("" for a combination).
	Key, Text string
	DX, DY    int
	Reason    string
	Error     string
	At        time.Time
}

// RawElement is the accessibility element under a click.
type RawElement struct {
	Role, Title, Description string
	Secure                   bool
}

// Step kinds.
const (
	StepClick      = "click"
	StepType       = "type"
	StepKey        = "key"
	StepScroll     = "scroll"
	StepActivate   = "activate"
	StepSecureType = "secure_type"
	StepOtherApp   = "other_app"
)

// Step is one step of a recording.
type Step struct {
	N    int    `json:"n"`
	Kind string `json:"kind"`
	// AppID and AppName are empty for other_app.
	AppID   string `json:"app_id,omitempty"`
	AppName string `json:"app_name,omitempty"`
	// Tier is what the active profile gave the app when it was recorded; Allowed whether that tier allows the step.
	Tier    string `json:"tier,omitempty"`
	Allowed bool   `json:"allowed"`
	// Element describes what was clicked ("button \"Save\"").
	Element string `json:"element,omitempty"`
	// X and Y are points from the window's top left corner.
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Window string  `json:"window,omitempty"`
	Button string  `json:"button,omitempty"`
	Count  int     `json:"count,omitempty"`
	// Text is what was typed (type); Keys the combination (key).
	Text   string `json:"text,omitempty"`
	Keys   string `json:"keys,omitempty"`
	DX     int    `json:"dx,omitempty"`
	DY     int    `json:"dy,omitempty"`
	Repeat int    `json:"repeat,omitempty"`
	// Screenshot is the file name of the window after the step, in the recording's folder ("" when none was taken).
	Screenshot string    `json:"screenshot,omitempty"`
	At         time.Time `json:"at"`

	windowID int64
	bounds   Rect
}

// RecordingFile is a finished recording, as kept in recording.json.
type RecordingFile struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended"`
	// Apps are the apps the user picked.
	Apps []App `json:"apps"`
	// Profile is the security profile active during the recording.
	Profile string `json:"profile"`
	Steps   []Step `json:"steps"`
	// StopReason is button, hotkey, frontend, limit or error.
	StopReason string `json:"stop_reason"`
	// Dir is where it is kept (not stored).
	Dir string `json:"-"`
}

// RecordingState is what the frontends show while a session records.
type RecordingState struct {
	Recording bool
	ID        string
	Started   time.Time
	Steps     int
	Apps      []App
	// Hotkey stops the recording from anywhere.
	Hotkey string
	// Last says what the last step was.
	Last string
}

// RecordingHotkey stops a recording from any app.
const RecordingHotkey = "ctrl+alt+cmd+."

// Limits of one recording: past them it stops by itself.
const (
	recordingMaxSteps    = 300
	recordingMaxDuration = 30 * time.Minute
	recordingShotDelay   = 600 * time.Millisecond
	recordingShotSide    = 1280
	recordingsKept       = 10
)

// Errors of recordings.
var (
	ErrRecordingBusy = errors.New("a recording is already running on this machine")
	ErrNoRecording   = errors.New("this session is not recording")
)

type recording struct {
	file    RecordingFile
	allowed map[string]App
	profile Profile
	feed    Feed
	stopped chan struct{}
	done    chan struct{}
	reason  string
	once    sync.Once
	// discard deletes it instead of keeping it.
	discard bool

	mu    sync.Mutex
	steps []Step
	last  string
}

// RecordingsDir is where recordings are kept: next to the computer use settings, on this machine only.
func (m *Manager) RecordingsDir() string {
	if m.Dir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(m.Dir), "demo-recordings")
}

func newRecordingID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "rec_" + time.Now().UTC().Format("20060102-150405") + "_" + hex.EncodeToString(b[:])
}

// RecordingApps lists the running apps the user can pick for a recording, with their tier. Blocked ones are listed too,
// so the card can say why they cannot be picked.
func (m *Manager) RecordingApps(ctx context.Context) ([]ListedApp, error) {
	if err := m.recordingAllowed(ctx); err != nil {
		return nil, err
	}
	apps, err := m.driver().Apps(ctx, false)
	if err != nil {
		return nil, err
	}
	profile := m.Profile()
	out := make([]ListedApp, 0, len(apps))
	for _, a := range apps {
		if isHarness(a) {
			continue
		}
		out = append(out, ListedApp{App: a, Tier: profile.TierFor(a)})
	}
	return out, nil
}

func (m *Manager) recordingAllowed(ctx context.Context) error {
	d := m.driver()
	if !d.Info().Supported {
		return errors.New(d.Info().Reason)
	}
	if !m.Enabled() {
		return ErrDisabled
	}
	if _, ok := d.(Watcher); !ok {
		return errors.New("this platform's driver cannot record a demo")
	}
	perms, err := d.Permissions(ctx)
	if err != nil {
		return err
	}
	if perms.Reason != "" {
		return errors.New(perms.Reason)
	}
	for _, id := range []string{PermAccessibility, PermScreenRecording} {
		if msg := perms.Missing(id); msg != "" {
			return errors.New(msg)
		}
	}
	return nil
}

// StartRecording starts recording for a session, watching the apps given by id. Only the user reaches this.
func (m *Manager) StartRecording(ctx context.Context, sessionID string, appIDs []string) (RecordingState, error) {
	if sessionID == "" {
		return RecordingState{}, errors.New("a recording needs a session")
	}
	if len(appIDs) == 0 {
		return RecordingState{}, errors.New("pick at least one app to record")
	}
	if err := m.recordingAllowed(ctx); err != nil {
		return RecordingState{}, err
	}
	if m.RecordingsDir() == "" {
		return RecordingState{}, errors.New("recordings need a data folder")
	}
	apps, err := m.driver().Apps(ctx, true)
	if err != nil {
		return RecordingState{}, err
	}
	profile := m.Profile()
	allowed := map[string]App{}
	var picked []App
	for _, id := range appIDs {
		a, ok := resolveApp(apps, id)
		if !ok {
			return RecordingState{}, fmt.Errorf("no app %q on this machine", id)
		}
		if isHarness(a) || profile.TierFor(a) == TierBlocked {
			return RecordingState{}, fmt.Errorf("%s is blocked in the current security profile and cannot be recorded", a.Name)
		}
		if _, dup := allowed[a.ID]; !dup {
			allowed[a.ID] = a
			picked = append(picked, a)
		}
	}
	m.mu.Lock()
	if m.rec != nil {
		m.mu.Unlock()
		return RecordingState{}, ErrRecordingBusy
	}
	r := &recording{
		file:    RecordingFile{ID: newRecordingID(), SessionID: sessionID, Started: time.Now(), Apps: picked, Profile: profile.Name},
		allowed: allowed, profile: profile, stopped: make(chan struct{}), done: make(chan struct{}),
	}
	m.rec = r
	m.mu.Unlock()
	feed, err := m.driver().(Watcher).Watch("Recording a demo. Press ⌃⌥⌘. or Stop to end it")
	if err != nil {
		m.mu.Lock()
		m.rec = nil
		m.mu.Unlock()
		return RecordingState{}, err
	}
	r.feed = feed
	go m.runRecording(r)
	st := m.recordingState(r)
	m.recordingChanged(sessionID, st)
	return st, nil
}

// RecordingStateOf returns a session's recording state.
func (m *Manager) RecordingStateOf(sessionID string) RecordingState {
	m.mu.Lock()
	r := m.rec
	m.mu.Unlock()
	if r == nil || r.file.SessionID != sessionID {
		return RecordingState{Hotkey: RecordingHotkey}
	}
	return m.recordingState(r)
}

func (m *Manager) recordingState(r *recording) RecordingState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RecordingState{Recording: true, ID: r.file.ID, Started: r.file.Started, Steps: len(r.steps), Apps: r.file.Apps, Hotkey: RecordingHotkey, Last: r.last}
}

func (m *Manager) recordingChanged(sessionID string, st RecordingState) {
	if m.OnRecording != nil {
		m.OnRecording(sessionID, st)
	}
}

// StopRecording ends a session's recording and returns it (kept), or deletes it with discard. It waits for the last
// screenshot.
func (m *Manager) StopRecording(sessionID string, discard bool) (RecordingFile, error) {
	m.mu.Lock()
	r := m.rec
	m.mu.Unlock()
	if r == nil || r.file.SessionID != sessionID {
		return RecordingFile{}, ErrNoRecording
	}
	r.mu.Lock()
	r.discard = r.discard || discard
	r.mu.Unlock()
	r.stop("frontend")
	<-r.done
	if discard {
		return RecordingFile{ID: r.file.ID, SessionID: sessionID, StopReason: "discarded"}, nil
	}
	return r.file, nil
}

// StopSessionRecording discards a session's recording (the session closed).
func (m *Manager) StopSessionRecording(sessionID string) {
	_, _ = m.StopRecording(sessionID, true)
}

func (r *recording) stop(reason string) {
	r.once.Do(func() {
		r.reason = reason
		close(r.stopped)
	})
}

// runRecording turns the feed into steps until the recording stops, then keeps or deletes it.
func (m *Manager) runRecording(r *recording) {
	defer close(r.done)
	dir := filepath.Join(m.RecordingsDir(), r.file.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.log().Warn("demo recording: cannot create its folder", "error", err)
		r.stop("error")
	}
	r.file.Dir = dir
	limit := time.NewTimer(recordingMaxDuration)
	defer limit.Stop()
	var shot *time.Timer
	var shotC <-chan time.Time
	pending := -1 // the step whose screenshot is due
	capture := func() {
		if pending >= 0 {
			m.captureStep(r, pending)
			pending = -1
		}
		if shot != nil {
			shot.Stop()
			shot, shotC = nil, nil
		}
	}
	events := r.feed.Events()
loop:
	for {
		select {
		case <-r.stopped:
			break loop
		case <-limit.C:
			r.stop("limit")
		case <-shotC:
			capture()
		case ev, ok := <-events:
			if !ok {
				r.stop("error")
				continue
			}
			switch ev.Type {
			case "stop":
				r.stop(ev.Reason)
				continue
			case "error":
				m.log().Warn("demo recording: the helper stopped", "error", ev.Error)
				r.stop("error")
				continue
			case "started":
				continue
			}
			// What came before is done: its screenshot is due now.
			idx, typing := m.addEvent(r, ev)
			if idx < 0 {
				continue
			}
			if pending >= 0 && pending != idx {
				capture()
			}
			if r.stepCount() >= recordingMaxSteps {
				r.stop("limit")
			}
			pending = idx
			if shot != nil {
				shot.Stop()
			}
			delay := recordingShotDelay
			if typing {
				delay *= 2 // after the last key of a run, not each one
			}
			shot = time.NewTimer(delay)
			shotC = shot.C
			m.recordingChanged(r.file.SessionID, m.recordingState(r))
		}
	}
	r.feed.Stop()
	capture()
	r.mu.Lock()
	r.file.Steps = slices.Clone(r.steps)
	r.file.StopReason = r.reason
	discard := r.discard
	r.mu.Unlock()
	r.file.Ended = time.Now()
	m.mu.Lock()
	if m.rec == r {
		m.rec = nil
	}
	m.mu.Unlock()
	if discard {
		_ = os.RemoveAll(dir)
	} else if err := writeRecording(dir, r.file); err != nil {
		m.log().Warn("demo recording: cannot save it", "error", err)
	} else {
		m.pruneRecordings(r.file.ID)
	}
	m.recordingChanged(r.file.SessionID, RecordingState{Hotkey: RecordingHotkey})
	// A recording the user stopped from the machine or a frontend goes to the agent; a discarded one does not.
	if !discard && m.OnRecorded != nil {
		m.OnRecorded(r.file.SessionID, r.file)
	}
}

func (r *recording) stepCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.steps)
}

func writeRecording(dir string, f RecordingFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "recording.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "recording.json"))
}

// pruneRecordings keeps the newest recordings (ids sort by time).
func (m *Manager) pruneRecordings(keep string) {
	root := m.RecordingsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "rec_") {
			ids = append(ids, e.Name())
		}
	}
	slices.Sort(ids)
	for len(ids) > recordingsKept {
		if ids[0] != keep {
			_ = os.RemoveAll(filepath.Join(root, ids[0]))
		}
		ids = ids[1:]
	}
}

// validRecordingID keeps an id from naming anything outside the recordings folder.
func validRecordingID(id string) bool {
	if !strings.HasPrefix(id, "rec_") || len(id) > 80 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// LoadRecording reads a kept recording.
func (m *Manager) LoadRecording(id string) (RecordingFile, error) {
	if !validRecordingID(id) || m.RecordingsDir() == "" {
		return RecordingFile{}, fmt.Errorf("no recording %q", id)
	}
	dir := filepath.Join(m.RecordingsDir(), id)
	data, err := os.ReadFile(filepath.Join(dir, "recording.json"))
	if err != nil {
		return RecordingFile{}, fmt.Errorf("no recording %q on this machine", id)
	}
	var f RecordingFile
	if err := json.Unmarshal(data, &f); err != nil {
		return RecordingFile{}, fmt.Errorf("recording %q: %w", id, err)
	}
	f.Dir = dir
	return f, nil
}

// DeleteRecording deletes a kept recording and its screenshots.
func (m *Manager) DeleteRecording(id string) error {
	if !validRecordingID(id) || m.RecordingsDir() == "" {
		return fmt.Errorf("no recording %q", id)
	}
	return os.RemoveAll(filepath.Join(m.RecordingsDir(), id))
}

// actionOf is what a step would need from the tier if the agent did it with computer use.
func actionOf(kind string, s Step) Action {
	switch kind {
	case StepClick:
		if s.Button == "right" {
			return ActionRightClick
		}
		return ActionClick
	case StepType:
		return ActionType
	case StepKey:
		return ActionKey
	case StepScroll:
		return ActionScroll
	}
	return ActionActivate
}

func describeElement(e RawElement) string {
	role := strings.TrimPrefix(e.Role, "AX")
	if e.Secure {
		return "password field"
	}
	var name string
	for _, s := range []string{e.Title, e.Description} {
		if s = strings.TrimSpace(s); s != "" {
			name = s
			break
		}
	}
	switch {
	case role == "" && name == "":
		return ""
	case name == "":
		return strings.ToLower(role)
	case role == "":
		return fmt.Sprintf("%q", name)
	}
	return fmt.Sprintf("%s %q", strings.ToLower(role), name)
}

// addEvent records an event and returns the index of the step it made or extended (-1: none), and whether it is typing.
func (m *Manager) addEvent(r *recording, ev RawEvent) (int, bool) {
	if ev.App.ID == "" || isHarness(ev.App) {
		return -1, false // the indicator's own panel, or something without an app
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	last := func() *Step {
		if len(r.steps) == 0 {
			return nil
		}
		return &r.steps[len(r.steps)-1]
	}
	app, ok := r.allowed[ev.App.ID]
	if !ok {
		// Not one the user picked: only that something happened elsewhere, never what.
		if l := last(); l != nil && l.Kind == StepOtherApp {
			return -1, false
		}
		r.steps = append(r.steps, Step{N: len(r.steps) + 1, Kind: StepOtherApp, At: ev.At})
		r.last = "Used another app (not recorded)"
		return -1, false
	}
	// The tier as the profile gives it now: a profile changed during the recording counts from then on.
	tier := m.Profile().TierFor(app)
	if tier == TierBlocked {
		if l := last(); l != nil && l.Kind == StepOtherApp {
			return -1, false
		}
		r.steps = append(r.steps, Step{N: len(r.steps) + 1, Kind: StepOtherApp, At: ev.At})
		return -1, false
	}
	s := Step{AppID: app.ID, AppName: app.Name, Tier: tier.String(), At: ev.At}
	if ev.Window != nil {
		s.windowID, s.bounds, s.Window = ev.Window.ID, ev.Window.Bounds, ev.Window.Title
	}
	typing := false
	switch ev.Type {
	case "click":
		s.Kind, s.Button, s.Count = StepClick, ev.Button, max(ev.Count, 1)
		s.Element = describeElement(ev.Element)
		if ev.Window != nil {
			s.X, s.Y = ev.X-ev.Window.Bounds.X, ev.Y-ev.Window.Bounds.Y
		}
		if len(ev.Modifiers) > 0 {
			s.Keys = strings.Join(ev.Modifiers, "+")
		}
		// A double click arrives as a second click with count 2: it replaces the first.
		if l := last(); l != nil && l.Kind == StepClick && l.AppID == s.AppID && s.Count > 1 && ev.At.Sub(l.At) < time.Second {
			l.Count = s.Count
			l.At = s.At
			r.last = fmt.Sprintf("Double click in %s", app.Name)
			return len(r.steps) - 1, false
		}
	case "scroll":
		if l := last(); l != nil && l.Kind == StepScroll && l.AppID == s.AppID {
			l.DX += ev.DX
			l.DY += ev.DY
			return len(r.steps) - 1, false
		}
		s.Kind, s.DX, s.DY = StepScroll, ev.DX, ev.DY
	case "secure_key":
		if l := last(); l != nil && l.Kind == StepSecureType && l.AppID == s.AppID {
			return len(r.steps) - 1, true
		}
		s.Kind = StepSecureType
		typing = true
	case "key":
		if ev.Text != "" {
			typing = true
			if l := last(); l != nil && l.Kind == StepType && l.AppID == s.AppID {
				l.Text += ev.Text
				l.At = ev.At
				return len(r.steps) - 1, true
			}
			s.Kind, s.Text = StepType, ev.Text
			break
		}
		combo := strings.Join(append(slices.Clone(ev.Modifiers), ev.Key), "+")
		if ev.Key == "" {
			return -1, false
		}
		if l := last(); l != nil && l.Kind == StepKey && l.AppID == s.AppID && l.Keys == combo {
			l.Repeat = max(l.Repeat, 1) + 1
			return len(r.steps) - 1, false
		}
		s.Kind, s.Keys = StepKey, combo
	case "activate":
		if l := last(); l != nil && l.Kind == StepActivate && l.AppID == s.AppID {
			return -1, false
		}
		s.Kind = StepActivate
	default:
		return -1, false
	}
	s.Allowed = tier.Allows(actionOf(s.Kind, s))
	s.N = len(r.steps) + 1
	r.steps = append(r.steps, s)
	r.last = s.Summary()
	return len(r.steps) - 1, typing
}

// Summary says what a step was, in one line, for people and the model.
func (s Step) Summary() string {
	switch s.Kind {
	case StepClick:
		what := "Clicked"
		switch {
		case s.Button == "right":
			what = "Right-clicked"
		case s.Count == 2:
			what = "Double-clicked"
		case s.Count > 2:
			what = "Triple-clicked"
		}
		if s.Keys != "" {
			what = s.Keys + "-" + strings.ToLower(what[:1]) + what[1:]
		}
		target := s.Element
		if target == "" {
			target = fmt.Sprintf("at (%.0f, %.0f) of the window", s.X, s.Y)
		}
		return fmt.Sprintf("%s %s in %s", what, target, s.AppName)
	case StepType:
		return fmt.Sprintf("Typed %q in %s", s.Text, s.AppName)
	case StepKey:
		if s.Repeat > 1 {
			return fmt.Sprintf("Pressed %s ×%d in %s", s.Keys, s.Repeat, s.AppName)
		}
		return fmt.Sprintf("Pressed %s in %s", s.Keys, s.AppName)
	case StepScroll:
		dir := "down"
		if s.DY < 0 {
			dir = "up"
		}
		if s.DY == 0 && s.DX != 0 {
			dir = "right"
			if s.DX < 0 {
				dir = "left"
			}
		}
		return fmt.Sprintf("Scrolled %s in %s", dir, s.AppName)
	case StepActivate:
		return "Switched to " + s.AppName
	case StepSecureType:
		return "Typed into a password field in " + s.AppName + " (not recorded)"
	case StepOtherApp:
		return "Used another app (not recorded)"
	}
	return s.Kind
}

// captureStep stores the screenshot of a step's window, with its secure fields painted black. A failure only means the
// step has no screenshot.
func (m *Manager) captureStep(r *recording, idx int) {
	r.mu.Lock()
	if idx < 0 || idx >= len(r.steps) {
		r.mu.Unlock()
		return
	}
	s := r.steps[idx]
	dir := r.file.Dir
	r.mu.Unlock()
	if s.AppID == "" || dir == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := m.driver()
	img, err := d.Screenshot(ctx, ScreenshotRequest{AppID: s.AppID, WindowID: s.windowID})
	if err != nil {
		m.log().Debug("demo recording: no screenshot", "step", s.N, "error", err)
		return
	}
	var secure []Rect
	if l, ok := d.(SecureFieldLister); ok {
		if secure, err = l.SecureFields(ctx, s.AppID, img.WindowID); err != nil {
			// Not knowing where the password fields are is a reason not to keep the image.
			m.log().Debug("demo recording: no secure field list, screenshot dropped", "step", s.N, "error", err)
			return
		}
	} else if d.Info().Capabilities.SecureFields {
		return
	}
	data, err := scrubFrame(img, secure, recordingShotSide)
	if err != nil {
		return
	}
	name := fmt.Sprintf("step-%03d.jpg", s.N)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return
	}
	r.mu.Lock()
	if idx < len(r.steps) && r.steps[idx].N == s.N {
		r.steps[idx].Screenshot = name
	}
	r.mu.Unlock()
}

// scrubFrame paints the secure fields (driver points) of a captured window black, scales it to fit maxSide and encodes
// it as JPEG.
func scrubFrame(img Image, secure []Rect, maxSide int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(img.Data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), src, b.Min, draw.Src)
	if img.Bounds.W > 0 && img.Bounds.H > 0 {
		sx, sy := float64(b.Dx())/img.Bounds.W, float64(b.Dy())/img.Bounds.H
		for _, r := range secure {
			// A little margin: a field's frame can be a point off its drawn border.
			x0, y0 := int((r.X-img.Bounds.X-2)*sx), int((r.Y-img.Bounds.Y-2)*sy)
			x1, y1 := int((r.X-img.Bounds.X+r.W+2)*sx+0.999), int((r.Y-img.Bounds.Y+r.H+2)*sy+0.999)
			draw.Draw(out, image.Rect(x0, y0, x1, y1).Intersect(out.Bounds()), image.NewUniform(color.Black), image.Point{}, draw.Src)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 95}); err != nil {
		return nil, err
	}
	data, _, _, err := encodeFrame(buf.Bytes(), maxSide)
	return data, err
}
