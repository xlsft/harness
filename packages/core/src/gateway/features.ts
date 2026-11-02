/** Request validation/normalization and per-attempt feature selection. */
import { ApiError } from "../lib/errors.ts";
import { downgradeEffort, type Effort, type ModelEntry } from "../services/models.ts";
import type { CanonicalRequest, ContentBlock, Message, TextBlock } from "./types.ts";

/** Names of features that capability negotiation can switch off for a model. */
export const NEGOTIABLE_FEATURES = [
  "thinking",
  "effort",
  "cache_control",
  "system_blocks",
  "stream_usage",
  "max_tokens_param",
  "parallel_tool_calls",
  "reasoning",
  "include",
] as const;
export type FeatureName = (typeof NEGOTIABLE_FEATURES)[number];

/** What an adapter may send upstream for this attempt. */
export interface Features {
  thinking: boolean;
  effort?: Effort;
  cache: boolean;
  /** Anthropic: send `system` as an array of blocks (otherwise a joined string). */
  systemBlocks: boolean;
  /** OpenAI: `stream_options.include_usage`. */
  streamUsage: boolean;
  /** OpenAI: use `max_tokens` (true) or `max_completion_tokens` (false). */
  maxTokensParam: boolean;
  /** OpenAI: allow sending `parallel_tool_calls`. */
  parallelToolCalls: boolean;
  /** OpenAI Responses: send the `reasoning` object (summary request; effort is governed by `effort`). */
  reasoning: boolean;
  /** OpenAI Responses: send `include: ["reasoning.encrypted_content"]` (stateless reasoning round-trip). */
  include: boolean;
  images: boolean;
  pdf: boolean;
}

export function computeFeatures(req: CanonicalRequest, model: ModelEntry, disabled: Set<string>): Features {
  const caps = model.capabilities;
  return {
    thinking: caps.thinking && !disabled.has("thinking"),
    effort: disabled.has("effort") ? undefined : downgradeEffort(req.effort, caps.effort),
    cache: caps.prompt_cache && !disabled.has("cache_control"),
    systemBlocks: !disabled.has("system_blocks"),
    streamUsage: !disabled.has("stream_usage"),
    maxTokensParam: !disabled.has("max_tokens_param"),
    parallelToolCalls: caps.parallel_tool_calls && !disabled.has("parallel_tool_calls"),
    reasoning: !disabled.has("reasoning"),
    include: !disabled.has("include"),
    images: caps.images,
    pdf: caps.pdf,
  };
}

const ROLES = new Set(["user", "assistant"]);
const BLOCK_TYPES = new Set(["text", "image", "document", "thinking", "redacted_thinking", "tool_use", "tool_result"]);

/** Validates and normalizes the client body into a CanonicalRequest. Throws ApiError(invalid_request). */
export function normalizeRequest(body: unknown, opts: { requireStream: boolean }): CanonicalRequest {
  if (!body || typeof body !== "object") throw new ApiError("invalid_request", "Body must be a JSON object");
  const b = body as Record<string, unknown>;
  if (typeof b.model !== "string" || !b.model) throw new ApiError("invalid_request", "model is required");
  if (opts.requireStream && b.stream !== true) {
    throw new ApiError("invalid_request", "Only stream: true is supported");
  }
  if (opts.requireStream && (typeof b.max_tokens !== "number" || !Number.isInteger(b.max_tokens) || b.max_tokens <= 0)) {
    throw new ApiError("invalid_request", "max_tokens must be a positive integer");
  }
  if (!Array.isArray(b.messages) || b.messages.length === 0) {
    throw new ApiError("invalid_request", "messages must be a non-empty array");
  }
  const messages: Message[] = b.messages.map((m, i) => {
    if (!m || typeof m !== "object" || !ROLES.has((m as Message).role)) {
      throw new ApiError("invalid_request", `messages[${i}].role must be user or assistant`);
    }
    const content = (m as Message).content;
    let blocks: ContentBlock[];
    if (typeof content === "string") blocks = [{ type: "text", text: content }];
    else if (Array.isArray(content)) blocks = content as ContentBlock[];
    else throw new ApiError("invalid_request", `messages[${i}].content must be a string or array`);
    blocks.forEach((blk, j) => {
      if (!blk || typeof blk !== "object" || !BLOCK_TYPES.has(blk.type)) {
        throw new ApiError("invalid_request", `messages[${i}].content[${j}] has unsupported type`);
      }
    });
    return { role: (m as Message).role, content: blocks };
  });

  let system: TextBlock[] = [];
  if (typeof b.system === "string") system = b.system ? [{ type: "text", text: b.system }] : [];
  else if (Array.isArray(b.system)) {
    system = b.system.map((s, i) => {
      if (!s || s.type !== "text" || typeof s.text !== "string") {
        throw new ApiError("invalid_request", `system[${i}] must be a text block`);
      }
      return s as TextBlock;
    });
  } else if (b.system !== undefined) throw new ApiError("invalid_request", "system must be an array of text blocks");

  if (b.tools !== undefined && !Array.isArray(b.tools)) throw new ApiError("invalid_request", "tools must be an array");
  for (const t of (b.tools as unknown[] | undefined) ?? []) {
    const tool = t as Record<string, unknown>;
    if (typeof tool.name !== "string" || typeof tool.input_schema !== "object" || tool.input_schema === null) {
      throw new ApiError("invalid_request", "each tool needs name and input_schema");
    }
  }

  return {
    ...(b as object),
    model: b.model,
    max_tokens: (b.max_tokens as number) ?? 1,
    system,
    messages,
    stream: true,
  } as CanonicalRequest;
}

export function blocksOf(m: Message): ContentBlock[] {
  return typeof m.content === "string" ? [{ type: "text", text: m.content }] : m.content;
}
