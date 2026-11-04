// Package workflow runs the scripts of the Workflow tool: a small program the session's model writes to run many
// subagents, so that big, parallel work does not flood its own context. A script runs in a sandboxed interpreter
// (engine.go) and has no effect on anything except through agent(), whose tool calls go through the session's
// permission checks like any agent's.
//
// goja is the interpreter: it runs async functions and promises, needs no cgo, and exposes nothing of the host unless
// it is given something. What a script returns is built from agents' reports, so it reaches the main agent as model
// output, inside a marked block.
package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/subagent"
	"github.com/xlsft/harness/packages/client/tools"
)

// Limits of what a run keeps and passes on.
const (
	// maxScriptBytes bounds a script from a file; maxResultChars what the reminder carries of a result (the rest stays
	// in a file next to the run); maxLogLines the script's log kept for the frontends.
	maxScriptBytes = 256 << 10
	maxResultChars = 50000
	maxLogLines    = 200
	maxLogChars    = 2000
	// stopWait is how long a stop or the end of a run waits for what it ended.
	stopWait = 8 * time.Second
	// maxPlans bounds the scripts read for calls that were then refused.
	maxPlans = 32
)

// Config is what a session gives its workflows.
type Config struct {
	// Dir holds the records of the runs: <transcript without .jsonl>/workflows.
	Dir string
	// NoPersistence keeps runs in memory.
	NoPersistence bool
	// LeadTranscript is the session's transcript: it records the runs' states, so that another machine can list them.
	LeadTranscript *session.Writer
	Agents         *subagent.Manager
	// Env is the tool environment of the session's main agent: script paths resolve in it and its read rules apply.
	Env *tools.Env
	// SearchDirs lists where saved workflows are looked for, later entries winning (SearchDirs below).
	SearchDirs func() []string
	// MaxAgents reads the hard cap on agent() calls per run; read when a run starts. Nil: the default.
	MaxAgents func() int
	// Limits overrides DefaultLimits (zero fields: the default).
	Limits Limits
	Logger *slog.Logger
}

// SearchDirs lists where saved workflows are, later entries winning: the user's directories, then (for a trusted
// workspace) the project's.
func SearchDirs(harnessHome, home, cwd string, trusted bool) []string {
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".claude", "workflows"))
	}
	if harnessHome != "" {
		dirs = append(dirs, filepath.Join(harnessHome, "workflows"))
	}
	if trusted {
		dirs = append(dirs, filepath.Join(cwd, ".claude", "workflows"), filepath.Join(cwd, ".harness", "workflows"))
	}
	return dirs
}

// cached is a finished agent's work, kept for a resume.
type cached struct {
	Text   string          `json:"text,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
}

// record is what stays on disk of a run.
type record struct {
	Info   tools.WorkflowInfo `json:"info"`
	Script string             `json:"script"`
	Source string             `json:"source,omitempty"`
	Args   json.RawMessage    `json:"args,omitempty"`
	// Cache holds finished agent() calls by callSlot.
	Cache map[string]cached `json:"cache,omitempty"`
	// Unreported: it was cut off when the session closed, and the main agent has not been told.
	Unreported bool `json:"unreported,omitempty"`
}

// plan is a call's script, as Plan read it.
type plan struct {
	spec    tools.WorkflowSpec
	script  *script
	source  string
	args    json.RawMessage
	problem error
}

// run is one run of a script, or its latest resume.
type run struct {
	rec    record
	script *script
	cancel context.CancelFunc
	// recorded is the state last written to the session's transcript.
	recorded tools.WorkflowState
	// generation counts executions: what an agent of an earlier one reports must not touch this one.
	generation int
	// done closes when an execution has settled.
	done     chan struct{}
	stopping bool
	// calls counts the identical calls of this execution; total all of its calls; maxAgents is its cap.
	calls     map[string]int
	total     int
	maxAgents int
	// agents counts the agent() calls of this execution still out; handles is its agents, true while they work, and
	// totalsOf how they ended.
	agents   *sync.WaitGroup
	handles  map[string]bool
	totalsOf map[string]tools.AgentInfo
}

// Manager is the session's set of runs. It implements tools.Workflows.
type Manager struct {
	cfg  Config
	lead *agent.Agent

	mu      sync.Mutex
	runs    map[string]*run
	order   []*run
	plans   map[string]*plan
	closing bool
	pubMu   sync.Mutex
	limits  Limits
}

var _ tools.Workflows = (*Manager)(nil)

// New creates a manager. Attach it to the session's main agent before use.
func New(cfg Config) *Manager {
	lim := DefaultLimits
	if cfg.Limits.Steps > 0 {
		lim.Steps = cfg.Limits.Steps
	}
	if cfg.Limits.Compute > 0 {
		lim.Compute = cfg.Limits.Compute
	}
	if cfg.Limits.WallClock > 0 {
		lim.WallClock = cfg.Limits.WallClock
	}
	return &Manager{cfg: cfg, runs: map[string]*run{}, plans: map[string]*plan{}, limits: lim}
}

// Attach sets the session's main agent and loads the runs of an earlier run of the client: those that were running read
// as interrupted, and the main agent is told so with its next request. They do not go on by themselves: that would
// spend without anyone asking.
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

func (m *Manager) recordPath(id string) string {
	return filepath.Join(m.cfg.Dir, "run-"+id+".json")
}

func (m *Manager) resultPath(id string) string {
	return filepath.Join(m.cfg.Dir, "run-"+id+".result.txt")
}

func (m *Manager) persistent() bool { return !m.cfg.NoPersistence && m.cfg.Dir != "" }

// save writes the run's record (m.mu held), and into the session's transcript when its state changed.
func (m *Manager) save(r *run) {
	if w := m.cfg.LeadTranscript; w != nil && r.recorded != r.rec.Info.State {
		r.recorded = r.rec.Info.State
		if data, err := json.Marshal(snapshot(r)); err == nil {
			_, _ = w.Append(session.Entry{Type: session.TypeMeta, Workflow: data})
		}
	}
	if !m.persistent() {
		return
	}
	if err := writeFile(m.recordPath(r.rec.Info.ID), r.rec); err != nil {
		m.log().Warn("workflow record", "err", err)
	}
}

func writeFile(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Manager) load() {
	if !m.persistent() {
		return
	}
	paths, _ := filepath.Glob(filepath.Join(m.cfg.Dir, "run-*.json"))
	var lost []*run
	m.mu.Lock()
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var rec record
		if json.Unmarshal(data, &rec) != nil || rec.Info.ID == "" {
			continue
		}
		r := &run{rec: rec, recorded: rec.Info.State, done: closedChan()}
		if rec.Info.State == tools.WorkflowRunning || rec.Unreported {
			r.rec.Info.State = tools.WorkflowInterrupted
			if r.rec.Info.FinishedAt == "" {
				r.rec.Info.FinishedAt = rfc3339(time.Now())
			}
			for i := range r.rec.Info.Agents {
				if r.rec.Info.Agents[i].State.Active() {
					r.rec.Info.Agents[i].State = tools.AgentInterrupted
				}
			}
			r.rec.Unreported = false
			m.save(r)
			lost = append(lost, r)
		}
		m.runs[rec.Info.ID] = r
		m.order = append(m.order, r)
	}
	sort.SliceStable(m.order, func(i, j int) bool { return m.order[i].rec.Info.StartedAt < m.order[j].rec.Info.StartedAt })
	m.mu.Unlock()
	for _, r := range lost {
		m.lead.Remind(agent.ReminderWorkflowReport, m.reportVars(r))
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
	return "w" + hex.EncodeToString(b)
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func snapshot(r *run) tools.WorkflowInfo {
	info := r.rec.Info
	info.Phases = append([]string(nil), info.Phases...)
	info.Agents = append([]tools.WorkflowAgent(nil), info.Agents...)
	info.Log = append([]string(nil), info.Log...)
	return info
}

var savedName = regexp.MustCompile(`^[\w.-]+$`)

// read finds the script of a call. Reading is all it does: Plan runs before the permission check.
func (m *Manager) read(spec tools.WorkflowSpec) (*plan, error) {
	if len(spec.Args) > 0 && !json.Valid(spec.Args) {
		return nil, errors.New("InputValidationError: args is not JSON")
	}
	sources := 0
	for _, s := range []string{spec.Script, spec.ScriptPath, spec.Name} {
		if s != "" {
			sources++
		}
	}
	if sources > 1 {
		return nil, errors.New("InputValidationError: give one of script, script_path and name")
	}
	var prior *run
	if spec.ResumeFrom != "" {
		m.mu.Lock()
		prior = m.runs[spec.ResumeFrom]
		closing := m.closing
		active := prior != nil && prior.rec.Info.State == tools.WorkflowRunning
		m.mu.Unlock()
		if closing {
			return nil, errors.New("the session is closing")
		}
		if prior == nil {
			return nil, fmt.Errorf("no workflow run %q in this session", spec.ResumeFrom)
		}
		if active {
			return nil, fmt.Errorf("workflow run %s is still running; TaskStop stops it", spec.ResumeFrom)
		}
	} else if sources == 0 {
		return nil, errors.New("InputValidationError: give script, script_path or name")
	}
	p := &plan{spec: spec, args: spec.Args}
	var text string
	switch {
	case spec.Script != "":
		text = spec.Script
	case spec.ScriptPath != "":
		path := spec.ScriptPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.cfg.Env.CurrentDir(), path)
		}
		if deny := m.cfg.Env.ReadDenied; deny != nil && deny(path) {
			return nil, fmt.Errorf("a permission rule denies reading %s", path)
		}
		var err error
		if text, err = readScript(path); err != nil {
			return nil, err
		}
		p.source = path
	case spec.Name != "":
		var err error
		if text, p.source, err = m.findSaved(spec.Name); err != nil {
			return nil, err
		}
	default:
		text, p.source = prior.rec.Script, prior.rec.Source
	}
	if len(p.args) == 0 && prior != nil {
		p.args = prior.rec.Args
	}
	s, err := parseScript(text)
	if err != nil {
		return nil, err
	}
	p.script = s
	return p, nil
}

func readScript(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the script: %w", err)
	}
	if !st.Mode().IsRegular() || st.Size() > maxScriptBytes {
		return "", fmt.Errorf("%s is not a script: a regular file of at most %d bytes", path, maxScriptBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the script: %w", err)
	}
	return string(data), nil
}

// findSaved looks for a saved workflow by file name, then by the name in its meta.
func (m *Manager) findSaved(name string) (text, path string, err error) {
	if !savedName.MatchString(name) {
		return "", "", fmt.Errorf("%q is not the name of a saved workflow", name)
	}
	var dirs []string
	if m.cfg.SearchDirs != nil {
		dirs = m.cfg.SearchDirs()
	}
	var known []string
	for i := len(dirs) - 1; i >= 0; i-- {
		p := filepath.Join(dirs[i], name+".js")
		if text, err := readScript(p); err == nil {
			return text, p, nil
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		files, _ := filepath.Glob(filepath.Join(dirs[i], "*.js"))
		for _, f := range files {
			text, err := readScript(f)
			if err != nil {
				continue
			}
			s, err := parseScript(text)
			if err != nil {
				continue
			}
			if s.meta.Name == name {
				return text, f, nil
			}
			known = append(known, strings.TrimSuffix(filepath.Base(f), ".js"))
		}
	}
	sort.Strings(known)
	if len(known) == 0 {
		return "", "", fmt.Errorf("no saved workflow %q, and none are saved", name)
	}
	return "", "", fmt.Errorf("no saved workflow %q; saved: %s", name, strings.Join(known, ", "))
}

func (m *Manager) maxAgents() int {
	if m.cfg.MaxAgents != nil {
		return m.cfg.MaxAgents()
	}
	return settings.DefaultWorkflowMaxAgents
}

// Plan implements tools.Workflows.
func (m *Manager) Plan(spec tools.WorkflowSpec) (tools.WorkflowPlan, error) {
	p, err := m.read(spec)
	if err != nil {
		p = &plan{spec: spec, problem: err}
	}
	m.mu.Lock()
	if len(m.plans) >= maxPlans {
		m.plans = map[string]*plan{}
	}
	m.plans[spec.ToolUseID] = p
	m.mu.Unlock()
	if err != nil {
		return tools.WorkflowPlan{}, err
	}
	return tools.WorkflowPlan{Name: p.script.meta.Name, Description: p.script.meta.Description, Phases: p.script.meta.Phases, Source: p.source, ResumeFrom: spec.ResumeFrom, MaxAgents: m.maxAgents()}, nil
}

func sameSpec(a, b tools.WorkflowSpec) bool {
	return a.Script == b.Script && a.ScriptPath == b.ScriptPath && a.Name == b.Name && a.ResumeFrom == b.ResumeFrom && a.ToolUseID == b.ToolUseID && string(a.Args) == string(b.Args)
}

// Start implements tools.Workflows.
func (m *Manager) Start(spec tools.WorkflowSpec) (tools.WorkflowInfo, error) {
	limit := m.maxAgents()
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.plans[spec.ToolUseID]
	delete(m.plans, spec.ToolUseID)
	if p != nil && p.problem != nil {
		return tools.WorkflowInfo{}, p.problem
	}
	if p == nil || !sameSpec(p.spec, spec) {
		return tools.WorkflowInfo{}, errors.New("this call did not go through the permission check; call Workflow again")
	}
	if m.closing {
		return tools.WorkflowInfo{}, errors.New("the session is closing")
	}
	var r *run
	if spec.ResumeFrom != "" {
		r = m.runs[spec.ResumeFrom]
		if r == nil {
			return tools.WorkflowInfo{}, fmt.Errorf("no workflow run %q in this session", spec.ResumeFrom)
		}
		if r.rec.Info.State == tools.WorkflowRunning {
			return tools.WorkflowInfo{}, fmt.Errorf("workflow run %s is still running; TaskStop stops it", spec.ResumeFrom)
		}
	} else {
		r = &run{rec: record{Info: tools.WorkflowInfo{ID: newID()}, Cache: map[string]cached{}}}
		m.runs[r.rec.Info.ID] = r
		m.order = append(m.order, r)
	}
	if r.rec.Cache == nil {
		r.rec.Cache = map[string]cached{}
	}
	meta := p.script.meta
	r.rec.Script, r.rec.Source, r.rec.Args, r.rec.Unreported = p.script.text, p.source, p.args, false
	r.rec.Info = tools.WorkflowInfo{ID: r.rec.Info.ID, ToolUseID: spec.ToolUseID, Name: meta.Name, Description: meta.Description,
		Phases: append([]string(nil), meta.Phases...), State: tools.WorkflowRunning, StartedAt: rfc3339(time.Now())}
	r.script = p.script
	r.generation++
	r.done = make(chan struct{})
	r.stopping = false
	r.calls, r.total, r.maxAgents = map[string]int{}, 0, limit
	r.handles, r.totalsOf = map[string]bool{}, map[string]tools.AgentInfo{}
	r.agents = &sync.WaitGroup{}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	m.save(r)
	info := snapshot(r)
	go m.execute(ctx, r, r.generation)
	go m.publish(r)
	return info, nil
}

// execute runs one execution of a run to its end. A script stuck in a builtin the interpreter cannot interrupt must not
// hold the run: it then ends without it, and the goroutine goes when the builtin returns.
func (m *Manager) execute(ctx context.Context, r *run, gen int) {
	m.mu.Lock()
	s, args := r.script, r.rec.Args
	m.mu.Unlock()
	type end struct {
		out string
		err error
	}
	ended := make(chan end, 1)
	limit := make(chan error, 1)
	host := &runHost{m: m, r: r, gen: gen, ctx: ctx, agents: r.agents, limit: limit}
	go func() {
		out, err := execute(ctx, s, args, host, m.limits)
		ended <- end{out, err}
	}()
	var e end
	wait := func(fallback error) {
		select {
		case e = <-ended:
		case <-time.After(stopWait):
			e = end{err: fallback}
		}
	}
	select {
	case e = <-ended:
	case <-ctx.Done():
		wait(errors.New("the script did not stop in time"))
	case err := <-limit:
		wait(err)
	}
	m.finish(r, e.out, e.err)
}

// finish settles a run: what is still running of it stops, and the main agent hears how it ended.
func (m *Manager) finish(r *run, out string, runErr error) {
	// First: an agent still starting sees this and stops by itself, any other is in handles below.
	r.cancel()
	m.mu.Lock()
	closing, stopping := m.closing, r.stopping
	ids := activeAgents(r)
	agents := r.agents
	m.mu.Unlock()
	strays := 0
	if !closing {
		// Agents a script did not wait for end with it: nothing could use what they find.
		strays = m.stopAgents(ids)
		wait := make(chan struct{})
		go func() {
			agents.Wait()
			close(wait)
		}()
		select {
		case <-wait:
		case <-time.After(stopWait):
		}
	}

	m.mu.Lock()
	info := &r.rec.Info
	switch {
	case closing:
		info.State = tools.WorkflowInterrupted
		r.rec.Unreported = true
	case stopping:
		info.State = tools.WorkflowStopped
	case runErr != nil:
		info.State = tools.WorkflowFailed
		info.Error = runErr.Error()
	default:
		info.State = tools.WorkflowFinished
		m.keepResult(r, out)
	}
	if strays > 0 && runErr == nil && !stopping {
		appendLog(info, fmt.Sprintf("Stopped %d agents the script did not wait for.", strays))
	}
	info.FinishedAt = rfc3339(time.Now())
	m.tally(r)
	state := info.State
	m.save(r)
	done := r.done
	m.mu.Unlock()
	m.publish(r)
	if !closing {
		m.lead.Remind(agent.ReminderWorkflowReport, m.reportVars(r))
		if state != tools.WorkflowStopped {
			m.lead.Wake()
		}
	}
	close(done)
}

// activeAgents lists the agents of the run's execution at work (m.mu held).
func activeAgents(r *run) []string {
	ids := make([]string, 0, len(r.handles))
	for id, active := range r.handles {
		if active {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// keepResult puts a result on the record, the end of a long one in a file (m.mu held).
func (m *Manager) keepResult(r *run, out string) {
	runes := []rune(out)
	if len(runes) <= maxResultChars {
		r.rec.Info.Result = out
		return
	}
	r.rec.Info.Result = string(runes[:maxResultChars])
	if !m.persistent() {
		r.rec.Info.Result += "\n[The result continues; it was not kept.]"
		return
	}
	path := m.resultPath(r.rec.Info.ID)
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		m.log().Warn("workflow result", "err", err)
		return
	}
	r.rec.Info.Result += "\n[The result continues in " + path + ".]"
}

// tally adds up what the run's agents used (m.mu held).
func (m *Manager) tally(r *run) {
	var in, out int64
	var cost float64
	for id := range r.handles {
		a, ok := r.totalsOf[id]
		if live, found := m.cfg.Agents.Get(id); found {
			a, ok = live, true
		}
		if ok {
			in += a.InputTokens
			out += a.OutputTokens
			cost += a.CostUSD
		}
	}
	r.rec.Info.InputTokens, r.rec.Info.OutputTokens, r.rec.Info.CostUSD = in, out, cost
}

func (m *Manager) stopAgents(ids []string) int {
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.cfg.Agents.Stop("", id); err != nil && !errors.Is(err, tools.ErrAgentNotRunning) {
				m.log().Warn("stop a workflow's agent", "agent", id, "err", err)
			}
		}()
	}
	wg.Wait()
	return len(ids)
}

// reportVars fills the workflow_report reminder.
func (m *Manager) reportVars(r *run) map[string]string {
	m.mu.Lock()
	info := snapshot(r)
	m.mu.Unlock()
	resume := fmt.Sprintf(" Workflow with resume_from %s continues it: finished agents are not run again.", info.ID)
	var status string
	switch info.State {
	case tools.WorkflowFinished:
		status = "finished."
		resume = ""
	case tools.WorkflowFailed:
		status = "failed: " + subagent.Defuse(info.Error) + "."
	case tools.WorkflowStopped:
		status = "was stopped."
	default:
		status = "was interrupted when the client stopped."
	}
	result := info.Result
	if strings.TrimSpace(result) == "" {
		result = "(nothing returned)"
	}
	var details strings.Builder
	finished, failed, fromCache := 0, 0, 0
	for _, a := range info.Agents {
		switch {
		case a.Cached:
			fromCache++
		case a.State == tools.AgentFinished:
			finished++
		default:
			failed++
		}
	}
	fmt.Fprintf(&details, "Its agents: %d finished, %d did not, %d answered from the cache.", finished, failed, fromCache)
	for _, a := range info.Agents {
		if a.AgentID == "" {
			continue
		}
		fmt.Fprintf(&details, "\n- %s (%s): %s", a.AgentID, subagent.Defuse(a.Label), a.State)
		if live, ok := m.cfg.Agents.Get(a.AgentID); ok && live.WorktreePath != "" {
			fmt.Fprintf(&details, ", in the git worktree %s on branch %s", live.WorktreePath, live.WorktreeBranch)
		}
	}
	return map[string]string{"run_id": info.ID, "name": subagent.Defuse(info.Name), "status": status + resume, "result": subagent.Defuse(result), "details": details.String()}
}

// publish sends the run as it is now to the session's frontends. Snapshots go out in the order they were taken.
func (m *Manager) publish(r *run) {
	m.pubMu.Lock()
	defer m.pubMu.Unlock()
	m.mu.Lock()
	info := snapshot(r)
	m.mu.Unlock()
	if m.lead != nil {
		m.lead.Emit(agent.Event{Kind: agent.EventWorkflow, Workflow: &info})
	}
}

// Stop implements tools.Workflows: TaskStop on a run, and the frontends' stop. Its agents stop with it.
func (m *Manager) Stop(id string) (tools.WorkflowInfo, error) {
	m.mu.Lock()
	r := m.runs[id]
	if r == nil {
		m.mu.Unlock()
		return tools.WorkflowInfo{}, fmt.Errorf("no workflow run %s", id)
	}
	if r.rec.Info.State != tools.WorkflowRunning {
		info := snapshot(r)
		m.mu.Unlock()
		return info, tools.ErrWorkflowNotRunning
	}
	r.stopping = true
	cancel, done := r.cancel, r.done
	ids := activeAgents(r)
	m.mu.Unlock()
	cancel()
	m.stopAgents(ids)
	select {
	case <-done:
	case <-time.After(2 * stopWait):
		m.log().Warn("workflow did not stop in time", "run", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return snapshot(r), nil
}

// Get implements tools.Workflows.
func (m *Manager) Get(id string) (tools.WorkflowInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil {
		return tools.WorkflowInfo{}, false
	}
	return snapshot(r), true
}

// List returns the session's runs, oldest first.
func (m *Manager) List() []tools.WorkflowInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tools.WorkflowInfo, 0, len(m.order))
	for _, r := range m.order {
		out = append(out, snapshot(r))
	}
	return out
}

// Active counts the runs still at work, their reports not yet delivered.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.order {
		select {
		case <-r.done:
		default:
			n++
		}
	}
	return n
}

// WaitIdle blocks until no run is at work (headless runs, which end with their process).
func (m *Manager) WaitIdle(ctx context.Context) error {
	for {
		m.mu.Lock()
		var wait <-chan struct{}
		for _, r := range m.order {
			select {
			case <-r.done:
			default:
				wait = r.done
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

// Shutdown ends every run (the session is closing): they read as interrupted, and their agents are left to the agents'
// own shutdown, which comes next and marks them interrupted too. Safe to call more than once.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return
	}
	m.closing = true
	var waits []chan struct{}
	for _, r := range m.order {
		if r.rec.Info.State == tools.WorkflowRunning {
			r.cancel()
			waits = append(waits, r.done)
		}
	}
	m.mu.Unlock()
	deadline := time.After(stopWait)
	for _, d := range waits {
		select {
		case <-d:
		case <-deadline:
			return
		}
	}
}

func appendLog(info *tools.WorkflowInfo, line string) {
	if r := []rune(line); len(r) > maxLogChars {
		line = string(r[:maxLogChars]) + "…"
	}
	info.Log = append(info.Log, line)
	if n := len(info.Log); n > maxLogLines {
		info.Log = append([]string(nil), info.Log[n-maxLogLines:]...)
	}
}

// runHost is the script's way out, for one execution.
type runHost struct {
	m      *Manager
	r      *run
	gen    int
	ctx    context.Context
	agents *sync.WaitGroup
	limit  chan<- error
}

// limitHit implements scriptHost.
func (h *runHost) limitHit(err error) {
	select {
	case h.limit <- err:
	default:
	}
}

func (h *runHost) phase(title string) {
	m, r := h.m, h.r
	m.mu.Lock()
	if r.generation != h.gen {
		m.mu.Unlock()
		return
	}
	notePhase(&r.rec.Info, title)
	r.rec.Info.CurrentPhase = title
	m.save(r)
	m.mu.Unlock()
	m.publish(r)
}

func notePhase(info *tools.WorkflowInfo, title string) {
	for _, p := range info.Phases {
		if p == title {
			return
		}
	}
	info.Phases = append(info.Phases, title)
}

func (h *runHost) log(line string) {
	m, r := h.m, h.r
	m.mu.Lock()
	if r.generation != h.gen {
		m.mu.Unlock()
		return
	}
	appendLog(&r.rec.Info, line)
	m.mu.Unlock()
	m.publish(r)
}

// callSlot names an agent() call in the run's cache: what it asks for, and which of the identical calls of the run it
// is, since asking twice is asking two agents. A label or a phase only names the call.
func callSlot(call agentCall, seen map[string]int) string {
	var schema any
	if len(call.Schema) > 0 {
		_ = json.Unmarshal(call.Schema, &schema) // maps marshal with sorted keys: the same schema, however written
	}
	data, _ := json.Marshal(map[string]any{"prompt": call.Prompt, "schema": schema, "model": call.Model, "type": call.Type, "isolation": call.Isolation})
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	n := seen[key]
	seen[key] = n + 1
	return fmt.Sprintf("%s#%d", key, n)
}

// agent implements scriptHost.
func (h *runHost) agent(call agentCall, done func(outcome)) {
	m, r := h.m, h.r
	label := call.Label
	if label == "" {
		label = firstLine(call.Prompt)
	}
	m.mu.Lock()
	if r.generation != h.gen {
		m.mu.Unlock()
		done(outcome{Err: "the run has ended", Code: "stopped"})
		return
	}
	r.total++
	if r.total > r.maxAgents {
		maxAgents := r.maxAgents
		m.mu.Unlock()
		done(outcome{Err: fmt.Sprintf("this run reached its cap of %d agent() calls (the workflowMaxAgents setting)", maxAgents), Code: "cap"})
		return
	}
	info := &r.rec.Info
	phase := call.Phase
	if phase == "" {
		phase = info.CurrentPhase
	} else {
		notePhase(info, phase)
	}
	slot := callSlot(call, r.calls)
	idx := len(info.Agents)
	info.Agents = append(info.Agents, tools.WorkflowAgent{Label: label, Phase: phase, State: tools.AgentQueued})
	hit, found := r.rec.Cache[slot]
	if found && !validCached(hit, call.Schema) {
		// What is on disk does not pass its own schema: it is not finished work.
		delete(r.rec.Cache, slot)
		found = false
	}
	if found {
		a := &info.Agents[idx]
		a.State, a.Cached = tools.AgentFinished, true
		m.save(r)
		m.mu.Unlock()
		m.publish(r)
		done(outcome{Text: hit.Text, Output: hit.Output})
		return
	}
	h.agents.Add(1)
	m.save(r)
	m.mu.Unlock()
	m.publish(r)
	go h.runAgent(call, label, slot, idx, done)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

// validCached re-validates a cached result against its schema. An agent's output is only validated in memory by the
// run that produced it, and a record can be older than this client or edited.
func validCached(c cached, schema json.RawMessage) bool {
	if len(schema) == 0 {
		return len(c.Output) == 0
	}
	return len(c.Output) > 0 && subagent.ValidateOutput(schema, c.Output) == nil
}

// runAgent runs one agent() call to its end, on its own goroutine.
func (h *runHost) runAgent(call agentCall, label, slot string, idx int, done func(outcome)) {
	m, r := h.m, h.r
	defer h.agents.Done()
	update := func(f func(a *tools.WorkflowAgent)) {
		m.mu.Lock()
		if r.generation == h.gen && idx < len(r.rec.Info.Agents) {
			f(&r.rec.Info.Agents[idx])
			m.save(r)
		}
		m.mu.Unlock()
		m.publish(r)
	}
	handle, err := m.cfg.Agents.SpawnSubagent(h.ctx, subagent.Options{
		AgentSpec:    tools.AgentSpec{Description: label, Prompt: call.Prompt, Type: call.Type, Model: call.Model, Isolation: call.Isolation},
		OutputSchema: call.Schema,
		Silent:       true,
	})
	if err != nil {
		update(func(a *tools.WorkflowAgent) { a.State, a.Error = tools.AgentFailed, err.Error() })
		done(outcome{Err: err.Error(), Code: "failed"})
		return
	}
	first := handle.Info()
	m.mu.Lock()
	if r.generation == h.gen {
		r.handles[handle.ID] = true
	}
	m.mu.Unlock()
	update(func(a *tools.WorkflowAgent) { a.AgentID, a.State = handle.ID, first.State })
	if h.ctx.Err() != nil {
		// The run ended while this agent was starting.
		_, _ = m.cfg.Agents.Stop("", handle.ID)
	}
	res, _ := handle.Wait(context.Background())
	m.mu.Lock()
	if r.generation == h.gen {
		r.handles[handle.ID] = false
		r.totalsOf[handle.ID] = res.Info
	}
	m.mu.Unlock()

	out := outcome{}
	switch {
	case res.OutputError != nil:
		out = outcome{Err: fmt.Sprintf("agent %s did not give the output asked for: %s", handle.ID, res.OutputError.Error()), Code: res.OutputError.Code}
	case res.Info.State == tools.AgentFinished && len(call.Schema) > 0 && len(res.Output) == 0:
		// Nothing checked this agent's last message (it was continued after a restart): it is not the output asked for.
		out = outcome{Err: fmt.Sprintf("agent %s finished without a validated output", handle.ID), Code: subagent.OutputInvalid}
	case res.Info.State == tools.AgentFinished && len(call.Schema) > 0 && len(res.Output) == 0:
		// Nothing checked this agent's last message (it was continued after a restart): it is not the output asked for.
		out = outcome{Err: fmt.Sprintf("agent %s finished without a validated output", handle.ID), Code: subagent.OutputInvalid}
	case res.Info.State == tools.AgentFinished:
		out = outcome{Text: res.Info.Report, Output: res.Output}
		if len(call.Schema) > 0 {
			out.Text = ""
		}
		m.mu.Lock()
		r.rec.Cache[slot] = cached{Text: out.Text, Output: out.Output}
		m.mu.Unlock()
	case res.Info.State == tools.AgentFailed:
		out = outcome{Err: fmt.Sprintf("agent %s failed: %s", handle.ID, res.Info.Error), Code: "failed"}
	default:
		out = outcome{Err: fmt.Sprintf("agent %s %s", handle.ID, res.Info.State), Code: string(res.Info.State)}
	}
	update(func(a *tools.WorkflowAgent) { a.State, a.Error = res.Info.State, out.Err })
	done(out)
}
