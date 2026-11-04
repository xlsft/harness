package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"

	"github.com/xlsft/harness/packages/cli/internal/client"
	"github.com/xlsft/harness/packages/cli/internal/render"
)

func blocksText(bs []*llmv1.ContentBlock) string {
	var parts []string
	for _, b := range bs {
		if t := b.GetText(); t != nil && t.GetText() != "" {
			parts = append(parts, t.GetText())
		}
	}
	return strings.Join(parts, "\n")
}

// isInterruptMarker recognizes the synthetic "[Request interrupted by user]" user message.
func isInterruptMarker(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "[request interrupted") || (strings.HasPrefix(l, "[") && strings.Contains(l, "interrupted by user"))
}

// onEvent applies one harness.agent.v1 session event.
func (m *Model) onEvent(ev *agentv1.SessionEvent) tea.Cmd {
	switch e := ev.GetEvent().(type) {
	case *agentv1.SessionEvent_SessionUpdated:
		s := e.SessionUpdated
		m.conn.SetSession(s)
		m.mode, m.model, m.style = s.GetPermissionMode(), s.GetModel(), s.GetOutputStyle()
		if s.GetGitBranch() != "" {
			m.branch = s.GetGitBranch()
		}

	case *agentv1.SessionEvent_UserMessage:
		text := blocksText(e.UserMessage.GetContent())
		if text == "" || isInterruptMarker(text) {
			return nil
		}
		m.closeOpen()
		m.push(&item{kind: itemUser, text: text})
		m.startTurn()

	case *agentv1.SessionEvent_TurnStarted:
		m.startTurn()

	case *agentv1.SessionEvent_AssistantDelta:
		d := e.AssistantDelta
		m.retry = nil
		switch x := d.GetDelta().(type) {
		case *agentv1.AssistantDelta_Text:
			m.status = "Writing"
			m.liveChars += len(x.Text)
			m.appendText(x.Text)
		case *agentv1.AssistantDelta_Thinking:
			m.status = "Thinking"
			m.liveChars += len(x.Thinking)
		case *agentv1.AssistantDelta_InputJson:
			m.liveChars += len(x.InputJson)
		}

	case *agentv1.SessionEvent_AssistantMessage:
		a := e.AssistantMessage
		m.retry = nil
		m.liveChars = 0
		if u := a.GetUsage(); u != nil {
			m.turnTokens += int(u.GetOutputTokens())
			if w := m.conn.ContextWindow(m.model); w > 0 {
				used := u.GetInputTokens() + u.GetCacheReadInputTokens() + u.GetCacheCreationInputTokens() + u.GetOutputTokens()
				m.ctxPct = int((used*100 + int64(w) - 1) / int64(w)) // round up: 0% only when empty
			}
		}
		// The streamed text is authoritative for display; close it. If nothing streamed (no
		// deltas, e.g. a replayed message), add the text now.
		if t := m.lastOpen(itemText); t != nil {
			t.closed = true
		} else if txt := strings.TrimSpace(blocksText(a.GetContent())); txt != "" && !m.streamedSinceUser() {
			m.closeOpen()
			m.push(&item{kind: itemText, text: txt, first: true, closed: true})
		}
		for _, b := range a.GetContent() {
			if tu := b.GetToolUse(); tu != nil {
				if _, ok := m.calls[tu.GetId()]; ok {
					continue
				}
				m.addCall(m.newCall(tu.GetId(), tu.GetName(), tu.GetInputJson()))
			}
		}
		m.status = "Thinking"

	case *agentv1.SessionEvent_ToolCallStarted:
		t := e.ToolCallStarted
		c := m.calls[t.GetToolUseId()]
		if c == nil {
			c = m.newCall(t.GetToolUseId(), t.GetName(), t.GetInputJson())
			m.addCall(c)
		}
		c.State = render.ToolRunning
		m.status = runningStatus(c.Name)

	case *agentv1.SessionEvent_ToolCallFinished:
		m.finishCall(e.ToolCallFinished)
		m.status = "Thinking"

	case *agentv1.SessionEvent_PermissionRequest:
		m.addPermission(e.PermissionRequest)
		m.status = "Waiting for approval"

	case *agentv1.SessionEvent_PermissionResolved:
		m.dropPermission(e.PermissionResolved.GetRequestId())

	case *agentv1.SessionEvent_Question:
		m.question = newQuestionPrompt(e.Question)
		m.status = "Waiting for your answer"

	case *agentv1.SessionEvent_QuestionResolved:
		if m.question != nil && m.question.req.GetRequestId() == e.QuestionResolved.GetRequestId() {
			m.question = nil
		}

	case *agentv1.SessionEvent_PlanProposed:
		m.plan = newPlanPrompt(e.PlanProposed)
		m.status = "Waiting for plan approval"

	case *agentv1.SessionEvent_PlanResolved:
		if m.plan != nil && m.plan.req.GetRequestId() == e.PlanResolved.GetRequestId() {
			m.plan = nil
		}

	case *agentv1.SessionEvent_QueueChanged:
		m.queued = nil
		for _, q := range e.QueueChanged.GetQueued() {
			m.queued = append(m.queued, blocksText(q.GetContent()))
		}

	case *agentv1.SessionEvent_TurnFinished:
		return m.finishTurn(e.TurnFinished)

	case *agentv1.SessionEvent_Notice:
		n := e.Notice
		if n.GetKind() == "retry" {
			m.retry, m.retryAt = parseRetry(n.GetText())
			return m.tick()
		}
		lvl := 0
		switch n.GetLevel() {
		case agentv1.Notice_LEVEL_WARNING:
			lvl = 1
		case agentv1.Notice_LEVEL_ERROR:
			lvl = 2
		}
		m.closeOpen()
		m.push(&item{kind: itemNotice, level: lvl, text: n.GetText()})

	case *agentv1.SessionEvent_TodosUpdated:
		m.todos = nil
		for _, t := range e.TodosUpdated.GetTodos() {
			st := 0
			switch t.GetStatus() {
			case agentv1.Todo_STATUS_IN_PROGRESS:
				st = 1
			case agentv1.Todo_STATUS_COMPLETED:
				st = 2
			}
			m.todos = append(m.todos, render.Todo{Content: t.GetContent(), ActiveForm: t.GetActiveForm(), Status: st})
		}

	case *agentv1.SessionEvent_TaskStarted:
		m.tasks[e.TaskStarted.GetTaskId()] = true
	case *agentv1.SessionEvent_TaskFinished:
		t := e.TaskFinished
		m.tasks[t.GetTaskId()] = false
		reason := map[agentv1.TaskFinished_Reason]string{
			agentv1.TaskFinished_REASON_EXITED: fmt.Sprintf("exited with code %d", t.GetExitCode()), agentv1.TaskFinished_REASON_STOPPED: "was stopped",
			agentv1.TaskFinished_REASON_TIME_LIMIT: "hit the time limit", agentv1.TaskFinished_REASON_SESSION_CLOSED: "ended with the session",
		}[t.GetReason()]
		m.closeOpen()
		m.push(&item{kind: itemNotice, text: fmt.Sprintf("Background task %s %s · %s", t.GetTaskId(), reason, render.Duration(time.Duration(t.GetDurationMs())*time.Millisecond))})

	case *agentv1.SessionEvent_PermissionModeChanged:
		m.mode = e.PermissionModeChanged.GetMode()

	case *agentv1.SessionEvent_Compacted:
		c := e.Compacted
		m.closeOpen()
		m.push(&item{kind: itemNotice, text: fmt.Sprintf("Conversation compacted (%s → %s tokens)", render.Tokens(int(c.GetTokensBefore())), render.Tokens(int(c.GetTokensAfter())))})

	case *agentv1.SessionEvent_Rewound:
		return m.onRewound(e.Rewound)

	case *agentv1.SessionEvent_SessionEnded:
		if !m.quitting {
			m.closeOpen()
			m.push(&item{kind: itemNotice, level: 1, text: "Session ended: " + e.SessionEnded.GetReason()})
		}
	}
	return nil
}

func (m *Model) startTurn() {
	if m.busy {
		return
	}
	m.busy = true
	m.turnStart = time.Now()
	m.turnTokens, m.liveChars = 0, 0
	m.status = "Thinking"
	m.retry = nil
}

// streamedSinceUser reports whether a text item was produced after the last user prompt.
func (m *Model) streamedSinceUser() bool {
	for i := len(m.pending) - 1; i >= 0; i-- {
		switch m.pending[i].kind {
		case itemUser:
			return false
		case itemText:
			return true
		}
	}
	return false
}

func runningStatus(name string) string {
	switch name {
	case "Read":
		return "Reading"
	case "Grep", "Glob", "WebSearch":
		return "Searching"
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "Editing"
	}
	return "Running " + name
}

// appendText adds streamed text. Complete paragraphs (outside code fences) are split off into
// their own closed item so they can scroll into the terminal history while the rest streams.
func (m *Model) appendText(delta string) {
	t := m.lastOpen(itemText)
	if t == nil {
		if strings.TrimSpace(delta) == "" {
			return
		}
		m.closeOpen()
		t = &item{kind: itemText, first: true}
		m.push(t)
		delta = strings.TrimLeft(delta, "\n")
	}
	t.text += delta
	cut := paragraphCut(t.text)
	if cut <= 0 {
		return
	}
	done := strings.TrimRight(t.text[:cut], "\n")
	rest := strings.TrimLeft(t.text[cut:], "\n")
	t.text, t.closed = done, true
	m.push(&item{kind: itemText, text: rest})
}

// paragraphCut returns the offset after the last blank line that is outside a code fence and
// followed by more text, or 0.
func paragraphCut(s string) int {
	inFence := false
	cut := 0
	off := 0
	lines := strings.SplitAfter(s, "\n")
	for i, l := range lines {
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			inFence = !inFence
		}
		off += len(l)
		if !inFence && trim == "" && i > 0 && i < len(lines)-1 && strings.HasSuffix(l, "\n") {
			// Keep list items and table rows together with what follows.
			cut = off
		}
	}
	if cut >= len(s) || strings.TrimSpace(s[cut:]) == "" {
		return 0
	}
	return cut
}

func (m *Model) finishCall(f *agentv1.ToolCallFinished) {
	c := m.calls[f.GetToolUseId()]
	if c == nil {
		return
	}
	c.Output = blocksText(f.GetContent())
	c.Summary = f.GetSummary()
	switch {
	case f.GetInterrupted():
		c.State = render.ToolInterrupted
	case m.rejected[c.ID]:
		c.State = render.ToolRejected
	case f.GetIsError():
		c.State = render.ToolError
	default:
		c.State = render.ToolOK
	}
	if sj := f.GetStructuredResultJson(); sj != "" && c.State == render.ToolOK {
		var st struct {
			Type     string             `json:"type"`
			FilePath string             `json:"filePath"`
			Content  string             `json:"content"`
			Patch    []render.PatchHunk `json:"structuredPatch"`
		}
		if json.Unmarshal([]byte(sj), &st) == nil {
			switch {
			case c.Name == "Write" && st.Type == "create":
				c.Diff = render.WriteDiff(c.Path, st.Content)
			case st.Patch != nil && (c.Name == "Edit" || c.Name == "Write" || c.Name == "MultiEdit" || c.Name == "NotebookEdit"):
				c.Diff = render.DiffFromPatch(c.Path, st.Patch)
			}
		}
	}
	if c.Diff == nil && (c.Name == "Edit" || c.Name == "Write") {
		c.Diff = previewDiff(c, inputJSON(c))
	}
}

func inputJSON(c *render.ToolCall) string {
	b, _ := json.Marshal(c.Input)
	return string(b)
}

// previewDiff computes the would-be diff of an Edit/Write from its input.
func previewDiff(c *render.ToolCall, input string) *render.FileDiff {
	_, patch, created, content := client.EditPreview(c.Name, input)
	if created {
		return render.WriteDiff(c.Path, content)
	}
	if patch == nil {
		return nil
	}
	return render.DiffFromPatch(c.Path, patch)
}

func (m *Model) finishTurn(t *agentv1.TurnFinished) tea.Cmd {
	m.closeOpen()
	for _, c := range m.calls {
		if !c.State.Done() {
			c.State = render.ToolInterrupted
		}
	}
	m.cost += t.GetCostUsd()
	dur := time.Duration(t.GetDurationMs()) * time.Millisecond
	if !m.turnStart.IsZero() {
		dur = time.Since(m.turnStart)
	}
	switch t.GetResult() {
	case agentv1.TurnFinished_RESULT_INTERRUPTED:
		// Interrupted before any output (e.g. during retries): remove the prompt from the
		// transcript and put it back into the input (reference §7b, §9a).
		if n := len(m.pending); n > 0 && m.pending[n-1].kind == itemUser {
			text := m.pending[n-1].text
			m.pending = m.pending[:n-1]
			m.restore(text)
			break
		}
		if n := len(m.printed); len(m.pending) == 0 && n > 0 && m.printed[n-1].kind == itemUser {
			text := m.printed[n-1].text
			m.printed = m.printed[:n-1]
			m.restore(text)
			m.busy, m.retry, m.status = false, nil, ""
			m.calls = map[string]*render.ToolCall{}
			return m.reprint()
		}
		m.push(&item{kind: itemInterrupted})
	case agentv1.TurnFinished_RESULT_SUCCESS:
		m.push(&item{kind: itemDone, dur: dur})
	default:
		msg := t.GetErrorMessage()
		if msg == "" {
			msg = "The turn ended with an error."
		}
		m.push(&item{kind: itemError, text: friendlyError(msg)})
	}
	m.busy, m.retry, m.status = false, nil, ""
	m.perms, m.question, m.plan = nil, nil, nil
	m.calls = map[string]*render.ToolCall{}
	m.rejected = map[string]bool{}
	return nil
}

// restore puts an unanswered prompt back into the input (when the input is empty).
func (m *Model) restore(text string) {
	if m.input.Empty() {
		m.input.Set(text)
	}
}

// friendlyError shortens gateway errors to one readable sentence.
func friendlyError(msg string) string {
	if i := strings.Index(msg, "unauthenticated"); i >= 0 {
		return "Not signed in or the session expired. Run `harness login <server-url>`."
	}
	return msg
}

// parseRetry reads the client's retry notice: "Retrying (attempt 2 of 10) in 4000 ms: overloaded".
func parseRetry(text string) (*render.Retry, time.Time) {
	r := &render.Retry{Error: "Unable to reach the model"}
	var ms int
	main, kind, _ := strings.Cut(text, ": ")
	_, _ = fmt.Sscanf(main, "Retrying (attempt %d of %d) in %d ms", &r.Attempt, &r.Max, &ms)
	if kind != "" {
		r.Error += " (" + kind + ")"
	}
	at := time.Now().Add(time.Duration(ms) * time.Millisecond)
	r.In = time.Duration(ms) * time.Millisecond
	return r, at
}

func (m *Model) onRewound(r *agentv1.Rewound) tea.Cmd {
	text := "Rewound"
	switch r.GetMode() {
	case agentv1.RewindRequest_MODE_CODE_AND_CONVERSATION:
		text = "Restored code and conversation"
	case agentv1.RewindRequest_MODE_CONVERSATION:
		text = "Restored conversation"
	case agentv1.RewindRequest_MODE_CODE:
		text = "Restored code"
	case agentv1.RewindRequest_MODE_SUMMARIZE_FROM, agentv1.RewindRequest_MODE_SUMMARIZE_UP_TO:
		text = "Summarized conversation"
	}
	if n := r.GetFilesRestored(); n > 0 {
		text += fmt.Sprintf(" · %d files restored", n)
	}
	if n := r.GetFilesSkipped(); n > 0 {
		text += fmt.Sprintf(" · %d skipped", n)
	}
	m.closeOpen()
	m.push(&item{kind: itemNotice, text: text})
	if p := r.GetRestoredPrompt(); p != "" && m.input.Empty() {
		m.input.Set(p)
	}
	return nil
}
