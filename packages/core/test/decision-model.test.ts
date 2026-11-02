/**
 * The `decision_model` provider (typed decision models such as TypeSafe's Jev) against a fake decisions
 * server on a random local port: request shape, label mapping, the probability / margin thresholds with
 * on_uncertain, strict answer validation, timeouts and caching (unit), then the admin API (write-only key,
 * reuse of a gateway provider's key) and the chain rules -> decision_model -> llm through DecisionService.
 * Never talks to the real OpenRouter or TypeSafe APIs.
 */
import { createServer, type IncomingHttpHeaders, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { resolve } from "node:path";
import { createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { clearDecisionCaches, DecisionCache } from "../src/decision/cache.ts";
import { DecisionModelProvider, parseDecisionAnswer, parseQuestion, renderState, type DecisionModelProviderOptions } from "../src/decision/decision-model.ts";
import { RulesProvider } from "../src/decision/rules-provider.ts";
import { DecisionChain, validateDecideRequest } from "../src/decision/service.ts";
import { ProviderError, type DecideInput, type DecisionContext, type DecisionProvider, type ProviderDecision } from "../src/decision/types.ts";
import { Decision, DecisionService } from "../src/gen/harness/decision/v1/decision_pb.ts";
import { MockUpstream, sse } from "./helpers/mock-upstream.ts";
import { adminApi, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const PROMPTS = resolve(import.meta.dirname, "../prompts");

const ctx: DecisionContext = {
  principal: { user: { id: "usr_1", email: "u@test.local", name: "u", role: "user" }, deviceId: "dev_1", via: "access_token" },
};

function input(command: string, extra: Partial<DecideInput> = {}): DecideInput {
  return {
    ...validateDecideRequest({
      toolName: "Bash",
      inputJson: JSON.stringify({ command, description: "run it" }),
      cwd: "/work/proj",
      sessionId: "s1",
      recentUserMessages: ["add the left-pad dependency"],
      assistantRationale: "Installing the package.",
      facts: { subcommands: [command], insideGitRepo: true, gitBranch: "feature/x" },
    }),
    ...extra,
  };
}

type Probs = Record<string, number>;

/** A decisions response shaped like the real one. */
function answer(choice: string, probabilities: Probs, extra: Record<string, unknown> = {}) {
  return {
    model: "typesafe/jev-1.13-20260917",
    answers: { decision: { type: "choice", choice, probabilities, confidence: probabilities[choice] ?? 0 } },
    usage: { input_tokens: 434, output_tokens: 38, cost: 0.000018228 },
    id: "gen-dec-1",
    provider: "TypeSafe",
    ...extra,
  };
}

// --- fake decisions server -----------------------------------------------------------------------

interface Reply {
  status?: number;
  /** JSON body, or a raw string. */
  body: unknown;
  delayMs?: number;
}

let server: Server;
let base = "";
let replies: Reply[] = [];
let seen: { url: string; headers: IncomingHttpHeaders; body: any }[] = [];

beforeAll(async () => {
  server = createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const c of req) chunks.push(c as Buffer);
    seen.push({ url: req.url ?? "", headers: req.headers, body: JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}") });
    const r = replies.shift() ?? { status: 500, body: { error: { message: "no reply queued", code: 500 } } };
    if (r.delayMs) await new Promise((x) => setTimeout(x, r.delayMs));
    if (res.destroyed) return;
    res.writeHead(r.status ?? 200, { "content-type": "application/json" }).end(typeof r.body === "string" ? r.body : JSON.stringify(r.body));
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

afterAll(async () => {
  server.closeAllConnections();
  await new Promise<void>((r) => server.close(() => r()));
});

beforeEach(() => {
  replies = [];
  seen = [];
});

const reply = (...r: Reply[]) => replies.push(...r);

function provider(extra: Partial<DecisionModelProviderOptions> = {}) {
  return new DecisionModelProvider({
    id: "decision_model",
    url: `${base}/api/alpha/decisions`,
    model: "typesafe/jev-1.13",
    apiKey: async () => "sk-test-key",
    // the fake server listens on loopback; real endpoints never need this
    allowPrivateNetwork: true,
    promptsDir: PROMPTS,
    ...extra,
  });
}

// --- unit --------------------------------------------------------------------------------------

describe("decision_model provider", () => {
  it("POSTs model, rendered state and one choice question with the bearer key", async () => {
    reply({ body: answer("allow", { allow: 1, ask: 0, deny: 0 }) });
    const r = await provider().decide(input("npm install left-pad"), ctx);
    expect(r).toMatchObject({ decision: "allow", confidence: 1, costUsd: 0.000018228, categories: [] });
    expect(r.reason).toContain("typesafe/jev-1.13-20260917");

    const s = seen[0]!;
    expect(s.url).toBe("/api/alpha/decisions");
    expect(s.headers.authorization).toBe("Bearer sk-test-key");
    expect(s.headers["content-type"]).toBe("application/json");
    expect(Object.keys(s.body).sort()).toEqual(["model", "questions", "state"]);
    expect(s.body.model).toBe("typesafe/jev-1.13");
    expect(typeof s.body.state).toBe("string");
    expect(s.body.state).toBe(renderState(input("npm install left-pad")));
    expect(s.body.state).toContain("permission_mode: auto");
    expect(s.body.state).toContain('"command": "npm install left-pad"');
    expect(s.body.state).toContain("cwd: /work/proj");
    expect(s.body.state).toContain("git_branch: feature/x");
    expect(s.body.state).toContain("add the left-pad dependency");
    expect(s.body.state).toContain("Installing the package.");
    expect(s.body.state).not.toContain("classify");
    expect(Object.keys(s.body.questions)).toEqual(["decision"]);
    const q = s.body.questions.decision;
    expect(q.type).toBe("choice");
    expect(q.instructions).toMatch(/auto permission mode/);
    expect(Object.keys(q.criteria).sort()).toEqual(["allow", "ask", "deny"]);
  });

  it("renders the state deterministically and truncates large inputs", () => {
    const big = "x".repeat(50_000);
    const a = renderState(input(big));
    expect(a).toBe(renderState(input(big)));
    expect(a.length).toBeLessThan(30_000);
    expect(a).toMatch(/truncated \d+ characters/);
  });

  it("sends no Authorization header without a key", async () => {
    reply({ body: answer("allow", { allow: 0.99, ask: 0.01, deny: 0 }) });
    await provider({ apiKey: undefined }).decide(input("x"), ctx);
    expect(seen[0]!.headers.authorization).toBeUndefined();
  });

  it("maps confident allow, ask and deny; a confident ask ends the chain", async () => {
    reply({ body: answer("allow", { allow: 0.95, ask: 0.04, deny: 0.01 }) });
    expect(await provider().decide(input("a"), ctx)).toMatchObject({ decision: "allow", confidence: 0.95 });

    reply({ body: answer("deny", { allow: 0.0, ask: 0.1, deny: 0.9 }) });
    const d = await provider().decide(input("b"), ctx);
    expect(d).toMatchObject({ decision: "deny", confidence: 0.9 });
    expect(d.reason).toMatch(/^Denied by Typed decision model/);

    reply({ body: answer("ask", { allow: 0.05, ask: 0.9, deny: 0.05 }) });
    expect(await provider().decide(input("c"), ctx)).toMatchObject({ decision: "ask", final: true, confidence: 0.9 });
  });

  it("low probability or low margin follows on_uncertain: next defers, ask ends the chain", async () => {
    const lowProb = { allow: 0.7, ask: 0.2, deny: 0.1 }; // p < 0.8
    const lowMargin = { allow: 0.85, ask: 0.15, deny: 0 }; // p passes 0.8, margin 0.7
    reply({ body: answer("allow", lowProb) });
    const n = await provider().decide(input("a"), ctx);
    expect(n).toMatchObject({ decision: "ask", confidence: 0.7 });
    expect(n.final).toBeUndefined();
    expect(n.reason).toMatch(/not sure \(leans allow/);

    reply({ body: answer("allow", lowProb) });
    expect(await provider({ onUncertain: "ask" }).decide(input("a"), ctx)).toMatchObject({ decision: "ask", final: true });

    // margin: 0.85 - 0.15 = 0.7 is below a 0.75 margin requirement
    reply({ body: answer("allow", lowMargin) });
    const m = await provider({ minMargin: 0.75 }).decide(input("b"), ctx);
    expect(m).toMatchObject({ decision: "ask" });
    expect(m.final).toBeUndefined();
    reply({ body: answer("deny", { allow: 0.45, ask: 0, deny: 0.55 }) });
    expect(await provider({ minProbability: 0.5, onUncertain: "ask" }).decide(input("b"), ctx)).toMatchObject({ decision: "ask", final: true });

    // the same answers pass with looser thresholds
    reply({ body: answer("allow", lowProb) });
    expect(await provider({ minProbability: 0.6, minMargin: 0.4 }).decide(input("a"), ctx)).toMatchObject({ decision: "allow" });

    // through the chain: next falls through to the following provider, ask stops there
    const after = new Fake("llm", { decision: "deny", reason: "from llm", categories: [] });
    reply({ body: answer("allow", lowProb) });
    expect(await new DecisionChain([provider(), after]).decide(input("a"), ctx)).toMatchObject({ decision: "deny", provider: "llm" });
    reply({ body: answer("allow", lowProb) });
    const stop = await new DecisionChain([provider({ onUncertain: "ask" }), after]).decide(input("a"), ctx);
    expect(stop).toMatchObject({ decision: "ask", provider: "decision_model" });
    expect(after.calls).toBe(1);
  });

  it("malformed answers and upstream errors are provider errors (the chain's on_error)", async () => {
    const bad: unknown[] = [
      "not json",
      { answers: {} },
      { answers: { decision: { type: "noul", choice: "allow", probabilities: { allow: 1, ask: 0, deny: 0 } } } },
      answer("maybe", { allow: 1, ask: 0, deny: 0 }),
      answer("allow", { allow: 1, ask: 0 }),
      answer("allow", { allow: 1, ask: 0, deny: 0, other: 0 }),
      answer("allow", { allow: "1", ask: 0, deny: 0 } as unknown as Probs),
      answer("allow", { allow: 1.5, ask: 0, deny: 0 }),
      answer("allow", { allow: 0.2, ask: 0.7, deny: 0.1 }), // the choice is not the top label
      { error: { message: "Model typesafe/jev does not exist", code: 400 } },
    ];
    for (const body of bad) {
      reply({ body });
      const err = await provider().decide(input("x"), ctx).catch((e) => e);
      expect(err, JSON.stringify(body)).toBeInstanceOf(ProviderError);
    }
    reply({ status: 400, body: { error: { message: "Model typesafe/jev does not exist", code: 400 } } });
    await expect(provider().decide(input("x"), ctx)).rejects.toThrow(/HTTP 400: Model typesafe\/jev does not exist/);
    reply({ status: 401, body: { error: { message: "No auth credentials found", code: 401 } } });
    const auth = await provider().decide(input("x"), ctx).catch((e) => e);
    expect((auth as ProviderError).code).toBe("config");

    reply({ body: answer("allow", { allow: 0.2, ask: 0.7, deny: 0.1 }) });
    expect(await new DecisionChain([provider()], "ask").decide(input("x"), ctx)).toMatchObject({ decision: "ask", provider: "decision_model" });
    const next = new Fake("llm", { decision: "allow", reason: "ok", categories: [] });
    reply({ status: 503, body: { error: { message: "busy", code: 503 } } });
    const r = await new DecisionChain([provider(), next], "next").decide(input("x"), ctx);
    expect(r).toMatchObject({ decision: "allow", provider: "llm" });
    expect(r.errors[0]).toMatch(/^decision_model: Decision model returned HTTP 503: busy/);
  });

  it("times out", async () => {
    reply({ body: answer("allow", { allow: 1, ask: 0, deny: 0 }), delayMs: 1_000 });
    const err = await provider({ timeoutMs: 100 }).decide(input("x"), ctx).catch((e) => e);
    expect(err).toBeInstanceOf(ProviderError);
    expect((err as ProviderError).code).toBe("timeout");
    expect((err as Error).message).toMatch(/timed out after 100 ms/);
  });

  it("is blocked by the SSRF guard without allow_private_network", async () => {
    const err = await provider({ allowPrivateNetwork: false }).decide(input("x"), ctx).catch((e) => e);
    expect((err as ProviderError).code).toBe("config");
    expect(seen).toHaveLength(0);
  });

  it("caches ALLOW and DENY, not ASK", async () => {
    const cache = new DecisionCache();
    const p = provider({ cache, cacheTtlMs: 60_000 });
    reply({ body: answer("deny", { allow: 0, ask: 0.05, deny: 0.95 }) });
    expect(await p.decide(input("npm install miner"), ctx)).toMatchObject({ decision: "deny", costUsd: 0.000018228 });
    expect(await p.decide(input("npm install miner", { assistantRationale: "again" }), ctx)).toMatchObject({ decision: "deny", cached: true, costUsd: 0 });
    expect(seen).toHaveLength(1);

    reply({ body: answer("allow", { allow: 0.6, ask: 0.3, deny: 0.1 }) }, { body: answer("allow", { allow: 0.6, ask: 0.3, deny: 0.1 }) });
    await p.decide(input("npm install vague"), ctx);
    expect((await p.decide(input("npm install vague"), ctx)).cached).toBeUndefined();
    expect(seen).toHaveLength(3);
  });
});

describe("answer and question parsing", () => {
  it("computes probability and margin and keeps the reported model and cost", () => {
    expect(parseDecisionAnswer(answer("allow", { allow: 0.7, ask: 0.2, deny: 0.1 }))).toEqual({
      choice: "allow",
      probability: 0.7,
      margin: expect.closeTo(0.5, 9),
      model: "typesafe/jev-1.13-20260917",
      costUsd: 0.000018228,
    });
    expect(parseDecisionAnswer({ answers: { decision: { type: "choice", choice: "ask", probabilities: { allow: 0.5, ask: 0.5, deny: 0 } } } })).toMatchObject({ choice: "ask", margin: 0 });
    expect(parseDecisionAnswer(answer("allow", { allow: Number.NaN, ask: 0, deny: 0 }))).toBeTypeOf("string");
  });

  it("the shipped question file is valid", async () => {
    const { readFileSync } = await import("node:fs");
    const q = parseQuestion(readFileSync(resolve(PROMPTS, "decision/typed/question.json"), "utf8"));
    expect(typeof q).toBe("object");
    expect(parseQuestion('{"type":"choice","instructions":"x","criteria":{"allow":"a","deny":"d"}}')).toMatch(/exactly the labels/);
    expect(parseQuestion('{"type":"score","instructions":"x","criteria":{}}')).toMatch(/choice/);
  });
});

describe("chain order rules -> decision_model -> llm", () => {
  it("rules decide first, the typed model next, the llm only when the typed model defers", async () => {
    const llm = new Fake("llm", { decision: "allow", reason: "llm says fine", categories: [], costUsd: 0.001 });
    const chain = () => new DecisionChain([new RulesProvider(), provider(), llm], "ask");

    // rules hard-deny and fast-allow never reach the typed model
    expect(await chain().decide(input("rm -rf /"), ctx)).toMatchObject({ decision: "deny", provider: "rules" });
    expect(await chain().decide(input("git status"), ctx)).toMatchObject({ decision: "allow", provider: "rules" });
    expect(seen).toHaveLength(0);

    reply({ body: answer("deny", { allow: 0.02, ask: 0.03, deny: 0.95 }) });
    expect(await chain().decide(input("npm install left-pad"), ctx)).toMatchObject({ decision: "deny", provider: "decision_model", confidence: 0.95 });
    expect(llm.calls).toBe(0);

    reply({ body: answer("allow", { allow: 0.5, ask: 0.3, deny: 0.2 }) });
    const r = await chain().decide(input("npm install left-pad"), ctx);
    expect(r).toMatchObject({ decision: "allow", provider: "llm" });
    expect(r.costUsd).toBeCloseTo(0.001 + 0.000018228, 12);
    expect(llm.calls).toBe(1);
    expect(seen).toHaveLength(2);
  });
});

class Fake implements DecisionProvider {
  calls = 0;
  readonly type = "llm" as const;
  readonly displayName: string;
  constructor(
    readonly id: string,
    private readonly answer: ProviderDecision,
  ) {
    this.displayName = `fake ${id}`;
  }
  async decide(): Promise<ProviderDecision> {
    this.calls++;
    return this.answer;
  }
  async healthy() {
    return true;
  }
}

// --- through the server --------------------------------------------------------------------------

describe("admin API and DecisionService", () => {
  let ts: TestServer;
  let token: string;
  let up: MockUpstream;
  let dec: Client<typeof DecisionService>;
  const api = (method: string, path: string, body?: unknown) => adminApi(ts.url, token, method, path, body);
  const endpoint = () => `${base}/api/alpha/decisions`;

  const bash = (command: string) => ({
    sessionId: "11111111-2222-4333-8444-555555555555",
    toolName: "Bash",
    inputJson: JSON.stringify({ command }),
    cwd: "/work/proj",
    recentUserMessages: ["add the left-pad dependency"],
  });

  function classify(v: unknown): string {
    const json = JSON.stringify(v);
    return sse([
      ["message_start", { type: "message_start", message: { id: "m", type: "message", role: "assistant", content: [], usage: { input_tokens: 10, output_tokens: 1 } } }],
      ["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "tool_use", id: "t", name: "classify", input: {} } }],
      ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "input_json_delta", partial_json: json } }],
      ["content_block_stop", { type: "content_block_stop", index: 0 }],
      ["message_delta", { type: "message_delta", delta: { stop_reason: "tool_use", stop_sequence: null }, usage: { output_tokens: 5 } }],
      ["message_stop", { type: "message_stop" }],
    ]);
  }

  beforeAll(async () => {
    up = await new MockUpstream().start();
    ts = await startTestServer();
    token = (await deviceLogin(ts)).accessToken;
    const transport = createConnectTransport({
      baseUrl: ts.url,
      httpVersion: "1.1",
      interceptors: [
        (next) => async (req) => {
          req.header.set("authorization", `Bearer ${token}`);
          return next(req);
        },
      ],
    });
    dec = createClient(DecisionService, transport);
    expect((await api("POST", "/api/admin/providers", { id: "anthropic-mock", type: "anthropic", base_url: up.url, api_key: "sk-ant-x", allow_private_network: true })).status).toBe(201);
    expect((await api("POST", "/api/admin/models", { id: "clf", provider_id: "anthropic-mock", upstream_model: "clf-up", context_window: 200000, max_output_tokens: 4096 })).status).toBe(201);
    expect((await api("PUT", "/api/admin/slots", { main: "clf", small: "clf", classifier: "clf" })).status).toBe(200);
    // a gateway provider on the fake decisions host, like the admin's OpenRouter provider
    expect(
      (await api("POST", "/api/admin/providers", { id: "or-chat", type: "openai-chat", base_url: `${base}/api/v1`, api_key: "sk-or-gateway-key", allow_private_network: true })).status,
    ).toBe(201);
    // the same key on another host must never be sent to the decisions endpoint
    expect(
      (await api("POST", "/api/admin/providers", { id: "elsewhere", type: "openai-chat", base_url: `http://localhost:${new URL(base).port}/v1`, api_key: "sk-other", allow_private_network: true }))
        .status,
    ).toBe(201);
  });

  afterAll(async () => {
    await ts.close();
    await up.stop();
  });

  beforeEach(() => {
    up.reset();
    clearDecisionCaches();
  });

  it("keeps the API key write-only and validates settings", async () => {
    const body = {
      providers: [
        { type: "rules" },
        {
          type: "decision_model",
          display_name: "Jev",
          settings: { endpoint: endpoint(), model: "jev-1.13.0", api_key: "sk-tsafe-secret-1", allow_private_network: true, timeout_ms: 2000, on_uncertain: "ask" },
        },
      ],
    };
    const put = await api("PUT", "/api/admin/decision", body);
    expect(put.status, JSON.stringify(put.body)).toBe(200);
    const got = (await api("GET", "/api/admin/decision")).body;
    expect(got.config.providers[1]).toEqual({
      type: "decision_model",
      id: "decision_model",
      display_name: "Jev",
      settings: {
        endpoint: endpoint(),
        model: "jev-1.13.0",
        allow_private_network: true,
        timeout_ms: 2000,
        min_probability: 0.8,
        min_margin: 0.15,
        on_uncertain: "ask",
        api_key_set: true,
      },
    });
    expect(JSON.stringify(got)).not.toContain("sk-tsafe-secret-1");
    expect(JSON.stringify(put.body)).not.toContain("sk-tsafe-secret-1");

    // omitting the key keeps it; it is sent as the bearer token
    const { api_key: _k, ...noKey } = body.providers[1]!.settings!;
    expect((await api("PUT", "/api/admin/decision", { providers: [body.providers[0], { ...body.providers[1], settings: noKey }] })).status).toBe(200);
    reply({ body: answer("allow", { allow: 0.99, ask: 0.01, deny: 0 }) });
    const r = await dec.decide(bash("npm install left-pad"));
    expect(r).toMatchObject({ decision: Decision.ALLOW, provider: "decision_model", confidence: 0.99 });
    expect(r.costUsd).toBeCloseTo(0.000018228, 12);
    expect(seen.at(-1)!.headers.authorization).toBe("Bearer sk-tsafe-secret-1");
    expect(seen.at(-1)!.body.model).toBe("jev-1.13.0");

    // null clears it (allowed for a custom endpoint)
    expect((await api("PUT", "/api/admin/decision", { providers: [{ type: "decision_model", settings: { ...noKey, api_key: null } }] })).status).toBe(200);
    expect((await api("GET", "/api/admin/decision")).body.config.providers[0].settings.api_key_set).toBe(false);

    const listed = await dec.listProviders({});
    expect(listed.providers.map((p) => [p.id, p.type, p.model, p.healthy])).toEqual([["decision_model", "decision_model", "jev-1.13.0", true]]);

    const bad = async (settings: unknown, status = 400) => {
      const res = await api("PUT", "/api/admin/decision", { providers: [{ type: "decision_model", id: "dm", settings }] });
      expect(res.status, JSON.stringify(res.body)).toBe(status);
    };
    await bad({}); // openrouter needs a key
    await bad({ endpoint: "typesafe" });
    await bad({ endpoint: "ftp://x.example.com", api_key: "k" });
    await bad({ endpoint: "http://decisions.example.com/v1", api_key: "k" }); // plain http on a public host
    await bad({ endpoint: endpoint(), api_key: "k" }, 403); // loopback without allow_private_network
    await bad({ endpoint: "https://decisions.example.com/v1" }); // custom endpoints need a model
    await bad({ api_key: "k", min_probability: 1.5 });
    await bad({ api_key: "k", min_margin: -0.1 });
    await bad({ api_key: "k", on_uncertain: "deny" });
    await bad({ api_key: "k", timeout_ms: 10 });
    await bad({ api_key: "k", surprise: true });
    await bad({ api_key: "k", api_key_provider: "or-chat" });
    await bad({ api_key_provider: "missing" });
    await bad({ api_key_provider: "or-chat" }); // or-chat is not on openrouter.ai
    await bad({ endpoint: endpoint(), allow_private_network: true, api_key_provider: "elsewhere" });
    expect((await api("PUT", "/api/admin/decision", { providers: [{ type: "decision_model", id: "decision_model:jev", settings: { api_key: "k" } }] })).body.config.providers[0]).toMatchObject({
      id: "decision_model:jev",
      settings: { endpoint: "openrouter", model: "typesafe/jev-1.13", timeout_ms: 3000, on_uncertain: "next", api_key_set: true },
    });
  });

  it("reuses a gateway provider's key on the same host, read at decision time", async () => {
    const put = await api("PUT", "/api/admin/decision", {
      providers: [{ type: "rules" }, { type: "decision_model", settings: { endpoint: endpoint(), allow_private_network: true, model: "typesafe/jev-1.13", api_key_provider: "or-chat" } }],
    });
    expect(put.status, JSON.stringify(put.body)).toBe(200);
    const got = (await api("GET", "/api/admin/decision")).body;
    expect(got.config.providers[1].settings).toMatchObject({ api_key_provider: "or-chat", api_key_set: false });
    expect(JSON.stringify(got)).not.toContain("sk-or-gateway-key");

    reply({ body: answer("allow", { allow: 1, ask: 0, deny: 0 }) });
    expect(await dec.decide(bash("npm install left-pad"))).toMatchObject({ decision: Decision.ALLOW, provider: "decision_model" });
    expect(seen.at(-1)!.headers.authorization).toBe("Bearer sk-or-gateway-key");

    // a rotated gateway key is picked up without touching the decision config
    expect((await api("PATCH", "/api/admin/providers/or-chat", { api_key: "sk-or-rotated" })).status).toBe(200);
    reply({ body: answer("allow", { allow: 1, ask: 0, deny: 0 }) });
    await dec.decide(bash("npm install other-pad"));
    expect(seen.at(-1)!.headers.authorization).toBe("Bearer sk-or-rotated");
  });

  it("runs rules -> decision_model -> llm", async () => {
    const put = await api("PUT", "/api/admin/decision", {
      providers: [
        { type: "rules" },
        { type: "decision_model", settings: { endpoint: endpoint(), allow_private_network: true, model: "typesafe/jev-1.13", api_key_provider: "or-chat" } },
        { type: "llm" },
      ],
      on_error: "next",
    });
    expect(put.status, JSON.stringify(put.body)).toBe(200);
    seen = [];

    expect(await dec.decide(bash("rm -rf /"))).toMatchObject({ decision: Decision.DENY, provider: "rules" });
    expect(seen).toHaveLength(0);

    reply({ body: answer("deny", { allow: 0, ask: 0.1, deny: 0.9 }) });
    expect(await dec.decide(bash("npm install miner"))).toMatchObject({ decision: Decision.DENY, provider: "decision_model" });
    expect(up.requests).toHaveLength(0);

    // unsure: deferred to the llm classifier
    reply({ body: answer("allow", { allow: 0.6, ask: 0.3, deny: 0.1 }) });
    up.enqueue({ sse: classify({ decision: "allow", reason: "The user asked for it", categories: [], confidence: 0.9 }) });
    expect(await dec.decide(bash("npm install left-pad"))).toMatchObject({ decision: Decision.ALLOW, provider: "llm", reason: "The user asked for it" });
    expect(up.requests).toHaveLength(1);

    // an erroring typed model with on_error next: the llm decides
    reply({ status: 500, body: { error: { message: "boom", code: 500 } } });
    up.enqueue({ sse: classify({ decision: "ask", reason: "Unclear", categories: [], confidence: 0.9 }) });
    expect(await dec.decide(bash("npm install odd-pad"))).toMatchObject({ decision: Decision.ASK, provider: "none" });

    const listed = await dec.listProviders({});
    expect(listed.providers.map((p) => p.id)).toEqual(["rules", "decision_model", "llm"]);
  });
});
