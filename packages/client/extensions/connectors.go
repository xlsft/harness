package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strings"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/plugins"
)

// endpointKey is what makes two servers the same one to look at. A command that names its plugin's own folder is that plugin's alone.
func endpointKey(remote bool, endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if !remote {
		if strings.Contains(endpoint, "PLUGIN_ROOT") || strings.Contains(endpoint, "PLUGIN_DATA") {
			return ""
		}
		return "cmd:" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "url:" + endpoint
	}
	return "url:" + strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/") + "?" + u.RawQuery
}

func isRemoteTransport(t string) bool {
	switch strings.ToLower(t) {
	case "", "stdio":
		return false
	}
	return true
}

func transportName(t mcpv1.Transport) string {
	switch t {
	case mcpv1.Transport_TRANSPORT_STDIO:
		return "stdio"
	case mcpv1.Transport_TRANSPORT_SSE:
		return "sse"
	case mcpv1.Transport_TRANSPORT_WS:
		return "ws"
	case mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP:
		return "http"
	}
	return ""
}

// entryStatus reads a managed server's state without connecting to it.
func entryStatus(e *agentv1.McpServerEntry) (agentv1.ConnectorStatus, string) {
	d := e.GetStateDetail()
	switch e.GetState() {
	case agentv1.McpServerState_MCP_SERVER_STATE_READY:
		if e.GetServer().GetAuth().GetState() == mcpv1.AuthState_AUTH_STATE_CONNECTED {
			return agentv1.ConnectorStatus_CONNECTOR_STATUS_CONNECTED, d
		}
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_IN_SESSIONS, d
	case agentv1.McpServerState_MCP_SERVER_STATE_DISABLED:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_OFF, d
	case agentv1.McpServerState_MCP_SERVER_STATE_NEEDS_TRUST:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_TRUST, d
	case agentv1.McpServerState_MCP_SERVER_STATE_NEEDS_APPROVAL:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_APPROVAL, d
	case agentv1.McpServerState_MCP_SERVER_STATE_DENIED:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_BLOCKED, d
	case agentv1.McpServerState_MCP_SERVER_STATE_NEEDS_AUTH:
		if e.GetServer().GetAuth().GetState() == mcpv1.AuthState_AUTH_STATE_ERROR {
			return agentv1.ConnectorStatus_CONNECTOR_STATUS_ERROR, d
		}
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_SIGN_IN, d
	case agentv1.McpServerState_MCP_SERVER_STATE_UNAVAILABLE:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_ERROR, d
	}
	return agentv1.ConnectorStatus_CONNECTOR_STATUS_UNSPECIFIED, d
}

func pluginConnectorStatus(in plugins.Info) agentv1.ConnectorStatus {
	switch in.Status {
	case plugins.StatusActive:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_IN_SESSIONS
	case plugins.StatusNeedsApproval:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_APPROVAL
	case plugins.StatusDisabled:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_OFF
	case plugins.StatusUntrustedFolder:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_TRUST
	case plugins.StatusBlockedByPolicy:
		return agentv1.ConnectorStatus_CONNECTOR_STATUS_BLOCKED
	}
	return agentv1.ConnectorStatus_CONNECTOR_STATUS_ERROR
}

// statusRank orders what several plugins say about one server: the one closest to working speaks for the row.
func statusRank(s agentv1.ConnectorStatus) int {
	return slices.Index([]agentv1.ConnectorStatus{
		agentv1.ConnectorStatus_CONNECTOR_STATUS_CONNECTED, agentv1.ConnectorStatus_CONNECTOR_STATUS_IN_SESSIONS, agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_APPROVAL,
		agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_SIGN_IN, agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_TRUST, agentv1.ConnectorStatus_CONNECTOR_STATUS_ERROR,
		agentv1.ConnectorStatus_CONNECTOR_STATUS_OFF, agentv1.ConnectorStatus_CONNECTOR_STATUS_BLOCKED,
	}, s)
}

func managerBadge(s agentv1.McpServerSource) string {
	switch s {
	case agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE:
		return "Device"
	case agentv1.McpServerSource_MCP_SERVER_SOURCE_ACCOUNT:
		return "Account"
	case agentv1.McpServerSource_MCP_SERVER_SOURCE_ORG:
		return "Admin"
	case agentv1.McpServerSource_MCP_SERVER_SOURCE_PROJECT:
		return "Repository"
	}
	return ""
}

// shortServerName is a plugin's server's name inside the plugin: plugin:kit:srv is srv.
func shortServerName(full string) string {
	if i := strings.LastIndex(full, ":"); i >= 0 {
		return full[i+1:]
	}
	return full
}

// FoldConnectors makes the rows of the table. Each managed server keeps its row, since each can be changed on its own. A plugin's
// server joins the row of the same endpoint, and plugins that bring one endpoint share a row: installing three plugins with the same
// server isn't three things to look at. A server the policy keeps out says so on its row.
func FoldConnectors(entries []*agentv1.McpServerEntry, installed []plugins.Info, pol plugins.Policy) []*agentv1.ExtensionConnector {
	out := make([]*agentv1.ExtensionConnector, 0, len(entries))
	byKey := map[string]*agentv1.ExtensionConnector{}
	for _, e := range entries {
		s := e.GetServer()
		t := transportName(s.GetTransport())
		endpoint := e.GetLaunchSummary()
		c := &agentv1.ExtensionConnector{Id: s.GetId(), Name: s.GetName(), Remote: isRemoteTransport(t), Transport: t, Endpoint: endpoint, Entry: e}
		if c.Id == "" {
			c.Id = "file:" + s.GetName()
		}
		c.Status, c.StatusDetail = entryStatus(e)
		if b := managerBadge(e.GetSource()); b != "" {
			c.Badges = append(c.Badges, b)
		}
		out = append(out, c)
		if k := endpointKey(c.Remote, endpoint); k != "" {
			if _, ok := byKey[k]; !ok {
				byKey[k] = c
			}
		}
	}
	managed := len(out)
	for _, in := range installed {
		if in.Status == plugins.StatusNotInstalled {
			continue
		}
		label := in.DisplayName
		if label == "" {
			label = in.Name
		}
		for _, m := range in.Contents.MCPServers {
			remote := isRemoteTransport(m.Transport)
			k := endpointKey(remote, m.Command)
			c := byKey[k]
			if k == "" || c == nil {
				sum := sha256.Sum256([]byte(in.ID + "\x00" + m.Name + "\x00" + m.Command))
				c = &agentv1.ExtensionConnector{Id: "plugin:" + hex.EncodeToString(sum[:8]), Name: shortServerName(m.Name), Remote: remote, Transport: m.Transport, Endpoint: m.Command}
				out = append(out, c)
				if k != "" {
					byKey[k] = c
				}
			}
			if !slices.Contains(c.Badges, "Plugin") {
				c.Badges = append(c.Badges, "Plugin")
			}
			c.Providers = append(c.Providers, &agentv1.ConnectorProvider{PluginId: in.ID, PluginName: label, Server: m.Name, PluginStatus: pluginStatusProto(in.Status)})
			if c.GetEntry() != nil {
				continue
			}
			st, detail := pluginConnectorStatus(in), in.Reason
			if !pol.MCPAllowed(m.Name, shortServerName(m.Name)) {
				st, detail = agentv1.ConnectorStatus_CONNECTOR_STATUS_BLOCKED, "Your organization's policy doesn't allow this server."
			}
			if c.Status == agentv1.ConnectorStatus_CONNECTOR_STATUS_UNSPECIFIED || statusRank(st) < statusRank(c.Status) {
				c.Status, c.StatusDetail = st, detail
			}
		}
	}
	byName := func(a, b *agentv1.ExtensionConnector) int {
		return strings.Compare(strings.ToLower(a.GetName()), strings.ToLower(b.GetName()))
	}
	slices.SortStableFunc(out[:managed], byName)
	slices.SortStableFunc(out[managed:], byName)
	return out
}

var pluginStatuses = map[plugins.Status]agentv1.PluginStatus{
	plugins.StatusActive: agentv1.PluginStatus_PLUGIN_STATUS_ACTIVE, plugins.StatusNeedsApproval: agentv1.PluginStatus_PLUGIN_STATUS_NEEDS_APPROVAL,
	plugins.StatusDisabled: agentv1.PluginStatus_PLUGIN_STATUS_DISABLED, plugins.StatusUntrustedFolder: agentv1.PluginStatus_PLUGIN_STATUS_UNTRUSTED_FOLDER,
	plugins.StatusBlockedByPolicy: agentv1.PluginStatus_PLUGIN_STATUS_BLOCKED_BY_POLICY, plugins.StatusChangedOnDisk: agentv1.PluginStatus_PLUGIN_STATUS_CHANGED_ON_DISK,
	plugins.StatusBroken: agentv1.PluginStatus_PLUGIN_STATUS_BROKEN, plugins.StatusNotInstalled: agentv1.PluginStatus_PLUGIN_STATUS_NOT_INSTALLED,
}

func pluginStatusProto(s plugins.Status) agentv1.PluginStatus { return pluginStatuses[s] }
