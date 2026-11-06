package server

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

func TestManagedPolicyInPreferences(t *testing.T) {
	s := &Server{}
	res, err := s.GetUserPreferences(context.Background(), connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetPreferences().GetManagedPolicy().GetActive() {
		t.Fatal("nothing imposed without a policy")
	}
	s.Base.ModePolicy = permissions.NewModePolicy()
	s.Base.ModePolicy.SetManaged(permissions.ManagedPolicy{DenyRules: []string{"Bash(rm:*)"}, SandboxRequired: true, RestrictMcpServers: true, AllowedMcpServers: []string{"github"}})
	res, err = s.GetUserPreferences(context.Background(), connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg.GetPreferences().GetManagedPolicy()
	if !m.GetActive() || !m.GetSandboxRequired() || m.GetDenyRules()[0] != "Bash(rm:*)" || !m.GetRestrictMcpServers() || m.GetAllowedMcpServers()[0] != "github" {
		t.Fatalf("view: %+v", m)
	}
}

func TestManagedPolicyRefusesAcpAgents(t *testing.T) {
	s := &Server{}
	s.Base.ModePolicy = permissions.NewModePolicy()
	s.Base.ModePolicy.SetManaged(permissions.ManagedPolicy{RestrictProviders: true, AllowedProviders: []string{"corp-gateway"}})
	_, err := s.openAcp(context.Background(), &agentv1.CreateSessionRequest{}, "some-agent", nil)
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("not refused: %v", err)
	}
}
