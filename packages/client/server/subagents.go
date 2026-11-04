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

// subagentOutputMax bounds a tool result in GetSubagentTranscript.
const subagentOutputMax = 8000

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
