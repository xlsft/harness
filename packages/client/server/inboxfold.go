package server

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// The inbox fold (docs/design/coordinator.md): what one session needs from the user, from its SessionEvents. The harness
// server folds relay events with the same rules (packages/core/src/relay/inbox-fold.ts); both are tested on the same
// scenarios.

// Bounds of what an item keeps.
const (
	inboxSummaryChars = 400
	inboxReportChars  = 600
	inboxMaxRequests  = 20
	inboxMaxAgents    = 50
)

// Turn results the fold remembers.
const (
	foldSuccess     = "success"
	foldFailed      = "failed"
	foldInterrupted = "interrupted"
)

// inboxFold is one session's item and what the fold needs between events.
type inboxFold struct {
	item *agentv1.InboxItem
	// lastText is the latest assistant text of the running turn; result how the latest turn ended.
	lastText, result string
	// attention is when it last needed attention, in this client's time.
	attention time.Time
}

func newInboxFold(id string) *inboxFold {
	return &inboxFold{item: &agentv1.InboxItem{SessionId: id, Open: true}}
}

// cutText collapses whitespace and bounds s to n runes.
func cutText(s string, n int) string {
	t := strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(t) <= n {
		return t
	}
	r := []rune(t)
	return string(r[:n-1]) + "…"
}

// inputSummary is a one-line summary of a tool input: the command, path or pattern when there is one, else the JSON.
func inputSummary(input string) string {
	var v map[string]any
	if json.Unmarshal([]byte(input), &v) == nil {
		for _, k := range []string{"command", "file_path", "path", "url", "pattern", "query", "prompt", "description"} {
			if s, ok := v[k].(string); ok && s != "" {
				return cutText(s, inboxSummaryChars)
			}
		}
	}
	return cutText(input, inboxSummaryChars)
}

// group is the group the item belongs to now; UNSPECIFIED when it needs nothing.
func (f *inboxFold) group() agentv1.InboxGroup {
	it := f.item
	if len(it.Requests) > 0 {
		return agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT
	}
	agentRunning, agentFailed := false, false
	for _, a := range it.Agents {
		switch a.GetState() {
		case agentv1.SubagentState_SUBAGENT_STATE_RUNNING, agentv1.SubagentState_SUBAGENT_STATE_QUEUED:
			agentRunning = true
		case agentv1.SubagentState_SUBAGENT_STATE_FAILED:
			agentFailed = true
		}
	}
	if it.Working || agentRunning {
		return agentv1.InboxGroup_INBOX_GROUP_RUNNING
	}
	run := it.GetProjectRun().GetState()
	switch {
	case run == agentv1.ProjectRunState_PROJECT_RUN_STATE_FAILED, f.result == foldFailed, f.result == "" && agentFailed:
		return agentv1.InboxGroup_INBOX_GROUP_FAILED
	case f.result == foldSuccess && it.Report != "", run == agentv1.ProjectRunState_PROJECT_RUN_STATE_DONE:
		return agentv1.InboxGroup_INBOX_GROUP_FINISHED
	}
	return agentv1.InboxGroup_INBOX_GROUP_UNSPECIFIED
}

func (f *inboxFold) removeRequest(id string) bool {
	n := len(f.item.Requests)
	out := f.item.Requests[:0]
	for _, r := range f.item.Requests {
		if r.GetRequestId() != id {
			out = append(out, r)
		}
	}
	f.item.Requests = out
	return len(out) != n
}

func (f *inboxFold) addRequest(r *agentv1.InboxRequest) {
	f.removeRequest(r.GetRequestId())
	f.item.Requests = append(f.item.Requests, r)
	if n := len(f.item.Requests); n > inboxMaxRequests {
		f.item.Requests = f.item.Requests[n-inboxMaxRequests:]
	}
}

// fold applies one event. It returns whether the item changed in a way the inbox shows; attention moves on requests,
// finished and failed turns, failed agents and paused or ended runs.
func (f *inboxFold) fold(ev *agentv1.SessionEvent, now time.Time) bool {
	it := f.item
	at := ev.GetTimestamp()
	if at == "" {
		at = now.UTC().Format(time.RFC3339Nano)
	}
	attention := func() {
		it.AttentionAt = at
		f.attention = now
	}
	switch e := ev.GetEvent().(type) {
	case *agentv1.SessionEvent_SessionUpdated:
		f.applyInfo(e.SessionUpdated)
	case *agentv1.SessionEvent_TurnStarted:
		it.Working, it.Error, f.lastText = true, "", ""
	case *agentv1.SessionEvent_AssistantMessage:
		var parts []string
		for _, b := range e.AssistantMessage.GetContent() {
			if t := b.GetText().GetText(); t != "" {
				parts = append(parts, t)
			}
		}
		if t := strings.Join(parts, "\n"); strings.TrimSpace(t) != "" {
			f.lastText = t
		}
		return false
	case *agentv1.SessionEvent_TurnFinished:
		tf := e.TurnFinished
		it.Working = false
		if u := tf.GetUsage(); u != nil {
			it.InputTokens += u.GetInputTokens() + u.GetCacheReadInputTokens() + u.GetCacheCreationInputTokens()
			it.OutputTokens += u.GetOutputTokens()
		}
		it.CostUsd += tf.GetCostUsd()
		switch tf.GetResult() {
		case agentv1.TurnFinished_RESULT_SUCCESS:
			f.result = foldSuccess
			it.Report, it.Error = cutText(f.lastText, inboxReportChars), ""
			if it.Report != "" {
				attention()
			}
		case agentv1.TurnFinished_RESULT_INTERRUPTED:
			f.result = foldInterrupted
		default:
			f.result = foldFailed
			msg := tf.GetErrorMessage()
			if msg == "" {
				msg = "The turn failed"
			}
			it.Error = cutText(msg, inboxReportChars)
			attention()
		}
	case *agentv1.SessionEvent_PermissionRequest:
		p := e.PermissionRequest
		rules := p.GetSuggestedRules()
		if len(rules) > 5 {
			rules = rules[:5]
		}
		f.addRequest(&agentv1.InboxRequest{Kind: agentv1.InboxRequest_KIND_PERMISSION, RequestId: p.GetRequestId(), ToolName: p.GetToolName(),
			Summary: inputSummary(p.GetInputJson()), SuggestedRules: rules, OneTimeOnly: p.GetOneTimeOnly(), AgentId: p.GetAgentId(),
			AgentName: p.GetAgentName(), CreatedAt: at})
		attention()
	case *agentv1.SessionEvent_PermissionResolved:
		if !f.removeRequest(e.PermissionResolved.GetRequestId()) {
			return false
		}
	case *agentv1.SessionEvent_Question:
		q := e.Question
		r := &agentv1.InboxRequest{Kind: agentv1.InboxRequest_KIND_QUESTION, RequestId: q.GetRequestId(), CreatedAt: at}
		for i, item := range q.GetItems() {
			if i == 8 {
				break
			}
			iq := &agentv1.InboxQuestion{Question: cutText(item.GetQuestion(), inboxSummaryChars), Header: cutText(item.GetHeader(), 60),
				MultiSelect: item.GetMultiSelect(), Type: int32(item.GetType())}
			for j, o := range item.GetOptions() {
				if j == 12 {
					break
				}
				iq.Options = append(iq.Options, cutText(o.GetLabel(), 120))
			}
			r.Questions = append(r.Questions, iq)
		}
		f.addRequest(r)
		attention()
	case *agentv1.SessionEvent_QuestionResolved:
		if !f.removeRequest(e.QuestionResolved.GetRequestId()) {
			return false
		}
	case *agentv1.SessionEvent_PlanProposed:
		f.addRequest(&agentv1.InboxRequest{Kind: agentv1.InboxRequest_KIND_PLAN, RequestId: e.PlanProposed.GetRequestId(),
			Summary: cutText(e.PlanProposed.GetPlan(), inboxSummaryChars), CreatedAt: at})
		attention()
	case *agentv1.SessionEvent_PlanResolved:
		if !f.removeRequest(e.PlanResolved.GetRequestId()) {
			return false
		}
	case *agentv1.SessionEvent_SubagentUpdated:
		a := e.SubagentUpdated.GetAgent()
		if a.GetId() == "" {
			return false
		}
		updated := a.GetFinishedAt()
		if updated == "" {
			updated = a.GetStartedAt()
		}
		if updated == "" {
			updated = at
		}
		next := &agentv1.InboxAgent{Id: a.GetId(), Name: cutText(a.GetName(), 120), State: a.GetState(), Report: cutText(a.GetReport(), inboxReportChars),
			Error: cutText(a.GetError(), inboxReportChars), CurrentTool: a.GetCurrentTool(), UpdatedAt: updated}
		var prev *agentv1.InboxAgent
		agents := make([]*agentv1.InboxAgent, 0, len(it.Agents)+1)
		for _, x := range it.Agents {
			if x.GetId() == a.GetId() {
				prev = x
				continue
			}
			agents = append(agents, x)
		}
		if prev != nil && prev.GetState() == next.GetState() && prev.GetCurrentTool() == next.GetCurrentTool() && prev.GetReport() == next.GetReport() {
			return false
		}
		if next.GetState() == agentv1.SubagentState_SUBAGENT_STATE_FAILED && prev.GetState() != agentv1.SubagentState_SUBAGENT_STATE_FAILED {
			attention()
		}
		agents = append(agents, next)
		if len(agents) > inboxMaxAgents {
			agents = agents[len(agents)-inboxMaxAgents:]
		}
		it.Agents = agents
	case *agentv1.SessionEvent_SessionEnded:
		it.Working, it.Open, it.Requests = false, false, nil
		for i, a := range it.Agents {
			if s := a.GetState(); s == agentv1.SubagentState_SUBAGENT_STATE_RUNNING || s == agentv1.SubagentState_SUBAGENT_STATE_QUEUED {
				c := proto.Clone(a).(*agentv1.InboxAgent)
				c.State = agentv1.SubagentState_SUBAGENT_STATE_INTERRUPTED
				it.Agents[i] = c
			}
		}
	case *agentv1.SessionEvent_ProjectRunUpdated:
		run := e.ProjectRunUpdated.GetRun()
		if run == nil {
			return false
		}
		it.ProjectRun = run
		id := "run:" + run.GetId()
		switch run.GetState() {
		case agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED:
			has := false
			for _, r := range it.Requests {
				has = has || r.GetRequestId() == id
			}
			if !has {
				reason := run.GetReason()
				if reason == "" {
					reason = "The run is paused"
				}
				f.addRequest(&agentv1.InboxRequest{Kind: agentv1.InboxRequest_KIND_PROJECT_RUN, RequestId: id, Summary: cutText(reason, inboxSummaryChars), CreatedAt: at})
				attention()
			}
		default:
			f.removeRequest(id)
		}
		if s := run.GetState(); s == agentv1.ProjectRunState_PROJECT_RUN_STATE_DONE || s == agentv1.ProjectRunState_PROJECT_RUN_STATE_FAILED {
			attention()
		}
	default:
		return false
	}
	it.UpdatedAt = at
	return true
}

func (f *inboxFold) applyInfo(info *agentv1.SessionInfo) {
	it := f.item
	if info.GetTitle() != "" {
		it.Title = info.GetTitle()
	}
	if info.GetCwd() != "" {
		it.Cwd = info.GetCwd()
	}
	if info.GetRepo() != "" {
		it.Repo = info.GetRepo()
	}
	it.ParentSessionId = info.GetParentSessionId()
	it.Working = info.GetStatus() == agentv1.SessionStatus_SESSION_STATUS_WORKING
}
