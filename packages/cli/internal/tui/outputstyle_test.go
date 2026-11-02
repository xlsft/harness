package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"

	"github.com/xlsft/harness/packages/cli/brand"
	"github.com/xlsft/harness/packages/cli/internal/render"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name>.golden (SGR attributes kept, no colors).
func golden(t *testing.T, name, got string, width int) {
	t.Helper()
	for i, l := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("%s: line %d is %d cells wide (max %d): %q", name, i+1, w, width, ansi.Strip(l))
		}
	}
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

func testStyles() []*agentv1.OutputStyleInfo {
	return []*agentv1.OutputStyleInfo{
		{Id: "default", Name: "Default", Description: "Completes coding tasks efficiently with concise responses", Source: "builtin"},
		{Id: "proactive", Name: "Proactive", Description: "Takes initiative on obvious next steps", Source: "builtin"},
		{Id: "concise", Name: "Concise", Description: "Short answers, no preamble", Source: "builtin"},
		{Id: "explanatory", Name: "Explanatory", Description: "Explains implementation choices and codebase patterns", Source: "builtin"},
		{Id: "learning", Name: "Learning", Description: "Pauses and asks you to write small pieces of code", Source: "builtin"},
		{Id: "Terse", Name: "Terse", Description: "One-line answers", Source: "project"},
		{Id: "pirate", Name: "pirate", Source: "user"},
	}
}

func TestOutputStylePickerRender(t *testing.T) {
	s := render.New(brand.Env{Depth: brand.DepthNone, Unicode: true})
	m := &Model{st: s}
	noop := func(string) tea.Cmd { return nil }
	for _, w := range []int{80, 120} {
		p := outputStylePicker(testStyles(), "explanatory", noop, func() tea.Cmd { return nil })
		golden(t, "output_style_picker_"+itoa(w), s.PromptBox(m.choiceView(p), w), w)
	}
	// Default active, cursor moved to "New style…".
	p := outputStylePicker(testStyles(), "default", noop, func() tea.Cmd { return nil })
	if p.opts.sel != 0 {
		t.Fatalf("default must be preselected, got %d", p.opts.sel)
	}
	p.opts.sel = len(p.options) - 1
	golden(t, "output_style_picker_new", s.PromptBox(m.choiceView(p), 80), 80)
	golden(t, "output_style_new_help", s.CommandOutput("/output-style", newStyleHelp(s), 80), 80)
}

func TestOutputStylePickerPick(t *testing.T) {
	var picked string
	newCalled := false
	p := outputStylePicker(testStyles(), "Terse", func(id string) tea.Cmd { picked = id; return nil }, func() tea.Cmd { newCalled = true; return nil })
	if p.opts.sel != 5 || p.opts.n != 8 || !strings.HasSuffix(p.options[5].Label, "✔") || p.options[5].Desc != "One-line answers · project" || p.options[6].Desc != "user" {
		t.Fatalf("picker %+v sel %d n %d", p.options, p.opts.sel, p.opts.n)
	}
	p.pick(3)
	if picked != "explanatory" {
		t.Fatalf("picked %q", picked)
	}
	p.pick(7)
	if !newCalled {
		t.Fatal("New style… not wired")
	}
	if footerStyle("default") != "" || footerStyle("") != "" || footerStyle("learning") != "learning" {
		t.Fatal("footer shows only a non-default style")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
