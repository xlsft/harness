package bootstrap_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/workspace"
)

func allMessageText(r *llm.Request) string {
	raw, _ := json.Marshal(r.Messages)
	return string(raw)
}

// TestAutoMemoryAcrossSessions: the model saves a note in one session; the next session in the project starts with the
// note in its index, and the switch takes both the index and the tool away.
func TestAutoMemoryAcrossSessions(t *testing.T) {
	gittest.Isolate(t)
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")

	srv, url := fg.Start(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("tu1", "Memory", map[string]any{
				"command": "write", "name": "deploys", "type": "project", "description": "Deploys go through the staging gate", "content": "Staging first, since the 2026-01 outage.",
			}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("done"))}
	})
	srv.Bundle.Tools["Memory"] = prompts.ToolPrompt{Description: "Keep notes.", InputSchema: json.RawMessage(`{"type":"object"}`)}

	proj, _ := filepath.EvalSymlinks(t.TempDir())
	tok := auth.StaticToken("hat_test")
	open := func() *bootstrap.Session {
		t.Helper()
		s, err := bootstrap.Open(context.Background(), bootstrap.Options{Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok), NoSync: true})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	first := open()
	if !slices.Contains(first.ToolNames(), "Memory") {
		t.Fatalf("ToolNames = %v", first.ToolNames())
	}
	if res := first.Agent.RunTurn(context.Background(), []llm.ContentBlock{llm.TextBlock("deploys go through staging, remember that")}); res.IsError {
		t.Fatalf("turn = %+v", res)
	}
	first.Close()

	store := memory.AutoStore{Dir: memory.AutoMemoryDir(config.Home(), workspace.ProjectKey(context.Background(), proj))}
	if !strings.Contains(store.Index(), "- [deploys](deploys.md) - Deploys go through the staging gate") {
		t.Fatalf("index: %q", store.Index())
	}
	if data, err := os.ReadFile(filepath.Join(store.Dir, "deploys.md")); err != nil || !strings.Contains(string(data), "since the 2026-01 outage") {
		t.Fatalf("%q %v", data, err)
	}

	second := open()
	if res := second.Agent.RunTurn(context.Background(), []llm.ContentBlock{llm.TextBlock("hello")}); res.IsError {
		t.Fatalf("turn = %+v", res)
	}
	second.Close()
	reqs := srv.Requests()
	if text := allMessageText(reqs[len(reqs)-1]); !strings.Contains(text, "deploys.md") || !strings.Contains(text, "staging gate") {
		t.Fatalf("the next session does not know: %s", text)
	}

	// Off: no tool, nothing loaded. What is stored stays for when it comes back on.
	dir := filepath.Join(proj, ".harness")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"autoMemoryEnabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	third := open()
	defer third.Close()
	if res := third.Agent.RunTurn(context.Background(), []llm.ContentBlock{llm.TextBlock("hello again")}); res.IsError {
		t.Fatalf("turn = %+v", res)
	}
	reqs = srv.Requests()
	last := reqs[len(reqs)-1]
	if text := allMessageText(last); strings.Contains(text, "deploys.md") {
		t.Fatalf("loaded with the switch off: %s", text)
	}
	for _, tool := range last.Tools {
		if tool.Name == "Memory" {
			t.Fatal("the model was offered Memory with the switch off")
		}
	}
	if !strings.Contains(store.Index(), "deploys.md") {
		t.Fatal("turning it off must not delete anything")
	}
}
