package llm

import (
	"encoding/json"
	"errors"
	"testing"
)

func ev(t string, raw string) StreamEvent { return StreamEvent{Type: t, Raw: json.RawMessage(raw)} }

func TestAccumulatorFullMessage(t *testing.T) {
	a := NewAccumulator()
	events := []StreamEvent{
		ev(EventMessageStart, `{"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":10}}}`),
		ev(EventContentBlockStart, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		ev(EventContentBlockDelta, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`),
		ev(EventContentBlockDelta, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`),
		ev(EventContentBlockStop, `{"type":"content_block_stop","index":0}`),
		ev(EventPing, `{"type":"ping"}`),
		ev(EventContentBlockStart, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		ev(EventContentBlockDelta, `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hel"}}`),
		ev(EventContentBlockDelta, `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lo"}}`),
		ev(EventContentBlockStop, `{"type":"content_block_stop","index":1}`),
		ev(EventContentBlockStart, `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}}`),
		ev(EventContentBlockDelta, `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"file_"}}`),
		ev(EventContentBlockDelta, `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"path\":\"/a\"}"}}`),
		ev(EventContentBlockStop, `{"type":"content_block_stop","index":2}`),
		ev(EventContentBlockStart, `{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_2","name":"Glob","input":{}}}`),
		ev(EventContentBlockStop, `{"type":"content_block_stop","index":3}`),
		ev(EventMessageDelta, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5,"cache_read_input_tokens":3}}`),
		ev(EventMessageStop, `{"type":"message_stop","harness":{"cost_usd":0.5,"upstream_model":"up"}}`),
	}
	for _, e := range events {
		if err := a.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	if !a.Done() {
		t.Fatal("not done")
	}
	r := a.Response()
	if r.ID != "msg_1" || r.StopReason != "tool_use" || r.Harness.CostUSD != 0.5 {
		t.Fatalf("bad meta: %+v", r)
	}
	if r.Usage.InputTokens != 10 || r.Usage.OutputTokens != 5 || r.Usage.CacheReadInputTokens != 3 {
		t.Fatalf("usage %+v", r.Usage)
	}
	c := r.Message.Content
	if len(c) != 4 || c[0].Thinking != "hmm" || c[0].Signature != "sig" || c[1].Text != "Hello" {
		t.Fatalf("content %+v", c)
	}
	if string(c[2].Input) != `{"file_path":"/a"}` || string(c[3].Input) != `{}` {
		t.Fatalf("tool inputs %s %s", c[2].Input, c[3].Input)
	}
	if len(r.Message.ToolUses()) != 2 {
		t.Fatal("tool uses")
	}
}

func TestAccumulatorErrorAndPartial(t *testing.T) {
	a := NewAccumulator()
	_ = a.Add(ev(EventMessageStart, `{"type":"message_start","message":{"id":"m"}}`))
	_ = a.Add(ev(EventContentBlockStart, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	_ = a.Add(ev(EventContentBlockDelta, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`))
	_ = a.Add(ev(EventContentBlockStop, `{"type":"content_block_stop","index":0}`))
	_ = a.Add(ev(EventContentBlockStart, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`))
	_ = a.Add(ev(EventContentBlockDelta, `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"half"}}`))
	if got := a.CompletedBlocks(); len(got) != 1 || got[0].Text != "done" {
		t.Fatalf("completed %+v", got)
	}
	err := a.Add(ev(EventError, `{"type":"error","error":{"type":"overloaded","message":"busy"}}`))
	var ae *APIError
	if !errors.As(err, &ae) || ae.Type != "overloaded" || !ae.Retryable() {
		t.Fatalf("err %v", err)
	}
}

func TestContentBlockJSON(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hi"}`), &m); err != nil || m.Content[0].Text != "hi" {
		t.Fatalf("string content: %v %+v", err, m)
	}
	var b ContentBlock
	if err := json.Unmarshal([]byte(`{"type":"tool_result","tool_use_id":"x","content":"out"}`), &b); err != nil || b.Content[0].Text != "out" {
		t.Fatalf("tool_result string content: %v %+v", err, b)
	}
	out, _ := json.Marshal(ContentBlock{Type: BlockToolUse, ID: "a", Name: "Bash"})
	if string(out) != `{"type":"tool_use","id":"a","name":"Bash","input":{}}` {
		t.Fatalf("tool_use json %s", out)
	}
	out, _ = json.Marshal(ContentBlock{Type: BlockToolResult, ToolUseID: "a"})
	if string(out) != `{"type":"tool_result","tool_use_id":"a","is_error":false,"content":[]}` {
		t.Fatalf("tool_result json %s", out)
	}
}
