/**
 * Contract tests for GatewayService against a local mock upstream: Anthropic passthrough,
 * OpenAI translation (text, parallel tool calls), DeepSeek reasoning_content, retries,
 * capability negotiation, fallback, keep-alive pings, usage/cost accounting.
 */
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { ErrorInfoSchema, ErrorType } from "../src/gen/harness/common/v1/errors_pb.ts";
import { Effort, Role, StopReason, Thinking_Mode, ToolChoice_Mode } from "../src/gen/harness/llm/v1/content_pb.ts";
import { StreamMessageRequestSchema, type StreamMessageResponse } from "../src/gen/harness/llm/v1/gateway_pb.ts";
import {
  anthropicErrorAfterContent,
  anthropicOverloadedInStream,
  anthropicThinkingText,
  anthropicToolUse,
  deepseekReasoning,
  openaiText,
  openaiToolCalls,
} from "./fixtures/upstream.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { adminApi, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let up: MockUpstream;
let token: string;
let gw: TestServer["gateway"];

const PRICING = { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 };

beforeAll(async () => {
  up = await new MockUpstream().start();
  ts = await startTestServer({ HARNESS_PING_INTERVAL_MS: "40" });
  token = (await deviceLogin(ts)).accessToken;
  gw = ts.as(token).gateway;
  const api = (method: string, path: string, body?: unknown) => adminApi(ts.url, token, method, path, body);

  expect(
    (await api("POST", "/api/admin/providers", { id: "anthropic-mock", type: "anthropic", base_url: up.url, api_key: "sk-ant-secret-1234", allow_private_network: true, headers: { "anthropic-beta": "test-beta" } })).status,
  ).toBe(201);
  expect((await api("POST", "/api/admin/providers", { id: "openai-mock", type: "openai-chat", base_url: `${up.url}/v1`, api_key: "sk-oai-secret-5678", allow_private_network: true })).status).toBe(201);

  const models = [
    { id: "claude-t", display_name: "Claude Test", provider_id: "anthropic-mock", upstream_model: "claude-up", context_window: 200000, max_output_tokens: 32000, capabilities: { thinking: true, effort: ["low", "medium", "high"], images: true, pdf: true, prompt_cache: true }, pricing: PRICING },
    { id: "claude-nt", provider_id: "anthropic-mock", upstream_model: "claude-old", context_window: 200000, max_output_tokens: 8000, capabilities: { thinking: true } },
    { id: "claude-fb", provider_id: "anthropic-mock", upstream_model: "claude-busy", context_window: 100000, max_output_tokens: 8000, fallbacks: ["tiny", "gpt-t"] },
    { id: "gpt-t", display_name: "GPT Test", provider_id: "openai-mock", upstream_model: "gpt-up", context_window: 400000, max_output_tokens: 16000, capabilities: { effort: ["low", "medium", "high"], images: true }, pricing: { input: 1, output: 4, cache_read: 0.1 } },
    { id: "tiny", provider_id: "openai-mock", upstream_model: "tiny-up", context_window: 8000, max_output_tokens: 1000 },
    { id: "ds-t", provider_id: "openai-mock", upstream_model: "deepseek-reasoner", context_window: 128000, max_output_tokens: 8000, capabilities: { thinking: true } },
  ];
  for (const m of models) expect((await api("POST", "/api/admin/models", m)).status).toBe(201);
  expect((await api("PUT", "/api/admin/slots", { main: "claude-t", small: "gpt-t", plan: "claude-t" })).status).toBe(200);
});

afterAll(async () => {
  await ts.close();
  await up.stop();
});

beforeEach(() => up.reset());

type Ev = StreamMessageResponse["event"];

async function collect(req: Parameters<typeof gw.streamMessage>[0]): Promise<{ events: Ev[]; error?: ConnectError }> {
  const events: Ev[] = [];
  try {
    for await (const r of gw.streamMessage(req)) events.push(r.event);
  } catch (e) {
    return { events, error: e as ConnectError };
  }
  return { events };
}

const userText = (text: string) => ({ role: Role.USER, content: [{ block: { case: "text" as const, value: { text } } }] });
const nonPing = (evs: Ev[]) => evs.filter((e) => e.case !== "ping");
const ofCase = <C extends NonNullable<Ev["case"]>>(evs: Ev[], c: C) => evs.filter((e): e is Extract<Ev, { case: C }> => e.case === c);

describe("ListModels", () => {
  it("returns the catalog with capabilities, pricing and slots", async () => {
    const r = await gw.listModels({});
    const claude = r.models.find((m) => m.id === "claude-t")!;
    expect(claude.displayName).toBe("Claude Test");
    expect(claude.provider).toBe("anthropic-mock");
    expect(claude.contextWindow).toBe(200000n);
    expect(claude.capabilities?.effort).toEqual([Effort.LOW, Effort.MEDIUM, Effort.HIGH]);
    expect(claude.capabilities?.thinking).toBe(true);
    expect(claude.pricing).toMatchObject(PRICING.input ? { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 } : {});
    expect(r.models.find((m) => m.id === "claude-nt")!.pricing).toBeUndefined();
    expect(r.slots).toMatchObject({ main: "claude-t", small: "gpt-t", plan: "claude-t" });
  });
});

describe("Anthropic adapter", () => {
  it("passes through thinking + text, maps cache/effort, normalizes usage and cost", async () => {
    up.enqueue({ sse: anthropicThinkingText });
    const { events, error } = await collect({
      model: "claude-t",
      maxTokens: 64000,
      system: [{ text: "static", cache: true }, { text: "dynamic" }],
      messages: [
        userText("hi"),
        {
          role: Role.ASSISTANT,
          content: [
            { block: { case: "thinking", value: { thinking: "old", signature: "sig-old" } } },
            { block: { case: "thinking", value: { thinking: "unsigned from another model", signature: "" } } },
            { block: { case: "text", value: { text: "prev" } } },
          ],
        },
        { role: Role.USER, content: [{ block: { case: "text", value: { text: "again", cache: true } } }] },
      ],
      thinking: { mode: Thinking_Mode.ENABLED, budgetTokens: 8000 },
      effort: Effort.XHIGH,
      metadata: { sessionId: "ses_1" },
    });
    expect(error).toBeUndefined();

    const sent = up.requests[0]!;
    expect(sent.url).toBe("/v1/messages");
    expect(sent.headers["x-api-key"]).toBe("sk-ant-secret-1234");
    expect(sent.headers["anthropic-version"]).toBe("2023-06-01");
    expect(sent.headers["anthropic-beta"]).toBe("test-beta");
    expect(sent.body.model).toBe("claude-up");
    expect(sent.body.max_tokens).toBe(32000); // clamped to the model's max_output_tokens
    expect(sent.body.stream).toBe(true);
    expect(sent.body.system).toEqual([
      { type: "text", text: "static", cache_control: { type: "ephemeral" } },
      { type: "text", text: "dynamic" },
    ]);
    expect(sent.body.thinking).toEqual({ type: "enabled", budget_tokens: 8000 });
    expect(sent.body.output_config).toEqual({ effort: "high" }); // xhigh lowered to the highest supported
    expect(sent.body.effort).toBeUndefined();
    expect(sent.body.metadata).toBeUndefined();
    expect(sent.body.messages[1].content).toEqual([
      { type: "thinking", thinking: "old", signature: "sig-old" },
      { type: "text", text: "prev" },
    ]);
    expect(sent.body.messages[2].content[0]).toEqual({ type: "text", text: "again", cache_control: { type: "ephemeral" } });

    const evs = nonPing(events);
    expect(evs.map((e) => e.case)).toEqual([
      "messageStart",
      "contentBlockStart", "contentBlockDelta", "contentBlockDelta", "contentBlockStop",
      "contentBlockStart", "contentBlockDelta", "contentBlockDelta", "contentBlockStop",
      "messageDelta", "messageStop",
    ]);
    const start = ofCase(evs, "messageStart")[0]!.value;
    expect(start.id).toMatch(/^msg_/);
    expect(start.model).toBe("claude-t");
    const blockStarts = ofCase(evs, "contentBlockStart");
    expect(blockStarts[0]!.value.block?.block.case).toBe("thinking");
    expect(blockStarts[1]!.value.index).toBe(1);
    const deltas = ofCase(evs, "contentBlockDelta").map((d) => d.value.delta);
    expect(deltas).toEqual([
      { case: "thinking", value: "Let me think" },
      { case: "signature", value: "sig-abc" },
      { case: "text", value: "Hel" },
      { case: "text", value: "lo" },
    ]);
    const md = ofCase(evs, "messageDelta")[0]!.value;
    expect(md.stopReason).toBe(StopReason.END_TURN);
    expect(md.usage).toMatchObject({ inputTokens: 120n, outputTokens: 42n, cacheReadInputTokens: 1000n, cacheCreationInputTokens: 50n });
    const stop = ofCase(evs, "messageStop")[0]!.value;
    expect(stop.upstreamModel).toBe("claude-up");
    expect(stop.retries).toBe(0);
    expect(stop.costUsd).toBeCloseTo((120 * 3 + 42 * 15 + 1000 * 0.3 + 50 * 3.75) / 1e6, 10);
  });

  it("streams tool_use input as input_json deltas", async () => {
    up.enqueue({ sse: anthropicToolUse, chunkSize: 5 });
    const { events } = await collect({
      model: "claude-t",
      maxTokens: 100,
      messages: [userText("read it")],
      tools: [{ name: "Read", description: "Reads", inputSchemaJson: '{"type":"object","properties":{"file_path":{"type":"string"}}}' }],
      toolChoice: { mode: ToolChoice_Mode.TOOL, toolName: "Read" },
    });
    expect(up.requests[0]!.body.tools[0]).toEqual({ name: "Read", description: "Reads", input_schema: { type: "object", properties: { file_path: { type: "string" } } } });
    expect(up.requests[0]!.body.tool_choice).toEqual({ type: "tool", name: "Read" });
    const start = ofCase(events, "contentBlockStart")[0]!.value.block!.block;
    expect(start).toMatchObject({ case: "toolUse", value: { id: "toolu_01", name: "Read", inputJson: "" } });
    const json = ofCase(events, "contentBlockDelta").map((d) => (d.value.delta.case === "inputJson" ? d.value.delta.value : "")).join("");
    expect(JSON.parse(json)).toEqual({ file_path: "/a.txt" });
    expect(ofCase(events, "messageDelta")[0]!.value.stopReason).toBe(StopReason.TOOL_USE);
  });
});

describe("OpenAI Chat adapter", () => {
  it("translates the request and normalizes usage", async () => {
    up.enqueue({ sse: openaiText });
    const png = new Uint8Array([137, 80, 78, 71]);
    const { events, error } = await collect({
      model: "gpt-t",
      maxTokens: 1000,
      system: [{ text: "sys A", cache: true }, { text: "sys B" }],
      messages: [
        userText("look"),
        {
          role: Role.ASSISTANT,
          content: [
            { block: { case: "thinking", value: { thinking: "hidden", signature: "s" } } },
            { block: { case: "text", value: { text: "Taking a screenshot." } } },
            { block: { case: "toolUse", value: { id: "call_1", name: "Bash", inputJson: '{"command":"shot"}' } } },
          ],
        },
        {
          role: Role.USER,
          content: [
            {
              block: {
                case: "toolResult",
                value: {
                  toolUseId: "call_1",
                  isError: false,
                  content: [
                    { block: { case: "text", value: { text: "saved" } } },
                    { block: { case: "image", value: { mediaType: "image/png", data: png } } },
                  ],
                },
              },
            },
            { block: { case: "text", value: { text: "what do you see?" } } },
          ],
        },
      ],
      tools: [{ name: "Bash", description: "Run", inputSchemaJson: '{"type":"object"}' }],
      toolChoice: { mode: ToolChoice_Mode.ANY, disableParallelToolUse: true },
      effort: Effort.MAX,
      thinking: { mode: Thinking_Mode.ENABLED, budgetTokens: 1000 },
    });
    expect(error).toBeUndefined();
    const sent = up.requests[0]!;
    expect(sent.url).toBe("/v1/chat/completions");
    expect(sent.headers.authorization).toBe("Bearer sk-oai-secret-5678");
    expect(sent.body).toMatchObject({
      model: "gpt-up",
      stream: true,
      stream_options: { include_usage: true },
      max_tokens: 1000,
      reasoning_effort: "high",
      tool_choice: "required",
      parallel_tool_calls: false,
      tools: [{ type: "function", function: { name: "Bash", description: "Run", parameters: { type: "object" } } }],
    });
    expect(sent.body.thinking).toBeUndefined();
    expect(sent.body.messages).toEqual([
      { role: "system", content: "sys A\n\nsys B" },
      { role: "user", content: "look" },
      { role: "assistant", content: "Taking a screenshot.", tool_calls: [{ id: "call_1", type: "function", function: { name: "Bash", arguments: '{"command":"shot"}' } }] },
      { role: "tool", tool_call_id: "call_1", content: "saved" },
      {
        role: "user",
        content: [
          { type: "text", text: "Image output of tool call call_1:" },
          { type: "image_url", image_url: { url: "data:image/png;base64,iVBORw==" } },
          { type: "text", text: "what do you see?" },
        ],
      },
    ]);

    const evs = nonPing(events);
    expect(evs.map((e) => e.case)).toEqual(["messageStart", "contentBlockStart", "contentBlockDelta", "contentBlockDelta", "contentBlockStop", "messageDelta", "messageStop"]);
    const md = ofCase(evs, "messageDelta")[0]!.value;
    expect(md.stopReason).toBe(StopReason.END_TURN);
    expect(md.usage).toMatchObject({ inputTokens: 476n, outputTokens: 7n, cacheReadInputTokens: 1024n, cacheCreationInputTokens: 0n });
    expect(ofCase(evs, "messageStop")[0]!.value.costUsd).toBeCloseTo((476 * 1 + 7 * 4 + 1024 * 0.1) / 1e6, 10);
  });

  it("turns streamed tool_calls into sequential tool_use blocks", async () => {
    up.enqueue({ sse: openaiToolCalls, chunkSize: 11 });
    const { events } = await collect({ model: "gpt-t", maxTokens: 100, messages: [userText("go")] });
    const evs = nonPing(events);
    const starts = ofCase(evs, "contentBlockStart").map((s) => [s.value.index, s.value.block!.block]);
    expect(starts).toEqual([
      [0, { case: "text", value: expect.objectContaining({ text: "" }) }],
      [1, { case: "toolUse", value: expect.objectContaining({ id: "call_A", name: "Read", inputJson: "" }) }],
      [2, { case: "toolUse", value: expect.objectContaining({ id: "call_B", name: "Glob", inputJson: "" }) }],
    ]);
    const jsonFor = (i: number) =>
      ofCase(evs, "contentBlockDelta").filter((d) => d.value.index === i).map((d) => d.value.delta.value).join("");
    expect(JSON.parse(jsonFor(1))).toEqual({ file_path: "/a" });
    expect(JSON.parse(jsonFor(2))).toEqual({ pattern: "**/*.go" });
    // every block is closed, in order, before message_delta
    expect(ofCase(evs, "contentBlockStop").map((s) => s.value.index)).toEqual([0, 1, 2]);
    expect(ofCase(evs, "messageDelta")[0]!.value.stopReason).toBe(StopReason.TOOL_USE);
  });

  it("maps DeepSeek reasoning_content to thinking blocks (and back into history)", async () => {
    up.enqueue({ sse: deepseekReasoning });
    const { events } = await collect({
      model: "ds-t",
      maxTokens: 100,
      messages: [
        userText("q1"),
        { role: Role.ASSISTANT, content: [{ block: { case: "thinking", value: { thinking: "prior reasoning", signature: "" } } }, { block: { case: "text", value: { text: "a1" } } }] },
        userText("q2"),
      ],
    });
    expect(up.requests[0]!.body.messages[1]).toEqual({ role: "assistant", content: "a1", reasoning_content: "prior reasoning" });
    const evs = nonPing(events);
    const starts = ofCase(evs, "contentBlockStart").map((s) => s.value.block!.block.case);
    expect(starts).toEqual(["thinking", "text"]);
    const thinking = ofCase(evs, "contentBlockDelta").filter((d) => d.value.delta.case === "thinking").map((d) => d.value.delta.value).join("");
    expect(thinking).toBe("First, consider X.");
    const text = ofCase(evs, "contentBlockDelta").filter((d) => d.value.delta.case === "text").map((d) => d.value.delta.value).join("");
    expect(text).toBe("Answer: 42");
    expect(ofCase(evs, "messageDelta")[0]!.value.usage).toMatchObject({ inputTokens: 260n, cacheReadInputTokens: 640n, outputTokens: 60n });
  });
});

describe("reliability", () => {
  it("retries overloaded upstreams before the first content block and reports Retry events", async () => {
    up.enqueue(
      { status: 529, headers: { "retry-after-ms": "10" }, json: { type: "error", error: { type: "overloaded_error", message: "Overloaded" } } },
      { sse: anthropicOverloadedInStream },
      { sse: anthropicThinkingText },
    );
    const { events, error } = await collect({ model: "claude-t", maxTokens: 100, messages: [userText("hi")] });
    expect(error).toBeUndefined();
    expect(up.requests).toHaveLength(3);
    const retries = ofCase(events, "retry").map((r) => r.value);
    expect(retries).toHaveLength(2);
    expect(retries[0]).toMatchObject({ attempt: 2, maxRetries: 8, retryDelayMs: 10n });
    expect(retries[0]!.error).toMatchObject({ type: ErrorType.OVERLOADED, upstreamStatus: 529 });
    expect(retries[1]!.attempt).toBe(3);
    expect(ofCase(events, "messageStart")).toHaveLength(1);
    // Retry events come before message_start
    expect(events.findIndex((e) => e.case === "messageStart")).toBeGreaterThan(events.findIndex((e) => e.case === "retry"));
    expect(ofCase(events, "messageStop")[0]!.value.retries).toBe(2);
  });

  it("does not retry after content has started; ends the stream with an error", async () => {
    up.enqueue({ sse: anthropicErrorAfterContent });
    const { events, error } = await collect({ model: "claude-t", maxTokens: 100, messages: [userText("hi")] });
    expect(up.requests).toHaveLength(1);
    expect(ofCase(events, "contentBlockDelta")[0]!.value.delta).toEqual({ case: "text", value: "partial" });
    expect(error).toBeInstanceOf(ConnectError);
    expect(error!.code).toBe(Code.Unavailable);
    expect(error!.findDetails(ErrorInfoSchema)[0]!.type).toBe(ErrorType.OVERLOADED);
  });

  it("does not retry client errors and surfaces upstream_status", async () => {
    up.enqueue({ status: 400, json: { type: "error", error: { type: "invalid_request_error", message: "messages: roles must alternate" } } });
    const { error, events } = await collect({ model: "claude-t", maxTokens: 100, messages: [userText("hi")] });
    expect(up.requests).toHaveLength(1);
    expect(ofCase(events, "retry")).toHaveLength(0);
    expect(error!.code).toBe(Code.InvalidArgument);
    expect(error!.rawMessage).toContain("roles must alternate");
    expect(error!.findDetails(ErrorInfoSchema)[0]!.upstreamStatus).toBe(400);
  });

  it("gives up when retry-after exceeds 60s", async () => {
    up.enqueue({ status: 429, headers: { "retry-after": "120" }, json: { error: { type: "rate_limit_error", message: "slow down" } } });
    const { error } = await collect({ model: "claude-t", maxTokens: 100, messages: [userText("hi")] });
    expect(up.requests).toHaveLength(1);
    expect(error!.code).toBe(Code.ResourceExhausted);
    const info = error!.findDetails(ErrorInfoSchema)[0]!;
    expect(info.type).toBe(ErrorType.RATE_LIMITED);
    expect(info.retryAfterMs).toBe(120000n);
  });

  it("retries without a rejected feature and remembers it for the model", async () => {
    const reject = (req: { body: any }) =>
      req.body.thinking
        ? { status: 400, json: { type: "error", error: { type: "invalid_request_error", message: "thinking: Extra inputs are not permitted" } } }
        : { sse: anthropicToolUse };
    up.enqueue(reject, reject);
    const req = { model: "claude-nt", maxTokens: 100, messages: [userText("hi")], thinking: { mode: Thinking_Mode.ENABLED, budgetTokens: 2000 } };
    const first = await collect(req);
    expect(first.error).toBeUndefined();
    expect(up.requests).toHaveLength(2);
    expect(up.requests[0]!.body.thinking).toBeDefined();
    expect(up.requests[1]!.body.thinking).toBeUndefined();
    expect(ofCase(first.events, "retry")).toHaveLength(0);

    up.reset();
    up.enqueue(reject);
    const second = await collect(req);
    expect(second.error).toBeUndefined();
    expect(up.requests).toHaveLength(1);
    expect(up.requests[0]!.body.thinking).toBeUndefined();

    const admin = await adminApi(ts.url, token, "GET", "/api/admin/models/claude-nt");
    expect(admin.body.negotiated_off.map((n: { feature: string }) => n.feature)).toEqual(["thinking"]);
  });

  it("falls back to the next model on persistent overload, skipping smaller context windows", async () => {
    up.fallback = (req) =>
      req.url === "/v1/messages"
        ? { status: 529, json: { type: "error", error: { type: "overloaded_error", message: "Overloaded" } } }
        : { sse: openaiText };
    try {
      const { events, error } = await collect({ model: "claude-fb", maxTokens: 100, messages: [userText("hi")] });
      expect(error).toBeUndefined();
      expect(up.requests.filter((r) => r.url === "/v1/messages")).toHaveLength(3); // 1 + 2 retries
      expect(up.requests.filter((r) => r.url === "/v1/chat/completions").map((r) => r.body.model)).toEqual(["gpt-up"]); // "tiny" skipped
      expect(ofCase(events, "retry")).toHaveLength(3);
      const stop = ofCase(events, "messageStop")[0]!.value;
      expect(stop.upstreamModel).toBe("gpt-up");
      expect(stop.retries).toBe(3);
      expect(ofCase(events, "messageStart")[0]!.value.model).toBe("claude-fb");
    } finally {
      up.fallback = () => ({ status: 500, json: {} });
    }
  });

  it("sends Ping events while the upstream is silent", async () => {
    up.enqueue({ sse: anthropicToolUse, chunkSize: 60, delayMs: 60 });
    const { events } = await collect({ model: "claude-t", maxTokens: 100, messages: [userText("hi")] });
    expect(ofCase(events, "ping").length).toBeGreaterThan(0);
    expect(ofCase(events, "messageStop")).toHaveLength(1);
  });

  it("rejects unknown models and invalid requests before streaming", async () => {
    const unknown = await collect({ model: "nope", maxTokens: 10, messages: [userText("x")] });
    expect(unknown.error!.code).toBe(Code.NotFound);
    const noMax = await collect({ model: "claude-t", maxTokens: 0, messages: [userText("x")] });
    expect(noMax.error!.code).toBe(Code.InvalidArgument);
    const badJson = await collect(
      create(StreamMessageRequestSchema, {
        model: "claude-t",
        maxTokens: 10,
        messages: [{ role: Role.ASSISTANT, content: [{ block: { case: "toolUse", value: { id: "t", name: "X", inputJson: "{oops" } } }] }],
      }),
    );
    expect(badJson.error!.code).toBe(Code.InvalidArgument);
    expect(up.requests).toHaveLength(0);
  });
});

describe("accounting and misc", () => {
  it("persists usage records per user with cost", async () => {
    up.enqueue({ sse: anthropicThinkingText });
    await collect({ model: "claude-t", maxTokens: 100, messages: [userText("hi")], metadata: { sessionId: "ses_usage", requestClass: 3 } });
    const r = await adminApi(ts.url, token, "GET", "/api/admin/usage?limit=5");
    const rec = r.body.usage.find((u: { session_id: string }) => u.session_id === "ses_usage");
    expect(rec).toMatchObject({ model: "claude-t", status: "ok", request_class: "compaction", input_tokens: 120, output_tokens: 42, cache_read_input_tokens: 1000, cache_creation_input_tokens: 50, stop_reason: "end_turn" });
    expect(rec.cost_usd).toBeGreaterThan(0);
    expect(rec.device_id).toMatch(/^dev_/);
  });

  it("counts tokens exactly via Anthropic and estimates elsewhere", async () => {
    up.enqueue({ json: { input_tokens: 77 } });
    const a = await gw.countTokens({ model: "claude-t", messages: [userText("hello there")] });
    expect(a).toMatchObject({ inputTokens: 77n, estimated: false });
    expect(up.requests[0]!.url).toBe("/v1/messages/count_tokens");
    expect(up.requests[0]!.body.max_tokens).toBeUndefined();
    const o = await gw.countTokens({ model: "gpt-t", messages: [userText("hello there, how are you?")] });
    expect(o.estimated).toBe(true);
    expect(o.inputTokens).toBeGreaterThan(0n);
  });

  it("enforces the SSRF guard unless allow_private_network is set", async () => {
    for (const base_url of ["http://127.0.0.1:9", "http://169.254.169.254/latest", "http://[::1]:8080", "http://localhost:11434", "file:///etc/passwd"]) {
      const r = await adminApi(ts.url, token, "POST", "/api/admin/providers", { type: "openai-chat", base_url });
      expect(r.status, base_url).toBeGreaterThanOrEqual(400);
    }
    const ok = await adminApi(ts.url, token, "POST", "/api/admin/providers", { id: "ollama", type: "openai-chat", base_url: "http://127.0.0.1:11434/v1", allow_private_network: true });
    expect(ok.status).toBe(201);
    // turning the flag off re-validates the stored URL
    const off = await adminApi(ts.url, token, "PATCH", "/api/admin/providers/ollama", { allow_private_network: false });
    expect(off.status).toBe(403);
  });

  it("never returns provider keys and stores them encrypted", async () => {
    const r = await adminApi(ts.url, token, "GET", "/api/admin/providers/anthropic-mock");
    expect(r.body).toMatchObject({ api_key_set: true, api_key_hint: "…1234", header_names: ["anthropic-beta"] });
    expect(JSON.stringify(r.body)).not.toContain("sk-ant-secret");
    const { getDb, schema } = await import("../src/db/index.ts");
    const rows = await getDb().select().from(schema.provider);
    const raw = JSON.stringify(rows);
    expect(raw).not.toContain("sk-ant-secret");
    expect(raw).not.toContain("test-beta");
    expect(rows.find((p) => p.id === "anthropic-mock")!.apiKeyEnc).toMatch(/^v1\./);
  });
});
