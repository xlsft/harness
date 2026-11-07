package bootstrap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/toolsearch"
)

// realBundle builds the prompt bundle from packages/core/prompts the way the server does (system sections, tools,
// reminders), so token counts are the real ones.
func realBundle(t *testing.T) *prompts.Bundle {
	t.Helper()
	root := filepath.Join("..", "..", "core", "prompts")
	if _, err := os.Stat(root); err != nil {
		t.Skip("packages/core/prompts is not next to this module")
	}
	b := fg.DefaultBundle()
	b.Version = "real"
	b.System = nil
	for _, part := range []string{"static", "dynamic"} {
		files, _ := filepath.Glob(filepath.Join(root, "system", part, "*.md"))
		sort.Strings(files)
		for _, f := range files {
			text, _ := os.ReadFile(f)
			id := strings.TrimSuffix(filepath.Base(f), ".md")
			if i := strings.IndexByte(id, '-'); i > 0 {
				id = id[i+1:]
			}
			b.System = append(b.System, prompts.Section{ID: id, Text: strings.TrimRight(string(text), " \t\r\n")})
		}
	}
	b.Tools = map[string]prompts.ToolPrompt{}
	dirs, _ := os.ReadDir(filepath.Join(root, "tools"))
	for _, d := range dirs {
		desc, err := os.ReadFile(filepath.Join(root, "tools", d.Name(), "description.md"))
		if err != nil {
			continue
		}
		var schema bytes.Buffer
		if raw, err := os.ReadFile(filepath.Join(root, "tools", d.Name(), "schema.json")); err == nil {
			if err := json.Compact(&schema, raw); err != nil {
				t.Fatalf("%s: %v", d.Name(), err)
			}
		}
		b.Tools[d.Name()] = prompts.ToolPrompt{Description: strings.TrimRight(string(desc), " \t\r\n"), InputSchema: schema.Bytes()}
	}
	files, _ := filepath.Glob(filepath.Join(root, "reminders", "*.md"))
	for _, f := range files {
		text, _ := os.ReadFile(f)
		b.Reminders[strings.TrimSuffix(filepath.Base(f), ".md")] = strings.TrimRight(string(text), " \t\r\n")
	}
	return b
}

type deferralEnv struct {
	home, proj, url string
	srv             *fg.Server
	tok             auth.TokenSource
}

func newDeferralEnv(t *testing.T, settingsJSON string, respond fg.Responder) *deferralEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HARNESS_HOME", home)
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	t.Setenv("HARNESS_NO_MCP", "1")
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	if settingsJSON != "" {
		dir := filepath.Join(proj, ".harness")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settingsJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv, url := fg.Start(t, respond)
	srv.Bundle = realBundle(t)
	return &deferralEnv{home: home, proj: proj, url: url, srv: srv, tok: auth.StaticToken("hat_test")}
}

func (e *deferralEnv) open(t *testing.T, resume string) *bootstrap.Session {
	t.Helper()
	s, err := bootstrap.Open(context.Background(), bootstrap.Options{
		Cwd: e.proj, Gateway: connectgw.New(e.url, nil, e.tok), Prompts: prompts.NewConnectSource(e.url, nil, e.tok),
		PermissionMode: permissions.ModeBypassPermissions, Resume: resume,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func reqToolNames(r *llm.Request) []string {
	var out []string
	for _, d := range r.Tools {
		out = append(out, d.Name)
	}
	return out
}

// TestDeferralTokenSavings measures the built-in tool definitions of a new session with the real prompt bundle, with
// deferral off and on (the numbers go to the test log: go test ./bootstrap -run TestDeferralTokenSavings -v).
func TestDeferralTokenSavings(t *testing.T) {
	measure := func(settingsJSON string) (agentUsage, int) {
		e := newDeferralEnv(t, settingsJSON, func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} })
		s := e.open(t, "")
		defer s.Close()
		u := s.Agent.ContextUsage()
		listing := 0
		if v := s.Agent.DeferredReminder(); v != nil {
			listing = agent.EstimateTextTokens(len(v["tools"]))
		}
		return agentUsage{u.SystemTools, u.DeferredSystemTools, u.SystemPrompt, listing}, len(s.ToolNames())
	}
	off, offN := measure(`{"deferBuiltinTools": false}`)
	on, onN := measure("")
	t.Logf("deferral off: %d tools offered, system tools %d tokens, system prompt %d", offN, off.tools, off.prompt)
	t.Logf("deferral on:  %d tools offered, system tools %d tokens (%d held back), system prompt %d, deferred listing %d once", onN, on.tools, on.deferred, on.prompt, on.listing)
	if off.deferred != 0 || off.listing != 0 || on.tools*2 > off.tools || on.deferred == 0 {
		t.Fatalf("off %+v, on %+v", off, on)
	}
}

type agentUsage struct{ tools, deferred, prompt, listing int }

// TestDeferredBuiltinToolsEndToEnd: a deferred built-in tool is listed by name, loaded with ToolSearch and appended to
// the tool list; a direct call of another deferred tool loads it too; the loads survive a compaction and a resume.
func TestDeferredBuiltinToolsEndToEnd(t *testing.T) {
	e := newDeferralEnv(t, "", func(n int, req *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("tu1", toolsearch.Name, map[string]any{"query": "select:TaskInput"}))}
		case 1:
			// A direct call of a deferred tool, with input its schema refuses: it loads, and the error carries the schema.
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("tu2", "ListAgents", map[string]any{"bogus": 1}))}
		case 2:
			return fg.Reply{Events: fg.Message("m3", fg.Text("done"))}
		case 3:
			return fg.Reply{Events: fg.Message("m4", fg.Text("<summary>the summary</summary>"))}
		default:
			return fg.Reply{Events: fg.Message("m5", fg.Text("after"))}
		}
	})
	s := e.open(t, "")
	before := s.ToolNames()
	if slices.Contains(before, "TaskInput") || slices.Contains(before, "ListAgents") || !slices.Contains(before, toolsearch.Name) || !slices.Contains(before, "Read") {
		t.Fatalf("tools before loading = %v", before)
	}
	res := s.Agent.RunTurn(context.Background(), []llm.ContentBlock{llm.TextBlock("go")})
	if res.IsError || res.Result != "done" {
		t.Fatalf("turn = %+v", res)
	}
	reqs := e.srv.Requests()
	first, second, third := reqToolNames(reqs[0]), reqToolNames(reqs[1]), reqToolNames(reqs[2])
	if txt := requestText(reqs[0]); !strings.Contains(txt, "\nTaskInput: ") || !strings.Contains(txt, "ToolSearch") {
		t.Fatalf("deferred listing missing from the first request: %q", txt)
	}
	// Loading appends: the earlier list is a prefix of the next one.
	if !slices.Equal(second[:len(first)], first) || second[len(second)-1] != "TaskInput" {
		t.Fatalf("second request tools = %v", second)
	}
	if !slices.Equal(third[:len(second)], second) || third[len(third)-1] != "ListAgents" {
		t.Fatalf("third request tools = %v", third)
	}
	if txt := requestText(reqs[2]); !strings.Contains(txt, "InputValidationError") || !strings.Contains(txt, `"name":"ListAgents"`) {
		t.Fatalf("direct call result = %q", txt)
	}

	// A compaction keeps the loaded tools and lists the deferred ones again.
	if _, err := s.Agent.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	s.Agent.RunTurn(context.Background(), []llm.ContentBlock{llm.TextBlock("more")})
	reqs = e.srv.Requests()
	last := reqs[len(reqs)-1]
	if got := reqToolNames(last); !slices.Equal(got, third) {
		t.Fatalf("tools after compaction = %v, want %v", got, third)
	}
	if txt := requestText(last); !strings.Contains(txt, "\nTaskGraphAdd: ") || strings.Contains(txt, "\nTaskInput: ") {
		t.Fatalf("listing after compaction = %q", txt)
	}

	// Resuming loads them again, in the same order.
	id := s.ID
	s.Close()
	s2 := e.open(t, id)
	defer s2.Close()
	if got := s2.ToolNames(); !slices.Equal(got, third) {
		t.Fatalf("resumed tools = %v, want %v", got, third)
	}

	// /tools shows deferral as a state of its own.
	report := s2.ToolReport()
	for _, st := range report {
		if st.Name == "TaskGraphAdd" && (st.Offered || st.Reason != bootstrap.ToolReasonDeferred) {
			t.Fatalf("TaskGraphAdd: %+v", st)
		}
		if st.Name == "TaskInput" && !st.Offered {
			t.Fatalf("loaded TaskInput: %+v", st)
		}
	}
	if out := bootstrap.FormatToolReport(report); !strings.Contains(out, "Deferred, loaded when the model first needs them") || strings.Contains(out, "TaskGraphAdd: deferred") {
		t.Fatalf("/tools:\n%s", out)
	}
}

// TestDeferralPolicy: the managed policy's choice wins over the user's.
func TestDeferralOff(t *testing.T) {
	e := newDeferralEnv(t, `{"deferBuiltinTools": false}`, func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} })
	s := e.open(t, "")
	defer s.Close()
	names := s.ToolNames()
	if slices.Contains(names, toolsearch.Name) || !slices.Contains(names, "TaskInput") {
		t.Fatalf("deferral off: tools = %v", names)
	}
	if s.Agent.DeferredReminder() != nil {
		t.Fatal("deferral off: no listing")
	}
}
