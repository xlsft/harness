package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
)

// Per-session remote control (the "Session relay" switch of the frontends).
//
// A session is mirrored to the harness server by the relay host (packages/client/relay) when remote control is
// available on this client (a relay host is attached, SetRelayHost) and on for the session: the last choice
// recorded in its transcript (SetSessionRelay, a harness-meta remoteControl entry), else the settings
// ("remoteControl", default true). A policy document with "remoteControl": false turns it off for good.

// RelayHost is the relay host mirroring this server's sessions (relay.Attach sets it).
type RelayHost interface {
	// SetSessionRelay starts mirroring a session (registers it and catches viewers up from its event log) or
	// stops (unregisters it upstream, no more events or commands).
	SetSessionRelay(sessionID string, enabled bool)
}

const (
	relayNotConnected = "Not signed in to a harness server"
	relayPolicyOff    = "Remote control is turned off by policy settings"
)

// SetRelayHost attaches the relay host: remote control becomes available for local sessions.
func (s *Server) SetRelayHost(h RelayHost) {
	if h == nil {
		s.relayHost.Store(nil)
		return
	}
	s.relayHost.Store(&h)
}

func (s *Server) relay() RelayHost {
	if p := s.relayHost.Load(); p != nil {
		return *p
	}
	return nil
}

// relaySettingsLocked reads the settings: whether policy turns remote control off, and the default for
// sessions without a recorded choice.
func (ls *liveSession) relaySettingsLocked() (policyOff, def bool) {
	st := ls.sess.Settings
	if st == nil {
		return false, true
	}
	def = true
	if raw, ok := st.Raw[settings.KeyRemoteControl]; ok {
		var on bool
		if json.Unmarshal(raw, &on) == nil {
			def = on
		}
	}
	return st.RemoteControlPolicyOff, def
}

// relayWantedLocked reports whether the session should be mirrored when a relay host runs (ls.mu held).
func (ls *liveSession) relayWantedLocked() bool {
	policyOff, def := ls.relaySettingsLocked()
	switch {
	case policyOff:
		return false
	case ls.relayChoice != nil:
		return *ls.relayChoice
	}
	return def
}

func (ls *liveSession) relayWanted() bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.relayWantedLocked()
}

// relayStateLocked fills the relay fields of info (ls.mu held).
func (ls *liveSession) relayStateLocked(info *agentv1.SessionInfo) {
	policyOff, _ := ls.relaySettingsLocked()
	switch {
	case ls.srv.relay() == nil:
		info.RelayUnavailableReason = ls.srv.RelayOffReason
		if info.RelayUnavailableReason == "" {
			info.RelayUnavailableReason = relayNotConnected
		}
	case policyOff:
		info.RelayUnavailableReason = relayPolicyOff
	default:
		info.RelayAvailable = true
		info.RelayEnabled = ls.relayWantedLocked()
	}
}

// SetSessionRelay turns remote control on or off for an open session (see the package comment above).
func (s *Server) SetSessionRelay(ctx context.Context, req *connect.Request[agentv1.SetSessionRelayRequest]) (*connect.Response[agentv1.SetSessionRelayResponse], error) {
	id := req.Msg.GetSessionId()
	ls, err := s.get(id)
	if err != nil {
		if info := s.remoteInfo(id); info != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("session %s runs on %s: its remote control is switched on that machine", id, deviceLabel(info)))
		}
		return nil, err
	}
	on := req.Msg.GetEnabled()
	ls.relayMu.Lock()
	defer ls.relayMu.Unlock()
	ls.mu.Lock()
	var st agentv1.SessionInfo
	ls.relayStateLocked(&st)
	record := ls.relayChoice == nil || *ls.relayChoice != on
	ls.mu.Unlock()
	if on && !st.RelayAvailable {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(st.RelayUnavailableReason))
	}
	if record {
		if _, err := ls.sess.Transcript.Append(session.Entry{Type: session.TypeMeta, RemoteControl: &on}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("record the remote control choice: %w", err))
		}
		ls.mu.Lock()
		ls.relayChoice = &on
		ls.mu.Unlock()
	}
	if h := s.relay(); h != nil {
		h.SetSessionRelay(id, on)
	}
	ls.publishInfo()
	return connect.NewResponse(&agentv1.SetSessionRelayResponse{Session: ls.info()}), nil
}
