package server

import (
	"context"
	"os"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/vcs"
)

// The session's pull request (vcs.Tracker): SessionInfo.pull_request shows it, SetPrMonitor is the auto-fix switch.

// maxResumedPRWatches bounds the sessions a client start reopens for their PRs.
const maxResumedPRWatches = 20

// startPR connects the tracker to the live session and resumes a stored binding's poll.
func (ls *liveSession) startPR() {
	ls.sess.ConnectPR(ls.publishInfo)
	ls.sess.PR.Start()
}

// SetPrMonitor turns watching the session's PR on or off: the switch in the frontends; the model's way is PrMonitor.
func (s *Server) SetPrMonitor(ctx context.Context, req *connect.Request[agentv1.SetPrMonitorRequest]) (*connect.Response[agentv1.SetPrMonitorResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if _, err := ls.sess.PR.SetMonitor(ctx, req.Msg.GetEnabled()); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.SetPrMonitorResponse{}), nil
}

// resumePRWatches reopens the sessions that were watching their PR when the client stopped, so a CI failure wakes them
// with no frontend open. Sessions closed on purpose stay closed (vcs.Tracker.Park); their watch resumes when they are
// opened. Runs once per server, one session after the other.
func (s *Server) resumePRWatches() {
	watches := vcs.ListPRWatches(bootstrap.PRStateDir())
	if len(watches) > maxResumedPRWatches {
		watches = watches[:maxResumedPRWatches]
	}
	for _, w := range watches {
		if s.ctx.Err() != nil {
			return
		}
		if _, err := os.Stat(w.Cwd); err != nil {
			s.log().Debug("not resuming a pull request watch: its folder is gone", "session", w.SessionID, "cwd", w.Cwd)
			continue
		}
		_, err := s.CreateSession(s.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: w.Cwd, ResumeSessionId: w.SessionID}))
		if err != nil {
			s.log().Warn("resuming a pull request watch failed", "session", w.SessionID, "err", err)
		}
	}
}
