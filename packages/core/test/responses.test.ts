/** Contract tests for the OpenAI Responses adapter (provider type `openai-responses`) against a mock upstream. */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { ErrorInfoSchema, ErrorType } from "../src/gen/harness/common/v1/errors_pb.ts";
import { Effort, Role, StopReason, ToolChoice_Mode } from "../src/gen/harness/llm/v1/content_pb.ts";
import type { StreamMessageResponse } from "../src/gen/harness/llm/v1/gateway_pb.ts";
import { decodeReasoningSignature, encodeReasoningSignature, RESPONSES_SIGNATURE_PREFIX } from "../src/gateway/adapters/openai-responses.ts";
import { responsesErrorEvent, responsesFailedAfterContent, responsesIncomplete, responsesReasoningToolCall, responsesText } from "./fixtures/responses.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { adminApi, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let up: MockUpstream;
let token: string;
let gw: TestServer["gateway"];

beforeAll(async () => {
  up = await new MockUpstream().start();
  ts = await startTestServer();
  token = (await deviceLogin(ts)).accessToken;
  gw = ts.as(token).gateway;
  const api = (m: string, p: string, b?: unknown) => adminApi(ts.url, token, m, p, b);
  expect(
    (await api("POST", "/api/admin/providers", { id: "resp", type: "openai-responses", base_url: `${up.url}/v1`, api_key: "sk-resp-9999", allow_private_network: true })).status,
  ).toBe(201);
  expect(
    (
      await api("POST", "/api/admin/models", {
        id: "resp-t",
        provider_id: "resp",
        upstream_model: "gpt-resp-up",
        context_window: 400000,
        max_output_tokens: 64000,
        capabilities: { thinking: true, effort: ["low", "medium", "high"], images: true, parallel_tool_calls: true },
        pricing: { input: 1.25, output: 10, cache_read: 0.125 },
      })
    ).status,
  ).toBe(201);
  expect((await api("POST", "/api/admin/models", { id: "resp-plain", provider_id: "resp", upstream_model: "gpt-plain", context_window: 128000, max_output_tokens: 4000 })).status).toBe(201);
});

afterAll(async () => {
  await ts.close();
  await up.stop();
});

beforeEach(() => up.reset());

type Ev = StreamMessageResponse["event"];
const ofCase = <C extends NonNullable<Ev["case"]>>(evs: Ev[], c: C) => evs.filter((e): e is Extract<Ev, { case: C }> => e.case === c);
const userText = (text: string) => ({ role: Role.USER, content: [{ block: { case: "text" as const, value: { text } } }] });

async function collect(req: Parameters<typeof gw.streamMessage>[0]): Promise<{ events: Ev[]; error?: ConnectError }> {
  const events: Ev[] = [];
  try {
    for await (const r of gw.streamMessage(req)) if (r.event.case !== "ping") events.push(r.event);
  } catch (e) {
    return { events, error: e as ConnectError };
  }
  return { events };
}

describe("OpenAI Responses adapter", () => {
  it("streams text and normalizes usage (cached tokens -> cache_read)", async () => {
    up.enqueue({ sse: responsesText, chunkSize: 23 });
    const { events, error } = await collect({ model: "resp-plain", maxTokens: 100000, system: [{ text: "sys A", cache: true }, { text: "sys B" }], messages: [userText("hi")] });
    expect(error).toBeUndefined();
    const sent = up.requests[0]!;
    expect(sent.url).toBe("/v1/responses");
    expect(sent.headers.authorization).toBe("Bearer sk-resp-9999");
    expect(sent.body).toEqual({
      model: "gpt-plain",
      input: [{ role: "user", content: [{ type: "input_text", text: "hi" }] }],
      stream: true,
      store: false,
      max_output_tokens: 4000,
      instructions: "sys A\n\nsys B",
    });
    expect(events.map((e) => e.case)).toEqual(["messageStart", "contentBlockStart", "contentBlockDelta", "contentBlockDelta", "contentBlockStop", "messageDelta", "messageStop"]);
    expect(ofCase(events, "contentBlockDelta").map((d) => d.value.delta.value)).toEqual(["Hello", " there"]);
    const md = ofCase(events, "messageDelta")[0]!.value;
    expect(md.stopReason).toBe(StopReason.END_TURN);
    expect(md.usage).toMatchObject({ inputTokens: 372n, outputTokens: 9n, cacheReadInputTokens: 128n, cacheCreationInputTokens: 0n });
    expect(ofCase(events, "messageStop")[0]!.value).toMatchObject({ upstreamModel: "gpt-plain", servedModel: "resp-plain" });
  });

  it("maps reasoning + function calls and replays reasoning items unchanged on the next request", async () => {
    up.enqueue({ sse: responsesReasoningToolCall, chunkSize: 41 });
    const tools = [{ name: "Read", description: "Reads a file", inputSchemaJson: '{"type":"object","properties":{"file_path":{"type":"string"}}}' }];
    const first = await collect({
      model: "resp-t",
      maxTokens: 1000,
      messages: [userText("read /a.ts")],
      tools,
      toolChoice: { mode: ToolChoice_Mode.TOOL, toolName: "Read", disableParallelToolUse: true },
      effort: Effort.XHIGH,
    });
    expect(first.error).toBeUndefined();
    expect(up.requests[0]!.body).toMatchObject({
      reasoning: { effort: "high", summary: "auto" }, // xhigh lowered to the highest supported level
      include: ["reasoning.encrypted_content"],
      store: false,
      tools: [{ type: "function", name: "Read", description: "Reads a file", parameters: { type: "object", properties: { file_path: { type: "string" } } }, strict: false }],
      tool_choice: { type: "function", name: "Read" },
      parallel_tool_calls: false,
    });

    const starts = ofCase(first.events, "contentBlockStart").map((s) => s.value.block!.block);
    expect(starts.map((b) => b.case)).toEqual(["thinking", "toolUse"]);
    expect(starts[1]!.value).toMatchObject({ id: "call_R1", name: "Read" });
    const deltas = ofCase(first.events, "contentBlockDelta").map((d) => d.value.delta);
    const thinking = deltas.filter((d) => d.case === "thinking").map((d) => d.value).join("");
    expect(thinking).toBe("Need to read the file.\n\nThen answer.");
    const signature = deltas.find((d) => d.case === "signature")!.value as string;
    expect(signature.startsWith(RESPONSES_SIGNATURE_PREFIX)).toBe(true);
    expect(deltas.filter((d) => d.case === "inputJson").map((d) => d.value).join("")).toBe('{"file_path":"/a.ts"}');
    const md = ofCase(first.events, "messageDelta")[0]!.value;
    expect(md.stopReason).toBe(StopReason.TOOL_USE);
    expect(md.usage).toMatchObject({ inputTokens: 300n, outputTokens: 80n });

    // Next turn: the client sends the blocks back verbatim (plus a foreign signed thinking block).
    up.enqueue({ sse: responsesText });
    const png = new Uint8Array([137, 80, 78, 71]);
    const second = await collect({
      model: "resp-t",
      maxTokens: 1000,
      tools,
      messages: [
        userText("read /a.ts"),
        {
          role: Role.ASSISTANT,
          content: [
            { block: { case: "thinking", value: { thinking: "from another provider", signature: "sig-anthropic" } } },
            { block: { case: "thinking", value: { thinking, signature } } },
            { block: { case: "text", value: { text: "Reading." } } },
            { block: { case: "toolUse", value: { id: "call_R1", name: "Read", inputJson: '{"file_path":"/a.ts"}' } } },
          ],
        },
        {
          role: Role.USER,
          content: [
            {
              block: {
                case: "toolResult",
                value: { toolUseId: "call_R1", content: [{ block: { case: "text", value: { text: "export {}" } } }, { block: { case: "image", value: { mediaType: "image/png", data: png } } }] },
              },
            },
            { block: { case: "text", value: { text: "and now?" } } },
          ],
        },
      ],
    });
    expect(second.error).toBeUndefined();
    expect(up.requests[1]!.body.input).toEqual([
      { role: "user", content: [{ type: "input_text", text: "read /a.ts" }] },
      {
        type: "reasoning",
        id: "rs_1",
        summary: [
          { type: "summary_text", text: "Need to read the file." },
          { type: "summary_text", text: "Then answer." },
        ],
        encrypted_content: "gAAAAENCRYPTED==",
      },
      { role: "assistant", content: "Reading." },
      { type: "function_call", call_id: "call_R1", name: "Read", arguments: '{"file_path":"/a.ts"}' },
      { type: "function_call_output", call_id: "call_R1", output: "export {}" },
      {
        role: "user",
        content: [
          { type: "input_text", text: "Image output of tool call call_R1:" },
          { type: "input_image", image_url: "data:image/png;base64,iVBORw==" },
          { type: "input_text", text: "and now?" },
        ],
      },
    ]);
  });

  it("maps incomplete(max_output_tokens) to MAX_TOKENS", async () => {
    up.enqueue({ sse: responsesIncomplete });
    const { events, error } = await collect({ model: "resp-plain", maxTokens: 5, messages: [userText("long")] });
    expect(error).toBeUndefined();
    expect(ofCase(events, "messageDelta")[0]!.value.stopReason).toBe(StopReason.MAX_TOKENS);
  });

  it("turns error events into Connect errors (not retried when invalid)", async () => {
    up.enqueue({ sse: responsesErrorEvent });
    const { error } = await collect({ model: "resp-plain", maxTokens: 50, messages: [userText("x")] });
    expect(error).toBeInstanceOf(ConnectError);
    expect(error!.code).toBe(Code.InvalidArgument);
    expect(error!.findDetails(ErrorInfoSchema)[0]?.type).toBe(ErrorType.INVALID_ARGUMENT);
    expect(error!.rawMessage).toMatch(/Invalid prompt content/);
    expect(up.requests).toHaveLength(1);
  });

  it("reports response.failed after content as an error without retrying", async () => {
    up.enqueue({ sse: responsesFailedAfterContent });
    const { events, error } = await collect({ model: "resp-plain", maxTokens: 50, messages: [userText("x")] });
    expect(ofCase(events, "contentBlockDelta")).toHaveLength(1);
    expect(error!.code).toBe(Code.Unavailable);
    expect(error!.findDetails(ErrorInfoSchema)[0]?.type).toBe(ErrorType.UPSTREAM);
    expect(up.requests).toHaveLength(1);
  });

  it("negotiates away a rejected `include` and remembers it", async () => {
    up.enqueue(
      { status: 400, json: { error: { message: "Unsupported parameter: 'include' is not supported with this model.", type: "invalid_request_error", param: "include" } } },
      { sse: responsesText },
    );
    const { error } = await collect({ model: "resp-t", maxTokens: 50, messages: [userText("x")] });
    expect(error).toBeUndefined();
    expect(up.requests[0]!.body.include).toEqual(["reasoning.encrypted_content"]);
    expect(up.requests[1]!.body.include).toBeUndefined();
    expect(up.requests[1]!.body.reasoning).toEqual({ summary: "auto" });
    const m = await adminApi(ts.url, token, "GET", "/api/admin/models/resp-t");
    expect(m.body.negotiated_off.map((n: { feature: string }) => n.feature)).toEqual(["include"]);
    await adminApi(ts.url, token, "PATCH", "/api/admin/models/resp-t", { reset_negotiation: true });
  });
});

describe("reasoning signatures", () => {
  it("round-trip the reasoning item and reject foreign signatures", () => {
    const sig = encodeReasoningSignature({ id: "rs_9", summary: [{ type: "summary_text", text: "a" }], encrypted_content: "enc" });
    expect(decodeReasoningSignature(sig)).toEqual({ type: "reasoning", id: "rs_9", summary: [{ type: "summary_text", text: "a" }], encrypted_content: "enc" });
    expect(decodeReasoningSignature("sig-abc")).toBeUndefined();
    expect(decodeReasoningSignature(`${RESPONSES_SIGNATURE_PREFIX}not-json`)).toBeUndefined();
    expect(decodeReasoningSignature(undefined)).toBeUndefined();
  });
});
