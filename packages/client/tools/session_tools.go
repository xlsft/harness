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

// ---- AskUserQuestion ----

// QuestionOption is one choice of a question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Question is one AskUserQuestion item.
type Question struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multiSelect"`
}

// AskUserQuestion asks the user 1–4 multiple-choice questions and blocks
// until they answer.
type AskUserQuestion struct{}

func (*AskUserQuestion) Name() string                  { return "AskUserQuestion" }
func (*AskUserQuestion) ReadOnly(json.RawMessage) bool { return true }

func validateQuestions(qs []Question) error {
	if len(qs) < 1 || len(qs) > 4 {
		return fmt.Errorf("questions must contain 1 to 4 items, got %d", len(qs))
	}
	seen := map[string]bool{}
	for i, q := range qs {
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("questions[%d].question is required", i)
		}
		if strings.TrimSpace(q.Header) == "" {
			return fmt.Errorf("questions[%d].header is required", i)
		}
		if seen[q.Question] {
			return fmt.Errorf("question texts must be unique: %q", q.Question)
		}
		seen[q.Question] = true
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return fmt.Errorf("questions[%d].options must contain 2 to 4 items, got %d", i, len(q.Options))
		}
		labels := map[string]bool{}
		for j, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" {
				return fmt.Errorf("questions[%d].options[%d].label is required", i, j)
			}
			if labels[o.Label] {
				return fmt.Errorf("option labels must be unique within a question: %q", o.Label)
			}
			labels[o.Label] = true
		}
	}
	return nil
}

func (*AskUserQuestion) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Questions []Question `json:"questions"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if err := validateQuestions(in.Questions); err != nil {
		return ErrorResult("InputValidationError: %v", err)
	}
	var ia Interactor
	if h := env.Host(); h != nil {
		ia = h.Interactor()
	}
	if ia == nil {
		return ErrorResult("AskUserQuestion is not available: this session is non-interactive. Make a reasonable assumption, state it explicitly, and continue.")
	}
	answers, err := ia.AskQuestions(ctx, call.ID, in.Questions)
	if errors.Is(err, ErrNonInteractive) {
		return ErrorResult("AskUserQuestion is not available: no user is attached to this session. Make a reasonable assumption, state it explicitly, and continue.")
	}
	if err != nil {
		return ErrorResult("question was not answered: %v", err)
	}
	byQuestion := map[string]string{}
	var parts []string
	for i, q := range in.Questions {
		var sel []string
		if i < len(answers) {
			sel = answers[i]
		}
		a := strings.Join(sel, ", ")
		byQuestion[q.Question] = a
		parts = append(parts, fmt.Sprintf("%q = %q", q.Question, a))
	}
	r := TextResult("The user answered your questions: "+strings.Join(parts, "; ")+". Continue with these answers in mind.",
		map[string]any{"questions": in.Questions, "answers": byQuestion})
	r.Summary = fmt.Sprintf("Answered %d question(s)", len(in.Questions))
	return r
}

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
		mode := "acceptEdits"
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

// TaskStop stops a background task.
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
