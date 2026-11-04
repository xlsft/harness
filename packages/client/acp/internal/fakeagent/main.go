// Command fakeagent is a tiny ACP agent for tests. It speaks ACP over stdio and scripts its replies by the
// prompt text:
//
//	"hello"  thought + text chunks, end_turn with usage
//	"tool"   a pending execute tool call, a permission request; allowed: runs `echo hi` in a client terminal,
//	         writes out.txt through fs/write_text_file (diff content), updates the plan, says "done";
//	         rejected: the call fails and it says "denied"
//	"slow"   one chunk, then waits for session/cancel and stops with "cancelled"
//	"mode"   switches to the "plan" mode
//	"limits" sends a rate-limit snapshot in an extension notification
//	"nearlimit" sends a snapshot with the 5h window 98% used
//	"ratelimited" fails the prompt with a usage-limit error
//	"newmodels" reports the models of FAKE_MODELS_FILE and the levels of FAKE_EFFORTS_FILE again in a config_option_update
//
// Environment: FAKE_AUTH=1 refuses session/new until "login" (agent auth) ran or FAKE_AUTH_FILE exists
// (terminal auth: `fakeagent --login` creates it); FAKE_LOAD=1 advertises loadSession; FAKE_MODELS_FILE holds the
// model config option, one "id=Name" per line, the first one current (no file or an empty one: no model option, as
// with an agent that has none); FAKE_EFFORTS_FILE holds its effort config option the same way, under the agent's own
// names; while FAKE_FAIL_FILE exists session/new fails; FAKE_LOG appends one line per
// process start and per request that matters to tests.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

type agent struct {
	conn *sdk.AgentSideConnection

	mu      sync.Mutex
	authed  bool
	cancels map[sdk.SessionId]context.CancelFunc
	cwd     map[sdk.SessionId]string
	model   map[sdk.SessionId]string
	effort  map[sdk.SessionId]string
	n       int
}

// logLine records what happened for the test that set FAKE_LOG.
func logLine(format string, args ...any) {
	f := os.Getenv("FAKE_LOG")
	if f == "" {
		return
	}
	w, err := os.OpenFile(f, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer w.Close()
	fmt.Fprintf(w, format+"\n", args...)
}

// selectOption is a config option as a file has it now, one "id=Name" per line; current empty is its first choice.
// nil without a file or choices.
func selectOption(file, id, name string, category sdk.SessionConfigOptionCategory, current string) []sdk.SessionConfigOption {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var opts sdk.SessionConfigSelectOptionsUngrouped
	for _, line := range strings.Split(string(b), "\n") {
		if id, name, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			opts = append(opts, sdk.SessionConfigSelectOption{Value: sdk.SessionConfigValueId(id), Name: name})
		}
	}
	if len(opts) == 0 {
		return nil
	}
	if current == "" {
		current = string(opts[0].Value)
	}
	return []sdk.SessionConfigOption{{Select: &sdk.SessionConfigOptionSelect{Id: sdk.SessionConfigId(id), Name: name, Type: "select", Category: &category,
		CurrentValue: sdk.SessionConfigValueId(current), Options: sdk.SessionConfigSelectOptions{Ungrouped: &opts}}}}
}

func effortOption(current string) []sdk.SessionConfigOption {
	return selectOption(os.Getenv("FAKE_EFFORTS_FILE"), "thoughts", "Thinking", sdk.SessionConfigOptionCategoryThoughtLevel, current)
}

// configOptions is the agent's config options as its files have them now.
func configOptions(model, effort string) []sdk.SessionConfigOption {
	return append(selectOption(os.Getenv("FAKE_MODELS_FILE"), "model", "Model", sdk.SessionConfigOptionCategoryModel, model), effortOption(effort)...)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--login" {
		if f := os.Getenv("FAKE_AUTH_FILE"); f != "" {
			_ = os.WriteFile(f, []byte("ok"), 0o600)
		}
		fmt.Println("logged in")
		return
	}
	logLine("spawn")
	a := &agent{cancels: map[sdk.SessionId]context.CancelFunc{}, cwd: map[sdk.SessionId]string{}, model: map[sdk.SessionId]string{}, effort: map[sdk.SessionId]string{}}
	a.conn = sdk.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	<-a.conn.Done()
}

func (a *agent) Initialize(ctx context.Context, p sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	desc := "Sign in in the browser"
	return sdk.InitializeResponse{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		AgentCapabilities: sdk.AgentCapabilities{
			LoadSession:         os.Getenv("FAKE_LOAD") == "1",
			PromptCapabilities:  sdk.PromptCapabilities{Image: false},
			SessionCapabilities: sdk.SessionCapabilities{Close: &sdk.SessionCloseCapabilities{}},
		},
		AgentInfo: &sdk.Implementation{Name: "fake", Version: "1.0.0"},
		AuthMethods: []sdk.AuthMethod{
			{Agent: &sdk.AuthMethodAgent{Id: "login", Name: "Log in", Description: &desc}},
			{Terminal: &sdk.AuthMethodTerminalInline{Id: "tlogin", Name: "Log in in a terminal", Type: "terminal", Args: []string{"--login"}}},
		},
	}, nil
}

func (a *agent) Authenticate(ctx context.Context, p sdk.AuthenticateRequest) (sdk.AuthenticateResponse, error) {
	if p.MethodId != "login" {
		return sdk.AuthenticateResponse{}, sdk.NewInvalidParams(map[string]any{"methodId": p.MethodId})
	}
	a.mu.Lock()
	a.authed = true
	a.mu.Unlock()
	return sdk.AuthenticateResponse{}, nil
}

func (a *agent) needsAuth() bool {
	if os.Getenv("FAKE_AUTH") != "1" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.authed {
		return false
	}
	if f := os.Getenv("FAKE_AUTH_FILE"); f != "" {
		if _, err := os.Stat(f); err == nil {
			return false
		}
	}
	return true
}

func modes() *sdk.SessionModeState {
	return &sdk.SessionModeState{CurrentModeId: "default", AvailableModes: []sdk.SessionMode{
		{Id: "default", Name: "Default"}, {Id: "acceptEdits", Name: "Accept edits"}, {Id: "plan", Name: "Plan"}}}
}

func (a *agent) NewSession(ctx context.Context, p sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	if a.needsAuth() {
		logLine("session/new auth required")
		return sdk.NewSessionResponse{}, sdk.NewAuthRequired(nil)
	}
	if _, err := os.Stat(os.Getenv("FAKE_FAIL_FILE")); err == nil {
		logLine("session/new failed")
		return sdk.NewSessionResponse{}, sdk.NewInternalError(map[string]any{"error": "the fake agent is broken"})
	}
	logLine("session/new %s", p.Cwd)
	a.mu.Lock()
	a.n++
	id := sdk.SessionId(fmt.Sprintf("fake-%d", a.n))
	a.cwd[id] = p.Cwd
	a.mu.Unlock()
	go func() {
		_ = a.conn.SessionUpdate(context.Background(), sdk.SessionNotification{SessionId: id, Update: sdk.SessionUpdate{
			AvailableCommandsUpdate: &sdk.SessionAvailableCommandsUpdate{SessionUpdate: "available_commands_update",
				AvailableCommands: []sdk.AvailableCommand{{Name: "review", Description: "Review the changes"}}}}})
	}()
	return sdk.NewSessionResponse{SessionId: id, Modes: modes(), ConfigOptions: configOptions("", "")}, nil
}

func (a *agent) LoadSession(ctx context.Context, p sdk.LoadSessionRequest) (sdk.LoadSessionResponse, error) {
	a.mu.Lock()
	a.cwd[p.SessionId] = p.Cwd
	a.mu.Unlock()
	// Replays the conversation; the client ignores it.
	_ = a.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: p.SessionId, Update: sdk.UpdateUserMessageText("old prompt")})
	_ = a.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: p.SessionId, Update: sdk.UpdateAgentMessageText("old reply")})
	return sdk.LoadSessionResponse{Modes: modes(), ConfigOptions: effortOption("")}, nil
}

func (a *agent) Cancel(ctx context.Context, p sdk.CancelNotification) error {
	a.mu.Lock()
	c := a.cancels[p.SessionId]
	a.mu.Unlock()
	if c != nil {
		c()
	}
	return nil
}

func (a *agent) send(ctx context.Context, id sdk.SessionId, u sdk.SessionUpdate) {
	_ = a.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: id, Update: u})
}

func (a *agent) Prompt(ctx context.Context, p sdk.PromptRequest) (sdk.PromptResponse, error) {
	text := ""
	for _, b := range p.Prompt {
		if b.Text != nil {
			text += b.Text.Text
		}
	}
	a.mu.Lock()
	logLine("prompt model=%s", a.model[p.SessionId])
	if e := a.effort[p.SessionId]; e != "" {
		logLine("effort at prompt %s", e)
	}
	a.mu.Unlock()
	tctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.cancels[p.SessionId] = cancel
	cwd := a.cwd[p.SessionId]
	a.mu.Unlock()
	defer cancel()
	id := p.SessionId
	usage := &sdk.Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}
	switch {
	case strings.Contains(text, "newmodels"):
		a.send(ctx, id, sdk.SessionUpdate{ConfigOptionUpdate: &sdk.SessionConfigOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: configOptions("", "")}})
		// The client handles the update after this answer otherwise.
		time.Sleep(20 * time.Millisecond)
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	case strings.Contains(text, "hello"):
		a.send(ctx, id, sdk.UpdateAgentThoughtText("Thinking it over."))
		a.send(ctx, id, sdk.UpdateAgentMessageText("Hello "))
		a.send(ctx, id, sdk.UpdateAgentMessageText("world"))
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn, Usage: usage}, nil
	case strings.Contains(text, "slow"):
		a.send(ctx, id, sdk.UpdateAgentMessageText("Working"))
		<-tctx.Done()
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
	case strings.Contains(text, "mode"):
		a.send(ctx, id, sdk.SessionUpdate{CurrentModeUpdate: &sdk.SessionCurrentModeUpdate{SessionUpdate: "current_mode_update", CurrentModeId: "plan"}})
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	case strings.Contains(text, "nearlimit"):
		_ = a.conn.NotifyExtension(ctx, "_codex/token_count", map[string]any{"rate_limits": map[string]any{
			"primary": map[string]any{"used_percent": 98, "window_minutes": 300, "resets_in_seconds": 3600}}})
		a.send(ctx, id, sdk.UpdateAgentMessageText("ok"))
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	case strings.Contains(text, "ratelimited"):
		return sdk.PromptResponse{}, sdk.NewInternalError(map[string]any{"error": "You've hit your usage limit. Try again later."})
	case strings.Contains(text, "limits"):
		_ = a.conn.NotifyExtension(ctx, "_codex/token_count", map[string]any{"rate_limits": map[string]any{
			"primary":   map[string]any{"used_percent": 42, "window_minutes": 300, "resets_in_seconds": 3600},
			"secondary": map[string]any{"used_percent": 18, "window_minutes": 10080}}})
		a.send(ctx, id, sdk.UpdateAgentMessageText("ok"))
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	case strings.Contains(text, "tool"):
		return a.toolTurn(ctx, tctx, id, cwd)
	}
	a.send(ctx, id, sdk.UpdateAgentMessageText("?"))
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

func (a *agent) toolTurn(ctx, tctx context.Context, id sdk.SessionId, cwd string) (sdk.PromptResponse, error) {
	a.send(ctx, id, sdk.UpdateAgentMessageText("Let me run it."))
	a.send(ctx, id, sdk.StartToolCall("call_1", "Run echo hi", sdk.WithStartKind(sdk.ToolKindExecute), sdk.WithStartStatus(sdk.ToolCallStatusPending)))
	resp, err := a.conn.RequestPermission(ctx, sdk.RequestPermissionRequest{SessionId: id,
		ToolCall: sdk.ToolCallUpdate{ToolCallId: "call_1", RawInput: map[string]any{"command": "echo hi"}},
		Options: []sdk.PermissionOption{
			{OptionId: "allow", Name: "Allow", Kind: sdk.PermissionOptionKindAllowOnce},
			{OptionId: "always", Name: "Always allow", Kind: sdk.PermissionOptionKindAllowAlways},
			{OptionId: "reject", Name: "Reject", Kind: sdk.PermissionOptionKindRejectOnce},
		}})
	if err != nil {
		return sdk.PromptResponse{}, err
	}
	if resp.Outcome.Cancelled != nil || tctx.Err() != nil {
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
	}
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId == "reject" {
		a.send(ctx, id, sdk.UpdateToolCall("call_1", sdk.WithUpdateStatus(sdk.ToolCallStatusFailed)))
		a.send(ctx, id, sdk.UpdateAgentMessageText("denied"))
		return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
	}
	term, err := a.conn.CreateTerminal(ctx, sdk.CreateTerminalRequest{SessionId: id, Command: "echo", Args: []string{"hi"}})
	if err != nil {
		return sdk.PromptResponse{}, err
	}
	if _, err := a.conn.WaitForTerminalExit(ctx, sdk.WaitForTerminalExitRequest{SessionId: id, TerminalId: term.TerminalId}); err != nil {
		return sdk.PromptResponse{}, err
	}
	a.send(ctx, id, sdk.UpdateToolCall("call_1", sdk.WithUpdateStatus(sdk.ToolCallStatusCompleted),
		sdk.WithUpdateContent([]sdk.ToolCallContent{sdk.ToolTerminalRef(term.TerminalId)})))
	_, _ = a.conn.ReleaseTerminal(ctx, sdk.ReleaseTerminalRequest{SessionId: id, TerminalId: term.TerminalId})

	path := filepath.Join(cwd, "out.txt")
	a.send(ctx, id, sdk.StartToolCall("call_2", "Write out.txt", sdk.WithStartKind(sdk.ToolKindEdit), sdk.WithStartStatus(sdk.ToolCallStatusInProgress),
		sdk.WithStartLocations([]sdk.ToolCallLocation{{Path: path}})))
	if _, err := a.conn.WriteTextFile(ctx, sdk.WriteTextFileRequest{SessionId: id, Path: path, Content: "one\ntwo\n"}); err != nil {
		return sdk.PromptResponse{}, err
	}
	a.send(ctx, id, sdk.UpdateToolCall("call_2", sdk.WithUpdateStatus(sdk.ToolCallStatusCompleted),
		sdk.WithUpdateContent([]sdk.ToolCallContent{sdk.ToolDiffContent(path, "one\ntwo\n")})))
	a.send(ctx, id, sdk.UpdatePlan(sdk.PlanEntry{Content: "Run echo", Status: sdk.PlanEntryStatusCompleted, Priority: sdk.PlanEntryPriorityMedium},
		sdk.PlanEntry{Content: "Write file", Status: sdk.PlanEntryStatusInProgress, Priority: sdk.PlanEntryPriorityMedium}))
	a.send(ctx, id, sdk.UpdateAgentMessageText("done"))
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn, Usage: &sdk.Usage{InputTokens: 300, OutputTokens: 50, TotalTokens: 350}}, nil
}

func (a *agent) SetSessionMode(ctx context.Context, p sdk.SetSessionModeRequest) (sdk.SetSessionModeResponse, error) {
	logLine("set_mode %s", p.ModeId)
	return sdk.SetSessionModeResponse{}, nil
}

func (a *agent) Logout(ctx context.Context, p sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, nil
}

func (a *agent) CloseSession(ctx context.Context, p sdk.CloseSessionRequest) (sdk.CloseSessionResponse, error) {
	logLine("session/close")
	return sdk.CloseSessionResponse{}, nil
}

func (a *agent) ListSessions(ctx context.Context, p sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	return sdk.ListSessionsResponse{}, nil
}

func (a *agent) ResumeSession(ctx context.Context, p sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	return sdk.ResumeSessionResponse{}, sdk.NewMethodNotFound("session/resume")
}

func (a *agent) SetSessionConfigOption(ctx context.Context, p sdk.SetSessionConfigOptionRequest) (sdk.SetSessionConfigOptionResponse, error) {
	if p.ValueId == nil {
		return sdk.SetSessionConfigOptionResponse{}, sdk.NewInvalidParams(map[string]any{"error": "a value is required"})
	}
	a.mu.Lock()
	if p.ValueId.ConfigId == "thoughts" {
		logLine("set_effort %s", p.ValueId.Value)
		a.effort[p.ValueId.SessionId] = string(p.ValueId.Value)
	} else {
		logLine("set_model %s", p.ValueId.Value)
		a.model[p.ValueId.SessionId] = string(p.ValueId.Value)
	}
	opts := configOptions(a.model[p.ValueId.SessionId], a.effort[p.ValueId.SessionId])
	a.mu.Unlock()
	if opts == nil {
		opts = []sdk.SessionConfigOption{}
	}
	return sdk.SetSessionConfigOptionResponse{ConfigOptions: opts}, nil
}
