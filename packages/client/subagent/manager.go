// Package subagent runs the agents a session's model starts with the Agent tool: each is an agent.Agent with its own
// driver, tool environment and transcript, in the background of the session. They use the session's permission
// engine, gateway and prompt bundle; what they finish with reaches the agent that started them as a reminder.
//
// Everything a subagent says is model output. Its report and its messages reach other agents inside marked blocks,
// and its calls are checked against the prompts of the session's user, never against what another agent told it.
package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/worktree"
)

// Limits.
const (
	// maxSteps is how many of an agent's latest tool calls its info carries.
	maxSteps = 12
	// maxReportChars bounds a report in a reminder; the transcript has all of it.
	maxReportChars = 30000
	// stopWait is how long Stop waits for an agent to end.
	stopWait = 5 * time.Second
	// worktreeCleanupTimeout bounds removing an untouched worktree at the end of the session.
	worktreeCleanupTimeout = 20 * time.Second
)

// Config is what a session gives its agents.
type Config struct {
	SessionID string
	// Dir holds the transcripts and the records of the agents: <transcript without .jsonl>/subagents.
	Dir string
	// NoPersistence keeps transcripts and records in memory.
	NoPersistence bool
	// LeadTranscript is the session's transcript: it records the agents' states, so that they sync with it and another
	// machine can list them.
	LeadTranscript *session.Writer
	// Limits reads how deep agents nest (the session's main agent starts agents at depth 1) and how many run at once;
	// called when an agent starts. Nil: the defaults.
	Limits func() (depth, running int)
	// Common fields of transcript entries.
	Cwd, GitBranch, Version, Entrypoint string

	Gateway gateway.Gateway
	Catalog *llm.Catalog
	// Bundle returns the prompt bundle for a model.
	Bundle func(ctx context.Context, model string) (*prompts.Bundle, error)
	// LeadEnv is the tool environment of the session's main agent.
	LeadEnv *tools.Env
	// Tools is the session's tool registry; DisabledTools and ExtraTools are as in agent.Config.
	Tools         *tools.Registry
	DisabledTools map[string]bool
	ExtraTools    func() []llm.ToolDef
	Permissions   permissions.Checker
	// Mode reads the session's permission mode.
	Mode func() string
	// AddWorkDir makes a worktree a working directory of the permission engine.
	AddWorkDir func(dir string)

	Vars           map[string]string
	ProjectContext string
	// MCPInstructions returns what MCP servers offer the model (nil: nothing).
	MCPInstructions func() string
	AutoCompactPct  float64
	UsageMonitor    agent.UsageMonitor
	Skills          agent.SkillSource
	// Hooks is the session's hook engine (agent.Config.Hooks; may be nil).
	Hooks *hooks.Engine
	// CustomTypes are the types loaded from definitions (LoadDefinitions).
	CustomTypes []Type
	// AgentHooks starts the hooks of a definition for one agent (may be nil).
	AgentHooks func(t Type, agentID string)
	Logger     *slog.Logger
}

// Options start one agent (SpawnSubagent).
type Options struct {
	tools.AgentSpec
	// ParentID is the agent that starts it; empty for the session's main agent.
	ParentID string
	// OutputSchema, when set, tells the agent to finish with one JSON value valid against this JSON schema. A final
	// message that is not gets the agent one more try with the error; Result.Output is the value, or Result.OutputError
	// says why there is none and the agent reads as failed. A schema that cannot be used fails SpawnSubagent.
	OutputSchema json.RawMessage
	// Silent sends no report to the parent and wakes nobody: the caller collects the result with Handle.Wait.
	Silent bool
}

// Result is how an agent ended (Handle.Wait).
type Result struct {
	Info tools.AgentInfo
	// Output is the JSON value of the final message, with Options.OutputSchema; OutputError says why there is none.
	Output      json.RawMessage
	OutputError *OutputError
}

// Codes of OutputError.
const (
	OutputNotJSON = "not_json"
	OutputInvalid = "schema_violation"
)

// OutputError says why an agent's final message is not the output asked for.
type OutputError struct {
	// Code is OutputNotJSON or OutputInvalid.
	Code string `json:"code"`
	// Message is the parser's or the validator's error.
	Message string `json:"message"`
	// Attempts counts the final messages the agent gave.
	Attempts int `json:"attempts"`
}

func (e *OutputError) Error() string { return e.Code + ": " + e.Message }

// Handle follows a started agent.
type Handle struct {
	ID string
	m  *Manager
	a  *sub
}

// Info returns the agent as it is now.
func (h *Handle) Info() tools.AgentInfo {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	return h.a.snapshot()
}

// Wait blocks until the agent's current run ends. A later SendMessage starts another run.
func (h *Handle) Wait(ctx context.Context) (Result, error) {
	for {
		h.m.mu.Lock()
		settled := h.a.settled
		active := !h.a.isSettled
		info := h.a.snapshot()
		output, outputErr := h.a.output, h.a.outputErr
		h.m.mu.Unlock()
		if !active {
			return Result{Info: info, Output: output, OutputError: outputErr}, nil
		}
		select {
		case <-settled:
		case <-ctx.Done():
			return Result{Info: info}, ctx.Err()
		}
	}
}

// ValidateOutput checks an output kept from an earlier run against its schema.
func ValidateOutput(schema, output json.RawMessage) error {
	resolved, err := compileSchema(schema)
	if err != nil {
		return err
	}
	if _, problem := checkOutput(resolved, string(output)); problem != nil {
		return problem
	}
	return nil
}

// compileSchema prepares an output schema for validation.
func compileSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("the output schema is not a JSON schema: %w", err)
	}
	r, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("the output schema cannot be used: %w", err)
	}
	return r, nil
}

// checkOutput validates a final message against the output schema.
func checkOutput(schema *jsonschema.Resolved, report string) (json.RawMessage, *OutputError) {
	out, problem := parseOutput(report)
	if problem != "" {
		return nil, &OutputError{Code: OutputNotJSON, Message: problem}
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, &OutputError{Code: OutputNotJSON, Message: err.Error()}
	}
	if err := schema.Validate(v); err != nil {
		return nil, &OutputError{Code: OutputInvalid, Message: err.Error()}
	}
	return out, nil
}

// parseOutput reads the JSON value of a final message, in a code fence or not.
func parseOutput(report string) (json.RawMessage, string) {
	s := strings.TrimSpace(report)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = strings.TrimSuffix(strings.TrimSpace(s[i+1:]), "```")
			s = strings.TrimSpace(s)
		}
	}
	if !json.Valid([]byte(s)) {
		return nil, "the final message is not valid JSON"
	}
	return json.RawMessage(s), ""
}

// sub is one agent.
type sub struct {
	info     tools.AgentInfo
	depth    int
	typeName string
	readOnly bool
	cwd      string
	wt       *worktree.Result
	silent   bool
	schema   json.RawMessage
	// resolved is schema, ready to validate with; retried: this run had its second try. output and outputErr are the
	// run's result.
	resolved  *jsonschema.Resolved
	retried   bool
	output    json.RawMessage
	outputErr *OutputError
	// recorded is the state last written to the session's transcript.
	recorded tools.AgentState

	ag     *agent.Agent
	drv    *agent.Driver
	writer *session.Writer
	tasks  *tools.TaskManager

	// pending is the input of the run waiting for a slot (none: it starts from reminders alone); waiting: it is in
	// the queue. slot: a run holds one of the session's slots. stopping: Stop was asked for this run.
	pending  []llm.ContentBlock
	waiting  bool
	slot     bool
	stopping bool
	// unreported: see record.
	unreported bool
	// nudges counts the times this team member was told about free tasks; nudgeClaims is what it had claimed at the
	// last one. It isn't told again until it picks something up.
	nudges, nudgeClaims int
	// settled closes when a run ends; isSettled says it has.
	settled   chan struct{}
	isSettled bool
}

func (a *sub) snapshot() tools.AgentInfo {
	info := a.info
	info.Steps = append([]tools.AgentStep(nil), a.info.Steps...)
	return info
}

// record is what stays on disk of an agent.
type record struct {
	Info     tools.AgentInfo  `json:"info"`
	Depth    int              `json:"depth"`
	Cwd      string           `json:"cwd"`
	ReadOnly bool             `json:"read_only,omitempty"`
	Worktree *worktree.Result `json:"worktree,omitempty"`
	// Silent: it reports to the code that started it, which goes with the client.
	Silent bool `json:"silent,omitempty"`
	// Unreported: it was cut off when the session closed, and the main agent has not been told.
	Unreported bool `json:"unreported,omitempty"`
}

// Manager is the session's set of agents. It implements tools.Agents.
type Manager struct {
	cfg  Config
	ctx  context.Context
	stop context.CancelFunc

	// lead is the session's main agent (Attach).
	lead *agent.Agent

	mu      sync.Mutex
	subs    map[string]*sub
	order   []*sub
	queue   []*sub
	running int
	closed  bool
	// maxDepth and maxRunning are the limits as last read (refreshLimits).
	maxDepth, maxRunning int
	// reviveMu orders rebuilding agents of an earlier run of the client.
	reviveMu sync.Mutex
	// pubMu orders the published snapshots.
	pubMu sync.Mutex
}

var _ tools.Agents = (*Manager)(nil)

// New creates a manager. Attach it to the session's main agent before use.
func New(cfg Config) *Manager {
	ctx, stop := context.WithCancel(context.Background())
	return &Manager{cfg: cfg, ctx: ctx, stop: stop, subs: map[string]*sub{}, maxDepth: settings.DefaultSubagentMaxDepth, maxRunning: settings.DefaultSubagentMaxRunning}
}

// refreshLimits reads the limits again and returns the depth limit.
func (m *Manager) refreshLimits() int {
	depth, running := settings.DefaultSubagentMaxDepth, settings.DefaultSubagentMaxRunning
	if m.cfg.Limits != nil {
		depth, running = m.cfg.Limits()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxDepth, m.maxRunning = depth, running
	return depth
}

// Attach sets the session's main agent and loads the agents of an earlier run: those that were running read as
// interrupted, and the main agent is told so with its next request.
func (m *Manager) Attach(lead *agent.Agent) {
	m.lead = lead
	m.load()
}

func (m *Manager) log() *slog.Logger {
	if m.cfg.Logger != nil {
		return m.cfg.Logger
	}
	return slog.Default()
}

func (m *Manager) metaPath(id string) string {
	return filepath.Join(m.cfg.Dir, "agent-"+id+".meta.json")
}

func (m *Manager) transcriptPath(id string) string {
	return filepath.Join(m.cfg.Dir, "agent-"+id+".jsonl")
}

// save writes the agent's record (m.mu held), and into the session's transcript when its state changed.
func (m *Manager) save(a *sub) {
	if w := m.cfg.LeadTranscript; w != nil && a.recorded != a.info.State {
		a.recorded = a.info.State
		if data, err := json.Marshal(a.snapshot()); err == nil {
			_, _ = w.Append(session.Entry{Type: session.TypeMeta, Subagent: data})
		}
	}
	if m.cfg.NoPersistence || m.cfg.Dir == "" {
		return
	}
	data, err := json.Marshal(record{Info: a.snapshot(), Depth: a.depth, Cwd: a.cwd, ReadOnly: a.readOnly, Worktree: a.wt, Silent: a.silent, Unreported: a.unreported})
	if err != nil {
		return
	}
	if err := os.MkdirAll(m.cfg.Dir, 0o700); err != nil {
		m.log().Warn("agent record", "err", err)
		return
	}
	tmp := m.metaPath(a.info.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		m.log().Warn("agent record", "err", err)
		return
	}
	if err := os.Rename(tmp, m.metaPath(a.info.ID)); err != nil {
		m.log().Warn("agent record", "err", err)
	}
}

func (m *Manager) load() {
	if m.cfg.NoPersistence || m.cfg.Dir == "" {
		return
	}
	paths, _ := filepath.Glob(filepath.Join(m.cfg.Dir, "agent-*.meta.json"))
	m.mu.Lock()
	var lost []*sub
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r record
		if json.Unmarshal(data, &r) != nil || r.Info.ID == "" {
			continue
		}
		a := &sub{info: r.Info, depth: r.Depth, cwd: r.Cwd, readOnly: r.ReadOnly, wt: r.Worktree, typeName: r.Info.Type, silent: r.Silent, settled: closedChan(), isSettled: true}
		if a.info.State.Active() || r.Unreported {
			m.settleLocked(a, tools.AgentInterrupted)
			if !a.silent {
				lost = append(lost, a)
			}
		}
		m.subs[a.info.ID] = a
		m.order = append(m.order, a)
	}
	sort.SliceStable(m.order, func(i, j int) bool { return m.order[i].info.StartedAt < m.order[j].info.StartedAt })
	m.mu.Unlock()
	for _, a := range lost {
		// Nested agents report to the main agent too: the agent that started them stopped with them.
		m.lead.Remind(agent.ReminderAgentReport, m.reportVars(a))
	}
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "a" + hex.EncodeToString(b)
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Types implements tools.Agents.
func (m *Manager) Types() []tools.AgentType {
	var out []tools.AgentType
	for _, t := range m.allTypes() {
		out = append(out, t.AgentType)
	}
	return out
}

func (m *Manager) allTypes() []Type { return append(BuiltinTypes(), m.cfg.CustomTypes...) }

func (m *Manager) typeOf(name string) (Type, bool) {
	for _, t := range m.allTypes() {
		if t.Name == name {
			return t, true
		}
	}
	return Type{}, false
}

// resolveModel takes a model id or a slot name ("small").
func (m *Manager) resolveModel(want string) (string, error) {
	if want == "" {
		return m.lead.Model(), nil
	}
	if m.cfg.Catalog == nil {
		return want, nil
	}
	if _, ok := m.cfg.Catalog.Find(want); ok {
		return want, nil
	}
	if id := m.cfg.Catalog.Slots[want]; id != "" {
		return id, nil
	}
	ids := make([]string, 0, len(m.cfg.Catalog.Models))
	for _, c := range m.cfg.Catalog.Models {
		ids = append(ids, c.ID)
	}
	return "", fmt.Errorf("unknown model %q; available: %s", want, strings.Join(ids, ", "))
}

// Spawn implements tools.Agents: the Agent tool.
func (m *Manager) Spawn(ctx context.Context, caller string, spec tools.AgentSpec) (tools.AgentInfo, error) {
	h, err := m.SpawnSubagent(ctx, Options{AgentSpec: spec, ParentID: caller})
	if err != nil {
		return tools.AgentInfo{}, err
	}
	return h.Info(), nil
}

// SpawnSubagent starts an agent in the background and returns at once: the entry point for code that runs agents
// (the Agent tool goes through here too). The depth limit applies; an agent over the concurrency cap waits for a slot.
func (m *Manager) SpawnSubagent(ctx context.Context, o Options) (*Handle, error) {
	typeName := o.Type
	if typeName == "" {
		typeName = TypeGeneral
	}
	t, ok := m.typeOf(typeName)
	if !ok {
		names := make([]string, 0, 4)
		for _, c := range m.allTypes() {
			names = append(names, c.Name)
		}
		return nil, fmt.Errorf("unknown subagent_type %q; available: %s", typeName, strings.Join(names, ", "))
	}
	var resolved *jsonschema.Resolved
	if len(o.OutputSchema) > 0 {
		var err error
		if resolved, err = compileSchema(o.OutputSchema); err != nil {
			return nil, err
		}
	}
	maxDepth := m.refreshLimits()
	depth := 1
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("the session is closing")
	}
	if o.ParentID != "" {
		p := m.subs[o.ParentID]
		if p == nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("no agent %s", o.ParentID)
		}
		depth = p.depth + 1
	}
	m.mu.Unlock()
	if depth > maxDepth {
		return nil, fmt.Errorf("agents nest at most %d levels deep: this one cannot start another", maxDepth)
	}
	want := o.Model
	if want == "" {
		want = t.Model
	}
	model, err := m.resolveModel(want)
	if err != nil {
		return nil, err
	}
	id := newID()
	a := &sub{depth: depth, typeName: t.Name, readOnly: t.ReadOnly, cwd: m.cfg.Cwd, silent: o.Silent, schema: o.OutputSchema, resolved: resolved, isSettled: true, settled: closedChan(),
		info: tools.AgentInfo{ID: id, ParentID: o.ParentID, ToolUseID: o.ToolUseID, Name: o.Description, Team: o.Team, Type: t.Name, Model: model, State: tools.AgentQueued, StartedAt: rfc3339(time.Now())}}
	if o.Isolation == tools.IsolationWorktree {
		r, err := worktree.Create(ctx, m.cfg.Cwd, worktree.Options{Name: "agent-" + id})
		if err != nil {
			return nil, fmt.Errorf("cannot create a worktree for the agent: %w", err)
		}
		a.wt, a.cwd = r, r.Cwd
		a.info.WorktreePath, a.info.WorktreeBranch = r.Path, r.Branch
		if m.cfg.AddWorkDir != nil {
			m.cfg.AddWorkDir(r.Path)
		}
	}
	if !m.cfg.NoPersistence && m.cfg.Dir != "" {
		a.info.Transcript = m.transcriptPath(id)
	}
	if err := m.build(ctx, a, t, nil); err != nil {
		if a.wt != nil {
			_ = worktree.Rollback(context.WithoutCancel(ctx), a.wt)
		}
		return nil, err
	}
	input := []llm.ContentBlock{llm.TextBlock(m.contextText(a, t))}
	if o.Team != "" {
		input = append(input, llm.TextBlock(m.teamContext(a)))
	}
	input = append(input, llm.TextBlock(m.wrapMessage(a, o.ParentID, withSchema(o.Prompt, o.OutputSchema))))

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		if a.writer != nil {
			_ = a.writer.Close()
		}
		return nil, errors.New("the session is closing")
	}
	m.subs[id] = a
	m.order = append(m.order, a)
	m.launchLocked(a, input)
	m.save(a)
	m.mu.Unlock()
	m.publish(a)
	return &Handle{ID: id, m: m, a: a}, nil
}

func withSchema(prompt string, schema json.RawMessage) string {
	if len(schema) == 0 {
		return prompt
	}
	return prompt + "\n\nEnd your work with a final message that is one JSON value and nothing else, matching this JSON schema:\n" + string(schema)
}

// contextText is the first block of an agent's first message: what it is and who speaks to it.
func (m *Manager) contextText(a *sub, t Type) string {
	bundle := m.bundleOf(a)
	instructions := t.Instructions
	if t.reminder != "" {
		instructions = bundle.Reminder(t.reminder, "")
	}
	return prompts.Render(bundle.Reminder("agent_context", agentContextFallback), map[string]string{
		"agent_id": a.info.ID, "agent_type": t.Name, "instructions": instructions,
	}, nil)
}

func (m *Manager) bundleOf(a *sub) *prompts.Bundle {
	m.mu.Lock()
	ag := a.ag
	m.mu.Unlock()
	if ag != nil {
		return ag.Bundle()
	}
	return m.lead.Bundle()
}

// Fallbacks for a bundle without the texts (they are never empty in a shipped bundle).
const (
	agentContextFallback = "You are subagent {{agent_id}} ({{agent_type}}). Finish with a final message reporting what you found or did.\n{{instructions}}"
	agentMessageFallback = "<agent_message from=\"{{from}}\">\n{{message}}\n</agent_message>"
	agentOutputFallback  = "Your final message is not the output asked for ({{error}}). Reply with one JSON value and nothing else, matching this JSON schema:\n{{schema}}"
)

// senderName names an agent in the message blocks.
func (m *Manager) senderName(id string) string {
	if id == "" {
		return "the main agent"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.subs[id]; s != nil {
		return fmt.Sprintf("agent %s (%s)", id, s.info.Name)
	}
	return "agent " + id
}

// wrapMessage marks text from another agent as such. A block that closes itself early or forges an application
// reminder could pass for something else, so those tags are defused.
func (m *Manager) wrapMessage(to *sub, from, text string) string {
	tmpl := m.bundleOf(to).Reminder("agent_message", agentMessageFallback)
	return prompts.Render(tmpl, map[string]string{"from": defuse(m.senderName(from)), "message": defuse(text)}, nil)
}

// Defuse is defuse, for what carries model output to an agent from elsewhere (package workflow).
func Defuse(s string) string { return defuse(s) }

// defuse keeps model output from closing or opening the blocks the application marks things with.
func defuse(s string) string {
	for _, tag := range []string{"agent_message", "agent_report", "workflow_report", "system-reminder"} {
		s = strings.ReplaceAll(s, "</"+tag, "&lt;/"+tag)
		s = strings.ReplaceAll(s, "<"+tag, "&lt;"+tag)
	}
	return s
}

// registryFor picks the tools of an agent.
func (m *Manager) registryFor(t Type, depth int) *tools.Registry {
	allowed := map[string]bool{}
	for _, n := range t.Tools {
		allowed[n] = true
	}
	m.mu.Lock()
	maxDepth := m.maxDepth
	m.mu.Unlock()
	reg := tools.NewRegistry()
	for _, n := range m.cfg.Tools.Names() {
		if leadOnlyTools[n] || (t.Tools != nil && !allowed[n]) {
			continue
		}
		if n == "Agent" && depth >= maxDepth {
			continue
		}
		if t.ReadOnly && (n == "Agent" || n == "SendMessage" || n == "ListAgents") {
			continue
		}
		if tool, ok := m.cfg.Tools.Get(n); ok {
			reg.Register(tool)
		}
	}
	return reg
}

// checker is the permission check of one agent: the session's engine, with the agent named for the prompt to the
// user and the user's own words as the intent. What the agent was told is another model's output, and must not
// count as the user asking for something.
type checker struct {
	m        *Manager
	id, name string
	readOnly bool
}

func (c *checker) Check(ctx context.Context, req permissions.Request) permissions.Decision {
	if c.readOnly && !req.ReadOnly {
		return permissions.Decision{Behavior: permissions.Deny, ResolvedBy: "agent", Message: fmt.Sprintf("This agent type is read-only: this %s call is not allowed. Report what should be done instead.", req.ToolName)}
	}
	req.AgentID, req.AgentName = c.id, c.name
	req.RecentUserMessages = c.m.lead.RecentPrompts()
	return c.m.cfg.Permissions.Check(ctx, req)
}

// build creates the agent's loop. entries is its transcript when it continues one of an earlier run.
func (m *Manager) build(ctx context.Context, a *sub, t Type, entries []session.Entry) error {
	id, model := a.info.ID, a.info.Model
	bundle, err := m.cfg.Bundle(ctx, model)
	if err != nil {
		return fmt.Errorf("no prompts for model %s: %w", model, err)
	}
	conv := session.Rebuild(entries)
	writer, err := session.OpenWriter(m.transcriptPath(id), session.WriterOptions{
		SessionID: m.cfg.SessionID, Cwd: a.cwd, GitBranch: m.cfg.GitBranch, Version: m.cfg.Version, Entrypoint: m.cfg.Entrypoint,
		LastUUID: conv.LastUUID, Initial: entries, AgentID: id, Disabled: m.cfg.NoPersistence || m.cfg.Dir == "",
	})
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		_, _ = writer.Append(session.Entry{Type: session.TypeMeta, PromptBundleVersion: bundle.Version, Model: model})
	}

	lead := m.cfg.LeadEnv
	scratch := filepath.Join(lead.ScratchDir, "agents", id)
	env := tools.NewEnv(a.cwd, scratch)
	if dirs := lead.WorkingDirs(); len(dirs) > 1 {
		env.AdditionalDirs = dirs[1:]
	}
	env.SetExtraEnv(lead.ExtraEnv())
	env.Sandbox = lead.Sandbox
	env.SessionID, env.PlansDir, env.RipgrepPath = lead.SessionID, lead.PlansDir, lead.RipgrepPath
	env.Agents, env.AgentID = m, id
	env.TaskGraph = lead.TaskGraph
	env.Edits = lead.Edits
	env.Skills = lead.Skills
	env.Library = lead.Library
	if a.wt == nil {
		// Checkpoints follow the session's folder; a worktree has git for that.
		env.BeforeEdit = lead.BeforeEdit
	}
	if lead.Tasks != nil {
		env.Tasks = tools.NewTaskManager(filepath.Join(scratch, "tasks"))
		env.Tasks.Unattended = lead.Tasks.Unattended
	}
	var (
		maxTokens     = 32000
		display       string
		contextWindow int
		thinking      bool
	)
	effort := m.lead.Effort()
	if info, ok := m.cfg.Catalog.Find(model); ok {
		display, contextWindow, thinking = info.DisplayName, info.ContextWindow, info.Capabilities.Thinking
		if info.MaxOutputTokens > 0 && info.MaxOutputTokens < maxTokens {
			maxTokens = info.MaxOutputTokens
		}
		if !hasEffort(info.Capabilities.Effort, effort) {
			effort = ""
		}
	}
	vars := make(map[string]string, len(m.cfg.Vars))
	for k, v := range m.cfg.Vars {
		vars[k] = v
	}
	vars["cwd"], vars["scratchpad_dir"] = a.cwd, scratch

	reg := m.registryFor(t, a.depth)
	extra := m.cfg.ExtraTools
	if t.ReadOnly || t.Tools != nil {
		extra = nil
	}
	ag := agent.New(agent.Config{
		Gateway: m.cfg.Gateway, Bundle: bundle, Tools: reg, ToolEnv: env,
		Permissions: &checker{m: m, id: id, name: a.info.Name, readOnly: a.readOnly},
		Transcript:  writer,
		Model:       model, ModelDisplayName: display, MaxTokens: maxTokens, ContextWindow: contextWindow, ThinkingCapable: thinking,
		AutoCompactPct: m.cfg.AutoCompactPct,
		RefreshBundle:  func(ctx context.Context) (*prompts.Bundle, error) { return m.cfg.Bundle(ctx, model) },
		Effort:         effort, SessionID: m.cfg.SessionID, RequestClass: "subagent", AgentID: id,
		Vars: vars, ProjectContext: m.cfg.ProjectContext, MCPInstructions: m.cfg.MCPInstructions, History: conv,
		DisabledTools: m.cfg.DisabledTools, ExtraTools: extra, PermissionMode: m.agentMode,
		DisableInterruptReply: true, UsageMonitor: m.cfg.UsageMonitor, Skills: m.cfg.Skills, Hooks: m.cfg.Hooks, AgentType: a.info.Type,
		OnEvent: func(e agent.Event) { m.onAgentEvent(a, e) }, Logger: m.cfg.Logger,
	})
	env.Reminders = func(k string) string { return ag.Bundle().Reminder(k, "") }
	if env.Tasks != nil {
		env.Tasks.OnEvent = ag.OnTaskEvent
	}
	if m.cfg.AgentHooks != nil {
		m.cfg.AgentHooks(t, id)
	}
	drv := agent.NewDriver(m.ctx, ag, func(res agent.TurnResult) { m.onResult(a, res) })

	m.mu.Lock()
	a.ag, a.drv, a.writer, a.tasks = ag, drv, writer, env.Tasks
	m.mu.Unlock()
	return nil
}

func hasEffort(levels []string, effort string) bool {
	if effort == "" {
		return true
	}
	for _, l := range levels {
		if l == effort {
			return true
		}
	}
	return false
}

// agentMode is the mode an agent's loop works under. Plan mode reads as default here: the plan and its approval
// belong to the main agent (the plan_mode reminder is not for an agent), and the engine, which sees the real mode,
// still refuses the agent's changes.
func (m *Manager) agentMode() string {
	mode := ""
	if m.cfg.Mode != nil {
		mode = m.cfg.Mode()
	}
	if mode == string(permissions.ModePlan) {
		return string(permissions.ModeDefault)
	}
	return mode
}

// launchLocked gets a run of the agent going, now or when a slot frees up (m.mu held). input nil starts from the
// agent's reminders alone.
func (m *Manager) launchLocked(a *sub, input []llm.ContentBlock) {
	a.pending = append(a.pending, input...)
	a.stopping = false
	a.retried, a.output, a.outputErr = false, nil, nil
	a.info.State = tools.AgentQueued
	a.info.FinishedAt = ""
	a.info.Error = ""
	if a.isSettled {
		a.settled, a.isSettled = make(chan struct{}), false
	}
	if !a.waiting {
		a.waiting = true
		m.queue = append(m.queue, a)
	}
	m.pumpLocked()
}

// pumpLocked starts waiting runs while there are free slots (m.mu held).
func (m *Manager) pumpLocked() {
	for m.running < m.maxRunning && len(m.queue) > 0 && !m.closed {
		a := m.queue[0]
		m.queue = m.queue[1:]
		a.waiting = false
		input := a.pending
		a.pending = nil
		a.slot = true
		m.running++
		a.info.State = tools.AgentRunning
		if len(input) > 0 {
			a.drv.Submit("", input)
			continue
		}
		a.drv.Wake()
		if !a.drv.Busy() {
			// Nothing to answer after all (the reminder went out with a turn that just ended).
			a.slot = false
			m.running--
			m.settleLocked(a, tools.AgentFinished)
			a.isSettled = true
			close(a.settled)
		}
	}
}

// settleLocked ends a run (m.mu held).
func (m *Manager) settleLocked(a *sub, state tools.AgentState) {
	a.info.State = state
	a.info.CurrentTool = ""
	a.info.FinishedAt = rfc3339(time.Now())
	for i := range a.info.Steps {
		if a.info.Steps[i].Status == tools.AgentStepRunning {
			a.info.Steps[i].Status = tools.AgentStepFailed
		}
	}
	m.save(a)
}

// releaseWaiters lets those waiting for the run go on, once its report has been delivered too (unless another run
// has started since).
func (m *Manager) releaseWaiters(a *sub) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !a.isSettled && !a.info.State.Active() {
		a.isSettled = true
		close(a.settled)
	}
}

func (m *Manager) onResult(a *sub, res agent.TurnResult) {
	m.mu.Lock()
	if a.ag.QueueLen() > 0 && !a.stopping && !m.closed {
		// A message came in as the turn ended: the next turn starts at once, this run goes on.
		m.mu.Unlock()
		return
	}
	closing := m.closed
	if a.resolved != nil && !closing && !a.stopping && !res.Interrupted && !res.IsError {
		out, problem := checkOutput(a.resolved, res.Result)
		if problem != nil && !a.retried {
			// One more try, told what was wrong. The run goes on.
			a.retried = true
			m.mu.Unlock()
			input := m.outputRetry(a, problem)
			m.mu.Lock()
			if !m.closed {
				a.drv.Submit("", input)
				m.mu.Unlock()
				return
			}
			closing = true
		} else {
			a.output = out
			if problem != nil {
				problem.Attempts = 2
				a.outputErr = problem
				a.info.Error = "the output is not what was asked for: " + problem.Error()
				res.IsError = true
				res.Errors = []string{a.info.Error}
			}
		}
	}
	if m.teamContinuesLocked(a, res, closing) {
		return
	}
	state := tools.AgentFinished
	switch {
	case closing:
		state = tools.AgentInterrupted
		a.unreported = true
	case a.stopping || res.Interrupted:
		state = tools.AgentStopped
	case res.IsError:
		state = tools.AgentFailed
		a.info.Error = strings.Join(res.Errors, "; ")
	}
	a.info.Report = truncateReport(res.Result)
	if a.slot {
		a.slot = false
		m.running--
	}
	m.settleLocked(a, state)
	m.pumpLocked()
	tasks := a.tasks
	silent := a.silent
	m.mu.Unlock()
	m.releaseClaims(a)
	m.publish(a)
	if tasks != nil {
		// The agent's background commands end with its run, which is what they were started for.
		go tasks.StopAll(tools.TaskSessionClosed)
	}
	if !closing && !silent {
		m.deliver(a, state)
	}
	m.releaseWaiters(a)
}

func truncateReport(s string) string {
	if r := []rune(s); len(r) > maxReportChars {
		return string(r[:maxReportChars]) + "\n[The report continues in the agent's transcript.]"
	}
	return s
}

// outputRetry is the message that sends an agent back to fix its output.
func (m *Manager) outputRetry(a *sub, problem *OutputError) []llm.ContentBlock {
	tmpl := m.bundleOf(a).Reminder("agent_output_invalid", agentOutputFallback)
	return []llm.ContentBlock{llm.TextBlock(prompts.Render(tmpl, map[string]string{"error": defuse(problem.Error()), "schema": defuse(string(a.schema))}, nil))}
}

// reportVars fills the agent_report reminder.
func (m *Manager) reportVars(a *sub) map[string]string {
	m.mu.Lock()
	info := a.snapshot()
	m.mu.Unlock()
	status := "finished"
	switch info.State {
	case tools.AgentFailed:
		status = "failed: " + defuse(info.Error)
	case tools.AgentStopped:
		status = "was stopped"
	case tools.AgentInterrupted:
		status = "was interrupted when the client stopped; SendMessage continues it"
	}
	report := info.Report
	if strings.TrimSpace(report) == "" {
		report = "(no final message)"
	}
	var details []string
	if info.Transcript != "" {
		details = append(details, "Its transcript is "+info.Transcript+".")
	}
	if info.WorktreePath != "" {
		details = append(details, fmt.Sprintf("It worked in the git worktree %s on branch %s; its changes stay there until you bring them over.", info.WorktreePath, info.WorktreeBranch))
	}
	return map[string]string{"agent_id": info.ID, "name": defuse(info.Name), "agent_type": info.Type, "status": status, "report": defuse(report), "details": strings.Join(details, " ")}
}

// deliver tells the agent that started this one how it ended, and wakes it when idle. An agent stopped on request
// wakes nobody: whoever stopped it knows.
func (m *Manager) deliver(a *sub, state tools.AgentState) {
	vars := m.reportVars(a)
	m.mu.Lock()
	var parent *sub
	if a.info.ParentID != "" {
		parent = m.subs[a.info.ParentID]
	}
	if parent != nil && parent.ag != nil {
		parent.ag.Remind(agent.ReminderAgentReport, vars)
		if state != tools.AgentStopped && !parent.info.State.Active() {
			m.launchLocked(parent, nil)
			m.save(parent)
			m.mu.Unlock()
			m.publish(parent)
			return
		}
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.lead.Remind(agent.ReminderAgentReport, vars)
	if state != tools.AgentStopped {
		m.lead.Wake()
	}
}

func (m *Manager) onAgentEvent(a *sub, e agent.Event) {
	switch e.Kind {
	case agent.EventTurnStarted:
		m.mu.Lock()
		if a.isSettled {
			// A message that came in just as the last run ended.
			a.settled, a.isSettled = make(chan struct{}), false
			a.info.FinishedAt, a.info.Error = "", ""
		}
		a.info.State = tools.AgentRunning
		m.save(a)
		m.mu.Unlock()
		m.publish(a)
	case agent.EventAssistant:
		if e.Response == nil {
			return
		}
		m.mu.Lock()
		u := e.Response.Usage
		a.info.InputTokens += int64(u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens)
		a.info.OutputTokens += int64(u.OutputTokens)
		a.info.CostUSD += e.Response.Harness.CostUSD
		m.mu.Unlock()
		m.publish(a)
	case agent.EventToolStart:
		m.mu.Lock()
		a.info.ToolCalls++
		a.info.CurrentTool = e.ToolName
		a.info.Steps = append(a.info.Steps, tools.AgentStep{ToolUseID: e.ToolUseID, Tool: e.ToolName, Target: Target(e.Input), Status: tools.AgentStepRunning})
		if n := len(a.info.Steps); n > maxSteps {
			a.info.Steps = append([]tools.AgentStep(nil), a.info.Steps[n-maxSteps:]...)
		}
		m.mu.Unlock()
		m.publish(a)
	case agent.EventToolEnd:
		m.mu.Lock()
		running := ""
		for i := range a.info.Steps {
			s := &a.info.Steps[i]
			if s.ToolUseID == e.ToolUseID {
				s.Status = tools.AgentStepDone
				if e.IsError {
					s.Status = tools.AgentStepFailed
				}
			}
			if s.Status == tools.AgentStepRunning {
				running = s.Tool
			}
		}
		a.info.CurrentTool = running
		m.mu.Unlock()
		m.publish(a)
	case agent.EventUsageLimit:
		// The monitor is the session's: this is news for it, whoever hit the limit first.
		m.lead.Emit(e)
	}
}

// Target picks what a tool call acts on from its input: a path, a pattern, a command...
func Target(input json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	for _, k := range []string{"file_path", "path", "pattern", "command", "url", "query", "description", "to", "task_id"} {
		if v, ok := in[k].(string); ok && v != "" {
			if r := []rune(v); len(r) > 120 {
				return string(r[:120]) + "…"
			}
			return v
		}
	}
	return ""
}

// publish sends the agent as it is now to the session's frontends. Snapshots go out in the order they were taken.
func (m *Manager) publish(a *sub) {
	m.pubMu.Lock()
	defer m.pubMu.Unlock()
	m.mu.Lock()
	info := a.snapshot()
	m.mu.Unlock()
	if m.lead != nil {
		m.lead.Emit(agent.Event{Kind: agent.EventSubagent, Subagent: &info})
	}
}

// resolve finds an agent by id or unique name (m.mu held).
func (m *Manager) resolve(to string) (*sub, error) {
	if a := m.subs[to]; a != nil {
		return a, nil
	}
	var found *sub
	for _, a := range m.order {
		if a.info.Name == to {
			if found != nil {
				return nil, fmt.Errorf("several agents are named %q; use the id", to)
			}
			found = a
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no agent %q in this session", to)
	}
	return found, nil
}

// revive rebuilds an agent of an earlier run of the client from its transcript.
func (m *Manager) revive(ctx context.Context, a *sub) error {
	m.reviveMu.Lock()
	defer m.reviveMu.Unlock()
	m.mu.Lock()
	done := a.ag != nil
	m.mu.Unlock()
	if done {
		return nil
	}
	if a.wt != nil {
		if _, err := os.Stat(a.cwd); err != nil {
			return fmt.Errorf("the worktree of agent %s is gone: %w", a.info.ID, err)
		}
		if m.cfg.AddWorkDir != nil {
			m.cfg.AddWorkDir(a.wt.Path)
		}
	}
	entries, err := session.Load(m.transcriptPath(a.info.ID))
	if err != nil {
		return fmt.Errorf("cannot read the transcript of agent %s: %w", a.info.ID, err)
	}
	m.refreshLimits()
	t, ok := m.typeOf(a.typeName)
	if !ok {
		// A definition that is gone: the agent goes on under the rules it is recorded with.
		t = Type{AgentType: tools.AgentType{Name: a.typeName, ReadOnly: a.readOnly}}
		if a.readOnly {
			t.Tools = readOnlyTools
		}
	}
	return m.build(ctx, a, t, entries)
}

// Send implements tools.Agents: SendMessage.
func (m *Manager) Send(ctx context.Context, caller, to, message string) (tools.AgentInfo, bool, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return tools.AgentInfo{}, false, errors.New("the session is closing")
	}
	if to == tools.GraphLead && caller != "" {
		m.mu.Unlock()
		return m.tellLead(caller, message), true, nil
	}
	a, err := m.resolve(to)
	if err != nil {
		m.mu.Unlock()
		return tools.AgentInfo{}, false, err
	}
	if a.info.ID == caller {
		m.mu.Unlock()
		return tools.AgentInfo{}, false, errors.New("an agent cannot send a message to itself")
	}
	lost := a.ag == nil
	m.mu.Unlock()
	if lost {
		if err := m.revive(ctx, a); err != nil {
			return tools.AgentInfo{}, false, err
		}
	}
	input := []llm.ContentBlock{llm.TextBlock(m.wrapMessage(a, caller, message))}

	m.mu.Lock()
	queued := false
	switch a.info.State {
	case tools.AgentRunning:
		// Injected after the current tool calls; if the turn just ended, the driver starts the next one with it.
		a.drv.Submit("", input)
		queued = true
	case tools.AgentQueued:
		a.pending = append(a.pending, input...)
		queued = true
	default:
		// Whoever continues it wants the report, also of an agent that first ran for code.
		a.silent = false
		m.launchLocked(a, input)
		m.save(a)
	}
	info := a.snapshot()
	m.mu.Unlock()
	m.publish(a)
	return info, queued, nil
}

// Stop implements tools.Agents: TaskStop on an agent, and the frontends' stop. The agents it started stop with it.
// An agent cannot stop itself or an agent above it.
func (m *Manager) Stop(caller, id string) (tools.AgentInfo, error) {
	m.mu.Lock()
	a := m.subs[id]
	if a == nil {
		m.mu.Unlock()
		return tools.AgentInfo{}, fmt.Errorf("no agent %s", id)
	}
	for c := caller; c != ""; {
		if c == id {
			m.mu.Unlock()
			return tools.AgentInfo{}, errors.New("an agent cannot stop itself or an agent above it")
		}
		p := m.subs[c]
		if p == nil {
			break
		}
		c = p.info.ParentID
	}
	var children []string
	for _, s := range m.order {
		if s.info.ParentID == id && s.info.State.Active() {
			children = append(children, s.info.ID)
		}
	}
	if !a.info.State.Active() && len(children) == 0 {
		info := a.snapshot()
		m.mu.Unlock()
		return info, tools.ErrAgentNotRunning
	}
	wasActive := a.info.State.Active()
	m.mu.Unlock()
	for _, c := range children {
		_, _ = m.Stop("", c)
	}
	if wasActive {
		m.stopOne(a)
	}
	m.mu.Lock()
	info := a.snapshot()
	m.mu.Unlock()
	return info, nil
}

func (m *Manager) stopOne(a *sub) {
	m.mu.Lock()
	if a.waiting {
		// Never started this run.
		for i, q := range m.queue {
			if q == a {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		a.waiting, a.pending = false, nil
		m.settleLocked(a, tools.AgentStopped)
		silent := a.silent
		m.mu.Unlock()
		m.publish(a)
		if !silent {
			m.deliver(a, tools.AgentStopped)
		}
		m.releaseWaiters(a)
		return
	}
	a.stopping = true
	settled, ag, drv := a.settled, a.ag, a.drv
	m.mu.Unlock()
	_ = ag.DrainQueue()
	deadline := time.After(stopWait)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		// Again until it ends: a turn that is just starting has nothing to interrupt yet.
		drv.Interrupt()
		select {
		case <-settled:
			return
		case <-deadline:
			m.log().Warn("agent did not stop in time", "agent", a.info.ID)
			return
		case <-tick.C:
		}
	}
}

// Get implements tools.Agents.
func (m *Manager) Get(id string) (tools.AgentInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.subs[id]
	if a == nil {
		return tools.AgentInfo{}, false
	}
	return a.snapshot(), true
}

// List implements tools.Agents: the session's agents, oldest first.
func (m *Manager) List() []tools.AgentInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tools.AgentInfo, 0, len(m.order))
	for _, a := range m.order {
		out = append(out, a.snapshot())
	}
	return out
}

// Active counts the agents still at work, their reports not yet delivered.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, a := range m.order {
		if !a.isSettled {
			n++
		}
	}
	return n
}

// WaitIdle blocks until no agent is queued or running (headless runs, which end with their process).
func (m *Manager) WaitIdle(ctx context.Context) error {
	for {
		m.mu.Lock()
		var wait <-chan struct{}
		for _, a := range m.order {
			if !a.isSettled {
				wait = a.settled
				break
			}
		}
		m.mu.Unlock()
		if wait == nil {
			return nil
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Entries returns the transcript of an agent.
func (m *Manager) Entries(id string) ([]session.Entry, error) {
	m.mu.Lock()
	a := m.subs[id]
	var w *session.Writer
	if a != nil {
		w = a.writer
	}
	m.mu.Unlock()
	if a == nil {
		return nil, fmt.Errorf("no agent %s", id)
	}
	if w != nil {
		return w.Entries(), nil
	}
	return session.Load(m.transcriptPath(id))
}

// Shutdown ends every agent (the session is closing): those still running read as interrupted. Worktrees nothing was
// done in go away. Safe to call more than once.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	all := append([]*sub(nil), m.order...)
	for _, a := range m.queue {
		a.waiting, a.pending, a.unreported = false, nil, true
		m.settleLocked(a, tools.AgentInterrupted)
		a.isSettled = true
		close(a.settled)
	}
	m.queue = nil
	m.mu.Unlock()
	m.stop()
	var wg sync.WaitGroup
	for _, a := range all {
		m.mu.Lock()
		drv, tasks, writer, wt := a.drv, a.tasks, a.writer, a.wt
		m.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if drv != nil {
				drv.Stop()
				drv.Wait()
			}
			if tasks != nil {
				tasks.StopAll(tools.TaskSessionClosed)
			}
			if writer != nil {
				_ = writer.Close()
			}
			if wt != nil {
				ctx, cancel := context.WithTimeout(context.Background(), worktreeCleanupTimeout)
				defer cancel()
				if _, err := worktree.RemoveIfUntouched(ctx, wt); err != nil {
					m.log().Warn("remove agent worktree", "path", wt.Path, "err", err)
				}
			}
		}()
	}
	wg.Wait()
}
