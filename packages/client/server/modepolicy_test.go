package server_test

import (
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/server"
)

func TestModePolicy(t *testing.T) {
	policy := permissions.NewModePolicy(permissions.ModeDontAsk)
	h := startHarnessSrv(t, nil, nil, nil,
		func(s *server.Server) { s.SetModePolicy(policy) })

	// Initialize leaves disabled modes out of the cycle and lists them.
	init := func() *agentv1.InitializeResponse {
		r, err := h.client.Initialize(h.ctx, connect.NewRequest(&agentv1.InitializeRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	if r := init(); !slices.Equal(r.DisabledPermissionModes, []agentv1.PermissionMode{agentv1.PermissionMode_PERMISSION_MODE_DONT_ASK}) || len(r.PermissionModes) != 4 {
		t.Fatalf("initialize %v / %v", r.PermissionModes, r.DisabledPermissionModes)
	}
	policy.Set([]permissions.Mode{permissions.ModeAuto, permissions.ModeBypassPermissions})
	r := init()
	if slices.Contains(r.PermissionModes, agentv1.PermissionMode_PERMISSION_MODE_AUTO) || len(r.PermissionModes) != 3 ||
		!slices.Equal(r.DisabledPermissionModes, []agentv1.PermissionMode{agentv1.PermissionMode_PERMISSION_MODE_AUTO, agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS}) {
		t.Fatalf("initialize after change %v / %v", r.PermissionModes, r.DisabledPermissionModes)
	}

	// CreateSession with a disabled mode is refused; with none, the fallback avoids disabled modes.
	_, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS}))
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("create in a disabled mode: %v", err)
	}

	// SetPermissionMode to a disabled mode is refused; to an allowed one works.
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	set := func(m agentv1.PermissionMode) error {
		_, err := h.client.SetPermissionMode(h.ctx, connect.NewRequest(&agentv1.SetPermissionModeRequest{SessionId: sid, Mode: m}))
		return err
	}
	if err := set(agentv1.PermissionMode_PERMISSION_MODE_AUTO); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("set disabled mode: %v", err)
	}
	if err := set(agentv1.PermissionMode_PERMISSION_MODE_PLAN); err != nil {
		t.Fatal(err)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		return ev.GetPermissionModeChanged().GetMode() == agentv1.PermissionMode_PERMISSION_MODE_PLAN
	})

	// Disabling the live session's mode switches it to the closest allowed one, with a notice.
	policy.Set([]permissions.Mode{permissions.ModePlan})
	var notice *agentv1.Notice
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if n := ev.GetNotice(); n != nil && n.Kind == "permission_mode_disabled" {
			notice = n
		}
		return notice != nil // published right after the mode change
	})
	if !strings.Contains(notice.Text, "Plan mode was disabled") || h.mode(sid) != agentv1.PermissionMode_PERMISSION_MODE_DEFAULT {
		t.Fatalf("notice %q mode %v", notice.Text, h.mode(sid))
	}
	// Re-enabled modes can be chosen again.
	policy.Set(nil)
	if err := set(agentv1.PermissionMode_PERMISSION_MODE_PLAN); err != nil {
		t.Fatal(err)
	}
}

// mode is the session's permission mode as ListSessions reports it.
func (h *harness) mode(sid string) agentv1.PermissionMode {
	h.t.Helper()
	list, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil {
		h.t.Fatal(err)
	}
	for _, s := range list.Msg.Sessions {
		if s.Id == sid {
			return s.PermissionMode
		}
	}
	h.t.Fatalf("session %s not listed", sid)
	return 0
}
