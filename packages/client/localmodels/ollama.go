package localmodels

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
)

// Ollama's native chat API (/api/chat) is used instead of its OpenAI-compatible endpoint because only the
// native one takes options.num_ctx: Ollama's default context (2–4K tokens) silently truncates an agent's prompt.

type olMessage struct {
	Role      string       `json:"role"`
	Content   string       `json:"content"`
	Thinking  string       `json:"thinking,omitempty"`
	Images    []string     `json:"images,omitempty"`
	ToolCalls []olToolCall `json:"tool_calls,omitempty"`
	ToolName  string       `json:"tool_name,omitempty"`
}

type olToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Index     *int            `json:"index,omitempty"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type olRequest struct {
	Model    string         `json:"model"`
	Messages []olMessage    `json:"messages"`
	Stream   bool           `json:"stream"`
	Tools    []oaTool       `json:"tools,omitempty"`
	Think    *bool          `json:"think,omitempty"`
	Options  map[string]any `json:"options,omitempty"`
}

func buildOllamaRequest(req *llm.Request, model Model, withTools bool) olRequest {
	out := olRequest{Model: model.Name, Stream: true, Options: map[string]any{}}
	if model.ContextWindow > 0 {
		out.Options["num_ctx"] = model.ContextWindow
	}
	if req.MaxTokens > 0 {
		out.Options["num_predict"] = req.MaxTokens
	}
	if model.Thinking != nil && *model.Thinking {
		think := req.Thinking != nil && req.Thinking.Type != "disabled"
		out.Think = &think
	}
	if sys := systemText(req); sys != "" {
		out.Messages = append(out.Messages, olMessage{Role: "system", Content: sys})
	}
	vision := model.Vision == nil || *model.Vision
	toolNames := map[string]string{} // tool_use id -> name
	for _, m := range req.Messages {
		if m.Role == llm.RoleAssistant {
			msg := olMessage{Role: "assistant"}
			for _, b := range m.Content {
				switch b.Type {
				case llm.BlockText:
					msg.Content += b.Text
				case llm.BlockToolUse:
					toolNames[b.ID] = b.Name
					in := b.Input
					if len(in) == 0 || !json.Valid(in) {
						in = json.RawMessage("{}")
					}
					if withTools {
						tc := olToolCall{}
						tc.Function.Name, tc.Function.Arguments = b.Name, in
						msg.ToolCalls = append(msg.ToolCalls, tc)
					} else {
						msg.Content += fmt.Sprintf("\n[called tool %s with %s]", b.Name, in)
					}
				}
			}
			out.Messages = append(out.Messages, msg)
			continue
		}
		user := olMessage{Role: "user"}
		var texts []string
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolResult:
				if withTools {
					out.Messages = append(out.Messages, olMessage{Role: "tool", Content: toolResultText(b), ToolName: toolNames[b.ToolUseID]})
				} else {
					texts = append(texts, "[tool result]\n"+toolResultText(b))
				}
			case llm.BlockText:
				texts = append(texts, b.Text)
			case llm.BlockImage:
				if vision && b.Source != nil && b.Source.Data != "" {
					user.Images = append(user.Images, b.Source.Data)
				} else {
					texts = append(texts, "[image omitted: the model cannot read images]")
				}
			case llm.BlockDocument:
				texts = append(texts, "[document omitted: the model cannot read documents]")
			}
		}
		if len(texts) > 0 || len(user.Images) > 0 {
			user.Content = strings.Join(texts, "\n\n")
			out.Messages = append(out.Messages, user)
		}
	}
	if withTools && len(req.Tools) > 0 && (req.ToolChoice == nil || req.ToolChoice.Type != "none") {
		for _, t := range req.Tools {
			ot := oaTool{Type: "function"}
			ot.Function.Name, ot.Function.Description, ot.Function.Parameters = t.Name, t.Description, t.InputSchema
			if len(ot.Function.Parameters) == 0 {
				ot.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			out.Tools = append(out.Tools, ot)
		}
	}
	return out
}

type olChunk struct {
	Model      string    `json:"model"`
	Message    olMessage `json:"message"`
	Done       bool      `json:"done"`
	DoneReason string    `json:"done_reason"`
	PromptEval int       `json:"prompt_eval_count"`
	Eval       int       `json:"eval_count"`
	Error      string    `json:"error"`
}

// ollamaStream reads /api/chat NDJSON into events. Tool calls arrive whole.
func ollamaStream(body io.ReadCloser, model string, onDone func(error, llm.Usage, string)) *chunkStream {
	em := newEmitter()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var (
		think thinkSplitter
		usage llm.Usage
		tools int
	)
	s := &chunkStream{em: em, body: body}
	s.onDone = func(err error) { onDone(err, usage, em.stop) }
	s.read = func() error {
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var c olChunk
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				continue
			}
			if c.Error != "" {
				return &llm.APIError{Type: "upstream", Message: "Ollama: " + c.Error}
			}
			em.start("msg_local", model)
			if c.Message.Thinking != "" {
				em.delta(llm.BlockThinking, c.Message.Thinking)
			}
			if c.Message.Content != "" {
				em.emitPieces(think.feed(c.Message.Content))
			}
			for _, tc := range c.Message.ToolCalls {
				em.emitPieces(think.flush())
				args := string(tc.Function.Arguments)
				if args == "" || args == "null" {
					args = "{}"
				}
				// Some models return the arguments as a JSON string.
				var str string
				if json.Unmarshal(tc.Function.Arguments, &str) == nil && json.Valid([]byte(str)) {
					args = str
				}
				em.toolStart(tools, tc.ID, tc.Function.Name)
				em.toolArgs(tools, args)
				tools++
			}
			if c.Done {
				usage = llm.Usage{InputTokens: c.PromptEval, OutputTokens: c.Eval}
				em.emitPieces(think.flush())
				em.finish(mapFinish(c.DoneReason), usage, model)
			}
			if len(em.queue) > 0 {
				return nil
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
		if !em.finished {
			if !em.started {
				return &llm.APIError{Type: "upstream", Message: "Ollama closed the stream without an answer"}
			}
			em.emitPieces(think.flush())
			em.finish("end_turn", usage, model)
			return nil
		}
		return io.EOF
	}
	return s
}

// PullProgress is one progress message of an Ollama pull.
type PullProgress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Done      bool   `json:"-"`
}

// PullOllamaModel pulls a model into an Ollama provider, calling progress for every update. Cancel ctx to stop.
func (r *Registry) PullOllamaModel(ctx context.Context, providerID, model string, progress func(PullProgress) error) error {
	c, err := r.ollamaProvider(providerID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(model) == "" {
		return &llm.APIError{Type: "invalid_request", Message: "model is required"}
	}
	body, err := postStream(ctx, r.http, rootURL(c.BaseURL)+"/api/pull", "", map[string]any{"model": model, "stream": true}, c.Name)
	if err != nil {
		return err
	}
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var p struct {
			PullProgress
			Error string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &p) != nil {
			continue
		}
		if p.Error != "" {
			return &llm.APIError{Type: "upstream", Message: "Ollama: " + p.Error}
		}
		p.Done = p.Status == "success"
		if err := progress(p.PullProgress); err != nil {
			return err
		}
		if p.Done {
			r.invalidate(c.ID)
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("Ollama ended the pull without success")
}

// DeleteOllamaModel removes a model from an Ollama provider.
func (r *Registry) DeleteOllamaModel(ctx context.Context, providerID, model string) error {
	c, err := r.ollamaProvider(providerID)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, rootURL(c.BaseURL)+"/api/delete", strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return &llm.APIError{Type: "upstream", Message: fmt.Sprintf("%s is not reachable: %v", c.Name, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode == http.StatusNotFound {
			return &llm.APIError{Status: 404, Type: "not_found", Message: c.Name + ": " + errorMessage(msg)}
		}
		return httpError(resp.StatusCode, c.Name, errorMessage(msg))
	}
	r.invalidate(c.ID)
	return nil
}
