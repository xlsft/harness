package subagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/xlsft/harness/packages/client/vcshooks"
	"github.com/xlsft/harness/packages/client/worktree"
)

// An agent with isolation gets a git worktree, or, when the session's WorktreeCreate hooks run, a working copy one of
// them made (docs/design/vcs-hooks.md). Either way it is a worktree.Result; Hook says which.

// createWorktree makes the isolated copy of the session's folder for agent id.
func (m *Manager) createWorktree(ctx context.Context, id string) (*worktree.Result, error) {
	name := "agent-" + id
	if h := vcshooks.Of(m.cfg.Hooks); h.Isolates() {
		c, err := h.Create(ctx, vcshooks.CreateRequest{Source: m.cfg.Cwd, Name: name, Kind: vcshooks.KindAgent, AgentID: id})
		if err != nil {
			return nil, fmt.Errorf("cannot create a working copy for the agent: %w. The agent did not start; ask the user before starting it without isolation", err)
		}
		return &worktree.Result{Name: c.Name, Path: c.Path, Cwd: c.Cwd, BaseRef: c.Base, RepositoryPath: c.Source, Hook: true}, nil
	}
	r, err := worktree.Create(ctx, m.cfg.Cwd, worktree.Options{Name: name})
	if err != nil {
		return nil, fmt.Errorf("cannot create a worktree for the agent: %w", err)
	}
	return r, nil
}

func (m *Manager) hookRemoval(wt *worktree.Result, force bool) vcshooks.RemoveRequest {
	return vcshooks.RemoveRequest{Path: wt.Path, Source: wt.RepositoryPath, Name: wt.Name, Kind: vcshooks.KindAgent, Force: force}
}

// rollbackWorktree takes away a copy made for an agent that failed to start: nothing ran in it.
func (m *Manager) rollbackWorktree(ctx context.Context, wt *worktree.Result) {
	if !wt.Hook {
		_ = worktree.Rollback(ctx, wt)
		return
	}
	if err := vcshooks.Of(m.cfg.Hooks).Remove(ctx, m.hookRemoval(wt, true)); err != nil && !errors.Is(err, vcshooks.ErrNoRemoveHook) {
		m.log().Warn("remove agent working copy", "path", wt.Path, "err", err)
	}
}

// removeIfUntouched takes away an agent's copy that holds nothing of its own once the session closes. A hook's copy goes
// only when a VcsStatus hook says it has no changes: without one nothing here can tell, and it stays.
func (m *Manager) removeIfUntouched(ctx context.Context, wt *worktree.Result) error {
	if !wt.Hook {
		_, err := worktree.RemoveIfUntouched(ctx, wt)
		return err
	}
	h := vcshooks.Of(m.cfg.Hooks)
	if !h.Reports() {
		return nil
	}
	st, err := h.Status(ctx, wt.Path)
	if err != nil || st == nil || len(st.Files) > 0 {
		return err
	}
	if err := h.Remove(ctx, m.hookRemoval(wt, false)); err != nil && !errors.Is(err, vcshooks.ErrNoRemoveHook) {
		return err
	}
	return nil
}
