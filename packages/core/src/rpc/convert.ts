/**
 * Conversions between the protobuf wire types (src/gen) and the internal canonical
 * Anthropic-shaped JSON model used by services and upstream adapters.
 */
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { ErrorInfoSchema } from "../gen/harness/common/v1/errors_pb.ts";
import {
  Effort,
  Role,
  StopReason,
  Thinking_Mode,
  ToolChoice_Mode,
  type ContentBlock as PbContentBlock,
  type Message as PbMessage,
  type TextBlock as PbTextBlock,
  type Tool as PbTool,
  type ToolChoice as PbToolChoice,
  type Thinking as PbThinking,
  type UsageSchema,
} from "../gen/harness/llm/v1/content_pb.ts";
import {
  RequestClass,
  StreamMessageResponseSchema,
  type ListModelsResponseSchema,
  type StreamMessageRequest,
  type StreamMessageResponse,
} from "../gen/harness/llm/v1/gateway_pb.ts";
import type { PromptBundleSchema } from "../gen/harness/prompts/v1/prompts_pb.ts";
import { DYNAMIC_BOUNDARY, type PromptBundle } from "../services/prompts.ts";
import type { ContentBlock, GatewayEvent, Message, StopReason as CanonStop, Tool, ToolChoice, Usage } from "../gateway/types.ts";
import { ApiError } from "../lib/errors.ts";
import { errorType } from "./errors.ts";

// --- enums -------------------------------------------------------------------------

const EFFORT_TO_STR: Record<number, string> = {
  [Effort.LOW]: "low",
  [Effort.MEDIUM]: "medium",
  [Effort.HIGH]: "high",
  [Effort.XHIGH]: "xhigh",
  [Effort.MAX]: "max",
};
const EFFORT_FROM_STR: Record<string, Effort> = Object.fromEntries(
  Object.entries(EFFORT_TO_STR).map(([k, v]) => [v, Number(k) as Effort]),
);

const REQUEST_CLASS: Record<number, string> = {
  [RequestClass.MAIN]: "main",
  [RequestClass.SUBAGENT]: "subagent",
  [RequestClass.COMPACTION]: "compaction",
  [RequestClass.AUXILIARY]: "auxiliary",
};

const STOP_FROM_STR: Record<string, StopReason> = {
  end_turn: StopReason.END_TURN,
  tool_use: StopReason.TOOL_USE,
  max_tokens: StopReason.MAX_TOKENS,
  stop_sequence: StopReason.STOP_SEQUENCE,
  refusal: StopReason.REFUSAL,
  pause_turn: StopReason.PAUSE_TURN,
};

export function effortToProto(e: string): Effort {
  return EFFORT_FROM_STR[e] ?? Effort.UNSPECIFIED;
}

export function effortName(e: Effort): string {
  return EFFORT_TO_STR[e] ?? "";
}

// --- request: proto -> canonical ------------------------------------------------------

function b64(data: Uint8Array): string {
  return Buffer.from(data.buffer, data.byteOffset, data.byteLength).toString("base64");
}

function parseJson(text: string, what: string): unknown {
  if (!text) return {};
  try {
    return JSON.parse(text);
  } catch {
    throw new ApiError("invalid_request", `${what} is not valid JSON`);
  }
}

function textBlock(t: PbTextBlock): ContentBlock {
  return t.cache ? { type: "text", text: t.text, cache: true } : { type: "text", text: t.text };
}

export function blockFromProto(b: PbContentBlock, path: string): ContentBlock {
  const v = b.block;
  switch (v.case) {
    case "text":
      return textBlock(v.value);
    case "image":
      return {
        type: "image",
        source: { type: "base64", media_type: v.value.mediaType, data: b64(v.value.data) },
        ...(v.value.cache ? { cache: true } : {}),
      };
    case "document":
      return {
        type: "document",
        source: { type: "base64", media_type: v.value.mediaType || "application/pdf", data: b64(v.value.data) },
        ...(v.value.title ? { title: v.value.title } : {}),
        ...(v.value.cache ? { cache: true } : {}),
      };
    case "thinking":
      return { type: "thinking", thinking: v.value.thinking, signature: v.value.signature };
    case "redactedThinking":
      return { type: "redacted_thinking", data: v.value.data };
    case "toolUse":
      return { type: "tool_use", id: v.value.id, name: v.value.name, input: parseJson(v.value.inputJson, `${path}.input_json`) };
    case "toolResult": {
      const content = v.value.content.map((c, i) => {
        const blk = blockFromProto(c, `${path}.content[${i}]`);
        if (blk.type !== "text" && blk.type !== "image") {
          throw new ApiError("invalid_request", `${path}.content[${i}]: only text and image blocks are allowed in tool results`);
        }
        return blk;
      });
      return {
        type: "tool_result",
        tool_use_id: v.value.toolUseId,
        is_error: v.value.isError,
        content,
        ...(v.value.cache ? { cache: true } : {}),
      };
    }
    default:
      throw new ApiError("invalid_request", `${path}: empty content block`);
  }
}

export function messagesFromProto(ms: PbMessage[]): Message[] {
  return ms.map((m, i) => {
    if (m.role !== Role.USER && m.role !== Role.ASSISTANT) {
      throw new ApiError("invalid_request", `messages[${i}].role must be USER or ASSISTANT`);
    }
    return {
      role: m.role === Role.USER ? "user" : "assistant",
      content: m.content.map((c, j) => blockFromProto(c, `messages[${i}].content[${j}]`)),
    };
  });
}

export function toolsFromProto(ts: PbTool[]): Tool[] {
  return ts.map((t, i) => ({
    name: t.name,
    description: t.description,
    input_schema: parseJson(t.inputSchemaJson || '{"type":"object"}', `tools[${i}].input_schema_json`) as Record<string, unknown>,
  }));
}

function toolChoiceFromProto(tc: PbToolChoice | undefined): ToolChoice | undefined {
  if (!tc) return undefined;
  const dp = tc.disableParallelToolUse ? { disable_parallel_tool_use: true } : {};
  switch (tc.mode) {
    case ToolChoice_Mode.ANY:
      return { type: "any", ...dp };
    case ToolChoice_Mode.TOOL:
      if (!tc.toolName) throw new ApiError("invalid_request", "tool_choice.tool_name is required for MODE_TOOL");
      return { type: "tool", name: tc.toolName, ...dp };
    case ToolChoice_Mode.NONE:
      return { type: "none" };
    default:
      return { type: "auto", ...dp };
  }
}

function thinkingFromProto(t: PbThinking | undefined) {
  if (!t) return undefined;
  if (t.mode === Thinking_Mode.ENABLED) return { type: "enabled" as const, budget_tokens: t.budgetTokens };
  if (t.mode === Thinking_Mode.ADAPTIVE) return { type: "adaptive" as const };
  return undefined;
}

/** Builds the JSON body accepted by services/gateway.prepareMessages(). */
export function streamRequestToCanonical(r: StreamMessageRequest): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: r.model,
    max_tokens: r.maxTokens,
    system: r.system.map(textBlock),
    messages: messagesFromProto(r.messages),
    stream: true,
  };
  if (r.tools.length) body.tools = toolsFromProto(r.tools);
  const tc = toolChoiceFromProto(r.toolChoice);
  if (tc) body.tool_choice = tc;
  const th = thinkingFromProto(r.thinking);
  if (th) body.thinking = th;
  if (r.effort !== Effort.UNSPECIFIED) body.effort = EFFORT_TO_STR[r.effort];
  if (r.stopSequences.length) body.stop_sequences = r.stopSequences;
  if (r.temperature !== undefined) body.temperature = r.temperature;
  if (r.metadata) {
    body.metadata = {
      session_id: r.metadata.sessionId || undefined,
      request_class: REQUEST_CLASS[r.metadata.requestClass],
      agent_id: r.metadata.agentId || undefined,
      prompt_bundle_version: r.metadata.promptBundleVersion || undefined,
    };
  }
  return body;
}

// --- response: canonical events -> proto ------------------------------------------------

function usageToProto(u: Partial<Usage> | undefined): MessageInitShape<typeof UsageSchema> {
  return {
    inputTokens: BigInt(u?.input_tokens ?? 0),
    outputTokens: BigInt(u?.output_tokens ?? 0),
    cacheReadInputTokens: BigInt(u?.cache_read_input_tokens ?? 0),
    cacheCreationInputTokens: BigInt(u?.cache_creation_input_tokens ?? 0),
  };
}

function startBlockToProto(cb: Record<string, unknown>) {
  switch (cb.type) {
    case "text":
      return { block: { case: "text" as const, value: { text: String(cb.text ?? "") } } };
    case "thinking":
      return { block: { case: "thinking" as const, value: { thinking: String(cb.thinking ?? ""), signature: String(cb.signature ?? "") } } };
    case "redacted_thinking":
      return { block: { case: "redactedThinking" as const, value: { data: String(cb.data ?? "") } } };
    case "tool_use":
      return { block: { case: "toolUse" as const, value: { id: String(cb.id ?? ""), name: String(cb.name ?? ""), inputJson: "" } } };
    default:
      return undefined;
  }
}

/** A gateway `error` event as a thrown service error (ends the Connect stream). */
export class StreamFailure extends ApiError {}

/**
 * Converts one canonical gateway event into a StreamMessageResponse.
 * Returns undefined for events with no proto equivalent; throws for `error`.
 */
export function eventToProto(ev: GatewayEvent): StreamMessageResponse | undefined {
  const d = ev.data as Record<string, any>;
  switch (ev.event) {
    case "message_start":
      return create(StreamMessageResponseSchema, {
        event: { case: "messageStart", value: { id: d.message.id, model: d.message.model, usage: usageToProto(d.message.usage) } },
      });
    case "content_block_start": {
      const block = startBlockToProto(d.content_block ?? {});
      if (!block) return undefined;
      return create(StreamMessageResponseSchema, { event: { case: "contentBlockStart", value: { index: d.index, block } } });
    }
    case "content_block_delta": {
      const delta = d.delta ?? {};
      let value: { case: "text" | "thinking" | "signature" | "inputJson"; value: string } | undefined;
      if (delta.type === "text_delta") value = { case: "text", value: delta.text };
      else if (delta.type === "thinking_delta") value = { case: "thinking", value: delta.thinking };
      else if (delta.type === "signature_delta") value = { case: "signature", value: delta.signature };
      else if (delta.type === "input_json_delta") value = { case: "inputJson", value: delta.partial_json };
      if (!value) return undefined;
      return create(StreamMessageResponseSchema, { event: { case: "contentBlockDelta", value: { index: d.index, delta: value } } });
    }
    case "content_block_stop":
      return create(StreamMessageResponseSchema, { event: { case: "contentBlockStop", value: { index: d.index } } });
    case "message_delta":
      return create(StreamMessageResponseSchema, {
        event: {
          case: "messageDelta",
          value: {
            stopReason: STOP_FROM_STR[d.delta?.stop_reason as CanonStop] ?? StopReason.UNSPECIFIED,
            stopSequence: d.delta?.stop_sequence ?? "",
            usage: usageToProto(d.usage),
          },
        },
      });
    case "message_stop":
      return create(StreamMessageResponseSchema, {
        event: {
          case: "messageStop",
          value: {
            costUsd: d.harness?.cost_usd ?? 0,
            upstreamModel: d.harness?.upstream_model ?? "",
            retries: d.harness?.retries ?? 0,
            servedModel: d.harness?.served_model ?? "",
          },
        },
      });
    case "harness_retry":
      return create(StreamMessageResponseSchema, {
        event: {
          case: "retry",
          value: {
            attempt: d.attempt,
            maxRetries: d.max_retries,
            retryDelayMs: BigInt(d.retry_delay_ms ?? 0),
            error: create(ErrorInfoSchema, {
              type: errorType(d.error?.type ?? "upstream"),
              retryAfterMs: BigInt(d.retry_delay_ms ?? 0),
              upstreamStatus: d.error?.upstream_status ?? 0,
            }),
          },
        },
      });
    case "ping":
      return create(StreamMessageResponseSchema, { event: { case: "ping", value: {} } });
    case "error":
      throw new StreamFailure(d.error?.type ?? "internal", d.error?.message ?? "Gateway error", {
        retryAfterMs: d.error?.retry_after_ms,
        upstreamStatus: d.error?.upstream_status,
      });
    default:
      return undefined;
  }
}

// --- models / prompts -----------------------------------------------------------------

export function modelsToProto(r: {
  models: Record<string, any>[];
  slots: Record<string, string | undefined>;
}): MessageInitShape<typeof ListModelsResponseSchema> {
  return {
    models: r.models.map((m) => ({
      id: m.id,
      displayName: m.display_name,
      provider: m.provider,
      providerName: m.provider_name,
      displayId: m.display_id,
      contextWindow: BigInt(m.context_window),
      maxOutputTokens: BigInt(m.max_output_tokens),
      capabilities: {
        thinking: m.capabilities.thinking,
        effort: (m.capabilities.effort as string[]).map(effortToProto),
        images: m.capabilities.images,
        pdf: m.capabilities.pdf,
        parallelToolCalls: m.capabilities.parallel_tool_calls,
        promptCache: m.capabilities.prompt_cache,
        defaultEffort: effortToProto(m.capabilities.default_effort ?? ""),
      },
      pricing: m.pricing
        ? {
            input: m.pricing.input,
            output: m.pricing.output,
            cacheRead: m.pricing.cache_read ?? m.pricing.input,
            cacheWrite: m.pricing.cache_write ?? m.pricing.input,
          }
        : undefined,
    })),
    slots: { main: r.slots.main ?? "", small: r.slots.small ?? "", plan: r.slots.plan ?? "", title: r.slots.title ?? "" },
  };
}

export function bundleToProto(b: PromptBundle): MessageInitShape<typeof PromptBundleSchema> {
  return {
    version: b.version,
    model: b.model,
    system: b.system.map((s) =>
      s.id === DYNAMIC_BOUNDARY ? { id: DYNAMIC_BOUNDARY, text: "", dynamicBoundary: true } : { id: s.id, text: (s as { text: string }).text, dynamicBoundary: false },
    ),
    tools: Object.fromEntries(
      Object.entries(b.tools).map(([name, t]) => [name, { description: t.description, inputSchemaJson: JSON.stringify(t.input_schema) }]),
    ),
    reminders: b.reminders,
    compactionInstruction: b.compaction.instruction,
    titleInstruction: b.title.instruction,
    outputStyles: Object.fromEntries(
      Object.entries(b.output_styles).map(([id, s]) => [
        id,
        { id: s.id, name: s.name, description: s.description, text: s.text, keepCodingInstructions: s.keep_coding_instructions },
      ]),
    ),
  };
}
