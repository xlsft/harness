package server

import (
	"context"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/settings"
)

// Idle compaction: a session nobody has used for IdleCompactAfter whose context is above IdleCompactMinFraction
// of the window is compacted in the background, unless the user turned it off (compactIdleSessions, the
// UserPreferences toggle; on by default).
//
// Only live sessions of this machine are candidates: sessions of other machines are never live here. A session
// is skipped while a turn or another compaction runs and while it waits for a permission, question or plan
// answer. Activity is the last event published for the session. Sessions become live when a frontend opens
// them, so after a client restart a session counts as active from the moment it is opened again: a restart
// never starts a burst of compactions, and each check compacts at most one session. A session that was handled
// (compacted, or found below the threshold) is not looked at again until its next turn.
const (
	IdleCompactAfter       = 30 * time.Minute
	IdleCompactMinFraction = 0.5
	// idleCheckInterval is how often live sessions are checked.
	idleCheckInterval = time.Minute
)

// idleCandidate is what the idle compactor needs from a live session.
type idleCandidate interface {
	// idleSince returns the time of the session's last activity and whether it could be compacted now (idle,
	// nothing pending, not handled since its last turn).
	idleSince() (time.Time, bool)
	contextFraction() float64
	idleCompactionEnabled() bool
	// markIdleHandled excludes the session until its next turn.
	markIdleHandled()
	compactIdle(ctx context.Context) error
}

// idleTick compacts the first candidate that has been idle for IdleCompactAfter at now with its context above
// IdleCompactMinFraction, and returns it (nil when none is due). Sessions found below the threshold are marked
// handled; sessions of a user who turned idle compaction off are not, so turning it on applies to them.
func idleTick(ctx context.Context, now time.Time, cands []idleCandidate) idleCandidate {
	for _, c := range cands {
		since, ok := c.idleSince()
		if !ok || now.Sub(since) < IdleCompactAfter {
			continue
		}
		if c.contextFraction() <= IdleCompactMinFraction {
			c.markIdleHandled()
			continue
		}
		if !c.idleCompactionEnabled() {
			continue
		}
		c.markIdleHandled()
		_ = c.compactIdle(ctx)
		return c
	}
	return nil
}

// runIdleCompaction checks the live sessions every idleCheckInterval until the server closes.
func (s *Server) runIdleCompaction() {
	t := time.NewTicker(idleCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-t.C:
			s.checkIdle(now)
		}
	}
}

// checkIdle runs one idle compaction check at now.
func (s *Server) checkIdle(now time.Time) {
	s.mu.Lock()
	cands := make([]idleCandidate, 0, len(s.live))
	for _, ls := range s.live {
		cands = append(cands, ls)
	}
	s.mu.Unlock()
	idleTick(s.ctx, now, cands)
}

func (ls *liveSession) idleSince() (time.Time, bool) {
	ls.mu.Lock()
	since := ls.updatedAt
	ok := !ls.closed && !ls.idleCompacted && ls.waiting == 0 && ls.status == agentv1.SessionStatus_SESSION_STATUS_IDLE && !since.IsZero()
	ls.mu.Unlock()
	return since, ok && !ls.compacting.Load() && !ls.driver.Busy()
}

func (ls *liveSession) contextFraction() float64 { return ls.sess.Agent.ContextUsage().Fraction() }

// idleCompactionEnabled reads the merged settings of the session's folder again, so a change of the user's
// synced settings document (UpdateUserPreferences) applies without reopening the session.
func (ls *liveSession) idleCompactionEnabled() bool {
	var docs settings.DocSource
	if ls.sess.Sync != nil {
		docs = ls.sess.Sync.Cache()
	}
	st, err := settings.Load(ls.ctx, settings.Options{Cwd: ls.sess.Cwd, Docs: docs, Trusted: ls.sess.Trusted})
	return err == nil && st.CompactIdleSessions
}

func (ls *liveSession) markIdleHandled() {
	ls.mu.Lock()
	ls.idleCompacted = true
	ls.mu.Unlock()
}

// compactIdle runs under the session's context (it ends when the session closes).
func (ls *liveSession) compactIdle(context.Context) error {
	_, err := ls.runCompaction(func() (agent.CompactInfo, error) { return ls.sess.Agent.CompactIdle(ls.ctx) })
	if err != nil && ls.ctx.Err() == nil {
		ls.srv.log().Warn("idle compaction failed", "session", ls.sess.ID, "err", err)
	}
	return err
}
