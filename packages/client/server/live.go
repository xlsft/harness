package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/tools"
	"google.golang.org/protobuf/proto"
)

type permissionAnswer struct {
	d        permissions.Decision
	decision agentv1.RespondToPermissionRequest_Decision
}

type pendingPermission struct {
	ch          chan permissionAnswer
	suggestions []string
}

type planAnswer struct {
	resp     tools.PlanResponse
	decision agentv1.RespondToPlanRequest_Decision
}

type liveSession struct {
	srv    *Server
	sess   *bootstrap.Session
	driver *agent.Driver
	hub    *hub
	files  fileCache

	// infoMu orders SessionUpdated events: the snapshot is taken and
	// published under it, so an older snapshot never follows a newer one.
	infoMu sync.Mutex

	mu          sync.Mutex
	status      agentv1.SessionStatus
	pendingPerm map[string]*pendingPermission
	pendingQ    map[string]*pendingQuestion
	pendingPlan map[string]chan planAnswer
	waiting     int
	queue       []*agentv1.UserMessage
	seen        map[string]bool
	messageID   string
	branch      string
	title       string
	titleSource titleSource
	// titleTurns counts finished turns while the title is provisional;
	// titleTried is set once the AI title request started.
	titleTurns int
	titleTried bool
	diff       *agentv1.DiffStats
	// history is the number of events replayed from the transcript at open.
	history   int64
	updatedAt time.Time
	closed    bool
	// env is the environment state of the session (environment.go).
	env sessionEnv
	// compacting is set while a manual or idle compaction runs (runCompaction); idleCompacted once the idle
	// compactor has handled the session, until the next turn starts (idle.go).
	compacting    atomic.Bool
	idleCompacted bool
	// shells are the user shell commands of the session (usershell.go).
	shells userShells
	// toolTitles tracks the Bash tool title requests (tooltitle.go; ls.mu).
	toolTitles toolTitles
	// relayChoice is the remote control switch on record (nil: settings decide; ls.mu); relayMu orders
	// SetSessionRelay calls (relay.go).
	relayChoice *bool
	relayMu     sync.Mutex

	// ctx ends when the session closes; bg tracks its background work
	// (title, diff stats), which close waits for.
	ctx    context.Context
	cancel context.CancelFunc
	bg     sync.WaitGroup
}

var (
	_ tools.Interactor = (*liveSession)(nil)
	_ tools.Shower     = (*liveSession)(nil)
)

func newLiveSession(s *Server) *liveSession {
	ctx, cancel := context.WithCancel(s.ctx)
	return &liveSession{srv: s, hub: newHub(), seen: map[string]bool{}, status: agentv1.SessionStatus_SESSION_STATUS_IDLE,
		pendingPerm: map[string]*pendingPermission{}, pendingQ: map[string]*pendingQuestion{}, pendingPlan: map[string]chan planAnswer{},
		ctx: ctx, cancel: cancel}
}

func (ls *liveSession) info() *agentv1.SessionInfo {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	updated := ls.updatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	info := &agentv1.SessionInfo{
		Id: ls.sess.ID, Cwd: ls.sess.Cwd, Model: ls.sess.Agent.Model(), GitBranch: ls.branch, Title: ls.title,
		PermissionMode: modeToProto(ls.sess.Permissions.Mode()), Status: ls.statusLocked(),
		UpdatedAt: updated.UTC().Format(time.RFC3339), ParentSessionId: ls.sess.ParentSessionID,
		OutputStyle: ls.sess.ActiveOutputStyle(), Worktree: worktreeToProto(ls.sess.Worktree), DiffStats: ls.diff,
		NoFolder: isNoFolder(ls.sess.ID, ls.sess.Cwd),
	}
	ls.relayStateLocked(info)
	ls.srv.applyState(info)
	return info
}

// statusLocked is the session status (ls.mu held). A busy status without a
// running turn is never reported: whatever left it behind, nothing runs.
func (ls *liveSession) statusLocked() agentv1.SessionStatus {
	st := ls.status
	busy := st == agentv1.SessionStatus_SESSION_STATUS_WORKING || st == agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT
	if busy && ls.driver != nil && !ls.driver.Busy() {
		return agentv1.SessionStatus_SESSION_STATUS_IDLE
	}
	return st
}

// publishInfo publishes SessionUpdated with the session as it is now.
func (ls *liveSession) publishInfo() {
	ls.infoMu.Lock()
	defer ls.infoMu.Unlock()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: ls.info()}})
}

// settleIdle is called when no turn runs although a frontend thinks one
// does (Interrupt with nothing to interrupt): a stale busy status is
// cleared and SessionUpdated is published either way, so the frontend
// catches up.
func (ls *liveSession) settleIdle() {
	ls.mu.Lock()
	if ls.statusLocked() == agentv1.SessionStatus_SESSION_STATUS_IDLE {
		ls.status = agentv1.SessionStatus_SESSION_STATUS_IDLE
	}
	ls.mu.Unlock()
	ls.publishInfo()
}

func (ls *liveSession) publish(ev *agentv1.SessionEvent) {
	ls.mu.Lock()
	ls.updatedAt = time.Now()
	ls.mu.Unlock()
	ls.hub.publish(ev)
}

func (ls *liveSession) notice(level agentv1.Notice_Level, kind, text string) {
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_Notice{Notice: &agentv1.Notice{Level: level, Kind: kind, Text: text}}})
}

func (ls *liveSession) setStatus(st agentv1.SessionStatus) {
	ls.mu.Lock()
	changed := ls.status != st
	ls.status = st
	ls.mu.Unlock()
	if changed {
		ls.publishInfo()
	}
}

// beginWait / endWait track pending user input (permission, question, plan).
func (ls *liveSession) beginWait() {
	ls.mu.Lock()
	ls.waiting++
	ls.mu.Unlock()
	ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT)
}

func (ls *liveSession) endWait() {
	ls.mu.Lock()
	ls.waiting--
	w := ls.waiting
	ls.mu.Unlock()
	if w == 0 {
		ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_WORKING)
	}
}

// close ends the session: interrupts the turn, kills background tasks,
// flushes the transcript and ends subscriptions.
func (ls *liveSession) close(reason string) {
	ls.mu.Lock()
	if ls.closed {
		ls.mu.Unlock()
		return
	}
	ls.closed = true
	ls.mu.Unlock()
	ls.cancel()
	ls.driver.Stop()
	ls.driver.Wait()
	ls.bg.Wait()
	ls.sess.Agent.SetInteractor(nil)
	ls.sess.Close()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionEnded{SessionEnded: &agentv1.SessionEnded{Reason: reason}}})
	ls.hub.close()
}

// onEvent maps agent events to session events.
func (ls *liveSession) onEvent(e agent.Event) {
	switch e.Kind {
	case agent.EventUser:
		ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_WORKING)
		// Tool results are reported through ToolCallFinished; only plain user
		// content (prompts, injected queued messages, interrupt markers) is sent.
		var content []llm.ContentBlock
		for _, b := range e.Message.Content {
			if b.Type != llm.BlockToolResult {
				content = append(content, b)
			}
		}
		if len(content) == 0 {
			return
		}
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserMessage{UserMessage: &agentv1.UserMessage{Uuid: e.UUID, Content: pbconv.Blocks(content), ClientUuids: e.ClientUUIDs}}})
	case agent.EventTurnStarted:
		ls.mu.Lock()
		ls.idleCompacted = false
		ls.mu.Unlock()
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnStarted{TurnStarted: &agentv1.TurnStarted{UserMessageUuid: e.UUID, Model: e.Model}}})
	case agent.EventStream:
		ls.onStream(e.Raw)
	case agent.EventAssistant:
		am := &agentv1.AssistantMessage{Content: pbconv.Blocks(e.Message.Content)}
		if r := e.Response; r != nil {
			am.MessageId, am.Model, am.StopReason, am.Usage = r.ID, r.Model, pbconv.StopReason(r.StopReason), pbconv.Usage(r.Usage)
		}
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_AssistantMessage{AssistantMessage: am}})
		ls.requestToolTitles(e.Message.Content)
	case agent.EventToolStart:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallStarted{ToolCallStarted: &agentv1.ToolCallStarted{
			ToolUseId: e.ToolUseID, Name: e.ToolName, InputJson: string(e.Input), Summary: toolSummary(e.ToolName, e.Input, ls.sess.Cwd),
		}}})
	case agent.EventToolEnd:
		structured := ""
		if e.Structured != nil {
			if b, err := json.Marshal(e.Structured); err == nil {
				structured = string(b)
			}
		}
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallFinished{ToolCallFinished: &agentv1.ToolCallFinished{
			ToolUseId: e.ToolUseID, IsError: e.IsError, Interrupted: e.Interrupted, Content: pbconv.Blocks(e.Content), DurationMs: e.Duration.Milliseconds(),
			StructuredResultJson: structured, Summary: e.Summary,
		}}})
	case agent.EventQueueDrained:
		ls.mu.Lock()
		ls.queue = nil
		ls.mu.Unlock()
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{}}})
	case agent.EventRetry:
		text := fmt.Sprintf("Retrying (attempt %d of %d) in %d ms", e.Retry.Attempt, e.Retry.MaxRetries, e.Retry.RetryDelayMs)
		if e.Retry.Error != nil {
			text += ": " + e.Retry.Error.Type
		}
		ls.notice(agentv1.Notice_LEVEL_WARNING, "retry", text)
	case agent.EventTodos:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TodosUpdated{TodosUpdated: &agentv1.TodosUpdated{Todos: todosToProto(e.Todos)}}})
	case agent.EventCheckpoint:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_CheckpointCreated{CheckpointCreated: &agentv1.CheckpointCreated{Checkpoint: checkpointToProto(*e.Checkpoint)}}})
	case agent.EventCompacted:
		tr := map[string]agentv1.Compacted_Trigger{"manual": agentv1.Compacted_TRIGGER_MANUAL, "auto": agentv1.Compacted_TRIGGER_AUTO, "rewind": agentv1.Compacted_TRIGGER_REWIND, "idle": agentv1.Compacted_TRIGGER_IDLE}
		c := e.Compaction
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_Compacted{Compacted: &agentv1.Compacted{Trigger: tr[c.Trigger], TokensBefore: int64(c.TokensBefore), TokensAfter: int64(c.TokensAfter)}}})
	case agent.EventRewound:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_Rewound{Rewound: rewoundToProto(*e.Rewind)}})
	case agent.EventTask:
		ls.onTask(e.Task)
	case agent.EventModeChanged:
		m, _ := permissions.ParseMode(e.Mode)
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionModeChanged{PermissionModeChanged: &agentv1.PermissionModeChanged{Mode: modeToProto(m)}}})
		ls.publishInfo()
	case agent.EventNotice:
		lv := map[string]agentv1.Notice_Level{"info": agentv1.Notice_LEVEL_INFO, "warning": agentv1.Notice_LEVEL_WARNING, "error": agentv1.Notice_LEVEL_ERROR}
		ls.notice(lv[e.Notice.Level], e.Notice.Kind, NoticeText(e.Notice))
	}
}

// AutoDecisionNotice is the JSON carried in Notice.text for kind
// "auto_decision" (Notice has no structured payload field): the verdict
// plus "text", the human-readable line.
type AutoDecisionNotice struct {
	agent.AutoDecision
	Text string `json:"text"`
}

// NoticeText renders a Notice for the wire: plain text, or the
// AutoDecisionNotice JSON for auto_decision notices.
func NoticeText(n *agent.Notice) string {
	if n.Kind != agent.NoticeAutoDecision || n.Auto == nil {
		return n.Text
	}
	b, err := json.Marshal(AutoDecisionNotice{AutoDecision: *n.Auto, Text: n.Text})
	if err != nil {
		return n.Text
	}
	return string(b)
}

func (ls *liveSession) onTask(e *tools.TaskEvent) {
	t := e.Task
	switch e.Kind {
	case tools.TaskEventStarted:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TaskStarted{TaskStarted: &agentv1.TaskStarted{
			TaskId: t.ID, ToolUseId: t.ToolUseID, Command: t.Command, Description: t.Description, OutputPath: t.OutputPath}}})
	case tools.TaskEventOutput:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TaskOutput{TaskOutput: &agentv1.TaskOutput{TaskId: t.ID, Text: e.Text}}})
	case tools.TaskEventFinished:
		reasons := map[tools.TaskFinishReason]agentv1.TaskFinished_Reason{
			tools.TaskExited: agentv1.TaskFinished_REASON_EXITED, tools.TaskStopped: agentv1.TaskFinished_REASON_STOPPED,
			tools.TaskTimeLimit: agentv1.TaskFinished_REASON_TIME_LIMIT, tools.TaskSessionClosed: agentv1.TaskFinished_REASON_SESSION_CLOSED,
		}
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TaskFinished{TaskFinished: &agentv1.TaskFinished{
			TaskId: t.ID, Reason: reasons[t.Reason], ExitCode: int32(t.ExitCode), DurationMs: t.Duration.Milliseconds()}}})
	}
}

func (ls *liveSession) onResult(res agent.TurnResult) {
	tf := &agentv1.TurnFinished{
		NumTurns: int32(res.NumTurns), Usage: pbconv.Usage(res.Usage), CostUsd: res.CostUSD,
		DurationMs: res.Duration.Milliseconds(), ErrorMessage: strings.Join(res.Errors, "; "),
	}
	switch {
	case res.Interrupted:
		tf.Result = agentv1.TurnFinished_RESULT_INTERRUPTED
	case res.Subtype == agent.SubtypeSuccess:
		tf.Result = agentv1.TurnFinished_RESULT_SUCCESS
	case res.Subtype == agent.SubtypeErrorMaxTurns:
		tf.Result = agentv1.TurnFinished_RESULT_ERROR_MAX_TURNS
	default:
		tf.Result = agentv1.TurnFinished_RESULT_ERROR_DURING_EXECUTION
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: tf}})
	if ls.srv.ctx.Err() == nil && ls.sess.Agent.QueueLen() == 0 {
		ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_IDLE)
	}
	ls.refreshDiffStats()
	ls.afterTurn()
}

// refreshDiffStats measures the working tree changes in the background and
// publishes SessionUpdated when they changed.
func (ls *liveSession) refreshDiffStats() {
	ls.mu.Lock()
	if ls.closed {
		ls.mu.Unlock()
		return
	}
	ls.bg.Add(1)
	ls.mu.Unlock()
	go func() {
		defer ls.bg.Done()
		ds := diffStats(ls.ctx, ls.sess.Cwd, ls.sess.Worktree)
		if ls.ctx.Err() != nil {
			return
		}
		ls.mu.Lock()
		changed := !proto.Equal(ds, ls.diff)
		ls.diff = ds
		ls.mu.Unlock()
		if changed {
			ls.publishInfo()
			ls.syncDiffStats(ds)
		}
	}()
}

func (ls *liveSession) syncDiffStats(ds *agentv1.DiffStats) {
	if ds == nil {
		return
	}
	ls.syncMeta(func(m *syncclient.SessionMeta) {
		m.DiffStats = &syncclient.DiffStats{FilesChanged: ds.FilesChanged, Additions: ds.Additions, Deletions: ds.Deletions}
	})
}

// Ask implements permissions.Asker: it publishes a PermissionRequest and
// waits for RespondToPermission.
func (ls *liveSession) Ask(ctx context.Context, req permissions.Request) (permissions.Decision, error) {
	id := newUUID("perm_")
	p := &pendingPermission{ch: make(chan permissionAnswer, 1), suggestions: req.Suggestions}
	ls.mu.Lock()
	ls.pendingPerm[id] = p
	ls.mu.Unlock()
	defer func() {
		ls.mu.Lock()
		delete(ls.pendingPerm, id)
		ls.mu.Unlock()
	}()
	pr := &agentv1.PermissionRequest{RequestId: id, ToolUseId: req.ToolUseID, ToolName: req.ToolName, InputJson: string(req.Input),
		SuggestedRules: req.Suggestions, Reason: req.Reason}
	ls.beginWait()
	defer ls.endWait()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionRequest{PermissionRequest: pr}})
	select {
	case <-ctx.Done():
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionResolved{PermissionResolved: &agentv1.PermissionResolved{
			RequestId: id, Decision: agentv1.RespondToPermissionRequest_DECISION_DENY, ResolvedBy: "interrupt"}}})
		return permissions.Decision{}, ctx.Err()
	case a := <-p.ch:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionResolved{PermissionResolved: &agentv1.PermissionResolved{
			RequestId: id, Decision: a.decision, ResolvedBy: "local"}}})
		return a.d, nil
	}
}

// ProposePlan implements tools.Interactor (ExitPlanMode).
func (ls *liveSession) ProposePlan(ctx context.Context, toolUseID, plan, path string) (tools.PlanResponse, error) {
	id := newUUID("plan_")
	ch := make(chan planAnswer, 1)
	ls.mu.Lock()
	ls.pendingPlan[id] = ch
	ls.mu.Unlock()
	defer func() {
		ls.mu.Lock()
		delete(ls.pendingPlan, id)
		ls.mu.Unlock()
	}()
	ls.beginWait()
	defer ls.endWait()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PlanProposed{PlanProposed: &agentv1.PlanProposed{RequestId: id, ToolUseId: toolUseID, Plan: plan, PlanFilePath: path}}})
	select {
	case <-ctx.Done():
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PlanResolved{PlanResolved: &agentv1.PlanResolved{RequestId: id, ResolvedBy: "interrupt"}}})
		return tools.PlanResponse{}, ctx.Err()
	case a := <-ch:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PlanResolved{PlanResolved: &agentv1.PlanResolved{RequestId: id, Decision: a.decision, ResolvedBy: "local"}}})
		return a.resp, nil
	}
}

// showKinds maps tools.ShowTarget kinds to the proto enum.
var showKinds = map[string]agentv1.ShowRequested_Kind{
	tools.ShowFile: agentv1.ShowRequested_KIND_FILE,
	tools.ShowDiff: agentv1.ShowRequested_KIND_DIFF,
	tools.ShowURL:  agentv1.ShowRequested_KIND_URL,
}

// Show implements tools.Shower: it publishes ShowRequested and reports
// whether a subscriber (a frontend, or the relay mirror) sees it now.
// RendersHTML reports whether the frontends draw Visual tool calls (the
// terminal UI does not).
func (ls *liveSession) RendersHTML() bool { return !ls.srv.NoPanes }

func (ls *liveSession) Show(toolUseID string, t tools.ShowTarget) tools.ShowStatus {
	if ls.srv.NoPanes {
		return tools.ShowUnavailable
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ShowRequested{ShowRequested: &agentv1.ShowRequested{
		ToolUseId: toolUseID, Kind: showKinds[t.Kind], Path: t.Path, LineStart: int32(t.LineStart), LineEnd: int32(t.LineEnd), Url: t.URL,
	}}})
	if !ls.hub.watched() {
		return tools.ShowPending
	}
	return tools.ShowOpened
}

// toolSummary renders a short label such as "Read src/main.go".
func toolSummary(name string, input []byte, cwd string) string {
	rel := func(p string) string {
		if r, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
		return p
	}
	switch name {
	case "Read", "Write", "Edit":
		return name + " " + rel(tools.FilePath(input))
	case "Bash":
		c := tools.BashCommand(input)
		c = truncateRunes(c, 80)
		return "Bash " + c
	case "Glob", "Grep":
		return name + " " + jsonField(input, "pattern")
	case "TaskStop", "TaskOutput":
		return name + " " + jsonField(input, "task_id")
	case "Wait":
		var w struct {
			TaskID string `json:"task_id"`
			Port   *int   `json:"port"`
			Host   string `json:"host"`
			Path   string `json:"path"`
		}
		_ = json.Unmarshal(input, &w)
		switch {
		case w.Port != nil && w.Host != "":
			return fmt.Sprintf("Wait port %s:%d", w.Host, *w.Port)
		case w.Port != nil:
			return fmt.Sprintf("Wait port %d", *w.Port)
		case w.Path != "":
			return "Wait " + rel(w.Path)
		case w.TaskID != "":
			return "Wait task " + w.TaskID
		}
	case "Show":
		if t := jsonField(input, "url"); t != "" {
			return "Show " + t
		}
		if p := jsonField(input, "path"); p != "" {
			return "Show " + rel(p)
		}
		return "Show " + jsonField(input, "target")
	}
	return name
}
