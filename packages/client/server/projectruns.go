package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/tools"
)

// Project runs (docs/design/coordinator.md): a coordinator session with a run record next to it, in
// HARNESS_HOME/project-runs/<id>.json. The coordinator's task graph is the plan; workers are sessions started like
// SessionStart ones (linked, the coordinator's mode and rules), here, in worktrees or on the user's hosts through the
// relay. Limits are enforced here, not by the model: parallel workers, budget, the managed policy.

const (
	projectRunPrefix = "prun_"
	// projectReportChars bounds a worker's report on record.
	projectReportChars = 600
	// projectPollEvery is how often remote workers are looked up in the server's inbox while some run.
	projectPollEvery = 30 * time.Second
	// projectGitTimeout bounds a merge.
	projectGitTimeout = 2 * time.Minute
	// Targets.
	targetWorktree = "worktree"
	targetHere     = "here"
	// restartReason is why runs a client left behind are paused.
	restartReason = "The client restarted while the run was going. Resume it to go on."
)

// projectRuns is the server's runs (Server.runs).
type projectRuns struct {
	mu     sync.Mutex
	loaded bool
	byID   map[string]*agentv1.ProjectRun
	// polling are the runs whose remote workers are being followed.
	polling map[string]bool
}

func projectRunsDir() string { return filepath.Join(config.Home(), "project-runs") }

func projectRunPath(id string) string { return filepath.Join(projectRunsDir(), id+".json") }

func validRunID(id string) bool {
	rest, ok := strings.CutPrefix(id, projectRunPrefix)
	if !ok || len(rest) == 0 || len(rest) > 64 {
		return false
	}
	for _, r := range rest {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }

func runEnded(st agentv1.ProjectRunState) bool {
	return st == agentv1.ProjectRunState_PROJECT_RUN_STATE_DONE || st == agentv1.ProjectRunState_PROJECT_RUN_STATE_FAILED ||
		st == agentv1.ProjectRunState_PROJECT_RUN_STATE_STOPPED
}

func workerActive(st agentv1.ProjectWorkerState) bool {
	return st == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_STARTING || st == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_RUNNING ||
		st == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_NEEDS_INPUT
}

// loadLocked reads the records once. A run that was going when the last client stopped is paused, and its local
// workers that were running are interrupted: their sessions closed with that client.
func (r *projectRuns) loadLocked() {
	if r.loaded {
		return
	}
	r.loaded, r.byID, r.polling = true, map[string]*agentv1.ProjectRun{}, map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(projectRunsDir(), projectRunPrefix+"*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		run := &agentv1.ProjectRun{}
		if protojson.Unmarshal(data, run) != nil || !validRunID(run.GetId()) {
			continue
		}
		changed := false
		if st := run.GetState(); !runEnded(st) && st != agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED {
			run.State, run.Reason, changed = agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED, restartReason, true
		}
		for _, w := range run.GetWorkers() {
			if !w.GetRemote() && workerActive(w.GetState()) {
				w.State, changed = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_INTERRUPTED, true
			}
		}
		r.byID[run.GetId()] = run
		if changed {
			run.UpdatedAt = nowRFC()
			_ = saveRun(run)
		}
	}
}

func saveRun(run *agentv1.ProjectRun) error {
	if err := os.MkdirAll(projectRunsDir(), 0o700); err != nil {
		return err
	}
	b, err := protojson.MarshalOptions{Multiline: true}.Marshal(run)
	if err != nil {
		return err
	}
	tmp := projectRunPath(run.GetId()) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, projectRunPath(run.GetId()))
}

// withRun runs fn on a run (r.mu held); a true result saves it and publishes it on the coordinator session.
func (s *Server) withRun(id string, fn func(run *agentv1.ProjectRun) (bool, error)) (*agentv1.ProjectRun, error) {
	r := &s.runs
	r.mu.Lock()
	r.loadLocked()
	run := r.byID[id]
	if run == nil {
		r.mu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project run %s not found on this machine", id))
	}
	changed, err := fn(run)
	var snap *agentv1.ProjectRun
	if changed {
		run.UpdatedAt = nowRFC()
		if serr := saveRun(run); serr != nil {
			s.log().Warn("project run not saved", "run", id, "err", serr)
		}
	}
	snap = proto.Clone(run).(*agentv1.ProjectRun)
	r.mu.Unlock()
	if changed {
		s.publishRun(snap)
	}
	return snap, err
}

// runOfSession returns the run a session coordinates or works for, and the worker (nil for the coordinator).
func (s *Server) runOfSession(id string) (runID string, coordinator bool) {
	r := &s.runs
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	for _, run := range r.byID {
		if run.GetCoordinatorSessionId() == id {
			return run.GetId(), true
		}
		for _, w := range run.GetWorkers() {
			if w.GetSessionId() == id {
				return run.GetId(), false
			}
		}
	}
	return "", false
}

// runOfSessionOrID returns id when it is a run of this machine.
func (s *Server) runOfSessionOrID(id string) (string, bool) {
	r := &s.runs
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	if r.byID[id] != nil {
		return id, true
	}
	return "", false
}

// publishRun tells the coordinator session's viewers (and the relay, so other devices) about the run, with its graph.
func (s *Server) publishRun(run *agentv1.ProjectRun) {
	ls := s.liveSession(run.GetCoordinatorSessionId())
	if ls == nil {
		return
	}
	out := proto.Clone(run).(*agentv1.ProjectRun)
	if g := ls.sess.ToolEnv.TaskGraph; g != nil {
		out.Tasks = taskGraphProto(g.List()).GetTasks()
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ProjectRunUpdated{ProjectRunUpdated: &agentv1.ProjectRunUpdated{Run: out}}})
}

// attachProjectRun is called when a session opens: a coordinator gets its tools back and shows its run.
func (s *Server) attachProjectRun(ls *liveSession) {
	id, coord := s.runOfSession(ls.sess.ID)
	if id == "" || !coord {
		return
	}
	ls.sess.Coordinator.Store(true)
	if run, err := s.withRun(id, func(*agentv1.ProjectRun) (bool, error) { return false, nil }); err == nil {
		s.publishRun(run)
	}
}

// effectiveLimits caps what is asked by the user's preferences and the policy (the coordinator session's settings).
func effectiveLimits(st *settings.Settings, asked *agentv1.ProjectRunLimits) *agentv1.ProjectRunLimits {
	if st == nil {
		st = &settings.Settings{ProjectMaxWorkers: settings.DefaultProjectMaxWorkers}
	}
	w, b := st.ProjectLimits(int(asked.GetMaxWorkers()), asked.GetBudgetUsd())
	tokens := asked.GetBudgetTokens()
	if tokens < 0 {
		tokens = 0
	}
	return &agentv1.ProjectRunLimits{MaxWorkers: int32(w), BudgetUsd: b, BudgetTokens: tokens}
}

func runTitle(goal string) string {
	t := cutText(goal, 80)
	if t == "" {
		return "Project run"
	}
	return t
}

// beginRun records a run that the open session ls coordinates and gives it the coordinator's tools. The caller briefs it.
func (s *Server) beginRun(ctx context.Context, ls *liveSession, goal, title string, limits *agentv1.ProjectRunLimits) (*agentv1.ProjectRun, error) {
	now := nowRFC()
	host, _ := os.Hostname()
	run := &agentv1.ProjectRun{Id: projectRunPrefix + uuid.NewString(), Goal: goal, Title: title, Cwd: ls.sess.Dir(), CoordinatorSessionId: ls.sess.ID,
		State: agentv1.ProjectRunState_PROJECT_RUN_STATE_PLANNING, Limits: effectiveLimits(ls.sess.Settings, limits), Usage: &agentv1.ProjectUsage{},
		PermissionMode: string(ls.sess.Permissions.Mode()), DeviceId: s.DeviceID, DeviceName: host, CreatedAt: now, UpdatedAt: now}
	run.Targets = s.projectTargets(ctx, run)
	if err := saveRun(run); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("save the run: %w", err))
	}
	r := &s.runs
	r.mu.Lock()
	r.loadLocked()
	r.byID[run.GetId()] = run
	snap := proto.Clone(run).(*agentv1.ProjectRun)
	r.mu.Unlock()
	ls.sess.Coordinator.Store(true)
	s.publishRun(snap)
	return snap, nil
}

// CreateProjectRun implements AgentService: opens the coordinator session, records the run and gives it the goal.
func (s *Server) CreateProjectRun(ctx context.Context, req *connect.Request[agentv1.CreateProjectRunRequest]) (*connect.Response[agentv1.CreateProjectRunResponse], error) {
	m := req.Msg
	goal := strings.TrimSpace(m.GetGoal())
	if goal == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("goal is required"))
	}
	if !filepath.IsAbs(m.GetCwd()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cwd must be an absolute folder"))
	}
	if len(goal) > maxSessionMessage {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the goal is longer than %d bytes", maxSessionMessage))
	}
	create := &agentv1.CreateSessionRequest{Cwd: m.GetCwd(), Model: m.GetModel()}
	if pm := m.GetPermissionMode(); pm != "" {
		mode, err := modeFromProto(modeProtoOf(permissions.Mode(pm)))
		if err != nil || string(mode) != pm {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown permission mode %q", pm))
		}
		create.PermissionMode = modeProtoOf(mode)
	}
	if remote, _ := ctx.Value(remoteOriginKey{}).(bool); remote && create.PermissionMode == agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS {
		create.PermissionMode = agentv1.PermissionMode_PERMISSION_MODE_DEFAULT
	}
	res, err := s.CreateSession(ctx, connect.NewRequest(create))
	if err != nil {
		return nil, err
	}
	sid := res.Msg.GetSession().GetId()
	ls := s.liveSession(sid)
	if ls == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the coordinator session closed right after it opened"))
	}
	if st := ls.sess.Settings; st != nil && st.ProjectRunsDisabled {
		s.closeLive(sid, "project runs disabled")
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("project runs are turned off by your organization's policy"))
	}
	title := strings.TrimSpace(m.GetTitle())
	if title == "" {
		title = runTitle(goal)
	}
	snap, err := s.beginRun(ctx, ls, goal, title, m.GetLimits())
	if err != nil {
		s.closeLive(sid, "project run not saved")
		return nil, err
	}
	if err := ls.setCustomTitle("Project: " + title); err != nil {
		s.log().Warn("project run: title not recorded", "err", err)
	}
	ls.sess.Agent.Remind(agent.ReminderProjectCoordinator, map[string]string{"run_id": snap.GetId(), "limits": limitsText(snap.GetLimits()), "targets": targetsText(snap.GetTargets())})
	if _, err := s.submitMessage(ls, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: uuid.NewString(),
		Content: pbconv.Blocks([]llm.ContentBlock{llm.TextBlock(goal)})})); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.CreateProjectRunResponse{Run: snap}), nil
}

func modeProtoOf(m permissions.Mode) agentv1.PermissionMode { return modeToProto(m) }

func limitsText(l *agentv1.ProjectRunLimits) string {
	parts := []string{fmt.Sprintf("at most %d workers at once", l.GetMaxWorkers())}
	if l.GetBudgetUsd() > 0 {
		parts = append(parts, fmt.Sprintf("a budget of $%.2f", l.GetBudgetUsd()))
	}
	if l.GetBudgetTokens() > 0 {
		parts = append(parts, fmt.Sprintf("a budget of %d tokens", l.GetBudgetTokens()))
	}
	return strings.Join(parts, ", ")
}

func targetsText(ts []*agentv1.ProjectTarget) string {
	var b strings.Builder
	for _, t := range ts {
		state := "available"
		if !t.GetAvailable() {
			state = "not available: " + t.GetReason()
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", t.GetId(), t.GetLabel(), state)
	}
	return strings.TrimRight(b.String(), "\n")
}

// projectTargets lists where workers can run: here, a worktree (in a repository), and the user's hosts that accept new
// sessions (always-on hosts).
func (s *Server) projectTargets(ctx context.Context, run *agentv1.ProjectRun) []*agentv1.ProjectTarget {
	out := []*agentv1.ProjectTarget{{Id: targetHere, Label: "the coordinator's folder", Available: true}}
	wt := &agentv1.ProjectTarget{Id: targetWorktree, Label: "a new git worktree on this machine", Available: true}
	if out, err := exec.CommandContext(ctx, "git", "-C", run.GetCwd(), "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		wt.Available, wt.Reason = false, "the folder is not a git repository"
	}
	out = append([]*agentv1.ProjectTarget{wt}, out...)
	if s.Relay == nil {
		return out
	}
	cctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	res, err := relayCall(cctx, s.Relay, relayv1connect.RelayServiceClient.ListHosts, &relayv1.ListHostsRequest{})
	if err != nil {
		return out
	}
	for _, h := range res.GetHosts() {
		if s.DeviceID != "" && h.GetDeviceId() == s.DeviceID {
			continue
		}
		t := &agentv1.ProjectTarget{Id: h.GetHostId(), Label: h.GetDeviceName(), Remote: true, Available: h.GetOnline() && h.GetDaemon()}
		switch {
		case !h.GetOnline():
			t.Reason = "offline"
		case !h.GetDaemon():
			t.Reason = "does not accept new sessions (not an always-on host)"
		default:
			t.Label += " (folders under " + strings.Join(h.GetAllowedRoots(), ", ") + ")"
		}
		out = append(out, t)
	}
	return out
}

// ---- The coordinator's tools (tools.ProjectCoordinator on the live session) ----

func (ls *liveSession) coordinatedRun() (string, error) {
	id, coord := ls.srv.runOfSession(ls.sess.ID)
	if id == "" || !coord {
		return "", errors.New(noRunHere)
	}
	return id, nil
}

const noRunHere = "This session coordinates no project run."

// ProjectStatus implements tools.ProjectCoordinator.
func (ls *liveSession) ProjectStatus(ctx context.Context) (string, error) {
	id, err := ls.coordinatedRun()
	if err != nil {
		return "", err
	}
	s := ls.srv
	s.refreshRemoteWorkers(ctx, id)
	targets := s.projectTargets(ctx, s.runSnapshot(id))
	run, err := s.withRun(id, func(run *agentv1.ProjectRun) (bool, error) {
		run.Targets = targets
		return false, nil
	})
	if err != nil {
		return "", err
	}
	return runStatusText(run), nil
}

func (s *Server) runSnapshot(id string) *agentv1.ProjectRun {
	run, _ := s.withRun(id, func(*agentv1.ProjectRun) (bool, error) { return false, nil })
	if run == nil {
		return &agentv1.ProjectRun{}
	}
	return run
}

func runStatusText(run *agentv1.ProjectRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s: %s\nState: %s", run.GetId(), run.GetTitle(), strings.TrimPrefix(run.GetState().String(), "PROJECT_RUN_STATE_"))
	if run.GetReason() != "" {
		fmt.Fprintf(&b, " (%s)", run.GetReason())
	}
	u, l := run.GetUsage(), run.GetLimits()
	fmt.Fprintf(&b, "\nLimits: %s\nUsed: $%.4f, %d input and %d output tokens\n", limitsText(l), u.GetCostUsd(), u.GetInputTokens(), u.GetOutputTokens())
	active := 0
	for _, w := range run.GetWorkers() {
		if workerActive(w.GetState()) {
			active++
		}
	}
	fmt.Fprintf(&b, "Workers (%d of %d running):\n", active, l.GetMaxWorkers())
	if len(run.GetWorkers()) == 0 {
		b.WriteString("- none yet\n")
	}
	for _, w := range run.GetWorkers() {
		where := w.GetTarget()
		if w.GetDeviceName() != "" {
			where += " on " + w.GetDeviceName()
		}
		fmt.Fprintf(&b, "- %s (task %s, %s, %s): %s", w.GetName(), w.GetTaskId(), where, strings.TrimPrefix(w.GetState().String(), "PROJECT_WORKER_STATE_"), w.GetTitle())
		if w.GetBranch() != "" {
			fmt.Fprintf(&b, " [branch %s]", w.GetBranch())
		}
		if w.GetReport() != "" {
			fmt.Fprintf(&b, "\n  report: %s", w.GetReport())
		}
		b.WriteString("\n")
	}
	b.WriteString("Targets:\n")
	b.WriteString(targetsText(run.GetTargets()))
	return b.String()
}

// budgetReason says why the run is over its budget ("" when it is not).
func budgetReason(run *agentv1.ProjectRun) string {
	u, l := run.GetUsage(), run.GetLimits()
	if b := l.GetBudgetUsd(); b > 0 && u.GetCostUsd() >= b {
		return fmt.Sprintf("Budget reached: $%.2f of $%.2f spent", u.GetCostUsd(), b)
	}
	if b := l.GetBudgetTokens(); b > 0 && u.GetInputTokens()+u.GetOutputTokens() >= b {
		return fmt.Sprintf("Budget reached: %d of %d tokens used", u.GetInputTokens()+u.GetOutputTokens(), b)
	}
	return ""
}

// workerMode is the mode a worker gets: the coordinator's, never more; bypass never on another machine; a mode the
// policy disables falls back to default.
func workerMode(coord permissions.Mode, remote bool, st *settings.Settings) permissions.Mode {
	m := coord
	if remote && m == permissions.ModeBypassPermissions {
		m = permissions.ModeDefault
	}
	if st != nil {
		for _, d := range st.Permissions.DisabledModes {
			if string(m) == d {
				m = permissions.ModeDefault
			}
		}
		if (st.Permissions.DisableBypass && m == permissions.ModeBypassPermissions) || (st.Permissions.DisableAutoMode && m == permissions.ModeAuto) {
			m = permissions.ModeDefault
		}
	}
	return m
}

func workerPrompt(run *agentv1.ProjectRun, task tools.GraphTask, prompt string) string {
	return fmt.Sprintf("You are a worker of the project run %q, task %s: %s\n\n%s\n\nWork only on this task. End your turn with a short report: what you did, what is left, and the branch or files that hold the result.",
		run.GetTitle(), task.ID, task.Title, prompt)
}

// ProjectWorkerStart implements tools.ProjectCoordinator.
func (ls *liveSession) ProjectWorkerStart(ctx context.Context, r tools.ProjectWorkerRequest) (string, error) {
	id, err := ls.coordinatedRun()
	if err != nil {
		return "", err
	}
	s := ls.srv
	graph := ls.sess.ToolEnv.TaskGraph
	if graph == nil {
		return "", errors.New("this session has no task graph")
	}
	var task tools.GraphTask
	found := false
	for _, t := range graph.List() {
		if t.ID == r.TaskID {
			task, found = t, true
		}
	}
	switch {
	case !found:
		return "", fmt.Errorf("no task %s in the graph: add it with TaskGraphAdd first", r.TaskID)
	case task.Status != tools.GraphPending:
		return "", fmt.Errorf("task %s is %s, not pending", task.ID, task.Status)
	case len(task.WaitingFor) > 0:
		return "", fmt.Errorf("task %s waits for %s", task.ID, strings.Join(task.WaitingFor, ", "))
	}
	target := r.Target
	if target == "" {
		target = targetWorktree
	}
	// The limits, checked and the worker reserved under the run's lock, so two starts can't both take the last slot.
	name := "w-" + strings.TrimPrefix(task.ID, "t") + "-" + uuid.NewString()[:4]
	worker := &agentv1.ProjectWorker{Name: name, TaskId: task.ID, Title: task.Title, State: agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_STARTING,
		Target: target, Usage: &agentv1.ProjectUsage{}, StartedAt: nowRFC(), UpdatedAt: nowRFC()}
	var runSnap *agentv1.ProjectRun
	if runSnap, err = s.withRun(id, func(run *agentv1.ProjectRun) (bool, error) {
		switch st := run.GetState(); {
		case runEnded(st):
			return false, fmt.Errorf("the run has ended (%s)", strings.ToLower(strings.TrimPrefix(st.String(), "PROJECT_RUN_STATE_")))
		case st == agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED:
			return false, fmt.Errorf("the run is paused: %s. Tell the user and wait", run.GetReason())
		}
		if why := budgetReason(run); why != "" {
			return false, fmt.Errorf("%s: no more workers start. Tell the user", why)
		}
		active := 0
		for _, w := range run.GetWorkers() {
			if workerActive(w.GetState()) {
				active++
			}
		}
		if active >= int(run.GetLimits().GetMaxWorkers()) {
			return false, fmt.Errorf("%d workers run already, the run's limit: wait for one to report", active)
		}
		run.Workers = append(run.Workers, worker)
		if run.State == agentv1.ProjectRunState_PROJECT_RUN_STATE_PLANNING {
			run.State = agentv1.ProjectRunState_PROJECT_RUN_STATE_RUNNING
		}
		return true, nil
	}); err != nil {
		return "", err
	}
	// The task is the worker's before it starts: a quick worker may report before the start returns.
	dropWorker := func() {
		_, _ = s.withRun(id, func(run *agentv1.ProjectRun) (bool, error) {
			kept := run.Workers[:0]
			for _, w := range run.GetWorkers() {
				if w.GetName() != name {
					kept = append(kept, w)
				}
			}
			run.Workers = kept
			return true, nil
		})
	}
	if _, cerr := graph.Claim(name, name, "", task.ID); cerr != nil {
		dropWorker()
		return "", fmt.Errorf("task %s could not be taken: %w", task.ID, cerr)
	}
	prompt := workerPrompt(runSnap, task, r.Prompt)
	coordMode := ls.sess.Permissions.Mode()
	var started *agentv1.ProjectWorker
	switch target {
	case targetWorktree, targetHere:
		mode := workerMode(coordMode, false, ls.sess.Settings)
		started, err = ls.startLocalWorker(ctx, task, prompt, target == targetWorktree, r.Model, mode)
	default:
		mode := workerMode(coordMode, true, ls.sess.Settings)
		started, err = s.startRemoteWorker(ctx, runSnap, target, r.Cwd, r.Model, task, prompt, mode)
	}
	if err != nil {
		dropWorker()
		pending := tools.GraphPending
		_, _ = graph.Update("", tools.GraphUpdate{ID: task.ID, Status: &pending})
		return "", err
	}
	_, _ = s.withRun(id, func(run *agentv1.ProjectRun) (bool, error) {
		for _, w := range run.GetWorkers() {
			if w.GetName() == name {
				w.SessionId, w.State, w.Remote = started.GetSessionId(), agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_RUNNING, started.GetRemote()
				w.DeviceId, w.DeviceName, w.Branch, w.WorktreePath = started.GetDeviceId(), started.GetDeviceName(), started.GetBranch(), started.GetWorktreePath()
				w.UpdatedAt = nowRFC()
			}
		}
		return true, nil
	})
	// It may have reported already, before it was on record.
	s.syncWorkerFromInbox(started.GetSessionId())
	if started.GetRemote() {
		s.followRemoteWorkers(id)
	}
	out := fmt.Sprintf("Started worker %s for task %s (session %s, %s).", name, task.ID, started.GetSessionId(), target)
	if started.GetBranch() != "" {
		out += " It works on branch " + started.GetBranch() + "."
	}
	if started.GetRemote() {
		out += " It runs on " + started.GetDeviceName() + "; ProjectStatus shows its state and report."
	} else {
		out += " Its report comes back to you when it ends a turn."
	}
	return out, nil
}

// startLocalWorker starts a linked session here (the SessionStart path) in the given mode.
func (ls *liveSession) startLocalWorker(ctx context.Context, task tools.GraphTask, prompt string, worktree bool, model string, mode permissions.Mode) (*agentv1.ProjectWorker, error) {
	linked, err := ls.StartSessionMode(ctx, tools.SessionRequest{Title: task.ID + ": " + task.Title, Prompt: prompt, Worktree: worktree, Model: model}, mode)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	return &agentv1.ProjectWorker{SessionId: linked.ID, DeviceId: ls.srv.DeviceID, DeviceName: host, Branch: linked.WorktreeBranch, WorktreePath: linked.WorktreePath}, nil
}

// startRemoteWorker spawns a session on one of the user's hosts through the relay.
func (s *Server) startRemoteWorker(ctx context.Context, run *agentv1.ProjectRun, hostID, cwd, model string, task tools.GraphTask, prompt string, mode permissions.Mode) (*agentv1.ProjectWorker, error) {
	if s.Relay == nil {
		return nil, fmt.Errorf("target %q: this client is not connected to the relay; use %q or %q", hostID, targetWorktree, targetHere)
	}
	var host *agentv1.ProjectTarget
	for _, t := range s.projectTargets(ctx, run) {
		if t.GetId() == hostID {
			host = t
		}
	}
	switch {
	case host == nil || !host.GetRemote():
		return nil, fmt.Errorf("unknown target %q: ProjectStatus lists the targets", hostID)
	case !host.GetAvailable():
		return nil, fmt.Errorf("host %s is %s", host.GetLabel(), host.GetReason())
	}
	if cwd == "" {
		cwd = run.GetCwd()
	}
	create := &agentv1.CreateSessionRequest{Cwd: cwd, Model: model, PermissionMode: modeToProto(mode)}
	first := &agentv1.SendUserMessageRequest{Uuid: uuid.NewString(), Content: pbconv.Blocks([]llm.ContentBlock{llm.TextBlock(prompt)})}
	cctx, cancel := context.WithTimeout(ctx, 2*relayCommandTimeout)
	defer cancel()
	res, err := relayCall(cctx, s.Relay, relayv1connect.RelayServiceClient.SpawnSession, &relayv1.SpawnSessionRequest{HostId: hostID,
		Spawn: &relayv1.SpawnSessionCommand{Create: create, InitialMessage: first, Worktree: true}, Options: commandOptions("spawn-" + run.GetId() + "-" + task.ID + "-" + first.Uuid)})
	if err != nil {
		return nil, relayError(err)
	}
	info := res.GetSession()
	return &agentv1.ProjectWorker{SessionId: info.GetId(), Remote: true, DeviceId: info.GetDeviceId(), DeviceName: host.GetLabel(),
		Branch: info.GetWorktree().GetBranch(), WorktreePath: info.GetWorktree().GetPath()}, nil
}

// ProjectWorkerMerge implements tools.ProjectCoordinator.
func (ls *liveSession) ProjectWorkerMerge(ctx context.Context, who string) (string, error) {
	id, err := ls.coordinatedRun()
	if err != nil {
		return "", err
	}
	s := ls.srv
	var w *agentv1.ProjectWorker
	run, _ := s.withRun(id, func(run *agentv1.ProjectRun) (bool, error) { return false, nil })
	for _, x := range run.GetWorkers() {
		if x.GetName() == who || x.GetSessionId() == who || x.GetTaskId() == who {
			w = x
		}
	}
	switch {
	case w == nil:
		return "", fmt.Errorf("no worker %q", who)
	case w.GetRemote():
		return "", fmt.Errorf("worker %s runs on %s: its branch %q is there; ask it to push and merge from the remote", w.GetName(), w.GetDeviceName(), w.GetBranch())
	case w.GetBranch() == "":
		return "", fmt.Errorf("worker %s worked in the coordinator's folder: there is nothing to merge", w.GetName())
	case workerActive(w.GetState()):
		return "", fmt.Errorf("worker %s is still working", w.GetName())
	case w.GetState() == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_MERGED:
		return "", fmt.Errorf("worker %s is merged already", w.GetName())
	}
	_, _ = s.withRun(id, func(run *agentv1.ProjectRun) (bool, error) {
		run.State = agentv1.ProjectRunState_PROJECT_RUN_STATE_MERGING
		return true, nil
	})
	gctx, cancel := context.WithTimeout(ctx, projectGitTimeout)
	defer cancel()
	dir := ls.sess.Dir()
	out, mergeErr := exec.CommandContext(gctx, "git", "-C", dir, "merge", "--no-ff", "--no-edit", w.GetBranch()).CombinedOutput()
	if mergeErr != nil {
		_ = exec.CommandContext(gctx, "git", "-C", dir, "merge", "--abort").Run()
	}
	_, _ = s.withRun(id, func(run *agentv1.ProjectRun) (bool, error) {
		if run.State == agentv1.ProjectRunState_PROJECT_RUN_STATE_MERGING {
			run.State = agentv1.ProjectRunState_PROJECT_RUN_STATE_RUNNING
		}
		for _, x := range run.GetWorkers() {
			if x.GetName() == w.GetName() && mergeErr == nil {
				x.State, x.UpdatedAt = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_MERGED, nowRFC()
			}
		}
		return true, nil
	})
	if mergeErr != nil {
		return "", fmt.Errorf("merging %s failed and was aborted, the branch is as it was:\n%s", w.GetBranch(), cutText(string(out), 2000))
	}
	return fmt.Sprintf("Merged %s into the coordinator's branch.\n%s", w.GetBranch(), cutText(string(out), 1000)), nil
}

// ProjectFinish implements tools.ProjectCoordinator.
func (ls *liveSession) ProjectFinish(ctx context.Context, ok bool, summary string) (string, error) {
	id, err := ls.coordinatedRun()
	if err != nil {
		return "", err
	}
	running := 0
	run, err := ls.srv.withRun(id, func(run *agentv1.ProjectRun) (bool, error) {
		if runEnded(run.GetState()) {
			return false, errors.New("the run has ended already")
		}
		for _, w := range run.GetWorkers() {
			if workerActive(w.GetState()) {
				running++
			}
		}
		run.State, run.Reason = agentv1.ProjectRunState_PROJECT_RUN_STATE_DONE, cutText(summary, 2000)
		if !ok {
			run.State = agentv1.ProjectRunState_PROJECT_RUN_STATE_FAILED
		}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	out := "The run is " + strings.ToLower(strings.TrimPrefix(run.GetState().String(), "PROJECT_RUN_STATE_")) + "."
	if running > 0 {
		out += fmt.Sprintf(" %d workers still run; they finish their turn on their own.", running)
	}
	return out, nil
}

// ---- Following sessions ----

// projectObserve updates a run from an event of its coordinator or one of its workers (called after the inbox fold,
// with the session's item).
func (s *Server) projectObserve(sessionID string, item *agentv1.InboxItem, group agentv1.InboxGroup, ev *agentv1.SessionEvent) {
	switch ev.GetEvent().(type) {
	case *agentv1.SessionEvent_TurnStarted, *agentv1.SessionEvent_TurnFinished, *agentv1.SessionEvent_PermissionRequest,
		*agentv1.SessionEvent_PermissionResolved, *agentv1.SessionEvent_Question, *agentv1.SessionEvent_QuestionResolved,
		*agentv1.SessionEvent_PlanProposed, *agentv1.SessionEvent_PlanResolved, *agentv1.SessionEvent_SessionEnded, *agentv1.SessionEvent_TaskGraphUpdated:
	default:
		return
	}
	id, coord := s.runOfSession(sessionID)
	if id == "" {
		return
	}
	if _, isGraph := ev.GetEvent().(*agentv1.SessionEvent_TaskGraphUpdated); isGraph {
		if coord {
			s.publishRun(s.runSnapshot(id))
		}
		return
	}
	var finished *agentv1.ProjectWorker
	_, _ = s.withRun(id, func(run *agentv1.ProjectRun) (bool, error) {
		if coord {
			return s.applyCoordinator(run, item, ev), nil
		}
		for _, w := range run.GetWorkers() {
			if w.GetSessionId() != sessionID {
				continue
			}
			before := w.GetState()
			applyWorker(w, item, group, ev)
			if before != w.GetState() && (w.GetState() == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_FINISHED || w.GetState() == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_FAILED) {
				finished = proto.Clone(w).(*agentv1.ProjectWorker)
			}
		}
		sumUsage(run)
		s.checkBudget(run)
		return true, nil
	})
	if finished != nil {
		s.closeWorkerTask(id, finished)
	}
}

// syncWorkerFromInbox applies what the inbox folded of a local session so far (a worker that ended its turn before it
// was on record).
func (s *Server) syncWorkerFromInbox(sessionID string) {
	in := &s.inbox
	in.mu.Lock()
	in.init()
	f := in.folds[sessionID]
	var item *agentv1.InboxItem
	var group agentv1.InboxGroup
	if f != nil {
		item, group = proto.Clone(f.item).(*agentv1.InboxItem), f.group()
	}
	in.mu.Unlock()
	if item != nil {
		s.projectObserve(sessionID, item, group, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: &agentv1.TurnFinished{}}})
	}
}

// applyCoordinator: the coordinator waiting for the user puts the run in waiting_for_user; its usage counts.
func (s *Server) applyCoordinator(run *agentv1.ProjectRun, item *agentv1.InboxItem, ev *agentv1.SessionEvent) bool {
	st := run.GetState()
	if runEnded(st) || st == agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED {
		if _, ok := ev.GetEvent().(*agentv1.SessionEvent_TurnFinished); ok {
			sumUsageWith(run, item)
			return true
		}
		return false
	}
	waiting := false
	for _, r := range item.GetRequests() {
		waiting = waiting || r.GetKind() != agentv1.InboxRequest_KIND_PROJECT_RUN
	}
	switch {
	case waiting:
		run.State = agentv1.ProjectRunState_PROJECT_RUN_STATE_WAITING_FOR_USER
	case st == agentv1.ProjectRunState_PROJECT_RUN_STATE_WAITING_FOR_USER:
		run.State = agentv1.ProjectRunState_PROJECT_RUN_STATE_RUNNING
	}
	sumUsageWith(run, item)
	s.checkBudget(run)
	return true
}

// coordinatorUsage is kept apart from the workers' in the record: usage = coordinator + workers.
var coordinatorUsage sync.Map // run id -> *agentv1.ProjectUsage

func sumUsageWith(run *agentv1.ProjectRun, coord *agentv1.InboxItem) {
	coordinatorUsage.Store(run.GetId(), &agentv1.ProjectUsage{InputTokens: coord.GetInputTokens(), OutputTokens: coord.GetOutputTokens(), CostUsd: coord.GetCostUsd()})
	sumUsage(run)
}

func sumUsage(run *agentv1.ProjectRun) {
	total := &agentv1.ProjectUsage{}
	if v, ok := coordinatorUsage.Load(run.GetId()); ok {
		c := v.(*agentv1.ProjectUsage)
		total.InputTokens, total.OutputTokens, total.CostUsd = c.GetInputTokens(), c.GetOutputTokens(), c.GetCostUsd()
	} else if u := run.GetUsage(); u != nil {
		// After a restart the coordinator's part is what the record held beyond the workers'.
		var w agentv1.ProjectUsage
		for _, x := range run.GetWorkers() {
			w.InputTokens += x.GetUsage().GetInputTokens()
			w.OutputTokens += x.GetUsage().GetOutputTokens()
			w.CostUsd += x.GetUsage().GetCostUsd()
		}
		c := &agentv1.ProjectUsage{InputTokens: max(0, u.GetInputTokens()-w.InputTokens), OutputTokens: max(0, u.GetOutputTokens()-w.OutputTokens), CostUsd: max(0, u.GetCostUsd()-w.CostUsd)}
		coordinatorUsage.Store(run.GetId(), c)
		total.InputTokens, total.OutputTokens, total.CostUsd = c.InputTokens, c.OutputTokens, c.CostUsd
	}
	for _, x := range run.GetWorkers() {
		total.InputTokens += x.GetUsage().GetInputTokens()
		total.OutputTokens += x.GetUsage().GetOutputTokens()
		total.CostUsd += x.GetUsage().GetCostUsd()
	}
	run.Usage = total
}

// checkBudget pauses a going run at its budget.
func (s *Server) checkBudget(run *agentv1.ProjectRun) {
	st := run.GetState()
	if runEnded(st) || st == agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED {
		return
	}
	if why := budgetReason(run); why != "" {
		run.State, run.Reason = agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED, why+". No more workers start; raise the limit or stop the run."
		if ls := s.liveSession(run.GetCoordinatorSessionId()); ls != nil {
			go ls.notice(agentv1.Notice_LEVEL_WARNING, "project_budget", run.GetReason())
		}
	}
}

func applyWorker(w *agentv1.ProjectWorker, item *agentv1.InboxItem, group agentv1.InboxGroup, ev *agentv1.SessionEvent) {
	w.Usage = &agentv1.ProjectUsage{InputTokens: item.GetInputTokens(), OutputTokens: item.GetOutputTokens(), CostUsd: item.GetCostUsd()}
	if r := item.GetReport(); r != "" {
		w.Report = cutText(r, projectReportChars)
	}
	if e := item.GetError(); e != "" && group == agentv1.InboxGroup_INBOX_GROUP_FAILED {
		w.Report = cutText(e, projectReportChars)
	}
	if w.GetState() == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_MERGED {
		return
	}
	switch {
	case group == agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT:
		w.State = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_NEEDS_INPUT
	case group == agentv1.InboxGroup_INBOX_GROUP_RUNNING:
		w.State = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_RUNNING
	case group == agentv1.InboxGroup_INBOX_GROUP_FAILED:
		w.State = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_FAILED
	case group == agentv1.InboxGroup_INBOX_GROUP_FINISHED:
		w.State = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_FINISHED
	}
	if _, ended := ev.GetEvent().(*agentv1.SessionEvent_SessionEnded); ended && workerActive(w.GetState()) {
		w.State = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_STOPPED
	}
	if tf, ok := ev.GetEvent().(*agentv1.SessionEvent_TurnFinished); ok && tf.TurnFinished.GetResult() == agentv1.TurnFinished_RESULT_INTERRUPTED {
		w.State = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_STOPPED
	}
	w.UpdatedAt = nowRFC()
}

// closeWorkerTask records a worker's outcome on its task of the coordinator's graph.
func (s *Server) closeWorkerTask(runID string, w *agentv1.ProjectWorker) {
	run := s.runSnapshot(runID)
	ls := s.liveSession(run.GetCoordinatorSessionId())
	if ls == nil || ls.sess.ToolEnv.TaskGraph == nil {
		return
	}
	status := tools.GraphDone
	if w.GetState() == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_FAILED {
		status = tools.GraphFailed
	}
	note := w.GetName() + ": " + w.GetReport()
	if _, err := ls.sess.ToolEnv.TaskGraph.Update("", tools.GraphUpdate{ID: w.GetTaskId(), Status: &status, Note: &note}); err != nil {
		s.log().Debug("project run: task not closed", "task", w.GetTaskId(), "err", err)
	}
	if w.GetRemote() {
		// A remote worker is not linked to the coordinator: its report comes as a note from here.
		n := session.SessionNote{ID: newUUID("note_"), Kind: noteReport, FromID: w.GetSessionId(), FromName: w.GetName(), FromTitle: w.GetTitle(),
			Status: strings.ToLower(strings.TrimPrefix(w.GetState().String(), "PROJECT_WORKER_STATE_")), Text: w.GetReport()}
		ls.deliverNote(n)
	}
}

// refreshRemoteWorkers reads the remote workers' items from the server's inbox and folds them in.
func (s *Server) refreshRemoteWorkers(ctx context.Context, runID string) (active int) {
	if s.Relay == nil {
		return 0
	}
	run := s.runSnapshot(runID)
	want := map[string]bool{}
	for _, w := range run.GetWorkers() {
		if w.GetRemote() && w.GetSessionId() != "" {
			want[w.GetSessionId()] = true
		}
	}
	if len(want) == 0 {
		return 0
	}
	found := map[string]*agentv1.InboxItem{}
	for _, g := range inboxGroups {
		cctx, cancel := context.WithTimeout(ctx, inboxCallTimeout)
		res, err := inboxCall(cctx, s.Relay, s.Relay.inbox.ListInbox, &relayv1.ListInboxRequest{Request: &agentv1.ListInboxRequest{Group: g, PageSize: inboxPageMax}})
		cancel()
		if err != nil {
			return 1
		}
		for _, it := range res.GetResponse().GetItems() {
			if want[it.GetSessionId()] {
				found[it.GetSessionId()] = it
			}
		}
	}
	var done []*agentv1.ProjectWorker
	_, _ = s.withRun(runID, func(run *agentv1.ProjectRun) (bool, error) {
		changed := false
		for _, w := range run.GetWorkers() {
			if !w.GetRemote() {
				continue
			}
			it := found[w.GetSessionId()]
			before := w.GetState()
			if it == nil {
				// Not in any group: idle with nothing to report, or closed and read.
				if workerActive(before) && before != agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_STARTING {
					w.State = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_FINISHED
				}
			} else {
				applyWorker(w, it, it.GetGroup(), &agentv1.SessionEvent{})
			}
			if before != w.GetState() {
				changed = true
				if !workerActive(w.GetState()) {
					done = append(done, proto.Clone(w).(*agentv1.ProjectWorker))
				}
			}
			if workerActive(w.GetState()) {
				active++
			}
		}
		if changed {
			sumUsage(run)
			s.checkBudget(run)
		}
		return changed, nil
	})
	for _, w := range done {
		s.closeWorkerTask(runID, w)
	}
	return active
}

// followRemoteWorkers polls the server's inbox for a run's remote workers until none of them works any more.
func (s *Server) followRemoteWorkers(runID string) {
	r := &s.runs
	r.mu.Lock()
	if r.polling[runID] {
		r.mu.Unlock()
		return
	}
	r.polling[runID] = true
	r.mu.Unlock()
	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.polling, runID)
			r.mu.Unlock()
		}()
		t := time.NewTicker(projectPollEvery)
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-t.C:
			}
			if s.refreshRemoteWorkers(s.ctx, runID) == 0 {
				return
			}
		}
	}()
}

// ---- RPCs ----

// ListProjectRuns implements AgentService.
func (s *Server) ListProjectRuns(ctx context.Context, req *connect.Request[agentv1.ListProjectRunsRequest]) (*connect.Response[agentv1.ListProjectRunsResponse], error) {
	r := &s.runs
	r.mu.Lock()
	r.loadLocked()
	ids := make([]string, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	out := &agentv1.ListProjectRunsResponse{}
	local := map[string]bool{}
	for _, id := range ids {
		run := s.runWithGraph(id)
		local[id] = true
		out.Runs = append(out.Runs, run)
	}
	if req.Msg.GetIncludeRemote() && s.Relay != nil {
		cctx, cancel := context.WithTimeout(ctx, inboxCallTimeout)
		res, err := inboxCall(cctx, s.Relay, s.Relay.inbox.ListInbox, &relayv1.ListInboxRequest{Request: &agentv1.ListInboxRequest{ProjectRunsOnly: true, PageSize: inboxPageMax}})
		cancel()
		if err == nil {
			for _, it := range res.GetResponse().GetItems() {
				if run := it.GetProjectRun(); run != nil && !local[run.GetId()] {
					out.Runs = append(out.Runs, run)
				}
			}
		}
	}
	sort.SliceStable(out.Runs, func(i, j int) bool { return out.Runs[i].GetCreatedAt() > out.Runs[j].GetCreatedAt() })
	return connect.NewResponse(out), nil
}

// runWithGraph is the run with its coordinator's task graph when that is open.
func (s *Server) runWithGraph(id string) *agentv1.ProjectRun {
	run := s.runSnapshot(id)
	if ls := s.liveSession(run.GetCoordinatorSessionId()); ls != nil && ls.sess.ToolEnv.TaskGraph != nil {
		run.Tasks = taskGraphProto(ls.sess.ToolEnv.TaskGraph.List()).GetTasks()
	}
	return run
}

// GetProjectRun implements AgentService.
func (s *Server) GetProjectRun(ctx context.Context, req *connect.Request[agentv1.GetProjectRunRequest]) (*connect.Response[agentv1.GetProjectRunResponse], error) {
	if _, err := s.withRun(req.Msg.GetId(), func(*agentv1.ProjectRun) (bool, error) { return false, nil }); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.GetProjectRunResponse{Run: s.runWithGraph(req.Msg.GetId())}), nil
}

// ControlProjectRun implements AgentService for a run of this machine (routed forwards the others).
func (s *Server) ControlProjectRun(ctx context.Context, req *connect.Request[agentv1.ControlProjectRunRequest]) (*connect.Response[agentv1.ControlProjectRunResponse], error) {
	m := req.Msg
	id := m.GetId()
	var coordinator string
	run, err := s.withRun(id, func(run *agentv1.ProjectRun) (bool, error) {
		coordinator = run.GetCoordinatorSessionId()
		changed := false
		if l := m.GetLimits(); l != nil {
			var st *settings.Settings
			if ls := s.liveSession(coordinator); ls != nil {
				st = ls.sess.Settings
			}
			run.Limits, changed = effectiveLimits(st, l), true
		}
		switch m.GetAction() {
		case agentv1.ControlProjectRunRequest_ACTION_SET_LIMITS:
			if m.GetLimits() == nil {
				return false, connect.NewError(connect.CodeInvalidArgument, errors.New("limits are required"))
			}
			if run.GetState() == agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED && budgetReason(run) == "" && strings.HasPrefix(run.GetReason(), "Budget reached") {
				run.State, run.Reason = agentv1.ProjectRunState_PROJECT_RUN_STATE_RUNNING, ""
			}
		case agentv1.ControlProjectRunRequest_ACTION_PAUSE:
			if runEnded(run.GetState()) {
				return false, connect.NewError(connect.CodeFailedPrecondition, errors.New("the run has ended"))
			}
			run.State, run.Reason, changed = agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED, "Paused by you", true
		case agentv1.ControlProjectRunRequest_ACTION_RESUME:
			if runEnded(run.GetState()) {
				return false, connect.NewError(connect.CodeFailedPrecondition, errors.New("the run has ended"))
			}
			if why := budgetReason(run); why != "" {
				return false, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s: raise the limit first", why))
			}
			run.State, run.Reason, changed = agentv1.ProjectRunState_PROJECT_RUN_STATE_RUNNING, "", true
		case agentv1.ControlProjectRunRequest_ACTION_STOP:
			if runEnded(run.GetState()) {
				return false, nil
			}
			run.State, run.Reason, changed = agentv1.ProjectRunState_PROJECT_RUN_STATE_STOPPED, "Stopped by you", true
		default:
			if !changed {
				return false, connect.NewError(connect.CodeInvalidArgument, errors.New("action is required"))
			}
		}
		return changed, nil
	})
	if err != nil {
		return nil, err
	}
	switch m.GetAction() {
	case agentv1.ControlProjectRunRequest_ACTION_RESUME:
		if err := s.resumeCoordinator(ctx, run); err != nil {
			return nil, err
		}
	case agentv1.ControlProjectRunRequest_ACTION_STOP:
		s.stopRun(ctx, run)
	}
	return connect.NewResponse(&agentv1.ControlProjectRunResponse{Run: s.runWithGraph(id)}), nil
}

// resumeCoordinator reopens the coordinator session if needed and tells it the state.
func (s *Server) resumeCoordinator(ctx context.Context, run *agentv1.ProjectRun) error {
	sid := run.GetCoordinatorSessionId()
	ls := s.liveSession(sid)
	if ls == nil {
		if _, err := s.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid})); err != nil {
			return err
		}
		if ls = s.liveSession(sid); ls == nil {
			return connect.NewError(connect.CodeInternal, errors.New("the coordinator session closed right after it opened"))
		}
	}
	s.refreshRemoteWorkers(ctx, run.GetId())
	ls.sess.Coordinator.Store(true)
	ls.sess.Agent.Remind(agent.ReminderProjectResumed, map[string]string{"status": runStatusText(s.runSnapshot(run.GetId()))})
	if !ls.driver.Busy() {
		ls.driver.Wake()
	}
	s.publishRun(s.runSnapshot(run.GetId()))
	return nil
}

// stopRun interrupts the coordinator and every worker that works.
func (s *Server) stopRun(ctx context.Context, run *agentv1.ProjectRun) {
	if ls := s.liveSession(run.GetCoordinatorSessionId()); ls != nil {
		ls.driver.Interrupt()
	}
	for _, w := range run.GetWorkers() {
		if !workerActive(w.GetState()) {
			continue
		}
		if ls := s.liveSession(w.GetSessionId()); ls != nil {
			ls.driver.Interrupt()
			continue
		}
		if w.GetRemote() && s.Relay != nil {
			cctx, cancel := context.WithTimeout(ctx, relayCommandTimeout)
			_, err := relayCall(cctx, s.Relay, relayv1connect.RelayServiceClient.Interrupt, &relayv1.InterruptRequest{Request: &agentv1.InterruptRequest{SessionId: w.GetSessionId()}, Options: commandOptions("stop-" + w.GetSessionId())})
			cancel()
			if err != nil {
				s.log().Debug("project run: remote worker not stopped", "session", w.GetSessionId(), "err", err)
			}
		}
	}
	_, _ = s.withRun(run.GetId(), func(r *agentv1.ProjectRun) (bool, error) {
		for _, w := range r.GetWorkers() {
			if workerActive(w.GetState()) {
				w.State, w.UpdatedAt = agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_STOPPED, nowRFC()
			}
		}
		return true, nil
	})
}

// DeleteProjectRun implements AgentService: forgets an ended run (its sessions stay).
func (s *Server) DeleteProjectRun(ctx context.Context, req *connect.Request[agentv1.DeleteProjectRunRequest]) (*connect.Response[agentv1.DeleteProjectRunResponse], error) {
	id := req.Msg.GetId()
	r := &s.runs
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	run := r.byID[id]
	switch {
	case run == nil:
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project run %s not found on this machine", id))
	case !runEnded(run.GetState()) && run.GetState() != agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("stop the run first"))
	}
	if err := os.Remove(projectRunPath(id)); err != nil && !os.IsNotExist(err) {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	delete(r.byID, id)
	coordinatorUsage.Delete(id)
	return connect.NewResponse(&agentv1.DeleteProjectRunResponse{}), nil
}
