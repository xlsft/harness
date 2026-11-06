package server

import (
	"context"
	"errors"
	"os"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/syncclient"
)

// hookRunStatus maps Run statuses to the proto enum.
var hookRunStatus = map[string]agentv1.HookRun_Status{
	hooks.RunRunning: agentv1.HookRun_STATUS_RUNNING, hooks.RunOK: agentv1.HookRun_STATUS_OK, hooks.RunBlocked: agentv1.HookRun_STATUS_BLOCKED,
	hooks.RunError: agentv1.HookRun_STATUS_ERROR, hooks.RunTimedOut: agentv1.HookRun_STATUS_TIMED_OUT, hooks.RunCanceled: agentv1.HookRun_STATUS_CANCELED,
}

func hookRunToProto(r hooks.Run) *agentv1.HookRun {
	out := &agentv1.HookRun{RunId: r.ID, Event: r.Event, HandlerType: r.Type, Command: r.Command, Matcher: r.Matcher, Source: r.Source, Status: hookRunStatus[r.Status],
		Output: r.Output, Decision: r.Decision, DurationMs: r.Duration, ToolName: r.ToolName, ToolUseId: r.ToolUseID, AgentId: r.AgentID, Async: r.Async}
	if r.ExitCode != nil {
		code := int32(*r.ExitCode)
		out.ExitCode = &code
	}
	return out
}

var configuredStatus = map[hooks.Status]agentv1.ConfiguredHook_Status{
	hooks.StatusActive: agentv1.ConfiguredHook_STATUS_ACTIVE, hooks.StatusDisabled: agentv1.ConfiguredHook_STATUS_DISABLED,
	hooks.StatusNeedsWorkspaceTrust: agentv1.ConfiguredHook_STATUS_NEEDS_WORKSPACE_TRUST, hooks.StatusNeedsDeviceTrust: agentv1.ConfiguredHook_STATUS_NEEDS_DEVICE_TRUST,
	hooks.StatusBlockedByPolicy: agentv1.ConfiguredHook_STATUS_BLOCKED_BY_POLICY, hooks.StatusUnsupported: agentv1.ConfiguredHook_STATUS_UNSUPPORTED,
}

// configuredHooks is the list frontends show: every hook as configured, highest settings level first, with what keeps it from
// running. off and policyOff are the switches as they are now.
func configuredHooks(set *hooks.Set, off, policyOff bool) []*agentv1.ConfiguredHook {
	var out []*agentv1.ConfiguredHook
	for i := len(set.All) - 1; i >= 0; i-- {
		h := set.All[i]
		status, reason := h.Status, h.Reason
		if status == hooks.StatusActive {
			switch {
			case os.Getenv("HARNESS_NO_HOOKS") == "1":
				status, reason = hooks.StatusDisabled, "HARNESS_NO_HOOKS turns hooks off for this client."
			case policyOff:
				status, reason = hooks.StatusDisabled, "A policy turned all hooks off."
			case off && h.Source.Level != settings.LevelPolicy:
				status, reason = hooks.StatusDisabled, "All hooks are turned off."
			}
		}
		out = append(out, &agentv1.ConfiguredHook{Event: string(h.Event), Matcher: h.Matcher, HandlerType: h.Type, Command: h.Command, TimeoutSeconds: int32(h.Timeout.Seconds()),
			Async: h.Async, Source: h.SourceLabel(), SourcePath: h.Source.Path, Status: configuredStatus[status], Reason: reason, TrustHash: h.TrustHash})
	}
	return out
}

// ListHooks reports the hooks of an open session as it runs them, or what a new session in a folder would run.
func (s *Server) ListHooks(ctx context.Context, req *connect.Request[agentv1.ListHooksRequest]) (*connect.Response[agentv1.ListHooksResponse], error) {
	if id := req.Msg.GetSessionId(); id != "" {
		s.mu.Lock()
		ls := s.live[id]
		s.mu.Unlock()
		if ls != nil {
			off, policyOff := ls.sess.HookSwitches()
			return connect.NewResponse(&agentv1.ListHooksResponse{Hooks: configuredHooks(ls.sess.Hooks.Set(), off, policyOff), Cwd: ls.sess.Dir(), Snapshot: true}), nil
		}
	}
	// A folder this machine does not have (a session of another device) lists like no folder. Then there are no project
	// files to read: a directory of ours stands in.
	cwd, known := config.Home(), false
	if abs, err := absDir(req.Msg.GetCwd()); err == nil {
		cwd, known = abs, true
	}
	trusted := known && (config.IsChatDir(cwd) || permissions.IsTrusted(cwd))
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	docs, err := s.hookDocs(ctx)
	if err != nil {
		return nil, syncError(err)
	}
	st, err := settings.Load(ctx, settings.Options{Cwd: cwd, Docs: docs, Trusted: trusted})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	folder := ""
	if known {
		folder = cwd
	}
	resp := &agentv1.ListHooksResponse{Hooks: configuredHooks(bootstrap.BuildHooks(st, folder), st.HooksOff, st.HooksPolicyOff)}
	if known {
		resp.Cwd = cwd
	}
	return connect.NewResponse(resp), nil
}

// SetHooksTrust records this device's trust in hooks the server delivered.
func (s *Server) SetHooksTrust(_ context.Context, req *connect.Request[agentv1.SetHooksTrustRequest]) (*connect.Response[agentv1.SetHooksTrustResponse], error) {
	if req.Msg.GetHash() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("hash is required"))
	}
	if err := bootstrap.HookTrustStore().Set(req.Msg.GetHash(), req.Msg.GetTrusted()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.SetHooksTrustResponse{}), nil
}

// fetchedDocs is the server's documents the hook list reads when no session has a cache of them.
type fetchedDocs []*syncv1.Document

func (d fetchedDocs) Synced() bool { return true }

func (d fetchedDocs) List(kind syncv1.DocumentKind) []*syncv1.Document {
	var out []*syncv1.Document
	for _, doc := range d {
		if doc.GetKind() == kind {
			out = append(out, doc)
		}
	}
	return out
}

// hookDocs fetches the documents that can hold hooks. Nil without a server: Load then reads the user's own file instead. Policy
// documents targeted at a role are not fetched here, as they have no fixed key; a session's list has them.
func (s *Server) hookDocs(ctx context.Context) (settings.DocSource, error) {
	if s.Preferences == nil {
		return nil, nil
	}
	refs := []*syncv1.DocumentRef{
		{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, Key: settings.UserSettingsKey},
		{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_HOOK, Key: "user"},
		{Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, Key: settings.UserSettingsKey},
		{Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_POLICY, Key: "default"},
	}
	var docs fetchedDocs
	for _, ref := range refs {
		res, err := s.Preferences.GetDocument(ctx, &syncv1.GetDocumentRequest{Selector: &syncv1.GetDocumentRequest_Ref{Ref: ref}})
		if err != nil {
			if syncclient.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if d := res.GetDocument(); d != nil {
			docs = append(docs, d)
		}
	}
	return docs, nil
}
