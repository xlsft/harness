package agent

import (
	"fmt"

	"github.com/xlsft/harness/packages/client/llm"
)

// What a reply cut off at the model's output limit (stop reason max_tokens) leads to.
const (
	outputLimitResume = iota + 1 // it wrote some of its answer: keep that and ask it to go on
	outputLimitFail              // nothing usable: end the turn with an error that says what to change
)

// maxOutputLimitResumes is how many times in one turn a reply cut off mid-answer is resumed before the turn fails.
const maxOutputLimitResumes = 3

// outputLimitResumeText asks the model to carry on after its reply hit the output limit. It is a meta message: the model
// reads it, the user does not.
const outputLimitResumeText = "Your previous reply reached the output token limit and was cut off. Continue exactly where it stopped, without repeating anything or apologising. If what is left is long, split it into smaller pieces, for example several tool calls."

// outputLimitAction decides what a reply that stopped at the output limit leads to. A reply with answer text and no tool
// call is resumed, up to maxOutputLimitResumes times a turn. A reply with only thinking has nothing to resume from, and a
// tool call cut off midway has incomplete input that must never run.
func (a *Agent) outputLimitAction(content []llm.ContentBlock, resumes *int) int {
	text := false
	for _, b := range content {
		switch b.Type {
		case llm.BlockToolUse:
			return outputLimitFail
		case llm.BlockText:
			if b.Text != "" {
				text = true
			}
		}
	}
	if !text || *resumes >= maxOutputLimitResumes {
		return outputLimitFail
	}
	*resumes++
	return outputLimitResume
}

// outputLimitError tells the user why the turn ended and what to change.
func outputLimitError(outputTokens int) string {
	limit := "its output limit"
	if outputTokens > 0 {
		limit = fmt.Sprintf("its output limit of %d tokens", outputTokens)
	}
	return fmt.Sprintf("The model used up %s before it finished its reply, so the turn stopped. Raise this model's maximum output in Settings > Models, or lower its effort.", limit)
}

// stopMaxTokens is the stop reason of a reply cut off at max_tokens; the server reports every provider's in this form.
const stopMaxTokens = "max_tokens"
