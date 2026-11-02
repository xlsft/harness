package render

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/xlsft/harness/packages/cli/brand"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// noColor is the environment of every golden test: attributes only (bold, dim, reverse).
func noColor() *Styles { return New(brand.Env{Depth: brand.DepthNone, Unicode: true}) }

// golden compares got with testdata/<name>.golden. The files keep the SGR attributes (bold, dim,
// reverse); `cat` them in a terminal to see the rendering.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got+"\n" {
		t.Errorf("%s mismatch\n--- want\n%s\n--- got\n%s", name, ansi.Strip(string(want)), ansi.Strip(got))
	}
}

// maxWidth fails when a rendered line is wider than width.
func maxWidth(t *testing.T, name, s string, width int) {
	t.Helper()
	for i, l := range strings.Split(s, "\n") {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("%s: line %d is %d cells wide (max %d): %q", name, i+1, w, width, ansi.Strip(l))
		}
	}
}

func check(t *testing.T, name, got string, width int) {
	t.Helper()
	maxWidth(t, name, got, width)
	golden(t, name, got)
}

func TestTranscriptItems(t *testing.T) {
	s := noColor()
	for _, w := range []int{80, 120} {
		sfx := "_" + itoa(w)
		check(t, "user_prompt"+sfx, s.UserPrompt("Use the Write tool to create notes.txt containing lines \"line 1\" through \"line 30\". Then run it with Bash and summarize what happened in one paragraph.", w), w)
		md := "# Release notes\n\nThe **parser** now handles `--flag=value` forms.\n\n- Faster startup\n- Fewer allocations\n  - in the lexer\n\n1. Build\n2. Test\n\n```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n\n> Breaking: the old API is gone.\n\nSee [the docs](https://example.com/docs)."
		check(t, "assistant_markdown"+sfx, s.Assistant(md, w, true), w)
		check(t, "assistant_continuation"+sfx, s.Assistant("A second paragraph streamed after the first one.", w, false), w)
	}
	check(t, "turn_done", s.TurnDone(75*time.Second), 80)
	check(t, "interrupted", s.Interrupted(), 80)
	check(t, "error_message", s.ErrorMessage("There's an issue with the selected model (bogus-model). It may not exist or you may not have access to it. Run /model to pick a different model.", 80), 80)
	check(t, "notice_warning", s.Notice(1, "permission rule not saved: settings.local.json is read-only", 80), 80)
	check(t, "command_output", s.CommandOutput("/status", s.Info([][2]string{{"Model", "Qwen 3.7 Flash (qwen-flash)"}, {"Mode", "manual"}, {"cwd", "/Users/someone/Projects/a-rather-long-directory-name/with/several/levels/of/nesting/api"}}, 75), 80), 80)
	check(t, "queued", s.Queued([]string{"then a haiku", "and a limerick about terminals that is long enough to need truncation at eighty columns"}, 80), 80)
}

func groupCalls(state ToolState) []*ToolCall {
	return []*ToolCall{
		{ID: "1", Name: "Grep", Input: map[string]any{"pattern": "func Add"}, State: ToolOK, Output: "calc.go:4:func Add(a, b int) int {"},
		{ID: "2", Name: "Read", Input: map[string]any{"file_path": "/w/calc.go"}, Path: "calc.go", AbsPath: "/w/calc.go", State: ToolOK, Output: "1\tpackage demo\n2\t\n3\tfunc Add() {}", Summary: "Read 3 lines"},
		{ID: "3", Name: "Read", Input: map[string]any{"file_path": "/w/README.md"}, Path: "README.md", AbsPath: "/w/README.md", State: ToolOK, Output: "# demo"},
		{ID: "4", Name: "Bash", Input: map[string]any{"command": "go test ./..."}, State: state, Output: "ok  \tdemo\t0.4s"},
	}
}

func TestToolGroup(t *testing.T) {
	s := noColor()
	check(t, "group_running", s.ToolGroup(groupCalls(ToolRunning), 80, false, 0), 80)
	check(t, "group_done", s.ToolGroup(groupCalls(ToolOK), 80, false, 0), 80)
	check(t, "group_failed", s.ToolGroup(groupCalls(ToolError), 80, false, 0), 80)
	check(t, "group_verbose", s.ToolGroup(groupCalls(ToolOK), 80, true, 0), 80)
	long := []*ToolCall{{ID: "1", Name: "Bash", State: ToolRunning, Input: map[string]any{"command": "cd /private/tmp/a-very-long-scratch-directory-name/that/keeps/going/and/going/proj && python3 -m unittest -v"}}}
	check(t, "group_long_command", s.ToolGroup(long, 80, false, 0), 80)
}

var calcPatch = []PatchHunk{{OldStart: 2, OldLines: 5, NewStart: 2, NewLines: 5, Lines: []string{
	" ", " // Add returns the sum of a and b.", " func Add(a, b int) int {", "-\treturn a - b", "+\treturn a + b", " }",
}}}

func TestEditBlocks(t *testing.T) {
	s := noColor()
	edit := &ToolCall{ID: "e", Name: "Edit", Path: "calc.go", AbsPath: "/w/calc.go", State: ToolOK, Diff: DiffFromPatch("calc.go", calcPatch)}
	check(t, "edit_done_80", s.ToolBlock(edit, 80, false, 0), 80)
	check(t, "edit_done_verbose", s.ToolBlock(edit, 80, true, 0), 80)
	pending := *edit
	pending.State = ToolPending
	check(t, "edit_pending", s.ToolBlock(&pending, 80, false, 0), 80)
	rejected := *edit
	rejected.State = ToolRejected
	check(t, "edit_rejected", s.ToolBlock(&rejected, 80, false, 0), 80)
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, "line "+itoa(i))
	}
	write := &ToolCall{ID: "w", Name: "Write", Path: "notes.txt", State: ToolOK, Diff: WriteDiff("notes.txt", strings.Join(lines, "\n")+"\n")}
	check(t, "write_done", s.ToolBlock(write, 80, false, 0), 80)
	failed := &ToolCall{ID: "f", Name: "Edit", Path: "calc.go", State: ToolError, Output: "String to replace not found in file.\nString: return a * b"}
	check(t, "edit_error", s.ToolBlock(failed, 80, false, 0), 80)
	multi := DiffFromPatch("x.go", []PatchHunk{
		{OldStart: 1, NewStart: 1, Lines: []string{" a", "-b", "+B"}},
		{OldStart: 120, NewStart: 120, Lines: []string{" y", "+z"}},
	})
	check(t, "diff_two_hunks", strings.Join(s.DiffBody(multi, "     ", 80), "\n"), 80)
}

func TestPermissionBox(t *testing.T) {
	s := noColor()
	for _, w := range []int{80, 120} {
		sfx := "_" + itoa(w)
		bash := Prompt{
			Title: "Bash command", Body: s.CommandPreview("python3 -m pytest test_calc.py -v", "Run the tests to verify the fix", w),
			Question: "Do you want to proceed?",
			Options:  []Option{{Label: "Yes"}, {Label: "Yes, and don’t ask again for Bash(python3 *)"}, {Label: "No, and tell harness what to do differently", Key: "(esc)"}},
			Hint:     "Esc to cancel · Tab to add a comment",
		}
		check(t, "perm_bash"+sfx, s.PromptBox(bash, w), w)
		edit := Prompt{
			Title: "Edit file", Subtitle: "calc.go", Dashed: true, Body: s.DiffBody(DiffFromPatch("calc.go", calcPatch), "", w-4),
			Question: "Do you want to make this edit to " + s.Bold.Render("calc.go") + "?",
			Options:  []Option{{Label: "Yes"}, {Label: "Yes, allow all edits during this session", Key: "(shift+tab)"}, {Label: "No, and tell harness what to do differently", Key: "(esc)"}},
			Selected: 1, Hint: "Esc to cancel · Tab to add a comment",
		}
		check(t, "perm_edit"+sfx, s.PromptBox(edit, w), w)
	}
	comment := Prompt{
		Title: "Bash command", Body: s.CommandPreview("rm -rf build", "", 80), Question: "Do you want to proceed?",
		Options:  []Option{{Label: "Yes"}, {Label: "Yes, and don’t ask again for Bash(rm *)"}, {Label: "No", Placeholder: "tell harness what to do differently"}},
		Selected: 2, Commenting: true, Comment: []rune("keep the build dir"), CommentAt: 18, Hint: "Enter to confirm · Esc to discard the comment",
	}
	check(t, "perm_comment", s.PromptBox(comment, 80), 80)
}

func TestFooter(t *testing.T) {
	s := noColor()
	for _, w := range []int{80, 120} {
		sfx := "_" + itoa(w)
		check(t, "footer_default"+sfx, s.FooterLine(Footer{Mode: ModeDefault, Model: "qwen-flash", ContextPct: 12, Cost: 0.0123}, w), w)
		check(t, "footer_accept"+sfx, s.FooterLine(Footer{Mode: ModeAcceptEdits, Model: "qwen-flash", ContextPct: -1, HasText: true}, w), w)
		check(t, "footer_plan_tasks"+sfx, s.FooterLine(Footer{Mode: ModePlan, Model: "glm-flash", ContextPct: 3, Tasks: 2}, w), w)
	}
	check(t, "shortcuts_120", s.Shortcuts(120), 120)
}

func TestDock(t *testing.T) {
	s := noColor()
	check(t, "working", s.WorkingLine(Working{Status: "Running Bash", Elapsed: 13 * time.Second, Tokens: 1130}, 2, 80), 80)
	check(t, "working_retry", s.WorkingLine(Working{Retry: &Retry{Error: "Unable to reach the model (overloaded)", In: 4 * time.Second, Attempt: 2, Max: 10}}, 0, 120), 120)
	in := s.InputLines([]rune("first line\nsecond line that is long enough to wrap around the right edge of an eighty column terminal"), 11, 80, "", "")
	check(t, "input_multiline", s.InputBox(in, 80, "\\ + enter for a new line"), 80)
	check(t, "input_placeholder", s.InputBox(s.InputLines(nil, 0, 80, "Messages are sent after the current tool calls", ""), 80, ""), 80)
	items := []MenuItem{
		{Name: "/help", Desc: "Show commands and keyboard shortcuts", Match: []int{0}},
		{Name: "/clear", Desc: "Clear the conversation and start a new session", Match: []int{0}},
		{Name: "/compact", Desc: "Compact the conversation (optional focus instructions)", Match: []int{0}},
		{Name: "/model", Desc: "Switch the model for this session", Match: []int{0}},
		{Name: "/rewind", Desc: "Restore the code and/or conversation to an earlier point", Match: []int{0}},
		{Name: "/status", Desc: "Show session, model, account and context usage", Match: []int{0}},
	}
	check(t, "menu_slash_120", s.Menu(items, 1, 120), 120)
	check(t, "menu_slash_scrolled_80", s.Menu(items, 5, 80), 80)
	check(t, "menu_files", s.Menu([]MenuItem{{Name: "+ calc.go", Match: []int{2, 3}}, {Name: "+ calc_test.go", Match: []int{2, 3}}}, 0, 80), 80)
	check(t, "todos", s.TodoList([]Todo{{Content: "Fix add bug", Status: 2}, {Content: "Run tests", Status: 1}, {Content: "Update README", Status: 0}}, 80), 80)
}

func TestWelcome(t *testing.T) {
	s := noColor()
	w := Welcome{Version: "v0.1.0", Model: "Qwen 3.7 Flash", User: "dev@example.com", Cwd: "/work/acme/api", Branch: "main"}
	check(t, "welcome_120", s.WelcomeBanner(w, 120), 120)
	check(t, "welcome_50", s.WelcomeBanner(w, 50), 50)
}

func TestColorDepthsStayWithinWidth(t *testing.T) {
	for _, d := range []brand.Depth{brand.DepthTrue, brand.Depth256, brand.Depth16} {
		for _, th := range []brand.Theme{brand.ThemeDark, brand.ThemeLight} {
			s := New(brand.Env{Depth: d, Theme: th, Unicode: true})
			maxWidth(t, "user", s.UserPrompt("hello there", 60), 60)
			maxWidth(t, "diff", strings.Join(s.DiffBody(DiffFromPatch("c", calcPatch), "     ", 60), "\n"), 60)
			maxWidth(t, "footer", s.FooterLine(Footer{Mode: ModeAcceptEdits, Model: "m", ContextPct: 5}, 60), 60)
		}
	}
}

func TestASCIIFallback(t *testing.T) {
	s := New(brand.Env{Depth: brand.DepthNone, Unicode: false})
	check(t, "ascii_group", s.ToolGroup(groupCalls(ToolOK), 80, false, 0)+"\n"+s.FooterLine(Footer{Mode: ModeAcceptEdits, ContextPct: -1}, 80), 80)
}
