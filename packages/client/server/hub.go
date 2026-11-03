package server

import (
	"encoding/json"
	"sync"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// subscriberBuffer is the per-subscriber backlog; a subscriber that falls
// further behind is dropped and must resubscribe with after_seq.
const subscriberBuffer = 4096

// hub keeps the ordered event log of one session and fans events out to
// subscribers. Replay and registration happen under one lock, so a
// subscriber sees history then live events with no gaps or duplicates.
type hub struct {
	sessionID string

	mu     sync.Mutex
	seq    int64
	events []*agentv1.SessionEvent
	subs   map[chan *agentv1.SessionEvent]struct{}
	closed bool
}

func newHub() *hub { return &hub{subs: map[chan *agentv1.SessionEvent]struct{}{}} }

func (h *hub) publish(ev *agentv1.SessionEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.seq++
	ev.Seq = h.seq
	ev.SessionId = h.sessionID
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	h.events = append(h.events, ev)
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// publishEphemeral fans out an event that is neither stored nor replayed
// and does not consume a seq (AssistantDelta): it carries the seq of the
// last durable event.
func (h *hub) publishEphemeral(ev *agentv1.SessionEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	ev.Seq = h.seq
	ev.SessionId = h.sessionID
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// close ends all subscriptions (after SessionEnded).
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

// watched reports whether any subscriber is following the session.
func (h *hub) watched() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs) > 0
}

func (h *hub) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

func (h *hub) subscribe(after int64) ([]*agentv1.SessionEvent, <-chan *agentv1.SessionEvent, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var replay []*agentv1.SessionEvent
	for _, ev := range h.events {
		if ev.Seq > after {
			replay = append(replay, ev)
		}
	}
	ch := make(chan *agentv1.SessionEvent, subscriberBuffer)
	if h.closed {
		close(ch)
		return replay, ch, func() {}
	}
	h.subs[ch] = struct{}{}
	return replay, ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// onStream converts raw gateway stream events into AssistantDelta events.
func (ls *liveSession) onStream(raw json.RawMessage) {
	var ev struct {
		Type    string `json:"type"`
		Index   int32  `json:"index"`
		Message *struct {
			ID string `json:"id"`
		} `json:"message"`
		Delta *struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			ls.mu.Lock()
			ls.messageID = ev.Message.ID
			ls.mu.Unlock()
		}
	case "content_block_delta":
		if ev.Delta == nil {
			return
		}
		ls.mu.Lock()
		mid := ls.messageID
		ls.mu.Unlock()
		d := &agentv1.AssistantDelta{MessageId: mid, Index: ev.Index}
		switch ev.Delta.Type {
		case "text_delta":
			d.Delta = &agentv1.AssistantDelta_Text{Text: ev.Delta.Text}
		case "thinking_delta":
			d.Delta = &agentv1.AssistantDelta_Thinking{Thinking: ev.Delta.Thinking}
		case "input_json_delta":
			d.Delta = &agentv1.AssistantDelta_InputJson{InputJson: ev.Delta.PartialJSON}
		default:
			return
		}
		ls.hub.publishEphemeral(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_AssistantDelta{AssistantDelta: d}})
	}
}

func jsonField(input []byte, key string) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	s, _ := m[key].(string)
	return s
}
