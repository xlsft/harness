package server

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

// SetModePolicy installs the server's live permission mode policy (bootstrap.WatchModePolicy) for every
// session opened from now on, and follows its changes: a live session whose mode becomes disabled switches to
// the closest allowed mode and gets a notice saying why. Call it before serving.
func (s *Server) SetModePolicy(p *permissions.ModePolicy) {
	s.Base.ModePolicy = p
	if p != nil {
		p.OnChange(s.applyModePolicy)
	}
}

func (s *Server) applyModePolicy(disabled []permissions.Mode) {
	s.mu.Lock()
	live := make([]*liveSession, 0, len(s.live))
	for _, ls := range s.live {
		live = append(live, ls)
	}
	s.mu.Unlock()
	for _, ls := range live {
		ls.applyModePolicy(disabled)
	}
}

// applyModePolicy updates the session's engine; a forced switch is announced with a notice (the mode change
// itself reaches frontends as PermissionModeChanged).
func (ls *liveSession) applyModePolicy(disabled []permissions.Mode) {
	if ls.sess == nil {
		return
	}
	from, to, switched := ls.sess.Permissions.SetPolicyDisabled(disabled)
	if !switched {
		return
	}
	ls.notice(agentv1.Notice_LEVEL_WARNING, "permission_mode_disabled",
		fmt.Sprintf("%s mode was disabled by your administrator; this session switched to %s mode.", modeLabel(from), modeLabel(to)))
}

// modeLabel is the mode's name as frontends show it.
func modeLabel(m permissions.Mode) string { return permissions.ModeLabel(m) }

// modeError maps a refused mode switch to PERMISSION_DENIED and anything else to code.
func modeError(err error, code connect.Code) error {
	var de *permissions.ModeDisabledError
	if errors.As(err, &de) {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	return connect.NewError(code, err)
}
