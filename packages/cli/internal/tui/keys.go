package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"

	"github.com/xlsft/harness/packages/cli/brand"
)

// Update is the Bubble Tea update function.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		widthChanged := msg.Width != m.width
		m.width, m.height = msg.Width, msg.Height
		if !m.started {
			m.started = true
			m.push(&item{kind: itemBanner})
			return m, m.flush()
		}
		if widthChanged {
			return m, m.reprint()
		}
		return m, nil

	case eventMsg:
		cmd := m.onEvent(msg.ev)
		return m, tea.Batch(cmd, m.flush(), m.maybeTick())

	case subEndedMsg:
		if m.quitting {
			return m, nil
		}
		m.push(&item{kind: itemError, text: "Lost the connection to the agent: " + errText(msg.err)})
		return m, m.flush()

	case tickMsg:
		m.ticking = false
		m.frame++
		if m.retry != nil {
			m.retry.In = max(time.Until(m.retryAt), 0)
		}
		return m, m.maybeTick()

	case errMsg:
		m.push(&item{kind: itemError, text: errText(msg.err)})
		return m, m.flush()

	case infoMsg:
		m.push(&item{kind: itemCommand, text: msg.cmd, output: msg.out})
		return m, m.flush()

	case suggestMsg:
		if m.menu != nil && m.menu.kind == menuFiles && msg.seq == m.suggestSeq {
			m.menu.setFiles(msg.paths)
		}
		return m, nil

	case checkpointsMsg:
		m.openRewind(msg.cps)
		return m, nil

	case repaintMsg:
		return m, tea.ClearScreen

	case sessionMsg:
		return m, m.subscribe()

	case tea.PasteMsg:
		m.target().Insert(msg.Content)
		m.updateMenu()
		return m, m.menuCmd()

	case tea.KeyPressMsg:
		cmd := m.onKey(msg)
		return m, tea.Batch(cmd, m.flush(), m.maybeTick())
	}
	return m, nil
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func (m *Model) maybeTick() tea.Cmd {
	if m.animating() || m.retry != nil || time.Now().Before(m.hintUntil.Add(brand.FrameInterval)) {
		return m.tick()
	}
	return nil
}

// target is the editor receiving typed text: a prompt's comment field or the main input.
func (m *Model) target() *editor {
	if len(m.perms) > 0 && m.perms[0].opts.commenting {
		return &m.perms[0].opts.comment
	}
	if m.plan != nil && m.plan.opts.commenting {
		return &m.plan.opts.comment
	}
	if m.question != nil && m.question.typing {
		return &m.question.free
	}
	return &m.input
}

func (m *Model) setHint(h string) {
	m.hint, m.hintUntil = h, time.Now().Add(2*time.Second)
}

func (m *Model) onKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()

	// Global keys.
	switch key {
	case "ctrl+c":
		if m.busy {
			return m.interrupt()
		}
		if !m.input.Empty() {
			m.input.Reset()
			m.menu = nil
			return nil
		}
		if time.Since(m.ctrlCAt) < 2*time.Second {
			return m.quit()
		}
		m.ctrlCAt = time.Now()
		m.setHint("Press Ctrl-C again to exit")
		return nil
	case "ctrl+d":
		if m.input.Empty() && m.modal() == nil {
			return m.quit()
		}
	case "ctrl+o":
		m.verbose = !m.verbose
		return m.reprint()
	case "ctrl+t":
		m.showTodos = !m.showTodos
		return nil
	}

	// Modals take the keyboard.
	switch {
	case len(m.perms) > 0:
		if key == "esc" && !m.perms[0].opts.commenting {
			// Esc rejects (reference §5f); the turn stops unless a comment was given.
			return m.answerPerm(m.perms[0], 2)
		}
		return m.permKey(m.perms[0], k)
	case m.question != nil:
		return m.questionKey(m.question, k)
	case m.plan != nil:
		return m.planKey(m.plan, k)
	case m.choice != nil:
		return m.choiceKey(m.choice, k)
	}

	if key == "shift+tab" {
		return m.cycleMode()
	}

	// Menu navigation.
	if m.menu != nil {
		switch key {
		case "up", "ctrl+p":
			m.menu.move(-1)
			return nil
		case "down", "ctrl+n":
			m.menu.move(1)
			return nil
		case "esc":
			m.menu = nil
			return nil
		case "tab":
			m.completeMenu(false)
			return m.menuCmd()
		case "enter":
			if m.menu.kind == menuSlash && len(m.menu.items) > 0 {
				name := m.menu.items[m.menu.sel].Name
				m.input.Set(name)
				m.menu = nil
				return m.submit()
			}
			if m.menu.kind == menuFiles && len(m.menu.items) > 0 {
				m.completeMenu(true)
				return nil
			}
			m.menu = nil
		}
	}

	switch key {
	case "esc":
		if m.busy {
			return m.interrupt()
		}
		if !m.input.Empty() {
			if time.Since(m.escAt) < 2*time.Second {
				m.input.Reset()
				m.escAt = time.Time{}
				m.hint = ""
				return nil
			}
			m.escAt = time.Now()
			m.setHint("Esc again to clear")
			return nil
		}
		if time.Since(m.escAt) < time.Second {
			m.escAt = time.Time{}
			return m.rewindCmd()
		}
		m.escAt = time.Now()
		return nil
	case "enter":
		if t := m.input.text; m.input.cursor > 0 && t[m.input.cursor-1] == '\\' {
			m.input.Backspace()
			m.input.Insert("\n")
			return nil
		}
		return m.submit()
	case "shift+enter", "alt+enter", "ctrl+j":
		m.input.Insert("\n")
		return nil
	case "up":
		if !m.input.OnFirstLine() {
			m.input.Up()
			return nil
		}
		m.historyMove(-1)
		return nil
	case "down":
		if !m.input.OnLastLine() {
			m.input.Down()
			return nil
		}
		m.historyMove(1)
		return nil
	case "ctrl+u":
		if k := m.input.KillLine(); k != "" {
			m.killed = k
			m.setHint("Ctrl+Y to paste deleted text")
		}
		m.updateMenu()
		return nil
	case "ctrl+y":
		m.input.Insert(m.killed)
		return nil
	}
	if key == "?" && m.input.Empty() {
		m.shortcuts = !m.shortcuts
		return nil
	}
	if editKey(&m.input, k) {
		m.shortcuts = false
		m.escAt = time.Time{}
		m.updateMenu()
		return m.menuCmd()
	}
	return nil
}

// editKey applies a text-editing key to e and reports whether it was one.
func editKey(e *editor, k tea.KeyPressMsg) bool {
	switch k.String() {
	case "backspace", "ctrl+h":
		e.Backspace()
	case "delete":
		e.Delete()
	case "left", "ctrl+b":
		e.Left()
	case "right", "ctrl+f":
		e.Right()
	case "home", "ctrl+a":
		e.Home()
	case "end", "ctrl+e":
		e.End()
	case "ctrl+w", "alt+backspace":
		e.DeleteWord()
	case "space":
		e.Insert(" ")
	default:
		if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
			return false
		}
		e.Insert(k.Text)
	}
	return true
}

func (m *Model) modal() any {
	switch {
	case len(m.perms) > 0:
		return m.perms[0]
	case m.question != nil:
		return m.question
	case m.plan != nil:
		return m.plan
	case m.choice != nil:
		return m.choice
	}
	return nil
}

func (m *Model) historyMove(d int) {
	if len(m.history) == 0 {
		return
	}
	if m.histIdx == len(m.history) {
		m.draft = m.input.String()
	}
	m.histIdx = min(max(m.histIdx+d, 0), len(m.history))
	if m.histIdx == len(m.history) {
		m.input.Set(m.draft)
		return
	}
	m.input.Set(m.history[m.histIdx])
}

// submit sends the input: slash commands run locally, everything else goes to the agent (queued
// by the client while a turn runs).
func (m *Model) submit() tea.Cmd {
	text := strings.TrimRight(m.input.String(), " \n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	m.input.Reset()
	m.menu = nil
	m.shortcuts = false
	if len(m.history) == 0 || m.history[len(m.history)-1] != text {
		m.history = append(m.history, text)
		appendHistory(text)
	}
	m.histIdx = len(m.history)
	if strings.HasPrefix(text, "/") {
		if cmd, ok := m.runCommand(text); ok {
			return cmd
		}
	}
	if !m.busy {
		m.startTurn()
	} else {
		m.queued = append(m.queued, text) // shown until QueueChanged confirms
	}
	ctx := m.ctx
	return func() tea.Msg {
		if err := m.conn.Send(ctx, text); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m *Model) interrupt() tea.Cmd {
	m.interrupts++
	m.status = "Interrupting"
	ctx := m.ctx
	return func() tea.Msg {
		if err := m.conn.Interrupt(ctx); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// modeCycle is the fallback shift+tab order (reference §6a) when the client does not report its
// modes in InitializeResponse.permission_modes.
var modeCycle = []agentv1.PermissionMode{
	agentv1.PermissionMode_PERMISSION_MODE_DEFAULT,
	agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS,
	agentv1.PermissionMode_PERMISSION_MODE_PLAN,
}

func (m *Model) cycleMode() tea.Cmd {
	cycle := modeCycle
	if len(m.conn.Modes) > 0 {
		cycle = m.conn.Modes // the client's own order (includes auto when available)
	}
	next := cycle[0]
	for i, md := range cycle {
		if md == m.mode || (m.mode == agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED && i == 0) {
			next = cycle[(i+1)%len(cycle)]
		}
	}
	m.mode = next
	ctx := m.ctx
	return func() tea.Msg {
		if err := m.conn.SetPermissionMode(ctx, next); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m *Model) quit() tea.Cmd {
	m.quitting = true
	return tea.Quit
}
