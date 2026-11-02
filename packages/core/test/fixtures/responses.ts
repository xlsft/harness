/** Synthetic OpenAI Responses API SSE fixtures, shaped after real streams. */
import { sse } from "../helpers/mock-upstream.ts";

const rev = (type: string, body: Record<string, unknown> = {}): [string, unknown] => [type, { type, ...body }];
const respObj = (status: string, extra: Record<string, unknown> = {}) => ({ id: "resp_1", object: "response", status, model: "gpt-resp-up", ...extra });

/** Plain text answer with cached input tokens. */
export const responsesText = sse([
  rev("response.created", { response: respObj("in_progress") }),
  rev("response.in_progress", { response: respObj("in_progress") }),
  rev("response.output_item.added", { output_index: 0, item: { id: "msg_1", type: "message", role: "assistant", status: "in_progress", content: [] } }),
  rev("response.content_part.added", { item_id: "msg_1", output_index: 0, content_index: 0, part: { type: "output_text", text: "" } }),
  rev("response.output_text.delta", { item_id: "msg_1", output_index: 0, content_index: 0, delta: "Hello" }),
  rev("response.output_text.delta", { item_id: "msg_1", output_index: 0, content_index: 0, delta: " there" }),
  rev("response.output_text.done", { item_id: "msg_1", output_index: 0, content_index: 0, text: "Hello there" }),
  rev("response.output_item.done", { output_index: 0, item: { id: "msg_1", type: "message", role: "assistant", status: "completed", content: [{ type: "output_text", text: "Hello there" }] } }),
  rev("response.completed", {
    response: respObj("completed", { usage: { input_tokens: 500, input_tokens_details: { cached_tokens: 128 }, output_tokens: 9, output_tokens_details: { reasoning_tokens: 0 }, total_tokens: 509 } }),
  }),
]);

/** Reasoning (two summary parts + encrypted content), then a function call. */
export const responsesReasoningToolCall = sse([
  rev("response.created", { response: respObj("in_progress") }),
  rev("response.output_item.added", { output_index: 0, item: { id: "rs_1", type: "reasoning", summary: [] } }),
  rev("response.reasoning_summary_part.added", { item_id: "rs_1", output_index: 0, summary_index: 0, part: { type: "summary_text", text: "" } }),
  rev("response.reasoning_summary_text.delta", { item_id: "rs_1", output_index: 0, summary_index: 0, delta: "Need to read" }),
  rev("response.reasoning_summary_text.delta", { item_id: "rs_1", output_index: 0, summary_index: 0, delta: " the file." }),
  rev("response.reasoning_summary_text.delta", { item_id: "rs_1", output_index: 0, summary_index: 1, delta: "Then answer." }),
  rev("response.output_item.done", {
    output_index: 0,
    item: { id: "rs_1", type: "reasoning", summary: [{ type: "summary_text", text: "Need to read the file." }, { type: "summary_text", text: "Then answer." }], encrypted_content: "gAAAAENCRYPTED==" },
  }),
  rev("response.output_item.added", { output_index: 1, item: { id: "fc_1", type: "function_call", status: "in_progress", call_id: "call_R1", name: "Read", arguments: "" } }),
  rev("response.function_call_arguments.delta", { item_id: "fc_1", output_index: 1, delta: '{"file_path":' }),
  rev("response.function_call_arguments.delta", { item_id: "fc_1", output_index: 1, delta: '"/a.ts"}' }),
  rev("response.function_call_arguments.done", { item_id: "fc_1", output_index: 1, arguments: '{"file_path":"/a.ts"}' }),
  rev("response.output_item.done", { output_index: 1, item: { id: "fc_1", type: "function_call", status: "completed", call_id: "call_R1", name: "Read", arguments: '{"file_path":"/a.ts"}' } }),
  rev("response.completed", {
    response: respObj("completed", { usage: { input_tokens: 300, input_tokens_details: { cached_tokens: 0 }, output_tokens: 80, output_tokens_details: { reasoning_tokens: 64 } } }),
  }),
]);

/** Output cut by max_output_tokens. */
export const responsesIncomplete = sse([
  rev("response.created", { response: respObj("in_progress") }),
  rev("response.output_item.added", { output_index: 0, item: { id: "msg_2", type: "message", role: "assistant", content: [] } }),
  rev("response.output_text.delta", { item_id: "msg_2", output_index: 0, content_index: 0, delta: "Partial" }),
  rev("response.incomplete", { response: respObj("incomplete", { incomplete_details: { reason: "max_output_tokens" }, usage: { input_tokens: 10, output_tokens: 5 } }) }),
]);

/** Failure after content started. */
export const responsesFailedAfterContent = sse([
  rev("response.created", { response: respObj("in_progress") }),
  rev("response.output_item.added", { output_index: 0, item: { id: "msg_3", type: "message", role: "assistant", content: [] } }),
  rev("response.output_text.delta", { item_id: "msg_3", output_index: 0, content_index: 0, delta: "Start" }),
  rev("response.failed", { response: respObj("failed", { error: { code: "server_error", message: "The model crashed" } }) }),
]);

/** Top-level error event before any content (not retryable). */
export const responsesErrorEvent = sse([rev("error", { code: "invalid_prompt", message: "Invalid prompt content", param: null })]);
