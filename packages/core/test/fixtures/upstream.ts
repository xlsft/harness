/**
 * Synthetic upstream SSE fixtures, shaped after real provider streams
 * (Anthropic Messages, OpenAI Chat Completions, DeepSeek reasoning_content).
 */
import { sse } from "../helpers/mock-upstream.ts";

/** Anthropic: thinking (signed) + text, with cache usage. */
export const anthropicThinkingText = sse([
  ["message_start", { type: "message_start", message: { id: "msg_up_1", type: "message", role: "assistant", model: "claude-up", content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 120, cache_read_input_tokens: 1000, cache_creation_input_tokens: 50, output_tokens: 1 } } }],
  ["ping", { type: "ping" }],
  ["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "thinking", thinking: "", signature: "" } }],
  ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "thinking_delta", thinking: "Let me think" } }],
  ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "signature_delta", signature: "sig-abc" } }],
  ["content_block_stop", { type: "content_block_stop", index: 0 }],
  ["content_block_start", { type: "content_block_start", index: 1, content_block: { type: "text", text: "" } }],
  ["content_block_delta", { type: "content_block_delta", index: 1, delta: { type: "text_delta", text: "Hel" } }],
  ["content_block_delta", { type: "content_block_delta", index: 1, delta: { type: "text_delta", text: "lo" } }],
  ["content_block_stop", { type: "content_block_stop", index: 1 }],
  ["message_delta", { type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 42 } }],
  ["message_stop", { type: "message_stop" }],
]);

/** Anthropic: tool_use with streamed input JSON. */
export const anthropicToolUse = sse([
  ["message_start", { type: "message_start", message: { id: "msg_up_2", type: "message", role: "assistant", content: [], usage: { input_tokens: 10, output_tokens: 1 } } }],
  ["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "tool_use", id: "toolu_01", name: "Read", input: {} } }],
  ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "input_json_delta", partial_json: "" } }],
  ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "input_json_delta", partial_json: '{"file_path":' } }],
  ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "input_json_delta", partial_json: '"/a.txt"}' } }],
  ["content_block_stop", { type: "content_block_stop", index: 0 }],
  ["message_delta", { type: "message_delta", delta: { stop_reason: "tool_use", stop_sequence: null }, usage: { output_tokens: 20 } }],
  ["message_stop", { type: "message_stop" }],
]);

/** Anthropic: overloaded error event before any content block. */
export const anthropicOverloadedInStream = sse([
  ["message_start", { type: "message_start", message: { id: "msg_up_3", type: "message", role: "assistant", content: [], usage: { input_tokens: 10, output_tokens: 0 } } }],
  ["error", { type: "error", error: { type: "overloaded_error", message: "Overloaded" } }],
]);

/** Anthropic: error after content already started (must not be retried). */
export const anthropicErrorAfterContent = sse([
  ["message_start", { type: "message_start", message: { id: "msg_up_4", type: "message", role: "assistant", content: [], usage: { input_tokens: 10, output_tokens: 0 } } }],
  ["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } }],
  ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "partial" } }],
  ["error", { type: "error", error: { type: "overloaded_error", message: "Overloaded" } }],
]);

const chunk = (delta: Record<string, unknown>, finish: string | null = null, extra: Record<string, unknown> = {}) => ({
  id: "chatcmpl-1",
  object: "chat.completion.chunk",
  created: 1,
  model: "gpt-up",
  choices: [{ index: 0, delta, finish_reason: finish }],
  ...extra,
});

/** OpenAI: plain text with usage (cached tokens) in a trailing chunk. */
export const openaiText = sse([
  [null, chunk({ role: "assistant", content: "" })],
  [null, chunk({ content: "Hello" })],
  [null, chunk({ content: " world" })],
  [null, chunk({}, "stop")],
  [null, { id: "chatcmpl-1", object: "chat.completion.chunk", choices: [], usage: { prompt_tokens: 1500, completion_tokens: 7, total_tokens: 1507, prompt_tokens_details: { cached_tokens: 1024 } } }],
  [null, "[DONE]"],
]);

/** OpenAI: two parallel tool calls, arguments split across chunks, with leading text. */
export const openaiToolCalls = sse([
  [null, chunk({ role: "assistant", content: "Reading both." })],
  [null, chunk({ tool_calls: [{ index: 0, id: "call_A", type: "function", function: { name: "Read", arguments: "" } }] })],
  [null, chunk({ tool_calls: [{ index: 0, function: { arguments: '{"file_' } }] })],
  [null, chunk({ tool_calls: [{ index: 0, function: { arguments: 'path":"/a"}' } }] })],
  [null, chunk({ tool_calls: [{ index: 1, id: "call_B", type: "function", function: { name: "Glob", arguments: '{"pattern":"**/*.go"}' } }] })],
  [null, chunk({}, "tool_calls", { usage: { prompt_tokens: 50, completion_tokens: 30 } })],
  [null, "[DONE]"],
]);

/** DeepSeek: reasoning_content deltas, then content; cache hit/miss usage fields. */
export const deepseekReasoning = sse([
  [null, chunk({ role: "assistant", content: null, reasoning_content: "" })],
  [null, chunk({ content: null, reasoning_content: "First, " })],
  [null, chunk({ content: null, reasoning_content: "consider X." })],
  [null, chunk({ content: "Answer: 42", reasoning_content: null })],
  [null, chunk({ content: "" }, "stop", { usage: { prompt_tokens: 900, completion_tokens: 60, prompt_cache_hit_tokens: 640, prompt_cache_miss_tokens: 260 } })],
  [null, "[DONE]"],
]);
