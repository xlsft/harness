package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
)

// useTools answers the first model request with one message of tool calls, the next with plain text.
func useTools(calls ...fg.Reply) fg.Responder {
	fs := make([]func(*llm.Request) fg.Reply, len(calls))
	for i, c := range calls {
		fs[i] = func(*llm.Request) fg.Reply { return c }
	}
	return fg.Sequence(fs...)
}

func toolCall(id, name string, in map[string]any) fg.Reply {
	return fg.Reply{Events: fg.Message("m", fg.ToolUse(id, name, in))}
}

// runTurn answers every permission prompt with decision and returns the prompts and the events of the turn.
func (h *harness) runTurn(sid string, ch <-chan *agentv1.SessionEvent, decision agentv1.RespondToPermissionRequest_Decision) ([]*agentv1.PermissionRequest, []*agentv1.SessionEvent) {
	h.t.Helper()
	var asked []*agentv1.PermissionRequest
	h.send(sid, "go")
	evs := h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			asked = append(asked, p)
			if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId, Decision: decision})); err != nil {
				h.t.Fatal(err)
			}
		}
		return isTurnFinished(ev)
	})
	return asked, evs
}

func notices(evs []*agentv1.SessionEvent) string {
	var sb strings.Builder
	for _, ev := range evs {
		if n := ev.GetNotice(); n != nil {
			sb.WriteString(n.Kind + ": " + n.Text + "\n")
		}
	}
	return sb.String()
}

const (
	allowOnce = agentv1.RespondToPermissionRequest_DECISION_ALLOW
	denyIt    = agentv1.RespondToPermissionRequest_DECISION_DENY
)

func TestSettingsSetAsksAndStaysOnTheDevice(t *testing.T) {
	docs := &countingDocs{}
	h := startHarnessSrv(t, useTools(toolCall("a", "SettingsSet", map[string]any{"key": "theme", "value": "dark"})), nil, nil, func(s *server.Server) { s.Preferences = docs })
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	asked, evs := h.runTurn(sid, ch, allowOnce)
	// Bypass does not answer for the user here.
	if len(asked) != 1 || !strings.Contains(asked[0].Reason, `"system"`) || !strings.Contains(asked[0].Reason, `"dark"`) {
		t.Fatalf("asked %+v", asked)
	}
	if len(asked[0].SuggestedRules) != 0 {
		t.Fatalf("no standing approval for a settings change: %v", asked[0].SuggestedRules)
	}
	got, err := h.client.GetDeviceSettings(h.ctx, connect.NewRequest(&agentv1.GetDeviceSettingsRequest{}))
	if err != nil || got.Msg.Settings.Theme != "dark" || !got.Msg.Settings.Notifications {
		t.Fatalf("%v %+v", err, got)
	}
	changed := false
	for _, ev := range evs {
		changed = changed || ev.GetDeviceSettingsChanged() != nil
	}
	if !changed || !strings.Contains(notices(evs), "theme set to dark") {
		t.Fatalf("changed=%v notices %q", changed, notices(evs))
	}
	if docs.gets != 0 || docs.puts != 0 {
		t.Fatalf("a device setting reached the server: %d gets, %d puts", docs.gets, docs.puts)
	}
	if b, err := os.ReadFile(filepath.Join(os.Getenv("HARNESS_HOME"), "device-settings.json")); err != nil || !strings.Contains(string(b), `"dark"`) {
		t.Fatalf("%v %s", err, b)
	}
}

func TestSettingsSetDeclinedChangesNothing(t *testing.T) {
	h := startHarness(t, useTools(toolCall("a", "SettingsSet", map[string]any{"key": "notifications", "value": false})), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	asked, _ := h.runTurn(sid, ch, denyIt)
	if len(asked) != 1 {
		t.Fatalf("asked %v", asked)
	}
	got, err := h.client.GetDeviceSettings(h.ctx, connect.NewRequest(&agentv1.GetDeviceSettingsRequest{}))
	if err != nil || !got.Msg.Settings.Notifications {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestSecuritySettingRefusedWithoutAPrompt(t *testing.T) {
	h := startHarness(t, useTools(toolCall("a", "SettingsSet", map[string]any{"key": "permissions.allow", "value": "Bash"})), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	asked, _ := h.runTurn(sid, ch, allowOnce)
	if len(asked) != 0 {
		t.Fatalf("asked %+v", asked)
	}
	if tr := lastResultText(h.gw.Requests()[1]); !strings.Contains(tr, "not reachable") {
		t.Fatalf("result %q", tr)
	}
}

func TestSyncedSettingWritesTheUserDocument(t *testing.T) {
	docs := &countingDocs{content: `{"other": 1}`}
	h := startHarnessSrv(t, useTools(toolCall("a", "SettingsSet", map[string]any{"key": "compactIdleSessions", "value": false})), nil, nil, func(s *server.Server) { s.Preferences = docs })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	asked, _ := h.runTurn(sid, ch, allowOnce)
	if len(asked) != 1 || !strings.Contains(asked[0].Reason, `"true"`) || !strings.Contains(asked[0].Reason, `"false"`) {
		t.Fatalf("asked %+v", asked)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(docs.content), &doc); err != nil || doc["compactIdleSessions"] != false || doc["other"] != float64(1) {
		t.Fatalf("%v %s", err, docs.content)
	}
}

func TestSessionSetModeStricterIsFreeWiderAsks(t *testing.T) {
	h := startHarness(t, useTools(
		toolCall("a", "SessionSetMode", map[string]any{"mode": "default"}),
		toolCall("b", "SessionSetMode", map[string]any{"mode": "acceptEdits"}),
		toolCall("c", "SessionSetMode", map[string]any{"mode": "bypassPermissions"}),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	asked, evs := h.runTurn(sid, ch, allowOnce)
	if len(asked) != 1 || asked[0].ToolUseId != "b" || !strings.Contains(asked[0].Reason, "Accept edits") {
		t.Fatalf("asked %+v", asked)
	}
	if h.mode(sid) != agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS {
		t.Fatalf("mode %v", h.mode(sid))
	}
	if !strings.Contains(notices(evs), "session_mode_changed") {
		t.Fatalf("notices %q", notices(evs))
	}
	if tr := lastResultText(h.gw.Requests()[3]); !strings.Contains(tr, "Bypass") {
		t.Fatalf("result %q", tr)
	}
}

// Calls of one message cannot use the mode as it was when the message arrived: the second call here would look like
// a no-op against it, and raises the mode the first one lowered.
func TestSessionSetModeBatchCannotSkipThePrompt(t *testing.T) {
	h := startHarness(t, useTools(fg.Reply{Events: fg.Message("m",
		fg.ToolUse("a", "SessionSetMode", map[string]any{"mode": "default"}),
		fg.ToolUse("b", "SessionSetMode", map[string]any{"mode": "acceptEdits"}),
	)}), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	asked, _ := h.runTurn(sid, ch, denyIt)
	if len(asked) != 1 || asked[0].ToolUseId != "b" {
		t.Fatalf("asked %+v", asked)
	}
	if h.mode(sid) != agentv1.PermissionMode_PERMISSION_MODE_DEFAULT {
		t.Fatalf("mode %v", h.mode(sid))
	}
}

func TestSessionSetModeDeclinedKeepsTheMode(t *testing.T) {
	h := startHarness(t, useTools(toolCall("a", "SessionSetMode", map[string]any{"mode": "auto"})), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	asked, _ := h.runTurn(sid, ch, denyIt)
	if len(asked) != 1 || h.mode(sid) != agentv1.PermissionMode_PERMISSION_MODE_DEFAULT {
		t.Fatalf("asked %+v mode %v", asked, h.mode(sid))
	}
}

// Plan mode entered with this tool behaves like EnterPlanMode: approving the plan returns to where it came from.
func TestSessionSetModePlanThenExitPlanMode(t *testing.T) {
	h := startHarness(t, useTools(
		toolCall("a", "SessionSetMode", map[string]any{"mode": "plan"}),
		toolCall("b", "ExitPlanMode", map[string]any{"plan": "# Plan\n1. Do it"}),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go")
	sawPlan := false
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if m := ev.GetPermissionModeChanged(); m != nil && m.Mode == agentv1.PermissionMode_PERMISSION_MODE_PLAN {
			sawPlan = true
		}
		if p := ev.GetPlanProposed(); p != nil {
			if _, err := h.client.RespondToPlan(h.ctx, connect.NewRequest(&agentv1.RespondToPlanRequest{SessionId: sid, RequestId: p.RequestId, Decision: agentv1.RespondToPlanRequest_DECISION_APPROVE_ACCEPT_EDITS})); err != nil {
				t.Fatal(err)
			}
		}
		return isTurnFinished(ev)
	})
	if !sawPlan || h.mode(sid) != agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS {
		t.Fatalf("sawPlan=%v mode %v", sawPlan, h.mode(sid))
	}
}

func TestLeavingPlanModeWithTheToolAsks(t *testing.T) {
	h := startHarness(t, useTools(
		toolCall("a", "EnterPlanMode", map[string]any{}),
		toolCall("b", "SessionSetMode", map[string]any{"mode": "default"}),
		toolCall("c", "Bash", map[string]any{"command": "touch x"}),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	asked, _ := h.runTurn(sid, ch, denyIt)
	if len(asked) != 1 || asked[0].ToolUseId != "b" || h.mode(sid) != agentv1.PermissionMode_PERMISSION_MODE_PLAN {
		t.Fatalf("asked %+v mode %v", asked, h.mode(sid))
	}
}

func TestSessionSetEffortAndTitleReachTheFrontends(t *testing.T) {
	h := startHarness(t, useTools(
		toolCall("a", "SessionSetEffort", map[string]any{"effort": "high"}),
		toolCall("b", "SessionSetTitle", map[string]any{"title": "  A  named   session "}),
		toolCall("c", "SessionSetEffort", map[string]any{"effort": "extreme"}),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	asked, evs := h.runTurn(sid, ch, allowOnce)
	if len(asked) != 0 {
		t.Fatalf("asked %+v", asked)
	}
	var effort, title string
	for _, ev := range evs {
		if u := ev.GetSessionUpdated(); u != nil {
			effort, title = u.Effort, u.Title
		}
	}
	if effort != "high" || title != "A named session" {
		t.Fatalf("effort %q title %q", effort, title)
	}
	n := notices(evs)
	if !strings.Contains(n, "Effort changed to high") || !strings.Contains(n, `renamed to "A named session"`) {
		t.Fatalf("notices %q", n)
	}
	// From the next model request on.
	reqs := h.gw.Requests()
	if reqs[0].Effort != "" || reqs[1].Effort != "high" {
		t.Fatalf("efforts %q %q", reqs[0].Effort, reqs[1].Effort)
	}
	if tr := lastResultText(reqs[3]); !strings.Contains(tr, "effort is one of") {
		t.Fatalf("result %q", tr)
	}
}

func TestSetEffortRPCPublishes(t *testing.T) {
	h := startHarness(t, fg.Sequence(), nil)
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	if info := h.info(sid); info.Effort != "" {
		t.Fatalf("effort %q", info.Effort)
	}
	if _, err := h.client.SetEffort(h.ctx, connect.NewRequest(&agentv1.SetEffortRequest{SessionId: sid, Effort: 3})); err != nil {
		t.Fatal(err)
	}
	if info := h.info(sid); info.Effort == "" {
		t.Fatal("the RPC and the tool show the same state")
	}
}

func TestSessionSetModelSwitchesFromTheNextRequest(t *testing.T) {
	h := startHarnessSrv(t, useTools(
		toolCall("a", "SessionSetModel", map[string]any{"model": "other-model"}),
		toolCall("b", "SessionSetModel", map[string]any{"model": "not-offered"}),
	), func(g *fg.Server) { g.Models = []string{"fake-model", "other-model"} }, nil, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Model: "fake-model"})
	asked, evs := h.runTurn(sid, ch, allowOnce)
	if len(asked) != 0 {
		t.Fatalf("asked %+v", asked)
	}
	reqs := h.gw.Requests()
	if reqs[0].Model != "fake-model" || reqs[1].Model != "other-model" || reqs[2].Model != "other-model" {
		t.Fatalf("models %q %q %q", reqs[0].Model, reqs[1].Model, reqs[2].Model)
	}
	if tr := lastResultText(reqs[2]); !strings.Contains(tr, "unknown model") {
		t.Fatalf("result %q", tr)
	}
	if !strings.Contains(notices(evs), "session_model_changed") || h.info(sid).Model != "other-model" {
		t.Fatalf("notices %q model %q", notices(evs), h.info(sid).Model)
	}
}

type fakeInhibit struct {
	mu               sync.Mutex
	starts, releases int
}

func (f *fakeInhibit) start() (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.releases++
	}, nil
}

func (f *fakeInhibit) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.releases
}

func TestKeepAwakeToolAndToggleAgree(t *testing.T) {
	inhibit := &fakeInhibit{}
	h := startHarnessSrv(t, useTools(toolCall("a", "KeepAwake", map[string]any{"enabled": true})), nil, nil, func(s *server.Server) { s.Inhibitor = inhibit.start })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	asked, evs := h.runTurn(sid, ch, allowOnce)
	if len(asked) != 0 {
		t.Fatalf("asked %+v", asked)
	}
	if !h.info(sid).KeepAwake {
		t.Fatal("the switch must read on after the tool")
	}
	if !strings.Contains(notices(evs), "keep_awake") {
		t.Fatalf("notices %q", notices(evs))
	}
	if starts, _ := inhibit.counts(); starts != 1 {
		t.Fatalf("started %d during the turn", starts)
	}
	// The toggle sees and changes the same switch.
	if _, err := h.client.SetKeepAwake(h.ctx, connect.NewRequest(&agentv1.SetKeepAwakeRequest{SessionId: sid, Enabled: false})); err != nil {
		t.Fatal(err)
	}
	if h.info(sid).KeepAwake {
		t.Fatal("the switch must read off after the toggle")
	}
	if _, err := h.client.SetKeepAwake(h.ctx, connect.NewRequest(&agentv1.SetKeepAwakeRequest{SessionId: sid, Enabled: true})); err != nil {
		t.Fatal(err)
	}
	h.closeSession(sid)
	starts, releases := inhibit.counts()
	if starts != releases {
		t.Fatalf("closing the session must release: %d starts, %d releases", starts, releases)
	}
	if _, err := h.client.SetKeepAwake(h.ctx, connect.NewRequest(&agentv1.SetKeepAwakeRequest{SessionId: sid, Enabled: true})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("closed session: %v", err)
	}
}

func TestDeviceToolsRefuseWhenDrivenFromElsewhere(t *testing.T) {
	// No editor can be found here, should the refusal ever fail: nothing really opens.
	t.Setenv("PATH", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	var srv *server.Server
	h := startHarnessSrv(t, useTools(
		toolCall("a", "OpenInEditor", map[string]any{"path": "a.txt"}),
		toolCall("b", "RevealPath", map[string]any{"path": "a.txt"}),
	), nil, nil, func(s *server.Server) { srv = s })
	if err := os.WriteFile(filepath.Join(h.proj, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	if _, err := srv.SendUserMessage(server.WithRemoteOrigin(context.Background()), connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Content: text("go")})); err != nil {
		t.Fatal(err)
	}
	h.until(ch, isTurnFinished)
	reqs := h.gw.Requests()
	for _, i := range []int{1, 2} {
		if tr := lastResultText(reqs[i]); !strings.Contains(tr, "another machine") {
			t.Fatalf("result %d: %q", i, tr)
		}
	}
}

func TestOutsidePathAsksTheUser(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	outside := filepath.Join(t.TempDir(), "o.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := startHarness(t, useTools(toolCall("a", "OpenInEditor", map[string]any{"path": outside})), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	asked, _ := h.runTurn(sid, ch, denyIt)
	if len(asked) != 1 || !strings.Contains(asked[0].Reason, outside) || len(asked[0].SuggestedRules) != 0 {
		t.Fatalf("asked %+v", asked)
	}
}

func TestDeviceSettingsRPC(t *testing.T) {
	h := startHarness(t, fg.Sequence(), nil)
	got, err := h.client.GetDeviceSettings(h.ctx, connect.NewRequest(&agentv1.GetDeviceSettingsRequest{}))
	if err != nil || got.Msg.Settings.Theme != "" || !got.Msg.Settings.Notifications {
		t.Fatalf("defaults %v %+v", err, got)
	}
	up, err := h.client.UpdateDeviceSettings(h.ctx, connect.NewRequest(&agentv1.UpdateDeviceSettingsRequest{Theme: proto.String("light")}))
	if err != nil || up.Msg.Settings.Theme != "light" || !up.Msg.Settings.Notifications {
		t.Fatalf("%v %+v", err, up)
	}
	up, err = h.client.UpdateDeviceSettings(h.ctx, connect.NewRequest(&agentv1.UpdateDeviceSettingsRequest{Notifications: proto.Bool(false)}))
	if err != nil || up.Msg.Settings.Theme != "light" || up.Msg.Settings.Notifications {
		t.Fatalf("an update keeps the other field: %v %+v", err, up)
	}
	if _, err := h.client.UpdateDeviceSettings(h.ctx, connect.NewRequest(&agentv1.UpdateDeviceSettingsRequest{Theme: proto.String("neon")})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("%v", err)
	}
}

// countingDocs is the user's settings document on the server.
type countingDocs struct {
	mu         sync.Mutex
	content    string
	version    int64
	gets, puts int
}

func (d *countingDocs) GetDocument(context.Context, *syncv1.GetDocumentRequest) (*syncv1.GetDocumentResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gets++
	if d.content == "" {
		return nil, &llm.APIError{Status: http.StatusNotFound, Message: "not found"}
	}
	return &syncv1.GetDocumentResponse{Document: &syncv1.Document{Version: d.version, Content: d.content}}, nil
}

func (d *countingDocs) PutDocument(_ context.Context, req *syncv1.PutDocumentRequest) (*syncv1.PutDocumentResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.puts++
	d.content, d.version = req.GetContent(), d.version+1
	return &syncv1.PutDocumentResponse{Result: &syncv1.PutDocumentResponse_Document{Document: &syncv1.Document{Version: d.version, Content: d.content}}}, nil
}
