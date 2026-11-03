package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/tools"
)

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
	return t.callStdio(ctx, env, call)
}

func (t *Tool) callRemote(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	args := string(call.Input)
	if args == "" {
		args = "{}"
	}
	req := &mcpv1.CallToolRequest{ServerId: t.srv.cfg.GetId(), ToolName: t.upstream, ArgumentsJson: args, ToolUseId: call.ID}
	if env != nil {
		req.SessionId = env.SessionID
	}
	res, err := t.m.o.Client.CallTool(ctx, req, nil)
	if err != nil {
		if ctx.Err() != nil {
			return tools.ErrorResult("The call was cancelled.")
		}
		if IsNeedsAuth(err) {
			return t.needsAuth()
		}
		return tools.ErrorResult("MCP server %s failed: %s", t.srv.name(), errorMessage(err))
	}
	return ResultFromJSON([]byte(res.GetResultJson()))
}

// needsAuth is the tool error for a server whose authorization is missing or expired.
func (t *Tool) needsAuth() tools.Result {
	hint := "open the MCP settings in the harness web or desktop app and connect it"
	if a := t.m.byName[ToolName(t.srv.name(), "authenticate")]; a != nil && a.auth {
		hint = "call " + a.name + " to get an authorization link, or " + hint
	}
	return tools.ErrorResult("MCP server %s needs authorization. Ask the user to authorize it (%s), then try again.", t.srv.name(), hint)
}

func (t *Tool) authenticate(ctx context.Context) tools.Result {
	r, err := t.m.o.Client.StartOAuth(ctx, t.srv.cfg.GetId())
	if err != nil {
		return tools.ErrorResult("Could not start the authorization of MCP server %s: %s", t.srv.name(), errorMessage(err))
	}
	msg := fmt.Sprintf("Ask the user to open this link in a browser to authorize the MCP server %s:\n%s\n\nThe authorization is stored on the harness server and works on every device. The server's tools become available in the next session once the user has finished.", t.srv.name(), r.GetAuthorizationUrl())
	return tools.TextResult(msg, map[string]any{"server": t.srv.name(), "authorizationUrl": r.GetAuthorizationUrl(), "expiresAt": r.GetExpiresAt()})
}

func (t *Tool) callStdio(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	conn, res := t.m.ensureStarted(ctx, env, t.srv, call.ID)
	if conn == nil {
		return res
	}
	raw, err := conn.call(ctx, t.upstream, call.Input)
	if err != nil {
		if ctx.Err() != nil {
			return tools.ErrorResult("The call was cancelled.")
		}
		msg := err.Error()
		if s := conn.stderr.String(); s != "" && errors.Is(err, context.DeadlineExceeded) {
			msg += " (" + lastLine(s) + ")"
		}
		return tools.ErrorResult("MCP server %s failed: %s", t.srv.name(), msg)
	}
	return ResultFromJSON(raw)
}

// ensureStarted returns the running connection of a stdio server, asking the user for approval and
// starting it when needed. On failure it returns nil and the tool error.
func (m *Manager) ensureStarted(ctx context.Context, env *tools.Env, s *server, toolUseID string) (*stdioConn, tools.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn, tools.Result{}
	}
	if s.unavailable != "" {
		return nil, tools.ErrorResult("MCP server %s is unavailable on this device: %s", s.name(), s.unavailable)
	}
	switch s.approval {
	case mcpv1.ApprovalState_APPROVAL_STATE_DENIED:
		return nil, tools.ErrorResult("The user did not allow MCP server %s to run on this device.", s.name())
	case mcpv1.ApprovalState_APPROVAL_STATE_PENDING:
		ok, err := m.askApproval(ctx, env, s, toolUseID)
		if err != nil {
			return nil, tools.ErrorResult("MCP server %s needs the user's approval before it can start on this device, and nobody can be asked now (%v). The user can approve it by running harness interactively or with HARNESS_MCP_APPROVE=%s.", s.name(), err, s.name())
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
			return nil, tools.ErrorResult("The user did not allow MCP server %s to run on this device.", s.name())
		}
	}
	err := m.launchLocked(ctx, s)
	m.report(ctx, s)
	if err != nil {
		return nil, tools.ErrorResult("MCP server %s could not start: %v", s.name(), err)
	}
	return s.conn, tools.Result{}
}

// Approval answers.
const (
	approveLabel = "Allow"
	denyLabel    = "Deny"
)

func (m *Manager) askApproval(ctx context.Context, env *tools.Env, s *server, toolUseID string) (bool, error) {
	var ia tools.Interactor
	if env != nil {
		if h := env.Host(); h != nil {
			ia = h.Interactor()
		}
	}
	if ia == nil {
		return false, tools.ErrNonInteractive
	}
	cmd := stdioCommand(s.cfg)
	if args := s.cfg.GetStdio().GetArgs(); len(args) > 0 {
		cmd += " " + fmt.Sprint(args)
	}
	noOther := false
	q := tools.Question{
		Header:     "MCP server",
		Type:       tools.QuestionSingle,
		AllowOther: &noOther,
		Question:   fmt.Sprintf("Allow the MCP server %q to start on this device? It runs: %s", s.name(), cmd),
		Options: []tools.QuestionOption{
			{Label: approveLabel, Description: "Start it now and remember the decision for this configuration"},
			{Label: denyLabel, Description: "Do not start it on this device"},
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

// Approve records a local decision for a stdio server by name (frontends' approval screens).
// It takes effect for the next launch of that server.
func (m *Manager) Approve(ctx context.Context, name string, approved bool) error {
	for _, s := range m.servers {
		if s.remote || s.name() != name {
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
	return fmt.Errorf("no stdio MCP server named %q", name)
}
