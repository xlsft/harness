import { describe, expect, it } from "vitest";
import { toAnthropicBody } from "../src/gateway/adapters/anthropic.ts";
import { OpenAIStreamTranslator, normalizeOpenAIUsage, toOpenAIBody } from "../src/gateway/adapters/openai-chat.ts";
import { joinUrl, type AdapterTarget } from "../src/gateway/adapters/types.ts";
import { computeCost, estimateTokens } from "../src/gateway/cost.ts";
import { classifyHttpFailure, parseRetryAfter } from "../src/gateway/errors.ts";
import { computeFeatures, normalizeRequest } from "../src/gateway/features.ts";
import type { AdapterEvent, CanonicalRequest } from "../src/gateway/types.ts";
import { downgradeEffort, type ModelEntry } from "../src/services/models.ts";

const model = (over: Partial<ModelEntry> = {}): ModelEntry => ({
  id: "m",
  displayName: "M",
  providerId: "p",
  upstreamModel: "up",
  contextWindow: 100000,
  maxOutputTokens: 4096,
  capabilities: { thinking: true, effort: ["low", "high"], images: true, pdf: true, parallel_tool_calls: true, prompt_cache: true },
  pricing: null,
  fallbacks: [],
  extraBody: null,
  roles: null,
  enabled: true,
  ...over,
});

const target = (req: CanonicalRequest, m = model(), disabled: string[] = []): AdapterTarget => ({
  model: m,
  provider: { id: "p", type: "anthropic", baseUrl: "http://x", headers: {}, allowPrivateNetwork: false, enabled: true },
  features: computeFeatures(req, m, new Set(disabled)),
});

const req = (over: Record<string, unknown>): CanonicalRequest =>
  normalizeRequest({ model: "m", max_tokens: 100, stream: true, messages: [{ role: "user", content: "hi" }], ...over }, { requireStream: true });

describe("features", () => {
  it("downgrades effort to the nearest supported level", () => {
    expect(downgradeEffort("max", ["low", "high"])).toBe("high");
    expect(downgradeEffort("medium", ["low", "high"])).toBe("low");
    expect(downgradeEffort("low", ["high"])).toBe("high");
    expect(downgradeEffort("high", [])).toBeUndefined();
    expect(downgradeEffort(undefined, ["low"])).toBeUndefined();
  });

  it("strips features the model lacks or that were negotiated off", () => {
    const r = req({ thinking: { type: "enabled", budget_tokens: 10 }, effort: "high" });
    const f = computeFeatures(r, model({ capabilities: { ...model().capabilities, thinking: false, prompt_cache: false } }), new Set(["effort"]));
    expect(f).toMatchObject({ thinking: false, effort: undefined, cache: false });
  });

  it("validates requests", () => {
    expect(() => normalizeRequest({ model: "m", max_tokens: 1, messages: [] }, { requireStream: true })).toThrow(/stream/);
    expect(() => normalizeRequest({ model: "m", max_tokens: 1, stream: true, messages: [] }, { requireStream: true })).toThrow(/messages/);
    expect(() => normalizeRequest({ model: "m", max_tokens: 1, stream: true, messages: [{ role: "system", content: "x" }] }, { requireStream: true })).toThrow(/role/);
  });
});

describe("Anthropic request mapping", () => {
  it("maps cache to cache_control with at most 4 breakpoints", () => {
    const r = req({
      system: [{ type: "text", text: "a", cache: true }, { type: "text", text: "b", cache: true }],
      messages: [
        { role: "user", content: [{ type: "text", text: "1", cache: true }, { type: "text", text: "2", cache: true }, { type: "text", text: "3", cache: true }] },
      ],
    });
    const body = toAnthropicBody(r, target(r));
    const marks = JSON.stringify(body).match(/cache_control/g) ?? [];
    expect(marks).toHaveLength(4);
    expect(JSON.stringify(body)).not.toContain('"cache"');
  });

  it("ignores cache when prompt caching is unsupported and joins system when blocks are rejected", () => {
    const r = req({ system: [{ type: "text", text: "a", cache: true }, { type: "text", text: "b" }] });
    const m = model({ capabilities: { ...model().capabilities, prompt_cache: false } });
    const body = toAnthropicBody(r, target(r, m, ["system_blocks"]));
    expect(body.system).toBe("a\n\nb");
  });

  it("replaces images when the model has no vision and drops thinking when unsupported", () => {
    const r = req({
      messages: [
        { role: "user", content: [{ type: "image", source: { type: "base64", media_type: "image/png", data: "AAAA" } }] },
        { role: "assistant", content: [{ type: "thinking", thinking: "t", signature: "s" }] },
        { role: "user", content: "next" },
      ],
      thinking: { type: "enabled", budget_tokens: 1024 },
    });
    const m = model({ capabilities: { ...model().capabilities, images: false, thinking: false } });
    const body = toAnthropicBody(r, target(r, m)) as { messages: { content: { type: string; text?: string }[] }[]; thinking?: unknown };
    expect(body.messages[0]!.content[0]).toMatchObject({ type: "text" });
    expect(body.messages[0]!.content[0]!.text).toMatch(/image omitted/);
    expect(body.messages).toHaveLength(2); // assistant message emptied by dropping thinking is removed
    expect(body.thinking).toBeUndefined();
  });
});

describe("OpenAI request mapping", () => {
  it("maps tool_choice, max_completion_tokens, extra_body and tool errors", () => {
    const r = req({
      messages: [
        { role: "assistant", content: [{ type: "tool_use", id: "c1", name: "Bash", input: { command: "false" } }] },
        { role: "user", content: [{ type: "tool_result", tool_use_id: "c1", is_error: true, content: "exit 1" }] },
      ],
      tools: [{ name: "Bash", input_schema: { type: "object" } }],
      tool_choice: { type: "none" },
      stop_sequences: ["END"],
    });
    const m = model({ extraBody: { chat_template_kwargs: { enable_thinking: false } } });
    const body = toOpenAIBody(r, target(r, m, ["max_tokens_param", "stream_usage"]));
    expect(body.max_completion_tokens).toBe(100);
    expect(body.max_tokens).toBeUndefined();
    expect(body.stream_options).toBeUndefined();
    expect(body.tool_choice).toBe("none");
    expect(body.stop).toEqual(["END"]);
    expect(body.chat_template_kwargs).toEqual({ enable_thinking: false });
    expect((body.messages as unknown[])[1]).toEqual({ role: "tool", tool_call_id: "c1", content: "Error: exit 1" });
  });

  it("drops reasoning_content when thinking is negotiated off", () => {
    const r = req({ messages: [{ role: "user", content: "q" }, { role: "assistant", content: [{ type: "thinking", thinking: "r" }, { type: "text", text: "a" }] }] });
    const body = toOpenAIBody(r, target(r, model(), ["thinking"]));
    expect((body.messages as unknown[])[1]).toEqual({ role: "assistant", content: "a" });
  });

  it("sends PDFs as file parts", () => {
    const r = req({ messages: [{ role: "user", content: [{ type: "document", source: { type: "base64", media_type: "application/pdf", data: "JVBE" } }] }] });
    const body = toOpenAIBody(r, target(r));
    expect((body.messages as { content: unknown }[])[0]!.content).toEqual([
      { type: "file", file: { filename: "document.pdf", file_data: "data:application/pdf;base64,JVBE" } },
    ]);
  });
});

describe("OpenAI stream translation", () => {
  const run = (chunks: Record<string, unknown>[]) => {
    const tr = new OpenAIStreamTranslator();
    const out: AdapterEvent[] = [];
    for (const c of chunks) out.push(...tr.push(c));
    out.push(...tr.end());
    return out;
  };
  const ch = (delta: Record<string, unknown>, finish: string | null = null) => ({ id: "x", choices: [{ index: 0, delta, finish_reason: finish }] });

  it("maps finish_reason to stop_reason", () => {
    const stops = (f: string) => run([ch({ content: "a" }, f)]).find((e) => e.kind === "stop") as { stop_reason: string };
    expect(stops("stop").stop_reason).toBe("end_turn");
    expect(stops("length").stop_reason).toBe("max_tokens");
    expect(stops("content_filter").stop_reason).toBe("refusal");
    expect(stops("tool_calls").stop_reason).toBe("tool_use");
  });

  it("reports tool_use when tool calls were streamed even if finish_reason is stop", () => {
    const evs = run([ch({ tool_calls: [{ index: 0, id: "c", function: { name: "X", arguments: "{}" } }] }, "stop")]);
    expect((evs.at(-1) as { stop_reason: string }).stop_reason).toBe("tool_use");
  });

  it("generates tool ids when the upstream omits them and supports OpenRouter `reasoning`", () => {
    const evs = run([ch({ reasoning: "hmm" }), ch({ tool_calls: [{ index: 0, function: { name: "X", arguments: '{"a":1}' } }] }, "tool_calls")]);
    const starts = evs.filter((e) => e.kind === "block" && e.event === "content_block_start") as unknown as { data: { content_block: { type: string; id?: string } } }[];
    expect(starts.map((s) => s.data.content_block.type)).toEqual(["thinking", "tool_use"]);
    expect(starts[1]!.data.content_block.id).toMatch(/^toolu_/);
  });

  /** Rebuilds tool_use blocks from block events, asserting sequential start/stop. */
  const toolBlocks = (evs: AdapterEvent[]) => {
    const blocks: { index: number; id: string; name: string; json: string }[] = [];
    let open: number | null = null;
    for (const e of evs) {
      if (e.kind !== "block") continue;
      const d = e.data as any;
      if (e.event === "content_block_start") {
        expect(open).toBeNull();
        open = d.index;
        if (d.content_block.type === "tool_use") blocks.push({ index: d.index, id: d.content_block.id, name: d.content_block.name, json: "" });
      } else if (e.event === "content_block_delta") {
        expect(d.index).toBe(open);
        if (d.delta.type === "input_json_delta") blocks.find((b) => b.index === d.index)!.json += d.delta.partial_json;
      } else if (e.event === "content_block_stop") {
        expect(d.index).toBe(open);
        open = null;
      }
    }
    expect(open).toBeNull();
    return blocks;
  };

  it("keeps interleaved tool-call deltas across indices and emits blocks in index order", () => {
    const tc = (index: number, args: string, extra: Record<string, unknown> = {}) => ch({ tool_calls: [{ index, ...extra, function: { arguments: args, ...(extra.name ? { name: extra.name } : {}) } }] });
    const evs = run([
      ch({ content: "Doing three things." }),
      tc(0, "", { id: "call_0", name: "Read" }),
      tc(1, '{"pat', { id: "call_1", name: "Glob" }),
      tc(0, '{"file_path":'),
      tc(2, '{"command":"ls"}', { id: "call_2", name: "Bash" }),
      tc(1, 'tern":"*.ts"}'),
      tc(0, '"/a"}'),
      ch({}, "tool_calls"),
    ]);
    const blocks = toolBlocks(evs);
    expect(blocks.map((b) => [b.id, b.name, JSON.parse(b.json)])).toEqual([
      ["call_0", "Read", { file_path: "/a" }],
      ["call_1", "Glob", { pattern: "*.ts" }],
      ["call_2", "Bash", { command: "ls" }],
    ]);
    expect(blocks.map((b) => b.index)).toEqual([1, 2, 3]);
    expect((evs.at(-1) as { stop_reason: string }).stop_reason).toBe("tool_use");
  });

  it("several deltas in one chunk and late promotion of buffered calls", () => {
    const evs = run([
      ch({
        tool_calls: [
          { index: 0, id: "a", function: { name: "A", arguments: '{"x":' } },
          { index: 1, id: "b", function: { name: "B", arguments: '{"y":2}' } },
        ],
      }),
      ch({ tool_calls: [{ index: 0, function: { arguments: "1}" } }, { index: 2, id: "c", function: { name: "C", arguments: "{}" } }] }, "tool_calls"),
    ]);
    expect(toolBlocks(evs).map((b) => [b.id, b.json])).toEqual([
      ["a", '{"x":1}'],
      ["b", '{"y":2}'],
      ["c", "{}"],
    ]);
  });

  it("streams sequential tool calls live (no buffering when calls do not interleave)", () => {
    const evs = run([
      ch({ tool_calls: [{ index: 0, id: "a", function: { name: "A", arguments: '{"x"' } }] }),
      ch({ tool_calls: [{ index: 0, function: { arguments: ":1}" } }] }),
      ch({ tool_calls: [{ index: 1, id: "b", function: { name: "B", arguments: "{}" } }] }, "tool_calls"),
    ]);
    const deltas = evs.flatMap((e) => (e.kind === "block" && e.event === "content_block_delta" ? [(e.data as any).delta.partial_json] : []));
    expect(deltas).toEqual(['{"x"', ":1}", "{}"]);
  });

  it("normalizes usage across providers", () => {
    expect(normalizeOpenAIUsage({ prompt_tokens: 100, completion_tokens: 5, prompt_tokens_details: { cached_tokens: 60 } })).toEqual({
      input_tokens: 40,
      output_tokens: 5,
      cache_read_input_tokens: 60,
      cache_creation_input_tokens: 0,
    });
    expect(normalizeOpenAIUsage({ prompt_tokens: 100, completion_tokens: 5, prompt_cache_hit_tokens: 30 })).toMatchObject({ input_tokens: 70, cache_read_input_tokens: 30 });
  });
});

describe("errors, cost, urls", () => {
  it("parses retry-after headers", () => {
    expect(parseRetryAfter(new Headers({ "retry-after-ms": "250" }))).toBe(250);
    expect(parseRetryAfter(new Headers({ "retry-after": "3" }))).toBe(3000);
    expect(parseRetryAfter(new Headers())).toBeUndefined();
  });

  it("classifies upstream failures and honors x-should-retry", () => {
    expect(classifyHttpFailure(529, new Headers(), "{}")).toMatchObject({ type: "overloaded", retryable: true });
    expect(classifyHttpFailure(401, new Headers(), "{}")).toMatchObject({ type: "upstream", retryable: false });
    expect(classifyHttpFailure(500, new Headers({ "x-should-retry": "false" }), "{}").retryable).toBe(false);
    expect(classifyHttpFailure(400, new Headers({ "x-should-retry": "true" }), "{}").retryable).toBe(true);
  });

  it("computes cost per 1M tokens", () => {
    expect(computeCost({ input_tokens: 1_000_000, output_tokens: 1_000_000, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 }, { input: 3, output: 15 })).toBe(18);
    expect(computeCost({ input_tokens: 10, output_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 }, null)).toBe(0);
    expect(estimateTokens({ text: "abcd".repeat(10) })).toBe(10);
  });

  it("joins base URLs", () => {
    expect(joinUrl("https://api.anthropic.com", "/v1/messages")).toBe("https://api.anthropic.com/v1/messages");
    expect(joinUrl("https://proxy.example/v1/", "/v1/messages")).toBe("https://proxy.example/v1/messages");
    expect(joinUrl("https://api.openai.com/v1", "/chat/completions")).toBe("https://api.openai.com/v1/chat/completions");
  });
});
