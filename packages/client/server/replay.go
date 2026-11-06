package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
)

// Limits of the history replayed into a resumed session's event log.
const (
	defaultHistoryTurns  = 200
	defaultHistoryEvents = 10000
)

// replayOptions configure replayHistory.
type replayOptions struct {
	// Cwd renders tool summaries relative to the session folder.
	Cwd string
	// Interrupts are the texts of interrupt markers (the user message that
	// ends an interrupted turn and the synthetic result of an interrupted
	// tool call).
	Interrupts []string
	// InterruptPrefixes also mark interrupts: texts starting with one of them
	// (used without the session's prompt bundle, whose reminders only keep
	// their documented prefix).
	InterruptPrefixes []string
	// MaxTurns and MaxEvents bound the replay (0: defaults). Older turns are
	// dropped first and a Notice says so.
	MaxTurns, MaxEvents int
}

// isInterrupt reports whether a user message or tool result text is an
// interrupt marker.
func (r *replayer) isInterrupt(text string) bool {
	text = strings.TrimSpace(text)
	if r.interrupts[text] {
		return true
	}
	for _, p := range r.o.InterruptPrefixes {
		if p != "" && strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// replayHistory rebuilds the durable SessionEvents a frontend would have
// seen live from a transcript, oldest first, so a resumed session's event
// log starts with its history. It follows the active branch across
// compaction boundaries (the full visible history, with a Compacted marker
// where each compaction happened); meta entries (project context, system
// reminders) and compaction summaries are not shown, as in the live stream.
// Deltas, permission prompts and background task events are not part of
// the replay. Seq, session id are left to the hub; timestamps come from the
// transcript.
func replayHistory(entries []session.Entry, o replayOptions) []*agentv1.SessionEvent {
	r := &replayer{o: o, interrupts: map[string]bool{}, pending: map[string]*pendingTool{}, toolTitles: map[string]string{}}
	for _, e := range entries {
		if e.Type == session.TypeToolTitle && e.ToolUseID != "" && strings.TrimSpace(e.ToolTitle) != "" {
			r.toolTitles[e.ToolUseID] = strings.TrimSpace(e.ToolTitle)
		}
	}
	for _, s := range o.Interrupts {
		if s = strings.TrimSpace(s); s != "" {
			r.interrupts[s] = true
		}
	}
	for _, e := range session.ActiveChain(entries, true) {
		r.entry(e)
	}
	r.flushAssistant()
	r.endTurn()
	return r.bounded()
}

type pendingTool struct {
	name  string
	input json.RawMessage
	at    time.Time
}

type replayTurn struct {
	promptID    string
	started     *agentv1.TurnStarted
	start, last time.Time
	usage       llm.Usage
	requests    int
	interrupted bool
	errMsg      string
	// lastResult is true while the last message of the turn is a tool result
	// (the turn never got its final answer: the process stopped mid-turn).
	lastResult bool
	lastAsst   bool
}

type replayer struct {
	o          replayOptions
	interrupts map[string]bool
	events     []*agentv1.SessionEvent
	// turnStarts are the indexes in events where each turn begins.
	turnStarts []int
	cur        *replayTurn
	pending    map[string]*pendingTool
	// pendingOrder keeps tool calls in request order for orphan handling.
	pendingOrder []string
	asst         *session.Entry
	asstBlocks   []llm.ContentBlock
	lastMode     string
	// compact is the last Compacted marker while its tokens_after is being
	// estimated from the entries that follow the boundary.
	compact      *agentv1.Compacted
	compactChars int
	// toolTitles are the recorded tool-title entries by tool use id (the
	// last one wins), emitted after the call's ToolCallStarted.
	toolTitles map[string]string
}

func entryTime(e session.Entry) time.Time {
	t, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (r *replayer) emit(at time.Time, ev *agentv1.SessionEvent) {
	if !at.IsZero() {
		ev.Timestamp = at.UTC().Format(time.RFC3339Nano)
	}
	r.events = append(r.events, ev)
	if r.cur != nil && !at.IsZero() {
		r.cur.last = at
	}
}

func (r *replayer) entry(e session.Entry) {
	at := entryTime(e)
	if e.Type != session.TypeAssistant || r.asst == nil || e.Message == nil || e.Message.ID == "" || r.asst.Message.ID != e.Message.ID {
		r.flushAssistant()
	}
	switch e.Type {
	case session.TypeUser:
		r.user(e, at)
	case session.TypeAssistant:
		if e.Message == nil {
			return
		}
		r.compact = nil
		if r.asst == nil && r.cur != nil && r.cur.interrupted && !r.cur.lastAsst && !r.cur.lastResult {
			// An answer right after the interrupt marker is the reply to the
			// interruption, published live after TurnFinished.
			r.endTurn()
		}
		if r.asst == nil {
			ec := e
			r.asst = &ec
			r.asstBlocks = nil
		} else {
			// Claude Code writes one line per content block of a message:
			// keep the last line's usage and stop reason.
			r.asst.Message.Usage, r.asst.Message.StopReason = e.Message.Usage, e.Message.StopReason
		}
		r.asstBlocks = append(r.asstBlocks, e.Blocks()...)
	case session.TypeSystem:
		switch e.Subtype {
		case session.SubtypeCompactBoundary:
			trigger, before := "", 0
			if m := e.CompactMetadata; m != nil {
				trigger, before = m.Trigger, m.PreTokens
			}
			if trigger != "auto" {
				// Manual compaction (and summarize-from-checkpoint) runs between turns.
				r.endTurn()
			}
			tr := map[string]agentv1.Compacted_Trigger{"manual": agentv1.Compacted_TRIGGER_MANUAL, "auto": agentv1.Compacted_TRIGGER_AUTO, "rewind": agentv1.Compacted_TRIGGER_REWIND, "idle": agentv1.Compacted_TRIGGER_IDLE}
			c := &agentv1.Compacted{Trigger: tr[trigger], TokensBefore: int64(before)}
			if c.Trigger == agentv1.Compacted_TRIGGER_UNSPECIFIED {
				c.Trigger = agentv1.Compacted_TRIGGER_MANUAL
			}
			r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_Compacted{Compacted: c}})
			r.compact, r.compactChars = c, 0
		case session.SubtypeRewind:
			r.endTurn()
			text := strings.TrimSpace(e.Content)
			if text == "" {
				text = "Conversation rewound"
			}
			r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_Notice{Notice: &agentv1.Notice{Level: agentv1.Notice_LEVEL_INFO, Kind: "rewind", Text: text}}})
		case session.SubtypeSessionMoved:
			r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_Notice{Notice: &agentv1.Notice{Level: agentv1.Notice_LEVEL_INFO, Kind: session.SubtypeSessionMoved, Text: e.Content}}})
		case "api_error":
			if r.cur != nil {
				r.cur.errMsg = e.Content
			}
		case session.SubtypeUserShell:
			r.userShell(e, at)
		case session.SubtypeSessionNote:
			if e.SessionNote != nil {
				r.emit(at, sessionNoteEvent(*e.SessionNote))
			}
		case session.SubtypeHookRun:
			if e.HookRun != nil {
				r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_HookRun{HookRun: hookRunToProto(*e.HookRun)}})
			}
		case session.SubtypeUsageLimit:
			if e.UsageLimit != nil {
				if u := pbconv.UsageLimit(*e.UsageLimit); u != nil {
					r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_UsageLimit{UsageLimit: u}})
				}
			}
		}
	}
}

func (r *replayer) user(e session.Entry, at time.Time) {
	if e.Message == nil {
		return
	}
	blocks := e.Blocks()
	if e.Skill != nil && !e.Skill.Restored {
		r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_SkillInvoked{SkillInvoked: skillInvokedProto(e.Skill)}})
	}
	if e.IsMeta || e.IsCompactSummary {
		if r.compact != nil {
			for _, b := range blocks {
				r.compactChars += len(b.Text)
			}
			r.compact.TokensAfter = int64(r.compactChars / 4)
			if e.IsCompactSummary && r.compact.Summary == "" && len(blocks) > 0 {
				r.compact.Summary = blocks[0].Text
			}
		}
		return
	}
	var results, other []llm.ContentBlock
	for _, b := range blocks {
		if b.Type == llm.BlockToolResult {
			results = append(results, b)
		} else {
			other = append(other, b)
		}
	}
	for _, b := range results {
		var structured any
		if len(results) == 1 {
			structured = e.ToolUseResult
		}
		r.toolResult(b, structured, at)
	}
	if len(results) > 0 && r.cur != nil {
		r.cur.lastResult, r.cur.lastAsst = true, false
	}
	if len(other) == 0 || commandNoise(other) {
		return
	}
	text := ""
	for _, b := range other {
		if b.Type == llm.BlockText {
			text += b.Text
		}
	}
	interrupt := r.isInterrupt(text)
	newTurn := !interrupt && (r.cur == nil ||
		(e.PromptID != "" && e.PromptID != r.cur.promptID) ||
		// Transcripts without prompt ids (older Claude Code): a prompt after a
		// finished answer starts a turn.
		(e.PromptID == "" && r.cur.promptID == "" && r.cur.lastAsst && len(r.pending) == 0))
	if newTurn {
		r.endTurn()
		if e.PermissionMode != "" && e.PermissionMode != r.lastMode {
			if m, err := permissions.ParseMode(e.PermissionMode); err == nil {
				r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionModeChanged{PermissionModeChanged: &agentv1.PermissionModeChanged{Mode: modeToProto(m)}}})
			}
			r.lastMode = e.PermissionMode
		}
		r.turnStarts = append(r.turnStarts, len(r.events))
		r.cur = &replayTurn{promptID: e.PromptID, start: at, last: at, started: &agentv1.TurnStarted{UserMessageUuid: e.UUID}}
		r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserMessage{UserMessage: &agentv1.UserMessage{Uuid: e.UUID, Content: pbconv.Blocks(other), ClientUuids: e.ClientUUIDs}}})
		r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnStarted{TurnStarted: r.cur.started}})
		return
	}
	r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserMessage{UserMessage: &agentv1.UserMessage{Uuid: e.UUID, Content: pbconv.Blocks(other), ClientUuids: e.ClientUUIDs}}})
	if r.cur != nil {
		r.cur.lastResult, r.cur.lastAsst = false, false
		if interrupt {
			r.cur.interrupted = true
		}
	}
}

// commandNoise reports Claude Code's local command echo lines
// (<command-name>, <local-command-stdout>), which are not prompts.
func commandNoise(blocks []llm.ContentBlock) bool {
	for _, b := range blocks {
		if b.Type != llm.BlockText {
			return false
		}
		t := strings.TrimSpace(b.Text)
		if !strings.HasPrefix(t, "<command-") && !strings.HasPrefix(t, "<local-command-") {
			return false
		}
	}
	return true
}

func (r *replayer) flushAssistant() {
	e := r.asst
	if e == nil {
		return
	}
	blocks := r.asstBlocks
	r.asst, r.asstBlocks = nil, nil
	if len(blocks) == 0 {
		return
	}
	at := entryTime(*e)
	m := e.Message
	id := m.ID
	if id == "" {
		id = "msg_" + e.UUID
	}
	am := &agentv1.AssistantMessage{MessageId: id, Model: m.Model, Content: pbconv.Blocks(blocks)}
	if m.StopReason != nil {
		am.StopReason = pbconv.StopReason(*m.StopReason)
	}
	if m.Usage != nil {
		am.Usage = pbconv.Usage(*m.Usage)
	}
	if t := r.cur; t != nil {
		if m.Usage != nil {
			t.usage.Add(*m.Usage)
		}
		t.requests++
		if t.started.Model == "" && m.Model != "" {
			t.started.Model = m.Model
		}
		t.lastAsst, t.lastResult = true, false
	}
	r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_AssistantMessage{AssistantMessage: am}})
	for _, b := range blocks {
		if b.Type != llm.BlockToolUse || b.ID == "" {
			continue
		}
		r.pending[b.ID] = &pendingTool{name: b.Name, input: b.Input, at: at}
		r.pendingOrder = append(r.pendingOrder, b.ID)
		r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallStarted{ToolCallStarted: &agentv1.ToolCallStarted{
			ToolUseId: b.ID, Name: b.Name, InputJson: string(b.Input), Summary: toolSummary(b.Name, b.Input, r.o.Cwd),
		}}})
		if t := r.toolTitles[b.ID]; t != "" {
			r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolTitle{ToolTitle: &agentv1.ToolTitle{ToolUseId: b.ID, Title: t}}})
		}
	}
}

func (r *replayer) toolResult(b llm.ContentBlock, structured any, at time.Time) {
	pt := r.pending[b.ToolUseID]
	if pt == nil {
		return // no matching tool call on the replayed branch
	}
	delete(r.pending, b.ToolUseID)
	text := ""
	for _, c := range b.Content {
		text += c.Text
	}
	interrupted := r.isInterrupt(text)
	fin := &agentv1.ToolCallFinished{ToolUseId: b.ToolUseID, IsError: b.IsError, Interrupted: interrupted, Content: pbconv.Blocks(b.Content)}
	if interrupted {
		fin.Summary = "Interrupted"
	}
	if !pt.at.IsZero() && !at.IsZero() && at.After(pt.at) {
		fin.DurationMs = at.Sub(pt.at).Milliseconds()
	}
	if structured != nil {
		if j, err := json.Marshal(structured); err == nil {
			fin.StructuredResultJson = string(j)
		}
		fin.Background = toolBackground(structured)
	}
	if !interrupted {
		r.cards(b.ToolUseID, pt, structured, b.IsError, at)
	} else if isQuestionTool(pt.name) {
		for _, ev := range questionCard(b.ToolUseID, pt.input, nil, true) {
			r.emit(at, ev)
		}
	}
	r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallFinished{ToolCallFinished: fin}})
	if pt.name == "TodoWrite" && !b.IsError && !interrupted {
		if todos, err := tools.ParseTodos(pt.input); err == nil {
			r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_TodosUpdated{TodosUpdated: &agentv1.TodosUpdated{Todos: todosToProto(todos)}}})
		}
	}
}

// cards replays the answered question / plan cards that replace the
// AskUserQuestion and ExitPlanMode tool calls in frontends.
func (r *replayer) cards(toolUseID string, pt *pendingTool, structured any, isError bool, at time.Time) {
	sm, _ := structured.(map[string]any)
	switch pt.name {
	case "AskUserQuestion", "SessionStart":
		for _, ev := range questionCard(toolUseID, pt.input, structured, false) {
			r.emit(at, ev)
		}
	case "ExitPlanMode":
		approved, ok := sm["approved"].(bool)
		if sm == nil || !ok {
			return // never answered
		}
		plan, _ := sm["plan"].(string)
		path, _ := sm["filePath"].(string)
		id := "plan_history_" + toolUseID
		d := agentv1.RespondToPlanRequest_DECISION_KEEP_PLANNING
		if approved {
			// The transcript does not record which approval was picked.
			d = agentv1.RespondToPlanRequest_DECISION_APPROVE_MANUAL
		}
		feedback, _ := sm["feedback"].(string)
		r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_PlanProposed{PlanProposed: &agentv1.PlanProposed{RequestId: id, ToolUseId: toolUseID, Plan: plan, PlanFilePath: path}}})
		r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_PlanResolved{PlanResolved: &agentv1.PlanResolved{RequestId: id, Decision: d, ResolvedBy: "history", Feedback: feedback}}})
	}
}

// endTurn closes the open turn: tool calls without a result are reported
// interrupted, then TurnFinished with the turn's usage and wall time.
func (r *replayer) endTurn() {
	r.flushAssistant()
	t := r.cur
	if t == nil {
		return
	}
	orphans := false
	for _, id := range r.pendingOrder {
		if r.pending[id] == nil {
			continue
		}
		pt := r.pending[id]
		delete(r.pending, id)
		orphans = true
		if isQuestionTool(pt.name) {
			// The process stopped while the question was open: the asking
			// turn is gone, so the card shows it cancelled.
			for _, ev := range questionCard(id, pt.input, nil, true) {
				r.emit(t.last, ev)
			}
		}
		r.emit(t.last, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallFinished{ToolCallFinished: &agentv1.ToolCallFinished{
			ToolUseId: id, IsError: true, Interrupted: true, Summary: "Interrupted"}}})
	}
	r.pendingOrder = nil
	tf := &agentv1.TurnFinished{NumTurns: int32(t.requests), Usage: pbconv.Usage(t.usage), ErrorMessage: t.errMsg}
	if !t.start.IsZero() && t.last.After(t.start) {
		tf.DurationMs = t.last.Sub(t.start).Milliseconds()
	}
	switch {
	case t.interrupted || orphans || (t.lastResult && t.errMsg == ""):
		tf.Result = agentv1.TurnFinished_RESULT_INTERRUPTED
	case t.errMsg != "":
		tf.Result = agentv1.TurnFinished_RESULT_ERROR_DURING_EXECUTION
	default:
		tf.Result = agentv1.TurnFinished_RESULT_SUCCESS
	}
	r.emit(t.last, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: tf}})
	r.cur = nil
}

// bounded applies MaxTurns / MaxEvents: whole turns are dropped from the
// start (with whatever came before the first kept turn) and a Notice is put
// first.
func (r *replayer) bounded() []*agentv1.SessionEvent {
	maxTurns, maxEvents := r.o.MaxTurns, r.o.MaxEvents
	if maxTurns <= 0 {
		maxTurns = defaultHistoryTurns
	}
	if maxEvents <= 0 {
		maxEvents = defaultHistoryEvents
	}
	evs, starts := r.events, r.turnStarts
	if len(evs) <= maxEvents && len(starts) <= maxTurns {
		return evs
	}
	total := len(starts)
	keep := min(total, maxTurns)
	for keep > 1 && len(evs)-starts[total-keep] > maxEvents {
		keep--
	}
	cut := 0
	if keep > 0 {
		cut = starts[total-keep]
	}
	if len(evs)-cut > maxEvents {
		// A single turn larger than the cap: keep its newest events.
		cut = len(evs) - maxEvents
	}
	text := fmt.Sprintf("Showing the last %d of %d turns of this session's history.", keep, total)
	if keep == 0 || cut > starts[total-keep] {
		text = "Showing only the end of this session's history."
	}
	out := make([]*agentv1.SessionEvent, 0, len(evs)-cut+1)
	notice := &agentv1.SessionEvent{Event: &agentv1.SessionEvent_Notice{Notice: &agentv1.Notice{Level: agentv1.Notice_LEVEL_INFO, Kind: "history_truncated", Text: text}}}
	if cut < len(evs) {
		notice.Timestamp = evs[cut].Timestamp
	}
	out = append(out, notice)
	return append(out, evs[cut:]...)
}
