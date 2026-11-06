/**
 * What provider limits count on: prices from presets and by hand (never over each other's), the balance estimated from an
 * entered amount, and only a plan's windows being shown. No real API calls: the upstream is a local mock.
 */
import { Code, ConnectError, createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { eq, inArray } from "drizzle-orm";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { computeCost, emptyUsage } from "../src/gateway/cost.ts";
import { Role } from "../src/gen/harness/llm/v1/content_pb.ts";
import { LimitsAudience, ProviderLimitsService } from "../src/gen/harness/llm/v1/limits_pb.ts";
import { estimatedBalance } from "../src/limits/balance.ts";
import { forgetRateLimits } from "../src/limits/headers.ts";
import { fillPresetPrices } from "../src/services/models.ts";
import { PRESETS, presetPriceOf } from "../src/services/presets.ts";
import { anthropicThinkingText } from "./fixtures/upstream.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const OPUS = { input: 4, output: 20, cache_read: 0.2, cache_write: 5 };
const OPUS_PB = { input: 4, output: 20, cacheRead: 0.2, cacheWrite: 5 };

let ts: TestServer;
let up: MockUpstream;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let user: Awaited<ReturnType<typeof deviceLogin>>;
let other: Awaited<ReturnType<typeof deviceLogin>>;
let userId: string;
let otherId: string;

function limits(token: string): Client<typeof ProviderLimitsService> {
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
  return createClient(ProviderLimitsService, transport);
}

async function code(p: Promise<unknown>) {
  try {
    await p;
    return 0;
  } catch (e) {
    return ConnectError.from(e).code;
  }
}

async function stream(token: string, model: string) {
  const req = { model, maxTokens: 100, messages: [{ role: Role.USER, content: [{ block: { case: "text" as const, value: { text: "hi" } } }] }] };
  for await (const _ of ts.as(token).gateway.streamMessage(req)) {
    // drain
  }
}

/** A provider row as an old database holds it: no key needed, nothing is called unless the test points it at the mock. */
async function provider(id: string, f: { owner?: string; presetId?: string; baseUrl?: string; type?: string } = {}) {
  const now = new Date();
  await getDb()
    .insert(schema.provider)
    .values({
      id,
      ownerUserId: f.owner ?? null,
      kind: "llm",
      type: f.type ?? "anthropic",
      presetId: f.presetId ?? null,
      baseUrl: f.baseUrl ?? up.url,
      allowPrivateNetwork: !f.baseUrl,
      enabled: true,
      createdAt: now,
      updatedAt: now,
    });
}

/** A model already in the catalog from before, without a price. */
async function model(id: string, providerId: string, upstreamModel: string) {
  const now = new Date();
  await getDb().insert(schema.model).values({ id, providerId, upstreamModel, displayName: id, contextWindow: 1000, maxOutputTokens: 100, capabilities: {}, fallbacks: [], enabled: true, sortOrder: 0, createdAt: now, updatedAt: now });
}

const priceOf = async (id: string) => (await getDb().query.model.findFirst({ where: eq(schema.model.id, id) }))?.pricing;

async function record(uid: string, providerId: string, cost: number, at = new Date(), priced = true) {
  await getDb()
    .insert(schema.usageRecord)
    .values({ id: `usg_${Math.random().toString(36).slice(2)}`, userId: uid, modelId: "m", providerId, inputTokens: 1, costUsd: cost, priced, status: "ok", createdAt: at });
}

const of = async (token: string, providerId: string) => (await limits(token).getProviderLimits({ providerIds: [providerId] })).providers[0]!;

beforeAll(async () => {
  up = await new MockUpstream().start();
  ts = await startTestServer();
  admin = await deviceLogin(ts);
  user = await createUserAndLogin(ts, admin.cookie, "prices-user@test.local");
  other = await createUserAndLogin(ts, admin.cookie, "prices-other@test.local");
  userId = (await ts.as(user.accessToken).auth.getMe({})).user!.id;
  otherId = (await ts.as(other.accessToken).auth.getMe({})).user!.id;
  // Metered, on the Anthropic preset (a global one here, so it can reach the mock).
  await provider("anth", { presetId: "anthropic" });
  // Metered, nothing known about it.
  await provider("cust");
  // The plan and the provider with a balance API of the real world; nothing here calls them.
  await provider("planp", { presetId: "zai-coding", baseUrl: "https://api.z.ai/api/anthropic" });
  await provider("router", { baseUrl: "https://openrouter.ai/api/v1", type: "openai-chat" });
  // What the running app shows: a personal key on the Anthropic preset, models added before prices existed.
  await provider("u-mine000002", { owner: userId, presetId: "anthropic", baseUrl: "https://api.anthropic.com" });
});

afterAll(async () => {
  await ts.close();
  await up.stop();
});

beforeEach(async () => {
  up.reset();
  up.fallback = () => ({ status: 500, json: { error: { type: "api_error", message: "no mock response queued" } } });
  await getDb().delete(schema.usageRecord);
  await getDb().delete(schema.setting).where(inArray(schema.setting.key, ["provider_limits_policy", "provider_budgets", "provider_balance_estimates"]));
  forgetRateLimits();
});

describe("preset prices", () => {
  it("cover the Anthropic preset, and no plan's", () => {
    const row = { presetId: "anthropic", kind: "llm", type: "anthropic", baseUrl: "https://api.anthropic.com" };
    expect(presetPriceOf(row, "claude-opus-5-5")).toEqual(OPUS);
    expect(presetPriceOf(row, "claude-sonnet-5-5")).toEqual({ input: 2, output: 10, cache_read: 0.1, cache_write: 2.5 });
    expect(presetPriceOf(row, "claude-haiku-5-5")).toMatchObject({ input: 0.1, output: 0.5, cache_read: 0.01, cache_write: 0.125, long_context: { above_tokens: 100_000, input: 0.5 } });
    expect(presetPriceOf(row, "claude-unknown")).toBeUndefined();
    for (const p of PRESETS.filter((x) => x.billing === "plan")) expect(p.default_models.filter((m) => m.pricing)).toEqual([]);
    // Billed in another currency, not confirmed: stays empty though the models are shared with the international platform.
    expect(PRESETS.find((p) => p.id === "moonshot-cn")!.default_models.filter((m) => m.pricing)).toEqual([]);
    expect(PRESETS.find((p) => p.id === "moonshot")!.default_models.find((m) => m.upstream_model === "kimi-k3")!.pricing).toMatchObject({ input: 3 });
    expect(PRESETS.find((p) => p.id === "moonshot")!.default_models.find((m) => m.upstream_model === "kimi-k2.7-code")!.pricing).toBeUndefined();
  });

  it("price a model when it is added; a price given or listed goes first", async () => {
    const a = ts.as(admin.accessToken).providers;
    const added = await a.addModel({ providerId: "anth", upstreamModel: "claude-opus-5-5", id: "add-opus" });
    expect(added.model!.pricing).toMatchObject(OPUS_PB);
    expect(added.model!.pricingSetByHand).toBe(false);
    await a.addModel({ providerId: "anth", upstreamModel: "claude-unlisted", id: "add-other" });
    expect(await priceOf("add-other")).toBeNull();
    // A custom provider has no preset to say anything.
    await a.addModel({ providerId: "cust", upstreamModel: "claude-opus-5-5", id: "add-cust" });
    expect(await priceOf("add-cust")).toBeNull();
  });

  it("fill a model added earlier at startup, for a personal provider too, with nothing asked of the service", async () => {
    await model("u-mine000002/claude-opus-5-5", "u-mine000002", "claude-opus-5-5");
    await model("u-mine000002/claude-other", "u-mine000002", "claude-other");
    const calls = up.requests.length;
    await fillPresetPrices();
    expect(await priceOf("u-mine000002/claude-opus-5-5")).toEqual(OPUS);
    expect(await priceOf("u-mine000002/claude-other")).toBeNull();
    expect(up.requests.length).toBe(calls);
    // The card shows it.
    const { models } = (await ts.as(user.accessToken).providers.listProviders({})).providers.find((p) => p.id === "u-mine000002")!;
    expect(models.find((m) => m.upstreamModel === "claude-opus-5-5")!.pricing).toMatchObject(OPUS_PB);
  });

  it("fill at discovery, even when the service's list fails", async () => {
    await model("disc-sonnet", "anth", "claude-sonnet-5-5");
    up.enqueue({ status: 401, json: { error: { type: "authentication_error", message: "bad key" } } });
    expect(await code(ts.as(admin.accessToken).providers.discoverModels({ providerId: "anth" }))).not.toBe(0);
    expect(await priceOf("disc-sonnet")).toMatchObject({ input: 2 });
    // A list without prices (Anthropic's) takes nothing away, and a model added from it has the preset's.
    up.enqueue({ json: { data: [{ id: "claude-sonnet-5-5", display_name: "Sonnet" }, { id: "claude-haiku-5-5", display_name: "Haiku" }], has_more: false } });
    await ts.as(admin.accessToken).providers.discoverModels({ providerId: "anth" });
    expect(await priceOf("disc-sonnet")).toMatchObject({ input: 2 });
    await ts.as(admin.accessToken).providers.addModel({ providerId: "anth", upstreamModel: "claude-haiku-5-5", id: "disc-haiku" });
    expect(await priceOf("disc-haiku")).toMatchObject({ input: 0.1, output: 0.5 });
  });

  it("never go over a price that is set", async () => {
    await model("keep", "anth", "claude-opus-5-5");
    await getDb().update(schema.model).set({ pricing: { input: 1, output: 1 } }).where(eq(schema.model.id, "keep"));
    await fillPresetPrices("anth");
    expect(await priceOf("keep")).toEqual({ input: 1, output: 1 });
  });
});

describe("prices by hand", () => {
  it("beat lists and presets, also when set to none, and survive other edits", async () => {
    const a = ts.as(admin.accessToken).providers;
    await a.addModel({ providerId: "cust", upstreamModel: "listed", id: "hand-listed" });
    await a.addModel({ providerId: "cust", upstreamModel: "listed-2", id: "hand-cleared" });
    await a.addModel({ providerId: "anth", upstreamModel: "claude-opus-5-5", id: "hand-preset" });
    expect((await a.setModelPricing({ modelId: "hand-listed", pricing: { input: 7, output: 8, cacheRead: 0.7, cacheWrite: 0 } })).model).toMatchObject({ pricing: { input: 7, output: 8, cacheRead: 0.7, cacheWrite: 0 }, pricingSetByHand: true });
    expect(await priceOf("hand-listed")).toEqual({ input: 7, output: 8, cache_read: 0.7 });
    await a.setModelPricing({ modelId: "hand-cleared" });
    await a.setModelPricing({ modelId: "hand-preset" });

    // A list saying otherwise changes neither; a price not set by hand it fills as before.
    const LIST = { data: ["listed", "listed-2", "listed-3"].map((id) => ({ id, pricing: { prompt: "0.000001", completion: "0.000002" } })) };
    await a.addModel({ providerId: "cust", upstreamModel: "listed-3", id: "from-list" });
    up.enqueue({ json: LIST });
    await a.discoverModels({ providerId: "cust" });
    expect(await priceOf("hand-listed")).toEqual({ input: 7, output: 8, cache_read: 0.7 });
    expect(await priceOf("hand-cleared")).toBeNull();
    expect(await priceOf("from-list")).toEqual({ input: 1, output: 2 });

    // The preset does not bring back a price someone took away.
    await fillPresetPrices();
    expect(await priceOf("hand-preset")).toBeNull();

    // Setting effort levels keeps the mark.
    await a.setModelEfforts({ modelId: "hand-preset", efforts: [] });
    await fillPresetPrices();
    expect(await priceOf("hand-preset")).toBeNull();
  });

  it("have the access rule of SetModelEfforts, and a plan takes none", async () => {
    const a = ts.as(admin.accessToken).providers;
    await a.addModel({ providerId: "cust", upstreamModel: "access", id: "hand-access" });
    await model("u-mine000002/own", "u-mine000002", "own");
    const pricing = { input: 1, output: 2, cacheRead: 0, cacheWrite: 0 };
    expect(await code(ts.as(user.accessToken).providers.setModelPricing({ modelId: "hand-access", pricing }))).toBe(Code.PermissionDenied);
    expect(await code(ts.as(other.accessToken).providers.setModelPricing({ modelId: "u-mine000002/own", pricing }))).toBe(Code.NotFound);
    // Admins do not reach a user's own key either.
    expect(await code(a.setModelPricing({ modelId: "u-mine000002/own", pricing }))).toBe(Code.NotFound);
    expect((await ts.as(user.accessToken).providers.setModelPricing({ modelId: "u-mine000002/own", pricing })).model!.pricingSetByHand).toBe(true);
    expect(await code(a.setModelPricing({ modelId: "hand-access", pricing: { ...pricing, input: -1 } }))).toBe(Code.InvalidArgument);
    expect(await code(a.setModelPricing({ modelId: "hand-access", pricing: { ...pricing, input: Number.NaN } }))).toBe(Code.InvalidArgument);

    await model("plan-m", "planp", "glm-5.3");
    expect(await code(a.setModelPricing({ modelId: "plan-m", pricing }))).toBe(Code.InvalidArgument);
  });
});

describe("computeCost", () => {
  it("charges the long-context prices for the whole request past the threshold, cached tokens counted", () => {
    const p = presetPriceOf({ presetId: "anthropic", kind: "llm", type: "anthropic", baseUrl: "" }, "claude-haiku-5-5")!;
    const short = { ...emptyUsage(), input_tokens: 100_000, output_tokens: 1_000_000 };
    expect(computeCost(short, p)).toBeCloseTo(0.01 + 0.5, 8);
    const long = { ...emptyUsage(), input_tokens: 50_000, cache_read_input_tokens: 40_000, cache_creation_input_tokens: 20_000, output_tokens: 1_000_000 };
    expect(computeCost(long, p)).toBeCloseTo(0.025 + 0.002 + 0.0125 + 2.5, 8);
    expect(computeCost(short, null)).toBe(0);
  });
});

describe("spend.partial", () => {
  it("is false for requests with a known price; earlier unpriced requests stay uncounted and keep it true", async () => {
    await provider("part", { presetId: "anthropic" });
    await model("part-opus", "part", "claude-opus-5-5");
    // Recorded before the price was known.
    up.fallback = () => ({ sse: anthropicThinkingText });
    await stream(admin.accessToken, "part-opus");
    let l = await of(admin.accessToken, "part");
    expect(l.spend).toMatchObject({ spent: 0, partial: true });

    await fillPresetPrices("part");
    await stream(admin.accessToken, "part-opus");
    l = await of(admin.accessToken, "part");
    // The new request counts; the old one still has no cost, so the sum stays a lower bound and says so.
    expect(l.spend!.spent).toBeGreaterThan(0);
    expect(l.spend!.partial).toBe(true);

    // Without that earlier record (a new month), a known price means an exact sum.
    await getDb().delete(schema.usageRecord).where(eq(schema.usageRecord.priced, false));
    l = await of(admin.accessToken, "part");
    expect(l.spend!.spent).toBeGreaterThan(0);
    expect(l.spend!.partial).toBe(false);
  });
});

describe("estimated balance", () => {
  const entered = { amount: 100, since: "2026-11-01T00:00:00.000Z" };
  const now = new Date("2026-11-06T00:00:00Z");

  it("is the entered amount minus the priced spend, below zero when more was spent", () => {
    expect(estimatedBalance(entered, 0.01, now)).toMatchObject({ currency: "USD", balance: 99.99, used: 0.01, total: 100, estimated: true, estimated_since: entered.since });
    expect(estimatedBalance(entered, 100, now).balance).toBe(0);
    expect(estimatedBalance(entered, 101.5, now).balance).toBe(-1.5);
    expect(estimatedBalance({ ...entered, amount: 0 }, 0, now)).toMatchObject({ balance: 0, total: 0 });
  });

  it("counts from the entered date, resets on a new entry (a top-up), and clears", async () => {
    const a = limits(admin.accessToken);
    const day = (d: number) => new Date(Date.UTC(2026, 9, d));
    await record(userId, "cust", 5, day(1));
    await record(otherId, "cust", 2, day(10));
    await record(userId, "cust", 1, new Date());
    await record(userId, "cust", 9, new Date(), false);

    const set = await a.setProviderBalanceEstimate({ providerId: "cust", amount: 100, asOf: day(5).toISOString() });
    // Everyone's requests on a global provider since the date: 2 + 1; an unpriced record carries 0 (its 9 is not priced spend).
    expect(set.limits!.balance).toMatchObject({ balance: 88, used: 12, total: 100, estimated: true, label: "estimate", estimatedSince: day(5).toISOString(), currency: "USD" });

    await getDb().delete(schema.usageRecord).where(eq(schema.usageRecord.priced, false));
    expect((await of(admin.accessToken, "cust")).balance).toMatchObject({ balance: 97, used: 3 });

    // Without a date: from now, so what came before is out.
    const topUp = await a.setProviderBalanceEstimate({ providerId: "cust", amount: 50 });
    expect(topUp.limits!.balance).toMatchObject({ balance: 50, used: 0, total: 50 });
    await record(userId, "cust", 0.25, new Date(Date.now() + 1000));
    expect((await of(admin.accessToken, "cust")).balance).toMatchObject({ balance: 49.75, used: 0.25 });

    const zero = await a.setProviderBalanceEstimate({ providerId: "cust", amount: 0, asOf: day(5).toISOString() });
    expect(zero.limits!.balance).toMatchObject({ balance: -3.25, total: 0 });

    expect((await a.setProviderBalanceEstimate({ providerId: "cust" })).limits!.balance).toBeUndefined();
  });

  it("is refused for bad input, on a provider that reports its own balance, and on a plan", async () => {
    const a = limits(admin.accessToken);
    expect(await code(a.setProviderBalanceEstimate({ providerId: "cust", amount: -1 }))).toBe(Code.InvalidArgument);
    expect(await code(a.setProviderBalanceEstimate({ providerId: "cust", amount: 1, asOf: "soon" }))).toBe(Code.InvalidArgument);
    expect(await code(a.setProviderBalanceEstimate({ providerId: "cust", amount: 1, asOf: new Date(Date.now() + 86_400_000).toISOString() }))).toBe(Code.InvalidArgument);
    expect(await code(a.setProviderBalanceEstimate({ providerId: "router", amount: 1 }))).toBe(Code.InvalidArgument);
    expect(await code(a.setProviderBalanceEstimate({ providerId: "planp", amount: 1 }))).toBe(Code.InvalidArgument);
    // The server says where the field goes: only for who may change it, never where the provider can tell.
    expect((await of(admin.accessToken, "cust")).balanceEntry).toBe(true);
    expect((await of(admin.accessToken, "anth")).balanceEntry).toBe(true);
    expect((await of(admin.accessToken, "router")).balanceEntry).toBe(false);
    expect((await of(admin.accessToken, "planp")).balanceEntry).toBe(false);
    expect((await of(user.accessToken, "cust")).balanceEntry).toBe(false);
    expect((await of(user.accessToken, "u-mine000002")).balanceEntry).toBe(true);
  });

  it("follows the balance policy on a global provider, for setting and for seeing", async () => {
    expect(await code(limits(user.accessToken).setProviderBalanceEstimate({ providerId: "cust", amount: 1 }))).toBe(Code.PermissionDenied);
    await limits(admin.accessToken).setProviderBalanceEstimate({ providerId: "cust", amount: 10 });
    expect((await of(user.accessToken, "cust")).balance?.estimated).toBe(true);
    const adminApi = ts.as(admin.accessToken).admin;
    const policy = (audience: LimitsAudience) => adminApi.setProviderLimitsPolicy({ policy: { providerId: "cust", balance: { audience, userIds: audience === LimitsAudience.SELECTED_USERS ? [otherId] : [] }, limits: { audience: LimitsAudience.EVERYONE, userIds: [] } } });
    await policy(LimitsAudience.ADMINS_ONLY);
    expect((await of(user.accessToken, "cust")).balance).toBeUndefined();
    expect((await of(user.accessToken, "cust")).spend).toBeDefined();
    expect((await of(admin.accessToken, "cust")).balance?.estimated).toBe(true);
    await policy(LimitsAudience.SELECTED_USERS);
    expect((await of(user.accessToken, "cust")).balance).toBeUndefined();
    expect((await of(other.accessToken, "cust")).balance?.estimated).toBe(true);
  });

  it("stays with its owner on a personal provider, and counts the owner's records", async () => {
    const earlier = new Date(Date.now() - 7_200_000);
    await record(userId, "u-mine000002", 2, earlier);
    expect(await code(limits(other.accessToken).setProviderBalanceEstimate({ providerId: "u-mine000002", amount: 1 }))).toBe(Code.NotFound);
    expect(await code(limits(admin.accessToken).setProviderBalanceEstimate({ providerId: "u-mine000002", amount: 1 }))).toBe(Code.NotFound);
    const set = await limits(user.accessToken).setProviderBalanceEstimate({ providerId: "u-mine000002", amount: 10, asOf: new Date(Date.now() - 3_600_000).toISOString() });
    expect(set.limits!.balance).toMatchObject({ balance: 10, estimated: true });
    await record(userId, "u-mine000002", 2, new Date(Date.now() - 60_000));
    await record(otherId, "u-mine000002", 40, new Date(Date.now() - 60_000));
    expect((await of(user.accessToken, "u-mine000002")).balance).toMatchObject({ balance: 8, used: 2 });
    for (const t of [admin.accessToken, other.accessToken]) {
      expect((await limits(t).getProviderLimits({})).providers.some((p) => p.providerId === "u-mine000002")).toBe(false);
    }
  });

  it("goes with its provider", async () => {
    await provider("gone");
    await limits(admin.accessToken).setProviderBalanceEstimate({ providerId: "gone", amount: 5 });
    await ts.as(admin.accessToken).providers.deleteProvider({ id: "gone" });
    const stored = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, "provider_balance_estimates") });
    expect(stored?.value).toEqual({});
  });
});

describe("windows", () => {
  const PLAN = { "anthropic-ratelimit-unified-5h-utilization": "0.63", "anthropic-ratelimit-unified-5h-reset": "1793750400" };
  const RATE = {
    "anthropic-ratelimit-requests-limit": "50",
    "anthropic-ratelimit-requests-remaining": "1",
    "anthropic-ratelimit-tokens-limit": "1000",
    "anthropic-ratelimit-tokens-remaining": "900",
  };
  const pairs = (w: { label: string; usedPercent: number }[]) => w.map((x) => [x.label, x.usedPercent]);

  it("are a plan's only; rate limits go apart, for the client's usage-limit watch", async () => {
    await provider("win");
    await model("win-m", "win", "claude-up");
    up.fallback = () => ({ sse: anthropicThinkingText, headers: { ...PLAN, ...RATE } });
    await stream(admin.accessToken, "win-m");
    const l = await of(admin.accessToken, "win");
    expect(pairs(l.windows)).toEqual([["5h", 63]]);
    expect(l.windowsSource).toBe("response_headers");
    expect(pairs(l.rateWindows)).toEqual([
      ["requests/min", 98],
      ["tokens/min", 10],
    ]);
  });

  it("are empty on an API key, with nothing dated", async () => {
    await provider("win2");
    await model("win2-m", "win2", "claude-up");
    up.fallback = () => ({ sse: anthropicThinkingText, headers: RATE });
    await stream(admin.accessToken, "win2-m");
    const l = await of(admin.accessToken, "win2");
    expect(l.windows).toEqual([]);
    expect(l.windowsUpdatedAt).toBe("");
    expect(l.windowsSource).toBe("");
    expect(l.rateWindows.length).toBe(2);
  });

  it("hide rate limits under the same policy as the rest", async () => {
    await provider("win3");
    await model("win3-m", "win3", "claude-up");
    up.fallback = () => ({ sse: anthropicThinkingText, headers: { ...PLAN, ...RATE } });
    await stream(admin.accessToken, "win3-m");
    await ts.as(admin.accessToken).admin.setProviderLimitsPolicy({ policy: { providerId: "win3", balance: { audience: LimitsAudience.EVERYONE, userIds: [] }, limits: { audience: LimitsAudience.ADMINS_ONLY, userIds: [] } } });
    const l = await of(user.accessToken, "win3");
    expect(l.windows).toEqual([]);
    expect(l.rateWindows).toEqual([]);
  });
});
