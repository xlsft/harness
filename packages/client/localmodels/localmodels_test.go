package localmodels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/gateway"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/llm"
)

// ---- fakes ----

// fakeOllama is an httptest Ollama: two chat models and one embedding model, a scripted /api/chat.
type fakeOllama struct {
	mu       sync.Mutex
	chats    []map[string]any // decoded /api/chat bodies
	chatNDJ  []string         // lines answered by /api/chat
	chatCode int
	chatErr  string
	deleted  []string
	shows    int
}

func (f *fakeOllama) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"0.12.3"}`)
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[
			{"name":"qwen3:8b","model":"qwen3:8b","modified_at":"2026-10-01T10:00:00Z","size":5200000000,"details":{"family":"qwen3","parameter_size":"8.2B","quantization_level":"Q4_K_M"}},
			{"name":"gemma:2b","model":"gemma:2b","size":1700000000,"details":{"family":"gemma","parameter_size":"2B"}},
			{"name":"nomic-embed-text:latest","model":"nomic-embed-text:latest","size":270000000}
		]}`)
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.shows++
		f.mu.Unlock()
		var in struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch in.Model {
		case "qwen3:8b":
			fmt.Fprint(w, `{"model_info":{"general.architecture":"qwen3","qwen3.context_length":40960},"capabilities":["completion","tools","thinking"]}`)
		case "gemma:2b":
			fmt.Fprint(w, `{"model_info":{"general.architecture":"gemma","gemma.context_length":8192},"capabilities":["completion"]}`)
		default:
			fmt.Fprint(w, `{"model_info":{"general.architecture":"nomic-bert","nomic-bert.context_length":2048},"capabilities":["embedding"]}`)
		}
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.chats = append(f.chats, body)
		code, msg, lines := f.chatCode, f.chatErr, f.chatNDJ
		f.mu.Unlock()
		if code != 0 && body["tools"] != nil {
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"error":%q}`, msg)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, l := range lines {
			fmt.Fprintln(w, l)
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		for _, l := range []string{
			`{"status":"pulling manifest"}`,
			`{"status":"downloading","digest":"sha256:abc","total":100,"completed":40}`,
			`{"status":"downloading","digest":"sha256:abc","total":100,"completed":100}`,
			`{"status":"verifying sha256 digest"}`,
			`{"status":"success"}`,
		} {
			fmt.Fprintln(w, l)
		}
	})
	mux.HandleFunc("DELETE /api/delete", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Model == "missing:1b" {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"model 'missing:1b' not found"}`)
			return
		}
		f.mu.Lock()
		f.deleted = append(f.deleted, in.Model)
		f.mu.Unlock()
	})
	return mux
}

// fakeOpenAI is an httptest OpenAI-compatible server with a scripted SSE answer.
type fakeOpenAI struct {
	mu       sync.Mutex
	bodies   []map[string]any
	auth     []string
	sse      []string // data payloads
	noTools  bool     // refuse requests with tools (HTTP 400)
	modelsJS string
}

func (f *fakeOpenAI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		js := f.modelsJS
		f.mu.Unlock()
		if js == "" {
			js = `{"object":"list","data":[{"id":"llama-3.2-3b","owned_by":"me","max_model_len":131072},{"id":"phi-2","owned_by":"me"}]}`
		}
		fmt.Fprint(w, js)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		lines, noTools := f.sse, f.noTools
		f.mu.Unlock()
		if noTools && body["tools"] != nil {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"phi-2 does not support tools","type":"invalid_request_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	return mux
}

type memSink struct {
	mu   sync.Mutex
	recs []UsageRecord
	fail bool
}

func (s *memSink) Report(_ context.Context, recs []UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return fmt.Errorf("offline")
	}
	s.recs = append(s.recs, recs...)
	return nil
}

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := New(Options{Path: filepath.Join(t.TempDir(), "local-providers.json"), NoBuiltins: true, ProbeTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func addProvider(t *testing.T, r *Registry, kind Kind, url, key string) Provider {
	t.Helper()
	p, err := r.Add(context.Background(), AddInput{Kind: kind, BaseURL: url, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func collect(t *testing.T, gw gateway.Gateway, req *llm.Request) llm.Response {
	t.Helper()
	st, err := gw.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	resp, _, err := gateway.Collect(st, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return resp
}

var bashTool = llm.ToolDef{Name: "Bash", Description: "run a command", InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)}

// ---- tests ----

func TestParseModelID(t *testing.T) {
	cases := []struct {
		id, p, n string
		ok       bool
	}{
		{"local:ollama/qwen3:8b", "ollama", "qwen3:8b", true},
		{"local:lmstudio/mlx-community/Qwen3-8B-4bit", "lmstudio", "mlx-community/Qwen3-8B-4bit", true},
		{"local:ollama/", "", "", false},
		{"local:/x", "", "", false},
		{"claude-sonnet", "", "", false},
	}
	for _, c := range cases {
		p, n, ok := ParseModelID(c.id)
		if p != c.p || n != c.n || ok != c.ok {
			t.Errorf("ParseModelID(%q) = %q %q %v", c.id, p, n, ok)
		}
	}
	if ModelID("ollama", "qwen3:8b") != "local:ollama/qwen3:8b" {
		t.Error("ModelID")
	}
}

func TestOllamaDetectionAndModels(t *testing.T) {
	f := &fakeOllama{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	r := newTestRegistry(t)
	p := addProvider(t, r, KindOllama, srv.URL, "")
	if !p.Reachable || p.Version != "0.12.3" {
		t.Fatalf("provider = %+v", p)
	}
	if len(p.Models) != 2 {
		t.Fatalf("models = %+v (the embedding model must be left out)", p.Models)
	}
	gemma, qwen := p.Models[0], p.Models[1]
	if qwen.ID != "local:"+p.ID+"/qwen3:8b" || qwen.MaxContextWindow != 40960 || qwen.ContextWindow != DefaultContextLength {
		t.Errorf("qwen = %+v", qwen)
	}
	if qwen.Tools == nil || !*qwen.Tools || qwen.Thinking == nil || !*qwen.Thinking || qwen.Vision == nil || *qwen.Vision {
		t.Errorf("qwen capabilities = %v %v %v", qwen.Tools, qwen.Thinking, qwen.Vision)
	}
	if gemma.ContextWindow != 8192 || gemma.Tools == nil || *gemma.Tools {
		t.Errorf("gemma = %+v", gemma)
	}
	if qwen.ParameterSize != "8.2B" || qwen.Quantization != "Q4_K_M" || qwen.SizeBytes != 5200000000 {
		t.Errorf("details = %+v", qwen)
	}
	// /api/show answers are cached by modification time.
	shows := f.shows
	r.Refresh(context.Background(), p.ID)
	if f.shows != shows {
		t.Errorf("show called again: %d -> %d", shows, f.shows)
	}
}

func TestDetectionTimeout(t *testing.T) {
	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer slow.Close()
	defer close(block)
	r, _ := New(Options{NoBuiltins: true, ProbeTimeout: 100 * time.Millisecond})
	start := time.Now()
	p, err := r.Add(context.Background(), AddInput{Kind: KindLMStudio, BaseURL: slow.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("probe took %v", time.Since(start))
	}
	if p.Reachable || !strings.Contains(p.Error, "not running") {
		t.Fatalf("provider = %+v", p)
	}
	// A refused connection is reported as not running too.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	p2, _ := r.Add(context.Background(), AddInput{Kind: KindJan, BaseURL: url + "/v1"})
	if p2.Reachable || !strings.Contains(p2.Error, "not running") {
		t.Fatalf("closed port = %+v", p2)
	}
}

func TestBuiltinsShareLlamaCppPort(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /props", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"build_info":"b6000"}`) })
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"qwen.gguf","owned_by":"llamacpp","meta":{"n_ctx_train":32768}}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := &prober{http: srv.Client(), timeout: time.Second}
	ll := p.probe(context.Background(), ProviderConfig{ID: "llamacpp", Kind: KindLlamaCpp, BaseURL: srv.URL + "/v1"}, "")
	if !ll.reachable || ll.version != "b6000" || len(ll.models) != 1 || ll.models[0].MaxContextWindow != 32768 {
		t.Fatalf("llama.cpp = %+v", ll)
	}
	mlx := p.probe(context.Background(), ProviderConfig{ID: "mlx", Kind: KindMLX, BaseURL: srv.URL + "/v1"}, "")
	if mlx.reachable {
		t.Fatalf("mlx must not claim llama.cpp's port: %+v", mlx)
	}
}

func TestLMStudioRichModels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"qwen3-8b"},{"id":"text-embedding-nomic"},{"id":"gemma-3-4b"}]}`)
	})
	mux.HandleFunc("GET /api/v0/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"id":"qwen3-8b","type":"llm","max_context_length":40960,"loaded_context_length":8192,"capabilities":["tool_use"]},
			{"id":"text-embedding-nomic","type":"embeddings","max_context_length":2048},
			{"id":"gemma-3-4b","type":"vlm","max_context_length":131072}
		]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := &prober{http: srv.Client(), timeout: time.Second}
	res := p.probe(context.Background(), ProviderConfig{ID: "lmstudio", Kind: KindLMStudio, BaseURL: srv.URL + "/v1"}, "")
	if len(res.models) != 2 {
		t.Fatalf("models = %+v", res.models)
	}
	g, q := res.models[0], res.models[1]
	if q.ContextWindow != 8192 || q.MaxContextWindow != 40960 || q.Tools == nil || !*q.Tools {
		t.Errorf("qwen = %+v", q)
	}
	if g.Vision == nil || !*g.Vision || g.Tools != nil {
		t.Errorf("gemma = %+v", g)
	}
}

func TestOllamaStreamTextThinkingAndTools(t *testing.T) {
	f := &fakeOllama{chatNDJ: []string{
		`{"model":"qwen3:8b","message":{"role":"assistant","content":"","thinking":"Let me "},"done":false}`,
		`{"model":"qwen3:8b","message":{"role":"assistant","content":"","thinking":"check."},"done":false}`,
		`{"model":"qwen3:8b","message":{"role":"assistant","content":"Running ls."},"done":false}`,
		`{"model":"qwen3:8b","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"Bash","arguments":{"command":"ls"}}}]},"done":false}`,
		`{"model":"qwen3:8b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":120,"eval_count":30}`,
	}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	r := newTestRegistry(t)
	sink := &memSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep := NewUsageReporter(ctx, sink, nil)
	r.SetUsageReporter(rep)
	p := addProvider(t, r, KindOllama, srv.URL, "")
	model := ModelID(p.ID, "qwen3:8b")
	req := &llm.Request{
		Model: model, MaxTokens: 1000, System: []llm.SystemBlock{{Type: "text", Text: "You are helpful."}},
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("list files")}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "old"}, {Type: llm.BlockToolUse, ID: "t1", Name: "Bash", Input: json.RawMessage(`{"command":"pwd"}`)}}},
			{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1", Content: []llm.ContentBlock{llm.TextBlock("/home")}}}},
		},
		Tools:    []llm.ToolDef{bashTool},
		Thinking: &llm.Thinking{Type: "adaptive"},
		Metadata: &llm.Metadata{SessionID: "s1", RequestClass: "main"},
	}
	resp := collect(t, &Router{Core: nil, Local: r}, req)
	if resp.StopReason != "tool_use" {
		t.Errorf("stop = %q", resp.StopReason)
	}
	c := resp.Message.Content
	if len(c) != 3 || c[0].Type != llm.BlockThinking || c[0].Thinking != "Let me check." || c[1].Text != "Running ls." || c[2].Type != llm.BlockToolUse || c[2].Name != "Bash" || string(c[2].Input) != `{"command":"ls"}` || c[2].ID == "" {
		t.Fatalf("content = %+v", c)
	}
	if resp.Usage.InputTokens != 120 || resp.Usage.OutputTokens != 30 || resp.Harness.CostUSD != 0 {
		t.Errorf("usage = %+v %+v", resp.Usage, resp.Harness)
	}
	// The request: num_ctx, think, tool history in Ollama's shape.
	body := f.chats[0]
	opts := body["options"].(map[string]any)
	if opts["num_ctx"].(float64) != DefaultContextLength || opts["num_predict"].(float64) != 1000 || body["think"] != true {
		t.Errorf("options = %v think=%v", opts, body["think"])
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %v", msgs)
	}
	asst := msgs[2].(map[string]any)
	tc := asst["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if tc["name"] != "Bash" || tc["arguments"].(map[string]any)["command"] != "pwd" {
		t.Errorf("assistant = %v", asst)
	}
	if tool := msgs[3].(map[string]any); tool["role"] != "tool" || tool["content"] != "/home" || tool["tool_name"] != "Bash" {
		t.Errorf("tool message = %v", tool)
	}
	// Usage is recorded with cost 0 and flushed to the sink.
	deadline := time.Now().Add(2 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.recs)
		sink.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.recs) != 1 {
		t.Fatalf("usage records = %+v", sink.recs)
	}
	u := sink.recs[0]
	if u.ModelID != model || u.ProviderID != p.ID || u.Status != "ok" || u.StopReason != "tool_use" || u.SessionID != "s1" || u.RequestClass != "main" || u.Usage.InputTokens != 120 {
		t.Errorf("usage record = %+v", u)
	}
}

func TestOllamaNoToolsModel(t *testing.T) {
	f := &fakeOllama{chatNDJ: []string{
		`{"message":{"role":"assistant","content":"<think>hmm</think>Hello"},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"length","prompt_eval_count":5,"eval_count":2}`,
	}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	r := newTestRegistry(t)
	p := addProvider(t, r, KindOllama, srv.URL, "")
	// gemma is known to have no tools: none are sent, history tool calls become text.
	req := &llm.Request{Model: ModelID(p.ID, "gemma:2b"), Tools: []llm.ToolDef{bashTool}, Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("hi")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "t1", Name: "Bash", Input: json.RawMessage(`{}`)}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1", Content: []llm.ContentBlock{llm.TextBlock("ok")}}}},
	}}
	resp := collect(t, r.routerFor(), req)
	if f.chats[0]["tools"] != nil || f.chats[0]["think"] != nil {
		t.Errorf("tools/think sent to a model without them: %v", f.chats[0])
	}
	msgs := f.chats[0]["messages"].([]any)
	if !strings.Contains(msgs[1].(map[string]any)["content"].(string), "[called tool Bash") || !strings.Contains(msgs[2].(map[string]any)["content"].(string), "[tool result]") {
		t.Errorf("history = %v", msgs)
	}
	c := resp.Message.Content
	if len(c) != 2 || c[0].Thinking != "hmm" || c[1].Text != "Hello" || resp.StopReason != "max_tokens" {
		t.Errorf("resp = %+v %q", c, resp.StopReason)
	}

	// A model with unknown capabilities that the server refuses tools for: retried without, remembered.
	f.chatCode, f.chatErr = 400, "registry.ollama.ai/library/qwen3:8b does not support tools"
	f.chats = nil
	req.Model = ModelID(p.ID, "qwen3:8b")
	r.mu.Lock()
	st := r.state[p.ID]
	for i := range st.models {
		st.models[i].Tools = nil
	}
	r.mu.Unlock()
	collect(t, r.routerFor(), req)
	if len(f.chats) != 2 || f.chats[0]["tools"] == nil || f.chats[1]["tools"] != nil {
		t.Fatalf("retry = %d requests", len(f.chats))
	}
	f.chats = nil
	collect(t, r.routerFor(), req)
	if len(f.chats) != 1 || f.chats[0]["tools"] != nil {
		t.Errorf("no-tools not remembered: %d requests", len(f.chats))
	}
}

func (r *Registry) routerFor() *Router { return &Router{Local: r} }

func TestOpenAIStreamTextReasoningAndToolCalls(t *testing.T) {
	f := &fakeOpenAI{sse: []string{
		`{"id":"chatcmpl-1","model":"llama","choices":[{"delta":{"role":"assistant","reasoning_content":"Think "}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"reasoning_content":"hard."}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"content":"I'll run "}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"content":"two."}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"Bash","arguments":""}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"comm"}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"Bash","arguments":"{\"command\":\"pwd\"}"}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":200,"completion_tokens":40,"prompt_tokens_details":{"cached_tokens":50}}}`,
	}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	r := newTestRegistry(t)
	p := addProvider(t, r, KindCustom, srv.URL+"/v1", "sk-test")
	if !p.HasAPIKey || !p.Reachable || len(p.Models) != 2 || p.Models[0].MaxContextWindow != 131072 {
		t.Fatalf("provider = %+v", p)
	}
	req := &llm.Request{
		Model: ModelID(p.ID, "llama-3.2-3b"), MaxTokens: 512, System: []llm.SystemBlock{{Type: "text", Text: "sys"}},
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("go"), {Type: llm.BlockImage, Source: &llm.Source{Type: "base64", MediaType: "image/png", Data: "AAAA"}}}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("ok"), {Type: llm.BlockToolUse, ID: "t1", Name: "Bash", Input: json.RawMessage(`{"command":"pwd"}`)}}},
			{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1", IsError: true, Content: []llm.ContentBlock{llm.TextBlock("denied")}}, llm.TextBlock("continue")}},
		},
		Tools:      []llm.ToolDef{bashTool},
		ToolChoice: &llm.ToolChoice{Type: "any"},
	}
	resp := collect(t, r.routerFor(), req)
	c := resp.Message.Content
	if len(c) != 4 {
		t.Fatalf("content = %+v", c)
	}
	if c[0].Thinking != "Think hard." || c[1].Text != "I'll run two." || c[2].ID != "call_a" || string(c[2].Input) != `{"command":"ls"}` || c[3].ID != "call_b" || string(c[3].Input) != `{"command":"pwd"}` {
		t.Errorf("content = %+v", c)
	}
	if resp.StopReason != "tool_use" || resp.Usage.InputTokens != 150 || resp.Usage.CacheReadInputTokens != 50 || resp.Usage.OutputTokens != 40 {
		t.Errorf("stop=%q usage=%+v", resp.StopReason, resp.Usage)
	}
	body := f.bodies[0]
	if body["model"] != "llama-3.2-3b" || body["tool_choice"] != "required" || body["max_tokens"].(float64) != 512 {
		t.Errorf("body = %v", body)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("messages = %v", msgs)
	}
	if parts, ok := msgs[1].(map[string]any)["content"].([]any); !ok || len(parts) != 2 || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,AAAA" {
		t.Errorf("user = %v", msgs[1])
	}
	if a := msgs[2].(map[string]any); a["content"] != "ok" || a["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"] != `{"command":"pwd"}` {
		t.Errorf("assistant = %v", a)
	}
	if tm := msgs[3].(map[string]any); tm["role"] != "tool" || tm["tool_call_id"] != "t1" || tm["content"] != "Error: denied" {
		t.Errorf("tool = %v", tm)
	}
	if u := msgs[4].(map[string]any); u["role"] != "user" || u["content"] != "continue" {
		t.Errorf("user text = %v", u)
	}
	for _, a := range f.auth {
		if a != "Bearer sk-test" {
			t.Errorf("auth header = %q", a)
		}
	}
}

func TestOpenAIThinkTagsSplitAcrossChunks(t *testing.T) {
	f := &fakeOpenAI{sse: []string{
		`{"choices":[{"delta":{"content":"<thi"}}]}`,
		`{"choices":[{"delta":{"content":"nk>plan "}}]}`,
		`{"choices":[{"delta":{"content":"it</th"}}]}`,
		`{"choices":[{"delta":{"content":"ink>\nAnswer <b>"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	r := newTestRegistry(t)
	p := addProvider(t, r, KindVLLM, srv.URL+"/v1", "")
	resp := collect(t, r.routerFor(), &llm.Request{Model: ModelID(p.ID, "phi-2"), Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("q")}}}})
	c := resp.Message.Content
	if len(c) != 2 || c[0].Thinking != "plan it" || c[1].Text != "\nAnswer <b>" || resp.StopReason != "end_turn" {
		t.Errorf("content = %+v stop=%q", c, resp.StopReason)
	}
}

func TestOpenAINoToolsRetry(t *testing.T) {
	f := &fakeOpenAI{noTools: true, sse: []string{`{"choices":[{"delta":{"content":"plain"},"finish_reason":"stop"}]}`}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	r := newTestRegistry(t)
	p := addProvider(t, r, KindCustom, srv.URL+"/v1", "")
	req := &llm.Request{Model: ModelID(p.ID, "phi-2"), Tools: []llm.ToolDef{bashTool}, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("q")}}}}
	resp := collect(t, r.routerFor(), req)
	if resp.Message.Text() != "plain" || len(f.bodies) != 2 || f.bodies[1]["tools"] != nil {
		t.Fatalf("resp=%q requests=%d", resp.Message.Text(), len(f.bodies))
	}
	ps := r.List(context.Background(), false)
	for _, m := range ps[0].Models {
		if m.Name == "phi-2" && (m.Tools == nil || *m.Tools) {
			t.Errorf("phi-2 tools = %v", m.Tools)
		}
	}
}

func TestStreamErrors(t *testing.T) {
	f := &fakeOpenAI{sse: []string{`{"error":{"message":"model crashed"}}`}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	r := newTestRegistry(t)
	p := addProvider(t, r, KindCustom, srv.URL+"/v1", "")
	st, err := r.Stream(context.Background(), &llm.Request{Model: ModelID(p.ID, "phi-2")})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := gateway.Collect(st, nil); err == nil || !strings.Contains(err.Error(), "model crashed") {
		t.Errorf("err = %v", err)
	}
	if _, err := r.Stream(context.Background(), &llm.Request{Model: "local:nope/x"}); err == nil || !strings.Contains(err.Error(), "unknown local provider") {
		t.Errorf("unknown provider err = %v", err)
	}
	srv.Close()
	r.invalidate(p.ID)
	if _, err := r.Stream(context.Background(), &llm.Request{Model: ModelID(p.ID, "phi-2")}); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("down server err = %v", err)
	}
}

type fakeCore struct {
	models  []string
	streams int
}

func (c *fakeCore) Stream(ctx context.Context, req *llm.Request) (gateway.EventStream, error) {
	c.streams++
	return nil, fmt.Errorf("core called for %s", req.Model)
}

func (c *fakeCore) Models(ctx context.Context) (*llm.Catalog, error) {
	cat := &llm.Catalog{Slots: map[string]string{"main": "glm"}}
	for _, m := range c.models {
		cat.Models = append(cat.Models, llm.Model{ID: m})
	}
	return cat, nil
}

func (c *fakeCore) CountTokens(ctx context.Context, req *llm.Request) (*llm.TokenCount, error) {
	return &llm.TokenCount{InputTokens: 1}, nil
}

func TestRouterRoutingCatalogAndPolicy(t *testing.T) {
	f := &fakeOllama{chatNDJ: []string{`{"message":{"content":"hi"},"done":true,"done_reason":"stop"}`}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	r := newTestRegistry(t)
	p := addProvider(t, r, KindOllama, srv.URL, "")
	core := &fakeCore{models: []string{"glm"}}
	gw := &Router{Core: core, Local: r}
	cat, err := gw.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Models) != 3 || cat.Models[0].ID != "glm" || cat.Slots["main"] != "glm" {
		t.Fatalf("catalog = %+v", cat.Models)
	}
	q, _ := cat.Find(ModelID(p.ID, "qwen3:8b"))
	if q.Provider != "Ollama" || q.ContextWindow != DefaultContextLength || !q.Capabilities.Thinking || q.Pricing == nil || q.Pricing.Input != 0 {
		t.Errorf("local entry = %+v", q)
	}
	collect(t, gw, &llm.Request{Model: ModelID(p.ID, "qwen3:8b")})
	if core.streams != 0 {
		t.Error("local request went to core")
	}
	if _, err := gw.Stream(context.Background(), &llm.Request{Model: "glm"}); err == nil || core.streams != 1 {
		t.Error("core request not routed to core")
	}
	if tc, _ := gw.CountTokens(context.Background(), &llm.Request{Model: ModelID(p.ID, "qwen3:8b"), System: []llm.SystemBlock{{Text: strings.Repeat("x", 400)}}}); !tc.Estimated || tc.InputTokens < 100 {
		t.Errorf("count = %+v", tc)
	}

	// The server's policy hides local models and refuses requests to them.
	r.SetPolicyDisabled(true)
	cat, _ = gw.Models(context.Background())
	if len(cat.Models) != 1 {
		t.Errorf("catalog under policy = %+v", cat.Models)
	}
	_, err = gw.Stream(context.Background(), &llm.Request{Model: ModelID(p.ID, "qwen3:8b")})
	if ae, ok := err.(*llm.APIError); !ok || ae.Type != "permission" {
		t.Errorf("err under policy = %v", err)
	}
	if err := r.PullOllamaModel(context.Background(), p.ID, "x", func(PullProgress) error { return nil }); err != ErrDisabledByPolicy {
		t.Errorf("pull under policy = %v", err)
	}
}

func TestPullAndDeleteOllamaModel(t *testing.T) {
	f := &fakeOllama{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	r := newTestRegistry(t)
	p := addProvider(t, r, KindOllama, srv.URL, "")
	var got []PullProgress
	if err := r.PullOllamaModel(context.Background(), p.ID, "llama3.2:1b", func(pp PullProgress) error { got = append(got, pp); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[1].Completed != 40 || got[1].Total != 100 || !got[4].Done || got[3].Done {
		t.Errorf("progress = %+v", got)
	}
	if err := r.DeleteOllamaModel(context.Background(), p.ID, "gemma:2b"); err != nil || len(f.deleted) != 1 {
		t.Errorf("delete: %v %v", err, f.deleted)
	}
	err := r.DeleteOllamaModel(context.Background(), p.ID, "missing:1b")
	if ae, ok := err.(*llm.APIError); !ok || ae.Type != "not_found" {
		t.Errorf("delete missing = %v", err)
	}
	other := addProvider(t, r, KindCustom, srv.URL+"/v1", "")
	if err := r.PullOllamaModel(context.Background(), other.ID, "x", func(PullProgress) error { return nil }); err == nil {
		t.Error("pull on a non-Ollama provider")
	}
}

func TestProviderStoreAndKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local-providers.json")
	secrets := &KeyringSecrets{FallbackPath: filepath.Join(dir, "keys.json"), NoKeyring: true}
	r, _ := New(Options{Path: path, Secrets: secrets, HTTP: offline})
	ps := r.List(context.Background(), false)
	if len(ps) != len(builtins) || ps[0].ID != "ollama" || !ps[0].Builtin {
		t.Fatalf("builtins = %+v", ps)
	}
	off := false
	if _, err := r.Update(context.Background(), "ollama", UpdateInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	name := "x"
	if _, err := r.Update(context.Background(), "ollama", UpdateInput{Name: &name}); err == nil {
		t.Error("renamed a built-in provider")
	}
	if err := r.Remove("lmstudio"); !IsBuiltinError(err) {
		t.Errorf("remove builtin = %v", err)
	}
	c, err := r.Add(context.Background(), AddInput{BaseURL: "http://127.0.0.1:9/v1/", APIKey: "sk-1", Name: "Box"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindCustom || c.BaseURL != "http://127.0.0.1:9/v1" || !c.HasAPIKey || !strings.HasPrefix(c.ID, "custom-") {
		t.Errorf("custom = %+v", c)
	}
	if _, err := r.Add(context.Background(), AddInput{BaseURL: "ftp://x"}); err == nil {
		t.Error("accepted a non-http URL")
	}

	// A new registry reads the file; the key stays out of it.
	r2, _ := New(Options{Path: path, Secrets: secrets, HTTP: offline})
	ps = r2.List(context.Background(), false)
	var custom, ollama Provider
	for _, p := range ps {
		switch p.ID {
		case c.ID:
			custom = p
		case "ollama":
			ollama = p
		}
	}
	if ollama.Enabled || custom.Name != "Box" || !custom.HasAPIKey {
		t.Errorf("reloaded: ollama=%+v custom=%+v", ollama, custom)
	}
	data, _ := readFile(path)
	if strings.Contains(data, "sk-1") {
		t.Error("API key written to the provider file")
	}
	empty := ""
	if _, err := r2.Update(context.Background(), c.ID, UpdateInput{APIKey: &empty}); err != nil {
		t.Fatal(err)
	}
	if k, _ := secrets.Get(c.ID); k != "" {
		t.Errorf("key not removed: %q", k)
	}
	if err := r2.Remove(c.ID); err != nil {
		t.Fatal(err)
	}
	if len(r2.List(context.Background(), false)) != len(builtins) {
		t.Error("custom provider not removed")
	}
}

// offline refuses every request, so the built-in endpoints are never probed for real.
var offline = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("dial %s: connection refused", r.URL.Host)
})}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func TestUsageReporterKeepsFailedRecords(t *testing.T) {
	sink := &memSink{fail: true}
	u := &UsageReporter{sink: sink, kick: make(chan struct{}, 1)}
	u.Add(UsageRecord{ModelID: "local:ollama/a"})
	u.Add(UsageRecord{ModelID: "local:ollama/b"})
	if err := u.Flush(context.Background()); err == nil || len(u.Pending()) != 2 {
		t.Fatalf("failed flush dropped records: %v %d", err, len(u.Pending()))
	}
	sink.fail = false
	if err := u.Flush(context.Background()); err != nil || len(u.Pending()) != 0 || len(sink.recs) != 2 || sink.recs[0].ID == "" {
		t.Fatalf("flush: %v pending=%d sent=%+v", err, len(u.Pending()), sink.recs)
	}
}

func TestPolicyDisabledIn(t *testing.T) {
	doc := func(scope commonv1.Scope, content string) *syncv1.Document {
		return &syncv1.Document{Scope: scope, Content: content}
	}
	cases := []struct {
		docs []*syncv1.Document
		want bool
	}{
		{nil, false},
		{[]*syncv1.Document{doc(commonv1.Scope_SCOPE_ORG, `{"permissions":{}}`)}, false},
		{[]*syncv1.Document{doc(commonv1.Scope_SCOPE_ORG, `{"disableLocalModels":true}`)}, true},
		{[]*syncv1.Document{doc(commonv1.Scope_SCOPE_ORG, `{"disableLocalModels":"disable"}`)}, true},
		{[]*syncv1.Document{doc(commonv1.Scope_SCOPE_ORG, `{"disableLocalModels":false}`)}, false},
		{[]*syncv1.Document{doc(commonv1.Scope_SCOPE_USER, `{"disableLocalModels":true}`)}, false},
	}
	for i, c := range cases {
		if got := PolicyDisabledIn(c.docs); got != c.want {
			t.Errorf("case %d: %v", i, got)
		}
	}
}
