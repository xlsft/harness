package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"

	"github.com/xlsft/harness/packages/cli/internal/render"
)

// newStyleLabel is the last entry of the /output-style picker.
const newStyleLabel = "New style…"

// isDefaultStyle reports whether id selects no output style.
func isDefaultStyle(id string) bool { return id == "" || strings.EqualFold(id, "default") }

// cmdOutputStyle switches the output style (`/output-style <name>`) or opens the picker.
func (m *Model) cmdOutputStyle(args string) tea.Cmd {
	if args != "" {
		return m.setOutputStyle(args)
	}
	ctx := m.ctx
	return func() tea.Msg {
		styles, active, err := m.conn.OutputStyles(ctx)
		if err != nil {
			return errMsg{err}
		}
		return stylesMsg{styles, active}
	}
}

// stylesMsg carries ListOutputStyles for the picker.
type stylesMsg struct {
	styles []*agentv1.OutputStyleInfo
	active string
}

func (m *Model) setOutputStyle(id string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		if err := m.conn.SetOutputStyle(ctx, id); err != nil {
			return errMsg{err}
		}
		if isDefaultStyle(id) {
			return infoMsg{"/output-style " + id, "Set output style to Default"}
		}
		return infoMsg{"/output-style " + id, "Set output style to " + id + "\nThe new style applies from your next message."}
	}
}

// openOutputStyles shows the picker.
func (m *Model) openOutputStyles(styles []*agentv1.OutputStyleInfo, active string) {
	m.choice = outputStylePicker(styles, active, m.setOutputStyle, func() tea.Cmd {
		help := newStyleHelp(m.st)
		return func() tea.Msg { return infoMsg{"/output-style", help} }
	})
}

// outputStylePicker builds the /output-style menu: one row per style (the active one marked ✔ and
// preselected, custom styles tagged with their source), then "New style…".
func outputStylePicker(styles []*agentv1.OutputStyleInfo, active string, set func(id string) tea.Cmd, newStyle func() tea.Cmd) *choicePrompt {
	p := &choicePrompt{title: "Select output style",
		subtitle: "How harness communicates. Pass a name to /output-style to skip this list.",
		hint:     "Enter to select · ↑/↓ to navigate · Esc to cancel"}
	ids := make([]string, 0, len(styles))
	for _, st := range styles {
		label := st.GetName()
		if label == "" {
			label = st.GetId()
		}
		if strings.EqualFold(st.GetId(), active) || (isDefaultStyle(active) && isDefaultStyle(st.GetId())) {
			label += " ✔"
			p.opts.sel = len(ids)
		}
		desc := st.GetDescription()
		if src := st.GetSource(); src != "" && src != "builtin" {
			desc = strings.TrimPrefix(desc+" · "+src, " · ")
		}
		p.options = append(p.options, render.Option{Label: label, Desc: desc})
		ids = append(ids, st.GetId())
	}
	p.options = append(p.options, render.Option{Label: newStyleLabel, Desc: "Create a style file in .harness/output-styles"})
	p.opts.n = len(p.options)
	p.pick = func(i int) tea.Cmd {
		if i == len(ids) {
			return newStyle()
		}
		return set(ids[i])
	}
	return p
}

// footerStyle is the style shown in the footer: nothing for the default.
func footerStyle(id string) string {
	if isDefaultStyle(id) {
		return ""
	}
	return id
}

// outputStyleLabel is the /status value.
func outputStyleLabel(id string) string {
	if isDefaultStyle(id) {
		return "default"
	}
	return id
}

// newStyleHelp tells where to create a style file, with a frontmatter template.
func newStyleHelp(s *render.Styles) string {
	var b strings.Builder
	b.WriteString("Create " + s.Bold.Render(".harness/output-styles/<name>.md") + " in the project (or ~/.harness/output-styles/ for all projects):\n\n")
	for _, l := range []string{
		"---",
		"name: <name>",
		"description: One line shown in this picker",
		"keep-coding-instructions: false",
		"---",
		"",
		"How harness should communicate: tone, length, structure.",
	} {
		b.WriteString(s.Sec.Render("  "+l) + "\n")
	}
	b.WriteString("\nThen run /output-style <name>. keep-coding-instructions: true keeps the built-in coding guidance.")
	return b.String()
}
