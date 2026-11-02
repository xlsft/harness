package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/xlsft/harness/packages/cli/internal/render"
)

// View renders the live dock (reference §0). Everything above it is terminal scrollback.
func (m *Model) View() tea.View {
	if m.quitting || !m.started {
		return tea.NewView("")
	}
	s := m.st
	w := m.width
	var parts []string

	// In-progress transcript items (live tool groups, the streaming paragraph).
	for i, it := range m.pending {
		body := m.renderItem(it, w, m.frame)
		if body == "" {
			continue
		}
		if (len(m.printed) > 0 || i > 0) && !joinItem(it) {
			parts = append(parts, "")
		}
		parts = append(parts, body)
	}

	// Working line.
	if m.busy {
		parts = append(parts, "")
		parts = append(parts, s.WorkingLine(m.working(), m.frame, w))
		if len(m.todos) > 0 && m.showTodos {
			parts = append(parts, indentTodos(s.TodoList(m.todos, w-3)))
		}
	}
	if len(m.queued) > 0 {
		parts = append(parts, s.Queued(m.queued, w))
	}
	if !m.busy && len(m.todos) > 0 && m.showTodos && !allDone(m.todos) {
		parts = append(parts, "", s.TodoList(m.todos, w))
	}

	// Modal prompt or input.
	parts = append(parts, "")
	if p := m.promptView(w); p != nil {
		parts = append(parts, s.PromptBox(*p, w))
	} else {
		if m.menu != nil {
			parts = append(parts, s.Menu(m.menu.items, m.menu.sel, w))
		}
		placeholder := ""
		if m.busy && len(m.queued) > 0 {
			placeholder = "Messages are sent after the current tool calls"
		}
		lines := s.InputLines(m.input.text, m.input.cursor, w, placeholder, "")
		hint := ""
		if time.Now().Before(m.hintUntil) {
			hint = m.hint
		} else if len(lines) > 1 {
			hint = "\\ + enter for a new line"
		}
		parts = append(parts, s.InputBox(lines, w, hint))
		parts = append(parts, "")
		if m.shortcuts {
			parts = append(parts, s.Shortcuts(w))
		} else {
			parts = append(parts, s.FooterLine(m.footer(), w))
		}
	}
	return tea.NewView(strings.Join(parts, "\n"))
}

func allDone(ts []render.Todo) bool {
	for _, t := range ts {
		if t.Status != 2 {
			return false
		}
	}
	return true
}

func indentTodos(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "   " + strings.TrimPrefix(l, "  ")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) working() render.Working {
	status := m.status
	if status == "" {
		status = "Thinking"
	}
	return render.Working{
		Status:  status,
		Elapsed: time.Since(m.turnStart),
		Tokens:  m.turnTokens + m.liveChars/4,
		Retry:   m.retry,
	}
}

func (m *Model) footer() render.Footer {
	f := render.Footer{
		Mode: m.footerMode(), Model: m.model, ContextPct: m.ctxPct, Cost: m.cost,
		Tasks: m.runningTasks(), HasText: !m.input.Empty(),
	}
	if m.menu != nil {
		f.Hint = "tab to complete · enter to select · esc to close"
	}
	return f
}

func (m *Model) promptView(w int) *render.Prompt {
	var p render.Prompt
	switch {
	case len(m.perms) > 0:
		p = m.permView(m.perms[0], w)
	case m.question != nil:
		p = m.questionView(m.question, w)
	case m.plan != nil:
		p = m.planView(m.plan, w)
	case m.choice != nil:
		p = m.choiceView(m.choice)
	default:
		return nil
	}
	return &p
}
