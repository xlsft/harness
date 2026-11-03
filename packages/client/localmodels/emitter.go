package localmodels

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/llm"
)

// emitter turns provider deltas into the client's stream events (the Anthropic streaming shape the
// llm.Accumulator folds): message_start, content blocks (thinking, text, tool_use), message_delta and
// message_stop. Text and thinking blocks close when the kind changes; a tool block closes when the next one
// starts or the message ends.
type emitter struct {
	queue []llm.StreamEvent

	next     int    // index of the next block
	open     string // llm.BlockText / llm.BlockThinking while one is open
	openIdx  int
	toolIdx  map[int]int // upstream tool index -> block index
	toolOpen int         // block index of the open tool block, -1 when none
	closed   map[int]bool
	sawTool  bool
	usage    llm.Usage
	started  bool
	finished bool
	stop     string
}

func newEmitter() *emitter {
	return &emitter{toolIdx: map[int]int{}, toolOpen: -1, closed: map[int]bool{}}
}

func (e *emitter) push(typ string, v any) {
	raw, _ := json.Marshal(v)
	e.queue = append(e.queue, llm.StreamEvent{Type: typ, Raw: raw})
}

func (e *emitter) start(id, model string) {
	if e.started {
		return
	}
	e.started = true
	e.push(llm.EventMessageStart, map[string]any{
		"type": llm.EventMessageStart,
		"message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{},
			"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

func (e *emitter) closeOpen() {
	if e.open != "" {
		e.stopBlock(e.openIdx)
		e.open = ""
	}
}

func (e *emitter) closeTool() {
	if e.toolOpen >= 0 {
		e.stopBlock(e.toolOpen)
		e.toolOpen = -1
	}
}

func (e *emitter) stopBlock(i int) {
	if e.closed[i] {
		return
	}
	e.closed[i] = true
	e.push(llm.EventContentBlockStop, map[string]any{"type": llm.EventContentBlockStop, "index": i})
}

// delta appends text or thinking, opening a block of that kind when needed.
func (e *emitter) delta(kind, s string) {
	if s == "" {
		return
	}
	if e.open != kind {
		e.closeOpen()
		e.closeTool()
		e.open, e.openIdx = kind, e.next
		e.next++
		block := map[string]any{"type": kind}
		if kind == llm.BlockText {
			block["text"] = ""
		} else {
			block["thinking"] = ""
		}
		e.push(llm.EventContentBlockStart, map[string]any{"type": llm.EventContentBlockStart, "index": e.openIdx, "content_block": block})
	}
	d := map[string]any{}
	if kind == llm.BlockText {
		d["type"], d["text"] = "text_delta", s
	} else {
		d["type"], d["thinking"] = "thinking_delta", s
	}
	e.push(llm.EventContentBlockDelta, map[string]any{"type": llm.EventContentBlockDelta, "index": e.openIdx, "delta": d})
}

// toolStart opens a tool_use block for upstream tool index up (a repeated start is ignored).
func (e *emitter) toolStart(up int, id, name string) {
	if _, ok := e.toolIdx[up]; ok {
		return
	}
	e.closeOpen()
	e.closeTool()
	if id == "" {
		id = newToolID()
	}
	i := e.next
	e.next++
	e.toolIdx[up], e.toolOpen, e.sawTool = i, i, true
	e.push(llm.EventContentBlockStart, map[string]any{
		"type": llm.EventContentBlockStart, "index": i,
		"content_block": map[string]any{"type": llm.BlockToolUse, "id": id, "name": name, "input": map[string]any{}},
	})
}

// toolArgs appends argument JSON to the tool block of upstream index up. Arguments of a block already closed
// (a server interleaving parallel calls) are dropped.
func (e *emitter) toolArgs(up int, s string) {
	i, ok := e.toolIdx[up]
	if !ok || s == "" || e.closed[i] {
		return
	}
	e.push(llm.EventContentBlockDelta, map[string]any{"type": llm.EventContentBlockDelta, "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": s}})
}

// finish closes every block and ends the message. stopReason is the Anthropic one; tool calls force tool_use.
func (e *emitter) finish(stopReason string, usage llm.Usage, upstreamModel string) {
	if e.finished {
		return
	}
	e.finished = true
	e.closeOpen()
	e.closeTool()
	if e.sawTool && (stopReason == "" || stopReason == "end_turn") {
		stopReason = "tool_use"
	}
	if stopReason == "" {
		stopReason = "end_turn"
	}
	e.usage, e.stop = usage, stopReason
	e.push(llm.EventMessageDelta, map[string]any{
		"type":  llm.EventMessageDelta,
		"delta": map[string]any{"stop_reason": stopReason},
		"usage": map[string]int{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "cache_read_input_tokens": usage.CacheReadInputTokens},
	})
	e.push(llm.EventMessageStop, map[string]any{"type": llm.EventMessageStop, "harness": map[string]any{"cost_usd": 0, "upstream_model": upstreamModel}})
}

// mapFinish maps an OpenAI / Ollama finish reason.
func mapFinish(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "stop", "eos", "":
		return "end_turn"
	}
	return "end_turn"
}

// thinkSplitter separates <think>…</think> spans of a text stream (reasoning models served without a
// reasoning parser) from the answer. Tags may be split across chunks.
type thinkSplitter struct {
	inThink bool
	buf     string
}

type piece struct {
	thinking bool
	text     string
}

const (
	openTag  = "<think>"
	closeTag = "</think>"
)

func (t *thinkSplitter) feed(s string) []piece {
	t.buf += s
	var out []piece
	for t.buf != "" {
		tag := openTag
		if t.inThink {
			tag = closeTag
		}
		if i := strings.Index(t.buf, tag); i >= 0 {
			if i > 0 {
				out = append(out, piece{t.inThink, t.buf[:i]})
			}
			t.buf = t.buf[i+len(tag):]
			t.inThink = !t.inThink
			continue
		}
		// Keep a trailing prefix of the tag for the next chunk.
		keep := 0
		for n := min(len(tag)-1, len(t.buf)); n > 0; n-- {
			if strings.HasSuffix(t.buf, tag[:n]) {
				keep = n
				break
			}
		}
		if emit := t.buf[:len(t.buf)-keep]; emit != "" {
			out = append(out, piece{t.inThink, emit})
		}
		t.buf = t.buf[len(t.buf)-keep:]
		break
	}
	return out
}

func (t *thinkSplitter) flush() []piece {
	if t.buf == "" {
		return nil
	}
	p := []piece{{t.inThink, t.buf}}
	t.buf = ""
	return p
}

// emitPieces sends split text to the emitter.
func (e *emitter) emitPieces(ps []piece) {
	for _, p := range ps {
		if p.thinking {
			e.delta(llm.BlockThinking, p.text)
		} else {
			e.delta(llm.BlockText, p.text)
		}
	}
}

// chunkStream is a pull-based gateway.EventStream: read parses the next upstream chunk into the emitter's
// queue and returns io.EOF at the end of the body (after calling finish).
type chunkStream struct {
	em     *emitter
	read   func() error
	body   io.Closer
	err    error
	done   bool
	once   sync.Once
	onDone func(err error)
}

func (s *chunkStream) Next() (llm.StreamEvent, error) {
	for len(s.em.queue) == 0 {
		if s.done {
			if s.err != nil {
				return llm.StreamEvent{}, s.err
			}
			return llm.StreamEvent{}, io.EOF
		}
		if err := s.read(); err != nil {
			s.done = true
			if !errors.Is(err, io.EOF) {
				s.err = err
			}
			s.complete(s.err)
		}
	}
	ev := s.em.queue[0]
	s.em.queue = s.em.queue[1:]
	return ev, nil
}

func (s *chunkStream) complete(err error) {
	s.once.Do(func() {
		if s.onDone != nil {
			s.onDone(err)
		}
	})
}

func (s *chunkStream) Close() error {
	switch {
	case s.em.finished:
		s.complete(nil) // the reader stopped at message_stop
	case !s.done:
		s.complete(errAborted)
	}
	return s.body.Close()
}

var errAborted = errors.New("aborted")
