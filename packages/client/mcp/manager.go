package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/tools"
)

// Bundle keys used by the manager.
const (
	// ToolSearchName is the deferred-tool loader; its description and schema come from the bundle.
	ToolSearchName = "ToolSearch"
	// AuthToolBundleKey is the bundle entry of the per-server authenticate tool (description may use
	// {{server}}). It is exposed as mcp__<server>__authenticate for servers that need authorization.
	AuthToolBundleKey = "McpAuthenticate"
	// ReminderDeferredTools lists deferred tool names ({{tools}}, one per line).
	ReminderDeferredTools = "deferred_tools"
)

// Tool search modes (HARNESS_TOOL_SEARCH).
const (
	ToolSearchAuto   = "auto"
	ToolSearchAlways = "always"
	ToolSearchNever  = "never"
)

// DefaultDeferPercent is the share of the context window MCP tool definitions may take before they
// are deferred behind ToolSearch in auto mode.
const DefaultDeferPercent = 10

// maxDescription caps an MCP tool description sent to the model.
const maxDescription = 2048

// Options configure a Manager.
type Options struct {
	// Client is the McpService client (required).
	Client *Client
	// Cwd is the project root: the default working directory of stdio servers.
	Cwd string
	// ProjectKey selects the project's servers (SCOPE_PROJECT); empty includes none.
	ProjectKey string
	// ProjectConfig is the path of the repository's .mcp.json. Its servers are registered on the
	// server as SCOPE_PROJECT (only set it for trusted workspaces).
	ProjectConfig string
	// Approvals is the local stdio approval store (nil: nothing is remembered).
	Approvals *Approvals
	// AutoApprove names stdio servers approved without asking ("*" approves all), e.g. from
	// HARNESS_MCP_APPROVE for non-interactive runs.
	AutoApprove []string
	// CatalogVersion is the frozen catalog snapshot of a resumed session ("" fetches the current one).
	CatalogVersion string
	// ContextWindow of the model in tokens (0 assumes 200k), for the deferral threshold.
	ContextWindow int
	// ToolSearch is "auto" (default, optionally "auto:<percent>"), "always" or "never".
	ToolSearch string
	// Bundle supplies the ToolSearch and authenticate tool descriptions.
	Bundle *prompts.Bundle
	// Disabled reports tools disabled by permission rules (may be nil).
	Disabled func(name string) bool
	// LaunchTimeout bounds starting one stdio server (default 30 s).
	LaunchTimeout time.Duration
	SessionID     string
	Logger        *slog.Logger
}

// Manager holds the session's MCP servers and tools. The tool set is fixed at Open (the catalog is
// frozen per session); ToolSearch only changes which deferred tools are offered to the model.
type Manager struct {
	o       Options
	log     *slog.Logger
	servers []*server
	tools   []*Tool
	byName  map[string]*Tool
	search  *ToolSearch
	catalog string

	mu       sync.Mutex
	loaded   map[string]bool
	order    []string // deferred tools in load order
	warnings []string
	reported map[string]*mcpv1.DeviceServerStatus
}

// server is one configured server.
type server struct {
	cfg       *mcpv1.Server
	remote    bool
	alwaysAsk bool

	mu          sync.Mutex
	approval    mcpv1.ApprovalState
	unavailable string
	conn        *stdioConn
	lastErr     string
	tools       []*mcpv1.Tool
}

func (s *server) name() string { return s.cfg.GetName() }

// Open loads the user's servers, starts approved stdio servers, fetches the tool catalog and builds
// the tool set. Problems with single servers become warnings; only a missing client is an error.
// An unreachable McpService yields an empty manager.
func Open(ctx context.Context, o Options) (*Manager, error) {
	if o.Client == nil {
		return nil, fmt.Errorf("mcp: no McpService client")
	}
	m := &Manager{o: o, log: o.Logger, byName: map[string]*Tool{}, loaded: map[string]bool{}, reported: map[string]*mcpv1.DeviceServerStatus{}}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.o.LaunchTimeout <= 0 {
		m.o.LaunchTimeout = 30 * time.Second
	}
	all, err := o.Client.ListServers(ctx, o.ProjectKey)
	if err != nil {
		m.log.Warn("MCP servers unavailable", "err", err)
		if connect.CodeOf(err) != connect.CodeUnimplemented {
			m.warn("MCP servers could not be loaded: %s", errorMessage(err))
		}
		return m, nil
	}
	if o.ProjectConfig != "" {
		if changed := m.syncProject(ctx, all); changed {
			if again, err := o.Client.ListServers(ctx, o.ProjectKey); err == nil {
				all = again
			}
		}
	}
	for _, cfg := range all {
		if !cfg.GetEnabled() {
			continue
		}
		s := &server{cfg: cfg, remote: cfg.GetTransport() != mcpv1.Transport_TRANSPORT_STDIO}
		s.alwaysAsk = cfg.GetRemote().GetRequiresUserInteraction()
		m.servers = append(m.servers, s)
	}
	m.startStdio(ctx)
	m.buildTools(ctx)
	return m, nil
}

func (m *Manager) warn(format string, args ...any) {
	m.mu.Lock()
	m.warnings = append(m.warnings, fmt.Sprintf(format, args...))
	m.mu.Unlock()
}

// Warnings returns problems found while opening (servers needing authorization or approval,
// launch failures), for frontends to show as notices.
func (m *Manager) Warnings() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.warnings...)
}

// CatalogVersion returns the frozen catalog snapshot the session uses ("" when none was fetched).
func (m *Manager) CatalogVersion() string {
	if m == nil {
		return ""
	}
	return m.catalog
}

func (m *Manager) autoApproved(name string) bool {
	for _, n := range m.o.AutoApprove {
		if n = strings.TrimSpace(n); n == "*" || n == name {
			return true
		}
	}
	return false
}

// startStdio evaluates availability and approval of every stdio server, reports them and starts
// the approved ones concurrently.
func (m *Manager) startStdio(ctx context.Context) {
	var launch []*server
	for _, s := range m.servers {
		if s.remote {
			continue
		}
		hash := s.cfg.GetConfigHash()
		s.unavailable = checkAvailable(s.cfg)
		s.approval = m.o.Approvals.State(s.cfg.GetId(), hash)
		if s.approval == mcpv1.ApprovalState_APPROVAL_STATE_PENDING && m.autoApproved(s.name()) {
			s.approval = mcpv1.ApprovalState_APPROVAL_STATE_APPROVED
		}
		if s.unavailable == "" && s.approval == mcpv1.ApprovalState_APPROVAL_STATE_APPROVED {
			launch = append(launch, s)
		}
	}
	m.report(ctx, m.stdioServers()...)
	var wg sync.WaitGroup
	for _, s := range launch {
		wg.Add(1)
		go func(s *server) {
			defer wg.Done()
			s.mu.Lock()
			defer s.mu.Unlock()
			_ = m.launchLocked(ctx, s)
		}(s)
	}
	wg.Wait()
	if len(launch) > 0 {
		m.report(ctx, launch...)
	}
}

func (m *Manager) stdioServers() []*server {
	var out []*server
	for _, s := range m.servers {
		if !s.remote {
			out = append(out, s)
		}
	}
	return out
}

// launchLocked starts s (s.mu held): IssueStdioLaunch, spawn, initialize, list tools.
func (m *Manager) launchLocked(ctx context.Context, s *server) error {
	ctx, cancel := context.WithTimeout(ctx, m.o.LaunchTimeout)
	defer cancel()
	launch, err := m.o.Client.IssueStdioLaunch(ctx, s.cfg.GetId(), s.cfg.GetConfigHash())
	if err != nil {
		s.lastErr = "launch data not issued: " + errorMessage(err)
		m.log.Warn("MCP stdio launch refused", "server", s.name(), "err", err)
		m.warn("MCP server %s could not start: %s", s.name(), s.lastErr)
		return fmt.Errorf("%s", s.lastErr)
	}
	conn, err := startStdio(ctx, launch, m.o.Cwd)
	launch = nil // drop the secrets
	_ = launch
	if err != nil {
		s.lastErr = err.Error()
		m.log.Warn("MCP stdio server failed to start", "server", s.name(), "err", err)
		m.warn("MCP server %s could not start: %s", s.name(), s.lastErr)
		return err
	}
	list, err := conn.listTools(ctx)
	if err != nil {
		conn.close()
		s.lastErr = "tools/list failed: " + err.Error()
		m.warn("MCP server %s could not start: %s", s.name(), s.lastErr)
		return fmt.Errorf("%s", s.lastErr)
	}
	s.conn, s.tools, s.lastErr = conn, list, ""
	return nil
}

// report sends the device status of the given stdio servers.
func (m *Manager) report(ctx context.Context, servers ...*server) {
	var sts []*mcpv1.DeviceServerStatus
	for _, s := range servers {
		sts = append(sts, s.status())
	}
	if len(sts) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := m.o.Client.ReportDeviceStatus(ctx, sts); err != nil {
		m.log.Warn("MCP device status report failed", "err", err)
		return
	}
	m.mu.Lock()
	for _, st := range sts {
		m.reported[st.GetServerId()] = st
	}
	m.mu.Unlock()
}

func (s *server) status() *mcpv1.DeviceServerStatus {
	st := &mcpv1.DeviceServerStatus{
		ServerId: s.cfg.GetId(), ConfigHash: s.cfg.GetConfigHash(), Approval: s.approval,
		Availability: mcpv1.Availability_AVAILABILITY_AVAILABLE, LastError: s.lastErr, Tools: s.tools,
	}
	if s.unavailable != "" {
		st.Availability, st.Reason = mcpv1.Availability_AVAILABILITY_UNAVAILABLE_ON_DEVICE, s.unavailable
	}
	return st
}

// Reported returns the last status reported for a server (tests and diagnostics).
func (m *Manager) Reported(serverID string) *mcpv1.DeviceServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reported[serverID]
}

// buildTools fetches the catalog and assembles the session's tools.
func (m *Manager) buildTools(ctx context.Context) {
	catalogTools := map[string][]*mcpv1.Tool{}
	cat, err := m.o.Client.GetToolCatalog(ctx, m.o.CatalogVersion, m.o.ProjectKey)
	if err != nil && m.o.CatalogVersion != "" && connect.CodeOf(err) == connect.CodeNotFound {
		cat, err = m.o.Client.GetToolCatalog(ctx, "", m.o.ProjectKey)
	}
	if err != nil {
		m.log.Warn("MCP tool catalog unavailable", "err", err)
	} else {
		m.catalog = cat.GetVersion()
		for _, cs := range cat.GetServers() {
			catalogTools[cs.GetServerId()] = cs.GetTools()
			if cs.GetRequiresUserInteraction() {
				for _, s := range m.servers {
					if s.cfg.GetId() == cs.GetServerId() {
						s.alwaysAsk = true
					}
				}
			}
		}
	}
	servers := append([]*server(nil), m.servers...)
	sort.SliceStable(servers, func(i, j int) bool { return servers[i].name() < servers[j].name() })
	for _, s := range servers {
		list := catalogTools[s.cfg.GetId()]
		if !s.remote {
			switch {
			case s.unavailable != "":
				m.warn("MCP server %s is unavailable on this device: %s", s.name(), s.unavailable)
				continue
			case s.approval == mcpv1.ApprovalState_APPROVAL_STATE_DENIED:
				continue
			case s.conn != nil:
				list = s.tools
			case s.approval == mcpv1.ApprovalState_APPROVAL_STATE_PENDING && len(list) == 0:
				m.warn("MCP server %s needs approval on this device before it can start (set HARNESS_MCP_APPROVE=%s to approve it)", s.name(), s.name())
				continue
			case s.approval == mcpv1.ApprovalState_APPROVAL_STATE_APPROVED:
				continue // failed to start (warned already)
			}
		}
		if s.remote && s.cfg.GetAuth().GetState() == mcpv1.AuthState_AUTH_STATE_NEEDS_AUTH {
			m.warn("MCP server %s needs authorization: open the MCP settings in the harness web or desktop app to connect it", s.name())
			if len(list) == 0 {
				m.addAuthTool(s)
				continue
			}
		}
		for _, t := range list {
			m.addTool(s, t)
		}
	}
	m.decideDeferral()
}

func (m *Manager) addTool(s *server, t *mcpv1.Tool) {
	name := ToolName(s.name(), t.GetName())
	if _, dup := m.byName[name]; dup {
		m.log.Warn("duplicate MCP tool name; skipped", "tool", name)
		return
	}
	desc := t.GetDescription()
	if desc == "" {
		desc = t.GetTitle()
	}
	if len(desc) > maxDescription {
		desc = truncate(desc, maxDescription) + "…"
	}
	tool := &Tool{m: m, srv: s, name: name, upstream: t.GetName(), alwaysAsk: s.alwaysAsk || annotationRequiresInteraction(t.GetAnnotationsJson()),
		def: llm.ToolDef{Name: name, Description: desc, InputSchema: objectSchema(t.GetInputSchemaJson())}}
	m.tools = append(m.tools, tool)
	m.byName[name] = tool
}

func (m *Manager) addAuthTool(s *server) {
	if m.o.Bundle == nil {
		return
	}
	tp, ok := m.o.Bundle.Tools[AuthToolBundleKey]
	if !ok {
		return
	}
	name := ToolName(s.name(), "authenticate")
	desc := prompts.Render(tp.Description, map[string]string{"server": s.name()}, nil)
	tool := &Tool{m: m, srv: s, name: name, auth: true, def: llm.ToolDef{Name: name, Description: desc, InputSchema: objectSchema(string(tp.InputSchema))}}
	m.tools = append(m.tools, tool)
	m.byName[name] = tool
}

func annotationRequiresInteraction(raw string) bool {
	if raw == "" {
		return false
	}
	var a struct {
		RequiresUserInteraction bool `json:"requiresUserInteraction"`
	}
	_ = json.Unmarshal([]byte(raw), &a)
	return a.RequiresUserInteraction
}

// objectSchema returns raw when it is a JSON object schema, else an empty object schema.
func objectSchema(raw string) json.RawMessage {
	var probe map[string]any
	if raw == "" || json.Unmarshal([]byte(raw), &probe) != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	if _, ok := probe["type"]; !ok {
		probe["type"] = "object"
		b, _ := json.Marshal(probe)
		return b
	}
	return json.RawMessage(raw)
}

// decideDeferral defers every tool of servers without always_load when tool search applies.
func (m *Manager) decideDeferral() {
	mode, pct := parseToolSearch(m.o.ToolSearch)
	if mode == ToolSearchNever || m.o.Bundle == nil {
		return
	}
	if _, ok := m.o.Bundle.Tools[ToolSearchName]; !ok {
		return
	}
	var candidates []*Tool
	size := 0
	for _, t := range m.tools {
		if t.auth || t.srv.cfg.GetAlwaysLoad() || m.disabled(t.name) {
			continue
		}
		candidates = append(candidates, t)
		size += len(t.def.Name) + len(t.def.Description) + len(t.def.InputSchema)
	}
	if len(candidates) == 0 {
		return
	}
	window := m.o.ContextWindow
	if window <= 0 {
		window = 200_000
	}
	if mode == ToolSearchAuto && size/4 <= window*pct/100 {
		return
	}
	for _, t := range candidates {
		t.deferred = true
	}
	m.search = &ToolSearch{m: m}
}

func parseToolSearch(s string) (string, int) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "always", "true", "1", "on":
		return ToolSearchAlways, 0
	case "never", "false", "0", "off":
		return ToolSearchNever, 0
	}
	if rest, ok := strings.CutPrefix(s, "auto:"); ok {
		if n, err := strconv.Atoi(rest); err == nil && n >= 0 && n <= 100 {
			return ToolSearchAuto, n
		}
	}
	return ToolSearchAuto, DefaultDeferPercent
}

func (m *Manager) disabled(name string) bool { return m.o.Disabled != nil && m.o.Disabled(name) }

// Tools returns the tools to register: every MCP tool plus ToolSearch when tools are deferred.
func (m *Manager) Tools() []tools.Tool {
	if m == nil {
		return nil
	}
	var out []tools.Tool
	if m.search != nil {
		out = append(out, m.search)
	}
	for _, t := range m.tools {
		out = append(out, t)
	}
	return out
}

// Deferred returns the names of deferred tools (sorted).
func (m *Manager) Deferred() []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, t := range m.tools {
		if t.deferred && !m.disabled(t.name) {
			out = append(out, t.name)
		}
	}
	sort.Strings(out)
	return out
}

// DeferredReminder returns the vars of the deferred_tools reminder, or nil when nothing is deferred.
func (m *Manager) DeferredReminder() map[string]string {
	names := m.Deferred()
	if len(names) == 0 {
		return nil
	}
	return map[string]string{"tools": strings.Join(names, "\n")}
}

// ToolDefs returns the definitions of the MCP tools currently offered to the model: every tool that
// is not deferred, then the deferred tools loaded by ToolSearch in load order. It is the agent's
// ExtraTools hook.
func (m *Manager) ToolDefs() []llm.ToolDef {
	if m == nil {
		return nil
	}
	var out []llm.ToolDef
	for _, t := range m.tools {
		if !t.deferred && !m.disabled(t.name) {
			out = append(out, t.def)
		}
	}
	m.mu.Lock()
	order := append([]string(nil), m.order...)
	m.mu.Unlock()
	for _, n := range order {
		if t := m.byName[n]; t != nil && !m.disabled(n) {
			out = append(out, t.def)
		}
	}
	return out
}

// ToolInfo describes one MCP tool of the session for the context breakdown.
type ToolInfo struct {
	// Server is the configured server name; Tool the tool's name on that server ("authenticate" for the
	// authorization pseudo tool).
	Server, Tool string
	Def          llm.ToolDef
	// Offered: the definition is sent to the model (not deferred, or loaded with ToolSearch).
	Offered bool
}

// Catalog lists the session's MCP tools in catalog order, disabled ones left out.
func (m *Manager) Catalog() []ToolInfo {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ToolInfo
	for _, t := range m.tools {
		if m.disabled(t.name) {
			continue
		}
		upstream := t.upstream
		if t.auth {
			upstream = "authenticate"
		}
		out = append(out, ToolInfo{Server: t.srv.name(), Tool: upstream, Def: t.def, Offered: !t.deferred || m.loaded[t.name]})
	}
	return out
}

// Names returns the names of the tools currently offered to the model (ToolSearch first).
func (m *Manager) Names() []string {
	if m == nil {
		return nil
	}
	var out []string
	if m.search != nil {
		out = append(out, ToolSearchName)
	}
	for _, d := range m.ToolDefs() {
		out = append(out, d.Name)
	}
	return out
}

// load marks deferred tools as loaded; it returns the newly loaded names.
func (m *Manager) load(names ...string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var added []string
	for _, n := range names {
		t := m.byName[n]
		if t == nil || !t.deferred || m.loaded[n] {
			continue
		}
		m.loaded[n] = true
		m.order = append(m.order, n)
		added = append(added, n)
	}
	return added
}

func (m *Manager) isLoaded(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loaded[name]
}

// RestoreLoaded re-loads the deferred tools a resumed conversation already used: tools it called
// and the results of its ToolSearch calls (replayed against the same tool set).
func (m *Manager) RestoreLoaded(history []llm.Message) {
	if m == nil || m.search == nil {
		return
	}
	for _, msg := range history {
		for _, b := range msg.ToolUses() {
			switch {
			case b.Name == ToolSearchName:
				var in searchInput
				if json.Unmarshal(b.Input, &in) == nil {
					m.load(m.search.find(in)...)
				}
			case m.byName[b.Name] != nil:
				m.load(b.Name)
			}
		}
	}
}

// Close stops the stdio servers.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	var wg sync.WaitGroup
	for _, s := range m.servers {
		s.mu.Lock()
		c := s.conn
		s.conn = nil
		s.mu.Unlock()
		if c != nil {
			wg.Add(1)
			go func() { defer wg.Done(); c.close() }()
		}
	}
	wg.Wait()
}
