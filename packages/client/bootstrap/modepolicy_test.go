package bootstrap

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

func TestWatchModePolicy(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_NO_SYNC", "")
	fs := synctest.New()
	srv := fs.Start()
	t.Cleanup(srv.Close)
	ref := &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_POLICY, Key: "default"}
	fs.Seed(ref, `{"permissions": {"disabledModes": ["bypassPermissions"]}}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := WatchModePolicy(ctx, srv.URL, srv.Client(), auth.StaticToken("hat_test"), &auth.User{ID: "usr_1"}, nil)
	if !slices.Equal(p.Disabled(), []permissions.Mode{permissions.ModeBypassPermissions}) {
		t.Fatalf("initial policy %v", p.Disabled())
	}

	// A live change of the document reaches the policy through the watch stream.
	changed := make(chan []permissions.Mode, 4)
	p.OnChange(func(d []permissions.Mode) { changed <- d })
	fs.Seed(ref, `{"permissions": {"disabledModes": ["auto", "dontAsk"]}}`)
	select {
	case d := <-changed:
		if !slices.Equal(d, []permissions.Mode{permissions.ModeAuto, permissions.ModeDontAsk}) {
			t.Fatalf("changed to %v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no live update")
	}
	cancel()

	// The last known policy applies when the server is unreachable at start.
	srv.Close()
	off := WatchModePolicy(context.Background(), srv.URL, srv.Client(), auth.StaticToken("hat_test"), &auth.User{ID: "usr_1"}, nil)
	if !slices.Equal(off.Disabled(), []permissions.Mode{permissions.ModeAuto, permissions.ModeDontAsk}) {
		t.Fatalf("offline policy %v", off.Disabled())
	}
}
