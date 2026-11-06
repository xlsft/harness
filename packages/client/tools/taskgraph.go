package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// The task graph: work items with dependencies, shared by a session's main agent and all its subagents. TodoWrite is
// one agent's checklist; this is what several agents divide among themselves. A claim is atomic, so two agents can't take
// the same task. It stays on this machine, next to the transcript.

// Graph task statuses.
const (
	GraphPending    = "pending"
	GraphInProgress = "in_progress"
	GraphDone       = "done"
	GraphFailed     = "failed"
	GraphCancelled  = "cancelled"
)

// GraphLead is the owner name of the session's main agent.
const GraphLead = "lead"

// Limits of the graph.
const (
	maxGraphTasks = 500
	maxGraphTitle = 200
)

// GraphTask is one node (harness.agent.v1.TaskNode).
type GraphTask struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"`
	Owner       string   `json:"owner,omitempty"`
	OwnerName   string   `json:"owner_name,omitempty"`
	BlockedBy   []string `json:"blocked_by,omitempty"`
	WaitingFor  []string `json:"waiting_for,omitempty"`
	Team        string   `json:"team,omitempty"`
	Note        string   `json:"note,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// TaskGraph is the session's graph. Methods are safe for concurrent use.
type TaskGraph struct {
	path string

	mu       sync.Mutex
	tasks    []*GraphTask
	next     int
	onChange func([]GraphTask)
	// claims counts what each agent took, to tell an agent that picks up work from one that doesn't.
	claims map[string]int
}

type graphFile struct {
	Next  int          `json:"next"`
	Tasks []*GraphTask `json:"tasks"`
}

// NewTaskGraph loads the graph from path; empty path keeps it in memory. A claim outlives no client: what agents held
// when the last one stopped is free again.
func NewTaskGraph(path string) *TaskGraph {
	g := &TaskGraph{path: path, claims: map[string]int{}}
	if path == "" {
		return g
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return g
	}
	var f graphFile
	if json.Unmarshal(data, &f) == nil {
		g.next, g.tasks = f.Next, f.Tasks
		for _, t := range g.tasks {
			if t.Status == GraphInProgress {
				t.Status, t.Owner, t.OwnerName, t.Note = GraphPending, "", "", "Whoever worked on it stopped with the client."
			}
		}
	}
	return g
}

// SetOnChange sets who hears about changes; it runs outside the graph's lock.
func (g *TaskGraph) SetOnChange(f func([]GraphTask)) {
	g.mu.Lock()
	g.onChange = f
	g.mu.Unlock()
}

// changedLocked saves the graph and returns what to tell (call the result after unlocking).
func (g *TaskGraph) changedLocked() func() {
	if g.path != "" {
		if b, err := json.MarshalIndent(graphFile{Next: g.next, Tasks: g.tasks}, "", " "); err == nil {
			if os.MkdirAll(filepath.Dir(g.path), 0o700) == nil {
				tmp := g.path + ".tmp"
				if os.WriteFile(tmp, b, 0o600) == nil {
					_ = os.Rename(tmp, g.path)
				}
			}
		}
	}
	f, snap := g.onChange, g.snapshotLocked()
	return func() {
		if f != nil {
			f(snap)
		}
	}
}

func (g *TaskGraph) find(id string) *GraphTask {
	for _, t := range g.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// waitingFor lists the tasks that keep t from being ready.
func (g *TaskGraph) waitingFor(t *GraphTask) []string {
	var out []string
	for _, id := range t.BlockedBy {
		if d := g.find(id); d == nil || d.Status != GraphDone {
			out = append(out, id)
		}
	}
	return out
}

func (g *TaskGraph) snapshotLocked() []GraphTask {
	out := make([]GraphTask, 0, len(g.tasks))
	for _, t := range g.tasks {
		c := *t
		c.BlockedBy = slices.Clone(t.BlockedBy)
		c.WaitingFor = g.waitingFor(t)
		out = append(out, c)
	}
	return out
}

// List returns the tasks in order, with what each waits for.
func (g *TaskGraph) List() []GraphTask {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshotLocked()
}

// mayTake: the main agent takes anything; an agent takes its team's tasks and those of nobody's team.
func mayTake(t *GraphTask, agentID, team string) bool {
	return agentID == "" || t.Team == "" || t.Team == team
}

func (g *TaskGraph) ready(t *GraphTask) bool {
	return t.Status == GraphPending && len(g.waitingFor(t)) == 0
}

// Ready returns the free tasks this agent could take now.
func (g *TaskGraph) Ready(agentID, team string) []GraphTask {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []GraphTask
	for _, t := range g.tasks {
		if g.ready(t) && mayTake(t, agentID, team) {
			out = append(out, *t)
		}
	}
	return out
}

// Claims counts the tasks an agent has claimed so far.
func (g *TaskGraph) Claims(agentID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.claims[agentID]
}

// Held returns the tasks an agent is working on.
func (g *TaskGraph) Held(agentID string) []GraphTask {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []GraphTask
	for _, t := range g.tasks {
		if t.Status == GraphInProgress && t.Owner == agentID {
			out = append(out, *t)
		}
	}
	return out
}

// GraphTaskSpec is a task to add.
type GraphTaskSpec struct {
	// Key names it within one call, for blocked_by of the others.
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	BlockedBy   []string `json:"blocked_by"`
	Team        string   `json:"team"`
}

// Add appends tasks; blocked_by names existing ids or the keys of this call. All or nothing.
func (g *TaskGraph) Add(specs []GraphTaskSpec) ([]GraphTask, error) {
	if len(specs) == 0 {
		return nil, errors.New("at least one task is required")
	}
	g.mu.Lock()
	if len(g.tasks)+len(specs) > maxGraphTasks {
		g.mu.Unlock()
		return nil, fmt.Errorf("a graph holds at most %d tasks", maxGraphTasks)
	}
	keys := map[string]string{}
	added := make([]*GraphTask, 0, len(specs))
	now := time.Now().UTC().Format(time.RFC3339)
	next := g.next
	for _, sp := range specs {
		title := strings.TrimSpace(sp.Title)
		if title == "" || len([]rune(title)) > maxGraphTitle {
			g.mu.Unlock()
			return nil, fmt.Errorf("a task needs a title of at most %d characters", maxGraphTitle)
		}
		next++
		id := fmt.Sprintf("t%d", next)
		if sp.Key != "" {
			if _, dup := keys[sp.Key]; dup {
				g.mu.Unlock()
				return nil, fmt.Errorf("the key %q is used twice", sp.Key)
			}
			keys[sp.Key] = id
		}
		added = append(added, &GraphTask{ID: id, Title: title, Description: sp.Description, Status: GraphPending, Team: strings.TrimSpace(sp.Team), UpdatedAt: now})
	}
	for i, sp := range specs {
		for _, b := range sp.BlockedBy {
			id := b
			if k, ok := keys[b]; ok {
				id = k
			} else if g.find(b) == nil && !slices.ContainsFunc(added, func(t *GraphTask) bool { return t.ID == b }) {
				g.mu.Unlock()
				return nil, fmt.Errorf("blocked_by names no task: %q", b)
			}
			if !slices.Contains(added[i].BlockedBy, id) {
				added[i].BlockedBy = append(added[i].BlockedBy, id)
			}
		}
	}
	all := append(slices.Clone(g.tasks), added...)
	if cycle := findCycle(all); cycle != "" {
		g.mu.Unlock()
		return nil, fmt.Errorf("these tasks would wait for each other: %s", cycle)
	}
	g.tasks, g.next = all, next
	out := make([]GraphTask, 0, len(added))
	for _, t := range added {
		c := *t
		c.WaitingFor = g.waitingFor(t)
		out = append(out, c)
	}
	tell := g.changedLocked()
	g.mu.Unlock()
	tell()
	return out, nil
}

// findCycle returns a dependency cycle of tasks, as "a -> b -> a"; empty when there is none.
func findCycle(tasks []*GraphTask) string {
	byID := make(map[string]*GraphTask, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	state := map[string]int{}
	var stack []string
	var visit func(string) string
	visit = func(id string) string {
		state[id] = 1
		stack = append(stack, id)
		if t := byID[id]; t != nil {
			for _, d := range t.BlockedBy {
				switch state[d] {
				case 0:
					if c := visit(d); c != "" {
						return c
					}
				case 1:
					i := slices.Index(stack, d)
					return strings.Join(append(slices.Clone(stack[i:]), d), " -> ")
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
		return ""
	}
	for _, t := range tasks {
		if state[t.ID] == 0 {
			if c := visit(t.ID); c != "" {
				return c
			}
		}
	}
	return ""
}

// Claim gives the agent a task: the named one, or the first it can take. Exactly one of several callers gets a task.
func (g *TaskGraph) Claim(agentID, agentName, team, id string) (GraphTask, error) {
	owner := agentID
	if owner == "" {
		owner, agentName = GraphLead, GraphLead
	}
	g.mu.Lock()
	var t *GraphTask
	if id != "" {
		if t = g.find(id); t == nil {
			g.mu.Unlock()
			return GraphTask{}, fmt.Errorf("no task %s", id)
		}
		if !mayTake(t, agentID, team) {
			g.mu.Unlock()
			return GraphTask{}, fmt.Errorf("task %s belongs to team %s", id, t.Team)
		}
		if t.Status == GraphInProgress {
			g.mu.Unlock()
			return GraphTask{}, fmt.Errorf("task %s is already being worked on by %s", id, t.OwnerName)
		}
		if t.Status != GraphPending {
			g.mu.Unlock()
			return GraphTask{}, fmt.Errorf("task %s is %s", id, t.Status)
		}
		if w := g.waitingFor(t); len(w) > 0 {
			g.mu.Unlock()
			return GraphTask{}, fmt.Errorf("task %s waits for %s", id, strings.Join(w, ", "))
		}
	} else {
		for _, c := range g.tasks {
			if g.ready(c) && mayTake(c, agentID, team) {
				t = c
				break
			}
		}
		if t == nil {
			g.mu.Unlock()
			return GraphTask{}, errors.New("no task is free right now")
		}
	}
	t.Status, t.Owner, t.OwnerName = GraphInProgress, owner, agentName
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	g.claims[agentID]++
	out := *t
	tell := g.changedLocked()
	g.mu.Unlock()
	tell()
	return out, nil
}

// GraphUpdate is a change to a task; nil fields stay as they are.
type GraphUpdate struct {
	ID           string   `json:"id"`
	Status       *string  `json:"status"`
	Note         *string  `json:"note"`
	Title        *string  `json:"title"`
	Description  *string  `json:"description"`
	AddBlockedBy []string `json:"add_blocked_by"`
}

// Update changes a task. Taking it goes through Claim. Only its owner or the main agent can finish a task someone holds.
func (g *TaskGraph) Update(agentID string, u GraphUpdate) (GraphTask, error) {
	g.mu.Lock()
	t := g.find(u.ID)
	if t == nil {
		g.mu.Unlock()
		return GraphTask{}, fmt.Errorf("no task %s", u.ID)
	}
	if t.Owner != "" && agentID != "" && t.Owner != agentID {
		g.mu.Unlock()
		return GraphTask{}, fmt.Errorf("task %s is held by %s", t.ID, t.OwnerName)
	}
	if u.Status != nil {
		switch *u.Status {
		case GraphDone, GraphFailed, GraphCancelled:
			t.Status, t.Owner = *u.Status, ""
		case GraphPending:
			t.Status, t.Owner, t.OwnerName = GraphPending, "", ""
		default:
			g.mu.Unlock()
			return GraphTask{}, fmt.Errorf("status must be %s, %s, %s or %s; TaskGraphClaim takes a task", GraphPending, GraphDone, GraphFailed, GraphCancelled)
		}
	}
	if u.Title != nil && strings.TrimSpace(*u.Title) != "" {
		t.Title = strings.TrimSpace(*u.Title)
	}
	if u.Description != nil {
		t.Description = *u.Description
	}
	if u.Note != nil {
		t.Note = *u.Note
	}
	if len(u.AddBlockedBy) > 0 {
		before := slices.Clone(t.BlockedBy)
		for _, b := range u.AddBlockedBy {
			if g.find(b) == nil {
				t.BlockedBy = before
				g.mu.Unlock()
				return GraphTask{}, fmt.Errorf("blocked_by names no task: %q", b)
			}
			if !slices.Contains(t.BlockedBy, b) {
				t.BlockedBy = append(t.BlockedBy, b)
			}
		}
		if cycle := findCycle(g.tasks); cycle != "" {
			t.BlockedBy = before
			g.mu.Unlock()
			return GraphTask{}, fmt.Errorf("these tasks would wait for each other: %s", cycle)
		}
	}
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	out := *t
	out.WaitingFor = g.waitingFor(t)
	tell := g.changedLocked()
	g.mu.Unlock()
	tell()
	return out, nil
}

// Release frees what an agent holds when its run ends. why goes in the note.
func (g *TaskGraph) Release(agentID, why string) {
	g.mu.Lock()
	n := 0
	for _, t := range g.tasks {
		if t.Status == GraphInProgress && t.Owner == agentID {
			t.Status, t.Owner, t.OwnerName, t.Note = GraphPending, "", "", why
			t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			n++
		}
	}
	if n == 0 {
		g.mu.Unlock()
		return
	}
	tell := g.changedLocked()
	g.mu.Unlock()
	tell()
}

// RegisterGraphTools adds the graph's tools to r.
func RegisterGraphTools(r *Registry) {
	for _, t := range []Tool{&TaskGraphAdd{}, &TaskGraphClaim{}, &TaskGraphUpdate{}, &TaskGraphList{}} {
		r.Register(t)
	}
}

func graphRow(t GraphTask) string {
	line := fmt.Sprintf("%s [%s] %s", t.ID, t.Status, t.Title)
	if t.Status == GraphInProgress && t.OwnerName != "" {
		line += " (" + t.OwnerName + ")"
	}
	if t.Team != "" {
		line += " team=" + t.Team
	}
	if len(t.WaitingFor) > 0 {
		line += " waits for " + strings.Join(t.WaitingFor, ", ")
	}
	if t.Note != "" {
		line += " | " + truncateLine(t.Note, 200)
	}
	return line
}

func truncateLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// graphOf returns the session's graph, and whoever is calling.
func graphOf(env *Env) (*TaskGraph, string, string) {
	g := env.TaskGraph
	if g == nil || env.AgentID == "" || env.Agents == nil {
		return g, "", ""
	}
	if a, ok := env.Agents.Get(env.AgentID); ok {
		return g, a.Name, a.Team
	}
	return g, "", ""
}

const noGraph = "the task graph is not available in this session"

// ---- TaskGraphAdd ----

// TaskGraphAdd adds tasks to the graph.
type TaskGraphAdd struct{}

func (*TaskGraphAdd) Name() string                  { return "TaskGraphAdd" }
func (*TaskGraphAdd) ReadOnly(json.RawMessage) bool { return true }

func (*TaskGraphAdd) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Tasks []GraphTaskSpec `json:"tasks"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	g, _, _ := graphOf(env)
	if g == nil {
		return ErrorResult(noGraph)
	}
	added, err := g.Add(in.Tasks)
	if err != nil {
		return ErrorResult("%v", err)
	}
	rows := make([]string, 0, len(added))
	for _, t := range added {
		rows = append(rows, graphRow(t))
	}
	r := TextResult(strings.Join(rows, "\n"), map[string]any{"tasks": added})
	r.Summary = fmt.Sprintf("Added %d tasks", len(added))
	return r
}

// ---- TaskGraphClaim ----

// TaskGraphClaim takes a task for the calling agent.
type TaskGraphClaim struct{}

func (*TaskGraphClaim) Name() string                  { return "TaskGraphClaim" }
func (*TaskGraphClaim) ReadOnly(json.RawMessage) bool { return true }

func (*TaskGraphClaim) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		ID string `json:"id"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	g, name, team := graphOf(env)
	if g == nil {
		return ErrorResult(noGraph)
	}
	t, err := g.Claim(env.AgentID, name, team, in.ID)
	if err != nil {
		return ErrorResult("%v", err)
	}
	text := fmt.Sprintf("You hold %s: %s", t.ID, t.Title)
	if t.Description != "" {
		text += "\n" + t.Description
	}
	r := TextResult(text, map[string]any{"task": t})
	r.Summary = "Claimed " + t.ID
	return r
}

// ---- TaskGraphUpdate ----

// TaskGraphUpdate changes a task.
type TaskGraphUpdate struct{}

func (*TaskGraphUpdate) Name() string                  { return "TaskGraphUpdate" }
func (*TaskGraphUpdate) ReadOnly(json.RawMessage) bool { return true }

func (*TaskGraphUpdate) Run(ctx context.Context, env *Env, call Call) Result {
	var in GraphUpdate
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.ID == "" {
		return ErrorResult("InputValidationError: id is required")
	}
	g, _, _ := graphOf(env)
	if g == nil {
		return ErrorResult(noGraph)
	}
	t, err := g.Update(env.AgentID, in)
	if err != nil {
		return ErrorResult("%v", err)
	}
	r := TextResult(graphRow(t), map[string]any{"task": t})
	r.Summary = t.ID + " " + t.Status
	return r
}

// ---- TaskGraphList ----

// TaskGraphList shows the graph.
type TaskGraphList struct{}

func (*TaskGraphList) Name() string                  { return "TaskGraphList" }
func (*TaskGraphList) ReadOnly(json.RawMessage) bool { return true }

func (*TaskGraphList) Run(ctx context.Context, env *Env, call Call) Result {
	g, _, _ := graphOf(env)
	if g == nil {
		return ErrorResult(noGraph)
	}
	list := g.List()
	if len(list) == 0 {
		return TextResult("The task graph is empty.", map[string]any{"tasks": list})
	}
	rows := make([]string, 0, len(list))
	for _, t := range list {
		rows = append(rows, graphRow(t))
	}
	r := TextResult(strings.Join(rows, "\n"), map[string]any{"tasks": list})
	r.Summary = fmt.Sprintf("%d tasks", len(list))
	return r
}
