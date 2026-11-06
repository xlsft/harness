package sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/sdk"
)

func headlessBase(t *testing.T, script func(int, *llm.Request) fg.Reply) (*fg.Server, bootstrap.Options) {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	srv, url := fg.Start(t, script)
	tok := auth.StaticToken("hat_test")
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	return srv, bootstrap.Options{Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok)}
}

func lastJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("last line %q: %v", lines[len(lines)-1], err)
	}
	return m
}

func requestText(r *llm.Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		for _, c := range m.Content {
			b.WriteString(c.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

const findingsSchema = `{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`

func TestJSONSchemaReturnsStructuredOutputAndRetriesOnce(t *testing.T) {
	srv, base := headlessBase(t, func(n int, _ *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.Text("sure, here it is"))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("```json\n{\"n\":3}\n```"))}
	})
	var out, errb bytes.Buffer
	code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "count", OutputFormat: "json", JSONSchema: json.RawMessage(findingsSchema), Stdout: &out, Stderr: &errb})
	if code != 0 {
		t.Fatalf("code %d %s %s", code, out.String(), errb.String())
	}
	res := lastJSON(t, out.String())
	got, ok := res["structured_output"].(map[string]any)
	if !ok || got["n"] != float64(3) {
		t.Fatalf("structured_output: %v", res)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	if !strings.Contains(requestText(reqs[0]), `"required":["n"]`) {
		t.Fatalf("the schema was not in the prompt: %s", requestText(reqs[0]))
	}
}

func TestJSONSchemaFailsAfterTheRetry(t *testing.T) {
	_, base := headlessBase(t, func(int, *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.Text(`{"n":"three"}`))}
	})
	var out, errb bytes.Buffer
	code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "count", OutputFormat: "json", JSONSchema: json.RawMessage(findingsSchema), Stdout: &out, Stderr: &errb})
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	res := lastJSON(t, out.String())
	if res["subtype"] != "error_max_structured_output_retries" || res["is_error"] != true {
		t.Fatalf("result %v", res)
	}
}

func TestJSONSchemaRejectsABadSchema(t *testing.T) {
	_, base := headlessBase(t, nil)
	var out, errb bytes.Buffer
	if code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "x", OutputFormat: "text", JSONSchema: json.RawMessage(`{"type":`), Stdout: &out, Stderr: &errb}); code != 1 || !strings.Contains(errb.String(), "--json-schema") {
		t.Fatalf("code %d err %q", code, errb.String())
	}
}

func TestSystemPromptFlags(t *testing.T) {
	srv, base := headlessBase(t, nil)
	base.SystemPrompt = "You only speak in haiku."
	base.AppendSystemPrompt = "Never mention frogs."
	var out, errb bytes.Buffer
	if code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "hi", OutputFormat: "text", Stdout: &out, Stderr: &errb}); code != 0 {
		t.Fatalf("code %d %s", code, errb.String())
	}
	sys := srv.Requests()[0].System
	if len(sys) != 2 || sys[0].Text != "You only speak in haiku." || sys[1].Text != "Never mention frogs." {
		t.Fatalf("system: %+v", sys)
	}
}

func TestAppendSystemPromptKeepsTheBundle(t *testing.T) {
	srv, base := headlessBase(t, nil)
	base.AppendSystemPrompt = "Never mention frogs."
	var out, errb bytes.Buffer
	if code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "hi", OutputFormat: "text", Stdout: &out, Stderr: &errb}); code != 0 {
		t.Fatalf("code %d %s", code, errb.String())
	}
	sys := srv.Requests()[0].System
	if len(sys) < 2 || sys[len(sys)-1].Text != "Never mention frogs." {
		t.Fatalf("system: %+v", sys)
	}
}

func TestStreamInputRunsMessagesInOrderWithOneResultEach(t *testing.T) {
	srv, base := headlessBase(t, func(n int, _ *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.Text("answer"))}
	})
	in := `{"type":"user","uuid":"c1","message":{"role":"user","content":"first"}}
not json
{"type":"user","uuid":"c2","message":{"role":"user","content":[{"type":"text","text":"second"}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_use","id":"x"}]}}
`
	var out, errb bytes.Buffer
	code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, InputFormat: "stream-json", OutputFormat: "stream-json", Stdin: strings.NewReader(in), Stdout: &out, Stderr: &errb})
	if code != 0 {
		t.Fatalf("code %d %s", code, errb.String())
	}
	var results, inputErrors int
	var clientIDs []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("output line %q: %v", line, err)
		}
		switch {
		case m["type"] == "result":
			results++
		case m["subtype"] == "input_error":
			inputErrors++
		case m["type"] == "user":
			if ids, ok := m["client_uuids"].([]any); ok {
				clientIDs = append(clientIDs, ids[0].(string))
			}
		}
	}
	if results != 2 || inputErrors != 2 {
		t.Fatalf("results %d, input errors %d:\n%s", results, inputErrors, out.String())
	}
	if strings.Join(clientIDs, ",") != "c1,c2" {
		t.Fatalf("client ids %v", clientIDs)
	}
	reqs := srv.Requests()
	if text := requestText(reqs[len(reqs)-1]); !strings.Contains(text, "first") || !strings.Contains(text, "second") {
		t.Fatalf("the session did not carry on: %s", text)
	}
}

func TestStreamInputWithoutMessagesFails(t *testing.T) {
	_, base := headlessBase(t, nil)
	var out, errb bytes.Buffer
	code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, InputFormat: "stream-json", OutputFormat: "stream-json", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb})
	if code != 1 || lastJSON(t, out.String())["is_error"] != true {
		t.Fatalf("code %d out %s", code, out.String())
	}
}

func TestStreamInputNeedsStreamOutput(t *testing.T) {
	_, base := headlessBase(t, nil)
	var out, errb bytes.Buffer
	code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, InputFormat: "stream-json", OutputFormat: "text", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb})
	if code != 1 || !strings.Contains(errb.String(), "requires --output-format stream-json") {
		t.Fatalf("code %d err %q", code, errb.String())
	}
}

// syncBuffer lets the test read the output while the run writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestStreamInputInterruptStopsTheTurnAndKeepsTheSession(t *testing.T) {
	var once sync.Once
	started := make(chan struct{})
	_, base := headlessBase(t, func(n int, _ *llm.Request) fg.Reply {
		if n == 0 {
			// The first request hangs until the test is done; the interrupt has to end the turn.
			once.Do(func() { close(started) })
			return fg.Reply{Hold: true}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("second answer"))}
	})
	pr, pw := io.Pipe()
	defer pw.Close()
	out := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- sdk.Run(context.Background(), sdk.Options{Bootstrap: base, InputFormat: "stream-json", OutputFormat: "stream-json", Stdin: pr, Stdout: out, Stderr: io.Discard})
	}()
	write := func(s string) {
		if _, err := io.WriteString(pw, s+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"type":"user","message":{"role":"user","content":"long"}}`)
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the first request never came")
	}
	write(`{"type":"control","subtype":"interrupt"}`)
	waitFor(t, func() bool { return strings.Contains(out.String(), `"interrupted":true`) })
	write(`{"type":"user","message":{"role":"user","content":"next"}}`)
	waitFor(t, func() bool {
		return strings.Contains(out.String(), "second answer") && strings.Count(out.String(), `"type":"result"`) == 2
	})
	pw.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code %d:\n%s", code, out.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the run did not end with its input")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBudgetStopsTheRunBeforeTheNextRequest(t *testing.T) {
	srv, base := headlessBase(t, func(n int, r *llm.Request) fg.Reply {
		return fg.Reply{
			Events: fg.Message("m", fg.ToolUse("t"+string(rune('a'+n)), "Read", map[string]any{"file_path": filepath.Join(fg.Cwd(r), "missing")})),
		}
	})
	base.MaxBudgetUSD = 0.0015 // the fake gateway charges 0.001 per response
	var out, errb bytes.Buffer
	code := sdk.Run(context.Background(), sdk.Options{Bootstrap: base, Prompt: "go", OutputFormat: "json", Stdout: &out, Stderr: &errb})
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	res := lastJSON(t, out.String())
	if res["subtype"] != "error_max_budget_usd" {
		t.Fatalf("result %v", res)
	}
	if n := len(srv.Requests()); n != 2 {
		t.Fatalf("%d requests, want the run to stop after the second", n)
	}
}
