package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/subagent"
	"github.com/xlsft/harness/packages/client/tools"
)

// subagentOutputMax bounds a tool result in GetSubagentTranscript, and subagentTranscriptMaxBytes the response, which
// keeps the end: it may travel through the relay, as one command ack.
const (
	subagentOutputMax          = 8000
	subagentTranscriptMaxBytes = 2 << 20
)

var subagentStates = map[tools.AgentState]agentv1.SubagentState{
	tools.AgentQueued:      agentv1.SubagentState_SUBAGENT_STATE_QUEUED,
	tools.AgentRunning:     agentv1.SubagentState_SUBAGENT_STATE_RUNNING,
	tools.AgentFinished:    agentv1.SubagentState_SUBAGENT_STATE_FINISHED,
	tools.AgentFailed:      agentv1.SubagentState_SUBAGENT_STATE_FAILED,
	tools.AgentStopped:     agentv1.SubagentState_SUBAGENT_STATE_STOPPED,
	tools.AgentInterrupted: agentv1.SubagentState_SUBAGENT_STATE_INTERRUPTED,
}

var subagentStepStatuses = map[string]agentv1.SubagentStepStatus{
	tools.AgentStepRunning: agentv1.SubagentStepStatus_SUBAGENT_STEP_STATUS_RUNNING,
	tools.AgentStepDone:    agentv1.SubagentStepStatus_SUBAGENT_STEP_STATUS_DONE,
	tools.AgentStepFailed:  agentv1.SubagentStepStatus_SUBAGENT_STEP_STATUS_FAILED,
}

func subagentToProto(a tools.AgentInfo) *agentv1.SubagentInfo {
	out := &agentv1.SubagentInfo{
		Id: a.ID, ParentId: a.ParentID, ToolUseId: a.ToolUseID, Name: a.Name, Type: a.Type, Model: a.Model, State: subagentStates[a.State],
		CurrentTool: a.CurrentTool, ToolCalls: int32(a.ToolCalls), InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CostUsd: a.CostUSD,
		StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, Report: strings.ToValidUTF8(a.Report, ""), Error: strings.ToValidUTF8(a.Error, ""),
		WorktreePath: a.WorktreePath, WorktreeBranch: a.WorktreeBranch,
	}
	for _, s := range a.Steps {
		out.Steps = append(out.Steps, &agentv1.SubagentStep{ToolUseId: s.ToolUseID, Tool: s.Tool, Target: strings.ToValidUTF8(s.Target, ""), Status: subagentStepStatuses[s.Status]})
	}
	return out
}

// StopSubagent stops an agent of the session on the user's request.
func (s *Server) StopSubagent(ctx context.Context, req *connect.Request[agentv1.StopSubagentRequest]) (*connect.Response[agentv1.StopSubagentResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetAgentId()
	if _, ok := ls.sess.Subagents.Get(id); !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such agent"))
	}
	info, err := ls.sess.Subagents.Stop("", id)
	if err != nil && !errors.Is(err, tools.ErrAgentNotRunning) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.StopSubagentResponse{Agent: subagentToProto(info)}), nil
}

// GetSubagentTranscript returns what an agent did, flattened for reading.
func (s *Server) GetSubagentTranscript(ctx context.Context, req *connect.Request[agentv1.GetSubagentTranscriptRequest]) (*connect.Response[agentv1.GetSubagentTranscriptResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	entries, err := ls.sess.Subagents.Entries(req.Msg.GetAgentId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&agentv1.GetSubagentTranscriptResponse{Items: subagentTranscript(entries)}), nil
}

// remoteSubagentTranscript reads an agent's transcript from the server's copy of a session of another machine.
func (s *Server) remoteSubagentTranscript(ctx context.Context, sessionID, agentID string) (*connect.Response[agentv1.GetSubagentTranscriptResponse], error) {
	if agentID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent_id is required"))
	}
	raw, err := s.remoteThread(ctx, sessionID, agentID)
	if err != nil {
		return nil, err
	}
	entries := make([]session.Entry, 0, len(raw))
	for _, e := range raw {
		var se session.Entry
		if json.Unmarshal([]byte(e.GetEntryJson()), &se) == nil {
			entries = append(entries, se)
		}
	}
	return connect.NewResponse(&agentv1.GetSubagentTranscriptResponse{Items: subagentTranscript(entries)}), nil
}

// recordedSubagents reads the agents of a session of another machine from the records in its transcript: the last
// state of each, in order of first appearance.
func recordedSubagents(entries []session.Entry) []*agentv1.SessionEvent {
	latest := map[string]tools.AgentInfo{}
	var order []string
	for _, e := range entries {
		if e.Type != session.TypeMeta || len(e.Subagent) == 0 {
			continue
		}
		var a tools.AgentInfo
		if json.Unmarshal(e.Subagent, &a) != nil || a.ID == "" {
			continue
		}
		if _, seen := latest[a.ID]; !seen {
			order = append(order, a.ID)
		}
		latest[a.ID] = a
	}
	evs := make([]*agentv1.SessionEvent, 0, len(order))
	for _, id := range order {
		evs = append(evs, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_SubagentUpdated{SubagentUpdated: &agentv1.SubagentUpdated{Agent: subagentToProto(latest[id])}}})
	}
	return evs
}

// subagentTranscript flattens transcript entries: messages in order, each tool call with its result.
func subagentTranscript(entries []session.Entry) []*agentv1.SubagentTranscriptItem {
	var items []*agentv1.SubagentTranscriptItem
	calls := map[string]*agentv1.SubagentTranscriptItem{}
	for _, e := range entries {
		if e.IsMeta || e.Message == nil {
			continue
		}
		switch e.Type {
		case session.TypeAssistant:
			var text []string
			for _, b := range e.Blocks() {
				switch b.Type {
				case llm.BlockText:
					text = append(text, b.Text)
				case llm.BlockToolUse:
					if len(text) > 0 {
						items = append(items, &agentv1.SubagentTranscriptItem{Kind: agentv1.SubagentTranscriptItem_KIND_ASSISTANT, Text: strings.Join(text, "\n")})
						text = nil
					}
					it := &agentv1.SubagentTranscriptItem{Kind: agentv1.SubagentTranscriptItem_KIND_TOOL, ToolUseId: b.ID, ToolName: b.Name, Target: subagent.Target(json.RawMessage(b.Input))}
					calls[b.ID] = it
					items = append(items, it)
				}
			}
			if len(text) > 0 {
				items = append(items, &agentv1.SubagentTranscriptItem{Kind: agentv1.SubagentTranscriptItem_KIND_ASSISTANT, Text: strings.Join(text, "\n")})
			}
		case session.TypeUser:
			var text []string
			for _, b := range e.Blocks() {
				switch b.Type {
				case llm.BlockText:
					text = append(text, b.Text)
				case llm.BlockToolResult:
					if it := calls[b.ToolUseID]; it != nil {
						it.IsError = b.IsError
						it.Output = truncateRunes(strings.ToValidUTF8(blocksText(b.Content), ""), subagentOutputMax)
					}
				}
			}
			if len(text) > 0 {
				items = append(items, &agentv1.SubagentTranscriptItem{Kind: agentv1.SubagentTranscriptItem_KIND_USER, Text: strings.Join(text, "\n")})
			}
		}
	}
	size := 0
	for i := len(items) - 1; i >= 0; i-- {
		size += len(items[i].GetText()) + len(items[i].GetOutput()) + len(items[i].GetTarget())
		if size > subagentTranscriptMaxBytes {
			return items[i+1:]
		}
	}
	return items
}

func blocksText(blocks []llm.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == llm.BlockText {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
