package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/xlsft/harness/packages/client/llm"
)

// ---- AskUserQuestion ----

// Question types.
const (
	QuestionSingle = "single"
	QuestionMulti  = "multi"
	QuestionText   = "text"
	QuestionYesNo  = "yes_no"
)

// Labels of the implicit options of a yes_no question.
const (
	YesLabel = "Yes"
	NoLabel  = "No"
)

// Limits of an AskUserQuestion call.
const (
	maxQuestions = 4
	minOptions   = 2
	maxOptions   = 6
	maxHeaderLen = 12
)

// QuestionOption is one choice of a question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Recommended options are shown first and highlighted.
	Recommended bool `json:"recommended,omitempty"`
}

// Question is one AskUserQuestion item. NormalizeQuestions fills the
// defaults: after it Type is set, yes_no carries its two options, Min / Max
// are set for multi and AllowOther is set.
type Question struct {
	Question string           `json:"question"`
	Header   string           `json:"header"`
	Type     string           `json:"type,omitempty"`
	Options  []QuestionOption `json:"options,omitempty"`
	// Min / Max bound the number of choices of a multi question ("Other"
	// counts as one).
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
	// AllowOther offers a free-text "Other" answer (default true for
	// questions with options).
	AllowOther *bool `json:"allowOther,omitempty"`
	// MultiSelect is the input of transcripts written before Type existed.
	MultiSelect bool `json:"multiSelect,omitempty"`
}

// Other reports whether the question accepts a free-text "Other" answer.
func (q Question) Other() bool { return q.AllowOther != nil && *q.AllowOther }

// Bounds returns the number of choices a multi question accepts.
func (q Question) Bounds() (lo, hi int) {
	hi = len(q.Options)
	if q.Other() {
		hi++
	}
	if q.Min != nil {
		lo = *q.Min
	}
	if q.Max != nil {
		hi = *q.Max
	}
	return lo, hi
}

// QuestionAnswer is the user's answer to one question.
type QuestionAnswer struct {
	// Selected are the labels of the chosen options.
	Selected []string `json:"selected,omitempty"`
	// Text is the "Other" answer, or the answer to a text question.
	Text string `json:"text,omitempty"`
	// Skipped: the user left the question unanswered.
	Skipped bool `json:"skipped,omitempty"`
}

// QuestionOutcome is how the user resolved a question card.
type QuestionOutcome string

// Question outcomes.
const (
	// QuestionAnswered: Answers has one entry per question.
	QuestionAnswered QuestionOutcome = "answered"
	// QuestionSkipped: the user skipped the whole card.
	QuestionSkipped QuestionOutcome = "skipped"
	// QuestionMessage: the user's chat message(s) are the free-form answer.
	QuestionMessage QuestionOutcome = "message"
)

// QuestionResponse is the answer to Interactor.AskQuestions.
type QuestionResponse struct {
	Outcome QuestionOutcome
	Answers []QuestionAnswer
	// Message is the content of the chat message(s) for QuestionMessage.
	Message []llm.ContentBlock
}

// MessageText returns the text of Message.
func (r QuestionResponse) MessageText() string {
	var parts []string
	for _, b := range r.Message {
		if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

// AskUserQuestion asks the user 1–4 questions and blocks until they answer.
type AskUserQuestion struct{}

func (*AskUserQuestion) Name() string                  { return "AskUserQuestion" }
func (*AskUserQuestion) ReadOnly(json.RawMessage) bool { return true }

// NormalizeQuestions validates questions and fills their defaults (see
// Question). It also accepts the older input without type (multiSelect).
func NormalizeQuestions(qs []Question) ([]Question, error) {
	if len(qs) < 1 || len(qs) > maxQuestions {
		return nil, fmt.Errorf("questions must contain 1 to %d items, got %d", maxQuestions, len(qs))
	}
	out := make([]Question, len(qs))
	seen := map[string]bool{}
	for i, q := range qs {
		if strings.TrimSpace(q.Question) == "" {
			return nil, fmt.Errorf("questions[%d].question is required", i)
		}
		if strings.TrimSpace(q.Header) == "" {
			return nil, fmt.Errorf("questions[%d].header is required", i)
		}
		if utf8.RuneCountInString(q.Header) > maxHeaderLen {
			return nil, fmt.Errorf("questions[%d].header must be at most %d characters", i, maxHeaderLen)
		}
		if seen[q.Question] {
			return nil, fmt.Errorf("question texts must be unique: %q", q.Question)
		}
		seen[q.Question] = true
		n, err := normalizeQuestion(q)
		if err != nil {
			return nil, fmt.Errorf("questions[%d]: %v", i, err)
		}
		out[i] = n
	}
	return out, nil
}

func normalizeQuestion(q Question) (Question, error) {
	q.Options = append([]QuestionOption(nil), q.Options...)
	if q.Type == "" {
		switch {
		case q.MultiSelect:
			q.Type = QuestionMulti
		case len(q.Options) == 0:
			q.Type = QuestionText
		default:
			q.Type = QuestionSingle
		}
	}
	q.MultiSelect = false
	switch q.Type {
	case QuestionText:
		if len(q.Options) > 0 {
			return q, errors.New("a text question has no options")
		}
		if q.Min != nil || q.Max != nil {
			return q, errors.New("min and max apply only to multi questions")
		}
		q.AllowOther = boolPtr(false)
		return q, nil
	case QuestionYesNo:
		if len(q.Options) > 0 && !isYesNo(q.Options) {
			return q, fmt.Errorf("a yes_no question has no options (they are %q and %q)", YesLabel, NoLabel)
		}
		if q.Min != nil || q.Max != nil {
			return q, errors.New("min and max apply only to multi questions")
		}
		q.Options = []QuestionOption{{Label: YesLabel}, {Label: NoLabel}}
	case QuestionSingle, QuestionMulti:
		if len(q.Options) < minOptions || len(q.Options) > maxOptions {
			return q, fmt.Errorf("options must contain %d to %d items, got %d", minOptions, maxOptions, len(q.Options))
		}
		if q.Type == QuestionSingle && (q.Min != nil || q.Max != nil) {
			return q, errors.New("min and max apply only to multi questions")
		}
	default:
		return q, fmt.Errorf("type must be single, multi, text or yes_no, got %q", q.Type)
	}
	labels := map[string]bool{}
	recommended := 0
	for j, o := range q.Options {
		if strings.TrimSpace(o.Label) == "" {
			return q, fmt.Errorf("options[%d].label is required", j)
		}
		if labels[o.Label] {
			return q, fmt.Errorf("option labels must be unique within a question: %q", o.Label)
		}
		labels[o.Label] = true
		if o.Recommended {
			recommended++
		}
	}
	if q.Type == QuestionSingle && recommended > 1 {
		return q, errors.New("a single question can recommend at most one option")
	}
	if q.AllowOther == nil {
		q.AllowOther = boolPtr(true)
	}
	if q.Type == QuestionMulti {
		lo, hi := q.Bounds()
		limit := len(q.Options)
		if q.Other() {
			limit++
		}
		if lo < 0 || hi < 1 || lo > hi || hi > limit {
			return q, fmt.Errorf("min and max must satisfy 0 <= min <= max, 1 <= max <= %d (the options, plus one for Other)", limit)
		}
		q.Min, q.Max = &lo, &hi
	}
	return q, nil
}

// ValidateAnswers checks answers against normalized questions: one answer
// per question; chosen labels exist; single and yes_no take one choice,
// multi between min and max ("Other" counting as one); text questions take
// text only; skipped answers are empty.
func ValidateAnswers(qs []Question, answers []QuestionAnswer) error {
	if len(answers) != len(qs) {
		return fmt.Errorf("expected %d answers, got %d", len(qs), len(answers))
	}
	for i, q := range qs {
		a := answers[i]
		text := strings.TrimSpace(a.Text)
		if a.Skipped {
			if len(a.Selected) > 0 || text != "" {
				return fmt.Errorf("answers[%d]: a skipped answer has no choices or text", i)
			}
			continue
		}
		if q.Type == QuestionText {
			if len(a.Selected) > 0 {
				return fmt.Errorf("answers[%d]: a text question has no options", i)
			}
			if text == "" {
				return fmt.Errorf("answers[%d]: text is empty (mark the question skipped instead)", i)
			}
			continue
		}
		if text != "" && !q.Other() {
			return fmt.Errorf("answers[%d]: this question does not accept a free-text answer", i)
		}
		labels := map[string]bool{}
		for _, o := range q.Options {
			labels[o.Label] = true
		}
		picked := map[string]bool{}
		for _, l := range a.Selected {
			if !labels[l] {
				return fmt.Errorf("answers[%d]: %q is not an option", i, l)
			}
			if picked[l] {
				return fmt.Errorf("answers[%d]: %q is chosen twice", i, l)
			}
			picked[l] = true
		}
		n := len(a.Selected)
		if text != "" {
			n++
		}
		if q.Type == QuestionMulti {
			lo, hi := q.Bounds()
			if n < lo || n > hi {
				return fmt.Errorf("answers[%d]: choose between %d and %d, got %d", i, lo, hi, n)
			}
			continue
		}
		if n != 1 {
			return fmt.Errorf("answers[%d]: choose exactly one, got %d", i, n)
		}
	}
	return nil
}

// answerEntry is one answer as the model sees it.
type answerEntry struct {
	Question string `json:"question"`
	// Selected is set (possibly empty) for answered questions with options.
	Selected *[]string `json:"selected,omitempty"`
	Other    string    `json:"other,omitempty"`
	Text     string    `json:"text,omitempty"`
	Skipped  bool      `json:"skipped,omitempty"`
}

func answerEntries(qs []Question, answers []QuestionAnswer) ([]answerEntry, bool) {
	out := make([]answerEntry, len(qs))
	skipped := false
	for i, q := range qs {
		e := answerEntry{Question: q.Question}
		var a QuestionAnswer
		if i < len(answers) {
			a = answers[i]
		} else {
			a.Skipped = true
		}
		text := strings.TrimSpace(a.Text)
		switch {
		case a.Skipped:
			e.Skipped, skipped = true, true
		case q.Type == QuestionText:
			e.Text = text
		default:
			sel := append([]string{}, a.Selected...)
			e.Selected = &sel
			e.Other = text
		}
		out[i] = e
	}
	return out, skipped
}

func compactJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(buf.String())
}

// QuestionResult renders the tool result for a response to normalized
// questions: the answers as JSON (answered), a hand-back of the decision
// (skipped) or the chat message as the free-form answer.
func QuestionResult(qs []Question, resp QuestionResponse) Result {
	structured := map[string]any{"questions": qs, "outcome": resp.Outcome}
	var r Result
	switch resp.Outcome {
	case QuestionSkipped:
		r = TextResult("The user skipped your questions without answering. Decide yourself: pick sensible defaults, state the assumptions you made, and continue.", structured)
		r.Summary = "Questions skipped"
	case QuestionMessage:
		msg := resp.MessageText()
		structured["message"] = msg
		r = TextResult("The user did not use the question card and replied with a chat message instead. Treat it as their free-form answer to your questions:\n\n"+msg, structured)
		for _, b := range resp.Message {
			if b.Type != llm.BlockText {
				r.Content = append(r.Content, b)
			}
		}
		r.Summary = "Answered with a message"
	default:
		entries, skipped := answerEntries(qs, resp.Answers)
		answers := make([]QuestionAnswer, len(qs))
		copy(answers, resp.Answers)
		structured["outcome"] = QuestionAnswered
		structured["answers"] = answers
		var sb strings.Builder
		sb.WriteString("The user answered your questions (one entry per question, in order):\n")
		sb.WriteString(compactJSON(entries))
		if skipped {
			sb.WriteString("\nThe user left the questions marked skipped unanswered: decide them yourself and state what you chose.")
		}
		sb.WriteString("\nContinue the task with these answers.")
		r = TextResult(sb.String(), structured)
		r.Summary = fmt.Sprintf("Answered %d question(s)", len(qs))
	}
	return r
}

func (*AskUserQuestion) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Questions []Question `json:"questions"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	qs, err := NormalizeQuestions(in.Questions)
	if err != nil {
		return ErrorResult("InputValidationError: %v", err)
	}
	var ia Interactor
	if h := env.Host(); h != nil {
		ia = h.Interactor()
	}
	if ia == nil {
		return ErrorResult("AskUserQuestion is not available: this session is non-interactive. Make a reasonable assumption, state it explicitly, and continue.")
	}
	resp, err := ia.AskQuestions(ctx, call.ID, qs)
	if errors.Is(err, ErrNonInteractive) {
		return ErrorResult("AskUserQuestion is not available: no user is attached to this session. Make a reasonable assumption, state it explicitly, and continue.")
	}
	if err != nil {
		return ErrorResult("question was not answered: %v", err)
	}
	return QuestionResult(qs, resp)
}

func boolPtr(v bool) *bool { return &v }

// isYesNo reports whether options are the implicit yes_no options (a
// normalized yes_no question).
func isYesNo(opts []QuestionOption) bool {
	return len(opts) == 2 && opts[0].Label == YesLabel && opts[1].Label == NoLabel
}
