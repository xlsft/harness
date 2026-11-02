package render

import (
	"regexp"
	"strings"
	"sync"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/xlsft/harness/packages/cli/brand"
)

// markdown caches glamour renderers per wrap width.
type markdown struct {
	mu      sync.Mutex
	byW     map[int]*glamour.TermRenderer
	conf    gansi.StyleConfig
	opts    []glamour.TermRendererOption
	unicode bool
}

// emptySGR matches a style that is reset before any text is printed.
var emptySGR = regexp.MustCompile(`\x1b\[[0-9;]+m(\x1b\[0?m)+`)

// blankRuns collapses runs of blank lines (glamour doubles them between some blocks).
var blankRuns = regexp.MustCompile(`\n(\x1b\[0?m)?\n(\x1b\[0?m|\n)+`)

func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }
func up(u uint) *uint     { return &u }

var registerChroma sync.Once

// Code highlighting uses the terminal's own ANSI palette roles (keywords blue, names yellow,
// comments green, strings red), like the reference; it is not part of the brand palette.
func registerChromaStyles() {
	registerChroma.Do(func() {
		chromastyles.Register(chroma.MustNewStyle("harness-dark", chroma.StyleEntries{
			chroma.Keyword: "#60A5FA", chroma.KeywordType: "#60A5FA", chroma.NameFunction: "#FACC15",
			chroma.NameBuiltin: "#FACC15", chroma.Comment: "#4ADE80", chroma.LiteralString: "#F87171",
			chroma.LiteralNumber: "#C084FC", chroma.Operator: "#D4D4D8",
		}))
		chromastyles.Register(chroma.MustNewStyle("harness-light", chroma.StyleEntries{
			chroma.Keyword: "#1D4ED8", chroma.KeywordType: "#1D4ED8", chroma.NameFunction: "#A16207",
			chroma.NameBuiltin: "#A16207", chroma.Comment: "#15803D", chroma.LiteralString: "#B91C1C",
			chroma.LiteralNumber: "#7E22CE", chroma.Operator: "#3F3F46",
		}))
	})
}

func newMarkdown(e brand.Env) *markdown {
	registerChromaStyles()
	accent := e.Hex(brand.Accent)
	color := func(s string) *string {
		if s == "" {
			return nil
		}
		return sp(s)
	}
	bullet, quote, hr := "- ", "▎ ", "────────"
	if !e.Unicode {
		quote, hr = "| ", "--------"
	}
	c := gansi.StyleConfig{
		Document:       gansi.StyleBlock{Margin: up(0)},
		Paragraph:      gansi.StyleBlock{},
		BlockQuote:     gansi.StyleBlock{Indent: up(1), IndentToken: sp(quote), StylePrimitive: gansi.StylePrimitive{Italic: bp(true)}},
		List:           gansi.StyleList{LevelIndent: 2},
		Heading:        gansi.StyleBlock{StylePrimitive: gansi.StylePrimitive{BlockSuffix: "\n", Bold: bp(true)}},
		H1:             gansi.StyleBlock{StylePrimitive: gansi.StylePrimitive{Bold: bp(true), Italic: bp(true), Underline: bp(true)}},
		H2:             gansi.StyleBlock{},
		H3:             gansi.StyleBlock{},
		H4:             gansi.StyleBlock{},
		H5:             gansi.StyleBlock{},
		H6:             gansi.StyleBlock{},
		Strikethrough:  gansi.StylePrimitive{CrossedOut: bp(true)},
		Emph:           gansi.StylePrimitive{Italic: bp(true)},
		Strong:         gansi.StylePrimitive{Bold: bp(true)},
		HorizontalRule: gansi.StylePrimitive{Color: color(e.Hex(brand.Line)), Format: "\n" + hr + "\n"},
		Item:           gansi.StylePrimitive{BlockPrefix: bullet},
		Enumeration:    gansi.StylePrimitive{BlockPrefix: ". "},
		Task:           gansi.StyleTask{Ticked: "[x] ", Unticked: "[ ] "},
		Link:           gansi.StylePrimitive{Color: color(e.Hex(brand.Secondary)), Format: "({{.text}})"},
		LinkText:       gansi.StylePrimitive{},
		Image:          gansi.StylePrimitive{Color: color(e.Hex(brand.Secondary))},
		ImageText:      gansi.StylePrimitive{Format: "Image: {{.text}} →"},
		Code:           gansi.StyleBlock{StylePrimitive: gansi.StylePrimitive{Color: color(accent)}},
		CodeBlock:      gansi.StyleCodeBlock{StyleBlock: gansi.StyleBlock{Margin: up(0)}},
	}
	if accent == "" {
		c.Code.Bold = bp(true)
	}
	var opts []glamour.TermRendererOption
	switch e.Depth {
	case brand.DepthTrue:
		opts = append(opts, glamour.WithChromaFormatter("terminal16m"))
	case brand.Depth256:
		opts = append(opts, glamour.WithChromaFormatter("terminal256"))
	case brand.Depth16:
		opts = append(opts, glamour.WithChromaFormatter("terminal16"))
	}
	if e.Depth != brand.DepthNone {
		c.CodeBlock.Theme = "harness-dark"
		if e.Theme == brand.ThemeLight {
			c.CodeBlock.Theme = "harness-light"
		}
	}
	return &markdown{byW: map[int]*glamour.TermRenderer{}, conf: c, opts: opts, unicode: e.Unicode}
}

func (m *markdown) render(src string, width int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.renderer(width)
	if r == nil {
		return src
	}
	body, blocks := extractBlocks(expandTabs(src))
	out := m.clean(r, body)
	if out == "" && body != "" {
		return src
	}
	out = hangLists(stripOSC8(out), width)
	out = restoreBlocks(out, blocks, func(b block) string {
		if b.rows != nil {
			return renderTable(b.rows, width, m.unicode)
		}
		return m.clean(r, b.code)
	})
	out = blankRuns.ReplaceAllString(out, "\n\n")
	return strings.Trim(out, "\n")
}

// renderer returns the cached glamour renderer for width, or nil if it cannot be built.
func (m *markdown) renderer(width int) *glamour.TermRenderer {
	r := m.byW[width]
	if r == nil {
		opts := append([]glamour.TermRendererOption{glamour.WithStyles(m.conf), glamour.WithWordWrap(width)}, m.opts...)
		var err error
		r, err = glamour.NewTermRenderer(opts...)
		if err != nil {
			return nil
		}
		m.byW[width] = r
	}
	return r
}

// clean renders src with r and strips glamour's empty styles, trailing padding and blank runs.
func (m *markdown) clean(r *glamour.TermRenderer, src string) string {
	out, err := r.Render(src)
	if err != nil {
		return ""
	}
	out = emptySGR.ReplaceAllString(out, "")
	out = TrimRight(out)
	out = emptySGR.ReplaceAllString(out, "")
	out = blankRuns.ReplaceAllString(out, "\n\n")
	return strings.Trim(out, "\n")
}

// Markdown renders assistant markdown to width columns (without the 2-column message indent).
func (s *Styles) Markdown(src string, width int) string {
	if s.md == nil {
		s.md = newMarkdown(s.Env)
	}
	return s.md.render(src, max(width, 10))
}
