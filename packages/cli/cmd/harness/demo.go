package main

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/xlsft/harness/packages/cli/brand"
)

// demo renders the brand primitives (banner, loaders, transcript styles) so they can be checked
// in real terminals.
func demo() {
	e := brand.Detect()
	width := 80
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil {
		width = w
	}
	dim, sec, line := e.Fg(brand.Dim), e.Fg(brand.Secondary), e.Fg(brand.Line)

	fmt.Println()
	fmt.Print(e.Banner(width, []string{
		e.Fg(brand.Accent).Bold(true).Render("harness") + dim.Render(" v0.0.0-demo"),
		sec.Render("claude-haiku · you@local"),
		sec.Render("~/Projects/acme/api  (main)"),
		dim.Render("/help for commands · shift+tab to switch modes"),
	}))
	fmt.Println()

	fmt.Println(e.Fg(brand.Accent).Render("❯ ") + sec.Render("fix the failing test in parser"))
	fmt.Println()
	fmt.Println(e.Bullet(true) + " " + e.Fg(brand.Secondary).Bold(true).Render("Read") + sec.Render("(internal/parser/parser_test.go)"))
	fmt.Println(line.Render("  └ ") + sec.Render("Read 84 lines"))
	fmt.Println(e.Bullet(false) + " " + e.Fg(brand.Secondary).Bold(true).Render("Bash") + sec.Render("(go test ./internal/parser/...)"))
	fmt.Println(line.Render("  └ ") + e.Fg(brand.Error).Render("--- FAIL: TestParseRange (0.00s)"))
	fmt.Println(e.Bullet(true) + " " + e.Fg(brand.Secondary).Bold(true).Render("Edit") + sec.Render("(internal/parser/range.go)"))
	fmt.Println(dim.Render("      27   ") + "  for i := start; ")
	fmt.Println(e.DiffDel().Render("      28 - " + "  for i <= end-1 {                      "))
	fmt.Println(e.DiffAdd().Render("      28 + " + "  for i <= end {                        "))
	fmt.Println(e.Fg(brand.Success).Render("▰ ") + "All tests pass. " + e.Fg(brand.Warning).Render("(1 warning)"))
	fmt.Println()

	depth := map[brand.Depth]string{brand.DepthTrue: "truecolor", brand.Depth256: "256", brand.Depth16: "16", brand.DepthNone: "none"}[e.Depth]
	theme := map[brand.Theme]string{brand.ThemeDark: "dark", brand.ThemeLight: "light"}[e.Theme]
	fmt.Println(dim.Render(fmt.Sprintf("color: %s · theme: %s · unicode: %v · reduced motion: %v", depth, theme, e.Unicode, e.ReducedMotion)))

	if !term.IsTerminal(os.Stdout.Fd()) {
		fmt.Println(e.Rail(1) + "  Thinking")
		return
	}
	start := time.Now()
	for n := 0; time.Since(start) < 4*time.Second; n++ {
		elapsed := int(time.Since(start).Seconds())
		fmt.Printf("\r%s  Thinking%s   %s Bash(go test ./...)",
			e.Rail(n), dim.Render(fmt.Sprintf(" · %ds · ↓ %.1fk tokens", elapsed, 1.2+0.3*float64(elapsed))), e.Pulse(n))
		time.Sleep(brand.FrameInterval)
	}
	fmt.Println()
}
