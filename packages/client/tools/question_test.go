package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
)

func opts(labels ...string) []any {
	var out []any
	for _, l := range labels {
		out = append(out, map[string]any{"label": l, "description": "about " + l})
	}
	return out
}

func TestAskUserQuestionHeadless(t *testing.T) {
	env := newEnv(t)
	q := map[string]any{"questions": []any{map[string]any{"question": "Which DB?", "header": "DB", "type": "single", "options": opts("Postgres", "SQLite")}}}
	if r := call(t, env, &AskUserQuestion{}, q); !r.IsError || !strings.Contains(text(r), "non-interactive") {
		t.Fatalf("headless: %q", text(r))
	}
}

func TestNormalizeQuestions(t *testing.T) {
	ok := []Question{
		{Question: "DB?", Header: "DB", Type: QuestionSingle, Options: []QuestionOption{{Label: "A"}, {Label: "B", Recommended: true}}},
		{Question: "Features?", Header: "Features", Type: QuestionMulti, Options: []QuestionOption{{Label: "X"}, {Label: "Y"}, {Label: "Z"}}},
		{Question: "Name?", Header: "Name", Type: QuestionText},
		{Question: "Proceed?", Header: "Go", Type: QuestionYesNo},
	}
	qs, err := NormalizeQuestions(ok)
	if err != nil {
		t.Fatal(err)
	}
	if !qs[0].Other() || qs[2].Other() || !qs[3].Other() {
		t.Fatalf("allowOther defaults: %+v", qs)
	}
	if lo, hi := qs[1].Bounds(); lo != 0 || hi != 4 || *qs[1].Min != 0 || *qs[1].Max != 4 {
		t.Fatalf("multi bounds %d..%d", lo, hi)
	}
	if len(qs[3].Options) != 2 || qs[3].Options[0].Label != YesLabel || qs[3].Options[1].Label != NoLabel {
		t.Fatalf("yes_no options %+v", qs[3].Options)
	}
	// Normalizing twice is a no-op (replay re-normalizes recorded questions).
	again, err := NormalizeQuestions(qs)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(qs)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatalf("not idempotent:\n%s\n%s", a, b)
	}
	// Older input: multiSelect without type.
	legacy, err := NormalizeQuestions([]Question{{Question: "Q?", Header: "H", MultiSelect: true, Options: []QuestionOption{{Label: "a"}, {Label: "b"}}}})
	if err != nil || legacy[0].Type != QuestionMulti {
		t.Fatalf("legacy: %+v %v", legacy, err)
	}

	n := func(v int) *int { return &v }
	f := false
	bad := map[string]Question{
		"1 to 4":                 {},
		"header is required":     {Question: "Q?", Type: QuestionText},
		"at most 12":             {Question: "Q?", Header: "a very long header", Type: QuestionText},
		"2 to 6":                 {Question: "Q?", Header: "H", Type: QuestionSingle, Options: []QuestionOption{{Label: "only"}}},
		"text question has no":   {Question: "Q?", Header: "H", Type: QuestionText, Options: []QuestionOption{{Label: "a"}, {Label: "b"}}},
		"yes_no question has no": {Question: "Q?", Header: "H", Type: QuestionYesNo, Options: []QuestionOption{{Label: "Sure"}, {Label: "Nope"}}},
		"type must be":           {Question: "Q?", Header: "H", Type: "rating"},
		"min and max apply":      {Question: "Q?", Header: "H", Type: QuestionSingle, Max: n(1), Options: []QuestionOption{{Label: "a"}, {Label: "b"}}},
		"0 <= min <= max":        {Question: "Q?", Header: "H", Type: QuestionMulti, Min: n(3), Max: n(2), Options: []QuestionOption{{Label: "a"}, {Label: "b"}, {Label: "c"}}},
		"1 <= max <= 2":          {Question: "Q?", Header: "H", Type: QuestionMulti, Max: n(3), AllowOther: &f, Options: []QuestionOption{{Label: "a"}, {Label: "b"}}},
		"must be unique within":  {Question: "Q?", Header: "H", Type: QuestionSingle, Options: []QuestionOption{{Label: "a"}, {Label: "a"}}},
		"at most one option":     {Question: "Q?", Header: "H", Type: QuestionSingle, Options: []QuestionOption{{Label: "a", Recommended: true}, {Label: "b", Recommended: true}}},
	}
	for want, q := range bad {
		in := []Question{q}
		if want == "1 to 4" {
			in = nil
		}
		if _, err := NormalizeQuestions(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	if _, err := NormalizeQuestions([]Question{{Question: "Q?", Header: "H", Type: QuestionText}, {Question: "Q?", Header: "H2", Type: QuestionText}}); err == nil {
		t.Error("duplicate question texts accepted")
	}
}

func TestValidateAnswers(t *testing.T) {
	n := func(v int) *int { return &v }
	f := false
	qs, err := NormalizeQuestions([]Question{
		{Question: "DB?", Header: "DB", Type: QuestionSingle, Options: []QuestionOption{{Label: "A"}, {Label: "B"}}},
		{Question: "Features?", Header: "F", Type: QuestionMulti, Min: n(1), Max: n(2), Options: []QuestionOption{{Label: "X"}, {Label: "Y"}, {Label: "Z"}}},
		{Question: "Name?", Header: "Name", Type: QuestionText},
		{Question: "Go?", Header: "Go", Type: QuestionYesNo, AllowOther: &f},
	})
	if err != nil {
		t.Fatal(err)
	}
	good := [][]QuestionAnswer{
		{{Selected: []string{"A"}}, {Selected: []string{"X", "Y"}}, {Text: "foo"}, {Selected: []string{"Yes"}}},
		{{Text: "MySQL"}, {Selected: []string{"X"}, Text: "W"}, {Skipped: true}, {Skipped: true}},
		{{Skipped: true}, {Skipped: true}, {Skipped: true}, {Selected: []string{"No"}}},
	}
	for i, a := range good {
		if err := ValidateAnswers(qs, a); err != nil {
			t.Errorf("good %d: %v", i, err)
		}
	}
	bad := map[string][]QuestionAnswer{
		"expected 4":         {{Skipped: true}},
		"not an option":      {{Selected: []string{"C"}}, {Skipped: true}, {Skipped: true}, {Skipped: true}},
		"exactly one":        {{Selected: []string{"A", "B"}}, {Skipped: true}, {Skipped: true}, {Skipped: true}},
		"between 1 and 2":    {{Skipped: true}, {Selected: []string{"X", "Y"}, Text: "W"}, {Skipped: true}, {Skipped: true}},
		"chosen twice":       {{Skipped: true}, {Selected: []string{"X", "X"}}, {Skipped: true}, {Skipped: true}},
		"text is empty":      {{Skipped: true}, {Skipped: true}, {Text: "  "}, {Skipped: true}},
		"has no options":     {{Skipped: true}, {Skipped: true}, {Selected: []string{"A"}}, {Skipped: true}},
		"free-text":          {{Skipped: true}, {Skipped: true}, {Skipped: true}, {Text: "maybe"}},
		"skipped answer has": {{Skipped: true, Selected: []string{"A"}}, {Skipped: true}, {Skipped: true}, {Skipped: true}},
	}
	for want, a := range bad {
		if err := ValidateAnswers(qs, a); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
}

func askEnv(t *testing.T, resp QuestionResponse) (*Env, *fakeIA) {
	env := newEnv(t)
	ia := &fakeIA{resp: resp}
	env.SetHost(&fakeHost{ia: ia})
	return env, ia
}

var twoQuestions = map[string]any{"questions": []any{
	map[string]any{"question": "Which DB?", "header": "DB", "type": "single", "options": []any{
		map[string]any{"label": "Postgres", "recommended": true}, map[string]any{"label": "SQLite"}}},
	map[string]any{"question": "Which features?", "header": "Features", "type": "multi", "max": 2, "options": opts("Auth", "Billing", "Search")},
	map[string]any{"question": "Project name?", "header": "Name", "type": "text"},
}}

func TestAskUserQuestionAnswered(t *testing.T) {
	env, ia := askEnv(t, QuestionResponse{Outcome: QuestionAnswered, Answers: []QuestionAnswer{
		{Selected: []string{"SQLite"}}, {Selected: []string{"Auth"}, Text: "audit <log>"}, {Skipped: true}}})
	r := call(t, env, &AskUserQuestion{}, twoQuestions)
	if r.IsError {
		t.Fatalf("error: %q", text(r))
	}
	if len(ia.asked) != 3 || !ia.asked[0].Options[0].Recommended || ia.asked[1].Type != QuestionMulti || *ia.asked[1].Max != 2 {
		t.Fatalf("asked %+v", ia.asked)
	}
	got := text(r)
	want := `[{"question":"Which DB?","selected":["SQLite"]},{"question":"Which features?","selected":["Auth"],"other":"audit <log>"},{"question":"Project name?","skipped":true}]`
	if !strings.Contains(got, want) {
		t.Fatalf("result:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(got, "skipped unanswered: decide them yourself") {
		t.Fatalf("no skip hint: %s", got)
	}
	sm := r.Structured.(map[string]any)
	if sm["outcome"] != QuestionAnswered || len(sm["answers"].([]QuestionAnswer)) != 3 || len(sm["questions"].([]Question)) != 3 {
		t.Fatalf("structured %+v", sm)
	}
	if r.Summary != "Answered 3 question(s)" {
		t.Fatalf("summary %q", r.Summary)
	}
}

func TestAskUserQuestionSkipped(t *testing.T) {
	env, _ := askEnv(t, QuestionResponse{Outcome: QuestionSkipped})
	r := call(t, env, &AskUserQuestion{}, twoQuestions)
	if r.IsError || !strings.Contains(text(r), "skipped your questions") || !strings.Contains(text(r), "Decide yourself") {
		t.Fatalf("skip: %q", text(r))
	}
	if r.Structured.(map[string]any)["outcome"] != QuestionSkipped {
		t.Fatalf("structured %+v", r.Structured)
	}
}

func TestAskUserQuestionWithMessage(t *testing.T) {
	img := llm.ContentBlock{Type: llm.BlockImage}
	env, _ := askEnv(t, QuestionResponse{Outcome: QuestionMessage, Message: []llm.ContentBlock{llm.TextBlock("use sqlite"), llm.TextBlock("and call it demo"), img}})
	r := call(t, env, &AskUserQuestion{}, twoQuestions)
	if r.IsError || !strings.Contains(text(r), "free-form answer") || !strings.HasSuffix(text(r), "use sqlite\n\nand call it demo") {
		t.Fatalf("message: %q", text(r))
	}
	if len(r.Content) != 2 || r.Content[1].Type != llm.BlockImage {
		t.Fatalf("attachments not passed: %+v", r.Content)
	}
	if sm := r.Structured.(map[string]any); sm["outcome"] != QuestionMessage || sm["message"] != "use sqlite\n\nand call it demo" {
		t.Fatalf("structured %+v", sm)
	}
}

func TestAskUserQuestionInvalidInput(t *testing.T) {
	env, ia := askEnv(t, QuestionResponse{})
	bad := map[string]any{"questions": []any{map[string]any{"question": "Q", "header": "H", "type": "single", "options": opts("only")}}}
	if r := call(t, env, &AskUserQuestion{}, bad); !r.IsError || !strings.Contains(text(r), "2 to 6") || ia.asked != nil {
		t.Fatalf("validation: %q", text(r))
	}
}
