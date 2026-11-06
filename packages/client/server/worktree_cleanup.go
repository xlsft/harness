package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/worktree"
)

const (
	// worktreeCleanupEvery is how often a long-running client looks for worktrees nobody needs.
	worktreeCleanupEvery = 24 * time.Hour
	// worktreeCleanupTranscripts bounds how many recent transcripts name the repositories to look at.
	worktreeCleanupTranscripts = 500
)

// runWorktreeCleanup removes stale worktrees when the client starts, and once a day after that.
func (s *Server) runWorktreeCleanup() {
	t := time.NewTicker(worktreeCleanupEvery)
	defer t.Stop()
	for {
		s.cleanupWorktrees(s.ctx, time.Now())
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// cleanupWorktrees runs worktree.Prune on every repository the local transcripts know of, under that repository's
// cleanupPeriodDays. 0 turns it off.
func (s *Server) cleanupWorktrees(ctx context.Context, now time.Time) {
	seen := map[string]bool{}
	for _, f := range recentTranscripts(worktreeCleanupTranscripts) {
		if ctx.Err() != nil {
			return
		}
		cwd, wt := transcriptHead(f.path, true)
		repo := ""
		switch {
		case wt != nil:
			repo = wt.RepositoryPath
		case cwd != "":
			if r, err := worktree.Find(ctx, cwd); err == nil {
				repo = r.Main
			}
		}
		if repo == "" || seen[repo] {
			continue
		}
		seen[repo] = true
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(worktree.Dir))); err != nil {
			continue
		}
		st, err := settings.Load(ctx, settings.Options{Cwd: repo})
		if err != nil || st.CleanupPeriodDays <= 0 {
			continue
		}
		pruned, err := worktree.Prune(ctx, repo, time.Duration(st.CleanupPeriodDays)*24*time.Hour, now, s.worktreeInUse)
		for _, p := range pruned {
			s.log().Info("removed a stale worktree", "path", p.Path, "branch", p.Branch)
		}
		if err != nil {
			s.log().Warn("worktree cleanup", "repository", repo, "err", err)
		}
	}
}

// worktreeInUse: an open session or one of its agents works there.
func (s *Server) worktreeInUse(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ls := range s.live {
		if w := ls.sess.CurrentWorktree(); w != nil && w.Path == path {
			return true
		}
		if d := ls.sess.Dir(); d == path || strings.HasPrefix(d, path+string(filepath.Separator)) {
			return true
		}
		if ls.sess.Subagents != nil {
			for _, a := range ls.sess.Subagents.List() {
				if a.WorktreePath == path && a.State.Active() {
					return true
				}
			}
		}
	}
	return false
}
