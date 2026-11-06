package mcp

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/llm"
)

// defaultRefreshEvery is how often the remote tool lists are checked while the session makes requests. Upstream
// notifications only reach the proxy while it holds a connection, so this is what notices the rest.
const defaultRefreshEvery = 2 * time.Minute

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && n > 0 {
		return n
	}
	return def
}

func envDuration(name string, unit, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * unit
		}
	}
	return def
}

// globMatch matches s against a pattern where '*' stands for any characters.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// toolVisible applies a server's allowed_tools and denied_tools.
func toolVisible(cfg *mcpv1.Server, tool string) bool {
	matches := func(list []string) bool {
		return slices.ContainsFunc(list, func(p string) bool { return globMatch(p, tool) })
	}
	if matches(cfg.GetDeniedTools()) {
		return false
	}
	return len(cfg.GetAllowedTools()) == 0 || matches(cfg.GetAllowedTools())
}

// hooksFor wires a direct connection to the manager.
func (m *Manager) hooksFor(s *server) connHooks {
	return connHooks{
		root: m.o.Cwd,
		elicit: func(_ context.Context, p *sdk.ElicitParams) (*sdk.ElicitResult, error) {
			return m.handleDirectElicitation(s, p)
		},
		listChanged: func(kind string) { m.directListChanged(s, kind) },
		// The token says which call it is about; the notification may arrive just after the call's answer.
		progress: func(token any, progress, total float64, message string) {
			if t, ok := token.(string); ok && strings.HasPrefix(t, progressPrefix) {
				m.emitProgress(strings.TrimPrefix(t, progressPrefix), progress, total, message)
			}
		},
	}
}

// directListChanged handles a notification of a direct connection. It runs on the connection's goroutine, which must
// stay free to read the answers, so refreshing the tools happens on another one.
func (m *Manager) directListChanged(s *server, kind string) {
	m.invalidateLists(s, kind)
	if f := m.listenersNow().ListChanged; f != nil {
		f(s.name(), kind)
	}
	if kind != "tools" {
		return
	}
	m.mu.Lock()
	if s.refreshing {
		s.dirty = true
		m.mu.Unlock()
		return
	}
	s.refreshing = true
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		for {
			m.refreshDirect(s)
			m.mu.Lock()
			if !s.dirty {
				s.refreshing = false
				m.mu.Unlock()
				return
			}
			s.dirty = false
			m.mu.Unlock()
		}
	}()
}

func (m *Manager) refreshDirect(s *server) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, m.o.LaunchTimeout)
	defer cancel()
	list, err := conn.listTools(ctx)
	if err != nil {
		m.log.Warn("MCP tools/list after list_changed failed", "server", s.name(), "err", err)
		return
	}
	s.mu.Lock()
	s.tools = list
	s.mu.Unlock()
	m.report(ctx, s)
	m.applyToolList(s, list)
}

// applyToolList makes the session's tools of a server match list and tells the listeners what changed.
func (m *Manager) applyToolList(s *server, list []*mcpv1.Tool) {
	prefix := ServerPrefix(s.name())
	m.mu.Lock()
	want := map[string]*Tool{}
	for _, t := range list {
		if nt := m.newTool(s, t); nt != nil {
			want[nt.name] = nt
		}
	}
	var added, removed, deferred []string
	var keep []*Tool
	for _, t := range m.tools {
		if t.srv != s || !strings.HasPrefix(t.name, prefix) {
			keep = append(keep, t)
			continue
		}
		nt, ok := want[t.name]
		if t.auth {
			if len(want) == 0 {
				keep = append(keep, t)
			} else {
				removed = append(removed, t.name)
				delete(m.byName, t.name)
			}
			continue
		}
		if !ok {
			removed = append(removed, t.name)
			delete(m.byName, t.name)
			delete(m.loaded, t.name)
			m.order = slices.DeleteFunc(m.order, func(n string) bool { return n == t.name })
			continue
		}
		// The same tool, maybe with a new description or schema.
		nt.deferred = t.deferred
		m.byName[t.name] = nt
		keep = append(keep, nt)
		delete(want, t.name)
	}
	m.tools = keep
	var fresh []*Tool
	for _, t := range list { // in the server's order
		nt := want[ToolName(s.name(), t.GetName())]
		if nt == nil {
			continue
		}
		if _, dup := m.byName[nt.name]; dup {
			continue
		}
		nt.deferred = m.search != nil && !s.cfg.GetAlwaysLoad()
		m.tools = append(m.tools, nt)
		m.byName[nt.name] = nt
		added = append(added, nt.name)
		if nt.deferred {
			deferred = append(deferred, nt.name)
		}
		fresh = append(fresh, nt)
	}
	l := m.listeners
	m.mu.Unlock()
	if r := m.o.Registry; r != nil {
		for _, n := range removed {
			r.Remove(n)
		}
		for _, t := range fresh {
			r.Register(t)
		}
		// Same tools with new definitions.
		for _, t := range m.toolList() {
			if t.srv == s {
				r.Register(t)
			}
		}
	}
	if (len(added) > 0 || len(removed) > 0) && l.ToolsChanged != nil {
		l.ToolsChanged(s.name(), added, removed, deferred)
	}
}

// listChangedRemote handles a list_changed the proxy passed on during a call.
func (m *Manager) listChangedRemote(s *server, k mcpv1.ListChanged_Kind) {
	kind := ""
	switch k {
	case mcpv1.ListChanged_KIND_TOOLS:
		kind = "tools"
	case mcpv1.ListChanged_KIND_PROMPTS:
		kind = "prompts"
	case mcpv1.ListChanged_KIND_RESOURCES:
		kind = "resources"
	default:
		return
	}
	m.invalidateLists(s, kind)
	if f := m.listenersNow().ListChanged; f != nil {
		f(s.name(), kind)
	}
	if kind == "tools" {
		m.startCatalogCheck(true)
	}
}

// startCatalogCheck starts a background comparison with the current catalog, at most one at a time. Unless forced, it
// waits for RefreshEvery since the last one.
func (m *Manager) startCatalogCheck(force bool) {
	if m == nil || m.ctx == nil {
		return
	}
	m.mu.Lock()
	due := force || (m.o.RefreshEvery > 0 && time.Since(m.lastCheck) >= m.o.RefreshEvery)
	if !due || m.checking || m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	m.checking = true
	m.lastCheck = time.Now()
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		if err := m.Refresh(ctx); err != nil {
			m.log.Debug("MCP catalog check failed", "err", err)
		}
		m.mu.Lock()
		m.checking = false
		m.mu.Unlock()
	}()
}

// Refresh compares the session's remote tools with the current catalog and applies the differences.
func (m *Manager) Refresh(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if !m.hasRemote() {
		return nil
	}
	cat, err := m.o.Client.GetToolCatalog(ctx, "", m.o.ProjectKey)
	if err != nil {
		return err
	}
	m.mu.Lock()
	same := cat.GetVersion() == m.catalog
	m.catalog = cat.GetVersion()
	l := m.listeners
	byID := map[string]*mcpv1.CatalogServer{}
	for _, cs := range cat.GetServers() {
		byID[cs.GetServerId()] = cs
		m.remoteInfo[cs.GetServerId()] = cs
	}
	m.mu.Unlock()
	if same {
		return nil
	}
	if l.CatalogChanged != nil {
		l.CatalogChanged(cat.GetVersion())
	}
	for _, s := range m.servers {
		if !s.remote {
			continue
		}
		// A server missing from the catalog was disabled or deleted.
		m.applyToolList(s, byID[s.cfg.GetId()].GetTools())
	}
	m.syncResourceTools()
	return nil
}

func (m *Manager) hasRemote() bool {
	for _, s := range m.servers {
		if s.remote {
			return true
		}
	}
	return false
}

// RequestToolDefs is ToolDefs for the agent's request loop: it also starts the periodic catalog check.
func (m *Manager) RequestToolDefs() []llm.ToolDef {
	m.startCatalogCheck(false)
	return m.ToolDefs()
}

// instructionSource is what one server says for the model.
type instructionSource struct {
	Server, Text string
}

// Budgets of server instructions in the context, in characters: they come from third parties and sit in every
// request, so one long-winded server must not crowd out the others.
const (
	maxInstructionsPerServer = 2048
	maxInstructionsTotal     = 8192
)

// instructionsText renders servers' instructions within the budget; "" when there are none.
func instructionsText(list []instructionSource) string {
	var b strings.Builder
	left := maxInstructionsTotal
	for _, it := range list {
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		if left <= 0 {
			fmt.Fprintf(&b, "<mcp-server name=%q>[left out: the instructions of MCP servers reached their size limit]</mcp-server>\n", it.Server)
			continue
		}
		limit := min(maxInstructionsPerServer, left)
		cut := false
		if len(text) > limit {
			text, cut = truncate(text, limit), true
		}
		left -= len(text)
		// The text must not be able to end the block it sits in.
		text = strings.NewReplacer("</mcp-server", "&lt;/mcp-server", "</system-reminder", "&lt;/system-reminder", "<system-reminder", "&lt;system-reminder").Replace(text)
		if cut {
			text += "\n[truncated: these instructions exceeded the size limit]"
		}
		fmt.Fprintf(&b, "<mcp-server name=%q>\n%s\n</mcp-server>\n", it.Server, text)
	}
	return strings.TrimSpace(b.String())
}

// Instructions returns the instructions of the servers usable now, for the session's context ("" when none).
func (m *Manager) Instructions() string {
	if m == nil {
		return ""
	}
	var list []instructionSource
	for _, s := range m.servers {
		s.mu.Lock()
		conn, approval := s.conn, s.approval
		s.mu.Unlock()
		switch {
		case conn != nil:
			list = append(list, instructionSource{s.name(), conn.instructions()})
		case s.remote && (!s.needsApproval || approval == mcpv1.ApprovalState_APPROVAL_STATE_APPROVED):
			m.mu.Lock()
			info := m.remoteInfo[s.cfg.GetId()]
			m.mu.Unlock()
			list = append(list, instructionSource{s.name(), info.GetInstructions()})
		}
	}
	slices.SortStableFunc(list, func(a, b instructionSource) int { return strings.Compare(a.Server, b.Server) })
	return instructionsText(list)
}
