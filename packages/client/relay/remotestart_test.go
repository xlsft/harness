package relay

import (
	"context"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
)

// recentFolders is what the fake backend lists as this machine's recent folders.
var recentFolders = []string{"/work/proj", "/work/other"}

func (b *fakeBackend) ListRecentFolders(_ context.Context, req *connect.Request[agentv1.ListRecentFoldersRequest]) (*connect.Response[agentv1.ListRecentFoldersResponse], error) {
	out := &agentv1.ListRecentFoldersResponse{}
	for _, p := range recentFolders {
		out.Folders = append(out.Folders, &agentv1.RecentFolder{Path: p, Name: p, Exists: true})
	}
	if n := int(req.Msg.GetLimit()); n > 0 && n < len(out.Folders) {
		out.Folders = out.Folders[:n]
	}
	return connect.NewResponse(out), nil
}

func spawnCmd(id, cwd string) *relayv1.Command {
	return &relayv1.Command{CommandId: id, ExpiresAt: future(),
		Payload: &relayv1.Command_SpawnSession{SpawnSession: &relayv1.SpawnSessionCommand{Create: &agentv1.CreateSessionRequest{Cwd: cwd}}}}
}

func TestRemoteStartsFollowTheSwitchAndRecentFolders(t *testing.T) {
	var on atomic.Bool
	r := newRig(t, func(o *Options) { o.RemoteStarts = on.Load })
	r.fake.waitFor(t, "stream", func() bool { return r.fake.streams >= 1 })
	r.fake.mu.Lock()
	if r.fake.lastReg.GetRemoteStarts() {
		t.Error("registered with remote starts while the switch is off")
	}
	r.fake.mu.Unlock()

	// Off: no folders, no spawns.
	r.send(&relayv1.Command{CommandId: "f_off", ExpiresAt: future(), Payload: &relayv1.Command_ListRecentFolders{ListRecentFolders: &agentv1.ListRecentFoldersRequest{}}})
	r.send(spawnCmd("sp_off", "/work/proj"))
	r.fake.waitFor(t, "acks while off", func() bool { return r.fake.ackFor("f_off") != nil && r.fake.ackFor("sp_off") != nil })
	if r.fake.ackFor("f_off").GetOutcome() != relayv1.CommandAck_OUTCOME_REJECTED || r.fake.ackFor("sp_off").GetOutcome() != relayv1.CommandAck_OUTCOME_REJECTED {
		t.Fatalf("off: %v / %v", r.fake.ackFor("f_off"), r.fake.ackFor("sp_off"))
	}

	// Turned on: the host registers again with remote_starts.
	on.Store(true)
	r.host.RefreshHost()
	r.fake.waitFor(t, "re-registration", func() bool { return r.fake.lastReg.GetRemoteStarts() })

	r.send(&relayv1.Command{CommandId: "f_on", ExpiresAt: future(), Payload: &relayv1.Command_ListRecentFolders{ListRecentFolders: &agentv1.ListRecentFoldersRequest{Limit: 1}}})
	r.send(spawnCmd("sp_elsewhere", "/etc"))
	r.send(spawnCmd("sp_sub", "/work/proj/sub"))
	r.send(spawnCmd("sp_ok", "/work/proj/"))
	r.fake.waitFor(t, "acks while on", func() bool {
		return r.fake.ackFor("f_on") != nil && r.fake.ackFor("sp_elsewhere") != nil && r.fake.ackFor("sp_sub") != nil && r.fake.ackFor("sp_ok") != nil
	})
	if f := r.fake.ackFor("f_on").GetListRecentFolders().GetFolders(); len(f) != 1 || f[0].GetPath() != "/work/proj" {
		t.Fatalf("folders: %v", r.fake.ackFor("f_on"))
	}
	for _, id := range []string{"sp_elsewhere", "sp_sub"} {
		if a := r.fake.ackFor(id); a.GetOutcome() != relayv1.CommandAck_OUTCOME_REJECTED {
			t.Errorf("%s: %v", id, a)
		}
	}
	if a := r.fake.ackFor("sp_ok"); a.GetOutcome() != relayv1.CommandAck_OUTCOME_OK || a.GetSpawnSession().GetSession().GetCwd() != "/work/proj/" {
		t.Fatalf("spawn in a recent folder: %v", a)
	}
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if len(r.backend.created) != 1 {
		t.Fatalf("created %d sessions, want 1", len(r.backend.created))
	}
}

func TestRemoteStartsRefuseBypass(t *testing.T) {
	r := newRig(t, func(o *Options) { o.RemoteStarts = func() bool { return true } })
	r.fake.waitFor(t, "stream", func() bool { return r.fake.streams >= 1 })
	c := spawnCmd("sp_bypass", "/work/proj")
	c.GetSpawnSession().Create.PermissionMode = agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS
	r.send(c)
	r.fake.waitFor(t, "ack", func() bool { return r.fake.ackFor("sp_bypass") != nil })
	if a := r.fake.ackFor("sp_bypass"); a.GetOutcome() != relayv1.CommandAck_OUTCOME_REJECTED {
		t.Fatalf("bypass spawn: %v", a)
	}
}
