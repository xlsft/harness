package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/config"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/outputstyle"
	"github.com/xlsft/harness/packages/client/plugins"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/skills"
	"github.com/xlsft/harness/packages/client/subagent"
)

// Plugins in a session. Package plugins says which may run: installed, enabled, approved on this device as they are now, allowed
// by policy, and in a trusted folder. Here what they hold joins the session's skills, hooks, MCP servers, agents and styles.
// Skills and styles are read again when they are next listed; hooks, MCP servers and agents when a session opens, like everything
// else of those kinds.

// PluginStore is this device's plugins. It holds nothing the server knows of.
func PluginStore() *plugins.Manager { return plugins.At(filepath.Join(config.Home(), "plugins")) }

// activePlugins returns the plugins that may run in a folder. HARNESS_NO_PLUGINS=1 turns them all off for this client.
func activePlugins(cwd string, trusted bool) []*plugins.Plugin {
	if os.Getenv("HARNESS_NO_PLUGINS") == "1" {
		return nil
	}
	return PluginStore().Active(plugins.Place{Cwd: cwd, Trusted: trusted})
}

// pluginSkillSources is what package skills reads for a folder.
func pluginSkillSources(cwd string, trusted bool) []skills.PluginSource {
	var out []skills.PluginSource
	for _, p := range activePlugins(cwd, trusted) {
		out = append(out, p.SkillSource())
	}
	return out
}

// pluginHookLayers is the hooks of the plugins for a folder. Unlike skills they are listed in a folder that isn't trusted, so the
// user can read why they don't run.
func pluginHookLayers(st *settings.Settings, cwd string) []settings.HookLayer {
	var out []settings.HookLayer
	for _, p := range activePlugins(cwd, true) {
		for _, block := range p.HookBlocks {
			l := settings.HookLayer{Source: settings.Source{Level: settings.LevelPlugin, Path: p.Root, Plugin: p.ID}, Raw: block, Hash: settings.ContentHash(block), Env: p.Env()}
			switch {
			case st != nil && st.HooksManagedOnly:
				l.Gate = settings.HookGateManaged
			case st != nil && !st.Trusted && cwd != "":
				l.Gate = settings.HookGateWorkspace
			}
			out = append(out, l)
		}
	}
	return out
}

// pluginMCPServers is the servers of the plugins for a folder. Those the policy's list of servers leaves out are dropped, with why.
func pluginMCPServers(cwd string, trusted bool) ([]*mcpv1.Server, []string) {
	pol := PluginStore().Policy()
	var out []*mcpv1.Server
	var warnings []string
	for _, p := range activePlugins(cwd, trusted) {
		names := make([]string, 0, len(p.MCP))
		for n := range p.MCP {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			full := plugins.MCPServerName(p.Name, n)
			if !pol.MCPAllowed(full, n) {
				warnings = append(warnings, fmt.Sprintf("MCP server %s is not allowed by your organization's policy", full))
				continue
			}
			s, err := mcp.PluginServer(p.ID, full, p.MCP[n], p.Vars())
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			out = append(out, s)
		}
	}
	return out, warnings
}

// pluginAgents is the agent types of the plugins for a folder.
func pluginAgents(cwd string, trusted bool) []subagent.Type {
	var defs []subagent.PluginDefinition
	for _, p := range activePlugins(cwd, trusted) {
		for _, a := range p.Agents {
			defs = append(defs, subagent.PluginDefinition{Name: a.Name, Path: a.Path, Root: p.Root, Data: p.Data})
		}
	}
	return subagent.LoadPluginDefinitions(defs)
}

// pluginStyles is the output styles of the plugins for a folder.
func pluginStyles(cwd string, trusted bool) []outputstyle.PluginStyle {
	var out []outputstyle.PluginStyle
	for _, p := range activePlugins(cwd, trusted) {
		for _, st := range p.Styles {
			out = append(out, outputstyle.PluginStyle{ID: st.ID, Path: st.Path})
		}
	}
	return out
}

// pluginWarnings says what waits for the user, once, as the session opens.
func pluginWarnings(cwd string, trusted bool) []string {
	if os.Getenv("HARNESS_NO_PLUGINS") == "1" {
		return nil
	}
	list, err := PluginStore().List(plugins.Place{Cwd: cwd, Trusted: trusted})
	if err != nil {
		return []string{"Plugins could not be read: " + err.Error()}
	}
	var waiting, changed []string
	for _, in := range list {
		switch in.Status {
		case plugins.StatusNeedsApproval:
			waiting = append(waiting, in.ID)
		case plugins.StatusChangedOnDisk:
			changed = append(changed, in.ID)
		}
	}
	var out []string
	if len(waiting) > 0 {
		out = append(out, "Plugins waiting for your approval, and not running: "+strings.Join(waiting, ", ")+". See /plugin.")
	}
	if len(changed) > 0 {
		out = append(out, "Plugins whose files changed since they were installed, and not running: "+strings.Join(changed, ", ")+".")
	}
	return out
}

// sourceLevel is the settings level a skill's source stands for, for its hooks.
func sourceLevel(source string) settings.Level {
	switch source {
	case skills.SourceOrganization:
		return settings.LevelOrg
	case skills.SourceProject:
		return settings.LevelProject
	case skills.SourcePlugin:
		return settings.LevelPlugin
	}
	return settings.LevelUser
}

// frontmatterHooks reads a hooks block of a skill or agent definition. What can configure one was gated on the way here (a project
// file needs the folder trusted, a plugin the device's approval), except a document of the server, which needs this device's
// trust like any hook it delivers, and allowManagedHooksOnly, which stops them all. Hooks that won't run come back in warnings.
func (s *Session) frontmatterHooks(raw json.RawMessage, level settings.Level, fromServer bool, plugin string, env []string, what string) ([]*hooks.Hook, []string) {
	l := settings.HookLayer{Source: settings.Source{Level: level, Plugin: plugin}, Raw: raw, Hash: settings.ContentHash(raw), Env: env}
	st := s.loadSettings()
	switch {
	case st != nil && st.HooksManagedOnly:
		l.Gate = settings.HookGateManaged
	case fromServer:
		l.Gate = settings.HookGateDevice
	}
	var active []*hooks.Hook
	var warnings []string
	for _, h := range hooks.ParseBlock(l, HookTrustStore().Trusted) {
		if h.Status != hooks.StatusActive {
			warnings = append(warnings, fmt.Sprintf("A hook of %s does not run: %s", what, h.Reason))
			continue
		}
		active = append(active, h)
	}
	return active, warnings
}

// skillHooks reads a skill's hooks, for its activation.
func (s *Session) skillHooks(sk skills.Skill) ([]*hooks.Hook, []string) {
	if len(sk.Hooks) == 0 {
		return nil, nil
	}
	var env []string
	if sk.PluginRoot != "" {
		env = []string{"CLAUDE_PLUGIN_ROOT=" + sk.PluginRoot, "HARNESS_PLUGIN_ROOT=" + sk.PluginRoot, "CLAUDE_PLUGIN_DATA=" + sk.PluginData, "HARNESS_PLUGIN_DATA=" + sk.PluginData}
	}
	return s.frontmatterHooks(sk.Hooks, sourceLevel(sk.Source), sk.FromServer(), sk.Plugin, env, "the skill "+sk.Name)
}

// agentHooks starts the hooks of an agent definition for one agent. They see that agent's events only, and its Stop is the
// subagent's: SubagentStop in the settings' terms.
func (s *Session) agentHooks(t subagent.Type, agentID string) {
	if len(t.Hooks) == 0 {
		return
	}
	list, warnings := s.frontmatterHooks(t.Hooks, settings.LevelUser, false, "", nil, "the agent type "+t.Name)
	s.warnAll(warnings)
	for _, h := range list {
		if h.Event == hooks.Stop {
			h.Event = hooks.SubagentStop
		}
	}
	s.agentHookGrants.add(agentID, s.Hooks.AddScoped(list, agentID))
}

// applyPluginPolicy takes in what the POLICY documents say about plugins, and fetches what they distribute in the background.
// Fetching runs nothing: each device still approves.
func applyPluginPolicy(ctx context.Context, docs []*syncv1.Document, log *slog.Logger) {
	store := PluginStore()
	pol := plugins.PolicyFromDocs(docs)
	plugins.SetPolicy(store.Root, pol)
	if len(pol.Distributed)+len(pol.Plugins) == 0 || os.Getenv("HARNESS_NO_PLUGINS") == "1" {
		return
	}
	go func() {
		for _, err := range store.SyncDistributed(ctx) {
			log.Warn("plugin distribution", "err", err)
		}
	}()
}
