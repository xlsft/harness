package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Advisor consults a second model about the session: it reads a copy of the conversation, has no tools and answers once.
// The session's main model decides when to ask and what to do with the answer. Ask is set by the session; the tool is left
// out of the session's list when no advisor model is chosen (bootstrap).
type Advisor struct {
	Ask func(ctx context.Context, question string) (string, error)
}

func (*Advisor) Name() string { return "Advisor" }

// ReadOnly: it changes nothing here, though the conversation so far goes to the advisor model's provider.
func (*Advisor) ReadOnly(json.RawMessage) bool { return true }

func (t *Advisor) Run(ctx context.Context, _ *Env, call Call) Result {
	var in struct {
		Question string `json:"question"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%s", err.Error())
	}
	question := strings.TrimSpace(in.Question)
	if question == "" {
		return ErrorResult("InputValidationError: question is empty")
	}
	if t.Ask == nil {
		return ErrorResult("no advisor model is available")
	}
	answer, err := t.Ask(ctx, question)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return ErrorResult("the question to the advisor was cancelled")
		}
		return ErrorResult("the advisor did not answer: %s", err.Error())
	}
	if strings.TrimSpace(answer) == "" {
		return ErrorResult("the advisor gave an empty answer")
	}
	return TextResult(answer, nil)
}
