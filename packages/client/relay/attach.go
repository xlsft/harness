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
	switch strings.ToLower(os.Getenv("HARNESS_REMOTE_CONTROL")) {
	case "0", "false", "off", "no":
		return false
	}
	return flag && serverURL != "" && signedIn
}

// Attach mirrors every session of srv (already open and opened later) through
// a relay host and starts it; it runs until ctx ends. o.Backend is set to srv.
// Sessions whose settings say "remoteControl": false are skipped.
func Attach(ctx context.Context, srv *server.Server, o Options) *Host {
	o.Backend = serverBackend{srv}
	h := New(o)
	srv.SetSessionObserver(func(id string) { h.AddSession(ctx, id) })
	for _, id := range srv.LiveSessionIDs() {
		h.AddSession(ctx, id)
	}
	go func() { _ = h.Run(ctx) }()
	return h
}
