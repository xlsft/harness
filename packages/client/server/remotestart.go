package server

import (
	"context"
	"path/filepath"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

// Remote starts (docs/design/chat.md): sessions started on this machine from the phone or a chat app through the relay.
// The machine takes them only while its owner allows it here (the device setting allow_remote_starts, off by default,
// never synced) and the organization's policy does not turn them off. They run only in one of this machine's recent
// folders; the relay host checks that before it creates the session.

// relayRefresher is a relay host that can register this machine again (RegisterHost carries remote_starts).
type relayRefresher interface {
	RefreshHost()
}

// RemoteStartsAllowed reports whether this machine takes remote starts now.
func (s *Server) RemoteStartsAllowed() bool {
	if s.Base.ModePolicy.Managed().RemoteStartsDisabled {
		return false
	}
	s.deviceMu.Lock()
	f := readDeviceSettings()
	s.deviceMu.Unlock()
	return f.AllowRemoteStarts != nil && *f.AllowRemoteStarts
}

// refreshRelayHost tells the relay what changed (the device setting or the policy).
func (s *Server) refreshRelayHost() {
	if r, ok := s.relay().(relayRefresher); ok {
		r.RefreshHost()
	}
}

// watchRemoteStartPolicy re-registers the relay host when the managed policy changes (SetModePolicy calls it).
func (s *Server) watchRemoteStartPolicy(p *permissions.ModePolicy) {
	if p == nil {
		return
	}
	last := p.Managed().RemoteStartsDisabled
	p.OnManagedChange(func() {
		now := p.Managed().RemoteStartsDisabled
		if now != last {
			last = now
			s.refreshRelayHost()
		}
	})
}

// RemoteStartFolderAllowed reports whether a remote start may use dir: it must be one of this machine's recent folders.
func (s *Server) RemoteStartFolderAllowed(ctx context.Context, dir string) bool {
	if dir == "" || !filepath.IsAbs(dir) {
		return false
	}
	want := filepath.Clean(dir)
	res, err := s.ListRecentFolders(ctx, connect.NewRequest(&agentv1.ListRecentFoldersRequest{Limit: maxRecentFolders}))
	if err != nil {
		return false
	}
	for _, f := range res.Msg.GetFolders() {
		if f.GetExists() && filepath.Clean(f.GetPath()) == want {
			return true
		}
	}
	return false
}

// validChatMode reports whether m may be the mode of sessions a chat app starts (never bypassPermissions).
func validChatMode(m string) bool {
	mode, err := permissions.ParseMode(m)
	return err == nil && mode != permissions.ModeBypassPermissions
}
