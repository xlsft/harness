package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

func isSideChatRequest(r *llm.Request) bool {
	return len(r.System) == 2 && strings.HasPrefix(r.System[1].Text, "<main_session>")
}

// askSideChat runs AskSideChat to the end and returns the question, the deltas and the answer.
func (h *harness) askSideChat(req *agentv1.AskSideChatRequest) (*agentv1.SideChatMessage, []string, *agentv1.AskSideChatResponse) {
	h.t.Helper()
	stream, err := h.client.AskSideChat(h.ctx, connect.NewRequest(req))
	if err != nil {
		h.t.Fatal(err)
	}
	var question *agentv1.SideChatMessage
	var deltas []string
	var answer *agentv1.AskSideChatResponse
	for stream.Receive() {
		switch m := stream.Msg(); {
		case m.GetQuestion() != nil:
			question = m.GetQuestion()
		case m.GetAnswer() != nil:
			answer = m
		default:
			deltas = append(deltas, m.GetDelta())
		}
	}
	if err := stream.Err(); err != nil {
		h.t.Fatalf("ask: %v", err)
	}
	return question, deltas, answer
}

func TestSideChats(t *testing.T) {
	respond := func(n int, r *llm.Request) fg.Reply {
		if !isSideChatRequest(r) {
			return fg.Reply{Events: fg.Message("msg_main", fg.Text("The login test races on the clock."))}
		}
		if strings.Contains(r.Messages[len(r.Messages)-1].Text(), "hold") {
			return fg.Reply{Events: fg.Partial("msg_hold", false, fg.Text("Half an answer")), Hold: true}
		}
		return fg.Reply{Events: fg.Message("msg_side", fg.Text("It uses a fake clock now, see the main thread."))}
	}
	h := startHarnessGW(t, respond, func(g *fg.Server) { g.Models = []string{"fake-model", "other-model"} }, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Model: "fake-model", Effort: llmv1.Effort_EFFORT_HIGH})
	h.send(sid, "Fix the flaky login test")
	h.until(ch, isTurnFinished)

	// A new side chat starts with the main session's model and effort.
	created, err := h.client.CreateSideChat(h.ctx, connect.NewRequest(&agentv1.CreateSideChatRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	sc := created.Msg.SideChat
	if sc.Model != "fake-model" || sc.Effort != llmv1.Effort_EFFORT_HIGH || sc.Title != "Side chat" || sc.SessionId != sid || sc.MessageCount != 0 {
		t.Fatalf("created: %+v", sc)
	}
	if _, err := h.client.CreateSideChat(h.ctx, connect.NewRequest(&agentv1.CreateSideChatRequest{SessionId: sid, Model: "nope"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown model: %v", err)
	}

	// Ask with another model: it streams, is remembered for this side chat only, and is recorded.
	q, deltas, ans := h.askSideChat(&agentv1.AskSideChatRequest{SessionId: sid, SideChatId: sc.SideChatId, Text: "Why did it fail?", Quote: "races on the clock", Model: "other-model"})
	if q == nil || q.Role != "user" || q.Text != "Why did it fail?" || q.Quote != "races on the clock" {
		t.Fatalf("question: %+v", q)
	}
	if len(deltas) < 2 || strings.Join(deltas, "") != "It uses a fake clock now, see the main thread." {
		t.Fatalf("deltas: %q", deltas)
	}
	a := ans.GetAnswer()
	if a.Role != "assistant" || a.Text != "It uses a fake clock now, see the main thread." || a.Model != "other-model" || a.Usage.GetOutputTokens() != 20 || a.Stopped || a.Error != "" {
		t.Fatalf("answer: %+v", a)
	}
	if info := ans.GetSideChat(); info.Title != "Why did it fail?" || info.Model != "other-model" || info.MessageCount != 2 || info.Answering {
		t.Fatalf("side chat after the answer: %+v", info)
	}

	// The request: tools-free, auxiliary, the side chat prompt, the main conversation and the quoted question.
	var side []*llm.Request
	for _, r := range h.gw.Requests() {
		if isSideChatRequest(r) {
			side = append(side, r)
		}
	}
	if len(side) != 1 {
		t.Fatalf("side chat requests: %d", len(side))
	}
	r := side[0]
	if len(r.Tools) != 0 || r.Metadata == nil || r.Metadata.RequestClass != "auxiliary" || r.Metadata.SessionID != sid || r.Model != "other-model" {
		t.Fatalf("request: tools %d, metadata %+v, model %s", len(r.Tools), r.Metadata, r.Model)
	}
	if !strings.Contains(r.System[0].Text, "side chat") {
		t.Fatalf("system prompt: %q", r.System[0].Text)
	}
	if !strings.Contains(r.System[1].Text, "User: Fix the flaky login test") || !strings.Contains(r.System[1].Text, "Assistant: The login test races on the clock.") {
		t.Fatalf("main session: %q", r.System[1].Text)
	}
	if len(r.Messages) != 1 || !strings.Contains(r.Messages[0].Text(), "> races on the clock") {
		t.Fatalf("messages: %+v", r.Messages)
	}

	// Nothing was added to the main thread.
	entries, err := session.Load(findTranscript(t, sid))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Message != nil && strings.Contains(string(e.Message.Content), "fake clock") {
			t.Fatal("the side chat answer reached the main transcript")
		}
	}

	// Stop keeps the partial text; the second question sees the first exchange as history.
	done := make(chan *agentv1.AskSideChatResponse, 1)
	go func() {
		_, _, ans := h.askSideChat(&agentv1.AskSideChatRequest{SessionId: sid, SideChatId: sc.SideChatId, Text: "hold on"})
		done <- ans
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := h.client.StopSideChat(h.ctx, connect.NewRequest(&agentv1.StopSideChatRequest{SessionId: sid, SideChatId: sc.SideChatId}))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Msg.Stopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the answer never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var stopped *agentv1.AskSideChatResponse
	select {
	case stopped = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not end the answer")
	}
	if a := stopped.GetAnswer(); !a.Stopped || a.Error != "" || !strings.HasPrefix("Half an answer", a.Text) {
		t.Fatalf("stopped answer: %+v", a)
	}

	// Persisted: GetSideChat (and the file next to the transcript) has all four messages.
	got, err := h.client.GetSideChat(h.ctx, connect.NewRequest(&agentv1.GetSideChatRequest{SessionId: sid, SideChatId: sc.SideChatId}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.Msg.Messages); n != 4 || got.Msg.Messages[3].Stopped != true || got.Msg.SideChat.Model != "other-model" {
		t.Fatalf("stored: %+v", got.Msg)
	}
	file := filepath.Join(strings.TrimSuffix(findTranscript(t, sid), ".jsonl"), "side-chats", sc.SideChatId+".json")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("side chat file: %v", err)
	}
	var second *llm.Request
	for _, r := range h.gw.Requests() {
		if isSideChatRequest(r) {
			second = r
		}
	}
	if len(second.Messages) != 3 || second.Messages[1].Text() != "It uses a fake clock now, see the main thread." {
		t.Fatalf("history: %+v", second.Messages)
	}

	// Update, list, delete.
	up, err := h.client.UpdateSideChat(h.ctx, connect.NewRequest(&agentv1.UpdateSideChatRequest{SessionId: sid, SideChatId: sc.SideChatId, Title: ptr("Clock"), Effort: ptr(llmv1.Effort_EFFORT_LOW)}))
	if err != nil || up.Msg.SideChat.Title != "Clock" || up.Msg.SideChat.Effort != llmv1.Effort_EFFORT_LOW || up.Msg.SideChat.Model != "other-model" {
		t.Fatalf("update: %v %+v", err, up)
	}
	if _, err := h.client.CreateSideChat(h.ctx, connect.NewRequest(&agentv1.CreateSideChatRequest{SessionId: sid, Title: "Second"})); err != nil {
		t.Fatal(err)
	}
	list, err := h.client.ListSideChats(h.ctx, connect.NewRequest(&agentv1.ListSideChatsRequest{SessionId: sid}))
	if err != nil || len(list.Msg.SideChats) != 2 || list.Msg.SideChats[0].Title != "Clock" || list.Msg.SideChats[1].Title != "Second" {
		t.Fatalf("list: %v %+v", err, list)
	}
	if _, err := h.client.DeleteSideChat(h.ctx, connect.NewRequest(&agentv1.DeleteSideChatRequest{SessionId: sid, SideChatId: sc.SideChatId})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.GetSideChat(h.ctx, connect.NewRequest(&agentv1.GetSideChatRequest{SessionId: sid, SideChatId: sc.SideChatId})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("deleted: %v", err)
	}

	// Errors: empty question, unknown session.
	if s, err := h.client.AskSideChat(h.ctx, connect.NewRequest(&agentv1.AskSideChatRequest{SessionId: sid, SideChatId: list.Msg.SideChats[1].SideChatId})); err == nil {
		for s.Receive() {
		}
		if connect.CodeOf(s.Err()) != connect.CodeInvalidArgument {
			t.Fatalf("empty question: %v", s.Err())
		}
	}
	if _, err := h.client.ListSideChats(h.ctx, connect.NewRequest(&agentv1.ListSideChatsRequest{SessionId: "00000000-0000-0000-0000-000000000000"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
}

// findTranscript returns the transcript path of a session under HARNESS_HOME.
func findTranscript(t *testing.T, sid string) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(os.Getenv("HARNESS_HOME"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) == sid+".jsonl" {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no transcript for %s", sid)
	}
	return found
}
