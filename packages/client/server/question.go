package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/tools"
)

// The question flow (AskUserQuestion):
//
//   - AskQuestions publishes Question and blocks; the session is NEEDS_INPUT.
//   - Chat messages sent meanwhile are queued as usual (even with
//     DELIVERY_NOW) and do not end the wait. After the answer they are
//     injected right after the tool result, in order.
//   - AnswerQuestion resolves it with answers, a skip, or the queued chat
//     message(s) as the free-form answer (they leave the queue).
//   - Interrupt (Stop) cancels it; the turn then ends as interrupted.
//   - Frontends that reconnect get the Question from the hub log and see it
//     still waiting. After a client restart the asking turn is gone, so the
//     replay shows the question cancelled, like every tool call left without
//     a result (see replayer.questionCard).

type pendingQuestion struct {
	ch chan tools.QuestionResponse
	qs []tools.Question
}

// questionPending reports whether a question is waiting for the user.
func (ls *liveSession) questionPending() bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return len(ls.pendingQ) > 0
}

var questionTypes = map[string]agentv1.QuestionItem_Type{
	tools.QuestionSingle: agentv1.QuestionItem_TYPE_SINGLE,
	tools.QuestionMulti:  agentv1.QuestionItem_TYPE_MULTI,
	tools.QuestionText:   agentv1.QuestionItem_TYPE_TEXT,
	tools.QuestionYesNo:  agentv1.QuestionItem_TYPE_YES_NO,
}

func questionToProto(id, toolUseID string, qs []tools.Question) *agentv1.Question {
	q := &agentv1.Question{RequestId: id, ToolUseId: toolUseID}
	for _, it := range qs {
		item := &agentv1.QuestionItem{Question: it.Question, Header: it.Header, Type: questionTypes[it.Type],
			MultiSelect: it.Type == tools.QuestionMulti, AllowOther: it.Other()}
		if it.Type == tools.QuestionMulti {
			lo, hi := it.Bounds()
			item.Min, item.Max = int32(lo), int32(hi)
		}
		for _, o := range it.Options {
			item.Options = append(item.Options, &agentv1.QuestionOption{Label: o.Label, Description: o.Description, Recommended: o.Recommended})
		}
		q.Items = append(q.Items, item)
	}
	return q
}

func answersToProto(in []tools.QuestionAnswer) []*agentv1.QuestionAnswer {
	out := make([]*agentv1.QuestionAnswer, len(in))
	for i, a := range in {
		out[i] = &agentv1.QuestionAnswer{Selected: a.Selected, Text: a.Text, Skipped: a.Skipped}
	}
	return out
}

func answersFromProto(in []*agentv1.QuestionAnswer) []tools.QuestionAnswer {
	out := make([]tools.QuestionAnswer, len(in))
	for i, a := range in {
		out[i] = tools.QuestionAnswer{Selected: a.GetSelected(), Text: a.GetText(), Skipped: a.GetSkipped()}
	}
	return out
}

// questionResolved builds the QuestionResolved event for a response.
func questionResolved(id, by string, resp tools.QuestionResponse) *agentv1.SessionEvent {
	qr := &agentv1.QuestionResolved{RequestId: id, ResolvedBy: by}
	switch resp.Outcome {
	case tools.QuestionAnswered:
		qr.Outcome = agentv1.QuestionOutcome_QUESTION_OUTCOME_ANSWERED
		qr.Answers = answersToProto(resp.Answers)
	case tools.QuestionSkipped:
		qr.Outcome = agentv1.QuestionOutcome_QUESTION_OUTCOME_SKIPPED
	case tools.QuestionMessage:
		qr.Outcome = agentv1.QuestionOutcome_QUESTION_OUTCOME_MESSAGE
		qr.Message = resp.MessageText()
	default:
		qr.Outcome = agentv1.QuestionOutcome_QUESTION_OUTCOME_CANCELLED
	}
	return &agentv1.SessionEvent{Event: &agentv1.SessionEvent_QuestionResolved{QuestionResolved: qr}}
}

// AskQuestions implements tools.Interactor (AskUserQuestion).
func (ls *liveSession) AskQuestions(ctx context.Context, toolUseID string, qs []tools.Question) (tools.QuestionResponse, error) {
	id := newUUID("q_")
	p := &pendingQuestion{ch: make(chan tools.QuestionResponse, 1), qs: qs}
	ls.mu.Lock()
	ls.pendingQ[id] = p
	ls.mu.Unlock()
	defer func() {
		ls.mu.Lock()
		delete(ls.pendingQ, id)
		ls.mu.Unlock()
	}()
	ls.beginWait()
	defer ls.endWait()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_Question{Question: questionToProto(id, toolUseID, qs)}})
	select {
	case <-ctx.Done():
		ls.publish(questionResolved(id, "interrupt", tools.QuestionResponse{}))
		return tools.QuestionResponse{}, ctx.Err()
	case resp := <-p.ch:
		ls.publish(questionResolved(id, "local", resp))
		return resp, nil
	}
}

func (s *Server) AnswerQuestion(ctx context.Context, req *connect.Request[agentv1.AnswerQuestionRequest]) (*connect.Response[agentv1.AnswerQuestionResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetRequestId()
	ls.mu.Lock()
	p := ls.pendingQ[id]
	ls.mu.Unlock()
	if p == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending question %s", id))
	}
	var resp tools.QuestionResponse
	switch req.Msg.GetOutcome() {
	case agentv1.QuestionOutcome_QUESTION_OUTCOME_UNSPECIFIED, agentv1.QuestionOutcome_QUESTION_OUTCOME_ANSWERED:
		resp = tools.QuestionResponse{Outcome: tools.QuestionAnswered, Answers: answersFromProto(req.Msg.GetAnswers())}
		if err := tools.ValidateAnswers(p.qs, resp.Answers); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	case agentv1.QuestionOutcome_QUESTION_OUTCOME_SKIPPED:
		resp = tools.QuestionResponse{Outcome: tools.QuestionSkipped}
	case agentv1.QuestionOutcome_QUESTION_OUTCOME_MESSAGE:
		resp = tools.QuestionResponse{Outcome: tools.QuestionMessage}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("outcome must be answered, skipped or message (use Interrupt to cancel)"))
	}
	// Claim the question; another frontend may have answered it meanwhile.
	ls.mu.Lock()
	if ls.pendingQ[id] != p {
		ls.mu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending question %s", id))
	}
	delete(ls.pendingQ, id)
	ls.mu.Unlock()
	if resp.Outcome == tools.QuestionMessage {
		resp.Message = ls.takeQueued(req.Msg.GetMessageUuids())
		if len(resp.Message) == 0 {
			ls.mu.Lock()
			ls.pendingQ[id] = p
			ls.mu.Unlock()
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no queued message to answer with"))
		}
	}
	p.ch <- resp
	return connect.NewResponse(&agentv1.AnswerQuestionResponse{}), nil
}

// takeQueued removes the queued messages with the given uuids (all queued
// messages when empty) and returns their content in queue order, messages
// separated so that their texts stay apart.
func (ls *liveSession) takeQueued(uuids []string) []llm.ContentBlock {
	ls.mu.Lock()
	want := map[string]bool{}
	for _, u := range uuids {
		want[u] = true
	}
	var picked []*agentv1.UserMessage
	for _, m := range ls.queue {
		if len(uuids) == 0 || want[m.GetUuid()] {
			picked = append(picked, m)
		}
	}
	ls.mu.Unlock()
	var out []llm.ContentBlock
	taken := map[string]bool{}
	for _, m := range picked {
		// A message already injected into the conversation stays there.
		if !ls.sess.Agent.RemoveQueued(m.GetUuid()) {
			continue
		}
		taken[m.GetUuid()] = true
		out = append(out, pbconv.FromBlocks(m.GetContent())...)
	}
	if len(taken) == 0 {
		return nil
	}
	ls.mu.Lock()
	kept := ls.queue[:0:0]
	for _, m := range ls.queue {
		if !taken[m.GetUuid()] {
			kept = append(kept, m)
		}
	}
	ls.queue = kept
	q := append([]*agentv1.UserMessage(nil), ls.queue...)
	ls.mu.Unlock()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{Queued: q}}})
	return out
}

// questionCard rebuilds the question card of a replayed AskUserQuestion
// call: the card as asked plus its outcome. structured is the recorded
// toolUseResult; cancelled marks a call that was interrupted or never got a
// result (the process stopped while the question was open). It returns nil
// when there is no card to show (invalid input, non-interactive error).
func questionCard(toolUseID string, input json.RawMessage, structured any, cancelled bool) []*agentv1.SessionEvent {
	sm, _ := structured.(map[string]any)
	var qs []tools.Question
	if raw, ok := sm["questions"]; ok && !cancelled {
		// Normalized questions recorded with the result.
		if b, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(b, &qs)
		}
	}
	if len(qs) == 0 {
		var in struct {
			Questions []tools.Question `json:"questions"`
		}
		if json.Unmarshal(input, &in) != nil {
			return nil
		}
		qs = in.Questions
	}
	qs, err := tools.NormalizeQuestions(qs)
	if err != nil {
		return nil
	}
	resp := tools.QuestionResponse{}
	if !cancelled {
		outcome, _ := sm["outcome"].(string)
		switch tools.QuestionOutcome(outcome) {
		case tools.QuestionSkipped:
			resp.Outcome = tools.QuestionSkipped
		case tools.QuestionMessage:
			resp.Outcome = tools.QuestionMessage
			msg, _ := sm["message"].(string)
			resp.Message = []llm.ContentBlock{llm.TextBlock(msg)}
		default:
			switch a := sm["answers"].(type) {
			case []any:
				resp.Outcome = tools.QuestionAnswered
				if b, err := json.Marshal(a); err == nil {
					_ = json.Unmarshal(b, &resp.Answers)
				}
			case map[string]any:
				// Older transcripts: question text -> comma-joined answer.
				resp.Outcome = tools.QuestionAnswered
				for _, q := range qs {
					text, _ := a[q.Question].(string)
					resp.Answers = append(resp.Answers, tools.QuestionAnswer{Text: text, Skipped: text == ""})
				}
			default:
				return nil // never asked (non-interactive, invalid input)
			}
		}
	}
	id := "q_history_" + toolUseID
	return []*agentv1.SessionEvent{
		{Event: &agentv1.SessionEvent_Question{Question: questionToProto(id, toolUseID, qs)}},
		questionResolved(id, "history", resp),
	}
}
