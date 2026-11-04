package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"path/filepath"
	"slices"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/computeruse"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/tools"
)

// Computer use (computer_use.proto): the agent controls apps of this machine (packages/client/computeruse). All of it is
// device-local: the switch is in HARNESS_HOME/computer-use, the grants are in memory, the events and screenshots stay out
// of the relay mirror and the transcript upload. The RPCs refuse a session of another machine and a frontend that is not
// on this one, and the relay has no command for them, so an access card can only be answered here.

// computerUse returns the client's manager (created on first use).
func (s *Server) computerUse() *computeruse.Manager {
	s.computerOnce.Do(func() {
		if s.ComputerUse == nil {
			s.ComputerUse = &computeruse.Manager{Dir: filepath.Join(config.Home(), "computer-use"), Logger: s.log()}
		}
		if s.ComputerUse.OnState == nil {
			s.ComputerUse.OnState = s.onComputerState
		}
		if s.ComputerUse.OnStop == nil {
			s.ComputerUse.OnStop = s.interruptSession
		}
	})
	return s.ComputerUse
}

// remoteOriginKey marks a request that reached this machine from elsewhere (WithRemoteOrigin).
type remoteOriginKey struct{}

// WithRemoteOrigin marks ctx as serving a command from another device (the relay host).
func WithRemoteOrigin(ctx context.Context) context.Context {
	return context.WithValue(ctx, remoteOriginKey{}, true)
}

// localPeer reports whether a request's peer is on this machine: a unix socket, or a loopback address.
func localPeer(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true // no host part: a unix socket or an in-process call
	}
	ip := net.ParseIP(host)
	return ip == nil || ip.IsLoopback()
}

// noteMessageOrigin records whether a session's newest message came from someone at this machine. Computer use
// refuses otherwise: the person who could see the machine and press Stop may not be there.
func (s *Server) noteMessageOrigin(ctx context.Context, sessionID, peer string) {
	remote, _ := ctx.Value(remoteOriginKey{}).(bool)
	s.computerUse().Session(sessionID).SetRemoteOrigin(remote || !localPeer(peer))
}

var computerTiers = map[computeruse.Tier]agentv1.ComputerAccessTier{
	computeruse.TierRead:    agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_READ,
	computeruse.TierClick:   agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_CLICK,
	computeruse.TierFull:    agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_FULL,
	computeruse.TierBlocked: agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_BLOCKED,
}

// computerTierFromProto: anything unclear is blocked.
func computerTierFromProto(t agentv1.ComputerAccessTier) computeruse.Tier {
	switch t {
	case agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_READ:
		return computeruse.TierRead
	case agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_CLICK:
		return computeruse.TierClick
	case agentv1.ComputerAccessTier_COMPUTER_ACCESS_TIER_FULL:
		return computeruse.TierFull
	}
	return computeruse.TierBlocked
}

func computerProfileToProto(p computeruse.Profile) *agentv1.ComputerUseProfile {
	out := &agentv1.ComputerUseProfile{
		Id: p.ID, Name: p.Name, Browsers: computerTiers[p.Browsers], Terminals: computerTiers[p.Terminals], Other: computerTiers[p.Other],
		LiftedBlocks: p.Lifted, AllowSecureTyping: p.AllowSecureTyping, AllowRemoteOrigin: p.AllowRemoteOrigin,
		AllowSystemShortcuts: p.AllowSystemShortcuts, AllowSettingsEdit: p.AllowSettingsEdit,
	}
	for _, id := range slices.Sorted(maps.Keys(p.Apps)) {
		out.AppTiers = append(out.AppTiers, &agentv1.ComputerUseAppTier{AppId: id, Tier: computerTiers[p.Apps[id]]})
	}
	return out
}

func computerProfileFromProto(m *agentv1.ComputerUseProfile) *computeruse.Profile {
	p := &computeruse.Profile{
		Browsers: computerTierFromProto(m.GetBrowsers()), Terminals: computerTierFromProto(m.GetTerminals()), Other: computerTierFromProto(m.GetOther()),
		Lifted: m.GetLiftedBlocks(), AllowSecureTyping: m.GetAllowSecureTyping(), AllowRemoteOrigin: m.GetAllowRemoteOrigin(),
		AllowSystemShortcuts: m.GetAllowSystemShortcuts(), AllowSettingsEdit: m.GetAllowSettingsEdit(),
	}
	for _, a := range m.GetAppTiers() {
		if p.Apps == nil {
			p.Apps = map[string]computeruse.Tier{}
		}
		p.Apps[a.GetAppId()] = computerTierFromProto(a.GetTier())
	}
	return p
}

func computerPermsToProto(items []computeruse.Permission) []*agentv1.ComputerUsePermission {
	out := make([]*agentv1.ComputerUsePermission, len(items))
	for i, p := range items {
		out[i] = &agentv1.ComputerUsePermission{Id: p.ID, Name: p.Name, Granted: p.Granted, HowToGrant: p.HowToGrant}
	}
	return out
}

// onComputerState publishes a session's state to its subscribers.
func (s *Server) onComputerState(sessionID string, st computeruse.State) {
	s.mu.Lock()
	ls := s.live[sessionID]
	s.mu.Unlock()
	if ls == nil {
		return
	}
	ev := &agentv1.ComputerUseState{Controlling: st.Controlling, Action: st.Action}
	for _, g := range st.Grants {
		ev.Grants = append(ev.Grants, &agentv1.ComputerUseGrant{AppId: g.App.ID, AppName: g.App.Name, Tier: computerTiers[g.Tier]})
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ComputerUseState{ComputerUseState: ev}})
}

// interruptSession stops a session's turn, as its Stop does.
func (s *Server) interruptSession(sessionID string) {
	s.mu.Lock()
	ls := s.live[sessionID]
	s.mu.Unlock()
	if ls != nil {
		ls.driver.Interrupt()
	}
}

// computerTurnEnded turns the indicator off when a turn ends.
func (ls *liveSession) computerTurnEnded() {
	if c := ls.srv.computerUse().Lookup(ls.sess.ID); c != nil {
		c.EndTurn()
	}
}

type pendingComputerAccess struct {
	ch  chan []string
	ids []string
}

// AskComputerAccess implements computeruse.AccessAsker: it publishes the access card and waits for the user's answer.
// Stop cancels the wait.
func (ls *liveSession) AskComputerAccess(ctx context.Context, req computeruse.AccessRequest) ([]string, error) {
	id := newUUID("ca_")
	p := &pendingComputerAccess{ch: make(chan []string, 1)}
	ev := &agentv1.ComputerAccessRequested{RequestId: id, ToolUseId: req.ToolUseID, Reason: req.Reason, ProfileName: req.Profile, MissingPermissions: computerPermsToProto(req.Missing)}
	for _, a := range req.Apps {
		p.ids = append(p.ids, a.App.ID)
		ev.Apps = append(ev.Apps, &agentv1.ComputerAccessApp{Id: a.App.ID, Name: a.App.Name, Tier: computerTiers[a.Tier], TierNote: a.Note})
	}
	ls.mu.Lock()
	if ls.pendingAccess == nil {
		ls.pendingAccess = map[string]*pendingComputerAccess{}
	}
	ls.pendingAccess[id] = p
	ls.mu.Unlock()
	defer func() {
		ls.mu.Lock()
		delete(ls.pendingAccess, id)
		ls.mu.Unlock()
	}()
	ls.beginWait()
	defer ls.endWait()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ComputerAccessRequested{ComputerAccessRequested: ev}})
	resolved := func(approved []string, by string) {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ComputerAccessResolved{ComputerAccessResolved: &agentv1.ComputerAccessResolved{RequestId: id, ApprovedAppIds: approved, ResolvedBy: by}}})
	}
	select {
	case <-ctx.Done():
		resolved(nil, "interrupt")
		return nil, ctx.Err()
	case approved := <-p.ch:
		resolved(approved, "local")
		return approved, nil
	}
}

var _ computeruse.AccessAsker = (*liveSession)(nil)
var _ tools.Interactor = (*liveSession)(nil)

// computerSession returns the live session a computer use RPC is for, refusing one that is not of this machine.
func (s *Server) computerSession(id string) (*liveSession, error) {
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	if ls != nil {
		return ls, nil
	}
	if _, _, ok := s.acpScope(id); ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on an ACP agent; computer use is only available to harness sessions", id))
	}
	if remote := s.remoteInfo(id); remote != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on %s; computer use can only be controlled on that machine", id, deviceLabel(remote)))
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open", id))
}

func refuseRemotePeer(addr string) error {
	if localPeer(addr) {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("computer use can only be controlled from this machine"))
}

func (s *Server) computerStatus(ctx context.Context) *agentv1.ComputerUseStatus {
	m := s.computerUse()
	info := m.Info()
	st := &agentv1.ComputerUseStatus{Enabled: m.Enabled(), Supported: info.Supported, Platform: info.Platform, UnavailableReason: info.Reason}
	profiles, active := m.Profiles()
	st.ActiveProfileId = active
	for _, p := range profiles {
		st.Profiles = append(st.Profiles, computerProfileToProto(p))
	}
	for _, e := range computeruse.BlockEntries() {
		st.BlockEntries = append(st.BlockEntries, &agentv1.ComputerUseBlockEntry{Id: e.ID, Label: e.Label, Warning: e.Warning})
	}
	if !info.Supported {
		return st
	}
	perms, err := m.Permissions(ctx)
	switch {
	case err != nil:
		st.UnavailableReason = err.Error()
	case perms.Reason != "":
		st.UnavailableReason = perms.Reason
	default:
		st.Permissions = computerPermsToProto(perms.Items)
	}
	return st
}

// GetComputerUseStatus reports the switch and what the OS allows. It never prompts.
func (s *Server) GetComputerUseStatus(ctx context.Context, _ *connect.Request[agentv1.GetComputerUseStatusRequest]) (*connect.Response[agentv1.GetComputerUseStatusResponse], error) {
	return connect.NewResponse(&agentv1.GetComputerUseStatusResponse{Status: s.computerStatus(ctx)}), nil
}

// SetComputerUseEnabled changes the device-local switch. Turning it off stops every session's computer use.
func (s *Server) SetComputerUseEnabled(ctx context.Context, req *connect.Request[agentv1.SetComputerUseEnabledRequest]) (*connect.Response[agentv1.SetComputerUseEnabledResponse], error) {
	if err := refuseRemotePeer(req.Peer().Addr); err != nil {
		return nil, err
	}
	m := s.computerUse()
	if req.Msg.GetEnabled() && !m.Info().Supported {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(m.Info().Reason))
	}
	if err := m.SetEnabled(req.Msg.GetEnabled()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.SetComputerUseEnabledResponse{Status: s.computerStatus(ctx)}), nil
}

// SetComputerUseProfile picks the security profile and edits the custom one. Only a frontend on this machine reaches it,
// and no tool of the model does.
func (s *Server) SetComputerUseProfile(ctx context.Context, req *connect.Request[agentv1.SetComputerUseProfileRequest]) (*connect.Response[agentv1.SetComputerUseProfileResponse], error) {
	if err := refuseRemotePeer(req.Peer().Addr); err != nil {
		return nil, err
	}
	var custom *computeruse.Profile
	if req.Msg.GetCustom() != nil {
		custom = computerProfileFromProto(req.Msg.GetCustom())
	}
	if err := s.computerUse().SetProfile(req.Msg.GetActiveProfileId(), custom); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentv1.SetComputerUseProfileResponse{Status: s.computerStatus(ctx)}), nil
}

// ListComputerUseApps lists this machine's apps for the profile editor, with the tier the active profile gives them.
func (s *Server) ListComputerUseApps(ctx context.Context, req *connect.Request[agentv1.ListComputerUseAppsRequest]) (*connect.Response[agentv1.ListComputerUseAppsResponse], error) {
	if err := refuseRemotePeer(req.Peer().Addr); err != nil {
		return nil, err
	}
	m := s.computerUse()
	apps, err := m.Apps(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	profile := m.Profile()
	out := &agentv1.ListComputerUseAppsResponse{}
	for _, a := range apps {
		out.Apps = append(out.Apps, &agentv1.ComputerAccessApp{Id: a.ID, Name: a.Name, Tier: computerTiers[profile.TierFor(a)], TierNote: computeruse.TierNote(profile, a)})
	}
	return connect.NewResponse(out), nil
}

// RespondToComputerAccess is the user's answer to an access card. Apps not in the request are ignored.
func (s *Server) RespondToComputerAccess(_ context.Context, req *connect.Request[agentv1.RespondToComputerAccessRequest]) (*connect.Response[agentv1.RespondToComputerAccessResponse], error) {
	if err := refuseRemotePeer(req.Peer().Addr); err != nil {
		return nil, err
	}
	ls, err := s.computerSession(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetRequestId()
	ls.mu.Lock()
	p := ls.pendingAccess[id]
	delete(ls.pendingAccess, id) // claimed: another frontend may have answered meanwhile
	ls.mu.Unlock()
	if p == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending access request %s", id))
	}
	var approved []string
	for _, a := range req.Msg.GetApprovedAppIds() {
		if slices.Contains(p.ids, a) && !slices.Contains(approved, a) {
			approved = append(approved, a)
		}
	}
	p.ch <- approved
	return connect.NewResponse(&agentv1.RespondToComputerAccessResponse{}), nil
}

// StopComputerUse stops computer use in a session at once. Any frontend reaching this client may stop it.
func (s *Server) StopComputerUse(_ context.Context, req *connect.Request[agentv1.StopComputerUseRequest]) (*connect.Response[agentv1.StopComputerUseResponse], error) {
	ls, err := s.computerSession(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if c := s.computerUse().Lookup(ls.sess.ID); c != nil {
		c.Stop()
	}
	ls.driver.Interrupt()
	return connect.NewResponse(&agentv1.StopComputerUseResponse{}), nil
}
