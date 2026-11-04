package server_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/computeruse"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
)

// fakeMachine is the Driver of these tests: nothing touches the real desktop. It leaves out what the turns do not reach
// (a nil embedded interface panics, which would show if they did).
type fakeMachine struct {
	computeruse.Driver
	mu    sync.Mutex
	typed []string
	// blockType makes Type wait for its ctx: an action in flight.
	blockType chan struct{}
}

func (*fakeMachine) Info() computeruse.Info {
	return computeruse.Info{Platform: "fake", Supported: true, Surface: computeruse.SurfaceDesktop, Capabilities: computeruse.Capabilities{Screenshot: true, Type: true}}
}

func (*fakeMachine) Permissions(context.Context) (computeruse.Permissions, error) {
	return computeruse.Permissions{Items: []computeruse.Permission{{ID: computeruse.PermAccessibility, Name: "Accessibility", Granted: false, HowToGrant: "Turn it on."}}}, nil
}

func (*fakeMachine) Apps(context.Context, bool) ([]computeruse.App, error) {
	return []computeruse.App{{ID: "com.apple.Notes", Name: "Notes", Running: true}, {ID: "com.1password.1password", Name: "1Password", Running: true}}, nil
}

func (*fakeMachine) Frontmost(context.Context) (computeruse.App, error) {
	return computeruse.App{ID: "com.apple.Notes", Name: "Notes"}, nil
}

func (m *fakeMachine) Type(ctx context.Context, t computeruse.TypeText) error {
	if m.blockType != nil {
		select {
		case <-m.blockType:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.typed = append(m.typed, t.Text)
	return nil
}

func (*fakeMachine) Screenshot(_ context.Context, r computeruse.ScreenshotRequest) (computeruse.Image, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 40, 30))); err != nil {
		return computeruse.Image{}, err
	}
	return computeruse.Image{Data: buf.Bytes(), Bounds: computeruse.Rect{W: 40, H: 30}}, nil
}

// grantingMachine has Accessibility granted, so input gets through.
type grantingMachine struct{ fakeMachine }

func (*grantingMachine) Permissions(context.Context) (computeruse.Permissions, error) {
	return computeruse.Permissions{Items: []computeruse.Permission{{ID: computeruse.PermAccessibility, Name: "Accessibility", Granted: true}}}, nil
}

func startComputer(t *testing.T, d computeruse.Driver, enabled bool, respond fg.Responder) (*harness, *server.Server) {
	t.Helper()
	var srv *server.Server
	h := startHarnessSrv(t, respond, nil, nil, func(s *server.Server) {
		srv = s
		s.ComputerUse = &computeruse.Manager{Driver: d, Dir: t.TempDir()}
		if enabled {
			if err := s.ComputerUse.SetEnabled(true); err != nil {
				t.Fatal(err)
			}
		}
	})
	return h, srv
}

func toolResults(r *llm.Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		for _, c := range m.Content {
			for _, p := range c.Content {
				b.WriteString(p.Text)
			}
		}
	}
	return b.String()
}

func TestComputerUseStatusAndSwitch(t *testing.T) {
	h, _ := startComputer(t, &fakeMachine{}, false, fg.Sequence())
	res, err := h.client.GetComputerUseStatus(h.ctx, connect.NewRequest(&agentv1.GetComputerUseStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Msg.GetStatus()
	if st.GetEnabled() || !st.GetSupported() || len(st.GetPermissions()) != 1 || st.GetPermissions()[0].GetGranted() {
		t.Fatalf("%+v", st)
	}
	set, err := h.client.SetComputerUseEnabled(h.ctx, connect.NewRequest(&agentv1.SetComputerUseEnabledRequest{Enabled: true}))
	if err != nil || !set.Msg.GetStatus().GetEnabled() {
		t.Fatalf("%v %+v", err, set)
	}
}

func TestComputerUseRPCsNeedASessionOfThisMachine(t *testing.T) {
	h, _ := startComputer(t, &fakeMachine{}, true, fg.Sequence())
	if _, err := h.client.RespondToComputerAccess(h.ctx, connect.NewRequest(&agentv1.RespondToComputerAccessRequest{SessionId: "nope", RequestId: "r"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("%v", err)
	}
	if _, err := h.client.StopComputerUse(h.ctx, connect.NewRequest(&agentv1.StopComputerUseRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("%v", err)
	}
}

func TestComputerUseOffRefusesInATurn(t *testing.T) {
	var saw string
	h, _ := startComputer(t, &fakeMachine{}, false, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("a", computeruse.ToolAccess, map[string]any{"apps": []string{"Notes"}}))}
		}
		saw = toolResults(r)
		return fg.Reply{Events: fg.Message("m2", fg.Text("done"))}
	})
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetComputerAccessRequested() != nil {
			t.Fatal("a card while off")
		}
		return ev.GetTurnFinished() != nil
	})
	if !strings.Contains(saw, "turned off") {
		t.Fatalf("%q", saw)
	}
}

func TestComputerUseCardGrantAndIndicator(t *testing.T) {
	d := &grantingMachine{}
	h, _ := startComputer(t, d, true, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("a", computeruse.ToolAccess, map[string]any{"apps": []string{"Notes", "1Password"}, "reason": "to write it down"}))}
		case 1:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("s", computeruse.ToolScreenshot, map[string]any{"app": "Notes"}))}
		case 2:
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("t", computeruse.ToolType, map[string]any{"app": "Notes", "text": "hello"}))}
		}
		return fg.Reply{Events: fg.Message("m3", fg.Text("done"))}
	})
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var card *agentv1.ComputerAccessRequested
	var resolved *agentv1.ComputerAccessResolved
	var states []*agentv1.ComputerUseState
	var image bool
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetComputerAccessRequested() != nil:
			card = ev.GetComputerAccessRequested()
			if _, err := h.client.RespondToComputerAccess(h.ctx, connect.NewRequest(&agentv1.RespondToComputerAccessRequest{SessionId: sid, RequestId: card.GetRequestId(), ApprovedAppIds: []string{"com.apple.Notes", "com.1password.1password"}})); err != nil {
				t.Fatal(err)
			}
		case ev.GetComputerAccessResolved() != nil:
			resolved = ev.GetComputerAccessResolved()
		case ev.GetComputerUseState() != nil:
			states = append(states, ev.GetComputerUseState())
		case ev.GetToolCallFinished() != nil && ev.GetToolCallFinished().GetToolUseId() == "s":
			for _, c := range ev.GetToolCallFinished().GetContent() {
				image = image || c.GetImage() != nil
			}
			if !strings.Contains(ev.GetToolCallFinished().GetStructuredResultJson(), `"computer_use":true`) {
				t.Fatalf("%s", ev.GetToolCallFinished().GetStructuredResultJson())
			}
		}
		return ev.GetTurnFinished() != nil
	})
	if card == nil || len(card.GetApps()) != 1 || card.GetApps()[0].GetTier() != agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_FULL || card.GetReason() != "to write it down" {
		t.Fatalf("card %+v", card)
	}
	if resolved == nil || len(resolved.GetApprovedAppIds()) != 1 || resolved.GetResolvedBy() != "local" {
		t.Fatalf("resolved %+v", resolved)
	}
	if !image {
		t.Fatal("no screenshot in the tool result")
	}
	if len(d.typed) != 1 || d.typed[0] != "hello" {
		t.Fatalf("typed %v", d.typed)
	}
	on := false
	for _, s := range states {
		on = on || (s.GetControlling() && strings.Contains(s.GetAction(), "Notes"))
	}
	if !on {
		t.Fatalf("no indicator: %+v", states)
	}
	// TurnFinished goes first; the indicator turns off right after it.
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		s := ev.GetComputerUseState()
		return s != nil && !s.GetControlling() && len(s.GetGrants()) == 1
	})
}

func TestComputerUseStopInterruptsAndRevokes(t *testing.T) {
	d := &grantingMachine{}
	d.blockType = make(chan struct{})
	h, srv := startComputer(t, d, true, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("a", computeruse.ToolAccess, map[string]any{"apps": []string{"Notes"}}))}
		case 1:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t", computeruse.ToolType, map[string]any{"app": "Notes", "text": "hello"}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("done"))}
	})
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	stopped := false
	var result agentv1.TurnFinished_Result
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetComputerAccessRequested() != nil:
			if _, err := h.client.RespondToComputerAccess(h.ctx, connect.NewRequest(&agentv1.RespondToComputerAccessRequest{SessionId: sid, RequestId: ev.GetComputerAccessRequested().GetRequestId(), ApprovedAppIds: []string{"com.apple.Notes"}})); err != nil {
				t.Fatal(err)
			}
		case ev.GetComputerUseState().GetControlling() && !stopped:
			stopped = true
			if _, err := h.client.StopComputerUse(h.ctx, connect.NewRequest(&agentv1.StopComputerUseRequest{SessionId: sid})); err != nil {
				t.Fatal(err)
			}
		case ev.GetTurnFinished() != nil:
			result = ev.GetTurnFinished().GetResult()
			return true
		}
		return false
	})
	if !stopped || result != agentv1.TurnFinished_RESULT_INTERRUPTED {
		t.Fatalf("stopped %v, result %v", stopped, result)
	}
	if len(d.typed) != 0 {
		t.Fatalf("typed %v", d.typed)
	}
	if st := srv.ComputerUse.Session(sid).State(); len(st.Grants) != 0 || st.Controlling {
		t.Fatalf("%+v", st)
	}
}

func TestComputerUseInterruptCancelsTheCard(t *testing.T) {
	h, _ := startComputer(t, &grantingMachine{}, true, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("a", computeruse.ToolAccess, map[string]any{"apps": []string{"Notes"}}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("done"))}
	})
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var resolved *agentv1.ComputerAccessResolved
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetComputerAccessRequested() != nil {
			if _, err := h.client.Interrupt(h.ctx, connect.NewRequest(&agentv1.InterruptRequest{SessionId: sid})); err != nil {
				t.Fatal(err)
			}
		}
		if r := ev.GetComputerAccessResolved(); r != nil {
			resolved = r
		}
		return ev.GetTurnFinished() != nil
	})
	if resolved == nil || resolved.GetResolvedBy() != "interrupt" || len(resolved.GetApprovedAppIds()) != 0 {
		t.Fatalf("%+v", resolved)
	}
}

// A message that came through the relay turns computer use off for the session until someone at the machine writes.
func TestComputerUseRemoteMessageRefuses(t *testing.T) {
	var results []string
	h, srv := startComputer(t, &grantingMachine{}, true, func(n int, r *llm.Request) fg.Reply {
		if n%2 == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("a", computeruse.ToolApps, map[string]any{}))}
		}
		results = append(results, toolResults(r))
		return fg.Reply{Events: fg.Message("m2", fg.Text("done"))}
	})
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	remote := server.WithRemoteOrigin(context.Background())
	if _, err := srv.SendUserMessage(remote, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Content: text("from another device")})); err != nil {
		t.Fatal(err)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetTurnFinished() != nil })
	if len(results) != 1 || !strings.Contains(results[0], "another device") {
		t.Fatalf("%q", results)
	}
	h.send(sid, "from here")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetTurnFinished() != nil })
	if len(results) != 2 || !strings.Contains(results[1], "Notes (com.apple.Notes)") {
		t.Fatalf("%q", results)
	}
}
