package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// Run starts the interactive program and blocks until it exits.
func Run(ctx context.Context, o Options) error {
	m := New(o)
	m.ctx = ctx
	p := tea.NewProgram(m, tea.WithContext(ctx))
	program = p
	_, err := p.Run()
	return err
}
