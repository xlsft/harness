package sdk_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/sdk"
)

func TestHeadlessPromptRunsASkill(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	var body atomic.Bool
	_, url := fg.Start(t, func(_ int, r *llm.Request) fg.Reply {
		for _, m := range r.Messages {
			for _, b := range m.Content {
				if strings.Contains(b.Text, "Ship prod now.") {
					body.Store(true)
				}
			}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	tok := auth.StaticToken("hat_test")
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	md := filepath.Join(proj, ".harness/skills/ship/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(md), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(md, []byte("---\ndescription: ship\n---\nShip $1 now."), 0o644); err != nil {
		t.Fatal(err)
	}
	yes := true
	base := bootstrap.Options{Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok), SkillHome: t.TempDir(), Trusted: &yes, NoPersistence: true}
	var out, errb bytes.Buffer
	if code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "/ship prod", OutputFormat: "text", Stdout: &out, Stderr: &errb}); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	if !body.Load() {
		t.Fatal("the skill's text didn't reach the model")
	}
	// Nothing to do ends the run with why, without a request.
	if code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "/code-review", OutputFormat: "text", Stdout: &out, Stderr: &errb}); code == 0 || !strings.Contains(errb.String(), "git repository") {
		t.Fatalf("%d %q", code, errb.String())
	}
}
