package server

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/sandbox"
)

func TestSandboxPreferences(t *testing.T) {
	ctx := context.Background()
	s := &Server{}
	got, err := s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil || !got.Msg.Preferences.GetSandbox().GetEnabled() || got.Msg.Preferences.GetSandbox().GetAutoAllowBash() {
		t.Fatalf("defaults: %v %+v", err, got)
	}
	docs := &fakeDocs{}
	s.Preferences = docs
	// The first write keeps what was not sent as the document's business, and the block whole.
	docs.write(`{"model":"big"}`)
	up, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{Sandbox: &agentv1.SandboxPreferences{
		Enabled: false, AllowedHosts: []string{"example.com", "*.corp.test:8443"}, AllowedWritePaths: []string{"~/extra", "/abs"}, AllowLocalhost: true}}))
	if err != nil {
		t.Fatal(err)
	}
	b := up.Msg.Preferences.GetSandbox()
	if b.GetEnabled() || !b.GetAllowLocalhost() || !slices.Equal(b.GetAllowedHosts(), []string{"example.com", "*.corp.test:8443"}) || !slices.Equal(b.GetAllowedWritePaths(), []string{"~/extra", "/abs"}) {
		t.Fatalf("update: %+v", b)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(docs.doc.GetContent()), &stored); err != nil {
		t.Fatal(err)
	}
	sb, _ := stored["sandbox"].(map[string]any)
	if stored["model"] != "big" || sb["enabled"] != false || sb["allowLocalhost"] != true {
		t.Fatalf("stored: %v", stored)
	}
	// Another preference leaves the block alone.
	up, err = s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{CompactIdleSessions: new(bool)}))
	if err != nil || up.Msg.Preferences.GetSandbox().GetEnabled() || !up.Msg.Preferences.GetSandbox().GetAllowLocalhost() {
		t.Fatalf("other: %v %+v", err, up)
	}
	// What cannot be applied is refused, naming it.
	for _, bad := range []*agentv1.SandboxPreferences{{AllowedHosts: []string{"not a host"}}, {AllowedWritePaths: []string{"relative/path"}}} {
		if _, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{Sandbox: bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}

func TestSandboxStatus(t *testing.T) {
	s := &Server{}
	res, err := s.GetSandboxStatus(context.Background(), connect.NewRequest(&agentv1.GetSandboxStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	info := sandbox.NewDriver().Info()
	if res.Msg.GetPlatform() != info.Platform || res.Msg.GetSupported() != info.Supported || res.Msg.GetRequiredByPolicy() || len(res.Msg.GetBuiltinHosts()) == 0 {
		t.Fatalf("status: %+v", res.Msg)
	}
	s.Base.ModePolicy = permissions.NewModePolicy()
	s.Base.ModePolicy.SetSandbox(true, true)
	res, err = s.GetSandboxStatus(context.Background(), connect.NewRequest(&agentv1.GetSandboxStatusRequest{}))
	if err != nil || !res.Msg.GetRequiredByPolicy() || !res.Msg.GetOutsideRefusedByPolicy() {
		t.Fatalf("policy: %v %+v", err, res.Msg)
	}
}
