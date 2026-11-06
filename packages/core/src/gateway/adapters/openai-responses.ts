/**
 * OpenAI Responses API adapter (`POST {base_url}/responses`, streaming). Works with OpenAI and
 * Responses-compatible endpoints (e.g. OpenRouter); the base URL includes `/v1` as for openai-chat.
 *
 * Request: system blocks -> `instructions`; messages -> input items (tool_use -> `function_call`,
 * tool_result -> `function_call_output`, images from tool results -> a follow-up user item);
 * tools -> `{type:"function", ..., strict:false}`; effort -> `reasoning.effort`; max_tokens ->
 * `max_output_tokens`.
 *
 * Reasoning without server-side state: we send `store: false` + `include: ["reasoning.encrypted_content"]`.
 * A returned reasoning item becomes a canonical thinking block: summary text -> `thinking`, and the
 * item itself (id, summary, encrypted_content) is encoded into `signature` with the
 * RESPONSES_SIGNATURE_PREFIX. On the next request such blocks are decoded back into the exact
 * reasoning input item and sent before the related function_call items. Signatures from any other
 * upstream are dropped (and the Anthropic adapter drops ours).
 */
import { parseSse } from "../../lib/sse.ts";
import { classifyStreamError } from "../errors.ts";
import { blocksOf, type FeatureName, type Features } from "../features.ts";
import type { AdapterEvent, CanonicalRequest, ContentBlock, ImageBlock, StopReason, TextBlock, Usage, UpstreamFailure } from "../types.ts";
import { joinUrl, type Adapter, type AdapterTarget, type UpstreamRequest } from "./types.ts";

export const RESPONSES_SIGNATURE_PREFIX = "oair1.";

const IMAGE_OMITTED = "[image omitted: the selected model does not accept images]";
const PDF_OMITTED = "[document omitted: the selected model does not accept PDF documents]";

interface ReasoningItem {
  type: "reasoning";
  id: string;
  summary: { type: "summary_text"; text: string }[];
  encrypted_content: string;
}

export function encodeReasoningSignature(item: { id: string; summary?: unknown; encrypted_content: string }): string {
  const summary = Array.isArray(item.summary)
    ? item.summary
        .filter((s): s is { text: string } => !!s && typeof (s as { text?: unknown }).text === "string")
        .map((s) => ({ type: "summary_text" as const, text: s.text }))
    : [];
  const payload = { id: item.id, summary, encrypted_content: item.encrypted_content };
  return RESPONSES_SIGNATURE_PREFIX + Buffer.from(JSON.stringify(payload), "utf8").toString("base64url");
}

/** Rebuilds the reasoning input item from a signature; undefined for foreign or malformed signatures. */
export function decodeReasoningSignature(signature: string | undefined): ReasoningItem | undefined {
  if (!signature?.startsWith(RESPONSES_SIGNATURE_PREFIX)) return undefined;
  try {
    const p = JSON.parse(Buffer.from(signature.slice(RESPONSES_SIGNATURE_PREFIX.length), "base64url").toString("utf8"));
    if (typeof p?.id !== "string" || typeof p?.encrypted_content !== "string" || !Array.isArray(p.summary)) return undefined;
    return { type: "reasoning", id: p.id, summary: p.summary, encrypted_content: p.encrypted_content };
  } catch {
    return undefined;
  }
}

type InputPart =
  | { type: "input_text"; text: string }
  | { type: "input_image"; image_url: string }
  | { type: "input_file"; filename: string; file_data: string };

function imagePart(img: ImageBlock, f: Features): InputPart {
  if (!f.images) return { type: "input_text", text: IMAGE_OMITTED };
  const url = img.source.type === "base64" ? `data:${img.source.media_type};base64,${img.source.data}` : img.source.url;
  return { type: "input_image", image_url: url };
}

function userPart(blk: ContentBlock, f: Features): InputPart | undefined {
  switch (blk.type) {
    case "text":
      return { type: "input_text", text: blk.text };
    case "image":
      return imagePart(blk, f);
    case "document":
      if (blk.source.type === "text") return { type: "input_text", text: blk.source.data };
      if (!f.pdf) return { type: "input_text", text: PDF_OMITTED };
      return { type: "input_file", filename: blk.title ?? "document.pdf", file_data: `data:${blk.source.media_type};base64,${blk.source.data}` };
    default:
      return undefined;
  }
}

function toolResultText(content: string | (TextBlock | ImageBlock)[] | undefined): string {
  if (content === undefined) return "";
  if (typeof content === "string") return content;
  return content
    .filter((c): c is TextBlock => c.type === "text")
    .map((c) => c.text)
    .join("\n");
}

/** Canonical messages -> Responses input items. Exported for unit tests. */
export function toResponsesInput(req: CanonicalRequest, f: Features): Record<string, unknown>[] {
  const out: Record<string, unknown>[] = [];
  for (const m of req.messages) {
    const blocks = blocksOf(m);
    if (m.role === "assistant") {
      let text = "";
      const flushText = () => {
        if (text) out.push({ role: "assistant", content: text });
        text = "";
      };
      for (const blk of blocks) {
        if (blk.type === "text") text += blk.text;
        else if (blk.type === "thinking") {
          // Only reasoning items that came from a Responses upstream can be replayed.
          const item = f.thinking ? decodeReasoningSignature(blk.signature) : undefined;
          if (item) {
            flushText();
            out.push({ ...item });
          }
        } else if (blk.type === "tool_use") {
          flushText();
          out.push({ type: "function_call", call_id: blk.id, name: blk.name, arguments: JSON.stringify(blk.input ?? {}) });
        }
      }
      flushText();
      continue;
    }

    const parts: InputPart[] = [];
    const toolImages: InputPart[] = [];
    for (const blk of blocks) {
      if (blk.type === "tool_result") {
        let text = toolResultText(blk.content);
        if (blk.is_error && !/^error/i.test(text)) text = `Error: ${text}`;
        out.push({ type: "function_call_output", call_id: blk.tool_use_id, output: text || "(no output)" });
        const images = Array.isArray(blk.content) ? blk.content.filter((c): c is ImageBlock => c.type === "image") : [];
        if (images.length) {
          toolImages.push({ type: "input_text", text: `Image output of tool call ${blk.tool_use_id}:` });
          for (const img of images) toolImages.push(imagePart(img, f));
        }
        continue;
      }
      const p = userPart(blk, f);
      if (p) parts.push(p);
    }
    const all = [...toolImages, ...parts];
    if (all.length) out.push({ role: "user", content: all });
  }
  return out;
}

export function toResponsesBody(req: CanonicalRequest, target: AdapterTarget): Record<string, unknown> {
  const f = target.features;
  const body: Record<string, unknown> = {
    model: target.model.upstreamModel,
    input: toResponsesInput(req, f),
    stream: true,
    store: false,
    max_output_tokens: Math.min(req.max_tokens, target.model.maxOutputTokens),
  };
  if (req.system.length) body.instructions = req.system.map((s) => s.text).join("\n\n");

  const reasoningModel = f.thinking || f.effort !== undefined;
  if (reasoningModel && f.reasoning) {
    const reasoning: Record<string, unknown> = {};
    if (f.effort) reasoning.effort = f.effort;
    if (f.thinking) reasoning.summary = "auto";
    if (Object.keys(reasoning).length) body.reasoning = reasoning;
  }
  if (f.thinking && f.include) body.include = ["reasoning.encrypted_content"];
  if (f.speed) body.service_tier = f.speed === "fast" ? "priority" : f.speed;
  if (f.cacheTtl === "24h") body.prompt_cache_retention = "24h";

  if (req.tools?.length) {
    body.tools = req.tools.map((t) => ({
      type: "function",
      name: t.name,
      description: t.description ?? "",
      parameters: t.input_schema,
      strict: false,
    }));
    const tc = req.tool_choice;
    if (tc) {
      if (tc.type === "auto") body.tool_choice = "auto";
      else if (tc.type === "any") body.tool_choice = "required";
      else if (tc.type === "none") body.tool_choice = "none";
      else if (tc.type === "tool") body.tool_choice = { type: "function", name: tc.name };
      if (tc.type !== "none" && tc.disable_parallel_tool_use && f.parallelToolCalls) body.parallel_tool_calls = false;
    }
  }
  if (req.temperature !== undefined) body.temperature = req.temperature;
  if (req.top_p !== undefined) body.top_p = req.top_p;
  // The Responses API has no stop sequences; they are dropped.
  if (target.model.extraBody) Object.assign(body, target.model.extraBody);
  return body;
}

export function normalizeResponsesUsage(u: Record<string, any> | undefined | null): Partial<Usage> {
  if (!u) return {};
  const input = Number(u.input_tokens ?? 0);
  const cached = Number(u.input_tokens_details?.cached_tokens ?? 0);
  return {
    input_tokens: Math.max(0, input - cached),
    // output_tokens already includes output_tokens_details.reasoning_tokens
    output_tokens: Number(u.output_tokens ?? 0),
    cache_read_input_tokens: cached,
    cache_creation_input_tokens: 0,
  };
}

function failureFrom(code: unknown, message: unknown): UpstreamFailure {
  const c = String(code ?? "");
  const msg = String(message ?? "Upstream stream error");
  let type: string;
  if (/rate_limit/i.test(c)) type = "rate_limit_error";
  else if (/overload|unavailable/i.test(c)) type = "overloaded_error";
  else if (/server_error|internal|timeout/i.test(c) || !c) type = "api_error";
  else type = "invalid_request_error";
  return classifyStreamError(type, msg);
}

/** Stateful translator from Responses stream events to canonical block events. Exported for unit tests. */
export class ResponsesStreamTranslator {
  private index = -1;
  /** output_index -> our block index, for the currently open item. */
  private open: { outputIndex: number; kind: "text" | "thinking" | "tool"; summaryIndex: number } | null = null;
  private sawTool = false;
  started = false;
  completed = false;
  status: string | null = null;
  incompleteReason: string | null = null;
  refusal = false;
  usage: Partial<Usage> | undefined;

  private emit(out: AdapterEvent[], event: "content_block_start" | "content_block_delta" | "content_block_stop", data: Record<string, unknown>) {
    out.push({ kind: "block", event, data: { type: event, index: this.index, ...data } });
  }

  private close(out: AdapterEvent[]) {
    if (!this.open) return;
    this.emit(out, "content_block_stop", {});
    this.open = null;
  }

  private start(out: AdapterEvent[], outputIndex: number, kind: "text" | "thinking" | "tool", block: Record<string, unknown>) {
    this.close(out);
    this.index++;
    this.open = { outputIndex, kind, summaryIndex: 0 };
    this.emit(out, "content_block_start", { content_block: block });
  }

  private isOpen(outputIndex: unknown, kind: "text" | "thinking" | "tool"): boolean {
    return !!this.open && this.open.kind === kind && (typeof outputIndex !== "number" || this.open.outputIndex === outputIndex);
  }

  push(type: string, ev: Record<string, any>): AdapterEvent[] {
    const out: AdapterEvent[] = [];
    if (!this.started) {
      this.started = true;
      out.push({ kind: "start", id: typeof ev.response?.id === "string" ? ev.response.id : undefined });
    }
    switch (type) {
      case "response.output_item.added": {
        const item = ev.item ?? {};
        const oi = typeof ev.output_index === "number" ? ev.output_index : this.index + 1;
        if (item.type === "reasoning") this.start(out, oi, "thinking", { type: "thinking", thinking: "", signature: "" });
        else if (item.type === "function_call") {
          this.sawTool = true;
          this.start(out, oi, "tool", { type: "tool_use", id: String(item.call_id ?? item.id ?? ""), name: String(item.name ?? ""), input: {} });
          if (typeof item.arguments === "string" && item.arguments) this.emit(out, "content_block_delta", { delta: { type: "input_json_delta", partial_json: item.arguments } });
        } else if (item.type === "message") this.start(out, oi, "text", { type: "text", text: "" });
        break;
      }
      case "response.output_text.delta":
      case "response.refusal.delta": {
        if (type === "response.refusal.delta") this.refusal = true;
        if (typeof ev.delta !== "string" || !ev.delta) break;
        if (!this.isOpen(ev.output_index, "text")) this.start(out, ev.output_index ?? this.index + 1, "text", { type: "text", text: "" });
        this.emit(out, "content_block_delta", { delta: { type: "text_delta", text: ev.delta } });
        break;
      }
      case "response.reasoning_summary_text.delta": {
        if (typeof ev.delta !== "string" || !ev.delta) break;
        if (!this.isOpen(ev.output_index, "thinking")) this.start(out, ev.output_index ?? this.index + 1, "thinking", { type: "thinking", thinking: "", signature: "" });
        const si = typeof ev.summary_index === "number" ? ev.summary_index : 0;
        let text = ev.delta;
        if (si !== this.open!.summaryIndex) {
          text = `\n\n${text}`;
          this.open!.summaryIndex = si;
        }
        this.emit(out, "content_block_delta", { delta: { type: "thinking_delta", thinking: text } });
        break;
      }
      case "response.function_call_arguments.delta": {
        if (typeof ev.delta !== "string" || !ev.delta || !this.isOpen(ev.output_index, "tool")) break;
        this.emit(out, "content_block_delta", { delta: { type: "input_json_delta", partial_json: ev.delta } });
        break;
      }
      case "response.output_item.done": {
        const item = ev.item ?? {};
        if (item.type === "reasoning") {
          if (!this.isOpen(ev.output_index, "thinking")) {
            // Reasoning item without streamed summary deltas: emit it whole.
            this.start(out, ev.output_index ?? this.index + 1, "thinking", { type: "thinking", thinking: "", signature: "" });
            const text = Array.isArray(item.summary) ? item.summary.map((s: { text?: string }) => s.text ?? "").filter(Boolean).join("\n\n") : "";
            if (text) this.emit(out, "content_block_delta", { delta: { type: "thinking_delta", thinking: text } });
          }
          if (typeof item.encrypted_content === "string" && item.encrypted_content && typeof item.id === "string") {
            this.emit(out, "content_block_delta", { delta: { type: "signature_delta", signature: encodeReasoningSignature(item) } });
          }
          this.close(out);
        } else if (item.type === "function_call") {
          if (!this.isOpen(ev.output_index, "tool")) {
            this.sawTool = true;
            this.start(out, ev.output_index ?? this.index + 1, "tool", { type: "tool_use", id: String(item.call_id ?? item.id ?? ""), name: String(item.name ?? ""), input: {} });
            if (typeof item.arguments === "string" && item.arguments) this.emit(out, "content_block_delta", { delta: { type: "input_json_delta", partial_json: item.arguments } });
          }
          this.close(out);
        } else if (item.type === "message") {
          if (this.isOpen(ev.output_index, "text")) this.close(out);
        }
        break;
      }
      case "response.completed":
      case "response.incomplete": {
        const r = ev.response ?? {};
        this.completed = true;
        this.status = typeof r.status === "string" ? r.status : type === "response.incomplete" ? "incomplete" : "completed";
        this.incompleteReason = r.incomplete_details?.reason ?? null;
        if (r.usage) this.usage = normalizeResponsesUsage(r.usage);
        break;
      }
      default:
        break;
    }
    return out;
  }

  stopReason(): StopReason {
    if (this.sawTool) return "tool_use";
    if (this.status === "incomplete" && this.incompleteReason === "max_output_tokens") return "max_tokens";
    if (this.incompleteReason === "content_filter" || this.refusal) return "refusal";
    return "end_turn";
  }

  end(): AdapterEvent[] {
    const out: AdapterEvent[] = [];
    this.close(out);
    out.push({ kind: "stop", stop_reason: this.stopReason(), stop_sequence: null, usage: this.usage });
    return out;
  }
}

export const openaiResponsesAdapter: Adapter = {
  buildRequest(req, target): UpstreamRequest {
    const h: Record<string, string> = { "content-type": "application/json", accept: "text/event-stream" };
    if (target.provider.apiKey) h.authorization = `Bearer ${target.provider.apiKey}`;
    return {
      url: joinUrl(target.provider.baseUrl, "/responses"),
      headers: { ...h, ...target.provider.headers },
      body: toResponsesBody(req, target),
    };
  },

  async *translate(body): AsyncGenerator<AdapterEvent> {
    const tr = new ResponsesStreamTranslator();
    for await (const msg of parseSse(body)) {
      const data = msg.data.trim();
      if (!data || data === "[DONE]") continue;
      let ev: Record<string, any>;
      try {
        ev = JSON.parse(data);
      } catch {
        continue;
      }
      const type = typeof ev.type === "string" ? ev.type : msg.event ?? "";
      if (type === "error") {
        const e = ev.error ?? ev;
        yield { kind: "error", error: failureFrom(e.code ?? e.type, e.message) };
        return;
      }
      if (type === "response.failed") {
        const e = ev.response?.error ?? {};
        yield { kind: "error", error: failureFrom(e.code, e.message ?? "Upstream response failed") };
        return;
      }
      for (const out of tr.push(type, ev)) yield out;
      if (tr.completed) break;
    }
    if (!tr.completed) {
      yield { kind: "error", error: { type: "upstream", message: "Upstream stream ended unexpectedly", retryable: true } };
      return;
    }
    for (const out of tr.end()) yield out;
  },

  rejectedFeature(failure: UpstreamFailure, sent: UpstreamRequest, _features: Features): FeatureName | undefined {
    if (failure.status !== 400 && failure.status !== 422) return undefined;
    const m = failure.raw ?? failure.message;
    const reasoning = sent.body.reasoning as Record<string, unknown> | undefined;
    if (sent.body.include && /include|encrypted_content/i.test(m)) return "include";
    if (reasoning?.effort && /effort/i.test(m)) return "effort";
    if (sent.body.service_tier && /service.?tier/i.test(m)) return "speed";
    if (sent.body.prompt_cache_retention && /retention/i.test(m)) return "cache_ttl";
    if (reasoning && /reasoning|summary/i.test(m)) return "reasoning";
    if (sent.body.parallel_tool_calls !== undefined && /parallel_tool_calls/i.test(m)) return "parallel_tool_calls";
    return undefined;
  },
};
