package tools

import (
	"context"

	"github.com/xlsft/harness/packages/client/worktree"
)

// ReminderWorktreeSetup tells the agent what the setup of its worktree did ({{path}}, {{summary}}), so that it does not
// install again what is installed, or knows what failed (docs/design/worktree-setup.md).
const ReminderWorktreeSetup = "worktree_setup"

// WorktreeSetupRequest asks the session to set up a worktree it just created (worktree.SetupFile).
type WorktreeSetupRequest struct {
	Worktree *worktree.Result
	// AgentID is the subagent the worktree is for ("" for the session's main agent).
	AgentID string
	// Remind queues a reminder for the agent working in the worktree: for a setup that runs later, when the user accepts
	// a suggestion. Nil: nobody is told.
	Remind func(id string, vars map[string]string)
}

// WorktreeSetupReport is what became of a setup.
type WorktreeSetupReport struct {
	// Ran: steps ran; Succeeded: all of them ended well.
	Ran, Succeeded bool
	// Suggested: there was no setup, and the user was offered one.
	Suggested bool
	// Summary is what the agent is told ("" when there is nothing to say: no setup and no suggestion).
	Summary string
}

// WorktreeSetupFunc runs the setup of a new worktree, and returns when it has run (a suggestion returns at once).
type WorktreeSetupFunc func(ctx context.Context, r WorktreeSetupRequest) WorktreeSetupReport

// SetupVars are the variables of ReminderWorktreeSetup.
func SetupVars(path, summary string) map[string]string {
	return map[string]string{"path": path, "summary": summary}
}
