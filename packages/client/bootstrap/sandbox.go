package bootstrap

import (
	"context"

	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/sandbox"
	"github.com/xlsft/harness/packages/client/settings"
)

// sandboxSettings reads the merged settings of the session's folder again for every command, so that the user's switch
// applies from the next one, wherever it was changed. When they cannot be read the sandbox stays on.
func (s *Session) sandboxSettings() sandbox.Settings {
	var docs settings.DocSource
	if s.Sync != nil {
		docs = s.Sync.Cache()
	}
	st, err := settings.Load(context.Background(), settings.Options{Cwd: s.Dir(), Docs: docs, Trusted: s.Trusted})
	if err != nil {
		return sandbox.Settings{Enabled: true}
	}
	b := st.Sandbox
	return sandbox.Settings{Enabled: b.Enabled, Forced: b.Forced, AllowedHosts: b.AllowedHosts, AllowedWritePaths: b.AllowedWritePaths,
		AllowLocalhost: b.AllowLocalhost, AutoAllowBash: b.AutoAllowBash, OutsideWithoutPrompt: b.OutsideWithoutPrompt, OutsideRefused: b.OutsideRefused}
}

// sandboxState is the permission engine's view of the sandbox. A sandbox that is required and cannot apply counts as
// active: its commands are refused, and leaving it still takes the user.
func (s *Session) sandboxState() permissions.SandboxState {
	st := s.Sandbox.State()
	return permissions.SandboxState{Active: st.Active || st.Refusing, AutoAllow: st.Active && st.Settings.AutoAllowBash,
		OutsideWithoutPrompt: st.Settings.OutsideWithoutPrompt, OutsideRefused: st.Settings.OutsideRefused}
}
