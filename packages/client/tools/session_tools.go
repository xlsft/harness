package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xlsft/harness/packages/client/prompts"
)

// ---- EnterPlanMode / ExitPlanMode ----

// EnterPlanMode switches the session to plan mode (read-only tools only).
type EnterPlanMode struct{}

func (*EnterPlanMode) Name() string                  { return "EnterPlanMode" }
func (*EnterPlanMode) ReadOnly(json.RawMessage) bool { return true }

// NextPlanPath returns the file the next plan of the session is written to:
// <PlansDir>/<n>.md.
func NextPlanPath(env *Env) string {
	n := 1
	entries, _ := os.ReadDir(env.PlansDir)
	for _, e := range entries {
		if v, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".md")); err == nil && v >= n {
			n = v + 1
		}
	}
	return filepath.Join(env.PlansDir, strconv.Itoa(n)+".md")
}

func (*EnterPlanMode) Run(ctx context.Context, env *Env, call Call) Result {
	h := env.Host()
	if h == nil {
		return ErrorResult("plan mode is not available in this session")
	}
	if h.PermissionMode() == "plan" {
		return TextResult("Plan mode is already active.", map[string]any{"message": "already in plan mode"})
	}
	if err := h.SetPermissionMode("plan"); err != nil {
		return ErrorResult("cannot enter plan mode: %v", err)
	}
	path := NextPlanPath(env)
	h.Remind("plan_mode", map[string]string{"plan_file_path": path})
	r := TextResult("Entered plan mode. Explore with read-only tools, then present the plan with ExitPlanMode.", map[string]any{"message": "entered plan mode", "planFilePath": path})
	r.Summary = "Entered plan mode"
	return r
}

// ExitPlanMode writes the plan to the session plans directory and asks the
// user to approve it.
type ExitPlanMode struct{}

func (*ExitPlanMode) Name() string                  { return "ExitPlanMode" }
func (*ExitPlanMode) ReadOnly(json.RawMessage) bool { return true }

func (*ExitPlanMode) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Plan string `json:"plan"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Plan) == "" {
		return ErrorResult("InputValidationError: plan is required")
	}
	h := env.Host()
	if h == nil || h.PermissionMode() != "plan" {
		return ErrorResult("ExitPlanMode can only be used in plan mode.")
	}
	path := NextPlanPath(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ErrorResult("cannot write plan: %v", err)
	}
	if err := os.WriteFile(path, []byte(in.Plan), 0o600); err != nil {
		return ErrorResult("cannot write plan: %v", err)
	}
	ia := h.Interactor()
	if ia == nil {
		return ErrorResult("The plan was saved to %s, but approving it requires an interactive session. Stop here and present the plan to the user.", path)
	}
	resp, err := ia.ProposePlan(ctx, call.ID, in.Plan, path)
	if errors.Is(err, ErrNonInteractive) {
		return ErrorResult("The plan was saved to %s, but no user is attached to approve it. Stop here and present the plan to the user.", path)
	}
	if err != nil {
		return ErrorResult("plan was not answered: %v", err)
	}
	structured := map[string]any{"plan": in.Plan, "filePath": path}
	switch resp.Decision {
	case PlanApproveAcceptEdits, PlanApproveManual:
		// The first approval returns to the mode plan mode was entered from (auto, accept edits, ...); from
		// "ask" (or unknown) it accepts edits. The second asks for each edit.
		mode := "acceptEdits"
		if p, ok := h.(interface{ PrePlanMode() string }); ok && p.PrePlanMode() != "default" && p.PrePlanMode() != "" {
			mode = p.PrePlanMode()
		}
		if resp.Decision == PlanApproveManual {
			mode = "default"
		}
		if err := h.SetPermissionMode(mode); err != nil {
			return ErrorResult("cannot leave plan mode: %v", err)
		}
		h.Remind("plan_approved", map[string]string{"plan_file_path": path})
		structured["approved"] = true
		r := TextResult(fmt.Sprintf("The user approved the plan. It is saved at %s. Start implementing it.", path), structured)
		r.Summary = "Plan approved"
		return r
	default:
		tmpl := env.reminder("plan_rejected")
		if tmpl == "" {
			tmpl = "The user wants to keep planning. Feedback: {{feedback}}"
		}
		structured["approved"] = false
		structured["feedback"] = resp.Feedback
		r := TextResult(prompts.Render(tmpl, map[string]string{"feedback": resp.Feedback}, nil), structured)
		r.IsError = true
		r.Summary = "Keep planning"
		return r
	}
}

// ---- WrapUp ----

// WrapUp closes a turn: the frontends show its recap, question and buttons under the last message (they read them
// from the call). A button sends its label as the user's reply.
type WrapUp struct{}

func (*WrapUp) Name() string                  { return "WrapUp" }
func (*WrapUp) ReadOnly(json.RawMessage) bool { return true }

func (*WrapUp) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Recap   string   `json:"recap"`
		Ask     string   `json:"ask"`
		Actions []string `json:"actions"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Recap) == "" {
		return ErrorResult("InputValidationError: recap is required")
	}
	if in.Ask != "" && len(in.Actions) == 0 {
		return ErrorResult("InputValidationError: ask needs actions: one or two answers the user can click")
	}
	if len(in.Actions) > 2 {
		return ErrorResult("InputValidationError: at most two actions")
	}
	r := TextResult("Shown to the user under your last message. Now write your closing message, your full answer, with no more tool calls.", nil)
	r.Summary = "Wrapped up"
	return r
}

// ---- TodoWrite ----

// Todo is one item of the session todo list.
type Todo struct {
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
}

// TodoWrite replaces the session todo list.
type TodoWrite struct{}

func (*TodoWrite) Name() string                  { return "TodoWrite" }
func (*TodoWrite) ReadOnly(json.RawMessage) bool { return true }

// ParseTodos decodes and validates a TodoWrite input.
func ParseTodos(input json.RawMessage) ([]Todo, error) {
	var in struct {
		Todos *[]Todo `json:"todos"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	if in.Todos == nil {
		return nil, errors.New("InputValidationError: todos is required")
	}
	for i, t := range *in.Todos {
		if strings.TrimSpace(t.Content) == "" {
			return nil, fmt.Errorf("InputValidationError: todos[%d].content is required", i)
		}
		if strings.TrimSpace(t.ActiveForm) == "" {
			return nil, fmt.Errorf("InputValidationError: todos[%d].activeForm is required", i)
		}
		switch t.Status {
		case "pending", "in_progress", "completed":
		default:
			return nil, fmt.Errorf("InputValidationError: todos[%d].status must be pending, in_progress or completed", i)
		}
	}
	return *in.Todos, nil
}

func (*TodoWrite) Run(ctx context.Context, env *Env, call Call) Result {
	todos, err := ParseTodos(call.Input)
	if err != nil {
		return ErrorResult("%v", err)
	}
	var old []Todo
	if h := env.Host(); h != nil {
		old = h.Todos()
		h.SetTodos(todos)
	}
	if old == nil {
		old = []Todo{}
	}
	done := 0
	for _, t := range todos {
		if t.Status == "completed" {
			done++
		}
	}
	r := TextResult("The todo list was updated. Keep it current as you work: mark items in_progress when you start them and completed as soon as they are done.",
		map[string]any{"oldTodos": old, "newTodos": todos})
	r.Summary = fmt.Sprintf("%d/%d todos completed", done, len(todos))
	return r
}

// ---- TaskStop ----

// TaskStop stops a background task or a subagent.
type TaskStop struct{}

func (*TaskStop) Name() string                  { return "TaskStop" }
func (*TaskStop) ReadOnly(json.RawMessage) bool { return true }

func (*TaskStop) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.TaskID == "" {
		return ErrorResult("InputValidationError: task_id is required")
	}
	if r, ok := stopAgent(env, in.TaskID); ok {
		return r
	}
	if env.Tasks == nil {
		return ErrorResult("no background tasks in this session")
	}
	info, err := env.Tasks.Stop(in.TaskID)
	if err != nil {
		return ErrorResult("%v", err)
	}
	r := TextResult(fmt.Sprintf("Stopped background task %s (%s). Its output is in %s.", info.ID, info.Command, info.OutputPath),
		map[string]any{"task_id": info.ID, "command": info.Command, "message": "stopped"})
	r.Summary = "Stopped " + info.ID
	return r
}
