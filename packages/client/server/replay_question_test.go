package server

import (
	"encoding/json"
	"fmt"
	"testing"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
)

// Question cards replay with their outcome; questions interrupted or left
// open when the process stopped replay as cancelled.
func TestReplayQuestionOutcomes(t *testing.T) {
	input := map[string]any{"questions": []map[string]any{
		{"question": "Which?", "header": "Pick", "type": "multi", "max": 2, "options": []map[string]any{{"label": "A", "recommended": true}, {"label": "B"}, {"label": "C"}}},
		{"question": "Name?", "header": "Name", "type": "text"},
	}}
	raw, _ := json.Marshal(input)
	var in struct {
		Questions []tools.Question `json:"questions"`
	}
	_ = json.Unmarshal(raw, &in)
	qs, err := tools.NormalizeQuestions(in.Questions)
	if err != nil {
		t.Fatal(err)
	}
	b := newTB()
	ask := func(n int, resp *tools.QuestionResponse) {
		p, id := fmt.Sprintf("p%d", n), fmt.Sprintf("q%d", n)
		b.prompt("ask", p, "default")
		b.assistant("msg_"+id, llm.Usage{}, toolUse(id, "AskUserQuestion", input))
		if resp != nil {
			r := tools.QuestionResult(qs, *resp)
			b.result(p, id, r.Content[0].Text, false, r.Structured)
			b.assistant("msg_after_"+id, llm.Usage{}, llm.TextBlock("ok"))
		}
	}
	ask(1, &tools.QuestionResponse{Outcome: tools.QuestionAnswered, Answers: []tools.QuestionAnswer{{Selected: []string{"A"}, Text: "D"}, {Skipped: true}}})
	ask(2, &tools.QuestionResponse{Outcome: tools.QuestionSkipped})
	ask(3, &tools.QuestionResponse{Outcome: tools.QuestionMessage, Message: []llm.ContentBlock{llm.TextBlock("just A")}})
	ask(4, nil)
	b.result("p4", "q4", testToolInterrupt, true, testToolInterrupt)
	b.add(session.Entry{Type: session.TypeUser, Message: session.UserText(testInterrupt), PromptID: "p4"})
	ask(5, nil) // the client stopped while the question was open
	evs := replayHistory(b.entries, replayOptions{Interrupts: []string{testInterrupt, testToolInterrupt}})

	var questions []*agentv1.Question
	var resolved []*agentv1.QuestionResolved
	for _, ev := range evs {
		if q := ev.GetQuestion(); q != nil {
			questions = append(questions, q)
		}
		if r := ev.GetQuestionResolved(); r != nil {
			resolved = append(resolved, r)
		}
	}
	if len(questions) != 5 || len(resolved) != 5 {
		t.Fatalf("cards: %s", kinds(evs))
	}
	it := questions[0].Items
	if it[0].Type != agentv1.QuestionItem_TYPE_MULTI || it[0].Max != 2 || !it[0].AllowOther || !it[0].Options[0].Recommended || it[1].Type != agentv1.QuestionItem_TYPE_TEXT {
		t.Fatalf("items %+v", it)
	}
	want := []agentv1.QuestionOutcome{
		agentv1.QuestionOutcome_QUESTION_OUTCOME_ANSWERED, agentv1.QuestionOutcome_QUESTION_OUTCOME_SKIPPED, agentv1.QuestionOutcome_QUESTION_OUTCOME_MESSAGE,
		agentv1.QuestionOutcome_QUESTION_OUTCOME_CANCELLED, agentv1.QuestionOutcome_QUESTION_OUTCOME_CANCELLED,
	}
	for i, r := range resolved {
		if r.Outcome != want[i] || r.ResolvedBy != "history" || r.RequestId != "q_history_"+questions[i].ToolUseId {
			t.Errorf("card %d: %+v", i, r)
		}
	}
	if a := resolved[0].Answers; len(a) != 2 || a[0].Selected[0] != "A" || a[0].Text != "D" || !a[1].Skipped {
		t.Fatalf("answers %+v", a)
	}
	if resolved[2].Message != "just A" {
		t.Fatalf("message %q", resolved[2].Message)
	}
	// The orphaned call still ends interrupted after its cancelled card.
	if got := kinds(evs[len(evs)-5:]); got != "start:q5 question answered end:q5 done:INTERRUPTED" {
		t.Fatalf("orphan tail: %s", got)
	}
}
