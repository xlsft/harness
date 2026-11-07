package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/worktree"
)

// The setup of a session's new worktrees (docs/design/worktree-setup.md): the client reads the project's
// .harness/worktree.json from the checkout the worktree came from, runs its steps in the worktree under the session's
// sandbox, publishes WorktreeSetupUpdated, and tells the agent what ran. A project with lockfiles and no setup gets a
// suggestion instead, which AnswerWorktreeSetup accepts (writing the file) or dismisses (on this device).

const (
	// setupOutputInterval throttles the output updates of a running step.
	setupOutputInterval = time.Second
	// setupSummaryTail is how much of a failed step's output the agent is told.
	setupSummaryTail = 2000
	// setupDismissedFile, in the harness home, lists the repositories whose suggestion the user dismissed. It is a choice
	// of this device: the project has no setup until someone writes one.
	setupDismissedFile = "worktree-setup-dismissed.json"
)

// pendingSetup is a suggestion waiting for the user.
type pendingSetup struct {
	req   tools.WorktreeSetupRequest
	cfg   *worktree.SetupConfig
	setup *agentv1.WorktreeSetup
}

// worktreeSetups is a session's suggestions waiting for an answer.
type worktreeSetups struct {
	mu      sync.Mutex
	pending map[string]*pendingSetup
}

// worktreeSetup implements tools.WorktreeSetupFunc for the session's tools, its subagents and CreateSession.
func (ls *liveSession) worktreeSetup(ctx context.Context, req tools.WorktreeSetupRequest) tools.WorktreeSetupReport {
	wt := req.Worktree
	if wt == nil {
		return tools.WorktreeSetupReport{}
	}
	src := wt.SourcePath
	if src == "" {
		src = wt.RepositoryPath
	}
	pb := &agentv1.WorktreeSetup{Id: uuid.NewString(), WorktreePath: wt.Path, AgentId: req.AgentID,
		ConfigPath: filepath.Join(src, filepath.FromSlash(worktree.SetupFile))}
	cfg, err := worktree.LoadSetup(src)
	if err != nil {
		pb.State, pb.Error = agentv1.WorktreeSetup_STATE_FAILED, err.Error()
		ls.publishSetup(pb, true)
		return tools.WorktreeSetupReport{Ran: true, Summary: fmt.Sprintf("Nothing was set up: the project's %s could not be used (%v).", worktree.SetupFile, err)}
	}
	if cfg == nil {
		return ls.suggestSetup(ctx, req, pb)
	}
	return ls.execSetup(ctx, wt, cfg, pb)
}

// execSetup plans and runs cfg in the worktree, publishing as it goes, and returns what the agent is told.
func (ls *liveSession) execSetup(ctx context.Context, wt *worktree.Result, cfg *worktree.SetupConfig, pb *agentv1.WorktreeSetup) tools.WorktreeSetupReport {
	steps, err := worktree.PlanSetup(ctx, wt.Path, cfg)
	if err != nil {
		pb.State, pb.Error = agentv1.WorktreeSetup_STATE_FAILED, err.Error()
		ls.publishSetup(pb, true)
		return tools.WorktreeSetupReport{Ran: true, Summary: fmt.Sprintf("Nothing was set up: %s could not be planned (%v).", worktree.SetupFile, err)}
	}
	if len(steps) == 0 {
		return tools.WorktreeSetupReport{}
	}
	start := time.Now()
	pb.State = agentv1.WorktreeSetup_STATE_RUNNING
	var lastStates string
	progress := func(results []worktree.StepResult) {
		pb.Steps = setupStepsProto(results)
		pb.DurationMs = time.Since(start).Milliseconds()
		// A change of a step's status is kept for replays; output in between is not.
		states := ""
		for _, r := range results {
			states += r.Status + ","
		}
		ls.publishSetup(pb, states != lastStates)
		lastStates = states
	}
	shell := ls.sess.ToolEnv.Shell
	build := func(command, dir string) (*exec.Cmd, error) { return shell.SetupCommand(command, dir, wt.Path), nil }
	results := worktree.RunSetup(ctx, wt.Path, steps, build, progress, setupOutputInterval)
	ok := worktree.SetupSucceeded(results)
	pb.Steps, pb.DurationMs = setupStepsProto(results), time.Since(start).Milliseconds()
	pb.State = agentv1.WorktreeSetup_STATE_SUCCEEDED
	if !ok {
		pb.State = agentv1.WorktreeSetup_STATE_FAILED
	}
	ls.publishSetup(pb, true)
	var text string
	if ok {
		text = "It ran before you started and every step succeeded, so what it installs is in place. Don't run these again:\n"
	} else {
		text = "It ran before you started and not every step succeeded. What succeeded is in place; repeat only what failed, and only if the task needs it:\n"
	}
	return tools.WorktreeSetupReport{Ran: true, Succeeded: ok, Summary: text + worktree.SetupSummary(results, setupSummaryTail)}
}

// suggestSetup offers a setup for a project with lockfiles and none configured, once per repository on this device
// (until it is answered), and returns what the agent is told.
func (ls *liveSession) suggestSetup(ctx context.Context, req tools.WorktreeSetupRequest, pb *agentv1.WorktreeSetup) tools.WorktreeSetupReport {
	wt := req.Worktree
	if setupDismissed(wt.RepositoryPath) {
		return tools.WorktreeSetupReport{}
	}
	ls.setups.mu.Lock()
	for _, p := range ls.setups.pending {
		if p.req.Worktree.RepositoryPath == wt.RepositoryPath {
			ls.setups.mu.Unlock()
			return tools.WorktreeSetupReport{} // one card per repository is enough
		}
	}
	ls.setups.mu.Unlock()
	cfg, steps := worktree.SuggestSetup(ctx, wt.Path)
	if cfg == nil {
		return tools.WorktreeSetupReport{}
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	pb.State, pb.SuggestedConfig = agentv1.WorktreeSetup_STATE_SUGGESTED, string(data)+"\n"
	results := make([]worktree.StepResult, len(steps))
	for i, s := range steps {
		results[i] = worktree.StepResult{SetupStep: s, Status: worktree.StepPending, ExitCode: -1}
	}
	pb.Steps = setupStepsProto(results)
	ls.setups.mu.Lock()
	if ls.setups.pending == nil {
		ls.setups.pending = map[string]*pendingSetup{}
	}
	ls.setups.pending[pb.Id] = &pendingSetup{req: req, cfg: cfg, setup: pb}
	ls.setups.mu.Unlock()
	ls.publishSetup(pb, true)
	var cmds []string
	for _, s := range steps {
		cmds = append(cmds, fmt.Sprintf("`%s` in %s", s.Command, s.Dir))
	}
	return tools.WorktreeSetupReport{Suggested: true, Summary: fmt.Sprintf("Nothing was set up: the project has lockfiles but no %s, so dependencies such as node_modules are not installed in this worktree. "+
		"The user was offered a setup that installs them from the package managers' caches (%s); if they accept, a note follows when it has run. "+
		"If the task needs them before then, install them in the worktree the same way; never link or copy another checkout's dependencies.",
		worktree.SetupFile, strings.Join(cmds, ", "))}
}

func (ls *liveSession) publishSetup(pb *agentv1.WorktreeSetup, durable bool) {
	ev := &agentv1.SessionEvent{Event: &agentv1.SessionEvent_WorktreeSetupUpdated{WorktreeSetupUpdated: &agentv1.WorktreeSetupUpdated{Setup: cloneSetup(pb)}}}
	if durable {
		ls.publish(ev)
	} else {
		ls.hub.publishEphemeral(ev)
	}
}

func cloneSetup(pb *agentv1.WorktreeSetup) *agentv1.WorktreeSetup {
	return proto.Clone(pb).(*agentv1.WorktreeSetup)
}

var setupStatus = map[string]agentv1.WorktreeSetupStep_Status{
	worktree.StepPending:  agentv1.WorktreeSetupStep_STATUS_PENDING,
	worktree.StepRunning:  agentv1.WorktreeSetupStep_STATUS_RUNNING,
	worktree.StepOK:       agentv1.WorktreeSetupStep_STATUS_OK,
	worktree.StepFailed:   agentv1.WorktreeSetupStep_STATUS_FAILED,
	worktree.StepTimedOut: agentv1.WorktreeSetupStep_STATUS_TIMED_OUT,
	worktree.StepCanceled: agentv1.WorktreeSetupStep_STATUS_CANCELED,
}

func setupStepsProto(results []worktree.StepResult) []*agentv1.WorktreeSetupStep {
	out := make([]*agentv1.WorktreeSetupStep, len(results))
	for i, r := range results {
		out[i] = &agentv1.WorktreeSetupStep{Command: r.Command, Dir: r.Dir, Status: setupStatus[r.Status], ExitCode: int32(r.ExitCode),
			Output: r.Output, DurationMs: r.Duration.Milliseconds(), Error: r.Error, Manager: r.Manager, TimeoutMs: r.Timeout.Milliseconds()}
	}
	return out
}

// startWorktreeSetup sets up the worktree CreateSession made, in the background; the first turn waits for it.
func (ls *liveSession) startWorktreeSetup(wt *worktree.Result) {
	ag := ls.sess.Agent
	done := make(chan struct{})
	ag.AddStartGate(done)
	ls.bg.Add(1)
	go func() {
		defer ls.bg.Done()
		defer close(done)
		rep := ls.worktreeSetup(ls.ctx, tools.WorktreeSetupRequest{Worktree: wt, Remind: ag.Remind})
		if rep.Summary != "" {
			ag.Remind(tools.ReminderWorktreeSetup, tools.SetupVars(wt.Path, rep.Summary))
		}
	}()
}

func (s *Server) AnswerWorktreeSetup(ctx context.Context, req *connect.Request[agentv1.AnswerWorktreeSetupRequest]) (*connect.Response[agentv1.AnswerWorktreeSetupResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ls.setups.mu.Lock()
	p := ls.setups.pending[req.Msg.GetSetupId()]
	delete(ls.setups.pending, req.Msg.GetSetupId())
	ls.setups.mu.Unlock()
	if p == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no worktree setup %s is waiting for an answer", req.Msg.GetSetupId()))
	}
	wt := p.req.Worktree
	if !req.Msg.GetAccept() {
		if err := dismissSetup(wt.RepositoryPath); err != nil {
			s.log().Warn("remember a dismissed worktree setup", "err", err)
		}
		p.setup.State = agentv1.WorktreeSetup_STATE_DISMISSED
		ls.publishSetup(p.setup, true)
		return connect.NewResponse(&agentv1.AnswerWorktreeSetupResponse{}), nil
	}
	src := wt.SourcePath
	if src == "" {
		src = wt.RepositoryPath
	}
	cfg := p.cfg
	path, err := worktree.WriteSetup(src, cfg)
	if errors.Is(err, worktree.ErrSetupExists) {
		// Someone wrote one meanwhile: that one is the project's setup.
		if cfg, err = worktree.LoadSetup(src); err == nil && cfg == nil {
			err = errors.New(worktree.SetupFile + " disappeared")
		}
	}
	if err != nil {
		ls.restorePending(p)
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("write %s: %w", path, err))
	}
	if _, err := os.Stat(wt.Path); err != nil {
		p.setup.State, p.setup.Error = agentv1.WorktreeSetup_STATE_FAILED, "the worktree is gone; the setup runs in the next one"
		ls.publishSetup(p.setup, true)
		return connect.NewResponse(&agentv1.AnswerWorktreeSetupResponse{}), nil
	}
	pb := &agentv1.WorktreeSetup{Id: p.setup.Id, WorktreePath: wt.Path, AgentId: p.req.AgentID, ConfigPath: filepath.Join(src, filepath.FromSlash(worktree.SetupFile))}
	ls.bg.Add(1)
	go func() {
		defer ls.bg.Done()
		rep := ls.execSetup(ls.ctx, wt, cfg, pb)
		if rep.Summary != "" && p.req.Remind != nil {
			p.req.Remind(tools.ReminderWorktreeSetup, tools.SetupVars(wt.Path, "The user accepted the suggested setup. "+rep.Summary))
		}
	}()
	return connect.NewResponse(&agentv1.AnswerWorktreeSetupResponse{}), nil
}

func (ls *liveSession) restorePending(p *pendingSetup) {
	ls.setups.mu.Lock()
	ls.setups.pending[p.setup.Id] = p
	ls.setups.mu.Unlock()
}

var setupDismissedMu sync.Mutex

func readDismissed() map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(filepath.Join(config.Home(), setupDismissedFile))
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func setupDismissed(repo string) bool {
	setupDismissedMu.Lock()
	defer setupDismissedMu.Unlock()
	_, ok := readDismissed()[repo]
	return ok
}

func dismissSetup(repo string) error {
	setupDismissedMu.Lock()
	defer setupDismissedMu.Unlock()
	m := readDismissed()
	m[repo] = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(config.Home(), setupDismissedFile), data, 0o600)
}
