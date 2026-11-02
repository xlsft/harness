package sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/sdk"
)

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
