package server_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/computeruse"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// recordingMachine is a fake machine with both permissions granted and a scripted feed of what the user does.
type recordingMachine struct {
	fakeMachine
	mu   sync.Mutex
	feed chan computeruse.RawEvent
}

type scriptedFeed struct {
	ch   chan computeruse.RawEvent
	once sync.Once
}

func (f *scriptedFeed) Events() <-chan computeruse.RawEvent { return f.ch }
func (f *scriptedFeed) Stop()                               { f.once.Do(func() { close(f.ch) }) }

func (*recordingMachine) Permissions(context.Context) (computeruse.Permissions, error) {
	return computeruse.Permissions{Items: []computeruse.Permission{
		{ID: computeruse.PermAccessibility, Name: "Accessibility", Granted: true},
		{ID: computeruse.PermScreenRecording, Name: "Screen Recording", Granted: true},
	}}, nil
}

func (m *recordingMachine) Watch(string) (computeruse.Feed, error) {
	f := &scriptedFeed{ch: make(chan computeruse.RawEvent, 64)}
	m.mu.Lock()
	m.feed = f.ch
	m.mu.Unlock()
	return f, nil
}

func (*recordingMachine) SecureFields(context.Context, string, int64) ([]computeruse.Rect, error) {
	return nil, nil
}

// TestDemoRecordingToSkill goes the whole way: the user records a demo through the RPCs, the session's model is told,
// reads it, drafts a skill, and the user edits and saves it in the review card; the file is the user's version.
func TestDemoRecordingToSkill(t *testing.T) {
	d := &recordingMachine{}
	var mu sync.Mutex
	var firstRequest string
	h, _ := startComputer(t, d, true, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			mu.Lock()
			raw, _ := json.Marshal(r.Messages)
			firstRequest = string(raw)
			mu.Unlock()
			return fg.Reply{Events: fg.Message("m0", fg.ToolUse("r", computeruse.ToolDemoRecording, map[string]any{}))}
		case 1:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("s", computeruse.ToolDemoSkillSave, map[string]any{
				"name": "new-note", "description": "Makes a note in Notes", "body": "1. Open Notes\n2. Click New Note",
			}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("saved"))}
	})
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	apps, err := h.client.ListDemoRecordingApps(h.ctx, connect.NewRequest(&agentv1.ListDemoRecordingAppsRequest{}))
	if err != nil || apps.Msg.GetUnavailableReason() != "" {
		t.Fatalf("%v %+v", err, apps)
	}
	tiers := map[string]agentv1.ComputerAccessTier{}
	for _, a := range apps.Msg.GetApps() {
		tiers[a.GetName()] = a.GetTier()
	}
	if tiers["Notes"] != agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_FULL || tiers["1Password"] != agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_BLOCKED {
		t.Fatalf("tiers %v", tiers)
	}
	if _, err := h.client.StartDemoRecording(h.ctx, connect.NewRequest(&agentv1.StartDemoRecordingRequest{SessionId: sid, AppIds: []string{"com.1password.1password"}})); err == nil {
		t.Fatal("a blocked app was recorded")
	}
	start, err := h.client.StartDemoRecording(h.ctx, connect.NewRequest(&agentv1.StartDemoRecordingRequest{SessionId: sid, AppIds: []string{"com.apple.Notes"}}))
	if err != nil || !start.Msg.GetState().GetRecording() || start.Msg.GetState().GetHotkey() == "" {
		t.Fatalf("%v %+v", err, start)
	}
	if _, err := h.client.StartDemoRecording(h.ctx, connect.NewRequest(&agentv1.StartDemoRecordingRequest{SessionId: sid, AppIds: []string{"com.apple.Notes"}})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("a second recording: %v", err)
	}
	d.mu.Lock()
	feed := d.feed
	d.mu.Unlock()
	notes := computeruse.App{ID: "com.apple.Notes", Name: "Notes"}
	win := &computeruse.Window{ID: 7, AppID: notes.ID, Title: "Notes", Bounds: computeruse.Rect{W: 40, H: 30}}
	now := time.Now()
	feed <- computeruse.RawEvent{Type: "started"}
	feed <- computeruse.RawEvent{Type: "activate", App: notes, Window: win, At: now}
	feed <- computeruse.RawEvent{Type: "click", App: notes, X: 10, Y: 10, Button: "left", Count: 1, Window: win, Element: computeruse.RawElement{Role: "AXButton", Title: "New Note"}, At: now.Add(50 * time.Millisecond)}
	feed <- computeruse.RawEvent{Type: "secure_key", App: notes, Window: win, At: now.Add(100 * time.Millisecond)}
	// The steps reach the recording before Stop.
	deadline := time.Now().Add(10 * time.Second)
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if time.Now().After(deadline) {
			t.Fatal("no recording state with steps")
		}
		st := ev.GetDemoRecordingState()
		return st != nil && st.GetSteps() >= 3
	})
	stop, err := h.client.StopDemoRecording(h.ctx, connect.NewRequest(&agentv1.StopDemoRecordingRequest{SessionId: sid}))
	if err != nil || stop.Msg.GetSteps() < 3 {
		t.Fatalf("%v %+v", err, stop)
	}

	var card *agentv1.DemoSkillReviewRequested
	var resolved *agentv1.DemoSkillReviewResolved
	edited := "---\nname: new-note\ndescription: Makes a note in Notes\n---\n\n1. Open Notes\n2. Click New Note\n3. Check the note is there\n"
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetDemoSkillReviewRequested() != nil:
			card = ev.GetDemoSkillReviewRequested()
			if _, err := h.client.RespondToDemoSkillReview(h.ctx, connect.NewRequest(&agentv1.RespondToDemoSkillReviewRequest{SessionId: sid, RequestId: card.GetRequestId(), Save: true, Content: edited, Scope: "project"})); err != nil {
				t.Fatal(err)
			}
		case ev.GetDemoSkillReviewResolved() != nil:
			resolved = ev.GetDemoSkillReviewResolved()
		}
		return ev.GetTurnFinished() != nil
	})
	mu.Lock()
	reminder := firstRequest
	mu.Unlock()
	if !strings.Contains(reminder, stop.Msg.GetRecordingId()) || !strings.Contains(reminder, "DemoSkillSave") {
		t.Fatalf("the model was not told about the recording: %q", reminder)
	}
	if card == nil || card.GetName() != "new-note" || !strings.Contains(card.GetContent(), "Click New Note") || card.GetProjectPath() == "" {
		t.Fatalf("card %+v", card)
	}
	if resolved == nil || !resolved.GetSaved() || resolved.GetResolvedBy() != "local" {
		t.Fatalf("resolved %+v", resolved)
	}
	got, err := os.ReadFile(card.GetProjectPath())
	if err != nil || string(got) != edited {
		t.Fatalf("%v %q", err, got)
	}
	if filepath.Base(filepath.Dir(card.GetProjectPath())) != "new-note" || !strings.Contains(card.GetProjectPath(), filepath.Join(".harness", "skills")) {
		t.Fatalf("path %s", card.GetProjectPath())
	}
	if _, err := h.client.RespondToDemoSkillReview(h.ctx, connect.NewRequest(&agentv1.RespondToDemoSkillReviewRequest{SessionId: sid, RequestId: card.GetRequestId(), Save: true})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("a second answer: %v", err)
	}
}
