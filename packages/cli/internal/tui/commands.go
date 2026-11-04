package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"

	"github.com/xlsft/harness/packages/cli/internal/render"
)

// command is a local slash command.
type command struct {
	name, desc string
	run        func(m *Model, args string) tea.Cmd
}

var commands []command

func init() {
	commands = []command{
		{"/help", "Show commands and keyboard shortcuts", (*Model).cmdHelp},
		{"/clear", "Clear the conversation and start a new session", (*Model).cmdClear},
		{"/compact", "Compact the conversation (optional focus instructions)", (*Model).cmdCompact},
		{"/model", "Switch the model for this session", (*Model).cmdModel},
		{"/output-style", "Set the output style (how harness communicates)", (*Model).cmdOutputStyle},
		{"/rewind", "Restore the code and/or conversation to an earlier point", func(m *Model, _ string) tea.Cmd { return m.rewindCmd() }},
		{"/status", "Show session, model, account and context usage", (*Model).cmdStatus},
		{"/tasks", "List background tasks", (*Model).cmdTasks},
		{"/proposals", "List sessions the agent proposed; start or dismiss <id>", (*Model).cmdProposals},
		{"/exit", "Exit harness", func(m *Model, _ string) tea.Cmd { return m.quit() }},
	}
}

// runCommand runs a slash command; ok is false for unknown commands (sent to the model as text).
func (m *Model) runCommand(text string) (tea.Cmd, bool) {
	name, args, _ := strings.Cut(text, " ")
	if name == "/quit" {
		name = "/exit"
	}
	for _, c := range commands {
		if c.name == name {
			return c.run(m, strings.TrimSpace(args)), true
		}
	}
	return nil, false
}

func (m *Model) command(cmd, out string) tea.Cmd {
	m.closeOpen()
	m.push(&item{kind: itemCommand, text: cmd, output: out})
	return nil
}

func (m *Model) cmdHelp(string) tea.Cmd {
	var b strings.Builder
	b.WriteString(m.st.Bold.Render("Commands") + "\n")
	for _, c := range commands {
		b.WriteString(render.PadRight(m.st.Accent.Render(c.name), 14) + "  " + m.st.Sec.Render(c.desc) + "\n")
	}
	b.WriteString("\n" + m.st.Bold.Render("Shortcuts") + "\n")
	for _, kv := range [][2]string{
		{"enter", "send (queued while a turn runs)"},
		{"\\ enter, shift+enter", "new line"},
		{"esc", "interrupt the turn · esc esc to rewind"},
		{"shift+tab", "cycle permission modes"},
		{"ctrl+o", "toggle verbose output"},
		{"ctrl+t", "toggle the task list"},
		{"↑ / ↓", "history"},
		{"@", "mention a file"},
		{"ctrl+c twice", "exit"},
	} {
		b.WriteString(render.PadRight(kv[0], 22) + m.st.Sec.Render(kv[1]) + "\n")
	}
	return m.command("/help", b.String())
}

func (m *Model) cmdClear(string) tea.Cmd {
	if m.busy {
		return m.command("/clear", m.st.Warning.Render("Wait for the turn to finish (or press esc) before clearing."))
	}
	ctx := m.ctx
	m.printed, m.pending = []*item{{kind: itemBanner}}, nil
	m.todos, m.queued, m.cost, m.ctxPct = nil, nil, 0, -1
	return tea.Sequence(m.reprint(), func() tea.Msg {
		if err := m.conn.NewSession(ctx, ""); err != nil {
			return errMsg{err}
		}
		return sessionMsg{}
	})
}

func (m *Model) cmdCompact(args string) tea.Cmd {
	m.command("/compact", m.st.Sec.Render("Compacting the conversation…"))
	ctx := m.ctx
	m.startTurn()
	m.status = "Compacting"
	return func() tea.Msg {
		err := m.conn.Compact(ctx, args)
		program.Send(eventMsg{&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: &agentv1.TurnFinished{Result: agentv1.TurnFinished_RESULT_SUCCESS}}}})
		if err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m *Model) cmdModel(args string) tea.Cmd {
	set := func(id string) tea.Cmd {
		ctx := m.ctx
		return func() tea.Msg {
			if err := m.conn.SetModel(ctx, id); err != nil {
				return errMsg{err}
			}
			return infoMsg{"/model " + id, "Set model to " + m.conn.DisplayName(id) + " (" + id + ")"}
		}
	}
	if args != "" {
		return set(args)
	}
	ids := append([]string(nil), m.conn.ModelIDs...)
	if len(ids) == 0 {
		return m.command("/model", "No models available from the server.")
	}
	p := &choicePrompt{title: "Select model", subtitle: "Switch the model for this session. Pass an id to /model to skip this list.",
		hint: "Enter to select · ↑/↓ to navigate · Esc to cancel", opts: optionList{n: len(ids)}}
	for i, id := range ids {
		label := id
		if id == m.model {
			label += " ✔"
			p.opts.sel = i
		}
		desc := ""
		if dn := m.conn.DisplayName(id); dn != id {
			desc = dn
		}
		if w := m.conn.ContextWindow(id); w > 0 {
			desc = strings.TrimPrefix(desc+" · "+render.Tokens(w)+" context", " · ")
		}
		p.options = append(p.options, render.Option{Label: label, Desc: desc})
	}
	p.pick = func(i int) tea.Cmd { return set(ids[i]) }
	m.choice = p
	return nil
}

func (m *Model) cmdStatus(string) tea.Cmd {
	s := m.conn.Session()
	ctxText := "unknown"
	if m.ctxPct >= 0 {
		ctxText = fmt.Sprintf("%d%% of %s tokens", m.ctxPct, render.Tokens(m.conn.ContextWindow(m.model)))
	}
	mode := map[agentv1.PermissionMode]string{
		agentv1.PermissionMode_PERMISSION_MODE_DEFAULT: "manual", agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS: "accept edits",
		agentv1.PermissionMode_PERMISSION_MODE_PLAN: "plan", agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS: "bypass permissions",
		agentv1.PermissionMode_PERMISSION_MODE_DONT_ASK: "don't ask", agentv1.PermissionMode_PERMISSION_MODE_AUTO: "auto",
	}[m.mode]
	rows := [][2]string{
		{"Version", m.conn.Version},
		{"Session", s.GetId()},
		{"cwd", m.cwd},
		{"Model", m.conn.DisplayName(m.model) + " (" + m.model + ")"},
		{"Mode", mode},
		{"Output style", outputStyleLabel(m.style)},
		{"Account", m.conn.Email},
		{"Context", ctxText},
		{"Cost", render.Cost(m.cost)},
	}
	if m.branch != "" {
		rows = append(rows[:3], append([][2]string{{"Branch", m.branch}}, rows[3:]...)...)
	}
	return m.command("/status", m.st.Info(rows, m.width-5))
}

func (m *Model) cmdTasks(string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		ts, err := m.conn.Tasks(ctx)
		if err != nil {
			return errMsg{err}
		}
		if len(ts) == 0 {
			return infoMsg{"/tasks", "No background tasks."}
		}
		var b strings.Builder
		for _, t := range ts {
			state := "running"
			if !t.GetRunning() {
				state = fmt.Sprintf("exited %d", t.GetExitCode())
			}
			fmt.Fprintf(&b, "%s  %s  %s\n", t.GetTaskId(), state, t.GetCommand())
		}
		return infoMsg{"/tasks", b.String()}
	}
}

// cmdProposals lists the waiting proposals of this session, or starts or dismisses one.
func (m *Model) cmdProposals(args string) tea.Cmd {
	verb, id, _ := strings.Cut(args, " ")
	id = strings.TrimSpace(id)
	if verb == "" {
		var b strings.Builder
		for _, p := range m.proposals {
			if p.GetState() == agentv1.SessionProposal_STATE_PENDING {
				fmt.Fprintf(&b, "%s  %s\n", p.GetId(), p.GetTitle())
			}
		}
		if b.Len() == 0 {
			return m.command("/proposals", "No proposals are waiting.")
		}
		return m.command("/proposals", b.String()+"Use /proposals start <id> or /proposals dismiss <id>.")
	}
	if (verb != "start" && verb != "dismiss") || id == "" {
		return m.command("/proposals", "Use /proposals, /proposals start <id> or /proposals dismiss <id>.")
	}
	ctx := m.ctx
	return func() tea.Msg {
		if err := m.conn.ResolveProposal(ctx, id, verb == "start"); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// ---- rewind (Esc Esc on an empty idle input, /rewind) ----

func (m *Model) rewindCmd() tea.Cmd {
	if m.busy {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		cps, err := m.conn.Checkpoints(ctx)
		if err != nil {
			return errMsg{err}
		}
		return checkpointsMsg{cps}
	}
}

func (m *Model) openRewind(cps []*agentv1.Checkpoint) {
	if len(cps) == 0 {
		m.command("/rewind", "Nothing to rewind to yet.")
		return
	}
	p := &choicePrompt{title: "Rewind", question: "Restore the code and/or conversation to the point before…",
		hint: "Enter to continue · Esc to cancel", opts: optionList{n: len(cps)}}
	p.opts.sel = len(cps) - 1
	for _, c := range cps {
		desc := "No code changes"
		if c.GetHasCodeChanges() {
			desc = "Code changed after this point"
		}
		if t, err := time.Parse(time.RFC3339, c.GetCreatedAt()); err == nil {
			desc += " · " + render.Duration(time.Since(t)) + " ago"
		}
		p.options = append(p.options, render.Option{Label: m.st.Truncate(strings.ReplaceAll(c.GetPromptPreview(), "\n", " "), m.width-14), Desc: desc})
	}
	p.pick = func(i int) tea.Cmd { m.confirmRewind(cps[i]); return nil }
	m.choice = p
}

func (m *Model) confirmRewind(cp *agentv1.Checkpoint) {
	type opt struct {
		label string
		mode  agentv1.RewindRequest_Mode
	}
	var opts []opt
	if cp.GetHasCodeChanges() {
		opts = append(opts, opt{"Restore code and conversation", agentv1.RewindRequest_MODE_CODE_AND_CONVERSATION})
	}
	opts = append(opts, opt{"Restore conversation", agentv1.RewindRequest_MODE_CONVERSATION})
	if cp.GetHasCodeChanges() {
		opts = append(opts, opt{"Restore code", agentv1.RewindRequest_MODE_CODE})
	}
	opts = append(opts, opt{"Summarize from here", agentv1.RewindRequest_MODE_SUMMARIZE_FROM}, opt{"Summarize up to here", agentv1.RewindRequest_MODE_SUMMARIZE_UP_TO})
	p := &choicePrompt{title: "Rewind", question: "Confirm you want to restore to the point before you sent this message:\n\n│ " + cp.GetPromptPreview(),
		hint: "Rewinding does not affect files edited manually or via bash.", opts: optionList{n: len(opts)}}
	for _, o := range opts {
		p.options = append(p.options, render.Option{Label: o.label})
	}
	p.pick = func(i int) tea.Cmd {
		ctx, mode := m.ctx, opts[i].mode
		return func() tea.Msg {
			if _, err := m.conn.Rewind(ctx, cp.GetId(), mode); err != nil {
				return errMsg{err}
			}
			return nil // the Rewound event updates the transcript
		}
	}
	m.choice = p
}

// ---- menus ----

type menuKind int

const (
	menuSlash menuKind = iota
	menuFiles
)

type menu struct {
	kind  menuKind
	items []render.MenuItem
	sel   int
	from  int // rune offset where the @token starts
	query string
}

func (mn *menu) move(d int) {
	if len(mn.items) == 0 {
		return
	}
	mn.sel = (mn.sel + d + len(mn.items)) % len(mn.items)
}

func (mn *menu) setFiles(paths []string) {
	mn.items = nil
	for _, p := range paths {
		mn.items = append(mn.items, render.MenuItem{Name: "+ " + p, Match: shift(fuzzyMatch(p, mn.query), 2)})
	}
	if mn.sel >= len(mn.items) {
		mn.sel = 0
	}
}

func shift(idx []int, n int) []int {
	for i := range idx {
		idx[i] += n
	}
	return idx
}

// updateMenu opens, filters or closes the popup menu after an input change.
func (m *Model) updateMenu() {
	text := m.input.String()
	if strings.HasPrefix(text, "/") && !strings.ContainsAny(text, " \n") {
		mn := m.menu
		if mn == nil || mn.kind != menuSlash {
			mn = &menu{kind: menuSlash}
		}
		mn.query = text
		mn.items = nil
		type scored struct {
			it    render.MenuItem
			score int
		}
		var ss []scored
		for _, c := range commands {
			idx := fuzzyMatch(c.name, text)
			if idx == nil {
				continue
			}
			score := idx[len(idx)-1] - idx[0]
			if strings.HasPrefix(c.name, text) {
				score -= 100
			}
			ss = append(ss, scored{render.MenuItem{Name: c.name, Desc: c.desc, Match: idx}, score})
		}
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].score < ss[j].score })
		for _, s := range ss {
			mn.items = append(mn.items, s.it)
		}
		if mn.sel >= len(mn.items) {
			mn.sel = 0
		}
		m.menu = mn
		return
	}
	word, from := m.input.WordBefore()
	if strings.HasPrefix(word, "@") {
		mn := m.menu
		if mn == nil || mn.kind != menuFiles {
			mn = &menu{kind: menuFiles}
		}
		mn.from, mn.query = from, strings.TrimPrefix(word, "@")
		m.menu = mn
		return
	}
	m.menu = nil
}

// menuCmd asks the client for file suggestions when the @ menu is open.
func (m *Model) menuCmd() tea.Cmd {
	if m.menu == nil || m.menu.kind != menuFiles {
		return nil
	}
	m.suggestSeq++
	seq, q, ctx := m.suggestSeq, m.menu.query, m.ctx
	return func() tea.Msg {
		paths, err := m.conn.SuggestFiles(ctx, q, 20)
		if err != nil {
			return nil
		}
		return suggestMsg{seq, paths}
	}
}

// completeMenu inserts the selected entry into the input.
func (m *Model) completeMenu(accept bool) {
	mn := m.menu
	if mn == nil || len(mn.items) == 0 {
		return
	}
	name := mn.items[mn.sel].Name
	if mn.kind == menuSlash {
		m.input.Set(name + " ")
		m.menu = nil
		return
	}
	path := strings.TrimPrefix(name, "+ ")
	m.input.ReplaceRange(mn.from, "@"+path+" ")
	m.menu = nil
	_ = accept
}

// fuzzyMatch returns the rune offsets of query's runes found in order in s (case-insensitive),
// or nil when they are not all present.
func fuzzyMatch(s, query string) []int {
	rs := []rune(s)
	var idx []int
	j := 0
	for _, q := range query {
		q = unicode.ToLower(q)
		for j < len(rs) && unicode.ToLower(rs[j]) != q {
			j++
		}
		if j == len(rs) {
			return nil
		}
		idx = append(idx, j)
		j++
	}
	if idx == nil {
		return []int{}
	}
	return idx
}
