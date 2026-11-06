package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/agent"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/mcp"
)

// fakeDocs keeps one user settings document with SyncService's version check.
type fakeDocs struct {
	doc *syncv1.Document
	// raceWith is written over the document before the next Put (another writer), once.
	raceWith string
	puts     int
}

func (f *fakeDocs) GetDocument(_ context.Context, req *syncv1.GetDocumentRequest) (*syncv1.GetDocumentResponse, error) {
	if f.doc == nil {
		return nil, &llm.APIError{Status: http.StatusNotFound, Message: "not found"}
	}
	return &syncv1.GetDocumentResponse{Document: proto.Clone(f.doc).(*syncv1.Document)}, nil
}

func (f *fakeDocs) PutDocument(_ context.Context, req *syncv1.PutDocumentRequest) (*syncv1.PutDocumentResponse, error) {
	f.puts++
	if f.raceWith != "" {
		f.write(f.raceWith)
		f.raceWith = ""
	}
	if req.GetBaseVersion() != f.doc.GetVersion() {
		return &syncv1.PutDocumentResponse{Result: &syncv1.PutDocumentResponse_Conflict{Conflict: &syncv1.Conflict{
			Current: &syncv1.Conflict_Document{Document: proto.Clone(f.doc).(*syncv1.Document)}}}}, nil
	}
	f.write(req.GetContent())
	return &syncv1.PutDocumentResponse{Result: &syncv1.PutDocumentResponse_Document{Document: proto.Clone(f.doc).(*syncv1.Document)}}, nil
}

func (f *fakeDocs) write(content string) {
	f.doc = &syncv1.Document{Id: "doc_1", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, Key: "settings",
		Version: f.doc.GetVersion() + 1, Content: content}
}

func TestUserPreferences(t *testing.T) {
	ctx := context.Background()
	s := &Server{}
	// Without sync: defaults, and updates are refused.
	got, err := s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil || !got.Msg.Preferences.CompactIdleSessions {
		t.Fatalf("defaults %v %+v", err, got)
	}
	if _, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{CompactIdleSessions: proto.Bool(false)})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("update without sync: %v", err)
	}

	docs := &fakeDocs{}
	s.Preferences = docs
	if got, err := s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{})); err != nil || !got.Msg.Preferences.CompactIdleSessions {
		t.Fatalf("no document: %v %+v", err, got)
	}
	// The first update creates the document.
	up, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{CompactIdleSessions: proto.Bool(false)}))
	if err != nil || up.Msg.Preferences.CompactIdleSessions || docs.doc.GetContent() == "" {
		t.Fatalf("create: %v %+v %v", err, up, docs.doc)
	}
	// Another writer changes the document in between: the update merges over its version and keeps its keys.
	docs.raceWith = `{"model":"big","compactIdleSessions":false,"permissions":{"allow":["Read"]}}`
	up, err = s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{CompactIdleSessions: proto.Bool(true)}))
	if err != nil || !up.Msg.Preferences.CompactIdleSessions {
		t.Fatalf("update: %v %+v", err, up)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(docs.doc.GetContent()), &stored); err != nil {
		t.Fatal(err)
	}
	if stored["model"] != "big" || stored["compactIdleSessions"] != true || stored["permissions"] == nil {
		t.Fatalf("stored %v", stored)
	}
	got, err = s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil || !got.Msg.Preferences.CompactIdleSessions {
		t.Fatalf("read back %v %+v", err, got)
	}
	// An empty update writes nothing.
	puts := docs.puts
	if _, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{})); err != nil || docs.puts != puts {
		t.Fatalf("empty update %v puts=%d", err, docs.puts-puts)
	}
	// A document that is not a JSON object is not overwritten.
	docs.write(`["x"]`)
	if _, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{CompactIdleSessions: proto.Bool(true)})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("invalid document: %v", err)
	}
}

func TestContextUsageToProto(t *testing.T) {
	u := agent.ContextUsage{Model: "m", Window: 10_000, SystemPrompt: 1000, SystemTools: 500, MCPTools: 100, Messages: 2400, Threshold: 9000}
	search := llm.ToolDef{Name: "mcp__docs__search", Description: "Search the docs", InputSchema: json.RawMessage(`{}`)}
	fetch := llm.ToolDef{Name: "mcp__docs__fetch", Description: "Fetch a page", InputSchema: json.RawMessage(`{"type":"object"}`)}
	p := contextUsageToProto(u, []mcp.ToolInfo{{Server: "docs", Tool: "search", Def: search, Offered: true}, {Server: "docs", Tool: "fetch", Def: fetch}})
	want := []struct {
		c agentv1.ContextUsage_Category
		n int64
	}{
		{agentv1.ContextUsage_CATEGORY_MESSAGES, 2400},
		{agentv1.ContextUsage_CATEGORY_MCP_TOOLS, 100},
		{agentv1.ContextUsage_CATEGORY_SYSTEM_TOOLS, 500},
		{agentv1.ContextUsage_CATEGORY_SYSTEM_PROMPT, 1000},
		{agentv1.ContextUsage_CATEGORY_MCP_SERVER_INSTRUCTIONS, 0},
		{agentv1.ContextUsage_CATEGORY_AUTOCOMPACT_BUFFER, 1000},
		{agentv1.ContextUsage_CATEGORY_FREE_SPACE, 10_000 - 4000 - 1000},
	}
	if len(p.Categories) != len(want) {
		t.Fatalf("categories %v", p.Categories)
	}
	sum := int64(0)
	for i, w := range want {
		if c := p.Categories[i]; c.Category != w.c || c.Tokens != w.n {
			t.Fatalf("category %d = %v, want %v %d", i, c, w.c, w.n)
		}
		sum += p.Categories[i].Tokens
	}
	if sum != p.ContextWindow || p.UsedTokens != 4000 || p.AutocompactThreshold != 9000 || p.TokensUntilAutocompact != 5000 || p.Model != "m" {
		t.Fatalf("totals sum=%d %+v", sum, p)
	}
	if len(p.McpTools) != 2 || p.McpTools[0].Deferred || !p.McpTools[1].Deferred || p.McpTools[1].Server != "docs" || p.McpTools[1].Tool != "fetch" ||
		p.McpTools[0].Tokens != int64(agent.EstimateToolTokens(search)) || p.DeferredMcpToolsTokens != int64(agent.EstimateToolTokens(fetch)) || p.DeferredSystemToolsTokens != 0 {
		t.Fatalf("mcp tools %+v deferred=%d", p.McpTools, p.DeferredMcpToolsTokens)
	}
}

func TestPRMonitorWakesPreference(t *testing.T) {
	ctx := context.Background()
	docs := &fakeDocs{}
	s := &Server{Preferences: docs}
	got, err := s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil || got.Msg.Preferences.PrMonitorWakesPerHour != 30 {
		t.Fatalf("default: %v %+v", err, got)
	}
	for _, bad := range []int32{0, -1, 241} {
		_, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{PrMonitorWakesPerHour: proto.Int32(bad)}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%d: %v", bad, err)
		}
	}
	if docs.puts != 0 {
		t.Fatal("an invalid value was written")
	}
	up, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{PrMonitorWakesPerHour: proto.Int32(60)}))
	if err != nil || up.Msg.Preferences.PrMonitorWakesPerHour != 60 || !up.Msg.Preferences.CompactIdleSessions {
		t.Fatalf("update: %v %+v", err, up)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(docs.doc.GetContent()), &stored); err != nil || stored["prMonitorWakesPerHour"] != float64(60) {
		t.Fatalf("stored: %v %s", err, docs.doc.GetContent())
	}
	// A bad value in the document (edited by hand) reads as the default.
	docs.write(`{"prMonitorWakesPerHour": 100000}`)
	got, err = s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil || got.Msg.Preferences.PrMonitorWakesPerHour != 30 {
		t.Fatalf("bad document: %v %+v", err, got)
	}
}

func TestLSPPreferences(t *testing.T) {
	ctx := context.Background()
	docs := &fakeDocs{}
	s := &Server{Preferences: docs}
	got, err := s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if p := got.Msg.GetPreferences(); err != nil || !p.LspEnabled || !p.LspDiagnostics || p.LspDiagnosticsStartServer {
		t.Fatalf("defaults: %v %+v", err, got)
	}
	up, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{LspEnabled: proto.Bool(false), LspDiagnosticsStartServer: proto.Bool(true)}))
	if p := up.Msg.GetPreferences(); err != nil || p.LspEnabled || !p.LspDiagnostics || !p.LspDiagnosticsStartServer {
		t.Fatalf("update: %v %+v", err, up)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(docs.doc.GetContent()), &stored); err != nil || stored["lspEnabled"] != false || stored["lspDiagnosticsStartServer"] != true {
		t.Fatalf("stored: %v %s", err, docs.doc.GetContent())
	}
	up, err = s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{LspDiagnostics: proto.Bool(false), LspEnabled: proto.Bool(true)}))
	if p := up.Msg.GetPreferences(); err != nil || !p.LspEnabled || p.LspDiagnostics {
		t.Fatalf("second update: %v %+v", err, up)
	}
}
