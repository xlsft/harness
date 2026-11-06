package server_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

func TestReportCommands(t *testing.T) {
	rec := &recording{next: func(n int, r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}}
	h := startHarnessGW(t, rec.respond, nil, trusted())
	sid, ch := h.create(&agentv1.CreateSessionRequest{Model: "fake-model"})

	h.send(sid, "/usage")
	if r := h.result(ch); r.GetIsError() || !strings.Contains(r.GetText(), "model requests") {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/debug-config")
	if r := h.result(ch); r.GetIsError() || !strings.Contains(r.GetText(), "Sources, highest precedence first") {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/features")
	if r := h.result(ch); r.GetIsError() || !strings.Contains(r.GetText(), "sandbox") {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/insights 7")
	if r := h.result(ch); r.GetIsError() || !strings.Contains(r.GetText(), "Last 7 days") {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/insights soon")
	if r := h.result(ch); !r.GetIsError() {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/fewer-prompts")
	if r := h.result(ch); r.GetIsError() || !strings.Contains(r.GetText(), "no rule worth suggesting") {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/workflows")
	if r := h.result(ch); r.GetIsError() || !strings.Contains(r.GetText(), "No saved workflows") {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/workflows save nope")
	if r := h.result(ch); !r.GetIsError() {
		t.Fatalf("%+v", r)
	}
	if n := len(rec.all()); n != 0 {
		t.Fatalf("%d model requests from commands that report", n)
	}
}

// What past sessions were asked about comes back as rules, without what no suggestion should grant, and apply saves them.
func TestFewerPrompts(t *testing.T) {
	bash := func(i int, cmd string) fg.BlockSpec {
		return fg.ToolUse("u"+string(rune('a'+i)), "Bash", map[string]any{"command": cmd, "description": "d"})
	}
	cmds := []string{"make build", "make build", "make build", "curl https://example.com", "curl https://example.com", "curl https://example.com", "rm x", "rm x", "rm x"}
	rec := &recording{next: func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			specs := make([]fg.BlockSpec, 0, len(cmds))
			for i, c := range cmds {
				specs = append(specs, bash(i, c))
			}
			return fg.Reply{Events: fg.Message("m0", specs...)}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}}
	h := startHarnessGW(t, rec.respond, nil, trusted())
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{Model: "fake-model", Cwd: cwd})
	// Refused, so nothing runs: what counts is that it was asked.
	h.turn(sid, ch, "go", agentv1.RespondToPermissionRequest_DECISION_DENY)
	h.send(sid, "/fewer-prompts")
	r := h.result(ch)
	if r.GetIsError() || !strings.Contains(r.GetText(), "Bash(make build") {
		t.Fatalf("%+v", r)
	}
	for _, never := range []string{"curl", "Bash(rm"} {
		if strings.Contains(r.GetText(), never) {
			t.Fatalf("%q was suggested: %s", never, r.GetText())
		}
	}
	h.send(sid, "/fewer-prompts apply")
	r = h.result(ch)
	if r.GetIsError() || !strings.Contains(r.GetText(), "Added") {
		t.Fatalf("%+v", r)
	}
	data, err := os.ReadFile(filepath.Join(cwd, ".harness", "settings.local.json"))
	if err != nil || !strings.Contains(string(data), "make build") {
		t.Fatalf("%s %v", data, err)
	}
}

func TestPromptCommandsReachTheModel(t *testing.T) {
	rec := &recording{}
	h := startHarnessGW(t, rec.respond, nil, trusted())
	sid, ch := h.create(&agentv1.CreateSessionRequest{Model: "fake-model"})

	// A research command without a question says so, and costs no request.
	h.send(sid, "/deep-research")
	if r := h.result(ch); !r.GetIsError() || !strings.Contains(r.GetText(), "needs to know what about") {
		t.Fatalf("%+v", r)
	}
	if n := len(rec.all()); n != 0 {
		t.Fatalf("requests %d", n)
	}

	res, err := h.client.ListSlashCommands(t.Context(), connect.NewRequest(&agentv1.ListSlashCommandsRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range res.Msg.GetCommands() {
		names = append(names, c.GetName())
	}
	for _, want := range []string{"deep-research", "orchestrate", "interview", "recap", "usage", "insights", "fewer-prompts", "debug-config", "features", "workflows"} {
		if !slices.Contains(names, want) {
			t.Errorf("no /%s in %v", want, names)
		}
	}
}
