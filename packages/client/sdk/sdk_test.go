package sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/sdk"
)

func TestHeadlessCompactAndPlanMode(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	srv, url := fg.Start(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" {
			return fg.Reply{Events: fg.Message("s", fg.Text("SUMMARY"))}
		}
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("w", "Write", map[string]any{"file_path": filepath.Join(fg.Cwd(r), "x.txt"), "content": "x"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	tok := auth.StaticToken("hat_test")
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	base := bootstrap.Options{Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok), PermissionMode: permissions.ModePlan, SessionID: "11111111-1111-4111-8111-111111111111"}
	var out, errb bytes.Buffer
	if code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "plan", OutputFormat: "json", Stdout: &out, Stderr: &errb}); code != 0 {
		t.Fatalf("plan run: %d %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(proj, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("plan mode must not write files")
	}
	if !strings.Contains(out.String(), `"permission_denials":[{"tool_name":"Write"`) {
		t.Fatalf("denials: %s", out.String())
	}
	out.Reset()
	resume := base
	resume.SessionID, resume.Resume = "", base.SessionID
	if code := sdk.Run(context.Background(), sdk.Options{Bootstrap: resume, Prompt: "/compact keep the plan", OutputFormat: "text", Stdout: &out, Stderr: &errb}); code != 0 || !strings.Contains(out.String(), "Compacted the conversation") {
		t.Fatalf("compact: %d %q %q", code, out.String(), errb.String())
	}
	reqs := srv.Requests()
	last := reqs[len(reqs)-1]
	if last.Metadata.RequestClass != "compaction" {
		t.Fatalf("last request class %q", last.Metadata.RequestClass)
	}
}

func TestHeadlessOutputFormats(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	_, url := fg.Start(t, nil)
	tok := auth.StaticToken("hat_test")
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	base := bootstrap.Options{Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok)}

	var out, errb bytes.Buffer
	if code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "hi", OutputFormat: "text", Stdout: &out, Stderr: &errb}); code != 0 || out.String() != "ok\n" {
		t.Fatalf("text: code=%d out=%q err=%q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "hi", OutputFormat: "json", Stdout: &out, Stderr: &errb}); code != 0 {
		t.Fatalf("json: code=%d err=%q", code, errb.String())
	}
	var res map[string]any
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res["type"] != "result" || res["subtype"] != "success" || res["result"] != "ok" || res["session_id"] == "" {
		t.Fatalf("json result %v %s", err, out.String())
	}
	out.Reset()
	sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "hi", OutputFormat: "stream-json", IncludePartial: true, Stdout: &out, Stderr: &errb})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.Contains(lines[0], `"subtype":"init"`) || !strings.Contains(out.String(), `"type":"stream_event"`) || !strings.Contains(lines[len(lines)-1], `"type":"result"`) {
		t.Fatalf("stream-json:\n%s", out.String())
	}
}
