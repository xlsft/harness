package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// IsolationWorktree is the Agent tool's isolation value for a git worktree of the agent's own.
const IsolationWorktree = "worktree"

// AgentState says how a subagent stands.
type AgentState string

// Subagent states.
const (
	AgentQueued      AgentState = "queued"
	AgentRunning     AgentState = "running"
	AgentFinished    AgentState = "finished"
	AgentFailed      AgentState = "failed"
	AgentStopped     AgentState = "stopped"
	AgentInterrupted AgentState = "interrupted"
)

// Active reports whether the agent is queued or running.
func (s AgentState) Active() bool { return s == AgentQueued || s == AgentRunning }

// AgentStep statuses.
const (
	AgentStepRunning = "running"
	AgentStepDone    = "done"
	AgentStepFailed  = "failed"
)

// AgentStep is one tool call of a subagent.
type AgentStep struct {
	ToolUseID string `json:"tool_use_id"`
	Tool      string `json:"tool"`
	Target    string `json:"target,omitempty"`
	Status    string `json:"status"`
}

// AgentInfo describes a subagent (harness.agent.v1.SubagentInfo).
type AgentInfo struct {
	ID             string      `json:"id"`
	ParentID       string      `json:"parent_id,omitempty"`
	ToolUseID      string      `json:"tool_use_id,omitempty"`
	Name           string      `json:"name"`
	Team           string      `json:"team,omitempty"`
	Type           string      `json:"type"`
	Model          string      `json:"model"`
	State          AgentState  `json:"state"`
	CurrentTool    string      `json:"current_tool,omitempty"`
	Steps          []AgentStep `json:"steps,omitempty"`
	ToolCalls      int         `json:"tool_calls"`
	InputTokens    int64       `json:"input_tokens"`
	OutputTokens   int64       `json:"output_tokens"`
	CostUSD        float64     `json:"cost_usd"`
	StartedAt      string      `json:"started_at"`
	FinishedAt     string      `json:"finished_at,omitempty"`
	Report         string      `json:"report,omitempty"`
	Error          string      `json:"error,omitempty"`
	WorktreePath   string      `json:"worktree_path,omitempty"`
	WorktreeBranch string      `json:"worktree_branch,omitempty"`
	Transcript     string      `json:"transcript,omitempty"`
}

// AgentType is a kind of subagent the Agent tool can start.
type AgentType struct {
	Name        string
	Description string
	// ReadOnly: only read-only calls are allowed, whatever the permission rules say.
	ReadOnly bool
}

// AgentSpec is one Agent tool call.
type AgentSpec struct {
	Description string
	Prompt      string
	Type        string
	Model       string
	Isolation   string
	ToolUseID   string
	// Team makes it a member of that team: peers that message each other and share the session's task graph.
	Team string
	// Fork starts it with a copy of the caller's conversation, on the caller's model.
	Fork bool
}

// Agents is the session's set of subagents (implemented by subagent.Manager). caller is the Env.AgentID of the
// calling agent: empty for the session's main agent.
type Agents interface {
	Spawn(ctx context.Context, caller string, spec AgentSpec) (AgentInfo, error)
	// Send gives an agent a message: a running one reads it after its current tool calls, any other continues with
	// its context. queued reports the former.
	Send(ctx context.Context, caller, to, message string) (info AgentInfo, queued bool, err error)
	Stop(caller, id string) (AgentInfo, error)
	Get(id string) (AgentInfo, bool)
	List() []AgentInfo
	Types() []AgentType
}

// ErrAgentNotRunning is returned by Agents.Stop for an agent that has ended.
var ErrAgentNotRunning = errors.New("agent is not running")

// ---- Agent ----

// Agent starts a subagent in the background.
type Agent struct{}

func (*Agent) Name() string { return "Agent" }

// ReadOnly: starting an agent changes nothing by itself, its calls are checked one by one under the same permission
// mode. A worktree does change the repository.
func (*Agent) ReadOnly(input json.RawMessage) bool {
	var in struct {
		Isolation string `json:"isolation"`
	}
	return json.Unmarshal(input, &in) == nil && in.Isolation == ""
}

func (*Agent) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		SubagentType string `json:"subagent_type"`
		Model        string `json:"model"`
		Isolation    string `json:"isolation"`
		Team         string `json:"team"`
		Fork         bool   `json:"fork"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Description) == "" || strings.TrimSpace(in.Prompt) == "" {
		return ErrorResult("InputValidationError: description and prompt are required")
	}
	if in.Isolation != "" && in.Isolation != IsolationWorktree {
		return ErrorResult("InputValidationError: isolation must be %q", IsolationWorktree)
	}
	if env.Agents == nil {
		return ErrorResult("subagents are not available in this session")
	}
	info, err := env.Agents.Spawn(ctx, env.AgentID, AgentSpec{Description: in.Description, Prompt: in.Prompt, Type: in.SubagentType, Model: in.Model, Isolation: in.Isolation, ToolUseID: call.ID, Team: strings.TrimSpace(in.Team), Fork: in.Fork})
	if err != nil {
		return ErrorResult("%v", err)
	}
	text := fmt.Sprintf("Agent %s started in the background (type %s, model %s). Its report comes to you as a reminder when it finishes, and starts a new turn if you are idle by then. Do not poll for it; go on with other work or end your turn.", info.ID, info.Type, info.Model)
	if info.State == AgentQueued {
		text = fmt.Sprintf("Agent %s is queued (type %s, model %s): the session runs as many agents as it may at once, and it starts when one ends. Its report comes to you as a reminder when it finishes, and starts a new turn if you are idle by then.", info.ID, info.Type, info.Model)
	}
	if info.WorktreePath != "" {
		text += fmt.Sprintf(" It works in the git worktree %s on branch %s.", info.WorktreePath, info.WorktreeBranch)
	}
	r := TextResult(text, map[string]any{"agent_id": info.ID, "name": info.Name, "type": info.Type, "model": info.Model, "state": string(info.State), "worktree_path": info.WorktreePath, "worktree_branch": info.WorktreeBranch})
	r.Summary = "Started " + info.ID
	LinkTodoAgent(env, info.ID)
	return r
}

// ---- SendMessage ----

// SendMessage gives a subagent a message.
type SendMessage struct{}

func (*SendMessage) Name() string                  { return "SendMessage" }
func (*SendMessage) ReadOnly(json.RawMessage) bool { return true }

func (*SendMessage) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		To      string `json:"to"`
		Message string `json:"message"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.To) == "" || strings.TrimSpace(in.Message) == "" {
		return ErrorResult("InputValidationError: to and message are required")
	}
	if env.Agents == nil {
		return ErrorResult("subagents are not available in this session")
	}
	info, queued, err := env.Agents.Send(ctx, env.AgentID, in.To, in.Message)
	if err != nil {
		return ErrorResult("%v", err)
	}
	text := fmt.Sprintf("Agent %s is continuing with your message. Its next report comes as a reminder.", info.ID)
	if queued {
		text = fmt.Sprintf("Agent %s is running: it reads your message after its current tool calls.", info.ID)
	}
	r := TextResult(text, map[string]any{"agent_id": info.ID, "name": info.Name, "state": string(info.State), "queued": queued})
	r.Summary = "Sent to " + info.ID
	return r
}

// ---- ListAgents ----

// ListAgents lists the session's subagents and the types the Agent tool can start.
type ListAgents struct{}

func (*ListAgents) Name() string                  { return "ListAgents" }
func (*ListAgents) ReadOnly(json.RawMessage) bool { return true }

func (*ListAgents) Run(ctx context.Context, env *Env, call Call) Result {
	if env.Agents == nil {
		return ErrorResult("subagents are not available in this session")
	}
	list := env.Agents.List()
	var b strings.Builder
	if len(list) == 0 {
		b.WriteString("No agents in this session.\n")
	}
	rows := make([]map[string]any, 0, len(list))
	for _, a := range list {
		fmt.Fprintf(&b, "%s  %s  type=%s  state=%s  model=%s  tool_calls=%d", a.ID, a.Name, a.Type, a.State, a.Model, a.ToolCalls)
		if a.ParentID != "" {
			fmt.Fprintf(&b, "  started_by=%s", a.ParentID)
		}
		if a.Team != "" {
			fmt.Fprintf(&b, "  team=%s", a.Team)
		}
		if a.CurrentTool != "" {
			fmt.Fprintf(&b, "  running=%s", a.CurrentTool)
		}
		b.WriteByte('\n')
		rows = append(rows, map[string]any{"id": a.ID, "name": a.Name, "type": a.Type, "state": string(a.State), "model": a.Model, "parent_id": a.ParentID, "team": a.Team})
	}
	b.WriteString("\nTypes you can start:\n")
	for _, t := range env.Agents.Types() {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Description)
	}
	r := TextResult(strings.TrimRight(b.String(), "\n"), map[string]any{"agents": rows})
	r.Summary = fmt.Sprintf("%d agents", len(list))
	return r
}

// ---- TaskStop on an agent ----

// stopAgent is TaskStop for a subagent id. ok is false when id names no agent.
func stopAgent(env *Env, id string) (Result, bool) {
	if env.Agents == nil {
		return Result{}, false
	}
	if _, found := env.Agents.Get(id); !found {
		return Result{}, false
	}
	info, err := env.Agents.Stop(env.AgentID, id)
	if err != nil {
		if errors.Is(err, ErrAgentNotRunning) {
			return ErrorResult("agent %s is not running (%s)", id, info.State), true
		}
		return ErrorResult("%v", err), true
	}
	r := TextResult(fmt.Sprintf("Stopped agent %s (%s). SendMessage continues it with its context.", info.ID, info.Name),
		map[string]any{"task_id": info.ID, "message": "stopped"})
	r.Summary = "Stopped " + info.ID
	return r, true
}
