package bootstrap

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/tools"
)

// Why a tool the session knows is not offered to the model (ToolStatus.Reason).
const (
	ToolReasonDisabled    = "disabled"     // a permission rule (deny without a pattern) takes it away
	ToolReasonHidden      = "hidden"       // the tool hides itself (a device-local switch is off)
	ToolReasonNoPrompt    = "no_prompt"    // the session's prompt bundle has no definition for it
	ToolReasonNeedsSync   = "needs_sync"   // it needs the server's sync service, which this session does not have
	ToolReasonMCPDeferred = "mcp_deferred" // an MCP tool the model loads with ToolSearch
)

// ToolStatus is one tool of a session and whether the model is offered it now.
type ToolStatus struct {
	Name string
	// Source: "builtin", "mcp" or "browser" (the fallback browser definitions).
	Source  string
	Offered bool
	// Reason and Detail say why a tool is not offered (Reason is one of the ToolReason constants).
	Reason, Detail string
}

// libraryToolNames are the tools RegisterLibraryTools adds: they need the server's sync service.
var libraryToolNames = func() []string {
	r := tools.NewRegistry()
	tools.RegisterLibraryTools(r)
	return r.Names()
}()

// ToolReport lists the tools offered to the model in this session right now (in request order), then every registered
// tool that is not, with the reason, then the tools that need server sync when the session has none.
func (s *Session) ToolReport() []ToolStatus {
	var out []ToolStatus
	offered := map[string]bool{}
	mcpTools := map[string]bool{}
	for _, t := range s.MCP.Catalog() {
		mcpTools[t.Def.Name] = true
	}
	browserFallback := map[string]bool{}
	for _, d := range s.browserFallbackDefs() {
		browserFallback[d.Name] = true
	}
	for _, d := range s.Agent.OfferedTools() {
		if offered[d.Name] {
			continue
		}
		offered[d.Name] = true
		src := "builtin"
		switch {
		case mcpTools[d.Name]:
			src = "mcp"
		case browserFallback[d.Name]:
			src = "browser"
		}
		out = append(out, ToolStatus{Name: d.Name, Source: src, Offered: true})
	}
	bundle := s.Agent.Bundle()
	deferred := map[string]bool{}
	for _, n := range s.MCP.Deferred() {
		deferred[n] = true
	}
	var rest []ToolStatus
	for _, n := range s.Tools.Names() {
		if offered[n] {
			continue
		}
		st := ToolStatus{Name: n, Source: "builtin"}
		if mcpTools[n] {
			st.Source = "mcp"
		}
		t, _ := s.Tools.Get(n)
		h, hides := t.(tools.ModelHider)
		_, inBundle := bundle.Tools[n]
		switch {
		case s.Agent.ToolDisabled(n) || s.Permissions.IsDisabled(n):
			st.Reason, st.Detail = ToolReasonDisabled, "a permission rule denies it"
		case hides && h.HiddenFromModel():
			st.Reason, st.Detail = ToolReasonHidden, "the tool is switched off on this device"
		case deferred[n]:
			st.Reason, st.Detail = ToolReasonMCPDeferred, "deferred: the model loads it with ToolSearch"
		case !inBundle && !mcpTools[n]:
			st.Reason, st.Detail = ToolReasonNoPrompt, fmt.Sprintf("the prompt bundle %s has no definition for it", bundle.Version)
		default:
			st.Reason, st.Detail = ToolReasonMCPDeferred, "not offered by its MCP server now"
		}
		rest = append(rest, st)
	}
	if s.Sync == nil {
		why := s.syncOffReason()
		for _, n := range libraryToolNames {
			if _, ok := s.Tools.Get(n); !ok {
				rest = append(rest, ToolStatus{Name: n, Source: "builtin", Reason: ToolReasonNeedsSync, Detail: why})
			}
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	return append(out, rest...)
}

// syncOffReason says why the session has no sync client.
func (s *Session) syncOffReason() string {
	if s.SyncOff != "" {
		return s.SyncOff
	}
	return "the session has no server connection"
}

// SyncState says in words whether the session has server sync now.
func (s *Session) SyncState() string {
	switch {
	case s.Sync != nil && s.Sync.Online():
		return "sync: on"
	case s.Sync != nil:
		return "sync: on, offline (serving the cached documents until the server answers)"
	}
	return "sync: off, " + s.syncOffReason()
}

// syncWarning is the session warning when a signed-in session runs without sync: the tools that are off and why.
func syncWarning(reason string) string {
	return fmt.Sprintf("Server sync is unavailable (%s): %s are off, and settings, memory and transcripts stay on this device.",
		reason, strings.Join(slices.Clone(libraryToolNames), ", "))
}

// FormatToolReport renders a report for a terminal or a slash command reply.
func FormatToolReport(list []ToolStatus) string {
	var on, off []string
	for _, t := range list {
		if t.Offered {
			name := t.Name
			if t.Source != "builtin" {
				name += " (" + t.Source + ")"
			}
			on = append(on, name)
			continue
		}
		off = append(off, fmt.Sprintf("- %s: %s (%s)", t.Name, t.Detail, t.Reason))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Offered to the model (%d): %s\n", len(on), strings.Join(on, ", "))
	if len(off) > 0 {
		fmt.Fprintf(&b, "\nNot offered (%d):\n%s\n", len(off), strings.Join(off, "\n"))
	}
	return b.String()
}
