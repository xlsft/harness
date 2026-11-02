package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/xlsft/harness/packages/cli/brand"
)

// Run starts the interactive program and blocks until it exits.
func Run(ctx context.Context, o Options) error {
	m := New(o)
	m.ctx = ctx
	// The renderer must not re-quantize what brand already resolved (HARNESS_COLOR may force a
	// depth the terminal does not advertise).
	profile := map[brand.Depth]colorprofile.Profile{
		brand.DepthTrue: colorprofile.TrueColor, brand.Depth256: colorprofile.ANSI256,
		brand.Depth16: colorprofile.ANSI, brand.DepthNone: colorprofile.Ascii,
	}[o.Env.Depth]
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithColorProfile(profile))
	program = p
	_, err := p.Run()
	return err
}
