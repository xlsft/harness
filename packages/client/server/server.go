// Package server implements harness.agent.v1.AgentService on top of the agent
// loop. Frontends (desktop over a local socket, SSH over forwarded stdio, the
// relay later) talk to the client through it.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Server implements agentv1connect.AgentServiceHandler.
type Server struct {
	agentv1connect.UnimplementedAgentServiceHandler

	// Base holds defaults applied to every session (gateway and prompt
	// adapters, allowed tools, …). Per-session fields are overridden.
	Base bootstrap.Options
	// UserEmail is reported by Initialize.
	UserEmail string
	Logger    *slog.Logger

	ctx    context.Context
	mu     sync.Mutex
	live   map[string]*liveSession
	cancel context.CancelFunc
	// observer is told about opened sessions (SetSessionObserver).
	observer func(sessionID string)
}

var _ agentv1connect.AgentServiceHandler = (*Server)(nil)

// New creates a server. Base.Gateway and Base.Prompts should be set so all
// sessions share one connection to the harness server.
func New(base bootstrap.Options) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{Base: base, ctx: ctx, cancel: cancel, live: map[string]*liveSession{}}
}

func (s *Server) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Handler returns the HTTP handler (path prefix and handler).
func (s *Server) Handler() (string, http.Handler) {
	return agentv1connect.NewAgentServiceHandler(s)
}

// Close interrupts all sessions and releases them.
func (s *Server) Close() {
	s.cancel()
	s.mu.Lock()
	live := s.live
	s.live = map[string]*liveSession{}
	s.mu.Unlock()
	for _, ls := range live {
		ls.close("host_shutdown")
	}
}

// ServeUnix serves AgentService over h2c on a unix socket until ctx ends.
func (s *Server) ServeUnix(ctx context.Context, socket string) error {
	if len(socket) >= 104 {
		return fmt.Errorf("socket path too long (%d bytes; unix sockets allow ~103): %s", len(socket), socket)
	}
	_ = os.Remove(socket)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	_ = os.Chmod(socket, 0o600)
	defer os.Remove(socket)
	return s.Serve(ctx, ln)
}

// Serve serves AgentService over h2c (and HTTP/1.1 for Connect) on ln.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	return s.serveHandler(ctx, ln, s.mux())
}

func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	path, h := s.Handler()
	mux.Handle(path, h)
	return mux
}

// serveHandler serves h over HTTP/1.1 and h2c on ln until ctx ends, then
// closes all sessions.
func (s *Server) serveHandler(ctx context.Context, ln net.Listener, h http.Handler) error {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: h, Protocols: p, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
	}()
	err := srv.Serve(ln)
	s.Close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ---- RPCs ----

func (s *Server) Initialize(ctx context.Context, req *connect.Request[agentv1.InitializeRequest]) (*connect.Response[agentv1.InitializeResponse], error) {
	resp := &agentv1.InitializeResponse{ClientVersion: config.Version, UserEmail: s.UserEmail}
	for _, m := range permissions.ModeCycle() {
		resp.PermissionModes = append(resp.PermissionModes, modeToProto(m))
	}
	if s.Base.Gateway != nil {
		if cat, err := s.Base.Gateway.Models(ctx); err == nil {
			for _, m := range cat.Models {
				resp.ModelIds = append(resp.ModelIds, m.ID)
			}
		}
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) CreateSession(ctx context.Context, req *connect.Request[agentv1.CreateSessionRequest]) (*connect.Response[agentv1.CreateSessionResponse], error) {
	m := req.Msg
	if m.GetFork() && m.GetResumeSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("fork requires resume_session_id"))
	}
	if m.Worktree != nil && m.GetResumeSessionId() != "" && !m.GetFork() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worktree cannot be combined with resume_session_id unless fork is set"))
	}
	if id := m.GetResumeSessionId(); id != "" && !m.GetFork() {
		s.mu.Lock()
		ls := s.live[id]
		s.mu.Unlock()
		if ls != nil {
			return connect.NewResponse(&agentv1.CreateSessionResponse{Session: ls.info()}), nil
		}
	}
	mode, err := modeFromProto(m.GetPermissionMode())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	opts := s.Base
	opts.Cwd = m.GetCwd()
	if opts.Cwd == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cwd is required"))
	}
	if m.GetModel() != "" {
		opts.Model = m.GetModel()
	}
	// UNSPECIFIED keeps the base mode, or leaves it to settings (permissions.defaultMode).
	if m.GetPermissionMode() != agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED {
		opts.PermissionMode = mode
	}
	if e := pbconv.FromEffort(m.GetEffort()); e != "" {
		opts.Effort = e
	}
	if n := m.GetMaxTurns(); n > 0 {
		opts.MaxTurns = int(n)
	}
	for _, r := range append(append([]string(nil), m.GetAllowedTools()...), m.GetDisallowedTools()...) {
		if err := permissions.ValidateRule(r); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	opts.AllowedTools = append(append([]string(nil), opts.AllowedTools...), m.GetAllowedTools()...)
	opts.DisallowedTools = append(append([]string(nil), opts.DisallowedTools...), m.GetDisallowedTools()...)
	opts.AdditionalDirs = append(append([]string(nil), opts.AdditionalDirs...), m.GetAdditionalDirectories()...)
	opts.Resume = m.GetResumeSessionId()
	opts.Fork = m.GetFork()
	opts.ForkAtCheckpoint = m.GetForkAtCheckpointId()
	if opts.Entrypoint == "" {
		opts.Entrypoint = "agent-service"
	}
	wt, wtMeta, err := createWorktree(ctx, m)
	if err != nil {
		return nil, err
	}
	if wt != nil {
		opts.Cwd, opts.Worktree = wt.Cwd, wtMeta
	}
	ls := newLiveSession(s)
	opts.OnEvent = ls.onEvent
	opts.Logger = s.log()
	sess, err := bootstrap.Open(ctx, opts)
	if err != nil {
		s.rollbackWorktree(wt)
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	ls.sess = sess
	ls.branch = workspace.GitBranch(ctx, sess.Cwd)
	ls.hub.sessionID = sess.ID
	ls.title = sessionTitle(sess.Transcript.Path())
	sess.Permissions.SetAsker(ls)
	sess.Agent.SetInteractor(ls)
	ls.driver = agent.NewDriver(s.ctx, sess.Agent, ls.onResult)
	s.mu.Lock()
	s.live[sess.ID] = ls
	s.mu.Unlock()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: ls.info()}})
	for _, w := range sess.Warnings {
		ls.notice(agentv1.Notice_LEVEL_WARNING, "session_warning", w)
	}
	if todos := sess.Agent.Todos(); len(todos) > 0 {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TodosUpdated{TodosUpdated: &agentv1.TodosUpdated{Todos: todosToProto(todos)}}})
	}
	s.sessionOpened(sess.ID)
	return connect.NewResponse(&agentv1.CreateSessionResponse{Session: ls.info()}), nil
}

func (s *Server) ListSessions(ctx context.Context, req *connect.Request[agentv1.ListSessionsRequest]) (*connect.Response[agentv1.ListSessionsResponse], error) {
	cwd := req.Msg.GetCwd()
	out := map[string]*agentv1.SessionInfo{}
	if cwd != "" {
		store := session.Store{Root: config.ProjectsDir()}
		files, _ := filepath.Glob(filepath.Join(store.ProjectDir(cwd), "*.jsonl"))
		for _, f := range files {
			st, err := os.Stat(f)
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
			out[id] = &agentv1.SessionInfo{Id: id, Cwd: cwd, Title: sessionTitle(f), Status: agentv1.SessionStatus_SESSION_STATUS_IDLE,
				UpdatedAt: st.ModTime().UTC().Format(time.RFC3339), ParentSessionId: forkedFrom(f), Worktree: transcriptWorktree(f)}
		}
	}
	s.mu.Lock()
	for id, ls := range s.live {
		if cwd == "" || ls.sess.Cwd == cwd {
			info := ls.info()
			if old, ok := out[id]; ok && info.Title == "" {
				info.Title = old.Title
			}
			out[id] = info
		}
	}
	s.mu.Unlock()
	resp := &agentv1.ListSessionsResponse{}
	for _, v := range out {
		resp.Sessions = append(resp.Sessions, v)
	}
	sort.Slice(resp.Sessions, func(i, j int) bool { return resp.Sessions[i].UpdatedAt > resp.Sessions[j].UpdatedAt })
	return connect.NewResponse(resp), nil
}

// sessionTitle returns the first user prompt (truncated) as a provisional title.
func sessionTitle(path string) string {
	entries, err := session.Load(path)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Type != session.TypeUser || e.IsMeta || e.IsCompactSummary || e.Message == nil || e.ToolUseResult != nil {
			continue
		}
		if t := titleOf(e.Blocks()); t != "" {
			return t
		}
	}
	return ""
}

func titleOf(blocks []llm.ContentBlock) string {
	for _, b := range blocks {
		if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" {
			t := strings.TrimSpace(strings.SplitN(strings.TrimSpace(b.Text), "\n", 2)[0])
			if len(t) > 80 {
				t = t[:80] + "…"
			}
			return t
		}
	}
	return ""
}

func forkedFrom(path string) string {
	entries, err := session.Load(path)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Type == session.TypeMeta && e.ForkedFrom != "" {
			return e.ForkedFrom
		}
	}
	return ""
}

func (s *Server) get(id string) (*liveSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ls := s.live[id]
	if ls == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open; call CreateSession first", id))
	}
	return ls, nil
}

func (s *Server) Subscribe(ctx context.Context, req *connect.Request[agentv1.SubscribeRequest], stream *connect.ServerStream[agentv1.SubscribeResponse]) error {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return err
	}
	replay, ch, unsub := ls.hub.subscribe(req.Msg.GetAfterSeq())
	defer unsub()
	for _, ev := range replay {
		if err := stream.Send(&agentv1.SubscribeResponse{Event: ev}); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				if ls.hub.isClosed() {
					return nil
				}
				return connect.NewError(connect.CodeResourceExhausted, errors.New("subscriber too slow; resubscribe with after_seq"))
			}
			if err := stream.Send(&agentv1.SubscribeResponse{Event: ev}); err != nil {
				return err
			}
		}
	}
}

func (s *Server) SendUserMessage(ctx context.Context, req *connect.Request[agentv1.SendUserMessageRequest]) (*connect.Response[agentv1.SendUserMessageResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	content := pbconv.FromBlocks(req.Msg.GetContent())
	if len(content) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("content is empty"))
	}
	if id := req.Msg.GetUuid(); id != "" {
		ls.mu.Lock()
		dup := ls.seen[id]
		ls.seen[id] = true
		ls.mu.Unlock()
		if dup {
			return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
		}
	}
	ls.mu.Lock()
	if ls.title == "" {
		ls.title = titleOf(content)
	}
	ls.mu.Unlock()
	id := req.Msg.GetUuid()
	if id == "" {
		id = uuid.NewString()
	}
	queued := ls.driver.Submit(id, content)
	if queued {
		ls.mu.Lock()
		ls.queue = append(ls.queue, &agentv1.UserMessage{Uuid: id, Content: req.Msg.GetContent()})
		q := append([]*agentv1.UserMessage(nil), ls.queue...)
		ls.mu.Unlock()
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{Queued: q}}})
		if req.Msg.GetDelivery() == agentv1.SendUserMessageRequest_DELIVERY_NOW {
			// Running shell commands move to the background so the message is
			// read in this turn right after the tool calls; otherwise the turn
			// is interrupted and the message starts the next one.
			if !ls.sess.ToolEnv.Shell.BackgroundRunning() {
				ls.driver.Interrupt()
			}
		}
	}
	return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
}

func (s *Server) RemoveQueuedMessage(ctx context.Context, req *connect.Request[agentv1.RemoveQueuedMessageRequest]) (*connect.Response[agentv1.RemoveQueuedMessageResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetUuid()
	if !ls.sess.Agent.RemoveQueued(id) {
		return connect.NewResponse(&agentv1.RemoveQueuedMessageResponse{}), nil
	}
	ls.mu.Lock()
	for i, m := range ls.queue {
		if m.GetUuid() == id {
			ls.queue = append(ls.queue[:i], ls.queue[i+1:]...)
			break
		}
	}
	q := append([]*agentv1.UserMessage(nil), ls.queue...)
	ls.mu.Unlock()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{Queued: q}}})
	return connect.NewResponse(&agentv1.RemoveQueuedMessageResponse{Removed: true}), nil
}

func (s *Server) Interrupt(ctx context.Context, req *connect.Request[agentv1.InterruptRequest]) (*connect.Response[agentv1.InterruptResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ls.driver.Interrupt()
	return connect.NewResponse(&agentv1.InterruptResponse{}), nil
}

func (s *Server) RespondToPermission(ctx context.Context, req *connect.Request[agentv1.RespondToPermissionRequest]) (*connect.Response[agentv1.RespondToPermissionResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	m := req.Msg
	if m.GetDecision() == agentv1.RespondToPermissionRequest_DECISION_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decision is required"))
	}
	ls.mu.Lock()
	p := ls.pendingPerm[m.GetRequestId()]
	delete(ls.pendingPerm, m.GetRequestId())
	ls.mu.Unlock()
	if p == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending permission request %s", m.GetRequestId()))
	}
	var d permissions.Decision
	switch m.GetDecision() {
	case agentv1.RespondToPermissionRequest_DECISION_ALLOW, agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS:
		d.Behavior = permissions.Allow
		d.Comment = m.GetComment()
		if j := m.GetUpdatedInputJson(); j != "" {
			if !json.Valid([]byte(j)) {
				ls.mu.Lock()
				ls.pendingPerm[m.GetRequestId()] = p
				ls.mu.Unlock()
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("updated_input_json is not valid JSON"))
			}
			d.UpdatedInput = []byte(j)
		}
		if m.GetDecision() == agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS {
			rules := p.suggestions
			if r := strings.TrimSpace(m.GetRule()); r != "" {
				rules = []string{r}
			}
			if err := ls.sess.Permissions.AllowAlways(rules); err != nil {
				ls.notice(agentv1.Notice_LEVEL_WARNING, "permission_rule_not_saved", err.Error())
			}
		}
	case agentv1.RespondToPermissionRequest_DECISION_DENY:
		d.Behavior = permissions.Deny
		d.Message = m.GetComment()
		// Without a comment the turn stops; with one the model continues.
		d.Interrupt = m.GetComment() == ""
	}
	d.ResolvedBy = "user"
	p.ch <- permissionAnswer{d: d, decision: m.GetDecision()}
	return connect.NewResponse(&agentv1.RespondToPermissionResponse{}), nil
}

func (s *Server) AnswerQuestion(ctx context.Context, req *connect.Request[agentv1.AnswerQuestionRequest]) (*connect.Response[agentv1.AnswerQuestionResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ls.mu.Lock()
	ch := ls.pendingQ[req.Msg.GetRequestId()]
	delete(ls.pendingQ, req.Msg.GetRequestId())
	ls.mu.Unlock()
	if ch == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending question %s", req.Msg.GetRequestId()))
	}
	var answers [][]string
	for _, a := range req.Msg.GetAnswers() {
		answers = append(answers, a.GetSelected())
	}
	ch <- answers
	return connect.NewResponse(&agentv1.AnswerQuestionResponse{}), nil
}

func (s *Server) RespondToPlan(ctx context.Context, req *connect.Request[agentv1.RespondToPlanRequest]) (*connect.Response[agentv1.RespondToPlanResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	var d tools.PlanDecision
	switch req.Msg.GetDecision() {
	case agentv1.RespondToPlanRequest_DECISION_APPROVE_ACCEPT_EDITS:
		d = tools.PlanApproveAcceptEdits
	case agentv1.RespondToPlanRequest_DECISION_APPROVE_MANUAL:
		d = tools.PlanApproveManual
	case agentv1.RespondToPlanRequest_DECISION_KEEP_PLANNING:
		d = tools.PlanKeepPlanning
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decision is required"))
	}
	ls.mu.Lock()
	ch := ls.pendingPlan[req.Msg.GetRequestId()]
	delete(ls.pendingPlan, req.Msg.GetRequestId())
	ls.mu.Unlock()
	if ch == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending plan %s", req.Msg.GetRequestId()))
	}
	ch <- planAnswer{resp: tools.PlanResponse{Decision: d, Feedback: req.Msg.GetFeedback()}, decision: req.Msg.GetDecision()}
	return connect.NewResponse(&agentv1.RespondToPlanResponse{}), nil
}

func (s *Server) SetModel(ctx context.Context, req *connect.Request[agentv1.SetModelRequest]) (*connect.Response[agentv1.SetModelResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetModel()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("model is required"))
	}
	display, window := id, 0
	if ls.sess.Catalog != nil {
		m, ok := ls.sess.Catalog.Find(id)
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown model %s", id))
		}
		display, window = m.DisplayName, m.ContextWindow
	}
	// The session keeps its frozen prompt bundle until the next compaction.
	ls.sess.Agent.SetModel(id, display, window)
	ls.mu.Lock()
	ls.sess.Model = id
	ls.mu.Unlock()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: ls.info()}})
	return connect.NewResponse(&agentv1.SetModelResponse{}), nil
}

func (s *Server) SetEffort(ctx context.Context, req *connect.Request[agentv1.SetEffortRequest]) (*connect.Response[agentv1.SetEffortResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ls.sess.Agent.SetEffort(pbconv.FromEffort(req.Msg.GetEffort()))
	return connect.NewResponse(&agentv1.SetEffortResponse{}), nil
}

func (s *Server) SetPermissionMode(ctx context.Context, req *connect.Request[agentv1.SetPermissionModeRequest]) (*connect.Response[agentv1.SetPermissionModeResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	mode, err := modeFromProto(req.Msg.GetMode())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := ls.sess.Permissions.SetMode(mode); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.SetPermissionModeResponse{}), nil
}

// SetOutputStyle switches the session's output style; the text reaches the
// model with the next request (see agent.OutputStyle).
func (s *Server) SetOutputStyle(ctx context.Context, req *connect.Request[agentv1.SetOutputStyleRequest]) (*connect.Response[agentv1.SetOutputStyleResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	deferred, err := ls.sess.SetOutputStyle(req.Msg.GetStyle())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if deferred {
		ls.notice(agentv1.Notice_LEVEL_INFO, "output_style_deferred",
			"The style applies now; its change to the coding instructions in the system prompt takes effect at the next compaction.")
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: ls.info()}})
	return connect.NewResponse(&agentv1.SetOutputStyleResponse{}), nil
}

// ListOutputStyles returns the built-in, user and project styles and the active one.
func (s *Server) ListOutputStyles(ctx context.Context, req *connect.Request[agentv1.ListOutputStylesRequest]) (*connect.Response[agentv1.ListOutputStylesResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp := &agentv1.ListOutputStylesResponse{Active: ls.sess.ActiveOutputStyle()}
	for _, st := range ls.sess.OutputStyles().List() {
		resp.Styles = append(resp.Styles, &agentv1.OutputStyleInfo{Id: st.ID, Name: st.Name, Description: st.Description, Source: st.Source})
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) SuggestFiles(ctx context.Context, req *connect.Request[agentv1.SuggestFilesRequest]) (*connect.Response[agentv1.SuggestFilesResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	files := ls.files.list(ctx, ls.sess.Cwd)
	return connect.NewResponse(&agentv1.SuggestFilesResponse{Paths: FuzzyFilter(files, req.Msg.GetQuery(), limit)}), nil
}

func (s *Server) ListCheckpoints(ctx context.Context, req *connect.Request[agentv1.ListCheckpointsRequest]) (*connect.Response[agentv1.ListCheckpointsResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp := &agentv1.ListCheckpointsResponse{}
	for _, c := range ls.sess.Agent.Checkpoints() {
		resp.Checkpoints = append(resp.Checkpoints, checkpointToProto(c))
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) Rewind(ctx context.Context, req *connect.Request[agentv1.RewindRequest]) (*connect.Response[agentv1.RewindResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	var mode agent.RewindMode
	switch req.Msg.GetMode() {
	case agentv1.RewindRequest_MODE_CODE_AND_CONVERSATION:
		mode = agent.RewindCodeAndConversation
	case agentv1.RewindRequest_MODE_CONVERSATION:
		mode = agent.RewindConversation
	case agentv1.RewindRequest_MODE_CODE:
		mode = agent.RewindCode
	case agentv1.RewindRequest_MODE_SUMMARIZE_FROM:
		mode = agent.RewindSummarizeFrom
	case agentv1.RewindRequest_MODE_SUMMARIZE_UP_TO:
		mode = agent.RewindSummarizeUpTo
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("mode is required"))
	}
	var res agent.RewindResult
	var rerr error
	if err := ls.driver.Exclusive(func() {
		res, rerr = ls.sess.Agent.Rewind(ctx, req.Msg.GetCheckpointId(), mode, req.Msg.GetSummaryInstructions())
	}); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if rerr != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, rerr)
	}
	return connect.NewResponse(&agentv1.RewindResponse{Result: rewoundToProto(res)}), nil
}

func (s *Server) Compact(ctx context.Context, req *connect.Request[agentv1.CompactRequest]) (*connect.Response[agentv1.CompactResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	var cerr error
	if err := ls.driver.Exclusive(func() {
		_, cerr = ls.sess.Agent.Compact(ctx, req.Msg.GetInstructions())
	}); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if cerr != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, cerr)
	}
	return connect.NewResponse(&agentv1.CompactResponse{}), nil
}

func (s *Server) ListTasks(ctx context.Context, req *connect.Request[agentv1.ListTasksRequest]) (*connect.Response[agentv1.ListTasksResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp := &agentv1.ListTasksResponse{}
	if ls.sess.Tasks != nil {
		for _, t := range ls.sess.Tasks.List() {
			resp.Tasks = append(resp.Tasks, &agentv1.BackgroundTask{TaskId: t.ID, Command: t.Command, Description: t.Description,
				OutputPath: t.OutputPath, Running: t.Running, ExitCode: int32(t.ExitCode), StartedAt: t.StartedAt.UTC().Format(time.RFC3339)})
		}
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) StopTask(ctx context.Context, req *connect.Request[agentv1.StopTaskRequest]) (*connect.Response[agentv1.StopTaskResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if ls.sess.Tasks == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no background tasks in this session"))
	}
	if _, err := ls.sess.Tasks.Stop(req.Msg.GetTaskId()); err != nil {
		code := connect.CodeFailedPrecondition
		if _, ok := ls.sess.Tasks.Get(req.Msg.GetTaskId()); !ok {
			code = connect.CodeNotFound
		}
		return nil, connect.NewError(code, err)
	}
	return connect.NewResponse(&agentv1.StopTaskResponse{}), nil
}

func (s *Server) CloseSession(ctx context.Context, req *connect.Request[agentv1.CloseSessionRequest]) (*connect.Response[agentv1.CloseSessionResponse], error) {
	s.mu.Lock()
	ls := s.live[req.Msg.GetSessionId()]
	delete(s.live, req.Msg.GetSessionId())
	s.mu.Unlock()
	if ls == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open", req.Msg.GetSessionId()))
	}
	// TODO(worktree): offer to remove the session's worktree (ls.sess.Worktree) here when it is clean and its
	// branch has no commits beyond base_ref; worktrees are never removed automatically today.
	ls.close("closed")
	return connect.NewResponse(&agentv1.CloseSessionResponse{}), nil
}

func modeFromProto(m agentv1.PermissionMode) (permissions.Mode, error) {
	switch m {
	case agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED, agentv1.PermissionMode_PERMISSION_MODE_DEFAULT:
		return permissions.ModeDefault, nil
	case agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS:
		return permissions.ModeAcceptEdits, nil
	case agentv1.PermissionMode_PERMISSION_MODE_PLAN:
		return permissions.ModePlan, nil
	case agentv1.PermissionMode_PERMISSION_MODE_DONT_ASK:
		return permissions.ModeDontAsk, nil
	case agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS:
		return permissions.ModeBypassPermissions, nil
	case agentv1.PermissionMode_PERMISSION_MODE_AUTO:
		return permissions.ModeAuto, nil
	}
	return "", fmt.Errorf("unknown permission mode %v", m)
}

func modeToProto(m permissions.Mode) agentv1.PermissionMode {
	switch m {
	case permissions.ModeAuto:
		return agentv1.PermissionMode_PERMISSION_MODE_AUTO
	case permissions.ModeAcceptEdits:
		return agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS
	case permissions.ModePlan:
		return agentv1.PermissionMode_PERMISSION_MODE_PLAN
	case permissions.ModeDontAsk:
		return agentv1.PermissionMode_PERMISSION_MODE_DONT_ASK
	case permissions.ModeBypassPermissions:
		return agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS
	}
	return agentv1.PermissionMode_PERMISSION_MODE_DEFAULT
}

func todosToProto(ts []tools.Todo) []*agentv1.Todo {
	out := make([]*agentv1.Todo, 0, len(ts))
	for _, t := range ts {
		st := agentv1.Todo_STATUS_PENDING
		switch t.Status {
		case "in_progress":
			st = agentv1.Todo_STATUS_IN_PROGRESS
		case "completed":
			st = agentv1.Todo_STATUS_COMPLETED
		}
		out = append(out, &agentv1.Todo{Content: t.Content, ActiveForm: t.ActiveForm, Status: st})
	}
	return out
}

func checkpointToProto(c agent.Checkpoint) *agentv1.Checkpoint {
	return &agentv1.Checkpoint{Id: c.ID, UserMessageUuid: c.UserMessageUUID, PromptPreview: c.PromptPreview,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339), HasCodeChanges: c.HasCodeChanges}
}

func rewoundToProto(r agent.RewindResult) *agentv1.Rewound {
	modes := map[agent.RewindMode]agentv1.RewindRequest_Mode{
		agent.RewindCodeAndConversation: agentv1.RewindRequest_MODE_CODE_AND_CONVERSATION,
		agent.RewindConversation:        agentv1.RewindRequest_MODE_CONVERSATION,
		agent.RewindCode:                agentv1.RewindRequest_MODE_CODE,
		agent.RewindSummarizeFrom:       agentv1.RewindRequest_MODE_SUMMARIZE_FROM,
		agent.RewindSummarizeUpTo:       agentv1.RewindRequest_MODE_SUMMARIZE_UP_TO,
	}
	return &agentv1.Rewound{CheckpointId: r.CheckpointID, Mode: modes[r.Mode], FilesRestored: int32(r.FilesRestored),
		FilesSkipped: int32(r.FilesSkipped), RestoredPrompt: r.RestoredPrompt}
}

// newUUID returns a request id with a prefix.
func newUUID(prefix string) string { return prefix + uuid.NewString() }
