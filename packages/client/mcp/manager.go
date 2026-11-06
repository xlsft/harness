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

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
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
	// ReminderToolsChanged tells the model a server's tools changed ({{server}}, {{added}}, {{removed}}, {{deferred}}).
	ReminderToolsChanged = "mcp_tools_changed"
	// ReminderInstructions carries the instructions of servers that started after the session did ({{servers}}).
	ReminderInstructions = "mcp_instructions"
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
	// PolicyAllows says whether the organization's managed policy allows a server by name (nil: all). A repository's
	// .mcp.json cannot add a server it does not; the server enforces the same list for everything it knows of.
	PolicyAllows func(name string) bool
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
	// Local is this device's own server list (nil: none). Its servers never reach the xlsft`s harness server.
	Local *LocalStore
	// Registry receives tools that appear or disappear while the session runs (nil: the tool set stays as it is).
	Registry *tools.Registry
	// ToolTimeout is the default limit of one tool call (0: HARNESS_MCP_TOOL_TIMEOUT_MS, else 10 minutes). A server's
	// tool_timeout_ms wins.
	ToolTimeout time.Duration
	// MaxOutputChars is the default limit of one tool result (0: HARNESS_MCP_MAX_OUTPUT_CHARS, else MaxOutputChars).
	MaxOutputChars int
	// RefreshEvery is how often ToolDefs starts a background check of the remote tool lists
	// (0: HARNESS_MCP_REFRESH_SECONDS, else 2 minutes; negative: never).
	RefreshEvery time.Duration
	SessionID    string
	Logger       *slog.Logger
}

// Listeners hear about changes while the session runs. Called from background goroutines.
type Listeners struct {
	// Progress reports a server's progress on a running tool call.
	Progress func(toolUseID string, progress, total float64, message string)
	// ListChanged reports that a server's tools, prompts or resources changed ("tools", "prompts", "resources").
	ListChanged func(server, kind string)
	// ToolsChanged reports tools that appeared and disappeared. deferred are the added ones the model must load.
	ToolsChanged func(server string, added, removed, deferred []string)
	// CatalogChanged reports a new catalog snapshot.
	CatalogChanged func(version string)
	// InstructionsAdded reports the instructions of a server that started after the session did.
	InstructionsAdded func(text string)
}

// Manager holds the session's MCP servers and tools. The catalog is frozen per session, for the prompt cache, until
// a server says its list changed or a background check finds the catalog moved; ToolSearch changes which deferred
// tools are offered to the model.
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
	// tools, byName, search and catalog change only under mu once Open returned.
	listeners     Listeners
	inflight      map[string][]*inflightCall // by server id: running calls, newest last
	lastCheck     time.Time
	checking      bool
	remoteInfo    map[string]*mcpv1.CatalogServer // by server id, from the last catalog
	promptCache   map[string]cachedList[*mcpv1.Prompt]
	resourceCache map[string]cachedList[*mcpv1.Resource]
	resourceTools bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// server is one configured server.
type server struct {
	cfg *mcpv1.Server
	// remote: reached through the xlsft`s harness server's proxy. Everything else has a direct connection.
	remote bool
	// local: from this device's own list; nothing about it goes to the xlsft`s harness server.
	local     bool
	alwaysAsk bool
	// needsApproval: this device decides before the first use (stdio servers and everything from a .mcp.json).
	needsApproval bool

	mu          sync.Mutex
	approval    mcpv1.ApprovalState
	unavailable string
	conn        *directConn
	lastErr     string
	tools       []*mcpv1.Tool
	// refreshing / dirty coalesce list_changed notifications (under the manager's mu).
	refreshing, dirty bool
}

func (s *server) name() string { return s.cfg.GetName() }

// Open loads the user's servers, starts approved stdio servers, fetches the tool catalog and builds
// the tool set. Problems with single servers become warnings; only a missing client is an error.
// An unreachable McpService yields an empty manager.
func Open(ctx context.Context, o Options) (*Manager, error) {
	if o.Client == nil {
		return nil, fmt.Errorf("mcp: no McpService client")
	}
	m := &Manager{o: o, log: o.Logger, byName: map[string]*Tool{}, loaded: map[string]bool{}, reported: map[string]*mcpv1.DeviceServerStatus{},
		inflight: map[string][]*inflightCall{}, remoteInfo: map[string]*mcpv1.CatalogServer{},
		promptCache: map[string]cachedList[*mcpv1.Prompt]{}, resourceCache: map[string]cachedList[*mcpv1.Resource]{}}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.o.LaunchTimeout <= 0 {
		m.o.LaunchTimeout = 30 * time.Second
	}
	if m.o.ToolTimeout <= 0 {
		m.o.ToolTimeout = envDuration("HARNESS_MCP_TOOL_TIMEOUT_MS", time.Millisecond, defaultToolTimeout)
	}
	if m.o.MaxOutputChars <= 0 {
		m.o.MaxOutputChars = envInt("HARNESS_MCP_MAX_OUTPUT_CHARS", MaxOutputChars)
	}
	if m.o.RefreshEvery == 0 {
		m.o.RefreshEvery = envDuration("HARNESS_MCP_REFRESH_SECONDS", time.Second, defaultRefreshEvery)
	}
	m.ctx, m.cancel = context.WithCancel(context.WithoutCancel(ctx))
	m.lastCheck = time.Now()
	for _, cfg := range o.Local.List() {
		if !cfg.GetEnabled() {
			continue
		}
		m.servers = append(m.servers, &server{cfg: cfg, local: true, needsApproval: true, alwaysAsk: cfg.GetRemote().GetRequiresUserInteraction()})
	}
	all, err := o.Client.ListServers(ctx, o.ProjectKey)
	if err != nil {
		m.log.Warn("MCP servers unavailable", "err", err)
		if connect.CodeOf(err) != connect.CodeUnimplemented {
			m.warn("MCP servers could not be loaded: %s", errorMessage(err))
		}
		all = nil
	}
	inFile := map[string]bool{}
	if o.ProjectConfig != "" && err == nil {
		if changed := m.syncProject(ctx, all); changed {
			if again, err := o.Client.ListServers(ctx, o.ProjectKey); err == nil {
				all = again
			}
		}
		// A server taken out of the file is no longer the repository's: its row may stay, for other branches.
		if want, err := ReadProjectConfig(o.ProjectConfig, o.ProjectKey); err == nil {
			for _, w := range want {
				inFile[w.GetName()] = true
			}
		}
	}
	for _, cfg := range all {
		if !cfg.GetEnabled() {
			continue
		}
		project := cfg.GetScope() == commonv1.Scope_SCOPE_PROJECT
		if project && o.ProjectConfig != "" && !inFile[cfg.GetName()] {
			continue
		}
		stdio := cfg.GetTransport() == mcpv1.Transport_TRANSPORT_STDIO
		s := &server{cfg: cfg, remote: !stdio, needsApproval: stdio || project}
		s.alwaysAsk = cfg.GetRemote().GetRequiresUserInteraction()
		m.servers = append(m.servers, s)
	}
	m.startDirect(ctx)
	m.buildTools(ctx)
	return m, nil
}

// UpdateListeners changes some of the listeners.
func (m *Manager) UpdateListeners(f func(*Listeners)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f(&m.listeners)
}

func (m *Manager) listenersNow() Listeners {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listeners
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

// startDirect evaluates availability and approval of every server that needs them, reports them and starts
// the approved direct ones concurrently.
func (m *Manager) startDirect(ctx context.Context) {
	var launch []*server
	for _, s := range m.servers {
		if !s.needsApproval {
			continue
		}
		hash := s.cfg.GetConfigHash()
		s.approval = m.o.Approvals.State(s.cfg.GetId(), hash)
		if s.approval == mcpv1.ApprovalState_APPROVAL_STATE_PENDING && m.autoApproved(s.name()) {
			s.approval = mcpv1.ApprovalState_APPROVAL_STATE_APPROVED
		}
		if s.remote {
			continue
		}
		s.unavailable = checkAvailable(s.cfg)
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

// stdioServers returns the registry's stdio servers: the ones reported to the xlsft`s harness server.
func (m *Manager) stdioServers() []*server {
	var out []*server
	for _, s := range m.servers {
		if !s.remote && !s.local {
			out = append(out, s)
		}
	}
	return out
}

// launchLocked starts s (s.mu held): launch data (IssueStdioLaunch, or this device's list), spawn or connect,
// initialize, list tools.
func (m *Manager) launchLocked(ctx context.Context, s *server) error {
	ctx, cancel := context.WithTimeout(ctx, m.o.LaunchTimeout)
	defer cancel()
	conn, err := connectDirect(ctx, s.cfg, s.local, m.o.Client, m.o.Local, m.o.Cwd, m.hooksFor(s))
	if err != nil {
		s.lastErr = err.Error()
		m.log.Warn("MCP server failed to start", "server", s.name(), "err", err)
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

// report sends the device status of the given servers. Servers from this device's own list are not reported: the
// harness server does not know them.
func (m *Manager) report(ctx context.Context, servers ...*server) {
	var sts []*mcpv1.DeviceServerStatus
	for _, s := range servers {
		if !s.local && !s.remote {
			sts = append(sts, s.status())
		}
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
			m.remoteInfo[cs.GetServerId()] = cs
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
		} else if s.approval == mcpv1.ApprovalState_APPROVAL_STATE_DENIED {
			continue
		}
		if s.remote && s.cfg.GetAuth().GetState() == mcpv1.AuthState_AUTH_STATE_NEEDS_AUTH {
			m.warn("MCP server %s needs authorization: open Settings > MCP in the xlsft`s harness web or desktop app to connect it", s.name())
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
	m.syncResourceTools()
}

// newTool builds the session's form of a server's tool (nil when the server's lists hide it).
func (m *Manager) newTool(s *server, t *mcpv1.Tool) *Tool {
	if !toolVisible(s.cfg, t.GetName()) {
		return nil
	}
	desc := t.GetDescription()
	if desc == "" {
		desc = t.GetTitle()
	}
	if len(desc) > maxDescription {
		desc = truncate(desc, maxDescription) + "…"
	}
	name := ToolName(s.name(), t.GetName())
	return &Tool{m: m, srv: s, name: name, upstream: t.GetName(), alwaysAsk: s.alwaysAsk || annotationRequiresInteraction(t.GetAnnotationsJson()),
		def: llm.ToolDef{Name: name, Description: desc, InputSchema: objectSchema(t.GetInputSchemaJson())}}
}

func (m *Manager) addTool(s *server, t *mcpv1.Tool) {
	tool := m.newTool(s, t)
	if tool == nil {
		return
	}
	if _, dup := m.byName[tool.name]; dup {
		m.log.Warn("duplicate MCP tool name; skipped", "tool", tool.name)
		return
	}
	m.tools = append(m.tools, tool)
	m.byName[tool.name] = tool
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
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tools.Tool
	if m.search != nil {
		out = append(out, m.search)
	}
	if m.resourceTools {
		out = append(out, &listResourcesTool{m: m}, &readResourceTool{m: m})
	}
	for _, t := range m.tools {
		out = append(out, t)
	}
	return out
}

// toolList returns the session's tools now.
func (m *Manager) toolList() []*Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Tool(nil), m.tools...)
}

func (m *Manager) lookup(name string) *Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byName[name]
}

// Deferred returns the names of deferred tools (sorted).
func (m *Manager) Deferred() []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, t := range m.toolList() {
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
	for _, t := range m.toolList() {
		if !t.deferred && !m.disabled(t.name) {
			out = append(out, t.def)
		}
	}
	m.mu.Lock()
	order := append([]string(nil), m.order...)
	m.mu.Unlock()
	for _, n := range order {
		if t := m.lookup(n); t != nil && !m.disabled(n) {
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
	var out []ToolInfo
	for _, t := range m.toolList() {
		if m.disabled(t.name) {
			continue
		}
		upstream := t.upstream
		if t.auth {
			upstream = "authenticate"
		}
		out = append(out, ToolInfo{Server: t.srv.name(), Tool: upstream, Def: t.def, Offered: !t.deferred || m.isLoaded(t.name)})
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
			case m.lookup(b.Name) != nil:
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
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
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
