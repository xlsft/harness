package bootstrap

import (
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/toolsearch"
)

// Tool deferral (docs/design/tool-deferral.md): the session's loader holds the deferred built-in and MCP tools and the
// ones loaded. It is made before MCP opens, so the MCP manager adds its deferred tools to it.

// deferBuiltinTools decides whether this session defers built-in tools: the organization's policy when it sets it,
// else the user's preference (default on). It is read once, when the session opens: the tool list of a running
// session must not change under the prompt cache.
func (s *Session) deferBuiltinTools(o Options, st *settings.Settings) bool {
	if _, ok := s.Bundle.Tools[toolsearch.Name]; !ok {
		return false // a server whose bundle has no ToolSearch: nothing could load a deferred tool
	}
	if o.ModePolicy != nil {
		if v := o.ModePolicy.Managed().DeferBuiltinTools; v != nil {
			return *v
		}
	}
	return st == nil || st.DeferBuiltinTools
}

// openDeferral makes the session's loader with the built-in tools as a source.
func (s *Session) openDeferral(o Options, st *settings.Settings) {
	s.Deferral = toolsearch.NewLoader(s.deferBuiltinTools(o, st))
	if s.Deferral.BuiltinDeferral() {
		s.Deferral.AddSource(toolsearch.SourceFunc(s.deferredBuiltinDefs))
	}
}

// deferredBuiltinDefs is the built-in source: the registered tools the classification defers, that the bundle defines
// and that are neither disabled nor hidden, in registration order.
func (s *Session) deferredBuiltinDefs() []llm.ToolDef {
	b := s.Bundle
	if s.Agent != nil {
		b = s.Agent.Bundle()
	}
	var names []string
	for _, n := range s.Tools.Names() {
		if !s.Deferral.DefersBuiltin(n) || (s.Permissions != nil && s.Permissions.IsDisabled(n)) {
			continue
		}
		if t, ok := s.Tools.Get(n); ok {
			if h, ok := t.(tools.ModelHider); ok && h.HiddenFromModel() {
				continue
			}
		}
		names = append(names, n)
	}
	return b.ToolDefs(names)
}

// registerToolSearch registers ToolSearch when anything is deferred (built-in tools, or MCP tools by tool search).
func (s *Session) registerToolSearch() {
	if s.Deferral.BuiltinDeferral() || s.MCP.Defers() {
		s.Tools.Register(&toolsearch.Tool{L: s.Deferral})
	}
}

// startDeferral runs once the agent exists: a resumed session loads again what it had loaded (its record, then its
// history for transcripts from before the record), and new loads are recorded from now on.
func (s *Session) startDeferral(conv session.Conversation) {
	s.Deferral.Restore(conv.LoadedTools)
	recorded := len(s.Deferral.Order())
	s.Deferral.RestoreFromHistory(conv.Messages)
	if found := s.Deferral.Order()[recorded:]; len(found) > 0 && s.Transcript != nil {
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, LoadedTools: found})
	}
	s.Deferral.OnLoad(func(names []string) {
		if s.Transcript != nil {
			_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, LoadedTools: names})
		}
	})
}
