package mcp

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
)

// projectEntry is one server of a repository's .mcp.json (Claude Code format).
type projectEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// ReadProjectConfig parses a .mcp.json file into server configs (SCOPE_PROJECT, enabled). Entries
// with an unknown transport are skipped.
func ReadProjectConfig(path, projectKey string) ([]*mcpv1.Server, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		MCPServers map[string]projectEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	var out []*mcpv1.Server
	for _, name := range slices.Sorted(maps.Keys(f.MCPServers)) {
		e := f.MCPServers[name]
		s := &mcpv1.Server{Name: name, Scope: commonv1.Scope_SCOPE_PROJECT, ProjectKey: projectKey, Enabled: true}
		switch strings.ToLower(e.Type) {
		case "", "stdio":
			if e.Command == "" {
				if e.URL == "" {
					continue
				}
				s.Transport = mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP
				s.Remote = &mcpv1.RemoteConfig{Url: e.URL, Headers: e.Headers}
				break
			}
			s.Transport = mcpv1.Transport_TRANSPORT_STDIO
			s.Stdio = &mcpv1.StdioConfig{Command: e.Command, Args: e.Args, Env: e.Env}
		case "http", "streamable-http", "streamable_http":
			s.Transport = mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP
			s.Remote = &mcpv1.RemoteConfig{Url: e.URL, Headers: e.Headers}
		case "sse":
			s.Transport = mcpv1.Transport_TRANSPORT_SSE
			s.Remote = &mcpv1.RemoteConfig{Url: e.URL, Headers: e.Headers}
		case "ws", "websocket":
			s.Transport = mcpv1.Transport_TRANSPORT_WS
			s.Remote = &mcpv1.RemoteConfig{Url: e.URL, Headers: e.Headers}
		default:
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// sameLaunchConfig compares the fields .mcp.json controls.
func sameLaunchConfig(a, b *mcpv1.Server) bool {
	if a.GetTransport() != b.GetTransport() {
		return false
	}
	if a.GetTransport() == mcpv1.Transport_TRANSPORT_STDIO {
		return a.GetStdio().GetCommand() == b.GetStdio().GetCommand() &&
			slices.Equal(a.GetStdio().GetArgs(), b.GetStdio().GetArgs()) &&
			maps.Equal(a.GetStdio().GetEnv(), b.GetStdio().GetEnv())
	}
	return a.GetRemote().GetUrl() == b.GetRemote().GetUrl() && maps.Equal(a.GetRemote().GetHeaders(), b.GetRemote().GetHeaders())
}

// syncProject registers the repository's .mcp.json servers on the server (SCOPE_PROJECT +
// project_key), creating missing ones and updating changed launch configs. Server-side fields
// (enabled, always_load, secrets, overrides) are kept. It reports whether anything changed.
func (m *Manager) syncProject(ctx context.Context, existing []*mcpv1.Server) bool {
	if m.o.ProjectKey == "" {
		return false
	}
	want, err := ReadProjectConfig(m.o.ProjectConfig, m.o.ProjectKey)
	if err != nil {
		if !os.IsNotExist(err) {
			m.warn("%s could not be read: %v", m.o.ProjectConfig, err)
		}
		return false
	}
	have := map[string]*mcpv1.Server{}
	for _, s := range existing {
		if s.GetScope() == commonv1.Scope_SCOPE_PROJECT && s.GetProjectKey() == m.o.ProjectKey {
			have[s.GetName()] = s
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].GetName() < want[j].GetName() })
	changed := false
	for _, w := range want {
		if m.o.PolicyAllows != nil && !m.o.PolicyAllows(w.GetName()) {
			m.warn("project MCP server %s is not allowed by your organization's policy", w.GetName())
			continue
		}
		cur := have[w.GetName()]
		switch {
		case cur == nil:
			if _, err := m.o.Client.CreateServer(ctx, w); err != nil {
				m.warn("project MCP server %s could not be registered: %s", w.GetName(), errorMessage(err))
				continue
			}
			changed = true
		case !sameLaunchConfig(cur, w):
			upd := cloneServer(cur)
			upd.Transport, upd.Stdio = w.GetTransport(), w.GetStdio()
			if w.GetRemote() != nil {
				r := w.GetRemote()
				if old := cur.GetRemote(); old != nil {
					r.Oauth, r.RequiresUserInteraction = old.GetOauth(), old.GetRequiresUserInteraction()
				}
				upd.Remote = r
			} else {
				upd.Remote = nil
			}
			if _, err := m.o.Client.UpdateServer(ctx, upd, cur.GetVersion()); err != nil {
				m.warn("project MCP server %s could not be updated: %s", w.GetName(), errorMessage(err))
				continue
			}
			changed = true
		}
	}
	return changed
}

func cloneServer(s *mcpv1.Server) *mcpv1.Server {
	return &mcpv1.Server{
		Id: s.GetId(), Name: s.GetName(), Scope: s.GetScope(), Transport: s.GetTransport(), Enabled: s.GetEnabled(),
		AlwaysLoad: s.GetAlwaysLoad(), Stdio: s.GetStdio(), Remote: s.GetRemote(), Overrides: s.GetOverrides(),
		TargetRoles: s.GetTargetRoles(), ProjectKey: s.GetProjectKey(), AllowPrivateNetwork: s.GetAllowPrivateNetwork(),
	}
}
