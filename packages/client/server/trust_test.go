package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

func TestWorkspaceTrustRPCs(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(repo, ".harness"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, ".harness", "settings.json"), []byte(`{"hooks":{"Stop":[]},"permissions":{"allow":["Bash(make *)"]}}`), 0o644)
	_ = os.WriteFile(filepath.Join(repo, ".mcp.json"), []byte(`{}`), 0o644)
	sub := filepath.Join(repo, "pkg")
	_ = os.MkdirAll(sub, 0o755)

	s := &Server{}
	ctx := context.Background()
	get := func(p string) *agentv1.GetWorkspaceTrustResponse {
		r, err := s.GetWorkspaceTrust(ctx, connect.NewRequest(&agentv1.GetWorkspaceTrustRequest{Path: p}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	if r := get(sub); r.Trusted || !r.HasProjectHooks || !r.HasProjectMcpServers || !r.HasCommittedAllowRules {
		t.Fatalf("untrusted state: %+v", r)
	}
	if _, err := s.SetWorkspaceTrust(ctx, connect.NewRequest(&agentv1.SetWorkspaceTrustRequest{Path: repo, Trusted: true})); err != nil {
		t.Fatal(err)
	}
	if r := get(sub); !r.Trusted || r.TrustedRoot == "" {
		t.Fatalf("child should inherit trust: %+v", r)
	}
	if _, err := s.SetWorkspaceTrust(ctx, connect.NewRequest(&agentv1.SetWorkspaceTrustRequest{Path: repo, Trusted: false})); err != nil {
		t.Fatal(err)
	}
	if r := get(sub); r.Trusted {
		t.Fatalf("trust should be removed: %+v", r)
	}
	if _, err := s.GetWorkspaceTrust(ctx, connect.NewRequest(&agentv1.GetWorkspaceTrustRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty path: %v", err)
	}
}
