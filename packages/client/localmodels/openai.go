package localmodels

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
)

func newToolID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "toolu_local_" + hex.EncodeToString(b[:])
}

// systemText joins the system prompt blocks.
func systemText(req *llm.Request) string {
	var parts []string
	for _, b := range req.System {
		if strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// blocksText renders tool result content as text; images and documents become placeholders.
func blocksText(blocks []llm.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case llm.BlockText:
			sb.WriteString(b.Text)
		case llm.BlockImage:
			sb.WriteString("[image omitted]")
		case llm.BlockDocument:
			sb.WriteString("[document omitted]")
		default:
			continue
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func toolResultText(b llm.ContentBlock) string {
	t := blocksText(b.Content)
	if b.IsError {
		t = "Error: " + t
	}
	if t == "" {
		t = "(no output)"
	}
	return t
}

// ---- request ----

type oaMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type oaPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model         string          `json:"model"`
	Messages      []oaMessage     `json:"messages"`
	Stream        bool            `json:"stream"`
	StreamOptions map[string]bool `json:"stream_options,omitempty"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Tools         []oaTool        `json:"tools,omitempty"`
	ToolChoice    any             `json:"tool_choice,omitempty"`
}

// buildOpenAIRequest converts a request for model (the provider's own name). Thinking blocks of earlier turns
// are not sent back; images go as data URLs when vision is not known to be missing.
func buildOpenAIRequest(req *llm.Request, model Model, withTools bool) oaRequest {
	out := oaRequest{Model: model.Name, Stream: true, StreamOptions: map[string]bool{"include_usage": true}, MaxTokens: req.MaxTokens}
	if sys := systemText(req); sys != "" {
		out.Messages = append(out.Messages, oaMessage{Role: "system", Content: sys})
	}
	vision := model.Vision == nil || *model.Vision
	for _, m := range req.Messages {
		if m.Role == llm.RoleAssistant {
			msg := oaMessage{Role: "assistant"}
			text := ""
			for _, b := range m.Content {
				switch b.Type {
				case llm.BlockText:
					text += b.Text
				case llm.BlockToolUse:
					tc := oaToolCall{ID: b.ID, Type: "function"}
					tc.Function.Name = b.Name
					in := string(b.Input)
					if in == "" {
						in = "{}"
					}
					tc.Function.Arguments = in
					msg.ToolCalls = append(msg.ToolCalls, tc)
				}
			}
			if !withTools && len(msg.ToolCalls) > 0 {
				// A model without tools sees earlier calls as text.
				for _, tc := range msg.ToolCalls {
					text += fmt.Sprintf("\n[called tool %s with %s]", tc.Function.Name, tc.Function.Arguments)
				}
				msg.ToolCalls = nil
			}
			msg.Content = text
			if text == "" && len(msg.ToolCalls) > 0 {
				msg.Content = nil
			}
			out.Messages = append(out.Messages, msg)
			continue
		}
		var parts []oaPart
		onlyText := true
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolResult:
				if withTools {
					out.Messages = append(out.Messages, oaMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: toolResultText(b)})
				} else {
					parts = append(parts, oaPart{Type: "text", Text: "[tool result]\n" + toolResultText(b)})
				}
			case llm.BlockText:
				parts = append(parts, oaPart{Type: "text", Text: b.Text})
			case llm.BlockImage:
				if vision && b.Source != nil && b.Source.Data != "" {
					p := oaPart{Type: "image_url", ImageURL: &struct {
						URL string `json:"url"`
					}{URL: "data:" + b.Source.MediaType + ";base64," + b.Source.Data}}
					parts = append(parts, p)
					onlyText = false
				} else {
					parts = append(parts, oaPart{Type: "text", Text: "[image omitted: the model cannot read images]"})
				}
			case llm.BlockDocument:
				parts = append(parts, oaPart{Type: "text", Text: "[document omitted: the model cannot read documents]"})
			}
		}
		if len(parts) == 0 {
			continue
		}
		if onlyText {
			var sb strings.Builder
			for i, p := range parts {
				if i > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(p.Text)
			}
			out.Messages = append(out.Messages, oaMessage{Role: "user", Content: sb.String()})
		} else {
			out.Messages = append(out.Messages, oaMessage{Role: "user", Content: parts})
		}
	}
	if withTools && len(req.Tools) > 0 && (req.ToolChoice == nil || req.ToolChoice.Type != "none") {
		for _, t := range req.Tools {
			ot := oaTool{Type: "function"}
			ot.Function.Name, ot.Function.Description = t.Name, t.Description
			ot.Function.Parameters = t.InputSchema
			if len(ot.Function.Parameters) == 0 {
				ot.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			out.Tools = append(out.Tools, ot)
		}
		if tc := req.ToolChoice; tc != nil {
			switch tc.Type {
			case "any":
				out.ToolChoice = "required"
			case "tool":
				out.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
			}
		}
	}
	return out
}

// ---- response ----

type oaChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string       `json:"content"`
			ReasoningContent string       `json:"reasoning_content"`
			Reasoning        string       `json:"reasoning"`
			ToolCalls        []oaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// openAIStream reads an SSE chat completions stream into events.
func openAIStream(body io.ReadCloser, model string, onDone func(error, llm.Usage, string)) *chunkStream {
	em := newEmitter()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var (
		think    thinkSplitter
		usage    llm.Usage
		finish   string
		toolSeen = map[int]bool{}
	)
	end := func() {
		em.emitPieces(think.flush())
		em.finish(mapFinish(finish), usage, model)
	}
	s := &chunkStream{em: em, body: body}
	s.onDone = func(err error) { onDone(err, usage, em.stop) }
	s.read = func() error {
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				em.start("msg_local", model)
				end()
				return nil
			}
			var c oaChunk
			if err := json.Unmarshal([]byte(data), &c); err != nil {
				continue
			}
			if len(c.Error) > 0 && string(c.Error) != "null" {
				return &llm.APIError{Type: "upstream", Message: errorMessage([]byte(data))}
			}
			id := c.ID
			if id == "" {
				id = "msg_local"
			}
			em.start(id, model)
			if u := c.Usage; u != nil {
				usage.InputTokens, usage.OutputTokens = u.PromptTokens, u.CompletionTokens
				if u.PromptTokensDetails != nil {
					usage.CacheReadInputTokens = u.PromptTokensDetails.CachedTokens
					usage.InputTokens -= u.PromptTokensDetails.CachedTokens
				}
			}
			for _, ch := range c.Choices {
				d := ch.Delta
				if r := d.ReasoningContent + d.Reasoning; r != "" {
					em.delta(llm.BlockThinking, r)
				}
				if d.Content != "" {
					em.emitPieces(think.feed(d.Content))
				}
				for i, tc := range d.ToolCalls {
					idx := i
					if tc.Index != nil {
						idx = *tc.Index
					}
					if !toolSeen[idx] {
						if tc.Function.Name == "" && tc.ID == "" {
							continue
						}
						toolSeen[idx] = true
						em.emitPieces(think.flush())
						em.toolStart(idx, tc.ID, tc.Function.Name)
					}
					em.toolArgs(idx, tc.Function.Arguments)
				}
				if ch.FinishReason != nil && *ch.FinishReason != "" {
					finish = *ch.FinishReason
				}
			}
			if len(em.queue) > 0 {
				return nil
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
		if !em.finished {
			// Some servers end the body without [DONE].
			if !em.started {
				return &llm.APIError{Type: "upstream", Message: "the local server closed the stream without an answer"}
			}
			end()
			return nil
		}
		return io.EOF
	}
	return s
}

// postStream POSTs a JSON body and returns the response body of a 2xx answer, or an *llm.APIError.
func postStream(ctx context.Context, hc *http.Client, url, key string, body any, providerName string) (io.ReadCloser, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/x-ndjson, application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.APIError{Type: "upstream", Message: fmt.Sprintf("%s is not reachable: %v", providerName, err)}
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, httpError(resp.StatusCode, providerName, errorMessage(msg))
	}
	return resp.Body, nil
}

func httpError(status int, providerName, msg string) *llm.APIError {
	t := "upstream"
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		t = "authentication"
	case status == http.StatusTooManyRequests:
		t = "rate_limit"
	case status >= 400 && status < 500:
		t = "invalid_request"
	}
	return &llm.APIError{Status: status, Type: t, Message: providerName + ": " + msg}
}

// isNoToolsError reports whether a server refused a request because the model has no tool support.
func isNoToolsError(err error) bool {
	ae, ok := err.(*llm.APIError)
	if !ok || ae.Type != "invalid_request" {
		return false
	}
	m := strings.ToLower(ae.Message)
	return strings.Contains(m, "tool") && (strings.Contains(m, "not support") || strings.Contains(m, "unsupported") || strings.Contains(m, "does not support") || strings.Contains(m, "not enabled"))
}
