package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/vcs"
	"github.com/xlsft/harness/packages/client/worktree"
)

// LocalAttemptsService (proto/harness/relay/v1/hosts.proto): best-of-N groups for a frontend of this client (docs/design/
// hosts.md, decisions 28 to 32). A host group is made on the server and runs there; a local group is made on the server too,
// and this client runs its attempts, each in a worktree of its own, as many at once as the group allows.

// localAttemptPoll is how often a running local attempt is looked at to see whether its turn ended.
const localAttemptPoll = 500 * time.Millisecond

type localAttemptsService struct{ s *Server }

// localAttemptBranch is the branch of a local attempt: always under harness/, which is what discarding may delete.
func localAttemptBranch(groupID string, index int32) string {
	return fmt.Sprintf("harness/attempt-%s-%d", groupID, index+1)
}

func (l *localAttemptsService) StartLocalAttempts(ctx context.Context, req *connect.Request[relayv1.StartLocalAttemptsRequest]) (*connect.Response[relayv1.StartLocalAttemptsResponse], error) {
	s := l.s
	in := req.Msg.GetRequest()
	if in == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("request is required"))
	}
	if in.GetPermissionMode() == agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("bypassPermissions cannot be used for attempts"))
	}
	hosts, _, tokens, err := s.hostsAPI()
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("best-of-N needs the server: %w", err))
	}
	msg := cloneStart(in)
	if msg.GetParallel() == 0 || msg.GetBudgetUsd() == 0 {
		// The user's own choices, which the server caps with the policy.
		if res, err := s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{})); err == nil {
			p := res.Msg.GetPreferences()
			if msg.Parallel == 0 {
				msg.Parallel = p.GetBestOfNParallel()
			}
			if msg.BudgetUsd == 0 {
				msg.BudgetUsd = p.GetBestOfNBudgetUsd()
			}
		}
	}
	if msg.GetHostId() == "" {
		cwd := strings.TrimSpace(msg.GetCwd())
		if cwd == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cwd is required for local attempts"))
		}
		repo, err := worktree.Find(ctx, cwd)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the folder is not in a git repository; each attempt needs a worktree of its own"))
		}
		msg.Cwd = cwd
		if msg.GetRepositoryKey() == "" {
			if remote, err := moveGit(ctx, repo.Toplevel, nil, "remote", "get-url", "origin"); err == nil {
				if r, ok := vcs.ParseRemote(strings.TrimSpace(remote)); ok {
					msg.RepositoryKey = r.Key
				}
			}
		}
	}
	res, err := coreCall(ctx, tokens, hosts.StartAttempts, msg)
	if err != nil {
		return nil, err
	}
	g := res.GetGroup()
	if g.GetHostId() == "" {
		s.runLocalGroup(g)
	}
	return connect.NewResponse(&relayv1.StartLocalAttemptsResponse{Group: g}), nil
}

func cloneStart(in *relayv1.StartAttemptsRequest) *relayv1.StartAttemptsRequest {
	return &relayv1.StartAttemptsRequest{HostId: in.GetHostId(), Count: in.GetCount(), Prompt: in.GetPrompt(), Title: in.GetTitle(),
		RepositoryKey: in.GetRepositoryKey(), BaseRef: in.GetBaseRef(), Model: in.GetModel(), Effort: in.GetEffort(),
		PermissionMode: in.GetPermissionMode(), TestCommand: in.GetTestCommand(), Parallel: in.GetParallel(), BudgetUsd: in.GetBudgetUsd(),
		Cwd: in.GetCwd(), IdempotencyKey: in.GetIdempotencyKey()}
}

// runLocalGroup runs the attempts of a local group in the background, at most group.parallel at once.
func (s *Server) runLocalGroup(g *relayv1.AttemptGroup) {
	parallel := int(g.GetParallel())
	if parallel <= 0 {
		parallel = len(g.GetAttempts())
	}
	slots := make(chan struct{}, parallel)
	go func() {
		for _, a := range g.GetAttempts() {
			if a.GetState() != relayv1.AttemptState_ATTEMPT_STATE_PENDING {
				continue
			}
			select {
			case slots <- struct{}{}:
			case <-s.ctx.Done():
				return
			}
			go func(index int32) {
				defer func() { <-slots }()
				s.runLocalAttempt(g, index)
			}(a.GetIndex())
		}
	}()
}

// runLocalAttempt makes the attempt's worktree and session, sends the prompt, and returns when the first turn ended.
func (s *Server) runLocalAttempt(g *relayv1.AttemptGroup, index int32) {
	ctx := s.ctx
	report := func(r *relayv1.ReportAttemptRequest) {
		r.GroupId, r.Index = g.GetId(), index
		s.reportAttempt(ctx, r)
	}
	branch := localAttemptBranch(g.GetId(), index)
	spec := AttemptSpec{GroupID: g.GetId(), Index: index, Count: int32(len(g.GetAttempts())), TestCommand: g.GetTestCommand(), BudgetUSD: g.GetBudgetUsd()}
	create := &agentv1.CreateSessionRequest{Cwd: g.GetCwd(), Model: g.GetModel(), Effort: g.GetEffort(), PermissionMode: g.GetPermissionMode(),
		Worktree: &agentv1.WorktreeOptions{Name: strings.TrimPrefix(branch, worktree.BranchPrefix), Branch: branch, BaseRef: g.GetBaseRef()}}
	res, err := s.CreateSession(WithAttempt(ctx, spec), connect.NewRequest(create))
	if err != nil {
		report(&relayv1.ReportAttemptRequest{State: relayv1.AttemptState_ATTEMPT_STATE_FAILED, Error: connectMessage(err)})
		return
	}
	info := res.Msg.GetSession()
	id := info.GetId()
	title := g.GetTitle()
	if title == "" {
		title = strings.SplitN(strings.TrimSpace(g.GetPrompt()), "\n", 2)[0]
	}
	if len(title) > 180 {
		title = title[:180]
	}
	title = fmt.Sprintf("%s (%d/%d)", title, index+1, len(g.GetAttempts()))
	if _, err := s.UpdateSession(ctx, connect.NewRequest(&agentv1.UpdateSessionRequest{SessionId: id, Title: &title})); err != nil {
		s.log().Debug("naming an attempt failed", "session", id, "err", err)
	}
	report(&relayv1.ReportAttemptRequest{State: relayv1.AttemptState_ATTEMPT_STATE_RUNNING, SessionId: id, Branch: info.GetWorktree().GetBranch()})
	if _, err := s.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: id, Uuid: "attempt-" + g.GetId() + "-" + fmt.Sprint(index),
		Content: pbconv.Blocks([]llm.ContentBlock{llm.TextBlock(g.GetPrompt())})})); err != nil {
		report(&relayv1.ReportAttemptRequest{State: relayv1.AttemptState_ATTEMPT_STATE_FAILED, Error: connectMessage(err)})
		return
	}
	// The place is taken until the first turn ends; the session reports itself then (afterAttemptTurn).
	time.Sleep(localAttemptPoll)
	for ctx.Err() == nil {
		s.mu.Lock()
		ls := s.live[id]
		s.mu.Unlock()
		if ls == nil || (!ls.driver.Busy() && ls.info().GetStatus() != agentv1.SessionStatus_SESSION_STATUS_WORKING) {
			return
		}
		time.Sleep(localAttemptPoll)
	}
}

func (l *localAttemptsService) DiscardLocalAttempts(ctx context.Context, req *connect.Request[relayv1.DiscardLocalAttemptsRequest]) (*connect.Response[relayv1.DiscardLocalAttemptsResponse], error) {
	s := l.s
	hosts, _, tokens, err := s.hostsAPI()
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	got, err := coreCall(ctx, tokens, hosts.GetAttemptGroup, &relayv1.GetAttemptGroupRequest{Id: req.Msg.GetGroupId()})
	if err != nil {
		return nil, err
	}
	g := got.GetGroup()
	if g.GetHostId() != "" {
		res, err := coreCall(ctx, tokens, hosts.DiscardAttempts, &relayv1.DiscardAttemptsRequest{GroupId: g.GetId()})
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&relayv1.DiscardLocalAttemptsResponse{Group: res.GetGroup()}), nil
	}
	if g.GetKeptIndex() < 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("keep one attempt first"))
	}
	if s.DeviceID != "" && g.GetDeviceId() != "" && g.GetDeviceId() != s.DeviceID {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("these attempts ran on another machine; discard them from there"))
	}
	var problems []string
	for _, a := range g.GetAttempts() {
		if a.GetIndex() == g.GetKeptIndex() || a.GetState() == relayv1.AttemptState_ATTEMPT_STATE_DISCARDED {
			continue
		}
		if err := s.discardLocalAttempt(ctx, g, a); err != nil {
			problems = append(problems, fmt.Sprintf("attempt %d: %s", a.GetIndex()+1, err))
			s.reportAttempt(ctx, &relayv1.ReportAttemptRequest{GroupId: g.GetId(), Index: a.GetIndex(), Error: err.Error()})
			continue
		}
		s.reportAttempt(ctx, &relayv1.ReportAttemptRequest{GroupId: g.GetId(), Index: a.GetIndex(), State: relayv1.AttemptState_ATTEMPT_STATE_DISCARDED})
	}
	got, err = coreCall(ctx, tokens, hosts.GetAttemptGroup, &relayv1.GetAttemptGroupRequest{Id: g.GetId()})
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("some attempts stay: %s", strings.Join(problems, "; ")))
	}
	return connect.NewResponse(&relayv1.DiscardLocalAttemptsResponse{Group: got.GetGroup()}), nil
}

// discardLocalAttempt closes the attempt's session and removes its worktree and branch. Only a branch of a local attempt goes.
func (s *Server) discardLocalAttempt(ctx context.Context, g *relayv1.AttemptGroup, a *relayv1.Attempt) error {
	if id := a.GetSessionId(); id != "" {
		s.closeLive(id, "discarded")
	}
	branch := localAttemptBranch(g.GetId(), a.GetIndex())
	repo, err := worktree.Find(ctx, g.GetCwd())
	if err != nil {
		return fmt.Errorf("the repository at %s is gone", g.GetCwd())
	}
	path := worktreeOfBranch(ctx, repo.Main, branch)
	if path == "" {
		// No worktree (it never started, or it is gone): the branch alone, if it is there.
		if worktree.BranchExists(ctx, repo.Main, branch) {
			if _, err := moveGit(ctx, repo.Main, nil, "branch", "-D", "--", branch); err != nil {
				return err
			}
		}
		return nil
	}
	return worktree.Remove(ctx, worktree.Removal{RepositoryPath: repo.Main, Path: path, Force: true, Branch: branch})
}

// worktreeOfBranch finds the worktree that has branch checked out, "" when none does.
func worktreeOfBranch(ctx context.Context, repo, branch string) string {
	out, err := moveGit(ctx, repo, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	path := ""
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			path = p
		}
		if b, ok := strings.CutPrefix(line, "branch "); ok && b == "refs/heads/"+branch {
			return path
		}
	}
	return ""
}
