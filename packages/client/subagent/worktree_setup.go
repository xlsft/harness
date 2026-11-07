package subagent

import (
	"github.com/xlsft/harness/packages/client/tools"
)

// setUpWorktree runs the setup of an agent's new worktree in the background; the agent's first turn waits for it, and
// starts with a note of what ran.
func (m *Manager) setUpWorktree(a *sub) {
	run := m.cfg.LeadEnv.WorktreeSetup
	ag := a.ag
	if run == nil || ag == nil {
		return
	}
	done := make(chan struct{})
	ag.AddStartGate(done)
	go func() {
		defer close(done)
		rep := run(m.ctx, tools.WorktreeSetupRequest{Worktree: a.wt, AgentID: a.info.ID, Remind: ag.Remind})
		if rep.Summary != "" {
			ag.Remind(tools.ReminderWorktreeSetup, tools.SetupVars(a.wt.Path, rep.Summary))
		}
	}()
}
