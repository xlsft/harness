package main

import "testing"

// serve shares one gateway and prompt source among sessions; without the credentials beside them the sessions opened no
// server connection, and the model never saw ArtifactPublish, KnowledgeSearch or the MCP tools.
func TestSharedAdaptersCarryTheServerConnection(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_ACCESS_TOKEN", "hat_test")
	t.Setenv("HARNESS_SERVER_URL", "http://127.0.0.1:1")
	base, _, err := sharedAdapters("")
	if err != nil {
		t.Fatal(err)
	}
	if base.Gateway == nil || base.Prompts == nil {
		t.Fatal("no shared adapters")
	}
	if base.Tokens == nil || base.ServerURL == "" || base.SyncHub == nil || base.MCP == nil {
		t.Fatalf("the sessions would have no server connection: tokens %v url %q hub %v mcp %v", base.Tokens, base.ServerURL, base.SyncHub, base.MCP)
	}
}
