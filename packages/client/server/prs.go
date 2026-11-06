package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/agent"
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

// draftTimeout bounds the model call behind DraftPullRequest; past it the commit messages make the draft.
const draftTimeout = 90 * time.Second

// DraftPullRequest reads the session's branch and has the small-slot model write the title and body. A model that fails
// costs the user nothing: the text then comes from the commit messages, and the note says so.
func (s *Server) DraftPullRequest(ctx context.Context, req *connect.Request[agentv1.DraftPullRequestRequest]) (*connect.Response[agentv1.DraftPullRequestResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	f, err := ls.sess.PR.Facts(ctx, req.Msg.GetBaseBranch())
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	res := &agentv1.DraftPullRequestResponse{
		HeadBranch: f.Head, BaseBranch: f.Base, RepositoryKey: f.Repo.Key, Commits: int32(f.Commits), DiffStat: f.Stat,
		Dirty: f.Dirty, NeedsPush: f.NeedsPush, Provider: f.Provider, Note: f.ProviderProblem,
	}
	model := ""
	if ls.sess.Catalog != nil {
		model = ls.sess.Catalog.Slots["small"]
	}
	if model == "" {
		model = ls.sess.Model
	}
	dctx, cancel := context.WithTimeout(ctx, draftTimeout)
	defer cancel()
	title, body, err := vcs.DraftWriter{Gateway: ls.sess.Gateway, Bundle: ls.sess.Agent.Bundle(), Model: model, SessionID: ls.sess.ID}.Write(dctx, f)
	if err != nil {
		s.log().Debug("drafting a pull request failed", "session", ls.sess.ID, "err", err)
		title, body = vcs.FallbackDraft(f)
		if res.Note == "" {
			res.Note = "The model could not write the draft (" + err.Error() + "), so it comes from the commit messages."
		}
	} else {
		res.Drafted = true
	}
	if a := ls.sess.PRAttribution(); a != "" {
		body = strings.TrimSpace(body + "\n\n" + a)
	}
	res.Title, res.Body = title, body
	return connect.NewResponse(res), nil
}

// CreatePullRequest pushes the branch and opens the PR, which then shows on the session like any bound PR.
func (s *Server) CreatePullRequest(ctx context.Context, req *connect.Request[agentv1.CreatePullRequestRequest]) (*connect.Response[agentv1.CreatePullRequestResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	m := req.Msg
	pr, err := ls.sess.PR.CreatePR(ctx, vcs.CreatePRRequest{Title: m.GetTitle(), Body: m.GetBody(), Base: m.GetBaseBranch(), Draft: m.GetDraft(), Monitor: m.GetMonitor()})
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.CreatePullRequestResponse{Url: pr.URL, Number: int32(pr.Number), Title: pr.Title, Existed: pr.Existed, Pushed: pr.Pushed}), nil
}

// GetVcsStatus says which local CLIs this client can reach code hosts with.
func (s *Server) GetVcsStatus(ctx context.Context, _ *connect.Request[agentv1.GetVcsStatusRequest]) (*connect.Response[agentv1.GetVcsStatusResponse], error) {
	return connect.NewResponse(&agentv1.GetVcsStatusResponse{Clis: vcs.CLIStatus(ctx, s.Base.VCSProviders)}), nil
}

// DeliverAutoFixJob takes a job of the server's auto-fix pipeline (relay command deliver_auto_fix_job). A closed session
// reopens for it: the point of the pipeline is that nobody has to be there. It starts a turn when the session is idle,
// through the pr_activity reminder, never as a user message.
func (s *Server) DeliverAutoFixJob(ctx context.Context, req *agentv1.DeliverAutoFixJobRequest) error {
	job := req.GetJob()
	if job == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("job is required"))
	}
	ls, err := s.openForNote(ctx, req.GetSessionId())
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	told, err := ls.sess.PR.DeliverJob(job)
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if told {
		ls.sess.Agent.Emit(agent.Event{Kind: agent.EventNotice, Notice: &agent.Notice{Level: "info", Kind: "auto_fix_job", Text: fmt.Sprintf("Auto-fix: the server reported activity on pull request #%d", job.GetPrNumber())}})
	}
	return nil
}
