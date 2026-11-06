package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/mcp/v1/mcpv1connect"
)

func permissionLists(t *testing.T, docs *fakeDocs) (allow, ask, deny []string) {
	t.Helper()
	var d struct {
		Permissions struct{ Allow, Ask, Deny []string } `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(docs.doc.GetContent()), &d); err != nil {
		t.Fatal(err)
	}
	return d.Permissions.Allow, d.Permissions.Ask, d.Permissions.Deny
}

func TestSetMcpToolRuleWritesTheSyncedSettings(t *testing.T) {
	ctx := context.Background()
	s := &Server{}
	set := func(server, tool, rule string) error {
		_, err := s.SetMcpToolRule(ctx, connect.NewRequest(&agentv1.SetMcpToolRuleRequest{Server: server, Tool: tool, Rule: rule}))
		return err
	}
	if err := set("docs", "search", "allow"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("without sync: %v", err)
	}
	docs := &fakeDocs{}
	s.Preferences = docs
	if err := set("", "x", "allow"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no server: %v", err)
	}
	if err := set("docs", "x", "sometimes"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad rule: %v", err)
	}
	// Other keys and other rules stay, also when another writer gets in between.
	docs.write(`{"model":"big","permissions":{"allow":["Read"],"defaultMode":"plan"}}`)
	docs.raceWith = `{"model":"bigger","permissions":{"allow":["Read","Glob"],"defaultMode":"plan"}}`
	if err := set("docs", "search", "allow"); err != nil {
		t.Fatal(err)
	}
	allow, ask, deny := permissionLists(t, docs)
	if !slices.Equal(allow, []string{"Read", "Glob", "mcp__docs__search"}) || len(ask) != 0 || len(deny) != 0 {
		t.Fatalf("lists %v %v %v", allow, ask, deny)
	}
	if !strings.Contains(docs.doc.GetContent(), `"bigger"`) || !strings.Contains(docs.doc.GetContent(), `"defaultMode"`) {
		t.Fatalf("other keys lost: %s", docs.doc.GetContent())
	}
	// Moving a rule leaves one copy; the whole server has a rule of its own.
	if err := set("docs", "search", "deny"); err != nil {
		t.Fatal(err)
	}
	if err := set("docs", "", "ask"); err != nil {
		t.Fatal(err)
	}
	allow, ask, deny = permissionLists(t, docs)
	if !slices.Equal(allow, []string{"Read", "Glob"}) || !slices.Equal(ask, []string{"mcp__docs"}) || !slices.Equal(deny, []string{"mcp__docs__search"}) {
		t.Fatalf("lists %v %v %v", allow, ask, deny)
	}
	if err := set("docs", "search", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, deny = permissionLists(t, docs); len(deny) != 0 {
		t.Fatalf("not removed: %v", deny)
	}
	got, ask, _ := s.mcpSyncedRules(ctx)
	if !slices.Contains(got, "Read") || !slices.Equal(ask, []string{"mcp__docs"}) {
		t.Fatalf("synced rules %v %v", got, ask)
	}
}

func TestCoreProxyForwardsOnlyTheMcpCallsAFrontendNeeds(t *testing.T) {
	hits := 0
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer core.Close()
	s := New(bootstrap.Options{})
	defer s.Close()
	s.Core = &CoreProxy{URL: core.URL, HTTP: core.Client(), Tokens: &proxyTokens{token: "hat"}}
	mux := s.mux()
	post := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	for _, path := range []string{mcpv1connect.McpServiceStartOAuthProcedure, mcpv1connect.McpServiceGetOAuthFlowProcedure, mcpv1connect.McpServiceRevokeAuthProcedure, mcpv1connect.McpServiceListAuditProcedure} {
		if code := post(path); code != http.StatusOK {
			t.Fatalf("%s: %d", path, code)
		}
	}
	if hits != 4 {
		t.Fatalf("forwarded %d", hits)
	}
	for _, path := range []string{mcpv1connect.McpServiceIssueStdioLaunchProcedure, mcpv1connect.McpServiceCallToolProcedure, mcpv1connect.McpServiceRespondToElicitationProcedure, mcpv1connect.McpServiceCreateServerProcedure} {
		if code := post(path); code == http.StatusOK {
			t.Fatalf("%s reached the server", path)
		}
	}
	if hits != 4 {
		t.Fatalf("a blocked call was forwarded: %d", hits)
	}
}
