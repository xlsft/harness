package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// ToolState is the lifecycle of one tool call.
type ToolState int

const (
	ToolPending     ToolState = iota // announced by the model, not started (or waiting for approval)
	ToolRunning                      // executing
	ToolOK                           // finished successfully
	ToolError                        // finished with an error
	ToolRejected                     // denied by the user or a rule
	ToolInterrupted                  // cancelled by an interrupt
)

// Done reports whether the call has finished in any way.
func (s ToolState) Done() bool { return s >= ToolOK }

// ToolCall is the display state of one tool call.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
	State ToolState
	// Output is the text result; Summary the client's one-line summary ("Read 42 lines").
	Output  string
	Summary string
	// Path is the file argument relative to the session cwd (absolute when outside it); AbsPath
	// is shown in verbose mode.
	Path    string
	AbsPath string
	// Diff is set for Edit/Write once known (from the permission preview or the result).
	Diff *FileDiff
}

// Str returns a string input field.
func (c *ToolCall) Str(key string) string {
	v, _ := c.Input[key].(string)
	return v
}

// group kinds of read-only calls that are merged into one summary line (reference §4a).
const (
	kindSearch = iota
	kindRead
	kindList
	kindRun
	kindCount
)

// GroupKind returns the summary kind of a groupable tool, or -1 for tools rendered as their own
// block (Edit, Write and everything unknown).
func GroupKind(name string) int {
	switch name {
	case "Grep", "WebSearch":
		return kindSearch
	case "Read", "WebFetch":
		return kindRead
	case "Glob", "LS":
		return kindList
	case "Bash", "BashOutput", "TaskOutput":
		return kindRun
	}
	return -1
}

// Hidden reports tools that never get a transcript block (their effect is rendered elsewhere:
// the todo list, the plan prompt, the question prompt).
func Hidden(name string) bool {
	switch name {
	case "TodoWrite", "ExitPlanMode", "EnterPlanMode", "AskUserQuestion":
		return true
	}
	return false
}

var groupVerbs = [kindCount][4]string{
	// done singular/plural noun, running, done
	{"pattern", "patterns", "Searching for", "Searched for"},
	{"file", "files", "Reading", "Read"},
	{"directory", "directories", "Listing", "Listed"},
	{"shell command", "shell commands", "Running", "Ran"},
}

// GroupSummary renders "Searched for 1 pattern, read 2 files" (done) or "Searching for 1
// pattern, reading 2 files…" (running). Counts are bold.
func (s *Styles) GroupSummary(calls []*ToolCall, running bool) string {
	var counts [kindCount]int
	for _, c := range calls {
		if k := GroupKind(c.Name); k >= 0 {
			counts[k]++
		}
	}
	var parts []string
	for k, n := range counts {
		if n == 0 {
			continue
		}
		v := groupVerbs[k]
		verb := v[3]
		if running {
			verb = v[2]
		}
		if len(parts) > 0 {
			verb = strings.ToLower(verb[:1]) + verb[1:]
		}
		noun := v[0]
		if n != 1 {
			noun = v[1]
		}
		parts = append(parts, s.Sec.Render(verb+" ")+s.Sec.Bold(true).Render(itoa(n))+s.Sec.Render(" "+noun))
	}
	out := strings.Join(parts, s.Sec.Render(", "))
	failed, rejected := 0, 0
	for _, c := range calls {
		switch c.State {
		case ToolError:
			failed++
		case ToolRejected:
			rejected++
		}
	}
	if failed > 0 {
		out += s.Sec.Render(" "+s.dot()+" ") + s.Error.Render(itoa(failed)+" failed")
	}
	if rejected > 0 {
		out += s.Sec.Render(" "+s.dot()+" ") + s.Error.Render(itoa(rejected)+" rejected")
	}
	if running {
		out += s.Sec.Render(s.ellipsis())
	}
	return out
}

// bullet renders the leading cell of a tool line: the Pulse while running or pending, the
// settled ▰ colored by result when done.
func (s *Styles) bullet(state ToolState, frame int) string {
	switch state {
	case ToolPending, ToolRunning:
		return s.Env.Pulse(frame)
	case ToolOK:
		return s.Env.Bullet(true)
	}
	return s.Env.Bullet(false)
}

func groupState(calls []*ToolCall) (state ToolState) {
	state = ToolOK
	for _, c := range calls {
		if !c.State.Done() {
			return ToolRunning
		}
		if c.State == ToolError || c.State == ToolRejected {
			state = ToolError
		}
	}
	return state
}

// result renders lines hanging from the `  └  ` connector (text starts at column 5).
func (s *Styles) result(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		if i == 0 {
			b.WriteString("  " + s.Line.Render(s.connector()) + "  ")
		} else {
			b.WriteString("\n     ")
		}
		b.WriteString(l)
	}
	return b.String()
}

// detail is the most recent call's argument shown under a running group.
func (s *Styles) detail(c *ToolCall) string {
	switch c.Name {
	case "Bash":
		return "$ " + c.Str("command")
	case "Grep", "Glob":
		return c.Str("pattern")
	}
	if c.Path != "" {
		return c.Path
	}
	return c.Name
}

// ToolGroup renders consecutive read/search/shell calls. Collapsed: one summary line (plus the
// latest call while running). Verbose: every call as its own block with full output.
func (s *Styles) ToolGroup(calls []*ToolCall, width int, verbose bool, frame int) string {
	if verbose {
		var blocks []string
		for _, c := range calls {
			blocks = append(blocks, s.ToolBlock(c, width, true, frame))
		}
		return strings.Join(blocks, "\n\n")
	}
	st := groupState(calls)
	running := !st.Done()
	head := s.bullet(st, frame) + " " + s.GroupSummary(calls, running)
	head = Hang("", "  ", head, width)
	if !running {
		return head
	}
	latest := calls[len(calls)-1]
	for _, c := range calls {
		if c.State == ToolRunning {
			latest = c
		}
	}
	lines := Wrap(s.detail(latest), width-5)
	for i := range lines {
		lines[i] = s.Sec.Render(lines[i])
	}
	return head + "\n" + s.result(lines)
}

// DisplayName is the header name of a tool (Edit shows as Update, like the reference).
func DisplayName(name string) string {
	if name == "Edit" || name == "MultiEdit" {
		return "Update"
	}
	return name
}

// toolArg is the parenthesized argument in a tool header.
func (s *Styles) toolArg(c *ToolCall, verbose bool) string {
	if c.Path != "" {
		if verbose && c.AbsPath != "" {
			return c.AbsPath
		}
		return c.Path
	}
	switch c.Name {
	case "Bash":
		return c.Str("command")
	case "Grep":
		return "pattern: \"" + c.Str("pattern") + "\""
	case "Glob":
		return c.Str("pattern")
	case "WebFetch":
		return c.Str("url")
	case "WebSearch":
		return c.Str("query")
	case "Task", "Agent":
		return c.Str("description")
	case "TaskStop", "TaskOutput", "BashOutput":
		if id := c.Str("task_id"); id != "" {
			return id
		}
		return c.Str("bash_id")
	}
	return ""
}

// ToolHeader renders `▰ Name(arg)` with the bullet for the call's state.
func (s *Styles) ToolHeader(c *ToolCall, width int, verbose bool, frame int) string {
	name := s.Bold.Render(DisplayName(c.Name))
	arg := s.toolArg(c, verbose)
	if arg != "" {
		arg = "(" + oneLine(arg) + ")"
	}
	return Hang(s.bullet(c.State, frame)+" "+name, strings.Repeat(" ", 2), arg, width-ansi.StringWidth(DisplayName(c.Name)))
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// OutputLines is the number of result lines shown in the collapsed view of a generic tool.
const OutputLines = 3

// ToolBlock renders one call as a block: header, a summary or error under `└`, and the diff or
// output. Edit/Write are always blocks; other calls only in verbose mode.
func (s *Styles) ToolBlock(c *ToolCall, width int, verbose bool, frame int) string {
	head := s.ToolHeader(c, width, verbose, frame)
	if !c.State.Done() {
		return head
	}
	var body []string
	switch {
	case c.State == ToolInterrupted:
		body = []string{s.Sec.Render("Interrupted")}
	case c.State == ToolRejected:
		body = []string{s.Sec.Render(s.rejectText(c))}
		body = append(body, s.diffLines(c, width)...)
	case c.State == ToolError:
		lines := Wrap(strings.TrimSpace(c.Output), width-5)
		if !verbose && len(lines) > OutputLines {
			lines = append(lines[:OutputLines], s.Dim.Render(s.ellipsis()+" +"+plural(len(lines)-OutputLines, "line", "lines")+" (ctrl+o to expand)"))
		}
		for i := range lines {
			lines[i] = s.Error.Render(lines[i])
		}
		body = lines
	case c.Name == "Edit" || c.Name == "MultiEdit" || c.Name == "Write" || c.Name == "NotebookEdit" || c.Name == "DocumentEdit":
		body = append([]string{s.editSummary(c)}, s.diffLines(c, width)...)
	default:
		body = s.outputLines(c, width, verbose)
	}
	if len(body) == 0 {
		return head
	}
	return head + "\n" + s.result(body)
}

func (s *Styles) rejectText(c *ToolCall) string {
	switch c.Name {
	case "Edit", "MultiEdit":
		return "User rejected update to " + c.Path
	case "Write":
		return "User rejected write to " + c.Path
	case "Bash":
		return "User rejected the command"
	}
	return "User rejected " + DisplayName(c.Name)
}

// editSummary is "Added 1 line, removed 1 line" or "Wrote 30 lines to notes.txt" (counts bold).
func (s *Styles) editSummary(c *ToolCall) string {
	d := c.Diff
	if d == nil {
		if c.Summary != "" {
			return s.Sec.Render(c.Summary)
		}
		return s.Sec.Render("Done")
	}
	b := func(n int) string { return s.Bold.Render(itoa(n)) }
	if d.Created {
		return s.Sec.Render("Wrote ") + b(len(d.Lines)) + s.Sec.Render(" "+pluralNoun(len(d.Lines), "line", "lines")+" to ") + s.Bold.Render(c.Path)
	}
	var parts []string
	if d.Added > 0 {
		parts = append(parts, s.Sec.Render("Added ")+b(d.Added)+s.Sec.Render(" "+pluralNoun(d.Added, "line", "lines")))
	}
	if d.Removed > 0 {
		verb := "Removed "
		if len(parts) > 0 {
			verb = "removed "
		}
		parts = append(parts, s.Sec.Render(verb)+b(d.Removed)+s.Sec.Render(" "+pluralNoun(d.Removed, "line", "lines")))
	}
	if len(parts) == 0 {
		return s.Sec.Render("No changes")
	}
	return strings.Join(parts, s.Sec.Render(", "))
}

func pluralNoun(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// diffLines renders the diff body under the summary, starting at column 6 (the connector puts
// the first line at column 5, so the body is indented by one more cell relative to it).
func (s *Styles) diffLines(c *ToolCall, width int) []string {
	if c.Diff == nil {
		return nil
	}
	var lines []string
	if c.Diff.Created {
		lines = s.WritePreview(c.Diff, "", width-5, 10)
	} else {
		lines = s.DiffBody(c.Diff, "", width-5)
	}
	return lines
}

// outputLines renders a generic result: the client's summary when collapsed, the full output in
// verbose mode.
func (s *Styles) outputLines(c *ToolCall, width int, verbose bool) []string {
	out := strings.TrimRight(expandTabs(c.Output), "\n")
	if c.Name == "Read" && !verbose {
		if c.Summary != "" {
			return []string{s.Sec.Render(c.Summary)}
		}
		return []string{s.Sec.Render(fmt.Sprintf("Read %s", plural(strings.Count(out, "\n")+1, "line", "lines")))}
	}
	if out == "" {
		if c.Summary != "" {
			return []string{s.Sec.Render(c.Summary)}
		}
		return []string{s.Dim.Render("(No output)")}
	}
	lines := Wrap(out, width-5)
	if !verbose && len(lines) > OutputLines {
		extra := len(lines) - OutputLines
		lines = append(lines[:OutputLines], s.Dim.Render(s.ellipsis()+" +"+plural(extra, "line", "lines")+" (ctrl+o to expand)"))
	}
	for i := range lines {
		lines[i] = s.Sec.Render(lines[i])
	}
	return lines
}
