package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
)

var workflowStates = map[tools.WorkflowState]agentv1.WorkflowState{
	tools.WorkflowRunning:     agentv1.WorkflowState_WORKFLOW_STATE_RUNNING,
	tools.WorkflowFinished:    agentv1.WorkflowState_WORKFLOW_STATE_FINISHED,
	tools.WorkflowFailed:      agentv1.WorkflowState_WORKFLOW_STATE_FAILED,
	tools.WorkflowStopped:     agentv1.WorkflowState_WORKFLOW_STATE_STOPPED,
	tools.WorkflowInterrupted: agentv1.WorkflowState_WORKFLOW_STATE_INTERRUPTED,
}

func workflowToProto(w tools.WorkflowInfo) *agentv1.WorkflowInfo {
	valid := func(s string) string { return strings.ToValidUTF8(s, "") }
	out := &agentv1.WorkflowInfo{
		Id: w.ID, ToolUseId: w.ToolUseID, Name: valid(w.Name), Description: valid(w.Description), CurrentPhase: valid(w.CurrentPhase), State: workflowStates[w.State],
		StartedAt: w.StartedAt, FinishedAt: w.FinishedAt, Error: valid(w.Error), Result: valid(w.Result),
		InputTokens: w.InputTokens, OutputTokens: w.OutputTokens, CostUsd: w.CostUSD,
	}
	for _, p := range w.Phases {
		out.Phases = append(out.Phases, valid(p))
	}
	for _, l := range w.Log {
		out.Log = append(out.Log, valid(l))
	}
	for _, a := range w.Agents {
		out.Agents = append(out.Agents, &agentv1.WorkflowAgent{AgentId: a.AgentID, Label: valid(a.Label), Phase: valid(a.Phase), State: subagentStates[a.State], Cached: a.Cached, Error: valid(a.Error)})
	}
	return out
}

// StopWorkflow stops a run of the session on the user's request, with its agents.
func (s *Server) StopWorkflow(ctx context.Context, req *connect.Request[agentv1.StopWorkflowRequest]) (*connect.Response[agentv1.StopWorkflowResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetWorkflowId()
	if _, ok := ls.sess.Workflows.Get(id); !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such workflow run"))
	}
	info, err := ls.sess.Workflows.Stop(id)
	if err != nil && !errors.Is(err, tools.ErrWorkflowNotRunning) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.StopWorkflowResponse{Workflow: workflowToProto(info)}), nil
}

// recordedWorkflows reads the runs of a session of another machine from the records in its transcript: the last state
// of each, in order of first appearance.
func recordedWorkflows(entries []session.Entry) []*agentv1.SessionEvent {
	latest := map[string]tools.WorkflowInfo{}
	var order []string
	for _, e := range entries {
		if e.Type != session.TypeMeta || len(e.Workflow) == 0 {
			continue
		}
		var w tools.WorkflowInfo
		if json.Unmarshal(e.Workflow, &w) != nil || w.ID == "" {
			continue
		}
		if _, seen := latest[w.ID]; !seen {
			order = append(order, w.ID)
		}
		latest[w.ID] = w
	}
	evs := make([]*agentv1.SessionEvent, 0, len(order))
	for _, id := range order {
		evs = append(evs, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_WorkflowUpdated{WorkflowUpdated: &agentv1.WorkflowUpdated{Workflow: workflowToProto(latest[id])}}})
	}
	return evs
}
