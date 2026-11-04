package agent

import (
	"context"
	"strconv"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// Limits for injected reminders.
const (
	maxFileChangedReminders = 5
	fileChangedDiffLines    = 40
)

var _ tools.Host = (*Agent)(nil)

// PermissionMode implements tools.Host.
func (a *Agent) PermissionMode() string { return a.permissionMode() }

// SetPermissionMode implements tools.Host.
func (a *Agent) SetPermissionMode(mode string) error {
	if a.cfg.Modes == nil {
		return nil
	}
	m, err := permissions.ParseMode(mode)
	if err != nil {
		return err
	}
	return a.cfg.Modes.SetMode(m)
}

// PrePlanMode is the mode plan mode was entered from ("default" when unknown); approving a plan returns to it.
func (a *Agent) PrePlanMode() string {
	if p, ok := a.cfg.Modes.(interface{ PrePlanMode() permissions.Mode }); ok {
		return string(p.PrePlanMode())
	}
	return string(permissions.ModeDefault)
}

// Remind implements tools.Host: the reminder is injected into the next
// model request (skipped when the bundle lacks the id).
func (a *Agent) Remind(id string, vars map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, r := range a.reminders {
		if r.id == id && id != "file_changed" && id != "background_task_done" && id != ReminderUserShell {
			if id == "environment_changed" || id == "session_moved" {
				a.reminders[i].vars = vars // the latest state replaces the pending one
			}
			return // one pending instance is enough
		}
	}
	a.reminders = append(a.reminders, pendingReminder{id: id, vars: vars})
}

// Todos implements tools.Host.
func (a *Agent) Todos() []tools.Todo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]tools.Todo(nil), a.todos...)
}

// SetTodos implements tools.Host.
func (a *Agent) SetTodos(t []tools.Todo) {
	a.mu.Lock()
	a.todos = append([]tools.Todo(nil), t...)
	a.roundsSinceTodo = 0
	a.mu.Unlock()
	a.emit(Event{Kind: EventTodos, Todos: append([]tools.Todo(nil), t...)})
}

// Interactor implements tools.Host.
func (a *Agent) Interactor() tools.Interactor {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.interactor
}

// SetInteractor attaches the interactive frontend (nil detaches).
func (a *Agent) SetInteractor(ia tools.Interactor) {
	a.mu.Lock()
	a.interactor = ia
	a.mu.Unlock()
}

// Background task completion notice: the tail of the output it carries.
const (
	taskDoneTailLines  = 10
	taskDoneTailBytes  = 1500
	taskDoneCommandMax = 200
)

// OnTaskEvent forwards a background task event and queues the
// background_task_done reminder when a task ends. The reminder rides on the
// next model request: the following request of a running turn, or a turn the
// task's end starts itself when the session is idle (SetWake). It is dropped
// when a TaskOutput or Wait result already reported the finish.
func (a *Agent) OnTaskEvent(e tools.TaskEvent) {
	ev := e
	a.emit(Event{Kind: EventTask, Task: &ev})
	if e.Kind == tools.TaskEventFinished && e.Task.Reason != tools.TaskSessionClosed {
		tail := tools.TailLines(tools.ReadTail(e.Task.OutputPath, 4*taskDoneTailBytes), taskDoneTailLines, taskDoneTailBytes)
		if strings.TrimSpace(tail) == "" {
			tail = "(no output)"
		}
		cmd := e.Task.Command
		if len(cmd) > taskDoneCommandMax {
			cmd = strings.ToValidUTF8(cmd[:taskDoneCommandMax], "") + "…"
		}
		a.Remind("background_task_done", map[string]string{
			"task_id": e.Task.ID, "exit_code": strconv.Itoa(e.Task.ExitCode), "output_path": e.Task.OutputPath,
			"command": cmd, "reason": string(e.Task.Reason), "status": tools.TaskStatus(e.Task), "tail": tail,
		})
		// A task that ended by itself (done, failed, out of time) wakes an idle session so the model answers it;
		// one stopped on request does not.
		a.mu.Lock()
		wake := a.wake
		a.mu.Unlock()
		if wake != nil && e.Task.Reason != tools.TaskStopped {
			go wake()
		}
	}
}

// SetWake sets what starts a turn for a notification while the session is idle (Driver.Wake).
func (a *Agent) SetWake(f func()) {
	a.mu.Lock()
	a.wake = f
	a.mu.Unlock()
}

// HasReminders reports whether reminders wait for the next model request (stale task reports excluded).
func (a *Agent) HasReminders() bool {
	a.mu.Lock()
	pending := append([]pendingReminder(nil), a.reminders...)
	a.mu.Unlock()
	for _, r := range pending {
		if !a.taskReported(r) {
			return true
		}
	}
	return false
}

// taskReported reports whether a pending background_task_done reminder is
// stale because a tool result already told the model.
func (a *Agent) taskReported(r pendingReminder) bool {
	if r.id != "background_task_done" || a.cfg.ToolEnv == nil || a.cfg.ToolEnv.Tasks == nil {
		return false
	}
	return a.cfg.ToolEnv.Tasks.Reported(r.vars["task_id"])
}

func (a *Agent) noteToolRound(uses []llm.ContentBlock) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, u := range uses {
		if u.Name == "TodoWrite" {
			a.roundsSinceTodo = 0
			return
		}
	}
	a.roundsSinceTodo++
}

func (a *Agent) todoToolAvailable() bool {
	if a.cfg.DisabledTools["TodoWrite"] {
		return false
	}
	if _, ok := a.cfg.Tools.Get("TodoWrite"); !ok {
		return false
	}
	_, ok := a.Bundle().Tools["TodoWrite"]
	return ok
}

// beforeRequest runs the checks due before every model request: changed
// files, the todo nudge, auto-compaction, then pending reminders are
// injected into the last user message.
func (a *Agent) beforeRequest(ctx context.Context, promptID string) {
	if env := a.cfg.ToolEnv; env != nil && env.Files != nil {
		changes := env.Files.Changes(fileChangedDiffLines)
		for i, ch := range changes {
			if i == maxFileChangedReminders {
				break
			}
			a.Remind("file_changed", map[string]string{"path": ch.Path, "diff": ch.Diff})
		}
	}
	a.mu.Lock()
	nudge := a.roundsSinceTodo >= a.cfg.TodoNudgeRounds
	if nudge {
		a.roundsSinceTodo = 0
	}
	a.mu.Unlock()
	if nudge && a.todoToolAvailable() {
		a.Remind("todo_nudge", nil)
	}
	a.maybeAutoCompact(ctx)
	a.injectReminders(promptID)
}

// injectReminders renders pending reminders and appends them to the last
// user message (and the transcript as a meta entry).
func (a *Agent) injectReminders(promptID string) {
	a.mu.Lock()
	pending := a.reminders
	a.reminders = nil
	bundle := a.bundle
	a.mu.Unlock()
	var blocks []llm.ContentBlock
	for _, r := range pending {
		if a.taskReported(r) {
			continue
		}
		tmpl, ok := bundle.Reminders[r.id]
		if r.id == ReminderOutputStyle {
			tmpl, ok = styleTemplate(bundle), true
			a.mu.Lock()
			a.noteStyleInjected(r.vars)
			a.mu.Unlock()
		}
		if r.id == ReminderUsageWrapUp {
			tmpl, ok = bundle.Reminder(r.id, usagelimit.WrapUpFallback), true
			a.mu.Lock()
			a.usage.delivered = true
			a.mu.Unlock()
		}
		if r.id == ReminderUsageReturned {
			tmpl, ok = bundle.Reminder(r.id, usagelimit.ReturnedFallback), true
		}
		if !ok || tmpl == "" {
			continue
		}
		text := prompts.Render(tmpl, r.vars, nil)
		if strings.TrimSpace(text) == "" {
			continue
		}
		blocks = append(blocks, llm.TextBlock(text))
	}
	if len(blocks) == 0 {
		return
	}
	a.appendMessage(llm.RoleUser, blocks)
	a.record(session.Entry{Type: session.TypeUser, Message: session.UserBlocks(blocks), IsMeta: true, PromptID: promptID})
}
