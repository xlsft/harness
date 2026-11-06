package bootstrap

import (
	"context"
	"path/filepath"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
)

// Hooks of a session. The set is read from the settings when the session opens and when it moves, not again: a change to a
// settings file reaches the next session, and the model cannot give itself a hook mid-session. Only the switch is read live.
//
// They run outside the OS sandbox on purpose, like MCP servers: the sandbox confines what the model chooses to run, and a hook
// is what the user or an administrator chose. Its job often is what the sandbox refuses (a formatter writing where it likes, a
// notifier reaching the network), and a hook that polices the model has to stand outside what it polices. The gates are on who
// can configure one: project files need the workspace trusted, what the server delivers needs this device's trust, and the
// sandbox and the permission engine both keep the model from writing to .claude and .harness.

// HookTrustStore is this device's record of the hooks from the server it trusts.
func HookTrustStore() *hooks.TrustStore {
	return &hooks.TrustStore{Path: filepath.Join(config.Home(), "hook-trust.json")}
}

// BuildHooks reads the hooks of merged settings with this device's trust.
func BuildHooks(st *settings.Settings) *hooks.Set {
	return hooks.Build(st, HookTrustStore().Trusted)
}

// newHookEngine makes the session's engine. Nothing runs before Open returns.
func (s *Session) newHookEngine(st *settings.Settings) *hooks.Engine {
	return hooks.New(BuildHooks(st), hooks.Options{
		SessionID:      s.ID,
		TranscriptPath: func() string { return s.Transcript.Path() },
		Cwd:            s.Dir,
		ProjectDir:     s.Dir,
		Mode:           func() string { return string(s.Permissions.Mode()) },
		ExtraEnv:       s.ToolEnv.ExtraEnv,
		Switches:       s.hookSwitches,
		OnRun:          s.onHookRun,
		Logger:         s.log,
	})
}

// hookSwitches reads disableAllHooks from the settings as they are now, so the user's switch reaches a running session. When
// they cannot be read hooks stay as they were configured.
func (s *Session) hookSwitches() (off, policyOff bool) {
	var docs settings.DocSource
	if s.Sync != nil {
		docs = s.Sync.Cache()
	}
	st, err := settings.Load(context.Background(), settings.Options{Cwd: s.Dir(), Docs: docs, Trusted: s.IsTrusted()})
	if err != nil {
		return false, false
	}
	return st.HooksOff, st.HooksPolicyOff
}

// HookSwitches is hookSwitches for frontends listing the session's hooks.
func (s *Session) HookSwitches() (off, policyOff bool) { return s.hookSwitches() }

// onHookRun shows a handler in the session: subagents' hooks too, in the session's transcript, which is where the user reads
// them.
func (s *Session) onHookRun(r hooks.Run) {
	if r.Status != hooks.RunRunning {
		entry := r
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeHookRun, Level: "info", Content: "Hook " + r.Event, HookRun: &entry})
	}
	s.Agent.Emit(agent.Event{Kind: agent.EventHookRun, HookRun: &r})
}

// permissionPromptHook runs when a tool call is about to prompt the user: PermissionRequest hooks may answer for the user.
// Deny rules were judged before this point, and a prompt only the user may answer keeps going to the user whatever they say.
func (s *Session) permissionPromptHook(ctx context.Context, req permissions.Request) (permissions.Decision, bool) {
	out := s.Hooks.Dispatch(ctx, hooks.PermissionRequest, req.ToolName, hooks.Input{ToolName: req.ToolName, ToolInput: req.Input, ToolUseID: req.ToolUseID,
		PermissionSuggestions: req.Suggestions, Reason: req.Reason, AgentID: req.AgentID})
	if a := out.Answer; a != nil && !req.UserOnly {
		if a.Allow {
			return permissions.Decision{Behavior: permissions.Allow, ResolvedBy: "hook", UpdatedInput: a.UpdatedInput}, true
		}
		message := a.Message
		if message == "" {
			message = "A hook refused this tool call."
		}
		return permissions.Decision{Behavior: permissions.Deny, ResolvedBy: "hook", Message: message, Interrupt: a.Interrupt}, true
	}
	s.Hooks.Fire(hooks.Notification, "permission_prompt", hooks.Input{NotificationType: "permission_prompt", Title: "Permission needed", Message: "Allow " + req.PromptName() + "?", AgentID: req.AgentID})
	return permissions.Decision{}, false
}

// StartHooks runs SessionStart in the background, once the caller can show what happens. The first turn waits for it, so that
// what it adds reaches the first request; opening the session does not.
func (s *Session) StartHooks() {
	if !s.Hooks.Set().Has(hooks.SessionStart) {
		return
	}
	source := "startup"
	if s.Resumed {
		source = "resume"
	}
	done := make(chan struct{})
	s.Agent.SetStartGate(done)
	started := s.Hooks.Spawn(func() {
		defer close(done)
		out := s.Hooks.Dispatch(s.Hooks.Context(), hooks.SessionStart, source, hooks.Input{Source: source, Model: s.Agent.Model()})
		s.Agent.AddHookContext(hooks.SessionStart, out.Context...)
	})
	if !started {
		close(done)
	}
}

// relocateHooks replaces the hooks of the folder the session left with those of the one it moved to, under that folder's trust.
func (s *Session) relocateHooks(st *settings.Settings, from, to string) {
	s.Hooks.Replace(BuildHooks(st))
	s.Hooks.Fire(hooks.CwdChanged, "", hooks.Input{OldCwd: from, NewCwd: to})
}

// SetEndReason says why the session is closing, for SessionEnd.
func (s *Session) SetEndReason(reason string) {
	s.placeMu.Lock()
	s.endReason = reason
	s.placeMu.Unlock()
}

// endHooks runs SessionEnd and stops what still runs.
func (s *Session) endHooks() {
	s.hooksEnded.Do(func() {
		s.placeMu.RLock()
		reason := s.endReason
		s.placeMu.RUnlock()
		if reason == "" {
			reason = "other"
		}
		s.Hooks.End(reason)
		s.Hooks.Close()
	})
}
