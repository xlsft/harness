// Package pbconv converts between the client's canonical llm types and the
// generated protobuf types of harness.llm.v1.
package pbconv

import (
	"encoding/base64"
	"encoding/json"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	"strings"

	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/llm"
)

// Block converts one content block to protobuf.
func Block(b llm.ContentBlock) *llmv1.ContentBlock {
	switch b.Type {
	case llm.BlockText:
		t := &llmv1.TextBlock{Text: b.Text, Cache: b.Cache}
		if o := b.Object; o != nil {
			t.Object = &commonv1.ObjectRef{Sha256: o.SHA256, Size: o.Size, MediaType: o.MediaType, Name: o.Name}
		}
		return &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Text{Text: t}}
	case llm.BlockImage:
		mt, data := source(b.Source)
		return &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Image{Image: &llmv1.ImageBlock{MediaType: mt, Data: data, Cache: b.Cache}}}
	case llm.BlockDocument:
		mt, data := source(b.Source)
		return &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Document{Document: &llmv1.DocumentBlock{MediaType: mt, Data: data, Cache: b.Cache}}}
	case llm.BlockThinking:
		return &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Thinking{Thinking: &llmv1.ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature}}}
	case llm.BlockRedactedThinking:
		return &llmv1.ContentBlock{Block: &llmv1.ContentBlock_RedactedThinking{RedactedThinking: &llmv1.RedactedThinkingBlock{Data: b.Data}}}
	case llm.BlockToolUse:
		in := string(b.Input)
		if in == "" {
			in = "{}"
		}
		return &llmv1.ContentBlock{Block: &llmv1.ContentBlock_ToolUse{ToolUse: &llmv1.ToolUseBlock{Id: b.ID, Name: b.Name, InputJson: in}}}
	case llm.BlockToolResult:
		return &llmv1.ContentBlock{Block: &llmv1.ContentBlock_ToolResult{ToolResult: &llmv1.ToolResultBlock{
			ToolUseId: b.ToolUseID, IsError: b.IsError, Content: Blocks(b.Content), Cache: b.Cache,
		}}}
	}
	return &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: b.Text}}}
}

func source(s *llm.Source) (string, []byte) {
	if s == nil {
		return "", nil
	}
	data, _ := base64.StdEncoding.DecodeString(s.Data)
	return s.MediaType, data
}

// Blocks converts a slice of blocks.
func Blocks(in []llm.ContentBlock) []*llmv1.ContentBlock {
	out := make([]*llmv1.ContentBlock, 0, len(in))
	for _, b := range in {
		out = append(out, Block(b))
	}
	return out
}

// FromBlock converts a protobuf block to the canonical form.
func FromBlock(b *llmv1.ContentBlock) llm.ContentBlock {
	switch v := b.GetBlock().(type) {
	case *llmv1.ContentBlock_Text:
		out := llm.ContentBlock{Type: llm.BlockText, Text: v.Text.GetText(), Cache: v.Text.GetCache()}
		if o := v.Text.GetObject(); o != nil {
			out.Object = &llm.ObjectRef{SHA256: o.GetSha256(), Size: o.GetSize(), MediaType: o.GetMediaType(), Name: o.GetName()}
		}
		return out
	case *llmv1.ContentBlock_Image:
		return llm.ContentBlock{Type: llm.BlockImage, Cache: v.Image.GetCache(), Source: &llm.Source{Type: "base64", MediaType: v.Image.GetMediaType(), Data: base64.StdEncoding.EncodeToString(v.Image.GetData())}}
	case *llmv1.ContentBlock_Document:
		return llm.ContentBlock{Type: llm.BlockDocument, Cache: v.Document.GetCache(), Source: &llm.Source{Type: "base64", MediaType: v.Document.GetMediaType(), Data: base64.StdEncoding.EncodeToString(v.Document.GetData())}}
	case *llmv1.ContentBlock_Thinking:
		return llm.ContentBlock{Type: llm.BlockThinking, Thinking: v.Thinking.GetThinking(), Signature: v.Thinking.GetSignature()}
	case *llmv1.ContentBlock_RedactedThinking:
		return llm.ContentBlock{Type: llm.BlockRedactedThinking, Data: v.RedactedThinking.GetData()}
	case *llmv1.ContentBlock_ToolUse:
		in := v.ToolUse.GetInputJson()
		var raw json.RawMessage
		if in != "" {
			raw = json.RawMessage(in)
		}
		return llm.ContentBlock{Type: llm.BlockToolUse, ID: v.ToolUse.GetId(), Name: v.ToolUse.GetName(), Input: raw}
	case *llmv1.ContentBlock_ToolResult:
		return llm.ContentBlock{Type: llm.BlockToolResult, ToolUseID: v.ToolResult.GetToolUseId(), IsError: v.ToolResult.GetIsError(), Content: FromBlocks(v.ToolResult.GetContent()), Cache: v.ToolResult.GetCache()}
	}
	return llm.ContentBlock{Type: llm.BlockText}
}

// FromBlocks converts a slice of protobuf blocks.
func FromBlocks(in []*llmv1.ContentBlock) []llm.ContentBlock {
	out := make([]llm.ContentBlock, 0, len(in))
	for _, b := range in {
		out = append(out, FromBlock(b))
	}
	return out
}

// Role converts a role name.
func Role(r string) llmv1.Role {
	if r == llm.RoleAssistant {
		return llmv1.Role_ROLE_ASSISTANT
	}
	return llmv1.Role_ROLE_USER
}

// FromRole converts a protobuf role.
func FromRole(r llmv1.Role) string {
	if r == llmv1.Role_ROLE_ASSISTANT {
		return llm.RoleAssistant
	}
	return llm.RoleUser
}

// Messages converts messages.
func Messages(in []llm.Message) []*llmv1.Message {
	out := make([]*llmv1.Message, 0, len(in))
	for _, m := range in {
		out = append(out, &llmv1.Message{Role: Role(m.Role), Content: Blocks(m.Content)})
	}
	return out
}

// FromMessages converts protobuf messages.
func FromMessages(in []*llmv1.Message) []llm.Message {
	out := make([]llm.Message, 0, len(in))
	for _, m := range in {
		out = append(out, llm.Message{Role: FromRole(m.GetRole()), Content: FromBlocks(m.GetContent())})
	}
	return out
}

var efforts = map[string]llmv1.Effort{
	"low": llmv1.Effort_EFFORT_LOW, "medium": llmv1.Effort_EFFORT_MEDIUM, "high": llmv1.Effort_EFFORT_HIGH,
	"xhigh": llmv1.Effort_EFFORT_XHIGH, "max": llmv1.Effort_EFFORT_MAX,
}

// Effort converts an effort name ("" = unspecified).
func Effort(s string) llmv1.Effort { return efforts[strings.ToLower(s)] }

// FromEffort converts a protobuf effort.
func FromEffort(e llmv1.Effort) string {
	for k, v := range efforts {
		if v == e {
			return k
		}
	}
	return ""
}

var stopReasons = map[string]llmv1.StopReason{
	"end_turn": llmv1.StopReason_STOP_REASON_END_TURN, "tool_use": llmv1.StopReason_STOP_REASON_TOOL_USE,
	"max_tokens": llmv1.StopReason_STOP_REASON_MAX_TOKENS, "stop_sequence": llmv1.StopReason_STOP_REASON_STOP_SEQUENCE,
	"refusal": llmv1.StopReason_STOP_REASON_REFUSAL, "pause_turn": llmv1.StopReason_STOP_REASON_PAUSE_TURN,
}

// StopReason converts a stop reason name.
func StopReason(s string) llmv1.StopReason { return stopReasons[s] }

// FromStopReason converts a protobuf stop reason.
func FromStopReason(r llmv1.StopReason) string {
	for k, v := range stopReasons {
		if v == r {
			return k
		}
	}
	return ""
}

// Usage converts usage.
func Usage(u llm.Usage) *llmv1.Usage {
	return &llmv1.Usage{InputTokens: int64(u.InputTokens), OutputTokens: int64(u.OutputTokens), CacheReadInputTokens: int64(u.CacheReadInputTokens), CacheCreationInputTokens: int64(u.CacheCreationInputTokens)}
}

// FromUsage converts protobuf usage.
func FromUsage(u *llmv1.Usage) llm.Usage {
	return llm.Usage{InputTokens: int(u.GetInputTokens()), OutputTokens: int(u.GetOutputTokens()), CacheReadInputTokens: int(u.GetCacheReadInputTokens()), CacheCreationInputTokens: int(u.GetCacheCreationInputTokens())}
}

func system(in []llm.SystemBlock) []*llmv1.TextBlock {
	out := make([]*llmv1.TextBlock, 0, len(in))
	for _, s := range in {
		out = append(out, &llmv1.TextBlock{Text: s.Text, Cache: s.Cache})
	}
	return out
}

func toolDefs(in []llm.ToolDef) []*llmv1.Tool {
	out := make([]*llmv1.Tool, 0, len(in))
	for _, t := range in {
		out = append(out, &llmv1.Tool{Name: t.Name, Description: t.Description, InputSchemaJson: string(t.InputSchema)})
	}
	return out
}

// StreamRequest converts a request.
func StreamRequest(r *llm.Request) *llmv1.StreamMessageRequest {
	out := &llmv1.StreamMessageRequest{
		Model: r.Model, MaxTokens: int32(r.MaxTokens), System: system(r.System),
		Messages: Messages(r.Messages), Tools: toolDefs(r.Tools), Effort: Effort(r.Effort),
	}
	if r.ToolChoice != nil {
		modes := map[string]llmv1.ToolChoice_Mode{"auto": llmv1.ToolChoice_MODE_AUTO, "any": llmv1.ToolChoice_MODE_ANY, "tool": llmv1.ToolChoice_MODE_TOOL, "none": llmv1.ToolChoice_MODE_NONE}
		out.ToolChoice = &llmv1.ToolChoice{Mode: modes[r.ToolChoice.Type], ToolName: r.ToolChoice.Name}
	}
	if r.Thinking != nil {
		mode := llmv1.Thinking_MODE_ENABLED
		if r.Thinking.Type == "adaptive" {
			mode = llmv1.Thinking_MODE_ADAPTIVE
		}
		out.Thinking = &llmv1.Thinking{Mode: mode, BudgetTokens: int32(r.Thinking.BudgetTokens)}
	}
	if r.Metadata != nil {
		classes := map[string]llmv1.RequestClass{"main": llmv1.RequestClass_REQUEST_CLASS_MAIN, "subagent": llmv1.RequestClass_REQUEST_CLASS_SUBAGENT, "compaction": llmv1.RequestClass_REQUEST_CLASS_COMPACTION, "auxiliary": llmv1.RequestClass_REQUEST_CLASS_AUXILIARY}
		out.Metadata = &llmv1.RequestMetadata{SessionId: r.Metadata.SessionID, RequestClass: classes[r.Metadata.RequestClass], AgentId: r.Metadata.AgentID, PromptBundleVersion: r.Metadata.PromptBundleVersion}
	}
	return out
}

// CountRequest converts a request for CountTokens.
func CountRequest(r *llm.Request) *llmv1.CountTokensRequest {
	return &llmv1.CountTokensRequest{Model: r.Model, System: system(r.System), Messages: Messages(r.Messages), Tools: toolDefs(r.Tools)}
}

// FromStreamRequest converts a protobuf request (used by fakes and tests).
func FromStreamRequest(r *llmv1.StreamMessageRequest) *llm.Request {
	out := &llm.Request{Model: r.GetModel(), MaxTokens: int(r.GetMaxTokens()), Messages: FromMessages(r.GetMessages()), Effort: FromEffort(r.GetEffort()), Stream: true}
	for _, s := range r.GetSystem() {
		out.System = append(out.System, llm.SystemBlock{Type: "text", Text: s.GetText(), Cache: s.GetCache()})
	}
	for _, t := range r.GetTools() {
		out.Tools = append(out.Tools, llm.ToolDef{Name: t.GetName(), Description: t.GetDescription(), InputSchema: json.RawMessage(t.GetInputSchemaJson())})
	}
	if m := r.GetMetadata(); m != nil {
		out.Metadata = &llm.Metadata{SessionID: m.GetSessionId(), AgentID: m.GetAgentId(), PromptBundleVersion: m.GetPromptBundleVersion()}
	}
	return out
}

// Model converts a catalog entry.
func FromModel(m *llmv1.Model) llm.Model {
	out := llm.Model{ID: m.GetId(), DisplayName: m.GetDisplayName(), Provider: m.GetProvider(), ContextWindow: int(m.GetContextWindow()), MaxOutputTokens: int(m.GetMaxOutputTokens())}
	if c := m.GetCapabilities(); c != nil {
		out.Capabilities.Thinking = c.GetThinking()
		out.Capabilities.Images = c.GetImages()
		out.Capabilities.PDF = c.GetPdf()
		out.Capabilities.ParallelToolCalls = c.GetParallelToolCalls()
		out.Capabilities.PromptCache = c.GetPromptCache()
		for _, e := range c.GetEffort() {
			out.Capabilities.Effort = append(out.Capabilities.Effort, FromEffort(e))
		}
	}
	if p := m.Pricing; p != nil {
		out.Pricing = &struct {
			Input      float64 `json:"input"`
			Output     float64 `json:"output"`
			CacheRead  float64 `json:"cache_read"`
			CacheWrite float64 `json:"cache_write"`
		}{p.GetInput(), p.GetOutput(), p.GetCacheRead(), p.GetCacheWrite()}
	}
	return out
}

// FromCatalog converts ListModelsResponse.
func FromCatalog(r *llmv1.ListModelsResponse) *llm.Catalog {
	c := &llm.Catalog{Slots: map[string]string{}}
	for _, m := range r.GetModels() {
		c.Models = append(c.Models, FromModel(m))
	}
	if s := r.GetSlots(); s != nil {
		c.Slots["main"], c.Slots["small"], c.Slots["plan"], c.Slots["title"] = s.GetMain(), s.GetSmall(), s.GetPlan(), s.GetTitle()
	}
	return c
}

// StreamEvent converts a protobuf stream event into the canonical event
// (Anthropic streaming JSON), so the transport-neutral accumulator and event
// forwarding work unchanged.
func StreamEvent(r *llmv1.StreamMessageResponse) llm.StreamEvent {
	var typ string
	var v any
	switch e := r.GetEvent().(type) {
	case *llmv1.StreamMessageResponse_MessageStart:
		typ = llm.EventMessageStart
		v = map[string]any{"type": typ, "message": map[string]any{"id": e.MessageStart.GetId(), "model": e.MessageStart.GetModel(), "role": "assistant", "content": []any{}, "usage": FromUsage(e.MessageStart.GetUsage())}}
	case *llmv1.StreamMessageResponse_ContentBlockStart:
		typ = llm.EventContentBlockStart
		var cb llm.ContentBlock
		if b := e.ContentBlockStart.GetBlock(); b != nil {
			cb = FromBlock(b)
		}
		v = map[string]any{"type": typ, "index": e.ContentBlockStart.GetIndex(), "content_block": cb}
	case *llmv1.StreamMessageResponse_ContentBlockDelta:
		typ = llm.EventContentBlockDelta
		d := map[string]any{}
		switch x := e.ContentBlockDelta.GetDelta().(type) {
		case *llmv1.ContentBlockDelta_Text:
			d["type"], d["text"] = "text_delta", x.Text
		case *llmv1.ContentBlockDelta_Thinking:
			d["type"], d["thinking"] = "thinking_delta", x.Thinking
		case *llmv1.ContentBlockDelta_Signature:
			d["type"], d["signature"] = "signature_delta", x.Signature
		case *llmv1.ContentBlockDelta_InputJson:
			d["type"], d["partial_json"] = "input_json_delta", x.InputJson
		}
		v = map[string]any{"type": typ, "index": e.ContentBlockDelta.GetIndex(), "delta": d}
	case *llmv1.StreamMessageResponse_ContentBlockStop:
		typ = llm.EventContentBlockStop
		v = map[string]any{"type": typ, "index": e.ContentBlockStop.GetIndex()}
	case *llmv1.StreamMessageResponse_MessageDelta:
		typ = llm.EventMessageDelta
		v = map[string]any{"type": typ, "delta": map[string]any{"stop_reason": FromStopReason(e.MessageDelta.GetStopReason()), "stop_sequence": e.MessageDelta.GetStopSequence()}, "usage": FromUsage(e.MessageDelta.GetUsage())}
	case *llmv1.StreamMessageResponse_MessageStop:
		typ = llm.EventMessageStop
		v = map[string]any{"type": typ, "harness": llm.HarnessMeta{CostUSD: e.MessageStop.GetCostUsd(), UpstreamModel: e.MessageStop.GetUpstreamModel(), Retries: int(e.MessageStop.GetRetries())}}
	case *llmv1.StreamMessageResponse_Ping:
		typ = llm.EventPing
		v = map[string]any{"type": typ}
	case *llmv1.StreamMessageResponse_Retry:
		typ = llm.EventHarnessRetry
		v = map[string]any{"type": typ, "attempt": e.Retry.GetAttempt(), "max_retries": e.Retry.GetMaxRetries(), "retry_delay_ms": e.Retry.GetRetryDelayMs()}
	default:
		return llm.StreamEvent{}
	}
	raw, _ := json.Marshal(v)
	return llm.StreamEvent{Type: typ, Raw: raw}
}
