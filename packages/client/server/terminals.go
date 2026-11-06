package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/agent"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/terminal"
)

// Terminals (terminal.proto): shells in a PTY for the UI's terminal pane. The RPCs manage them; the I/O goes
// over the WebSocket of terminal.Manager.SocketHandler at terminal.SocketPath, authenticated by the one-time
// tickets CreateTerminal and AttachTerminal return (TCPGuard lets that path through without the bearer token).

const (
	maxTerminalSize = 2000
	maxTerminalEnv  = 100
)

// terminals returns the client's terminal manager (created on first use).
func (s *Server) terminals() *terminal.Manager {
	s.termOnce.Do(func() {
		if s.terms == nil {
			s.terms = &terminal.Manager{}
		}
		s.terms.OnRunFinished = s.onTerminalRunFinished
		s.terms.Refuse = s.refuseTerminals
	})
	return s.terms
}

// onTerminalRunFinished tells the session's model that a command it started in a tab has ended, in its next request,
// which the end starts itself when the session is idle (like a background task). The session is looked up now, so a
// tab that outlived the agent it started with (a resume) still reaches the model.
func (s *Server) onTerminalRunFinished(f terminal.RunFinished) {
	s.mu.Lock()
	ls := s.live[f.SessionID]
	s.mu.Unlock()
	if ls == nil {
		return
	}
	ls.sess.Agent.Remind(agent.ReminderTerminalDone, map[string]string{
		"tab_id": f.TerminalID, "title": f.Title, "command": truncateRunes(f.Command, 200), "status": f.Status, "tail": f.TailBlock(),
	})
	ls.driver.Wake()
}

// refuseTerminals says why an agent cannot use terminals: its session runs on another machine, and terminals are
// local to the machine they run on.
func (s *Server) refuseTerminals(id string) error {
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	if ls != nil {
		return nil
	}
	if remote := s.remoteInfo(id); remote != nil {
		return fmt.Errorf("session %s runs on %s; terminals are local to the machine they run on and are only available for sessions of this machine", id, deviceLabel(remote))
	}
	return nil
}

// registerTerminalSocket serves the terminal WebSocket on mux; requests with an Origin header must come from
// origins.
func (s *Server) registerTerminalSocket(mux *http.ServeMux, origins []string) {
	mux.Handle(terminal.SocketPath, s.terminals().SocketHandler(origins))
}

func (s *Server) CreateTerminal(ctx context.Context, req *connect.Request[agentv1.CreateTerminalRequest]) (*connect.Response[agentv1.CreateTerminalResponse], error) {
	m := req.Msg
	home, _ := os.UserHomeDir()
	defaultCwd, dirs := home, []string(nil)
	if id := m.GetSessionId(); id != "" {
		s.mu.Lock()
		ls := s.live[id]
		s.mu.Unlock()
		if cwd, acpDirs, ok := s.acpScope(id); ok && ls == nil {
			defaultCwd, dirs = cwd, acpDirs
		} else if ls == nil {
			if remote := s.remoteInfo(id); remote != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on %s; terminals are only available for sessions of this machine", id, deviceLabel(remote)))
			}
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open; call CreateSession first", id))
		}
		if ls != nil {
			defaultCwd, dirs = ls.sess.Dir(), []string{ls.sess.Dir()}
			if ls.sess.Permissions != nil {
				dirs = ls.sess.Permissions.WorkingDirs()
			}
		}
	}
	// The session's folder is gone: a terminal still opens, at home. A path asked for by name is checked as usual.
	if strings.TrimSpace(m.GetCwd()) == "" {
		if st, err := os.Stat(defaultCwd); err != nil || !st.IsDir() {
			defaultCwd, dirs = home, nil
		}
	}
	cwd, err := terminalCwd(m.GetCwd(), defaultCwd, home, dirs)
	if err != nil {
		return nil, err
	}
	cols, rows := m.GetCols(), m.GetRows()
	if cols > maxTerminalSize || rows > maxTerminalSize {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cols and rows must be at most %d", maxTerminalSize))
	}
	if err := validTerminalEnv(m.GetEnv()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	mgr := s.terminals()
	t, err := mgr.Create(terminal.Options{
		SessionID: m.GetSessionId(), Cwd: cwd, Title: m.GetTitle(), Cols: uint16(cols), Rows: uint16(rows), Env: m.GetEnv(),
	})
	if err != nil {
		return nil, terminalError(err)
	}
	ticket, err := mgr.IssueTicket(t.ID())
	if err != nil {
		return nil, terminalError(err)
	}
	return connect.NewResponse(&agentv1.CreateTerminalResponse{Terminal: terminalInfo(t.Info()), Ticket: ticket}), nil
}

func (s *Server) AttachTerminal(ctx context.Context, req *connect.Request[agentv1.AttachTerminalRequest]) (*connect.Response[agentv1.AttachTerminalResponse], error) {
	mgr := s.terminals()
	t := mgr.Get(req.Msg.GetTerminalId())
	if t == nil {
		return nil, terminalError(terminal.ErrNotFound)
	}
	ticket, err := mgr.IssueTicket(t.ID())
	if err != nil {
		return nil, terminalError(err)
	}
	return connect.NewResponse(&agentv1.AttachTerminalResponse{Terminal: terminalInfo(t.Info()), Ticket: ticket}), nil
}

func (s *Server) ListTerminals(ctx context.Context, req *connect.Request[agentv1.ListTerminalsRequest]) (*connect.Response[agentv1.ListTerminalsResponse], error) {
	resp := &agentv1.ListTerminalsResponse{}
	for _, t := range s.terminals().List(req.Msg.GetSessionId()) {
		resp.Terminals = append(resp.Terminals, terminalInfo(t.Info()))
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) KillTerminal(ctx context.Context, req *connect.Request[agentv1.KillTerminalRequest]) (*connect.Response[agentv1.KillTerminalResponse], error) {
	if !s.terminals().Kill(req.Msg.GetTerminalId()) {
		return nil, terminalError(terminal.ErrNotFound)
	}
	return connect.NewResponse(&agentv1.KillTerminalResponse{}), nil
}

func (s *Server) ResizeTerminal(ctx context.Context, req *connect.Request[agentv1.ResizeTerminalRequest]) (*connect.Response[agentv1.ResizeTerminalResponse], error) {
	m := req.Msg
	if m.GetCols() == 0 || m.GetRows() == 0 || m.GetCols() > maxTerminalSize || m.GetRows() > maxTerminalSize {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cols and rows must be between 1 and %d", maxTerminalSize))
	}
	t := s.terminals().Get(m.GetTerminalId())
	if t == nil {
		return nil, terminalError(terminal.ErrNotFound)
	}
	if err := t.Resize(uint16(m.GetCols()), uint16(m.GetRows())); err != nil {
		return nil, terminalError(err)
	}
	return connect.NewResponse(&agentv1.ResizeTerminalResponse{}), nil
}

// terminalCwd resolves a terminal's directory like ListDirectory does (absolute, or "~/..."; empty: def). With
// working directories (a session's), it must be inside one of them.
func terminalCwd(p, def, home string, dirs []string) (string, error) {
	if strings.TrimSpace(p) == "" {
		p = def
	}
	cwd, err := expandPath(p, home)
	if err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	st, err := os.Stat(cwd)
	if err != nil {
		return "", fsError(cwd, err)
	}
	if !st.IsDir() {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a directory", cwd))
	}
	if dirs != nil && !permissions.WithinAny(cwd, dirs) {
		return "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s is outside the session's working directories", cwd))
	}
	return cwd, nil
}

func validTerminalEnv(env map[string]string) error {
	if len(env) > maxTerminalEnv {
		return fmt.Errorf("at most %d environment variables", maxTerminalEnv)
	}
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return fmt.Errorf("invalid environment variable %q", k)
		}
	}
	return nil
}

func terminalError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, terminal.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, terminal.ErrLimit):
		code = connect.CodeResourceExhausted
	case errors.Is(err, terminal.ErrExited):
		code = connect.CodeFailedPrecondition
	case errors.Is(err, terminal.ErrUnsupported):
		code = connect.CodeUnimplemented
	case errors.Is(err, terminal.ErrClosed):
		code = connect.CodeUnavailable
	}
	return connect.NewError(code, err)
}

func terminalInfo(i terminal.Info) *agentv1.TerminalInfo {
	ti := &agentv1.TerminalInfo{
		TerminalId: i.ID, SessionId: i.SessionID, Title: i.Title, Cwd: i.Cwd, Shell: i.Shell, Pid: int32(i.Pid),
		Running: i.Running, CreatedAt: rfc3339(i.CreatedAt), Cols: uint32(i.Cols), Rows: uint32(i.Rows),
	}
	if !i.Running {
		code := int32(i.ExitCode)
		ti.ExitCode = &code
	}
	return ti
}
