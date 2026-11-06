package bootstrap_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/fakelsp"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/tools"
)

// lspSession opens a session with the fake language server on its environment. It runs on a test's fake home.
func lspSession(t *testing.T, proj string, trusted bool, respond func(n int, r *llm.Request) fg.Reply) (*bootstrap.Session, *fg.Server, string) {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	log := filepath.Join(t.TempDir(), "server.log")
	for _, kv := range fakelsp.Environ(fakelsp.Options{Log: log}) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	srv, url := fg.Start(t, respond)
	tok := auth.StaticToken("hat_test")
	s, err := bootstrap.Open(context.Background(), bootstrap.Options{
		Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok), Trusted: &trusted,
		PermissionMode: permissions.ModeBypassPermissions, NoMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, srv, log
}

func fakeServerSettings(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"lspServers": map[string]any{"fake": map[string]any{"command": exe, "extensions": []string{".fake"}}}})
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForLog(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q never logged:\n%s", want, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func symbolsCall(s *bootstrap.Session, path string) tools.Result {
	tool, _ := s.Tools.Get("LSP")
	input, _ := json.Marshal(map[string]any{"operation": "document_symbols", "file_path": path})
	return tool.Run(context.Background(), s.ToolEnv, tools.Call{Input: input})
}

// TestLSPInASession goes through the whole path: the setting picks the server, the tool starts it, an edit in a
// turn brings the server's error into the next model request, and moving the session stops the server.
func TestLSPInASession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	writeFile(t, filepath.Join(proj, ".harness", "settings.local.json"), fakeServerSettings(t))
	file := filepath.Join(proj, "a.fake")
	writeFile(t, file, "def a\n")
	s, srv, log := lspSession(t, proj, true, func(n int, _ *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t1", "LSP", map[string]any{"operation": "document_symbols", "file_path": file}))}
		case 1:
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("t2", "Read", map[string]any{"file_path": file}))}
		case 2:
			return fg.Reply{Events: fg.Message("m3", fg.ToolUse("t3", "Edit", map[string]any{"file_path": file, "old_string": "def a", "new_string": "ERROR:you broke it"}))}
		default:
			return fg.Reply{Events: fg.Message("m4", fg.Text("done"))}
		}
	})
	res := s.Agent.RunTurn(context.Background(), []llm.ContentBlock{llm.TextBlock("go")})
	if res.IsError || res.Result != "done" {
		t.Fatalf("turn = %+v", res)
	}
	reqs := srv.Requests()
	if text := requestText(reqs[1]); !strings.Contains(text, "function a") {
		t.Fatalf("tool result: %s", text)
	}
	if text := requestText(reqs[2]); strings.Contains(text, "lsp-diagnostics") {
		t.Fatalf("a reminder before the edit: %s", text)
	}
	if text := requestText(reqs[3]); !strings.Contains(text, "<lsp-diagnostics>\na.fake:1:1: you broke it [fake E1]\n</lsp-diagnostics>") {
		t.Fatalf("after the edit: %s", text)
	}

	other := t.TempDir()
	if err := s.ToolEnv.Mover().Move(context.Background(), tools.Move{Cwd: other}); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, log, "exit")
}

// TestLSPServerChoiceOfAProject: settings in a repository choose no binary until the folder is trusted, and in an
// untrusted folder no server starts at all.
func TestLSPServerChoiceOfAProject(t *testing.T) {
	for _, name := range []string{"settings.json", "settings.local.json"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			proj, _ := filepath.EvalSymlinks(t.TempDir())
			writeFile(t, filepath.Join(proj, ".harness", name), fakeServerSettings(t))
			file := filepath.Join(proj, "a.fake")
			writeFile(t, file, "def a\n")
			noTurns := func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("done"))} }

			s, _, log := lspSession(t, proj, false, noTurns)
			res := symbolsCall(s, file)
			if res.IsError == false || !strings.Contains(res.Content[0].Text, "No language server is known") {
				t.Fatalf("untrusted: %+v", res.Content)
			}
			if _, err := os.Stat(log); err == nil {
				t.Fatal("the repository's binary ran")
			}
			_ = s.Close()

			s, _, log = lspSession(t, proj, true, noTurns)
			if res := symbolsCall(s, file); res.IsError || !strings.Contains(res.Content[0].Text, "function a") {
				t.Fatalf("trusted: %+v", res.Content)
			}
			waitForLog(t, log, "start ")
		})
	}
}

// TestLSPServerInAnUntrustedFolder: no server starts over a folder the user did not trust.
func TestLSPServerInAnUntrustedFolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	file := filepath.Join(proj, "a.go")
	writeFile(t, file, "package a\n")
	s, _, log := lspSession(t, proj, false, func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("done"))} })
	res := symbolsCall(s, file)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "not a trusted workspace") {
		t.Fatalf("got %+v", res.Content)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("a server ran")
	}
}

// TestLSPTurnedOffByTheUser hides the tool from the model.
func TestLSPTurnedOffByTheUser(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	writeFile(t, filepath.Join(proj, ".harness", "settings.local.json"), `{"lspEnabled": false}`)
	s, _, _ := lspSession(t, proj, true, func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("done"))} })
	tool, ok := s.Tools.Get("LSP")
	if !ok {
		t.Fatal("not registered")
	}
	if !tool.(tools.ModelHider).HiddenFromModel() {
		t.Fatal("offered to the model")
	}
}
