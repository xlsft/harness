package server_test

import (
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/server"
)

// refreshingHost is a relay host that counts re-registrations.
type refreshingHost struct{ refreshes atomic.Int32 }

func (h *refreshingHost) SetSessionRelay(string, bool) {}
func (h *refreshingHost) RefreshHost()                 { h.refreshes.Add(1) }

func TestRemoteStartsSwitch(t *testing.T) {
	var srv *server.Server
	mp := permissions.NewModePolicy()
	relay := &refreshingHost{}
	h := startHarnessSrv(t, fg.Sequence(), nil, nil, func(s *server.Server) {
		srv = s
		s.SetModePolicy(mp)
		s.SetRelayHost(relay)
	})
	got, err := h.client.GetDeviceSettings(h.ctx, connect.NewRequest(&agentv1.GetDeviceSettingsRequest{}))
	if err != nil || got.Msg.Settings.AllowRemoteStarts || srv.RemoteStartsAllowed() {
		t.Fatalf("remote starts must be off by default: %v %+v", err, got)
	}
	// Changing another setting does not touch the relay registration.
	if _, err := h.client.UpdateDeviceSettings(h.ctx, connect.NewRequest(&agentv1.UpdateDeviceSettingsRequest{Theme: proto.String("dark")})); err != nil || relay.refreshes.Load() != 0 {
		t.Fatalf("theme: %v, refreshes %d", err, relay.refreshes.Load())
	}
	up, err := h.client.UpdateDeviceSettings(h.ctx, connect.NewRequest(&agentv1.UpdateDeviceSettingsRequest{AllowRemoteStarts: proto.Bool(true)}))
	if err != nil || !up.Msg.Settings.AllowRemoteStarts || up.Msg.Settings.Theme != "dark" || !srv.RemoteStartsAllowed() {
		t.Fatalf("turn on: %v %+v", err, up)
	}
	if relay.refreshes.Load() != 1 {
		t.Fatalf("the relay host was not told: %d", relay.refreshes.Load())
	}
	// The organization's policy wins, and the host is told.
	mp.SetManaged(permissions.ManagedPolicy{RemoteStartsDisabled: true})
	if srv.RemoteStartsAllowed() || relay.refreshes.Load() != 2 {
		t.Fatalf("policy off: allowed=%v refreshes=%d", srv.RemoteStartsAllowed(), relay.refreshes.Load())
	}
	mp.SetManaged(permissions.ManagedPolicy{})
	if !srv.RemoteStartsAllowed() || relay.refreshes.Load() != 3 {
		t.Fatalf("policy back on: allowed=%v refreshes=%d", srv.RemoteStartsAllowed(), relay.refreshes.Load())
	}
}

func TestChatDefaultsAreSyncedPreferences(t *testing.T) {
	docs := &countingDocs{content: `{"other": 1}`}
	h := startHarnessSrv(t, fg.Sequence(), nil, nil, func(s *server.Server) { s.Preferences = docs })
	update := func(d *agentv1.ChatDefaults) (*connect.Response[agentv1.UpdateUserPreferencesResponse], error) {
		return h.client.UpdateUserPreferences(h.ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{ChatDefaults: d}))
	}
	if _, err := update(&agentv1.ChatDefaults{PermissionMode: "bypassPermissions"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bypass as a chat default: %v", err)
	}
	if _, err := update(&agentv1.ChatDefaults{Folder: "relative/dir"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("relative folder: %v", err)
	}
	if _, err := update(&agentv1.ChatDefaults{DeviceId: "dev_1", Folder: "/work/proj", PermissionMode: "acceptEdits"}); err != nil {
		t.Fatal(err)
	}
	got, err := h.client.GetUserPreferences(h.ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if d := got.Msg.GetPreferences().GetChatDefaults(); d.GetDeviceId() != "dev_1" || d.GetFolder() != "/work/proj" || d.GetPermissionMode() != "acceptEdits" {
		t.Fatalf("chat defaults: %+v (doc %s)", d, docs.content)
	}
}
