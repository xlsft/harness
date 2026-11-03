package bootstrap

import (
	"github.com/xlsft/harness/packages/client/browser"
	"github.com/xlsft/harness/packages/client/llm"
)

// extraTools is the agent's ExtraTools hook: the MCP tools, plus the browser tools' built-in definitions when
// the prompt bundle has none (a server older than the browser tools).
func (s *Session) extraTools(useBrowser bool) func() []llm.ToolDef {
	mcpDefs := s.mcpExtraTools()
	if !useBrowser {
		return mcpDefs
	}
	return func() []llm.ToolDef {
		var out []llm.ToolDef
		if mcpDefs != nil {
			out = mcpDefs()
		}
		return append(out, s.browserFallbackDefs()...)
	}
}

// browserFallbackDefs returns the fallback browser tool definitions when the session has a browser and the
// bundle defines none of its tools (a bundle that defines some made its choice, per-model overrides included).
func (s *Session) browserFallbackDefs() []llm.ToolDef {
	if s.browsers == nil || s.Agent == nil {
		return nil
	}
	b := s.Agent.Bundle()
	if b == nil {
		return nil
	}
	for _, n := range browser.ToolNames {
		if _, ok := b.Tools[n]; ok {
			return nil
		}
	}
	var out []llm.ToolDef
	for _, d := range browser.FallbackToolDefs() {
		if s.Permissions == nil || !s.Permissions.IsDisabled(d.Name) {
			out = append(out, d)
		}
	}
	return out
}
