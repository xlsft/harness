package render

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/xlsft/harness/packages/cli/brand"
)

const mdBlocks = "Steps:\n\n" +
	"- A first item that is long enough to wrap across the terminal width when it is rendered at eighty columns.\n" +
	"- Second item\n" +
	"  - A nested item that also wraps, because it keeps going on and on past the end of the line.\n\n" +
	"1. A numbered item that wraps around the limit too, since this sentence is deliberately long enough.\n" +
	"2. Short one\n\n" +
	"| Feature | Status | Notes |\n|---|:---:|---|\n" +
	"| **Tables** | done | box drawing, a rule between every row |\n" +
	"| Links | done | see [the docs](https://example.com/docs) |\n\n" +
	"> Quoted text stays italic.\n\n" +
	"See [the docs](https://example.com/docs) for more.\n\n" +
	"```diff\n- removed line that must not be re-wrapped\n  continuation\n+ added line\n```\n"

func TestMarkdownBlocks(t *testing.T) {
	s := noColor()
	for _, w := range []int{80, 120} {
		check(t, "markdown_blocks_"+strconv.Itoa(w), s.Markdown(mdBlocks, w), w)
	}
}

func TestMarkdownNoHyperlinks(t *testing.T) {
	for _, d := range []brand.Depth{brand.DepthNone, brand.DepthTrue} {
		out := New(brand.Env{Depth: d, Unicode: true}).Markdown("See [x](https://example.com).", 80)
		if strings.Contains(out, "\x1b]8;") {
			t.Errorf("depth %v: OSC 8 hyperlink left in %q", d, out)
		}
		if got := ansi.Strip(out); got != "See x (https://example.com)." {
			t.Errorf("depth %v: got %q", d, got)
		}
	}
}

func TestMarkdownNarrowTableFits(t *testing.T) {
	src := "| Column one | Column two |\n|---|---|\n| a fairly long cell that has to wrap | another long cell value here |\n"
	maxWidth(t, "narrow table", noColor().Markdown(src, 30), 30)
}

func TestMarkdownASCIITable(t *testing.T) {
	out := New(brand.Env{Depth: brand.DepthNone}).Markdown("| a | b |\n|---|---|\n| 1 | 2 |\n", 40)
	want := "+---+---+\n| a | b |\n+---+---+\n| 1 | 2 |\n+---+---+"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}
