/**
 * OpenAI Chat Completions adapter (OpenAI, DeepSeek, OpenRouter, vLLM, Ollama, ...).
 *
 * Request: system blocks -> one system message; tool_result -> `tool` messages, images from
 * tool results -> a following user message; assistant thinking -> `reasoning_content`;
 * tools -> functions; effort -> reasoning_effort.
 *
 * Stream: deltas are re-shaped into Anthropic content_block_* events (text, thinking from
 * `reasoning_content`/`reasoning`, tool_use with input_json_delta), finish_reason -> stop_reason,
 * usage normalized (cached tokens -> cache_read_input_tokens).
 */
import { randomBytes } from "node:crypto";
import { parseSse } from "../../lib/sse.ts";
import { classifyStreamError } from "../errors.ts";
import { blocksOf, type FeatureName, type Features } from "../features.ts";
import type {
  AdapterEvent,
  CanonicalRequest,
  ContentBlock,
  ImageBlock,
  StopReason,
  TextBlock,
  Usage,
  UpstreamFailure,
} from "../types.ts";
import { joinUrl, type Adapter, type AdapterTarget, type UpstreamRequest } from "./types.ts";

const IMAGE_OMITTED = "[image omitted: the selected model does not accept images]";
const PDF_OMITTED = "[document omitted: the selected model does not accept PDF documents]";

type OAContentPart =
  | { type: "text"; text: string }
  | { type: "image_url"; image_url: { url: string } }
  | { type: "file"; file: { filename: string; file_data: string } };

type OAMessage =
  | { role: "system"; content: string }
  | { role: "user"; content: string | OAContentPart[] }
  | {
      role: "assistant";
      content: string | null;
      reasoning_content?: string;
      tool_calls?: { id: string; type: "function"; function: { name: string; arguments: string } }[];
    }
  | { role: "tool"; tool_call_id: string; content: string };

export const FINISH_REASON_MAP: Record<string, StopReason> = {
  stop: "end_turn",
  tool_calls: "tool_use",
  function_call: "tool_use",
  length: "max_tokens",
  content_filter: "refusal",
};

function imagePart(img: ImageBlock, f: Features): OAContentPart {
  if (!f.images) return { type: "text", text: IMAGE_OMITTED };
  const url = img.source.type === "base64" ? `data:${img.source.media_type};base64,${img.source.data}` : img.source.url;
  return { type: "image_url", image_url: { url } };
}

function userPart(blk: ContentBlock, f: Features): OAContentPart | undefined {
  switch (blk.type) {
    case "text":
      return { type: "text", text: blk.text };
    case "image":
      return imagePart(blk, f);
    case "document":
      if (!f.pdf || blk.source.type !== "base64") {
        if (blk.source.type === "text") return { type: "text", text: blk.source.data };
        return { type: "text", text: PDF_OMITTED };
      }
      return {
        type: "file",
        file: { filename: blk.title ?? "document.pdf", file_data: `data:${blk.source.media_type};base64,${blk.source.data}` },
      };
    default:
      return undefined;
  }
}

/** Collapses all-text parts into a plain string (widest compatibility across OpenAI-compatible servers). */
function packUserContent(parts: OAContentPart[]): string | OAContentPart[] {
  if (parts.every((p) => p.type === "text")) return parts.map((p) => (p as { text: string }).text).join("\n\n");
  return parts;
}

function toolResultText(content: string | (TextBlock | ImageBlock)[] | undefined): string {
  if (content === undefined) return "";
  if (typeof content === "string") return content;
  return content
    .filter((c): c is TextBlock => c.type === "text")
    .map((c) => c.text)
    .join("\n");
}

/** Canonical messages -> OpenAI chat messages. Exported for unit tests. */
export function toOpenAIMessages(req: CanonicalRequest, f: Features): OAMessage[] {
  const out: OAMessage[] = [];
  if (req.system.length) out.push({ role: "system", content: req.system.map((s) => s.text).join("\n\n") });

  for (const m of req.messages) {
    const blocks = blocksOf(m);
    if (m.role === "assistant") {
      const text = blocks
        .filter((b): b is TextBlock => b.type === "text")
        .map((b) => b.text)
        .join("");
      const reasoning = blocks
        .filter((b) => b.type === "thinking")
        .map((b) => (b as { thinking: string }).thinking)
        .join("");
      const toolCalls = blocks
        .filter((b) => b.type === "tool_use")
        .map((b) => {
          const t = b as { id: string; name: string; input: unknown };
          return { id: t.id, type: "function" as const, function: { name: t.name, arguments: JSON.stringify(t.input ?? {}) } };
        });
      if (!text && toolCalls.length === 0) continue;
      const msg: OAMessage = { role: "assistant", content: text || null };
      if (toolCalls.length) msg.tool_calls = toolCalls;
      if (f.thinking && reasoning) msg.reasoning_content = reasoning;
      out.push(msg);
      continue;
    }

    // user
    const toolMsgs: OAMessage[] = [];
    const toolImages: OAContentPart[] = [];
    const parts: OAContentPart[] = [];
    for (const blk of blocks) {
      if (blk.type === "tool_result") {
        let text = toolResultText(blk.content);
        if (blk.is_error && !/^error/i.test(text)) text = `Error: ${text}`;
        toolMsgs.push({ role: "tool", tool_call_id: blk.tool_use_id, content: text || "(no output)" });
        const images = Array.isArray(blk.content) ? blk.content.filter((c): c is ImageBlock => c.type === "image") : [];
        if (images.length) {
          toolImages.push({ type: "text", text: `Image output of tool call ${blk.tool_use_id}:` });
          for (const img of images) toolImages.push(imagePart(img, f));
        }
        continue;
      }
      const p = userPart(blk, f);
      if (p) parts.push(p);
    }
    out.push(...toolMsgs);
    const all = [...toolImages, ...parts];
    if (all.length) out.push({ role: "user", content: packUserContent(all) });
  }
  return out;
}

export function toOpenAIBody(req: CanonicalRequest, target: AdapterTarget): Record<string, unknown> {
  const f = target.features;
  const body: Record<string, unknown> = {
    model: target.model.upstreamModel,
    messages: toOpenAIMessages(req, f),
    stream: true,
  };
  const maxTokens = Math.min(req.max_tokens, target.model.maxOutputTokens);
  if (f.maxTokensParam) body.max_tokens = maxTokens;
  else body.max_completion_tokens = maxTokens;
  if (f.streamUsage) body.stream_options = { include_usage: true };

  if (req.tools?.length) {
    body.tools = req.tools.map((t) => ({
      type: "function",
      function: { name: t.name, description: t.description ?? "", parameters: t.input_schema },
    }));
    const tc = req.tool_choice;
    if (tc) {
      if (tc.type === "auto") body.tool_choice = "auto";
      else if (tc.type === "any") body.tool_choice = "required";
      else if (tc.type === "none") body.tool_choice = "none";
      else if (tc.type === "tool") body.tool_choice = { type: "function", function: { name: tc.name } };
      if (tc.type !== "none" && tc.disable_parallel_tool_use && f.parallelToolCalls) body.parallel_tool_calls = false;
    }
  }
  if (f.effort) body.reasoning_effort = f.effort;
  if (req.temperature !== undefined) body.temperature = req.temperature;
  if (req.top_p !== undefined) body.top_p = req.top_p;
  if (req.stop_sequences?.length) body.stop = req.stop_sequences;
  if (target.model.extraBody) Object.assign(body, target.model.extraBody);
  return body;
}

export function normalizeOpenAIUsage(u: Record<string, any> | undefined | null): Partial<Usage> {
  if (!u) return {};
  const prompt = Number(u.prompt_tokens ?? 0);
  const cached = Number(u.prompt_tokens_details?.cached_tokens ?? u.prompt_cache_hit_tokens ?? 0);
  const cacheWrite = Number(u.prompt_tokens_details?.cache_write_tokens ?? 0);
  return {
    input_tokens: Math.max(0, prompt - cached - cacheWrite),
    output_tokens: Number(u.completion_tokens ?? 0),
    cache_read_input_tokens: cached,
    cache_creation_input_tokens: cacheWrite,
  };
}

function toolId(): string {
  return `toolu_${randomBytes(12).toString("base64url")}`;
}

interface ToolCallState {
  id: string;
  name: string;
  args: string;
  /** buffered: not emitted yet; live: its block is open and streams; done: its block is closed. */
  status: "buffered" | "live" | "done";
}

function isCompleteJson(s: string): boolean {
  if (!s.trim()) return false;
  try {
    JSON.parse(s);
    return true;
  } catch {
    return false;
  }
}

/**
 * Stateful translator from OpenAI chat chunks to Anthropic-shaped block events. Exported for unit tests.
 *
 * Tool calls: OpenAI streams them by `index` and some upstreams interleave deltas of several calls.
 * Anthropic blocks are sequential (one open block at a time), so one call streams live and the others
 * are buffered per index. A buffered call is promoted (start + buffered arguments in one delta) once
 * the live call's arguments form complete JSON; whatever is still buffered at the end is emitted in
 * index order.
 */
export class OpenAIStreamTranslator {
  private index = -1;
  private open: { kind: "text" | "thinking" | "tool"; toolIndex?: number } | null = null;
  private tools = new Map<number, ToolCallState>();
  private sawTool = false;
  started = false;
  finish: string | null = null;
  usage: Partial<Usage> | undefined;

  private close(out: AdapterEvent[]) {
    if (!this.open) return;
    out.push({ kind: "block", event: "content_block_stop", data: { type: "content_block_stop", index: this.index } });
    if (this.open.kind === "tool" && this.open.toolIndex !== undefined) {
      const t = this.tools.get(this.open.toolIndex);
      if (t) t.status = "done";
    }
    this.open = null;
  }

  private openBlock(out: AdapterEvent[], kind: "text" | "thinking", block: Record<string, unknown>) {
    this.close(out);
    this.index++;
    this.open = { kind };
    out.push({ kind: "block", event: "content_block_start", data: { type: "content_block_start", index: this.index, content_block: block } });
  }

  private argsDelta(out: AdapterEvent[], args: string) {
    out.push({
      kind: "block",
      event: "content_block_delta",
      data: { type: "content_block_delta", index: this.index, delta: { type: "input_json_delta", partial_json: args } },
    });
  }

  /** Opens the block of a buffered tool call and emits everything buffered for it so far. */
  private startTool(out: AdapterEvent[], ti: number, t: ToolCallState) {
    this.close(out);
    this.index++;
    this.open = { kind: "tool", toolIndex: ti };
    t.status = "live";
    if (!t.id) t.id = toolId();
    out.push({
      kind: "block",
      event: "content_block_start",
      data: { type: "content_block_start", index: this.index, content_block: { type: "tool_use", id: t.id, name: t.name, input: {} } },
    });
    if (t.args) this.argsDelta(out, t.args);
  }

  /** Lowest-index buffered tool call. */
  private nextBuffered(): [number, ToolCallState] | undefined {
    let best: [number, ToolCallState] | undefined;
    for (const [i, t] of this.tools) if (t.status === "buffered" && (!best || i < best[0])) best = [i, t];
    return best;
  }

  /** Promotes buffered calls in index order while no call is live or the live one is complete. */
  private promote(out: AdapterEvent[]) {
    for (;;) {
      const live = this.open?.kind === "tool" ? this.tools.get(this.open.toolIndex!) : undefined;
      if (live && !isCompleteJson(live.args)) return;
      const next = this.nextBuffered();
      if (!next) return;
      this.startTool(out, next[0], next[1]);
    }
  }

  private pushToolDelta(out: AdapterEvent[], tc: Record<string, any>) {
    const ti: number = typeof tc.index === "number" ? tc.index : 0;
    const args = typeof tc.function?.arguments === "string" ? tc.function.arguments : "";
    let t = this.tools.get(ti);
    if (!t) {
      t = { id: "", name: "", args: "", status: "buffered" };
      this.tools.set(ti, t);
      this.sawTool = true;
    }
    if (typeof tc.id === "string" && tc.id && !t.id) t.id = tc.id;
    if (typeof tc.function?.name === "string" && tc.function.name && !t.name) t.name = tc.function.name;
    switch (t.status) {
      case "live":
        t.args += args;
        if (args) this.argsDelta(out, args);
        // As soon as the live call is complete, buffered calls can stream instead of waiting for the end.
        if (args && this.nextBuffered()) this.promote(out);
        return;
      case "done":
        // Only reachable after the call's block was closed by text/thinking or because its arguments
        // were already complete JSON; more arguments cannot be appended to a closed block.
        if (args) console.warn(`[harness] dropped arguments for closed tool call ${t.id}`);
        return;
      case "buffered":
        t.args += args;
        if (this.open?.kind !== "tool") {
          // No call is streaming: start this one now (text/thinking before it gets closed).
          this.startTool(out, ti, t);
          return;
        }
        this.promote(out);
    }
  }

  /** Processes one parsed chunk; returns events to emit. */
  push(chunk: Record<string, any>): AdapterEvent[] {
    const out: AdapterEvent[] = [];
    if (!this.started) {
      this.started = true;
      out.push({ kind: "start", id: typeof chunk.id === "string" ? chunk.id : undefined });
    }
    if (chunk.usage) this.usage = normalizeOpenAIUsage(chunk.usage);
    const choice = Array.isArray(chunk.choices) ? chunk.choices[0] : undefined;
    if (!choice) return out;
    const delta = choice.delta ?? {};

    const reasoning = typeof delta.reasoning_content === "string" ? delta.reasoning_content : typeof delta.reasoning === "string" ? delta.reasoning : "";
    if (reasoning) {
      if (this.open?.kind !== "thinking") this.openBlock(out, "thinking", { type: "thinking", thinking: "", signature: "" });
      out.push({
        kind: "block",
        event: "content_block_delta",
        data: { type: "content_block_delta", index: this.index, delta: { type: "thinking_delta", thinking: reasoning } },
      });
    }

    if (typeof delta.content === "string" && delta.content) {
      if (this.open?.kind !== "text") this.openBlock(out, "text", { type: "text", text: "" });
      out.push({
        kind: "block",
        event: "content_block_delta",
        data: { type: "content_block_delta", index: this.index, delta: { type: "text_delta", text: delta.content } },
      });
    }

    if (Array.isArray(delta.tool_calls)) {
      for (const tc of delta.tool_calls) if (tc && typeof tc === "object") this.pushToolDelta(out, tc);
    }

    if (choice.finish_reason) this.finish = choice.finish_reason;
    return out;
  }

  /** Called at end of stream. */
  end(): AdapterEvent[] {
    const out: AdapterEvent[] = [];
    this.close(out);
    // Flush calls that were still buffered (interleaved with the live one), in index order.
    for (let next = this.nextBuffered(); next; next = this.nextBuffered()) {
      this.startTool(out, next[0], next[1]);
      this.close(out);
    }
    let stop: StopReason = FINISH_REASON_MAP[this.finish ?? "stop"] ?? "end_turn";
    if (this.sawTool && stop === "end_turn") stop = "tool_use";
    out.push({ kind: "stop", stop_reason: stop, stop_sequence: null, usage: this.usage });
    return out;
  }
}

export const openaiChatAdapter: Adapter = {
  buildRequest(req, target): UpstreamRequest {
    const h: Record<string, string> = { "content-type": "application/json", accept: "text/event-stream" };
    if (target.provider.apiKey) h.authorization = `Bearer ${target.provider.apiKey}`;
    return {
      url: joinUrl(target.provider.baseUrl, "/chat/completions"),
      headers: { ...h, ...target.provider.headers },
      body: toOpenAIBody(req, target),
    };
  },

  async *translate(body): AsyncGenerator<AdapterEvent> {
    const tr = new OpenAIStreamTranslator();
    let done = false;
    for await (const msg of parseSse(body)) {
      const data = msg.data.trim();
      if (data === "[DONE]") {
        done = true;
        break;
      }
      if (!data) continue;
      let chunk: Record<string, any>;
      try {
        chunk = JSON.parse(data);
      } catch {
        continue;
      }
      if (chunk.error) {
        const e = chunk.error;
        const code = String(e.type ?? e.code ?? "");
        const type = /overload/i.test(code) || e.code === 529 || e.code === 503 ? "overloaded_error" : /rate/i.test(code) || e.code === 429 ? "rate_limit_error" : "api_error";
        yield { kind: "error", error: classifyStreamError(type, String(e.message ?? "Upstream stream error")) };
        return;
      }
      for (const ev of tr.push(chunk)) yield ev;
    }
    if (!done && tr.finish === null) {
      yield { kind: "error", error: { type: "upstream", message: "Upstream stream ended unexpectedly", retryable: true } };
      return;
    }
    for (const ev of tr.end()) yield ev;
  },

  rejectedFeature(failure: UpstreamFailure, sent: UpstreamRequest, _features: Features): FeatureName | undefined {
    if (failure.status !== 400 && failure.status !== 422) return undefined;
    const m = failure.raw ?? failure.message;
    const msgs = (sent.body.messages as OAMessage[]) ?? [];
    if (/reasoning_content/i.test(m) && msgs.some((x) => x.role === "assistant" && x.reasoning_content)) return "thinking";
    if (sent.body.reasoning_effort && /reasoning|effort/i.test(m)) return "effort";
    if (sent.body.stream_options && /stream_options|include_usage/i.test(m)) return "stream_usage";
    if (sent.body.max_tokens !== undefined && /max_completion_tokens/i.test(m)) return "max_tokens_param";
    if (sent.body.parallel_tool_calls !== undefined && /parallel_tool_calls/i.test(m)) return "parallel_tool_calls";
    return undefined;
  },
};
