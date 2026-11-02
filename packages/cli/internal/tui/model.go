// Package tui is the interactive terminal UI: a Bubble Tea v2 program in inline mode. Finished
// transcript items are printed above the program into the terminal's scrollback (tea.Println);
// the program's own view is only the live dock: in-progress items, the working line, menus, the
// input or a modal prompt, and the footer.
//
// Files: model.go (state, transcript items), events.go (harness.agent.v1 session events →
// state), keys.go (input handling), commands.go (slash commands), view.go (the dock).
package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"

	"github.com/xlsft/harness/packages/cli/brand"
	"github.com/xlsft/harness/packages/cli/internal/client"
	"github.com/xlsft/harness/packages/cli/internal/render"
)

// itemKind enumerates transcript items.
type itemKind int

const (
	itemBanner itemKind = iota
	itemUser
	itemText
	itemGroup
	itemBlock
	itemDone
	itemInterrupted
	itemError
	itemNotice
	itemCommand
)

// item is one transcript entry. Items are kept after printing so the whole transcript can be
// re-rendered on resize and when verbose mode is toggled.
type item struct {
	kind   itemKind
	text   string // markdown, prompt, error or notice text; command for itemCommand
	output string // itemCommand output
	first  bool   // itemText: carries the bullet
	level  int    // itemNotice level
	calls  []*render.ToolCall
	dur    time.Duration
	closed bool // itemGroup: no more calls will join; itemText: complete
}

// done reports whether the item is final and can be printed.
func (it *item) done() bool {
	switch it.kind {
	case itemText:
		return it.closed
	case itemGroup:
		if !it.closed {
			return false
		}
		fallthrough
	case itemBlock:
		for _, c := range it.calls {
			if !c.State.Done() {
				return false
			}
		}
	}
	return true
}

func (m *Model) renderItem(it *item, width, frame int) string {
	s := m.st
	switch it.kind {
	case itemBanner:
		return s.WelcomeBanner(m.welcome(), width)
	case itemUser:
		return s.UserPrompt(it.text, width)
	case itemText:
		return s.Assistant(it.text, width, it.first)
	case itemGroup:
		return s.ToolGroup(it.calls, width, m.verbose, frame)
	case itemBlock:
		return s.ToolBlock(it.calls[0], width, m.verbose, frame)
	case itemDone:
		return s.TurnDone(it.dur)
	case itemInterrupted:
		return s.Interrupted()
	case itemError:
		return s.ErrorMessage(it.text, width)
	case itemNotice:
		return s.Notice(it.level, it.text, width)
	case itemCommand:
		return s.CommandOutput(it.text, it.output, width)
	}
	return ""
}

// joinItem reports whether it hangs directly under the previous item without a blank line.
func joinItem(it *item) bool {
	return it.kind == itemInterrupted
}

// Options configure the TUI.
type Options struct {
	Conn  *client.Conn
	Env   brand.Env
	Cwd   string
	Model string
}

// Model is the Bubble Tea model.
type Model struct {
	conn *client.Conn
	st   *render.Styles
	ctx  context.Context
	cwd  string

	width, height int
	started       bool

	// Transcript: printed items, then pending (live) items in order.
	printed []*item
	pending []*item
	verbose bool

	// Turn state.
	busy       bool
	turnStart  time.Time
	turnTokens int
	liveChars  int
	status     string
	retry      *render.Retry
	retryAt    time.Time
	frame      int
	ticking    bool
	calls      map[string]*render.ToolCall
	rejected   map[string]bool
	queued     []string
	interrupts int

	// Input.
	input      editor
	history    []string
	histIdx    int
	draft      string
	menu       *menu
	escAt      time.Time
	ctrlCAt    time.Time
	hint       string
	hintUntil  time.Time
	killed     string
	shortcuts  bool
	suggestSeq int

	// Modals (at most one is shown; permissions queue up).
	perms    []*permPrompt
	question *questionPrompt
	plan     *planPrompt
	choice   *choicePrompt

	// Session info.
	mode      agentv1.PermissionMode
	model     string
	branch    string
	ctxPct    int
	cost      float64
	todos     []render.Todo
	showTodos bool
	tasks     map[string]bool // background task id → running

	quitting bool
}

// New creates the model.
func New(o Options) *Model {
	m := &Model{
		conn: o.Conn, st: render.New(o.Env), ctx: context.Background(), cwd: o.Cwd,
		calls: map[string]*render.ToolCall{}, rejected: map[string]bool{}, tasks: map[string]bool{},
		ctxPct: -1, showTodos: true, width: 80, height: 24,
	}
	if s := o.Conn.Session(); s != nil {
		m.mode, m.model, m.branch = s.GetPermissionMode(), s.GetModel(), s.GetGitBranch()
	}
	m.history = loadHistory()
	m.histIdx = len(m.history)
	return m
}

func (m *Model) welcome() render.Welcome {
	return render.Welcome{Version: "v" + strings.TrimPrefix(m.conn.Version, "v"), Model: m.conn.DisplayName(m.model), User: m.conn.Email, Cwd: m.cwd, Branch: m.branch}
}

// ---- messages ----

type eventMsg struct{ ev *agentv1.SessionEvent }
type subEndedMsg struct{ err error }
type tickMsg struct{}
type errMsg struct{ err error }
type suggestMsg struct {
	seq   int
	paths []string
}
type checkpointsMsg struct{ cps []*agentv1.Checkpoint }
type rewoundMsg struct{ r *agentv1.Rewound }
type sessionMsg struct{}
type repaintMsg struct{}
type infoMsg struct{ cmd, out string }

// Init subscribes to the session.
func (m *Model) Init() tea.Cmd {
	return m.subscribe()
}

// program is set by Run so subscription callbacks can deliver events.
var program *tea.Program

func (m *Model) subscribe() tea.Cmd {
	return func() tea.Msg {
		m.conn.Subscribe(m.ctx, func(ev *agentv1.SessionEvent) {
			program.Send(eventMsg{ev})
		}, func(err error) { program.Send(subEndedMsg{err}) })
		return nil
	}
}

func (m *Model) tick() tea.Cmd {
	if m.ticking {
		return nil
	}
	m.ticking = true
	return tea.Tick(brand.FrameInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// animating reports whether something on screen moves (rail, pulse, retry countdown).
func (m *Model) animating() bool {
	if m.busy {
		return true
	}
	for _, it := range m.pending {
		if !it.done() {
			return true
		}
	}
	return false
}

// ---- transcript plumbing ----

// push appends a live item.
func (m *Model) push(it *item) { m.pending = append(m.pending, it) }

// flush prints the leading run of finished pending items above the program.
func (m *Model) flush() tea.Cmd {
	var out []string
	n := 0
	for n < len(m.pending) && m.pending[n].done() {
		it := m.pending[n]
		out = append(out, m.printText(it))
		m.printed = append(m.printed, it)
		n++
	}
	m.pending = m.pending[n:]
	if len(out) == 0 {
		return nil
	}
	return tea.Println(strings.Join(out, "\n"))
}

// printText renders an item for scrollback, with the blank separator line before it.
func (m *Model) printText(it *item) string {
	body := m.renderItem(it, m.width, 0)
	if len(m.printed) == 0 || joinItem(it) {
		return body
	}
	return "\n" + body
}

// reprint clears the screen and scrollback and prints the whole transcript again at the
// current width and verbosity (resize, ctrl+o).
func (m *Model) reprint() tea.Cmd {
	var out []string
	for i, it := range m.printed {
		body := m.renderItem(it, m.width, 0)
		if i > 0 && !joinItem(it) {
			body = "\n" + body
		}
		out = append(out, body)
	}
	// The clear sequences travel inside the printed text: Bubble Tea writes printed lines
	// immediately but defers its own clear to the next frame, so separate commands would run in
	// the wrong order. Afterwards the dock is repainted from scratch.
	clear := "\x1b[H\x1b[2J\x1b[3J"
	return tea.Sequence(tea.Println(clear+strings.Join(out, "\n")),
		tea.Tick(30*time.Millisecond, func(time.Time) tea.Msg { return repaintMsg{} }))
}

// lastOpen returns the last pending item when it is still open and of kind k.
func (m *Model) lastOpen(k itemKind) *item {
	if n := len(m.pending); n > 0 {
		if it := m.pending[n-1]; it.kind == k && !it.closed {
			return it
		}
	}
	return nil
}

// closeOpen closes the open text and group items (a different kind of item follows).
func (m *Model) closeOpen() {
	for _, it := range m.pending {
		if it.kind == itemText || it.kind == itemGroup {
			it.closed = true
		}
	}
}

// ---- tool calls ----

func (m *Model) newCall(id, name string, inputJSON string) *render.ToolCall {
	c := &render.ToolCall{ID: id, Name: name}
	_ = json.Unmarshal([]byte(inputJSON), &c.Input)
	if p, _ := c.Input["file_path"].(string); p != "" {
		c.AbsPath = p
		c.Path = m.rel(p)
	} else if p, _ := c.Input["notebook_path"].(string); p != "" {
		c.AbsPath, c.Path = p, m.rel(p)
	}
	m.calls[id] = c
	return c
}

func (m *Model) rel(p string) string {
	if r, err := filepath.Rel(m.cwd, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// addCall places a tool call announced by the model: groupable calls join the open group,
// others get their own block.
func (m *Model) addCall(c *render.ToolCall) {
	if render.Hidden(c.Name) {
		return
	}
	if t := m.lastOpen(itemText); t != nil {
		t.closed = true
	}
	if render.GroupKind(c.Name) >= 0 {
		if g := m.lastOpen(itemGroup); g != nil {
			g.calls = append(g.calls, c)
			return
		}
		m.closeOpen()
		m.push(&item{kind: itemGroup, calls: []*render.ToolCall{c}})
		return
	}
	m.closeOpen()
	m.push(&item{kind: itemBlock, calls: []*render.ToolCall{c}})
}

// ---- footer data ----

func (m *Model) footerMode() render.Mode {
	switch m.mode {
	case agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS:
		return render.ModeAcceptEdits
	case agentv1.PermissionMode_PERMISSION_MODE_PLAN:
		return render.ModePlan
	case agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS:
		return render.ModeBypass
	case agentv1.PermissionMode_PERMISSION_MODE_DONT_ASK:
		return render.ModeDontAsk
	}
	return render.ModeDefault
}

func (m *Model) runningTasks() int {
	n := 0
	for _, r := range m.tasks {
		if r {
			n++
		}
	}
	return n
}

// ---- history ----

func historyFile() string {
	home := os.Getenv("HARNESS_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(h, ".harness")
	}
	return filepath.Join(home, "cli-history")
}

func loadHistory() []string {
	data, err := os.ReadFile(historyFile())
	if err != nil {
		return nil
	}
	var h []string
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if l != "" {
			h = append(h, strings.ReplaceAll(l, "\\n", "\n"))
		}
	}
	if len(h) > 500 {
		h = h[len(h)-500:]
	}
	return h
}

func appendHistory(s string) {
	p := historyFile()
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(strings.ReplaceAll(s, "\n", "\\n") + "\n")
}
