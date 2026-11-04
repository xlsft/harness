package server

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/preview"
)

// Preview servers (preview.proto): the session's dev servers, run by this client for the agent's Preview* tools.
// They outlive the session's open and close, so the RPCs do not need a live session, only one of this machine.
// Nothing here is synced or published as a session event; the Browser pane asks.

// previews returns the client's preview manager (created on first use).
func (s *Server) previews() *preview.Manager {
	s.previewOnce.Do(func() {
		s.previewMgr = &preview.Manager{Pages: s.browsers(), Logger: s.log()}
	})
	return s.previewMgr
}

// checkPreviewSession refuses sessions whose processes cannot be here.
func (s *Server) checkPreviewSession(id string) error {
	if id == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	if ls != nil {
		return nil
	}
	if _, _, ok := s.acpScope(id); ok {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on an ACP agent; preview servers are only available to harness sessions", id))
	}
	if remote := s.remoteInfo(id); remote != nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on %s; its preview servers run there and are not synced", id, deviceLabel(remote)))
	}
	return nil
}

func previewError(err error) error {
	if errors.Is(err, preview.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (s *Server) ListPreviewServers(_ context.Context, req *connect.Request[agentv1.ListPreviewServersRequest]) (*connect.Response[agentv1.ListPreviewServersResponse], error) {
	id := req.Msg.GetSessionId()
	if err := s.checkPreviewSession(id); err != nil {
		return nil, err
	}
	out := &agentv1.ListPreviewServersResponse{}
	for _, i := range s.previews().List(id) {
		out.Servers = append(out.Servers, i.Proto())
	}
	return connect.NewResponse(out), nil
}

func (s *Server) StopPreviewServer(_ context.Context, req *connect.Request[agentv1.StopPreviewServerRequest]) (*connect.Response[agentv1.StopPreviewServerResponse], error) {
	id := req.Msg.GetSessionId()
	if err := s.checkPreviewSession(id); err != nil {
		return nil, err
	}
	i, err := s.previews().Stop(id, req.Msg.GetServerId())
	if err != nil {
		return nil, previewError(err)
	}
	return connect.NewResponse(&agentv1.StopPreviewServerResponse{Server: i.Proto()}), nil
}

func (s *Server) GetPreviewLogs(_ context.Context, req *connect.Request[agentv1.GetPreviewLogsRequest]) (*connect.Response[agentv1.GetPreviewLogsResponse], error) {
	m := req.Msg
	if err := s.checkPreviewSession(m.GetSessionId()); err != nil {
		return nil, err
	}
	lines, dropped, err := s.previews().Logs(m.GetSessionId(), m.GetServerId(), preview.LogFilter{
		Lines: preview.ClampLogLines(int(m.GetLines())), ErrorsOnly: m.GetErrorsOnly(), Search: m.GetSearch(),
	})
	if err != nil {
		return nil, previewError(err)
	}
	return connect.NewResponse(&agentv1.GetPreviewLogsResponse{Lines: lines, Truncated: dropped}), nil
}
