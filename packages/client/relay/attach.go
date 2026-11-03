package relay

import (
	"context"
	"os"
	"strings"

	"github.com/xlsft/harness/packages/client/server"
)

// serverBackend adapts server.Server (AgentService + live session feeds) to Backend.
type serverBackend struct{ *server.Server }

func (b serverBackend) SessionFeed(id string) (Feed, bool) {
	f, ok := b.Server.SessionFeed(id)
	if !ok {
		return nil, false
	}
	return f, true
}

// Enabled reports whether remote control should run: on by default when the
// client is signed in to a server (serverURL and tokens known), off with
// HARNESS_REMOTE_CONTROL=0 (or "false") or when the caller's flag says so.
func Enabled(flag bool, serverURL string, signedIn bool) bool {
	return DisabledReason(flag, serverURL, signedIn) == ""
}

// DisabledReason says why remote control does not run ("" when it does, see
// Enabled); frontends show it next to the session relay switch.
func DisabledReason(flag bool, serverURL string, signedIn bool) string {
	switch strings.ToLower(os.Getenv("HARNESS_REMOTE_CONTROL")) {
	case "0", "false", "off", "no":
		return "Remote control is turned off on this machine (HARNESS_REMOTE_CONTROL=0)"
	}
	switch {
	case !flag:
		return "Remote control is turned off on this machine (--remote-control=false)"
	case serverURL == "" || !signedIn:
		return "Not signed in to a harness server"
	}
	return ""
}

// hostSwitch adapts a Host to server.RelayHost (the per-session switch).
type hostSwitch struct {
	ctx context.Context
	h   *Host
}

func (s hostSwitch) SetSessionRelay(sessionID string, enabled bool) {
	s.h.SetSessionMirrored(s.ctx, sessionID, enabled)
}

// Attach mirrors every session of srv (already open and opened later) through
// a relay host and starts it; it runs until ctx ends. o.Backend is set to srv.
// Sessions with remote control off (settings "remoteControl": false, or the
// per-session switch, SetSessionRelay) are skipped until it is turned on.
func Attach(ctx context.Context, srv *server.Server, o Options) *Host {
	o.Backend = serverBackend{srv}
	h := New(o)
	srv.SetRelayHost(hostSwitch{ctx: ctx, h: h})
	srv.SetSessionObserver(func(id string) { h.AddSession(ctx, id) })
	for _, id := range srv.LiveSessionIDs() {
		h.AddSession(ctx, id)
	}
	go func() { _ = h.Run(ctx) }()
	return h
}
