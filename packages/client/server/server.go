// Package server implements harness.agent.v1.AgentService on top of the agent
// loop. Frontends (desktop over a local socket, SSH over forwarded stdio, the
// relay later) talk to the client through it.
package server

import (
	"context"
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
	defer s.mu.Unlock()
	for _, ls := range s.live {
		ls.driver.Interrupt()
		ls.driver.Wait()
		ls.sess.Close()
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
	mux := http.NewServeMux()
	path, h := s.Handler()
	mux.Handle(path, h)
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: mux, Protocols: p, ReadHeaderTimeout: 10 * time.Second}
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
	if m.GetFork() {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("fork is not implemented yet"))
	}
	if id := m.GetResumeSessionId(); id != "" {
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
	if m.GetPermissionMode() != agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED || opts.PermissionMode == "" {
		opts.PermissionMode = mode
	}
	opts.Resume = m.GetResumeSessionId()
	if opts.Entrypoint == "" {
		opts.Entrypoint = "agent-service"
	}
	ls := &liveSession{srv: s, hub: newHub(), pending: map[string]chan permissions.Decision{}, seen: map[string]bool{}, status: agentv1.SessionStatus_SESSION_STATUS_IDLE}
	opts.OnEvent = ls.onEvent
	opts.Logger = s.log()
	sess, err := bootstrap.Open(ctx, opts)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	ls.sess = sess
	ls.branch = workspace.GitBranch(ctx, sess.Cwd)
	ls.hub.sessionID = sess.ID
	sess.Permissions.SetAsker(ls)
	ls.driver = agent.NewDriver(s.ctx, sess.Agent, ls.onResult)
	s.mu.Lock()
	s.live[sess.ID] = ls
	s.mu.Unlock()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: ls.info()}})
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
			out[id] = &agentv1.SessionInfo{Id: id, Cwd: cwd, Title: sessionTitle(f), Status: agentv1.SessionStatus_SESSION_STATUS_IDLE, UpdatedAt: st.ModTime().UTC().Format(time.RFC3339)}
		}
	}
	s.mu.Lock()
	for id, ls := range s.live {
		if cwd == "" || ls.sess.Cwd == cwd {
			info := ls.info()
			if old, ok := out[id]; ok {
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
		if e.Type != session.TypeUser || e.IsMeta || e.Message == nil {
			continue
		}
		for _, b := range e.Blocks() {
			if b.Type == llm.BlockText && b.Text != "" {
				t := strings.TrimSpace(strings.SplitN(b.Text, "\n", 2)[0])
				if len(t) > 80 {
					t = t[:80] + "…"
				}
				return t
			}
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
	queued := ls.driver.Submit(content)
	if queued {
		ls.mu.Lock()
		ls.queue = append(ls.queue, &agentv1.UserMessage{Uuid: req.Msg.GetUuid(), Content: req.Msg.GetContent()})
		q := append([]*agentv1.UserMessage(nil), ls.queue...)
		ls.mu.Unlock()
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{Queued: q}}})
		if req.Msg.GetDelivery() == agentv1.SendUserMessageRequest_DELIVERY_NOW {
			// Interrupt so the queued message starts the next turn right away.
			// TODO(M1): background running Bash instead of killing it.
			ls.driver.Interrupt()
		}
	}
	return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
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
	ls.mu.Lock()
	ch := ls.pending[m.GetRequestId()]
	delete(ls.pending, m.GetRequestId())
	ls.mu.Unlock()
	if ch == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending permission request %s", m.GetRequestId()))
	}
	var d permissions.Decision
	switch m.GetDecision() {
	case agentv1.RespondToPermissionRequest_DECISION_ALLOW, agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS:
		d.Behavior = permissions.Allow
		if j := m.GetUpdatedInputJson(); j != "" {
			d.UpdatedInput = []byte(j)
		}
		if m.GetDecision() == agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS && m.GetRule() != "" {
			// TODO(M1): persist the rule to .claude/settings.local.json.
			_ = ls.sess.Permissions.AllowAlways([]string{m.GetRule()})
		}
	case agentv1.RespondToPermissionRequest_DECISION_DENY:
		d.Behavior = permissions.Deny
		d.Message = m.GetComment()
		// Without a comment the turn stops; with one the model continues.
		d.Interrupt = m.GetComment() == ""
	default:
		ls.mu.Lock()
		ls.pending[m.GetRequestId()] = ch
		ls.mu.Unlock()
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decision is required"))
	}
	ch <- d
	return connect.NewResponse(&agentv1.RespondToPermissionResponse{}), nil
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
	display := id
	if ls.sess.Catalog != nil {
		m, ok := ls.sess.Catalog.Find(id)
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown model %s", id))
		}
		display = m.DisplayName
	}
	// TODO(M1): the prompt bundle is per model; the session keeps its frozen bundle.
	ls.sess.Agent.SetModel(id, display)
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
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: ls.info()}})
	return connect.NewResponse(&agentv1.SetPermissionModeResponse{}), nil
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
	}
	return "", fmt.Errorf("unknown permission mode %v", m)
}

func modeToProto(m permissions.Mode) agentv1.PermissionMode {
	switch m {
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

// ---- live session ----

type liveSession struct {
	srv    *Server
	sess   *bootstrap.Session
	driver *agent.Driver
	hub    *hub

	mu        sync.Mutex
	status    agentv1.SessionStatus
	pending   map[string]chan permissions.Decision
	queue     []*agentv1.UserMessage
	seen      map[string]bool
	messageID string
	branch    string
	updatedAt time.Time
}

func (ls *liveSession) info() *agentv1.SessionInfo {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	updated := ls.updatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	return &agentv1.SessionInfo{
		Id: ls.sess.ID, Cwd: ls.sess.Cwd, Model: ls.sess.Agent.Model(), GitBranch: ls.branch,
		PermissionMode: modeToProto(ls.sess.Permissions.Mode()), Status: ls.status,
		UpdatedAt: updated.UTC().Format(time.RFC3339),
	}
}

func (ls *liveSession) publish(ev *agentv1.SessionEvent) {
	ls.mu.Lock()
	ls.updatedAt = time.Now()
	ls.mu.Unlock()
	ls.hub.publish(ev)
}

func (ls *liveSession) setStatus(st agentv1.SessionStatus) {
	ls.mu.Lock()
	changed := ls.status != st
	ls.status = st
	ls.mu.Unlock()
	if changed {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: ls.info()}})
	}
}

// onEvent maps agent events to session events.
func (ls *liveSession) onEvent(e agent.Event) {
	switch e.Kind {
	case agent.EventUser:
		ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_WORKING)
		// Tool results are reported through ToolCallFinished; only plain user
		// content (prompts, injected queued messages, interrupt markers) is sent.
		var content []llm.ContentBlock
		for _, b := range e.Message.Content {
			if b.Type != llm.BlockToolResult {
				content = append(content, b)
			}
		}
		if len(content) == 0 {
			return
		}
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserMessage{UserMessage: &agentv1.UserMessage{Uuid: e.UUID, Content: pbconv.Blocks(content)}}})
	case agent.EventStream:
		ls.onStream(e.Raw)
	case agent.EventAssistant:
		am := &agentv1.AssistantMessage{Content: pbconv.Blocks(e.Message.Content)}
		if r := e.Response; r != nil {
			am.MessageId, am.Model, am.StopReason, am.Usage = r.ID, r.Model, pbconv.StopReason(r.StopReason), pbconv.Usage(r.Usage)
		}
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_AssistantMessage{AssistantMessage: am}})
	case agent.EventToolStart:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallStarted{ToolCallStarted: &agentv1.ToolCallStarted{
			ToolUseId: e.ToolUseID, Name: e.ToolName, InputJson: string(e.Input), Summary: toolSummary(e.ToolName, e.Input, ls.sess.Cwd),
		}}})
	case agent.EventToolEnd:
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallFinished{ToolCallFinished: &agentv1.ToolCallFinished{
			ToolUseId: e.ToolUseID, IsError: e.IsError, Interrupted: e.Interrupted, Content: pbconv.Blocks(e.Content), DurationMs: e.Duration.Milliseconds(),
		}}})
	case agent.EventQueueDrained:
		ls.mu.Lock()
		ls.queue = nil
		ls.mu.Unlock()
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{}}})
	case agent.EventRetry:
		text := fmt.Sprintf("Retrying (attempt %d of %d) in %d ms", e.Retry.Attempt, e.Retry.MaxRetries, e.Retry.RetryDelayMs)
		if e.Retry.Error != nil {
			text += ": " + e.Retry.Error.Type
		}
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_Notice{Notice: &agentv1.Notice{Level: agentv1.Notice_LEVEL_WARNING, Kind: "retry", Text: text}}})
	}
}

func (ls *liveSession) onResult(res agent.TurnResult) {
	tf := &agentv1.TurnFinished{
		NumTurns: int32(res.NumTurns), Usage: pbconv.Usage(res.Usage), CostUsd: res.CostUSD,
		DurationMs: res.Duration.Milliseconds(), ErrorMessage: strings.Join(res.Errors, "; "),
	}
	switch {
	case res.Interrupted:
		tf.Result = agentv1.TurnFinished_RESULT_INTERRUPTED
	case res.Subtype == agent.SubtypeSuccess:
		tf.Result = agentv1.TurnFinished_RESULT_SUCCESS
	case res.Subtype == agent.SubtypeErrorMaxTurns:
		tf.Result = agentv1.TurnFinished_RESULT_ERROR_MAX_TURNS
	default:
		tf.Result = agentv1.TurnFinished_RESULT_ERROR_DURING_EXECUTION
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: tf}})
	if ls.srv.ctx.Err() == nil && ls.sess.Agent.QueueLen() == 0 {
		ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_IDLE)
	}
}

// Ask implements permissions.Asker: it publishes a PermissionRequest and
// waits for RespondToPermission.
func (ls *liveSession) Ask(ctx context.Context, req permissions.Request) (permissions.Decision, error) {
	id := "perm_" + uuid.NewString()
	ch := make(chan permissions.Decision, 1)
	ls.mu.Lock()
	ls.pending[id] = ch
	ls.mu.Unlock()
	defer func() {
		ls.mu.Lock()
		delete(ls.pending, id)
		ls.mu.Unlock()
	}()
	pr := &agentv1.PermissionRequest{RequestId: id, ToolUseId: req.ToolUseID, ToolName: req.ToolName, InputJson: string(req.Input)}
	if req.ToolName == "Bash" && req.Command != "" {
		if f := strings.Fields(req.Command); len(f) > 0 {
			pr.SuggestedRules = []string{"Bash(" + f[0] + " *)"}
		}
	} else {
		pr.SuggestedRules = []string{req.ToolName}
	}
	ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT)
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionRequest{PermissionRequest: pr}})
	defer ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_WORKING)
	select {
	case <-ctx.Done():
		return permissions.Decision{}, ctx.Err()
	case d := <-ch:
		return d, nil
	}
}

// toolSummary renders a short label such as "Read src/main.go".
func toolSummary(name string, input []byte, cwd string) string {
	rel := func(p string) string {
		if r, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
		return p
	}
	switch name {
	case "Read", "Write", "Edit":
		return name + " " + rel(toolsFilePath(input))
	case "Bash":
		c := toolsBashCommand(input)
		if len(c) > 80 {
			c = c[:80] + "…"
		}
		return "Bash " + c
	case "Glob", "Grep":
		return name + " " + jsonField(input, "pattern")
	}
	return name
}
