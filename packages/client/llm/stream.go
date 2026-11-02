package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Stream event types (Anthropic streaming plus harness extensions).
const (
	EventMessageStart      = "message_start"
	EventContentBlockStart = "content_block_start"
	EventContentBlockDelta = "content_block_delta"
	EventContentBlockStop  = "content_block_stop"
	EventMessageDelta      = "message_delta"
	EventMessageStop       = "message_stop"
	EventPing              = "ping"
	EventHarnessRetry      = "harness_retry"
	EventError             = "error"
)

// StreamEvent is one decoded event of a messages stream. Raw keeps the original
// JSON so it can be forwarded verbatim (e.g. as SDK stream_event).
type StreamEvent struct {
	Type string
	Raw  json.RawMessage
}

// RetryInfo is the payload of harness_retry.
type RetryInfo struct {
	Attempt      int       `json:"attempt"`
	MaxRetries   int       `json:"max_retries"`
	RetryDelayMs int       `json:"retry_delay_ms"`
	Error        *APIError `json:"error,omitempty"`
}

type wireEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Usage      *Usage `json:"usage"`
	} `json:"message"`
	Index        int           `json:"index"`
	ContentBlock *ContentBlock `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage   *Usage       `json:"usage"`
	Harness *HarnessMeta `json:"harness"`
	Error   *APIError    `json:"error"`
}

// Accumulator folds stream events into a Response. It is transport-neutral:
// both the SSE adapter and future adapters feed it StreamEvents.
type Accumulator struct {
	resp     Response
	blocks   []*ContentBlock
	partial  map[int]*strings.Builder
	startIn  map[int]json.RawMessage
	open     map[int]bool
	started  bool
	stopped  bool
	sawBlock bool
}

// NewAccumulator returns an empty accumulator.
func NewAccumulator() *Accumulator {
	return &Accumulator{partial: map[int]*strings.Builder{}, startIn: map[int]json.RawMessage{}, open: map[int]bool{}}
}

// Started reports whether message_start was seen.
func (a *Accumulator) Started() bool { return a.started }

// SawContent reports whether any content block started.
func (a *Accumulator) SawContent() bool { return a.sawBlock }

// Done reports whether message_stop was seen.
func (a *Accumulator) Done() bool { return a.stopped }

// Add processes one event. It returns an *APIError for error events.
func (a *Accumulator) Add(ev StreamEvent) error {
	switch ev.Type {
	case EventPing, EventHarnessRetry, "":
		return nil
	}
	var w wireEvent
	if err := json.Unmarshal(ev.Raw, &w); err != nil {
		return fmt.Errorf("decode %s event: %w", ev.Type, err)
	}
	switch ev.Type {
	case EventMessageStart:
		a.started = true
		if w.Message != nil {
			a.resp.ID, a.resp.Model = w.Message.ID, w.Message.Model
			if w.Message.Usage != nil {
				a.resp.Usage = *w.Message.Usage
			}
		}
	case EventContentBlockStart:
		if w.ContentBlock == nil {
			return fmt.Errorf("content_block_start without content_block")
		}
		a.sawBlock = true
		for len(a.blocks) <= w.Index {
			a.blocks = append(a.blocks, nil)
		}
		b := *w.ContentBlock
		if b.Type == BlockToolUse {
			// input arrives through input_json_delta; a non-empty start input is kept as a fallback.
			if len(b.Input) > 0 && string(b.Input) != "{}" {
				a.startIn[w.Index] = b.Input
			}
			b.Input = nil
		}
		a.blocks[w.Index] = &b
		a.open[w.Index] = true
	case EventContentBlockDelta:
		if w.Delta == nil || w.Index >= len(a.blocks) || a.blocks[w.Index] == nil {
			return fmt.Errorf("content_block_delta for unknown block %d", w.Index)
		}
		b := a.blocks[w.Index]
		switch w.Delta.Type {
		case "text_delta":
			b.Text += w.Delta.Text
		case "thinking_delta":
			b.Thinking += w.Delta.Thinking
		case "signature_delta":
			b.Signature += w.Delta.Signature
		case "input_json_delta":
			sb := a.partial[w.Index]
			if sb == nil {
				sb = &strings.Builder{}
				a.partial[w.Index] = sb
			}
			sb.WriteString(w.Delta.PartialJSON)
		}
	case EventContentBlockStop:
		if w.Index < len(a.blocks) && a.blocks[w.Index] != nil {
			a.finishBlock(w.Index)
		}
	case EventMessageDelta:
		if w.Delta != nil && w.Delta.StopReason != "" {
			a.resp.StopReason = w.Delta.StopReason
		}
		if w.Usage != nil {
			mergeUsage(&a.resp.Usage, *w.Usage)
		}
	case EventMessageStop:
		a.stopped = true
		if w.Harness != nil {
			a.resp.Harness = *w.Harness
		}
	case EventError:
		e := w.Error
		if e == nil {
			e = &APIError{Type: "internal", Message: "stream error"}
		}
		return e
	}
	return nil
}

func (a *Accumulator) finishBlock(i int) {
	b := a.blocks[i]
	if !a.open[i] {
		return
	}
	a.open[i] = false
	if b.Type == BlockToolUse {
		in := ""
		if sb := a.partial[i]; sb != nil {
			in = strings.TrimSpace(sb.String())
		}
		if in == "" {
			in = strings.TrimSpace(string(a.startIn[i]))
		}
		if in == "" {
			in = "{}"
		}
		if !json.Valid([]byte(in)) {
			// Keep the raw text so the tool reports an input error instead of crashing the stream.
			q, _ := json.Marshal(map[string]string{"__invalid_json__": in})
			in = string(q)
		}
		b.Input = json.RawMessage(in)
	}
}

func mergeUsage(dst *Usage, u Usage) {
	if u.InputTokens != 0 {
		dst.InputTokens = u.InputTokens
	}
	if u.OutputTokens != 0 {
		dst.OutputTokens = u.OutputTokens
	}
	if u.CacheReadInputTokens != 0 {
		dst.CacheReadInputTokens = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != 0 {
		dst.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
}

// Response returns the accumulated response. Blocks that never received
// content_block_stop are finalized as well.
func (a *Accumulator) Response() Response {
	r := a.resp
	r.Message = Message{Role: RoleAssistant, Content: []ContentBlock{}}
	for i, b := range a.blocks {
		if b == nil {
			continue
		}
		a.finishBlock(i)
		r.Message.Content = append(r.Message.Content, *b)
	}
	return r
}

// CompletedBlocks returns only blocks that received content_block_stop; used
// when a stream is interrupted.
func (a *Accumulator) CompletedBlocks() []ContentBlock {
	var out []ContentBlock
	for i, b := range a.blocks {
		if b == nil || a.open[i] {
			continue
		}
		out = append(out, *b)
	}
	return out
}
