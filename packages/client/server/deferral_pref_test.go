package server

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

func TestDeferBuiltinToolsPreference(t *testing.T) {
	ctx := context.Background()
	docs := &fakeDocs{}
	s := &Server{Preferences: docs}
	got, err := s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil || !got.Msg.GetPreferences().GetDeferBuiltinTools() {
		t.Fatalf("default on: %v %+v", err, got)
	}
	up, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{DeferBuiltinTools: proto.Bool(false)}))
	if err != nil || up.Msg.GetPreferences().GetDeferBuiltinTools() {
		t.Fatalf("update: %v %+v", err, up)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(docs.doc.GetContent()), &stored); err != nil || stored["deferBuiltinTools"] != false {
		t.Fatalf("stored: %v %s", err, docs.doc.GetContent())
	}

	// A policy that forces it: reported as the policy says, and the user's switch is refused.
	s.Base.ModePolicy = permissions.NewModePolicy()
	on := true
	s.Base.ModePolicy.SetManaged(permissions.ManagedPolicy{DeferBuiltinTools: &on})
	got, err = s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	p := got.Msg.GetPreferences()
	if err != nil || !p.GetDeferBuiltinTools() || p.GetManagedPolicy().DeferBuiltinTools == nil || !p.GetManagedPolicy().GetDeferBuiltinTools() || !p.GetManagedPolicy().GetActive() {
		t.Fatalf("forced: %v %+v", err, p)
	}
	_, err = s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{DeferBuiltinTools: proto.Bool(false)}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a forced choice must be refused: %v", err)
	}
}
