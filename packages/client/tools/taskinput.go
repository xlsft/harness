package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// TaskInput writes to the standard input of a background command started with stdin.
type TaskInput struct{}

func (*TaskInput) Name() string { return "TaskInput" }

// ReadOnly: whatever reads the input runs it.
func (*TaskInput) ReadOnly(json.RawMessage) bool { return false }

func (*TaskInput) Run(_ context.Context, env *Env, call Call) Result {
	var in struct {
		TaskID string  `json:"task_id"`
		Input  *string `json:"input"`
		Close  bool    `json:"close"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.TaskID == "" || (in.Input == nil && !in.Close) {
		return ErrorResult("InputValidationError: task_id and one of input or close are required")
	}
	if env.Tasks == nil {
		return ErrorResult("Background commands are not available in this session.")
	}
	text := ""
	if in.Input != nil {
		text = *in.Input
	}
	if err := env.Tasks.WriteStdin(in.TaskID, text, in.Close); err != nil {
		return ErrorResult("%v", err)
	}
	msg := fmt.Sprintf("Wrote %d bytes to task %s. TaskOutput shows what it answered.", len(text), in.TaskID)
	if in.Close {
		msg = fmt.Sprintf("Wrote %d bytes to task %s and closed its input.", len(text), in.TaskID)
	}
	r := TextResult(msg, map[string]any{"task_id": in.TaskID, "bytes": len(text), "closed": in.Close})
	r.Summary = "Wrote to " + in.TaskID
	return r
}

// WriteStdin writes to a running task's standard input, and closes it if asked.
func (m *TaskManager) WriteStdin(id, text string, closeAfter bool) error {
	m.mu.Lock()
	t := m.tasks[id]
	var w io.WriteCloser
	running := false
	if t != nil {
		w, running = t.stdin, t.info.Running
		if closeAfter {
			t.stdin = nil
		}
	}
	m.mu.Unlock()
	switch {
	case t == nil:
		return fmt.Errorf("no background task %s", id)
	case !running:
		return fmt.Errorf("task %s has ended", id)
	case w == nil:
		return fmt.Errorf("task %s takes no input: start it with stdin true on Bash, or its input was closed", id)
	}
	if text != "" {
		if _, err := io.WriteString(w, text); err != nil {
			return fmt.Errorf("task %s did not take the input: %w", id, err)
		}
	}
	if closeAfter {
		return errors.Join(w.Close())
	}
	return nil
}
