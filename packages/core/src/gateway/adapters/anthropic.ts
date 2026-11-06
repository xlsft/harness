/**
 * Anthropic Messages adapter. The canonical format is nearly Anthropic's own, so this is
 * mostly a passthrough: `cache: true` -> `cache_control`, `effort` -> `output_config.effort`,
 * thinking blocks (with signatures) are kept verbatim.
 */
import { parseSse } from "../../lib/sse.ts";
import { classifyStreamError } from "../errors.ts";
import { blocksOf, type FeatureName, type Features } from "../features.ts";
import type { AdapterEvent, CanonicalRequest, ContentBlock, StopReason, Usage, UpstreamFailure } from "../types.ts";
import { RESPONSES_SIGNATURE_PREFIX } from "./openai-responses.ts";
import { joinUrl, type Adapter, type AdapterTarget, type UpstreamRequest } from "./types.ts";

export const ANTHROPIC_VERSION = "2023-06-01";
const MAX_CACHE_BREAKPOINTS = 4;
const FAST_MODE_BETA = "fast-mode-2026-02-01";

const IMAGE_OMITTED = "[image omitted: the selected model does not accept images]";
const PDF_OMITTED = "[document omitted: the selected model does not accept PDF documents]";

export function normalizeAnthropicUsage(u: Record<string, unknown> | undefined): Partial<Usage> {
  if (!u) return {};
  const out: Partial<Usage> = {};
  for (const k of ["input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"] as const) {
    if (typeof u[k] === "number") out[k] = u[k] as number;
  }
  return out;
}

/** Builds the Anthropic request body. Exported for unit tests. */
export function toAnthropicBody(req: CanonicalRequest, target: AdapterTarget): Record<string, unknown> {
  const f = target.features;
  let breakpoints = 0;
  const cacheMark = (cache: unknown): Record<string, unknown> => {
    if (cache === true && f.cache && breakpoints < MAX_CACHE_BREAKPOINTS) {
      breakpoints++;
      // Five minutes is the default and is left unsaid, so a request with no choice stays byte for byte what it was.
      return { cache_control: { type: "ephemeral", ...(f.cacheTtl === "1h" && { ttl: "1h" }) } };
    }
    return {};
  };

  const convertBlock = (blk: ContentBlock): Record<string, unknown> | undefined => {
    const { cache, ...rest } = blk as ContentBlock & { cache?: boolean };
    switch (blk.type) {
      case "thinking":
        // Unsigned thinking (e.g. from an OpenAI-compatible model) is rejected by Anthropic.
        if (!f.thinking || !blk.signature) return undefined;
        // Signatures minted for OpenAI Responses reasoning items are meaningless to Anthropic.
        if (blk.signature.startsWith(RESPONSES_SIGNATURE_PREFIX)) return undefined;
        return { ...rest };
      case "redacted_thinking":
        if (!f.thinking) return undefined;
        return { ...rest };
      case "image":
        if (!f.images) return { type: "text", text: IMAGE_OMITTED, ...cacheMark(cache) };
        return { ...rest, ...cacheMark(cache) };
      case "document":
        if (!f.pdf) return { type: "text", text: PDF_OMITTED, ...cacheMark(cache) };
        return { ...rest, ...cacheMark(cache) };
      case "tool_result": {
        const content =
          typeof blk.content === "string" || blk.content === undefined
            ? blk.content
            : blk.content.map((c) => {
                const { cache: _c, ...inner } = c as typeof c & { cache?: boolean };
                if (c.type === "image" && !f.images) return { type: "text", text: IMAGE_OMITTED };
                return inner;
              });
        const out: Record<string, unknown> = { ...rest, ...cacheMark(cache) };
        if (content !== undefined) out.content = content;
        return out;
      }
      default:
        return { ...rest, ...cacheMark(cache) };
    }
  };

  // Tools come first in Anthropic's cache prefix order (tools -> system -> messages).
  const tools = req.tools?.map((t) => {
    const { cache, ...rest } = t;
    return { ...rest, ...cacheMark(cache) };
  });

  let system: unknown;
  if (req.system.length > 0) {
    if (f.systemBlocks) {
      system = req.system.map((s) => {
        const { cache, ...rest } = s;
        return { ...rest, ...cacheMark(cache) };
      });
    } else {
      system = req.system.map((s) => s.text).join("\n\n");
    }
  }

  const messages = req.messages
    .map((m) => ({ role: m.role, content: blocksOf(m).map(convertBlock).filter((b) => b !== undefined) }))
    .filter((m) => m.content.length > 0);

  const body: Record<string, unknown> = {
    model: target.model.upstreamModel,
    max_tokens: Math.min(req.max_tokens, target.model.maxOutputTokens),
    messages,
    stream: true,
  };
  if (system !== undefined) body.system = system;
  if (tools && tools.length) body.tools = tools;
  if (req.tool_choice && tools && tools.length) body.tool_choice = req.tool_choice;
  if (f.thinking && req.thinking && req.thinking.type !== "disabled") body.thinking = req.thinking;
  if (f.effort) body.output_config = { effort: f.effort };
  if (f.speed === "fast") body.speed = "fast";
  if (req.temperature !== undefined) body.temperature = req.temperature;
  if (req.top_p !== undefined) body.top_p = req.top_p;
  if (req.top_k !== undefined) body.top_k = req.top_k;
  if (req.stop_sequences?.length) body.stop_sequences = req.stop_sequences;
  if (target.model.extraBody) Object.assign(body, target.model.extraBody);
  return body;
}

function headers(target: AdapterTarget): Record<string, string> {
  const h: Record<string, string> = {
    "content-type": "application/json",
    accept: "text/event-stream",
    "anthropic-version": ANTHROPIC_VERSION,
    ...(target.features.speed === "fast" && { "anthropic-beta": FAST_MODE_BETA }),
  };
  if (target.provider.apiKey) h["x-api-key"] = target.provider.apiKey;
  return { ...h, ...target.provider.headers };
}

export const anthropicAdapter: Adapter = {
  buildRequest(req, target): UpstreamRequest {
    return { url: joinUrl(target.provider.baseUrl, "/v1/messages"), headers: headers(target), body: toAnthropicBody(req, target) };
  },

  buildCountTokens(req, target): UpstreamRequest {
    const body = toAnthropicBody(req, target);
    delete body.max_tokens;
    delete body.stream;
    delete body.temperature;
    delete body.top_p;
    delete body.top_k;
    delete body.stop_sequences;
    const h = headers(target);
    delete h.accept;
    return { url: joinUrl(target.provider.baseUrl, "/v1/messages/count_tokens"), headers: h, body };
  },

  async *translate(body): AsyncGenerator<AdapterEvent> {
    let stopped = false;
    for await (const msg of parseSse(body)) {
      if (msg.event === "ping") continue;
      let data: Record<string, unknown>;
      try {
        data = JSON.parse(msg.data);
      } catch {
        continue;
      }
      const type = (data.type as string) ?? msg.event;
      switch (type) {
        case "message_start": {
          const m = (data.message ?? {}) as Record<string, unknown>;
          yield { kind: "start", id: m.id as string | undefined, usage: normalizeAnthropicUsage(m.usage as Record<string, unknown>) };
          break;
        }
        case "content_block_start":
        case "content_block_delta":
        case "content_block_stop":
          yield { kind: "block", event: type, data };
          break;
        case "message_delta": {
          const d = (data.delta ?? {}) as Record<string, unknown>;
          stopped = true;
          yield {
            kind: "stop",
            stop_reason: (d.stop_reason as StopReason | null) ?? null,
            stop_sequence: (d.stop_sequence as string | null | undefined) ?? null,
            usage: normalizeAnthropicUsage(data.usage as Record<string, unknown>),
          };
          break;
        }
        case "message_stop":
          return;
        case "error": {
          const e = (data.error ?? {}) as Record<string, unknown>;
          yield { kind: "error", error: classifyStreamError(e.type as string, String(e.message ?? "Upstream stream error")) };
          return;
        }
        default:
          break;
      }
    }
    if (!stopped) {
      yield { kind: "error", error: { type: "upstream", message: "Upstream stream ended unexpectedly", retryable: true } };
    }
  },

  rejectedFeature(failure: UpstreamFailure, sent: UpstreamRequest, features: Features): FeatureName | undefined {
    if (failure.status !== 400 && failure.status !== 422) return undefined;
    const m = failure.raw ?? failure.message;
    if (/signature/i.test(m)) return undefined; // bad thinking signature in history: not a capability issue
    if (sent.body.thinking && /thinking/i.test(m)) return "thinking";
    if (sent.body.output_config && /effort|output_config/i.test(m)) return "effort";
    if (sent.body.speed && /speed|fast/i.test(m)) return "speed";
    if (features.cacheTtl && /\bttl\b/i.test(m)) return "cache_ttl";
    if (features.cache && /cache_control/i.test(m)) return "cache_control";
    if (Array.isArray(sent.body.system) && /system/i.test(m)) return "system_blocks";
    return undefined;
  },
};
