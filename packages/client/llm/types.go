// Package llm defines the canonical message model shared by the client and the
// harness LLM gateway (see docs/protocol/gateway.md). It is a subset of the
// Anthropic Messages API: content blocks, tools, usage and streaming events.
//
// The types are transport-neutral: the HTTP/SSE gateway adapter and any future
// adapter (e.g. ConnectRPC) translate to and from them.
package llm

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Block types.
const (
	BlockText             = "text"
	BlockThinking         = "thinking"
	BlockRedactedThinking = "redacted_thinking"
	BlockToolUse          = "tool_use"
	BlockToolResult       = "tool_result"
	BlockImage            = "image"
	BlockDocument         = "document"
)

// Roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Source is the payload of image and document blocks.
type Source struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ObjectRef names a file in the server's object store (harness.common.v1.ObjectRef).
type ObjectRef struct {
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type,omitempty"`
	Name      string `json:"name,omitempty"`
}

// ContentBlock is a single content block. Only the fields relevant to Type are set.
type ContentBlock struct {
	Type string `json:"type"`
	// Title names a document.
	Title string `json:"title,omitempty"`

	// text
	Text string `json:"text,omitempty"`
	// Object: this text stands in for bytes in the object store (transcripts from the server).
	Object *ObjectRef `json:"object,omitempty"`

	// thinking / redacted_thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   []ContentBlock `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`

	// image / document
	Source *Source `json:"source,omitempty"`

	// Cache marks the last block of a stable prefix (gateway.md). Request-only.
	Cache bool `json:"cache,omitempty"`
}

// UnmarshalJSON accepts tool_result content given either as a string or as an
// array of blocks (Claude Code transcripts use both).
func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	type alias ContentBlock
	var raw struct {
		alias
		Content json.RawMessage `json:"content,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*b = ContentBlock(raw.alias)
	b.Content = nil
	if len(raw.Content) > 0 && string(raw.Content) != "null" {
		blocks, err := decodeContent(raw.Content)
		if err != nil {
			return fmt.Errorf("content of %s block: %w", b.Type, err)
		}
		b.Content = blocks
	}
	return nil
}

// MarshalJSON guarantees that tool_use blocks always carry an input object and
// text blocks always carry a text field.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	type alias ContentBlock
	switch b.Type {
	case BlockToolUse:
		if len(b.Input) == 0 {
			b.Input = json.RawMessage("{}")
		}
	case BlockText:
		return json.Marshal(struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Cache bool   `json:"cache,omitempty"`
		}{b.Type, b.Text, b.Cache})
	case BlockThinking:
		return json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
			Cache     bool   `json:"cache,omitempty"`
		}{b.Type, b.Thinking, b.Signature, b.Cache})
	case BlockToolResult:
		content := b.Content
		if content == nil {
			content = []ContentBlock{}
		}
		return json.Marshal(struct {
			Type      string         `json:"type"`
			ToolUseID string         `json:"tool_use_id"`
			IsError   bool           `json:"is_error"`
			Content   []ContentBlock `json:"content"`
			Cache     bool           `json:"cache,omitempty"`
		}{b.Type, b.ToolUseID, b.IsError, content, b.Cache})
	}
	return json.Marshal(alias(b))
}

func decodeContent(raw json.RawMessage) ([]ContentBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return []ContentBlock{TextBlock(s)}, nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// DecodeContent parses message content given as a string or a block array.
func DecodeContent(raw json.RawMessage) ([]ContentBlock, error) { return decodeContent(raw) }

// TextBlock builds a text block.
func TextBlock(s string) ContentBlock { return ContentBlock{Type: BlockText, Text: s} }

// Message is one conversation message.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// UnmarshalJSON accepts string content.
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	blocks, err := decodeContent(raw.Content)
	if err != nil {
		return err
	}
	m.Role, m.Content = raw.Role, blocks
	return nil
}

// ToolUses returns the tool_use blocks of the message.
func (m Message) ToolUses() []ContentBlock {
	var out []ContentBlock
	for _, b := range m.Content {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

// Text concatenates the text blocks of the message.
func (m Message) Text() string {
	s := ""
	for _, b := range m.Content {
		if b.Type == BlockText {
			s += b.Text
		}
	}
	return s
}

// Usage is the normalized token usage reported by the gateway.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// Add accumulates u2 into u.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += u2.InputTokens
	u.OutputTokens += u2.OutputTokens
	u.CacheReadInputTokens += u2.CacheReadInputTokens
	u.CacheCreationInputTokens += u2.CacheCreationInputTokens
}

// SystemBlock is one block of the system prompt.
type SystemBlock struct {
	Type  string `json:"type"` // "text"
	Text  string `json:"text"`
	Cache bool   `json:"cache,omitempty"`
}

// ToolDef is a tool definition sent to the model.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolChoice controls tool selection.
type ToolChoice struct {
	Type string `json:"type"` // auto | any | tool | none
	Name string `json:"name,omitempty"`
}

// Thinking configures extended thinking.
type Thinking struct {
	Type         string `json:"type"` // enabled | adaptive
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// Metadata carries harness-specific request metadata.
type Metadata struct {
	SessionID           string `json:"session_id,omitempty"`
	RequestClass        string `json:"request_class,omitempty"` // main | subagent | compaction | auxiliary
	AgentID             string `json:"agent_id,omitempty"`
	PromptBundleVersion string `json:"prompt_bundle_version,omitempty"`
}

// Request is the body of POST /api/llm/messages (and count_tokens).
type Request struct {
	Model      string        `json:"model"`
	MaxTokens  int           `json:"max_tokens,omitempty"`
	System     []SystemBlock `json:"system"`
	Messages   []Message     `json:"messages"`
	Tools      []ToolDef     `json:"tools,omitempty"`
	ToolChoice *ToolChoice   `json:"tool_choice,omitempty"`
	Thinking   *Thinking     `json:"thinking,omitempty"`
	Effort     string        `json:"effort,omitempty"`
	// Speed is a speed tier and CacheTTL a prompt cache lifetime to ask for ("": the provider's default).
	Speed    string    `json:"speed,omitempty"`
	CacheTTL string    `json:"cache_ttl,omitempty"`
	Stream   bool      `json:"stream,omitempty"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// HarnessMeta is attached by the gateway to message_stop.
type HarnessMeta struct {
	CostUSD       float64 `json:"cost_usd"`
	UpstreamModel string  `json:"upstream_model,omitempty"`
	Retries       int     `json:"retries,omitempty"`
}

// Response is a fully accumulated assistant response.
type Response struct {
	ID         string
	Model      string
	StopReason string
	Message    Message
	Usage      Usage
	Harness    HarnessMeta
}

// Model is one entry of the gateway model catalog.
type Model struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	Provider        string `json:"provider,omitempty"`
	ProviderName    string `json:"provider_name,omitempty"`
	DisplayID       string `json:"display_id,omitempty"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	Capabilities    struct {
		Thinking          bool     `json:"thinking"`
		Effort            []string `json:"effort"`
		Images            bool     `json:"images"`
		PDF               bool     `json:"pdf"`
		ParallelToolCalls bool     `json:"parallel_tool_calls"`
		PromptCache       bool     `json:"prompt_cache"`
		// DefaultEffort is the level recommended for a new session, one of Effort ("" when none).
		DefaultEffort string `json:"default_effort,omitempty"`
		// SpeedTiers and CacheTTLs are the choices the provider has for the model (empty: none).
		SpeedTiers []string `json:"speed_tiers,omitempty"`
		CacheTTLs  []string `json:"cache_ttls,omitempty"`
	} `json:"capabilities"`
	Pricing *struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"pricing,omitempty"`
}

// Catalog is the response of GET /api/llm/models.
type Catalog struct {
	Models []Model           `json:"models"`
	Slots  map[string]string `json:"slots"`
}

// Find returns the model with the given id.
func (c *Catalog) Find(id string) (Model, bool) {
	if c == nil {
		return Model{}, false
	}
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// effortLevels are the harness's effort levels, lowest first.
var effortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// RecommendedEffort is the model's recommended level, if it takes that level ("" otherwise).
func (m Model) RecommendedEffort() string {
	if slices.Contains(m.Capabilities.Effort, m.Capabilities.DefaultEffort) {
		return m.Capabilities.DefaultEffort
	}
	return ""
}

// StartingEffort is the level a new session on the model starts on when nobody chose one: the recommended level, else
// medium or the nearest the model takes, the lower one of two as near ("" for a model without levels). The frontends'
// fallback is the same (startEffort in packages/ui), so what they show and what runs cannot differ.
func (m Model) StartingEffort() string {
	if e := m.RecommendedEffort(); e != "" {
		return e
	}
	best, bestDistance := "", len(effortLevels)
	for i, l := range effortLevels {
		d := i - slices.Index(effortLevels, "medium")
		if d < 0 {
			d = -d
		}
		if d < bestDistance && slices.Contains(m.Capabilities.Effort, l) {
			best, bestDistance = l, d
		}
	}
	return best
}

// TokenCount is the response of count_tokens.
type TokenCount struct {
	InputTokens int  `json:"input_tokens"`
	Estimated   bool `json:"estimated"`
}

// APIError is the uniform error body of the harness server (protocol README).
type APIError struct {
	Status       int    `json:"-"`
	Type         string `json:"type"`
	Message      string `json:"message"`
	RetryAfterMs int    `json:"retry_after_ms,omitempty"`
}

func (e *APIError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("harness server error %d (%s): %s", e.Status, e.Type, e.Message)
	}
	return fmt.Sprintf("harness server error (%s): %s", e.Type, e.Message)
}

// Retryable reports whether the error class is transient.
func (e *APIError) Retryable() bool {
	switch e.Type {
	case "overloaded", "upstream", "internal":
		return true
	case "rate_limit":
		return true
	}
	return e.Status == 429 && e.Type != "quota_exceeded" || e.Status >= 500
}
