package server_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

const workflowScript = `export const meta = { name: "demo", description: "d", phases: ["One"] }
phase("One")
return await agent("hang")`

// A run reaches frontends as WorkflowUpdated, its agent as SubagentUpdated; StopWorkflow stops both.
func TestWorkflowEventsAndStop(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		if isAgentRequest(r) {
			return fg.Reply{Hold: true}
		}
		if hasToolResult(r) || strings.Contains(lastUserText(r), "<workflow_report>") {
			return fg.Reply{Events: fg.Message("p", fg.Text("ok"))}
		}
		return fg.Reply{Events: fg.Message("p", fg.ToolUse("w1", "Workflow", map[string]any{"script": workflowScript}))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	var run *agentv1.WorkflowInfo
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if u := ev.GetWorkflowUpdated(); u != nil && len(u.Workflow.Agents) == 1 && u.Workflow.Agents[0].AgentId != "" {
			run = u.Workflow
			return true
		}
		return false
	})
	if run.Name != "demo" || run.State != agentv1.WorkflowState_WORKFLOW_STATE_RUNNING || run.Phases[0] != "One" || run.ToolUseId != "w1" || run.Agents[0].Phase != "One" {
		t.Fatalf("%+v", run)
	}
	stopped, err := h.client.StopWorkflow(h.ctx, connect.NewRequest(&agentv1.StopWorkflowRequest{SessionId: sid, WorkflowId: run.Id}))
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Msg.Workflow.State != agentv1.WorkflowState_WORKFLOW_STATE_STOPPED {
		t.Fatalf("%+v", stopped.Msg.Workflow)
	}
	if _, err := h.client.StopWorkflow(h.ctx, connect.NewRequest(&agentv1.StopWorkflowRequest{SessionId: sid, WorkflowId: "wnope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown run: %v", err)
	}
	// Stopping again reads as what it ended as.
	again, err := h.client.StopWorkflow(h.ctx, connect.NewRequest(&agentv1.StopWorkflowRequest{SessionId: sid, WorkflowId: run.Id}))
	if err != nil || again.Msg.Workflow.State != agentv1.WorkflowState_WORKFLOW_STATE_STOPPED {
		t.Fatalf("%+v %v", again, err)
	}
	h.closeSession(sid)
}

func (f *fakeRelay) StopWorkflow(ctx context.Context, req *connect.Request[relayv1.StopWorkflowRequest]) (*connect.Response[relayv1.StopWorkflowResponse], error) {
	return connect.NewResponse(&relayv1.StopWorkflowResponse{Response: &agentv1.StopWorkflowResponse{Workflow: &agentv1.WorkflowInfo{
		Id: req.Msg.GetRequest().GetWorkflowId(), State: agentv1.WorkflowState_WORKFLOW_STATE_STOPPED}}}), nil
}

// Stopping a run of a session on another machine goes through the relay.
func TestRemoteControlledWorkflowStop(t *testing.T) {
	relay := &fakeRelay{online: true}
	h := startRemoteHarness(t, relay)
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello", "hi")
	relay.sessions = []*agentv1.SessionInfo{{Id: remote, Status: agentv1.SessionStatus_SESSION_STATUS_WORKING}}
	if r := h.listAll()[remote]; r == nil || !r.RemoteControllable {
		t.Fatalf("session %+v", r)
	}
	res, err := h.client.StopWorkflow(h.ctx, connect.NewRequest(&agentv1.StopWorkflowRequest{SessionId: remote, WorkflowId: "w1"}))
	if err != nil || res.Msg.Workflow.Id != "w1" || res.Msg.Workflow.State != agentv1.WorkflowState_WORKFLOW_STATE_STOPPED {
		t.Fatalf("%+v %v", res, err)
	}
}
