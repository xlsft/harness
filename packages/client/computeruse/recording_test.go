package computeruse

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/tools"
)

// watchDriver is the fake driver with a scripted feed of the user's input and known password fields.
type watchDriver struct {
	*fakeDriver
	feed   *fakeFeed
	secure []Rect
}

type fakeFeed struct {
	ch      chan RawEvent
	once    sync.Once
	stopped chan struct{}
}

func (f *fakeFeed) Events() <-chan RawEvent { return f.ch }
func (f *fakeFeed) Stop()                   { f.once.Do(func() { close(f.stopped) }) }

func (w *watchDriver) Watch(string) (Feed, error) {
	w.feed = &fakeFeed{ch: make(chan RawEvent, 64), stopped: make(chan struct{})}
	return w.feed, nil
}

func (w *watchDriver) SecureFields(context.Context, string, int64) ([]Rect, error) { return w.secure, nil }

// Screenshot of a window is all white, so a black pixel can only be blanking.
func (w *watchDriver) Screenshot(ctx context.Context, r ScreenshotRequest) (Image, error) {
	img, err := w.fakeDriver.Screenshot(ctx, r)
	if err != nil {
		return img, err
	}
	src := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := range 600 {
		for x := range 800 {
			src.Set(x, y, color.White)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, src)
	img.Data = buf.Bytes()
	return img, nil
}

func recordingSetup(t *testing.T) (*watchDriver, *Manager, chan RecordingFile) {
	t.Helper()
	d := &watchDriver{fakeDriver: newFake(), secure: []Rect{{X: 300, Y: 250, W: 200, H: 30}}}
	recorded := make(chan RecordingFile, 1)
	m := &Manager{Driver: d, Dir: filepath.Join(t.TempDir(), "computer-use"), OnRecorded: func(_ string, f RecordingFile) { recorded <- f }}
	if err := m.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	return d, m, recorded
}

func noteWin() *Window {
	return &Window{ID: 11, AppID: notes, Title: "A note", Bounds: Rect{100, 50, 800, 600}}
}

func TestRecordingRefusals(t *testing.T) {
	d, m, _ := recordingSetup(t)
	ctx := context.Background()
	if _, err := m.StartRecording(ctx, "s1", []string{vault}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("a blocked app is recorded: %v", err)
	}
	if _, err := m.StartRecording(ctx, "s1", nil); err == nil {
		t.Fatal("a recording of no app")
	}
	d.perms.Items[0].Granted = false
	if _, err := m.StartRecording(ctx, "s1", []string{notes}); err == nil || !strings.Contains(err.Error(), "Screen Recording") {
		t.Fatalf("no Screen Recording: %v", err)
	}
	d.perms.Items[0].Granted = true
	_ = m.SetEnabled(false)
	if _, err := m.StartRecording(ctx, "s1", []string{notes}); err != ErrDisabled {
		t.Fatalf("computer use off: %v", err)
	}
	_ = m.SetEnabled(true)
	if _, err := m.StartRecording(ctx, "s1", []string{notes}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartRecording(ctx, "s2", []string{notes}); err != ErrRecordingBusy {
		t.Fatalf("two recordings at once: %v", err)
	}
	if _, err := m.StopRecording("s2", false); err != ErrNoRecording {
		t.Fatalf("another session stops it: %v", err)
	}
	f, err := m.StopRecording("s1", true)
	if err != nil || f.StopReason != "discarded" {
		t.Fatalf("discard: %+v %v", f, err)
	}
	if entries, _ := os.ReadDir(m.RecordingsDir()); len(entries) != 0 {
		t.Fatalf("a discarded recording stays: %v", entries)
	}
}

func TestRecordingStepsAndScrubbing(t *testing.T) {
	d, m, recorded := recordingSetup(t)
	st, err := m.StartRecording(context.Background(), "s1", []string{"Notes"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Recording || st.Hotkey == "" {
		t.Fatalf("%+v", st)
	}
	now := time.Now()
	at := func(i int) time.Time { return now.Add(time.Duration(i) * 50 * time.Millisecond) }
	n := d.app(notes)
	feed := d.feed.ch
	feed <- RawEvent{Type: "started"}
	feed <- RawEvent{Type: "activate", App: n, Window: noteWin(), At: at(0)}
	feed <- RawEvent{Type: "click", App: n, X: 400, Y: 300, Button: "left", Count: 1, Window: noteWin(), Element: RawElement{Role: "AXButton", Title: "New Note"}, At: at(1)}
	for i, ch := range "Hi" {
		feed <- RawEvent{Type: "key", App: n, Key: strings.ToLower(string(ch)), Text: string(ch), Window: noteWin(), At: at(2 + i)}
	}
	feed <- RawEvent{Type: "click", App: n, X: 400, Y: 265, Button: "left", Count: 1, Window: noteWin(), Element: RawElement{Role: "AXTextField", Secure: true}, At: at(4)}
	for i := range 3 {
		feed <- RawEvent{Type: "secure_key", App: n, Window: noteWin(), At: at(5 + i)}
	}
	feed <- RawEvent{Type: "key", App: n, Key: "s", Modifiers: []string{"cmd"}, Window: noteWin(), At: at(8)}
	f := d.app(finder)
	feed <- RawEvent{Type: "click", App: f, X: 10, Y: 10, Element: RawElement{Role: "AXButton", Title: "Secret project"}, At: at(9)}
	feed <- RawEvent{Type: "key", App: f, Key: "x", Text: "x", At: at(10)}
	feed <- RawEvent{Type: "click", App: App{}, X: 5, Y: 5, At: at(11)} // the panel's own Stop button
	feed <- RawEvent{Type: "stop", Reason: "hotkey"}
	var rec RecordingFile
	select {
	case rec = <-recorded:
	case <-time.After(10 * time.Second):
		t.Fatal("the recording did not end")
	}
	if rec.StopReason != "hotkey" {
		t.Fatalf("stop reason %q", rec.StopReason)
	}
	var kinds []string
	for _, s := range rec.Steps {
		kinds = append(kinds, s.Kind)
	}
	want := []string{StepActivate, StepClick, StepType, StepClick, StepSecureType, StepKey, StepOtherApp}
	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Fatalf("steps %v, want %v", kinds, want)
	}
	if rec.Steps[2].Text != "Hi" || rec.Steps[5].Keys != "cmd+s" || rec.Steps[1].Element != `button "New Note"` || rec.Steps[3].Element != "password field" {
		t.Fatalf("%+v", rec.Steps)
	}
	if rec.Steps[1].X != 300 || rec.Steps[1].Y != 250 {
		t.Fatalf("click relative to the window: %+v", rec.Steps[1])
	}
	raw, _ := json.Marshal(rec)
	for _, leak := range []string{"Secret project", `"text":"x"`, "Finder"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("the recording holds %s from an app not picked: %s", leak, raw)
		}
	}
	// The screenshot after the secure field step has the field black, the rest white.
	var shot string
	for _, s := range rec.Steps {
		if s.Kind == StepSecureType {
			shot = s.Screenshot
		}
	}
	if shot == "" {
		t.Fatalf("no screenshot of the password step: %+v", rec.Steps)
	}
	data, err := os.ReadFile(filepath.Join(rec.Dir, shot))
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	// Window at (100, 50), 800×600 points, image 800×600 pixels: the field (300, 250) is at pixel (200, 200).
	if r, g, b, _ := img.At(300, 215).RGBA(); r > 0x2000 || g > 0x2000 || b > 0x2000 {
		t.Fatalf("the password field is not blank: %v %v %v", r, g, b)
	}
	if r, _, _, _ := img.At(50, 50).RGBA(); r < 0xd000 {
		t.Fatal("the rest of the window is blanked too")
	}
	if st, _ := os.Stat(rec.Dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("recording folder mode %v", st.Mode().Perm())
	}
	loaded, err := m.LoadRecording(rec.ID)
	if err != nil || len(loaded.Steps) != len(rec.Steps) {
		t.Fatalf("reload: %v", err)
	}
	if _, err := m.LoadRecording("../computer-use"); err == nil {
		t.Fatal("an id outside the folder loads")
	}
	if got := m.RecordingStateOf("s1"); got.Recording {
		t.Fatal("still recording")
	}
}

type reviewer struct {
	tools.Interactor
	ans  SkillReviewAnswer
	seen []SkillReview
}

func (r *reviewer) ReviewSkill(_ context.Context, req SkillReview) (SkillReviewAnswer, error) {
	r.seen = append(r.seen, req)
	return r.ans, nil
}

func TestDemoTools(t *testing.T) {
	d, m, recorded := recordingSetup(t)
	if _, err := m.StartRecording(context.Background(), "s1", []string{notes}); err != nil {
		t.Fatal(err)
	}
	d.feed.ch <- RawEvent{Type: "click", App: d.app(notes), X: 400, Y: 300, Count: 1, Window: noteWin(), Element: RawElement{Role: "AXButton", Title: "New Note"}}
	time.Sleep(50 * time.Millisecond)
	if _, err := m.StopRecording("s1", false); err != nil {
		t.Fatal(err)
	}
	rec := <-recorded

	cwd := t.TempDir()
	rv := &reviewer{ans: SkillReviewAnswer{Saved: true, Scope: SkillScopeProject}}
	run := func(name string, input any) tools.Result {
		env := tools.NewEnv(cwd, t.TempDir())
		env.SessionID = "s1"
		env.SetHost(toolHost{ia: rv})
		raw, _ := json.Marshal(input)
		for _, tool := range m.RecordingTools() {
			if tool.Name() == name {
				return tool.Run(context.Background(), env, tools.Call{ID: "tu1", Input: raw})
			}
		}
		t.Fatalf("no tool %s", name)
		return tools.Result{}
	}
	r := run(ToolDemoRecording, map[string]any{"screenshots": []int{1}})
	if r.IsError || !strings.Contains(resultText(r), `Clicked button "New Note" in Notes`) || len(r.Content) != 3 {
		t.Fatalf("%s", resultText(r))
	}
	if s, _ := r.Structured.(map[string]any); s["computer_use"] != true {
		t.Fatal("screenshots are not kept off the upload")
	}
	if r := run(ToolDemoSkillSave, map[string]any{"name": "Bad Name", "description": "x", "body": "y"}); !r.IsError {
		t.Fatal("a bad name is accepted")
	}
	in := map[string]any{"recording_id": rec.ID, "name": "new-note", "description": "Makes a note: use when asked", "body": "1. Open Notes"}
	r = run(ToolDemoSkillSave, in)
	path := filepath.Join(cwd, ".harness", "skills", "new-note", "SKILL.md")
	if r.IsError || !strings.Contains(resultText(r), path) {
		t.Fatalf("%s", resultText(r))
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "---\nname: new-note\ndescription: \"Makes a note: use when asked\"\n---") || !strings.Contains(string(data), "1. Open Notes") {
		t.Fatalf("%s", data)
	}
	if r := run(ToolDemoSkillSave, in); !r.IsError || !strings.Contains(resultText(r), "already exists") {
		t.Fatal("an existing skill is overwritten")
	}
	// Edited by the user, into personal skills.
	rv.ans = SkillReviewAnswer{Saved: true, Scope: SkillScopePersonal, Content: "---\nname: other\ndescription: edited\n---\nmine\n"}
	in["name"] = "other"
	r = run(ToolDemoSkillSave, in)
	personal := filepath.Join(filepath.Dir(m.Dir), "skills", "other", "SKILL.md")
	if data, _ := os.ReadFile(personal); r.IsError || !strings.Contains(string(data), "mine") || !strings.Contains(resultText(r), "edited") {
		t.Fatalf("%s %s", resultText(r), data)
	}
	rv.ans = SkillReviewAnswer{Note: "not now"}
	in["name"] = "third"
	if r := run(ToolDemoSkillSave, in); r.IsError || !strings.Contains(resultText(r), "not now") {
		t.Fatalf("%s", resultText(r))
	}
	if _, err := os.Stat(filepath.Join(cwd, ".harness", "skills", "third")); err == nil {
		t.Fatal("a discarded draft was written")
	}
	if len(rv.seen) != 3 || rv.seen[0].RecordingID != rec.ID || !strings.Contains(rv.seen[0].Content, "name: new-note") {
		t.Fatalf("%+v", rv.seen)
	}
}
