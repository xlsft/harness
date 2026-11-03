package server_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
)

func noAITitles(s *server.Server) { s.NoAITitles = true }

func (h *harness) sendID(sid, id, msg string, d agentv1.SendUserMessageRequest_Delivery) {
	h.t.Helper()
	if _, err := h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: id, Content: text(msg), Delivery: d})); err != nil {
		h.t.Fatal(err)
	}
}

func userMessages(evs []*agentv1.SessionEvent) []*agentv1.UserMessage {
	var out []*agentv1.UserMessage
	for _, ev := range evs {
		if u := ev.GetUserMessage(); u != nil {
			out = append(out, u)
		}
	}
	return out
}

func userText(u *agentv1.UserMessage) string {
	var parts []string
	for _, b := range u.GetContent() {
		if t := b.GetText(); t != nil {
			parts = append(parts, t.GetText())
		}
	}
	return strings.Join(parts, "|")
}

// Every user message event names the frontend messages it carries (client_uuids): the prompt that starts a turn,
// queued messages injected into the running turn together, and a NOW message that starts the next turn. The
// client's queue reports when each message was queued, and the uuids survive a reload of the transcript.
func TestDeliveryClientUUIDs(t *testing.T) {
	h := startHarnessSrv(t, func(n int, r *llm.Request) fg.Reply {
		last := r.Messages[len(r.Messages)-1]
		lb := last.Content[len(last.Content)-1]
		switch {
		case lb.Type == llm.BlockText && lb.Text == "start":
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("b", "Bash", map[string]any{"command": "sleep 1"}))}
		case lb.Type == llm.BlockText && lb.Text == "hold":
			return fg.Reply{Events: fg.Partial("h", false, fg.Text("thinking...")), Hold: true}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ack"))}
	}, nil, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash"} }, noAITitles)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	// 1. A prompt sent while idle starts a turn: its user message carries the frontend uuid, and the turn
	// started names that user message.
	h.sendID(sid, "c-start", "start", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	var queues []*agentv1.QueueChanged
	evs := h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetToolCallStarted() != nil {
			// 2. Two messages sent while the tool runs wait in the client's queue.
			h.sendID(sid, "c-a", "a", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
			h.sendID(sid, "c-b", "b", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
		}
		if q := ev.GetQueueChanged(); q != nil {
			queues = append(queues, q)
		}
		return isTurnFinished(ev)
	})
	um := userMessages(evs)
	if len(um) != 2 || !slices.Equal(um[0].ClientUuids, []string{"c-start"}) || userText(um[0]) != "start" {
		t.Fatalf("prompt user message: %+v", um)
	}
	var started *agentv1.TurnStarted
	for _, ev := range evs {
		if ts := ev.GetTurnStarted(); ts != nil {
			started = ts
		}
	}
	if started == nil || started.UserMessageUuid != um[0].Uuid {
		t.Fatalf("turn started %+v, prompt %s", started, um[0].Uuid)
	}
	// 3. Both queued messages were injected into the turn in one user message carrying both uuids.
	if !slices.Equal(um[1].ClientUuids, []string{"c-a", "c-b"}) || userText(um[1]) != "a|b" {
		t.Fatalf("injected user message: %+v", um[1])
	}
	full := false
	for _, q := range queues {
		if len(q.Queued) == 2 && q.Queued[0].Uuid == "c-a" && q.Queued[1].Uuid == "c-b" {
			full = true
			for _, m := range q.Queued {
				if _, err := time.Parse(time.RFC3339Nano, m.QueuedAt); err != nil {
					t.Fatalf("queued_at %q: %v", m.QueuedAt, err)
				}
			}
		}
	}
	if !full || len(queues[len(queues)-1].Queued) != 0 {
		t.Fatalf("queue changes: %+v", queues)
	}

	// 4. A NOW message while the model generates stops the turn and starts the next one with its uuid.
	h.sendID(sid, "c-hold", "hold", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	sent := false
	finished := 0
	evs = h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetAssistantDelta() != nil && !sent {
			sent = true
			h.sendID(sid, "c-now", "now", agentv1.SendUserMessageRequest_DELIVERY_NOW)
		}
		if isTurnFinished(ev) {
			finished++
		}
		return finished == 2
	})
	var now *agentv1.UserMessage
	for _, u := range userMessages(evs) {
		if userText(u) == "now" {
			now = u
		}
	}
	if now == nil || !slices.Equal(now.ClientUuids, []string{"c-now"}) {
		t.Fatalf("NOW message: %+v", userMessages(evs))
	}

	// 5. The uuids are in the transcript: a reload replays them.
	h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid}))
	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	evs = h.until(h.subscribe(sid), func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated() != nil })
	got := map[string][]string{}
	for _, u := range userMessages(evs) {
		got[userText(u)] = u.ClientUuids
	}
	if !slices.Equal(got["start"], []string{"c-start"}) || !slices.Equal(got["a|b"], []string{"c-a", "c-b"}) || !slices.Equal(got["hold"], []string{"c-hold"}) || !slices.Equal(got["now"], []string{"c-now"}) {
		t.Fatalf("replayed client uuids: %v", got)
	}
}

// A fork from a message holds the conversation before that message, names its parent and is titled after it.
// Detaching it (live, then from its transcript) makes it a top-level session.
func TestForkFromMessageAndDetach(t *testing.T) {
	h := startHarnessSrv(t, func(n int, r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, nil, nil, noAITitles)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	var prompts []*agentv1.UserMessage
	for _, p := range []string{"one", "two", "three"} {
		h.send(sid, p)
		prompts = append(prompts, userMessages(h.until(ch, isTurnFinished))...)
	}
	if len(prompts) != 3 {
		t.Fatalf("prompts %+v", prompts)
	}

	fk, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid, Fork: true, ForkAtCheckpointId: prompts[2].Uuid}))
	if err != nil {
		t.Fatal(err)
	}
	fork := fk.Msg.Session
	if fork.Id == sid || fork.ParentSessionId != sid || fork.Title != "one (fork)" {
		t.Fatalf("fork %+v", fork)
	}
	evs := h.until(h.subscribe(fork.Id), func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated() != nil })
	var texts []string
	for _, u := range userMessages(evs) {
		texts = append(texts, userText(u))
	}
	if strings.Join(texts, ",") != "one,two" {
		t.Fatalf("fork transcript: %v", texts)
	}

	// A fork of the fork keeps a single suffix and names the fork, not the grandparent.
	fk2, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: fork.Id, Fork: true}))
	if err != nil || fk2.Msg.Session.ParentSessionId != fork.Id || fk2.Msg.Session.Title != "one (fork)" {
		t.Fatalf("fork of fork %v %+v", err, fk2)
	}
	fork2 := fk2.Msg.Session.Id
	h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: fork2}))
	parentOf := func(id string) string {
		t.Helper()
		ls, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ls.Msg.Sessions {
			if s.Id == id {
				return s.ParentSessionId
			}
		}
		t.Fatalf("session %s not listed", id)
		return ""
	}
	if p := parentOf(fork2); p != fork.Id {
		t.Fatalf("listed parent of the closed fork of fork: %q", p)
	}

	// Detach the live fork.
	up, err := h.client.UpdateSession(h.ctx, connect.NewRequest(&agentv1.UpdateSessionRequest{SessionId: fork.Id, DetachFromParent: new(true)}))
	if err != nil || up.Msg.Session.ParentSessionId != "" {
		t.Fatalf("detach live %v %+v", err, up)
	}
	h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: fork.Id}))
	if p := parentOf(fork.Id); p != "" {
		t.Fatalf("detached fork still lists parent %q", p)
	}
	// A resume agrees with the transcript.
	rs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: fork.Id}))
	if err != nil || rs.Msg.Session.ParentSessionId != "" {
		t.Fatalf("resumed detached fork %v %+v", err, rs)
	}

	// Detach a session that is not live (its transcript records it).
	up, err = h.client.UpdateSession(h.ctx, connect.NewRequest(&agentv1.UpdateSessionRequest{SessionId: fork2, DetachFromParent: new(true)}))
	if err != nil || up.Msg.Session.ParentSessionId != "" {
		t.Fatalf("detach closed %v %+v", err, up)
	}
	if p := parentOf(fork2); p != "" {
		t.Fatalf("detached closed fork still lists parent %q", p)
	}
}
