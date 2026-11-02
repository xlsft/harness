package bootstrap

import (
	"fmt"
	"os"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/outputstyle"
)

// OutputStyles discovers the output styles available to the session now
// (built-ins of the current bundle, user styles from sync or ~/.claude and
// ~/.harness, project styles of the git root and the cwd).
func (s *Session) OutputStyles() *outputstyle.Catalog {
	o := s.styleOpts
	if s.Agent != nil {
		o.Bundle = s.Agent.Bundle()
	}
	return outputstyle.Discover(o)
}

// ActiveOutputStyle returns the active style id or name ("default" when none).
func (s *Session) ActiveOutputStyle() string {
	if s.Agent == nil {
		return outputstyle.Default
	}
	st := s.Agent.OutputStyle()
	if st == nil {
		return outputstyle.Default
	}
	return st.ID
}

// SetOutputStyle switches the output style by id or name; "default" or ""
// clears it. Unknown names are an error. deferred reports that the coding
// system sections change only at the next compaction (see agent.OutputStyle).
func (s *Session) SetOutputStyle(name string) (deferred bool, err error) {
	if outputstyle.IsDefault(name) {
		return s.Agent.SetOutputStyle(nil), nil
	}
	st, ok := s.OutputStyles().Lookup(name)
	if !ok {
		return false, fmt.Errorf("unknown output style %q", name)
	}
	return s.Agent.SetOutputStyle(agentStyle(st)), nil
}

func agentStyle(st outputstyle.Style) *agent.OutputStyle {
	if outputstyle.IsDefault(st.ID) {
		return nil
	}
	return &agent.OutputStyle{ID: st.ID, Name: st.Name, Text: st.Text, KeepCodingInstructions: st.KeepCodingInstructions}
}

func styleOptions(docs outputstyle.DocSource, gitRoot, cwd string) outputstyle.Options {
	home, _ := os.UserHomeDir()
	return outputstyle.Options{Docs: docs, Home: home, HarnessHome: config.Home(), ProjectDirs: []string{gitRoot, cwd}}
}
