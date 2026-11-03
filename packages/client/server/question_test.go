package server_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// askOnce asks one single-choice question on the first main request and
// answers "done" to everything else.
func askOnce(n int, r *llm.Request) fg.Reply {
	if n == 0 {
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("q", "AskUserQuestion", map[string]any{"questions": []any{
			map[string]any{"question": "Which database?", "header": "DB", "type": "single", "options": []any{
				map[string]any{"label": "Postgres", "recommended": true}, map[string]any{"label": "SQLite", "description": "embedded"}}},
			map[string]any{"question": "Ship it?", "header": "Ship", "type": "yes_no", "allowOther": false},
		}}))}
	}
	return fg.Reply{Events: fg.Message("m_done", fg.Text("done"))}
}

func (h *harness) answer(req *agentv1.AnswerQuestionRequest) error {
	_, err := h.client.AnswerQuestion(h.ctx, connect.NewRequest(req))
	return err
}

func (h *harness) sendNow(sid, msg string) {
	h.t.Helper()
	if _, err := h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Content: text(msg),
		Delivery: agentv1.SendUserMessageRequest_DELIVERY_NOW})); err != nil {
		h.t.Fatal(err)
	}
}

// lastUserBlocks returns the blocks of the last user message of a request.
func lastUserBlocks(r *llm.Request) []llm.ContentBlock {
	return r.Messages[len(r.Messages)-1].Content
}

func TestQuestionWireFormat(t *testing.T) {
	h := startHarness(t, askOnce, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var resolved *agentv1.QuestionResolved
	var q *agentv1.Question
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetQuestion() != nil:
			q = ev.GetQuestion()
			// Invalid answers are rejected and the question keeps waiting.
			bad := &agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: q.RequestId, Answers: []*agentv1.QuestionAnswer{{Selected: []string{"MySQL"}}, {Skipped: true}}}
			if err := h.answer(bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("invalid answer: %v", err)
			}
			cancel := &agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: q.RequestId, Outcome: agentv1.QuestionOutcome_QUESTION_OUTCOME_CANCELLED}
			if err := h.answer(cancel); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("cancel outcome: %v", err)
			}
			good := &agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: q.RequestId, Answers: []*agentv1.QuestionAnswer{{Text: "MySQL"}, {Skipped: true}}}
			if err := h.answer(good); err != nil {
				t.Fatal(err)
			}
		case ev.GetQuestionResolved() != nil:
			resolved = ev.GetQuestionResolved()
		}
		return isTurnFinished(ev)
	})
	it := q.GetItems()
	if len(it) != 2 || it[0].Type != agentv1.QuestionItem_TYPE_SINGLE || !it[0].AllowOther || !it[0].Options[0].Recommended ||
		it[1].Type != agentv1.QuestionItem_TYPE_YES_NO || it[1].AllowOther || len(it[1].Options) != 2 || it[1].Options[0].Label != "Yes" {
		t.Fatalf("question %+v", q)
	}
	if resolved.Outcome != agentv1.QuestionOutcome_QUESTION_OUTCOME_ANSWERED || resolved.ResolvedBy != "local" ||
		len(resolved.Answers) != 2 || resolved.Answers[0].Text != "MySQL" || !resolved.Answers[1].Skipped {
		t.Fatalf("resolved %+v", resolved)
	}
	tr := lastResultText(h.gw.Requests()[1])
	if !strings.Contains(tr, `[{"question":"Which database?","selected":[],"other":"MySQL"},{"question":"Ship it?","skipped":true}]`) {
		t.Fatalf("result %q", tr)
	}
}

// Chat messages sent while the question is open (even "send now") do not
// cancel it; after the answer they follow the tool result, in order.
func TestQuestionChatWhileWaiting(t *testing.T) {
	h := startHarness(t, askOnce, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var reqID string
	var result agentv1.TurnFinished_Result
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetQuestion() != nil:
			reqID = ev.GetQuestion().RequestId
			h.sendNow(sid, "first note")
			h.send(sid, "second note")
		case ev.GetQueueChanged() != nil && len(ev.GetQueueChanged().Queued) == 2:
			if err := h.answer(&agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: reqID,
				Answers: []*agentv1.QuestionAnswer{{Selected: []string{"SQLite"}}, {Selected: []string{"Yes"}}}}); err != nil {
				t.Fatal(err)
			}
		case ev.GetTurnFinished() != nil:
			result = ev.GetTurnFinished().Result
			return true
		}
		return false
	})
	if result != agentv1.TurnFinished_RESULT_SUCCESS {
		t.Fatalf("turn %v", result)
	}
	reqs := h.gw.Requests()
	blocks := lastUserBlocks(reqs[1])
	if len(blocks) != 3 || blocks[0].Type != llm.BlockToolResult || blocks[1].Text != "first note" || blocks[2].Text != "second note" {
		t.Fatalf("order: %+v", blocks)
	}
	if !strings.Contains(fg.ResultText(blocks[0]), `"selected":["SQLite"]`) {
		t.Fatalf("result %q", fg.ResultText(blocks[0]))
	}
}

// "Answer with message": the queued chat messages leave the queue and
// become the free-form answer.
func TestQuestionAnswerWithMessage(t *testing.T) {
	h := startHarness(t, askOnce, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var reqID string
	var resolved *agentv1.QuestionResolved
	queueCleared := false
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetQuestion() != nil:
			reqID = ev.GetQuestion().RequestId
			msg := &agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: reqID, Outcome: agentv1.QuestionOutcome_QUESTION_OUTCOME_MESSAGE}
			if err := h.answer(msg); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("message without queue: %v", err)
			}
			h.send(sid, "SQLite, and")
			h.send(sid, "do not ship yet")
		case ev.GetQueueChanged() != nil && len(ev.GetQueueChanged().Queued) == 2:
			if err := h.answer(&agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: reqID, Outcome: agentv1.QuestionOutcome_QUESTION_OUTCOME_MESSAGE}); err != nil {
				t.Fatal(err)
			}
		case ev.GetQueueChanged() != nil && len(ev.GetQueueChanged().Queued) == 0 && resolved == nil:
			queueCleared = true
		case ev.GetQuestionResolved() != nil:
			resolved = ev.GetQuestionResolved()
		}
		return isTurnFinished(ev)
	})
	if !queueCleared {
		t.Fatal("queue not cleared before the answer")
	}
	if resolved.Outcome != agentv1.QuestionOutcome_QUESTION_OUTCOME_MESSAGE || resolved.Message != "SQLite, and\n\ndo not ship yet" {
		t.Fatalf("resolved %+v", resolved)
	}
	blocks := lastUserBlocks(h.gw.Requests()[1])
	if len(blocks) != 1 || blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("messages delivered twice: %+v", blocks)
	}
	if tr := fg.ResultText(blocks[0]); !strings.Contains(tr, "free-form answer") || !strings.HasSuffix(tr, "SQLite, and\n\ndo not ship yet") {
		t.Fatalf("result %q", tr)
	}
}

func TestQuestionSkip(t *testing.T) {
	h := startHarness(t, askOnce, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var resolved *agentv1.QuestionResolved
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetQuestion() != nil:
			if err := h.answer(&agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: ev.GetQuestion().RequestId,
				Outcome: agentv1.QuestionOutcome_QUESTION_OUTCOME_SKIPPED}); err != nil {
				t.Fatal(err)
			}
		case ev.GetQuestionResolved() != nil:
			resolved = ev.GetQuestionResolved()
		}
		return isTurnFinished(ev)
	})
	if resolved.Outcome != agentv1.QuestionOutcome_QUESTION_OUTCOME_SKIPPED {
		t.Fatalf("resolved %+v", resolved)
	}
	if tr := lastResultText(h.gw.Requests()[1]); !strings.Contains(tr, "Decide yourself") {
		t.Fatalf("result %q", tr)
	}
}

// Stop cancels the question; the turn ends interrupted.
func TestQuestionCancelledOnStop(t *testing.T) {
	h := startHarness(t, askOnce, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var resolved *agentv1.QuestionResolved
	var reqID string
	got := h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetQuestion() != nil:
			reqID = ev.GetQuestion().RequestId
			h.interrupt(sid)
		case ev.GetQuestionResolved() != nil:
			resolved = ev.GetQuestionResolved()
		}
		return isTurnFinished(ev)
	})
	if resolved == nil || resolved.Outcome != agentv1.QuestionOutcome_QUESTION_OUTCOME_CANCELLED || resolved.ResolvedBy != "interrupt" {
		t.Fatalf("resolved %+v", resolved)
	}
	if r := got[len(got)-1].GetTurnFinished().Result; r != agentv1.TurnFinished_RESULT_INTERRUPTED {
		t.Fatalf("turn %v", r)
	}
	if err := h.answer(&agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: reqID, Outcome: agentv1.QuestionOutcome_QUESTION_OUTCOME_SKIPPED}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("answer after cancel: %v", err)
	}
}

// A frontend that reconnects (tab closed and reopened) gets the open
// question from the event log, still waiting, and can answer it.
func TestQuestionSurvivesResubscribe(t *testing.T) {
	h := startHarness(t, askOnce, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetQuestion() != nil })
	ch2 := h.subscribe(sid)
	var reqID string
	needsInput := false
	h.until(ch2, func(ev *agentv1.SessionEvent) bool {
		if ev.GetQuestionResolved() != nil {
			t.Fatal("question resolved before the answer")
		}
		if ev.GetSessionUpdated() != nil {
			needsInput = ev.GetSessionUpdated().Status == agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT
		}
		if ev.GetQuestion() != nil {
			reqID = ev.GetQuestion().RequestId
		}
		return reqID != ""
	})
	if !needsInput {
		t.Fatal("replayed log does not show the session waiting for input")
	}
	if err := h.answer(&agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: reqID, Outcome: agentv1.QuestionOutcome_QUESTION_OUTCOME_SKIPPED}); err != nil {
		t.Fatal(err)
	}
	h.until(ch2, isTurnFinished)
}
