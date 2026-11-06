package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/tools"
)

// defaultToolTimeout limits one tool call when neither the server nor HARNESS_MCP_TOOL_TIMEOUT_MS says otherwise.
const defaultToolTimeout = 10 * time.Minute

// Tool is one MCP tool exposed to the model as mcp__<server>__<tool>.
type Tool struct {
	m        *Manager
	srv      *server
	name     string
	upstream string
	def      llm.ToolDef
	// deferred tools are offered only after ToolSearch loaded them.
	deferred  bool
	alwaysAsk bool
	// auth marks the pseudo tool that starts the server's authorization.
	auth bool
}

var (
	_ tools.Tool        = (*Tool)(nil)
	_ tools.AlwaysAsker = (*Tool)(nil)
)

// Name implements tools.Tool.
func (t *Tool) Name() string { return t.name }

// Def returns the model-facing definition.
func (t *Tool) Def() llm.ToolDef { return t.def }

// Deferred reports whether the tool is offered only after ToolSearch loaded it.
func (t *Tool) Deferred() bool { return t.deferred }

// ReadOnly implements tools.Tool. MCP tools always go through the permission check: annotations
// such as readOnlyHint come from the server and are not trusted to skip it.
func (t *Tool) ReadOnly(json.RawMessage) bool { return false }

// AlwaysAsk implements tools.AlwaysAsker: servers with requires_user_interaction confirm every call.
func (t *Tool) AlwaysAsk() bool { return t.alwaysAsk }

// Run implements tools.Tool.
func (t *Tool) Run(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	if t.auth {
		return t.authenticate(ctx)
	}
	if t.deferred && !t.m.isLoaded(t.name) {
		return tools.ErrorResult("%s is a deferred tool and its schema has not been loaded. Call %s with query \"select:%s\" first, then call it again.", t.name, ToolSearchName, t.name)
	}
	if len(call.Input) > 0 {
		var probe map[string]any
		if err := json.Unmarshal(call.Input, &probe); err != nil {
			return tools.ErrorResult("InputValidationError: input must be a JSON object: %v", err)
		}
	}
	if t.srv.remote {
		return t.callRemote(ctx, env, call)
	}
	return t.callDirect(ctx, env, call)
}

func (t *Tool) callRemote(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	if res, ok := t.m.ensureApproved(ctx, env, t.srv, call.ID); !ok {
		return res
	}
	args := string(call.Input)
	if args == "" {
		args = "{}"
	}
	timeout := t.m.timeoutOf(t.srv)
	req := &mcpv1.CallToolRequest{ServerId: t.srv.cfg.GetId(), ToolName: t.upstream, ArgumentsJson: args, ToolUseId: call.ID, TimeoutMs: timeout.Milliseconds()}
	if env != nil {
		req.SessionId = env.SessionID
	}
	handlers := CallHandlers{
		OnProgress: func(p *mcpv1.Progress) { t.m.emitProgress(call.ID, p.GetProgress(), p.GetTotal(), p.GetMessage()) },
		OnListChanged: func(k mcpv1.ListChanged_Kind) {
			t.m.listChangedRemote(t.srv, k)
		},
		OnElicit: func(ctx context.Context, e *mcpv1.ElicitationRequest) *mcpv1.RespondToElicitationRequest {
			return t.m.answerRemoteElicitation(ctx, env, t.srv, call.ID, e)
		},
	}
	res, err := t.m.o.Client.CallTool(ctx, req, handlers)
	if err != nil {
		if ctx.Err() != nil {
			return tools.ErrorResult("The call was cancelled.")
		}
		if IsNeedsAuth(err) {
			return t.needsAuth()
		}
		return tools.ErrorResult("MCP server %s failed: %s", t.srv.name(), errorMessage(err))
	}
	return t.m.result(t.srv, env, []byte(res.GetResultJson()))
}

// needsAuth is the tool error for a server whose authorization is missing or expired.
func (t *Tool) needsAuth() tools.Result {
	hint := "open Extensions > Connectors in the xlsft`s harness web or desktop app and connect it"
	if a := t.m.lookup(ToolName(t.srv.name(), "authenticate")); a != nil && a.auth {
		hint = "call " + a.name + " to get an authorization link, or " + hint
	}
	return tools.ErrorResult("MCP server %s needs authorization. Ask the user to authorize it (%s), then try again.", t.srv.name(), hint)
}

func (t *Tool) authenticate(ctx context.Context) tools.Result {
	r, err := t.m.o.Client.StartOAuth(ctx, t.srv.cfg.GetId())
	if err != nil {
		return tools.ErrorResult("Could not start the authorization of MCP server %s: %s", t.srv.name(), errorMessage(err))
	}
	msg := fmt.Sprintf("Ask the user to open this link in a browser to authorize the MCP server %s:\n%s\n\nThe authorization is stored on the xlsft`s harness server and works on every device. The server's tools become available in the next session once the user has finished.", t.srv.name(), r.GetAuthorizationUrl())
	return tools.TextResult(msg, map[string]any{"server": t.srv.name(), "authorizationUrl": r.GetAuthorizationUrl(), "expiresAt": r.GetExpiresAt()})
}

// errCallTimeout ends a call that ran past its server's limit.
var errCallTimeout = errors.New("the call timed out")

// inflightCall is a running tool call of a direct server: elicitations and progress find their call through it.
type inflightCall struct {
	ctx   context.Context
	env   *tools.Env
	id    string
	token string
	timer *pausableTimer
}

func (t *Tool) callDirect(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	conn, res := t.m.ensureStarted(ctx, env, t.srv, call.ID)
	if conn == nil {
		return res
	}
	t.m.syncResourceTools() // a server that started just now may be the first with resources
	limit := t.m.timeoutOf(t.srv)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := newPausableTimer(limit, func() { cancel(errCallTimeout) })
	defer timer.stop()
	track := &inflightCall{ctx: ctx, env: env, id: call.ID, token: progressToken(call.ID), timer: timer}
	t.m.addInflight(t.srv, track)
	defer t.m.removeInflight(t.srv, track)
	raw, err := conn.call(ctx, t.upstream, call.Input, track.token)
	if err != nil {
		if errors.Is(context.Cause(ctx), errCallTimeout) {
			return tools.ErrorResult("MCP server %s did not answer within %s.", t.srv.name(), limit)
		}
		if ctx.Err() != nil {
			return tools.ErrorResult("The call was cancelled.")
		}
		msg := err.Error()
		if s := conn.stderr.String(); s != "" && errors.Is(err, context.DeadlineExceeded) {
			msg += " (" + lastLine(s) + ")"
		}
		t.m.dropIfDead(t.srv, conn)
		return tools.ErrorResult("MCP server %s failed: %s", t.srv.name(), msg)
	}
	return t.m.result(t.srv, env, raw)
}

const progressPrefix = "harness-"

// progressToken names the call in the progress notifications a server sends about it.
func progressToken(toolUseID string) string { return progressPrefix + toolUseID }

func (m *Manager) addInflight(s *server, c *inflightCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight[s.cfg.GetId()] = append(m.inflight[s.cfg.GetId()], c)
}

func (m *Manager) removeInflight(s *server, c *inflightCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.inflight[s.cfg.GetId()]
	for i, x := range list {
		if x == c {
			m.inflight[s.cfg.GetId()] = append(list[:i:i], list[i+1:]...)
			return
		}
	}
}

// currentCall returns the newest running call of a server.
func (m *Manager) currentCall(s *server) *inflightCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if list := m.inflight[s.cfg.GetId()]; len(list) > 0 {
		return list[len(list)-1]
	}
	return nil
}

// dropIfDead forgets a connection whose process or stream ended, so that the next call starts it again.
func (m *Manager) dropIfDead(s *server, conn *directConn) {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	if conn.session.Ping(ctx, nil) == nil {
		return
	}
	s.mu.Lock()
	if s.conn == conn {
		s.conn = nil
	}
	s.mu.Unlock()
	conn.close()
}

func (m *Manager) timeoutOf(s *server) time.Duration {
	if ms := s.cfg.GetToolTimeoutMs(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return m.o.ToolTimeout
}

func (m *Manager) result(s *server, env *tools.Env, raw []byte) tools.Result {
	limit := m.o.MaxOutputChars
	if n := int(s.cfg.GetMaxOutputChars()); n > 0 {
		limit = n
	}
	dir := ""
	if env != nil {
		dir = env.ScratchDir
	}
	return resultFromJSON(raw, limit, dir)
}

func (m *Manager) emitProgress(toolUseID string, progress, total float64, message string) {
	if f := m.listenersNow().Progress; f != nil {
		f(toolUseID, progress, total, message)
	}
}

// pausableTimer fires after a duration of running time; the time the user takes to answer an elicitation is excluded.
type pausableTimer struct {
	mu      sync.Mutex
	left    time.Duration
	started time.Time
	t       *time.Timer
	fire    func()
	paused  int
	done    bool
}

func newPausableTimer(d time.Duration, fire func()) *pausableTimer {
	p := &pausableTimer{left: d, fire: fire, started: time.Now()}
	p.t = time.AfterFunc(d, fire)
	return p
}

func (p *pausableTimer) pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused++
	if p.paused == 1 && !p.done && p.t.Stop() {
		p.left -= time.Since(p.started)
	}
}

func (p *pausableTimer) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused--
	if p.paused == 0 && !p.done {
		p.started = time.Now()
		p.t = time.AfterFunc(max(p.left, 0), p.fire)
	}
}

func (p *pausableTimer) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = true
	p.t.Stop()
}

// ensureApproved asks this device's decision about a server that needs one, once. ok false: the tool error to return.
func (m *Manager) ensureApproved(ctx context.Context, env *tools.Env, s *server, toolUseID string) (tools.Result, bool) {
	if !s.needsApproval {
		return tools.Result{}, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return m.ensureApprovedLocked(ctx, env, s, toolUseID)
}

func (m *Manager) ensureApprovedLocked(ctx context.Context, env *tools.Env, s *server, toolUseID string) (tools.Result, bool) {
	switch s.approval {
	case mcpv1.ApprovalState_APPROVAL_STATE_DENIED:
		return tools.ErrorResult("The user did not allow MCP server %s to run on this device.", s.name()), false
	case mcpv1.ApprovalState_APPROVAL_STATE_PENDING, mcpv1.ApprovalState_APPROVAL_STATE_UNSPECIFIED:
		ok, err := m.askApproval(ctx, env, s, toolUseID)
		if err != nil {
			return tools.ErrorResult("MCP server %s needs the user's approval before it can start on this device, and nobody can be asked now (%v). The user can approve it in Extensions > Connectors, by running harness interactively or with HARNESS_MCP_APPROVE=%s.", s.name(), err, s.name()), false
		}
		if ok {
			s.approval = mcpv1.ApprovalState_APPROVAL_STATE_APPROVED
		} else {
			s.approval = mcpv1.ApprovalState_APPROVAL_STATE_DENIED
		}
		if err := m.o.Approvals.Set(s.cfg.GetId(), s.name(), s.cfg.GetConfigHash(), ok); err != nil {
			m.log.Warn("saving MCP approval failed", "err", err)
		}
		m.report(ctx, s)
		if !ok {
			return tools.ErrorResult("The user did not allow MCP server %s to run on this device.", s.name()), false
		}
	}
	return tools.Result{}, true
}

// ensureStarted returns the running connection of a direct server, asking the user for approval and
// starting it when needed. On failure it returns nil and the tool error.
func (m *Manager) ensureStarted(ctx context.Context, env *tools.Env, s *server, toolUseID string) (*directConn, tools.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn, tools.Result{}
	}
	if s.unavailable != "" {
		return nil, tools.ErrorResult("MCP server %s is unavailable on this device: %s", s.name(), s.unavailable)
	}
	if res, ok := m.ensureApprovedLocked(ctx, env, s, toolUseID); !ok {
		return nil, res
	}
	err := m.launchLocked(ctx, s)
	m.report(ctx, s)
	if err != nil {
		return nil, tools.ErrorResult("MCP server %s could not start: %v", s.name(), err)
	}
	if text := instructionsText([]instructionSource{{Server: s.name(), Text: s.conn.instructions()}}); text != "" {
		if f := m.listenersNow().InstructionsAdded; f != nil {
			f(text)
		}
	}
	return s.conn, tools.Result{}
}

// Approval answers.
const (
	approveLabel = "Allow"
	denyLabel    = "Deny"
)

// launchLine says what approving a server starts or contacts.
func launchLine(s *mcpv1.Server) string {
	if s.GetTransport() != mcpv1.Transport_TRANSPORT_STDIO {
		return s.GetRemote().GetUrl()
	}
	return strings.TrimSpace(stdioCommand(s) + " " + strings.Join(s.GetStdio().GetArgs(), " "))
}

func hostInteractor(env *tools.Env) tools.Interactor {
	if env != nil {
		if h := env.Host(); h != nil {
			return h.Interactor()
		}
	}
	return nil
}

func (m *Manager) askApproval(ctx context.Context, env *tools.Env, s *server, toolUseID string) (bool, error) {
	ia := hostInteractor(env)
	if ia == nil {
		return false, tools.ErrNonInteractive
	}
	text := fmt.Sprintf("Allow the MCP server %q to start on this device? It runs: %s", s.name(), launchLine(s.cfg))
	if s.remote {
		text = fmt.Sprintf("Allow the MCP server %q from this repository's .mcp.json to be used? It connects to: %s", s.name(), launchLine(s.cfg))
	}
	noOther := false
	q := tools.Question{
		Header:     "MCP server",
		Type:       tools.QuestionSingle,
		AllowOther: &noOther,
		Question:   text,
		Options: []tools.QuestionOption{
			{Label: approveLabel, Description: "Use it now and remember the decision for this configuration"},
			{Label: denyLabel, Description: "Do not use it on this device"},
		},
	}
	resp, err := ia.AskQuestions(ctx, toolUseID, []tools.Question{q})
	if err != nil {
		return false, err
	}
	// Skipping or answering with a message does not approve.
	a := resp.Answers
	return resp.Outcome == tools.QuestionAnswered && len(a) > 0 && len(a[0].Selected) == 1 && a[0].Selected[0] == approveLabel, nil
}

// Approve records a local decision for a server by name (frontends' approval screens).
// It takes effect for the next launch of that server.
func (m *Manager) Approve(ctx context.Context, name string, approved bool) error {
	for _, s := range m.servers {
		if !s.needsApproval || s.name() != name {
			continue
		}
		s.mu.Lock()
		if approved {
			s.approval = mcpv1.ApprovalState_APPROVAL_STATE_APPROVED
		} else {
			s.approval = mcpv1.ApprovalState_APPROVAL_STATE_DENIED
		}
		err := m.o.Approvals.Set(s.cfg.GetId(), s.name(), s.cfg.GetConfigHash(), approved)
		s.mu.Unlock()
		m.report(ctx, s)
		return err
	}
	return fmt.Errorf("no MCP server named %q needs approval", name)
}
