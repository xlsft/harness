package web

import (
	"context"
	"errors"
	"strings"

	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/tools"
)

// SummaryTemplateKey is the bundle reminder that holds the WebFetch
// summarization prompt; placeholders: url, prompt, content.
const SummaryTemplateKey = "webfetch_summary"

// SummaryRequest is the input of a Summarizer.
type SummaryRequest struct {
	URL     string
	Prompt  string
	Content string
	// Truncated reports that Content was cut to the size limit.
	Truncated bool
}

// Summarizer answers a prompt about fetched content.
type Summarizer interface {
	Summarize(ctx context.Context, env *tools.Env, req SummaryRequest) (string, error)
}

// GatewaySummarizer asks a model through the harness gateway (the server's
// small slot) with the bundle's summarization prompt.
type GatewaySummarizer struct {
	Gateway gateway.Gateway
	// Model returns the model to use (the small slot, else the main model).
	Model func() string
	// Template returns the prompt template; default env.Reminders(SummaryTemplateKey).
	Template func(env *tools.Env) string
	// BundleVersion returns the prompt bundle version for request metadata (optional).
	BundleVersion func() string
	// MaxTokens bounds the answer (default 4096).
	MaxTokens int
}

// ErrNoTemplate means the prompt bundle has no WebFetch summarization prompt.
var ErrNoTemplate = errors.New("the prompt bundle has no WebFetch summarization prompt (" + SummaryTemplateKey + ")")

// Summarize implements Summarizer.
func (s *GatewaySummarizer) Summarize(ctx context.Context, env *tools.Env, req SummaryRequest) (string, error) {
	tmpl := ""
	switch {
	case s.Template != nil:
		tmpl = s.Template(env)
	case env != nil && env.Reminders != nil:
		tmpl = env.Reminders(SummaryTemplateKey)
	}
	if strings.TrimSpace(tmpl) == "" {
		return "", ErrNoTemplate
	}
	model := ""
	if s.Model != nil {
		model = s.Model()
	}
	if model == "" {
		return "", errors.New("no model is configured for WebFetch (set the small slot on the server)")
	}
	content := req.Content
	if strings.TrimSpace(content) == "" {
		content = "(the page has no text content)"
	}
	if req.Truncated {
		content += "\n\n[The page was truncated here because it is too long.]"
	}
	text := prompts.Render(tmpl, map[string]string{"url": req.URL, "prompt": req.Prompt, "content": content}, nil)
	maxTokens := s.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	md := &llm.Metadata{RequestClass: "auxiliary"}
	if env != nil {
		md.SessionID = env.SessionID
	}
	if s.BundleVersion != nil {
		md.PromptBundleVersion = s.BundleVersion()
	}
	r := &llm.Request{
		Model:     model,
		MaxTokens: maxTokens,
		System:    []llm.SystemBlock{},
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(text)}}},
		Stream:    true,
		Metadata:  md,
	}
	resp, _, err := gateway.Send(ctx, s.Gateway, r, nil, nil)
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(resp.Message.Text())
	if answer == "" {
		return "", errors.New("the model returned an empty answer")
	}
	return answer, nil
}
