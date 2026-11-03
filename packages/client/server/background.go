package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/tools"
)

// BackgroundToolCall moves the running foreground Bash command of a tool call
// to the background; the tool call then completes with the note for reason.
func (s *Server) BackgroundToolCall(ctx context.Context, req *connect.Request[agentv1.BackgroundToolCallRequest]) (*connect.Response[agentv1.BackgroundToolCallResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetToolUseId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("tool_use_id is required"))
	}
	var reason string
	switch req.Msg.GetReason() {
	case agentv1.BackgroundReason_BACKGROUND_REASON_UNSPECIFIED, agentv1.BackgroundReason_BACKGROUND_REASON_USER:
		reason = tools.BackgroundUser
	case agentv1.BackgroundReason_BACKGROUND_REASON_QUEUED_MESSAGE:
		reason = tools.BackgroundQueuedMessage
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("reason must be USER or QUEUED_MESSAGE"))
	}
	env := ls.sess.ToolEnv
	ok := env != nil && env.Shell != nil && env.Shell.BackgroundToolCall(req.Msg.GetToolUseId(), reason)
	return connect.NewResponse(&agentv1.BackgroundToolCallResponse{Backgrounded: ok}), nil
}

var backgroundReasons = map[string]agentv1.BackgroundReason{
	tools.BackgroundUser:          agentv1.BackgroundReason_BACKGROUND_REASON_USER,
	tools.BackgroundQueuedMessage: agentv1.BackgroundReason_BACKGROUND_REASON_QUEUED_MESSAGE,
	tools.BackgroundAgent:         agentv1.BackgroundReason_BACKGROUND_REASON_AGENT,
	tools.BackgroundAuto:          agentv1.BackgroundReason_BACKGROUND_REASON_AUTO,
}

// toolBackground reads the background marker of a Bash structured result
// ("backgroundTaskId" / "backgroundReason"), live or from the transcript.
// Older results without a recorded reason get UNSPECIFIED.
func toolBackground(structured any) *agentv1.ToolBackground {
	m, _ := structured.(map[string]any)
	id, _ := m["backgroundTaskId"].(string)
	if id == "" {
		return nil
	}
	r, _ := m["backgroundReason"].(string)
	return &agentv1.ToolBackground{TaskId: id, Reason: backgroundReasons[r]}
}
