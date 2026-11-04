package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/xlsft/harness/packages/client/permissions"
)

// WorkflowState says how a run of the Workflow tool stands.
type WorkflowState string

// Run states.
const (
	WorkflowRunning     WorkflowState = "running"
	WorkflowFinished    WorkflowState = "finished"
	WorkflowFailed      WorkflowState = "failed"
	WorkflowStopped     WorkflowState = "stopped"
	WorkflowInterrupted WorkflowState = "interrupted"
)

// WorkflowAgent is one agent() call of a run (harness.agent.v1.WorkflowAgent).
type WorkflowAgent struct {
	AgentID string     `json:"agent_id,omitempty"`
	Label   string     `json:"label"`
	Phase   string     `json:"phase,omitempty"`
	State   AgentState `json:"state"`
	Cached  bool       `json:"cached,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// WorkflowInfo describes a run (harness.agent.v1.WorkflowInfo).
type WorkflowInfo struct {
	ID           string          `json:"id"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Phases       []string        `json:"phases,omitempty"`
	CurrentPhase string          `json:"current_phase,omitempty"`
	State        WorkflowState   `json:"state"`
	Agents       []WorkflowAgent `json:"agents,omitempty"`
	Log          []string        `json:"log,omitempty"`
	StartedAt    string          `json:"started_at"`
	FinishedAt   string          `json:"finished_at,omitempty"`
	Error        string          `json:"error,omitempty"`
	Result       string          `json:"result,omitempty"`
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
	CostUSD      float64         `json:"cost_usd"`
}

// WorkflowSpec is one Workflow tool call.
type WorkflowSpec struct {
	// Script, ScriptPath and Name say where the script is; at most one, and none only with ResumeFrom.
	Script     string
	ScriptPath string
	Name       string
	Args       json.RawMessage
	ResumeFrom string
	ToolUseID  string
}

// WorkflowPlan is what the user approves: read from the script without running it. Name, Description and Phases are
// the script's words.
type WorkflowPlan struct {
	Name        string
	Description string
	Phases      []string
	// Source says where the script came from ("" for one written in the call).
	Source string
	// ResumeFrom is the run this call continues.
	ResumeFrom string
	// MaxAgents is the most agent() calls the run may make.
	MaxAgents int
}

// Workflows is the session's set of runs (implemented by workflow.Manager).
type Workflows interface {
	// Plan reads a call's script without running it, and keeps what it read for Start: the run is the script that was
	// approved, whatever happens to a file meanwhile.
	Plan(spec WorkflowSpec) (WorkflowPlan, error)
	// Start starts the run Plan read for this call, in the background. A call Plan did not read does not start: it
	// reads as the error Plan gave, or as not checked.
	Start(spec WorkflowSpec) (WorkflowInfo, error)
	Stop(id string) (WorkflowInfo, error)
	Get(id string) (WorkflowInfo, bool)
}

// ErrWorkflowNotRunning is returned by Workflows.Stop for a run that has ended.
var ErrWorkflowNotRunning = errors.New("workflow is not running")

// ---- Workflow ----

// Workflow runs a script that orchestrates subagents, in the background.
type Workflow struct{}

func (*Workflow) Name() string { return "Workflow" }

// ReadOnly: a run spends a lot and can do what its agents may, so plan mode refuses it.
func (*Workflow) ReadOnly(json.RawMessage) bool { return false }

func workflowSpec(call Call) (WorkflowSpec, error) {
	var in struct {
		Script     string          `json:"script"`
		ScriptPath string          `json:"script_path"`
		Name       string          `json:"name"`
		Args       json.RawMessage `json:"args"`
		ResumeFrom string          `json:"resume_from"`
	}
	if err := decode(call.Input, &in); err != nil {
		return WorkflowSpec{}, err
	}
	return WorkflowSpec{Script: in.Script, ScriptPath: in.ScriptPath, Name: in.Name, Args: in.Args, ResumeFrom: in.ResumeFrom, ToolUseID: call.ID}, nil
}

// PermissionHints implements PermissionHinter: only the user may approve a run, once for the whole run, shown its name,
// description and phases. What it cannot run passes the check and fails in Run. In bypass mode nobody is asked.
func (*Workflow) PermissionHints(_ context.Context, env *Env, call Call) PermissionHints {
	spec, err := workflowSpec(call)
	if err != nil || env.Workflows == nil {
		return PermissionHints{NoEffect: true}
	}
	plan, err := env.Workflows.Plan(spec)
	if err != nil {
		return PermissionHints{NoEffect: true}
	}
	label := "Workflow " + promptText(plan.Name, 80)
	if h := env.Host(); h != nil && h.PermissionMode() == string(permissions.ModeBypassPermissions) {
		return PermissionHints{Label: label}
	}
	return PermissionHints{Label: label, Sensitive: workflowPrompt(plan)}
}

// workflowPrompt is the text of the permission prompt. The script's words go through promptText: they are the model's,
// and must not be able to lay out a prompt of their own.
func workflowPrompt(p WorkflowPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run the workflow %q: %s", promptText(p.Name, 80), promptText(p.Description, 400))
	if len(p.Phases) > 0 {
		phases := p.Phases
		more := ""
		if len(phases) > 12 {
			more = fmt.Sprintf(" and %d more", len(phases)-12)
			phases = phases[:12]
		}
		names := make([]string, len(phases))
		for i, n := range phases {
			names[i] = promptText(n, 60)
		}
		fmt.Fprintf(&b, " Phases: %s%s.", strings.Join(names, ", "), more)
	}
	if p.Source != "" {
		fmt.Fprintf(&b, " Script: %s.", promptText(p.Source, 200))
	}
	if p.ResumeFrom != "" {
		fmt.Fprintf(&b, " It continues run %s: finished agents are not run again.", p.ResumeFrom)
	}
	fmt.Fprintf(&b, " It can start up to %d agents, which can spend a lot; their tool calls are checked as usual.", p.MaxAgents)
	return b.String()
}

// promptText flattens the script's words to one bounded line.
func promptText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

func (*Workflow) Run(ctx context.Context, env *Env, call Call) Result {
	spec, err := workflowSpec(call)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if env.Workflows == nil {
		return ErrorResult("workflows are not available here: only the session's main agent can run one")
	}
	info, err := env.Workflows.Start(spec)
	if err != nil {
		return ErrorResult("%v", err)
	}
	text := fmt.Sprintf("Workflow run %s (%s) started in the background. Its result comes to you as a reminder when it ends, and starts a new turn if you are idle by then. Do not poll for it; go on with other work or end your turn. TaskStop with this id stops it and its agents.", info.ID, info.Name)
	r := TextResult(text, map[string]any{"run_id": info.ID, "name": info.Name, "state": string(info.State)})
	r.Summary = "Started " + info.ID
	return r
}

// stopWorkflow is TaskStop for a run id. ok is false when id names no run.
func stopWorkflow(env *Env, id string) (Result, bool) {
	if env.Workflows == nil {
		return Result{}, false
	}
	if _, found := env.Workflows.Get(id); !found {
		return Result{}, false
	}
	info, err := env.Workflows.Stop(id)
	if err != nil {
		if errors.Is(err, ErrWorkflowNotRunning) {
			return ErrorResult("workflow run %s is not running (%s)", id, info.State), true
		}
		return ErrorResult("%v", err), true
	}
	r := TextResult(fmt.Sprintf("Stopped workflow run %s (%s) and its agents. Workflow with resume_from %s continues it: finished agents are not run again.", info.ID, info.Name, info.ID),
		map[string]any{"task_id": info.ID, "message": "stopped"})
	r.Summary = "Stopped " + info.ID
	return r, true
}
