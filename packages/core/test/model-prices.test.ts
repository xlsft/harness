/**
 * Prices that stay current: the catalog document, matching, the order of authority, the guard against wild changes, the
 * snapshot across failures and restarts. Never the network: the catalog and the upstreams are a local mock.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { eq, inArray } from "drizzle-orm";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { closeDb, ensureDb, getDb, schema } from "../src/db/index.ts";
import { resetConfig } from "../src/env.ts";
import { computeCost, emptyUsage } from "../src/gateway/cost.ts";
import { Role } from "../src/gen/harness/llm/v1/content_pb.ts";
import { xaiListedPrices } from "../src/services/discovery.ts";
import { applyModelPrices, changeIsHeld, decidePrice, refreshIfStale, samePrice } from "../src/services/model-prices.ts";
import type { ModelPricing } from "../src/services/models.ts";
import { matchCatalogModel, parseCatalog, parseCost } from "../src/services/price-catalog.ts";
import { PRESETS, PRICE_CATALOG_PROVIDERS } from "../src/services/presets.ts";
import { anthropicThinkingText } from "./fixtures/upstream.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const OPUS = { input: 4, output: 20, cache_read: 0.2, cache_write: 5 };

let ts: TestServer;
let up: MockUpstream;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let user: Awaited<ReturnType<typeof deviceLogin>>;
let userId: string;
let seq = 0;

/** A catalog document with these Anthropic models. */
const doc = (models: Record<string, unknown>, more: Record<string, unknown> = {}) => ({ anthropic: { id: "anthropic", models: Object.fromEntries(Object.entries(models).map(([id, cost]) => [id, { id, cost }])) }, ...more });

const catalogServes = (json: unknown, status = 200) => {
  up.fallback = () => ({ status, json });
};

async function provider(id: string, f: { owner?: string; presetId?: string; baseUrl?: string; type?: string } = {}) {
  const now = new Date();
  await getDb()
    .insert(schema.provider)
    .values({ id, ownerUserId: f.owner ?? null, kind: "llm", type: f.type ?? "anthropic", presetId: f.presetId ?? null, baseUrl: f.baseUrl ?? up.url, allowPrivateNetwork: !f.baseUrl, enabled: true, createdAt: now, updatedAt: now });
}

/** A model already in the catalog, as it stands in a database: a price, a source, or neither. */
async function model(providerId: string, upstreamModel: string, f: { pricing?: ModelPricing; source?: string; byHand?: boolean } = {}) {
  const id = `m${++seq}`;
  const now = new Date();
  await getDb()
    .insert(schema.model)
    .values({ id, providerId, upstreamModel, displayName: id, contextWindow: 1000, maxOutputTokens: 100, capabilities: f.byHand ? { set_by_hand: ["pricing"] } : {}, pricing: f.pricing ?? null, pricingSource: f.source ?? null, fallbacks: [], enabled: true, sortOrder: 0, createdAt: now, updatedAt: now });
  return id;
}

const row = async (id: string) => (await getDb().query.model.findFirst({ where: eq(schema.model.id, id) }))!;

const refresh = () => ts.as(admin.accessToken).providers.refreshModelPrices({});

async function setCatalog(json: unknown) {
  catalogServes(json);
  const r = await refresh();
  expect(r.ok).toBe(true);
  return r;
}

const code = async (p: Promise<unknown>) => {
  try {
    await p;
    return 0;
  } catch (e) {
    return ConnectError.from(e).code;
  }
};

beforeAll(async () => {
  up = await new MockUpstream().start();
  // Persisted, so that a test can close the database and open it again.
  ts = await startTestServer({ DATABASE_URL: "", HARNESS_MODEL_PRICES: "on", HARNESS_MODEL_PRICES_URL: `${up.url}/api.json` });
  admin = await deviceLogin(ts);
  user = await createUserAndLogin(ts, admin.cookie, "catalog-user@test.local");
  userId = (await ts.as(user.accessToken).auth.getMe({})).user!.id;
});

afterAll(async () => {
  await ts.close();
  await up.stop();
});

beforeEach(async () => {
  up.reset();
  catalogServes({}, 500);
  process.env.HARNESS_MODEL_PRICES = "on";
  resetConfig();
  await getDb().delete(schema.model);
  await getDb().delete(schema.provider);
  await getDb().delete(schema.setting).where(inArray(schema.setting.key, ["model_price_catalog"]));
});

describe("the document", () => {
  it("gives a model's prices, 0 cache prices saying nothing", () => {
    expect(parseCost({ input: 1.4, output: 4.4, cache_read: 0.26, cache_write: 0 })).toEqual({ pricing: { input: 1.4, output: 4.4, cache_read: 0.26 } });
    expect(parseCost({ input: 1, output: 2, reasoning: 2, input_audio: 9 })).toEqual({ pricing: { input: 1, output: 2 } });
  });

  it("takes a tier by context as long_context", () => {
    const tier = { input: 0.5, output: 2.5, cache_read: 0.05, cache_write: 0.625, tier: { type: "context", size: 100_000 } };
    expect(parseCost({ input: 0.1, output: 0.5, tiers: [tier], context_over_200k: { input: 9, output: 9 } })).toEqual({
      pricing: { input: 0.1, output: 0.5, long_context: { above_tokens: 100_000, input: 0.5, output: 2.5, cache_read: 0.05, cache_write: 0.625 } },
    });
  });

  it("leaves out what is no price or cannot be billed right", () => {
    const tier = (type: string, size: unknown = 1000) => ({ input: 2, output: 3, tier: { type, size } });
    const cases: [unknown, string][] = [
      [undefined, "no cost"],
      [{ input: 0, output: 0 }, "no price"],
      [{ input: 0.1, output: 0 }, "no price"],
      [{ output: 1 }, "no price"],
      [{ input: -1, output: 1 }, "no price"],
      [{ input: "1", output: 1 }, "no price"],
      [{ input: 1, output: 1, cache_read: -1 }, "no price"],
      [{ input: 1, output: 2, reasoning: 9 }, "reasoning priced apart from output"],
      [{ input: 1, output: 2, tiers: [tier("time_of_day")] }, "tier type time_of_day"],
      [{ input: 1, output: 2, tiers: [tier("context"), tier("context", 2000)] }, "2 tiers"],
      [{ input: 1, output: 2, tiers: [tier("context", 0)] }, "malformed tier"],
      [{ input: 1, output: 2, tiers: [{ input: 0, output: 3, tier: { type: "context", size: 1 } }] }, "malformed tier"],
      [{ input: 1, output: 2, tiers: "many" }, "tiers is not a list"],
      [{ input: 1, output: 2, context_over_200k: { input: 2, output: 3 } }, "context_over_200k without tiers"],
    ];
    for (const [cost, reason] of cases) expect(parseCost(cost), JSON.stringify(cost)).toEqual({ reason });
  });

  it("keeps only the mapped providers, and a model without a price as known and empty", () => {
    const { prices, left_out } = parseCatalog({ ...doc({ a: { input: 1, output: 2 }, b: { input: 0, output: 0 } }), otherlab: { models: { x: { cost: { input: 1, output: 1 } } } } });
    expect(prices).toEqual({ anthropic: { a: { input: 1, output: 2 }, b: null } });
    expect(left_out).toEqual([{ provider: "anthropic", model: "b", reason: "no price" }]);
  });

  it("refuses a document of the wrong shape", () => {
    for (const bad of [null, [], "x", 3, {}, { anthropic: {} }, { anthropic: { models: [] } }, { error: "nope" }, doc({ a: {} }), doc({ a: { input: 0, output: 0 } })]) {
      expect(() => parseCatalog(bad), JSON.stringify(bad)).toThrow();
    }
  });
});

describe("matching", () => {
  const a = { input: 1, output: 2 };
  const b = { input: 3, output: 4 };

  it("is by id exactly first", () => {
    expect(matchCatalogModel({ "m-1": a, "m-1-20250101": b }, "m-1-20250101")).toEqual(b);
    expect(matchCatalogModel({ "m-1": a, "m-1-20250101": b }, "m-1")).toEqual(a);
    expect(matchCatalogModel({ "m-1": a }, "m-2")).toBeUndefined();
    expect(matchCatalogModel({ toString: a }, "constructor")).toBeUndefined();
  });

  it("then takes a dated suffix off, either side, in both forms, only when one model is left", () => {
    expect(matchCatalogModel({ "m-1": a }, "m-1-20250929")).toEqual(a);
    expect(matchCatalogModel({ "m-1": a }, "m-1-2025-09-29")).toEqual(a);
    expect(matchCatalogModel({ "m-1-20250929": a }, "m-1")).toEqual(a);
    expect(matchCatalogModel({ "m-1-20250101": a }, "m-1-20250929")).toEqual(a);
    expect(matchCatalogModel({ "m-1": a, "m-1-20250101": b }, "m-1-20250929")).toBeUndefined();
    expect(matchCatalogModel({ "m-1-20250101": a, "m-1-2025-02-02": b }, "m-1")).toBeUndefined();
  });

  it("does not take what only looks like a date", () => {
    expect(matchCatalogModel({ "grok-4.20": a }, "grok-4.20-0309")).toBeUndefined();
    expect(matchCatalogModel({ "m-1": a }, "m-1-20251399")).toBeUndefined();
    expect(matchCatalogModel({ "m-1": a }, "m-1-10-2025")).toBeUndefined();
  });

  it("does not use a sibling for a model the catalog knows and has no price for", () => {
    expect(matchCatalogModel({ "m-1": null, "m-1-20250101": a }, "m-1")).toBeUndefined();
    expect(matchCatalogModel({ "m-1-20250101": null }, "m-1")).toBeUndefined();
  });

  it("has a table naming presets that exist, and no plan or aggregator", () => {
    for (const id of Object.keys(PRICE_CATALOG_PROVIDERS)) {
      const preset = PRESETS.find((p) => p.id === id);
      expect(preset?.group).toBe("llm");
      expect(preset?.billing ?? "metered").toBe("metered");
    }
    expect(PRICE_CATALOG_PROVIDERS).not.toHaveProperty("openrouter");
  });
});

describe("deciding", () => {
  const now = new Date("2026-11-06T00:00:00Z");
  const p = (input: number, output = input * 5): ModelPricing => ({ input, output });
  const state = (pricing: ModelPricing | null, source: "preset" | "catalog" | "list" | null = null) => ({ pricing, source, held: null });

  it("goes list, catalog, preset, and no lower source over a higher one", () => {
    const offers = { list: p(1), catalog: p(2), preset: p(3) };
    expect(decidePrice(state(null), offers, now)).toMatchObject({ pricing: p(1), source: "list" });
    expect(decidePrice(state(null), { catalog: p(2), preset: p(3) }, now)).toMatchObject({ pricing: p(2), source: "catalog" });
    expect(decidePrice(state(null), { preset: p(3) }, now)).toMatchObject({ pricing: p(3), source: "preset" });
    for (const [have, offer] of [
      ["list", { catalog: p(2) }],
      ["list", { preset: p(2) }],
      ["catalog", { preset: p(2) }],
    ] as const) {
      const s = state(p(1), have);
      expect(decidePrice(s, offer, now)).toBe(s);
    }
    expect(decidePrice(state(p(1), "catalog"), { list: p(2) }, now)).toMatchObject({ pricing: p(2), source: "list" });
    expect(decidePrice(state(p(1), "preset"), { catalog: p(2) }, now)).toMatchObject({ pricing: p(2), source: "catalog" });
    expect(decidePrice(state(p(1), "catalog"), { catalog: p(2) }, now)).toMatchObject({ pricing: p(2), source: "catalog" });
  });

  it("changes nothing with nothing to say, and only the source for the same number", () => {
    const s = state(p(1), "catalog");
    expect(decidePrice(s, {}, now)).toBe(s);
    expect(decidePrice(s, { catalog: p(1) }, now)).toBe(s);
    expect(decidePrice(state(p(1), "preset"), { catalog: { output: 5, input: 1 } }, now)).toEqual({ pricing: p(1), source: "catalog", held: null });
  });

  it("reads a price from before sources: the preset's number as the preset's, any other as a list's", () => {
    expect(decidePrice(state(p(1)), { catalog: p(2), preset: p(1) }, now)).toMatchObject({ pricing: p(2), source: "catalog" });
    const unknown = state(p(1));
    expect(decidePrice(unknown, { catalog: p(2), preset: p(3) }, now)).toBe(unknown);
    expect(decidePrice(unknown, { list: p(2) }, now)).toMatchObject({ pricing: p(2), source: "list" });
  });

  it("holds a change past 5x either way, or to and from zero, with exactly 5x still applying", () => {
    expect(changeIsHeld(p(1), p(5))).toBe(false);
    expect(changeIsHeld(p(5), p(1))).toBe(false);
    expect(changeIsHeld(p(1), p(5.01))).toBe(true);
    expect(changeIsHeld(p(5.01), p(1))).toBe(true);
    expect(changeIsHeld({ input: 1, output: 1 }, { input: 1, output: 6 })).toBe(true);
    expect(changeIsHeld({ input: 1, output: 1 }, { input: 0, output: 1 })).toBe(true);
    expect(changeIsHeld({ input: 0, output: 0 }, { input: 1, output: 1 })).toBe(true);
    expect(changeIsHeld({ input: 1, output: 1, cache_read: 0.1 }, { input: 1, output: 1, cache_read: 0.9 })).toBe(true);
    // A cache price that appears only says what used to follow input.
    expect(changeIsHeld({ input: 1, output: 1 }, { input: 1, output: 1, cache_read: 0.01 })).toBe(false);
    const long = (i: number) => ({ input: 1, output: 1, long_context: { above_tokens: 9, input: i, output: 1 } });
    expect(changeIsHeld(long(1), long(9))).toBe(true);
    expect(changeIsHeld({ input: 1, output: 1 }, long(9))).toBe(false);
  });

  it("guards list and catalog, not a preset, and no first price", () => {
    expect(decidePrice(state(p(1), "catalog"), { catalog: p(100) }, now)).toEqual({ pricing: p(1), source: "catalog", held: { proposed: p(100), source: "catalog", held_at: now.toISOString() } });
    expect(decidePrice(state(p(1), "list"), { list: { input: 0, output: 0 } }, now).held?.source).toBe("list");
    expect(decidePrice(state(p(1), "preset"), { preset: p(100) }, now)).toMatchObject({ pricing: p(100), held: null });
    expect(decidePrice(state(null), { catalog: p(100) }, now)).toMatchObject({ pricing: p(100), held: null });
    expect(decidePrice(state(null), { list: { input: 0, output: 0 } }, now)).toMatchObject({ pricing: { input: 0, output: 0 }, held: null });
  });

  it("keeps a hold while the proposal stands, and drops it when a value is fine again", () => {
    const held = decidePrice(state(p(1), "catalog"), { catalog: p(100) }, now);
    expect(decidePrice(held, { catalog: p(100) }, new Date())).toBe(held);
    expect(decidePrice(held, { catalog: p(200) }, now).held?.proposed).toEqual(p(200));
    expect(decidePrice(held, { catalog: p(2) }, now)).toEqual({ pricing: p(2), source: "catalog", held: null });
    expect(decidePrice(held, { catalog: p(1) }, now)).toEqual({ pricing: p(1), source: "catalog", held: null });
  });

  it("compares prices by what they say", () => {
    expect(samePrice({ input: 1, output: 2, cache_read: 0 }, { output: 2, input: 1 })).toBe(true);
    expect(samePrice({ input: 1, output: 2 }, { input: 1, output: 2, long_context: { above_tokens: 1, input: 1, output: 1 } })).toBe(false);
    expect(samePrice(null, undefined)).toBe(true);
  });
});

describe("the refresh", () => {
  it("updates a preset's price, keeps tiers, and says where it came from", async () => {
    await provider("anth", { presetId: "anthropic" });
    const added = (await ts.as(admin.accessToken).providers.addModel({ providerId: "anth", upstreamModel: "claude-opus-5-5", id: "op" })).model!;
    expect(added).toMatchObject({ pricingSource: "preset", pricing: { input: 4 } });
    expect(added.pricingUpdatedAt).not.toBe("");

    const r = await setCatalog(doc({ "claude-opus-5-5": { input: 5, output: 25, tiers: [{ input: 10, output: 50, tier: { type: "context", size: 200_000 } }] } }));
    expect(r).toMatchObject({ ok: true, updated: 1, held: 0, priceCatalog: { enabled: true } });
    expect(r.priceCatalog!.updatedAt).not.toBe("");
    expect(up.requests.filter((q) => q.url === "/api.json").length).toBe(1);
    expect(await row("op")).toMatchObject({ pricing: { input: 5, output: 25, long_context: { above_tokens: 200_000, input: 10 } }, pricingSource: "catalog" });

    const { providers, priceCatalog } = await ts.as(admin.accessToken).providers.listProviders({});
    expect(priceCatalog!.updatedAt).toBe(r.priceCatalog!.updatedAt);
    expect(providers.find((p) => p.id === "anth")!.models[0]).toMatchObject({ pricingSource: "catalog", pricing: { input: 5, output: 25 } });
    // Clients see it through the normal list.
    const listed = await ts.as(admin.accessToken).gateway.listModels({});
    expect(listed.models.find((m) => m.id === "op")!.pricing).toMatchObject({ input: 5, output: 25 });
  });

  it("gives a new model the catalog's price before the preset's, and matches by a dated id", async () => {
    await setCatalog(doc({ "claude-opus-5-5": { input: 5, output: 25 }, "claude-new-1": { input: 1, output: 2 } }));
    await provider("anth", { presetId: "anthropic" });
    const a = ts.as(admin.accessToken).providers;
    expect((await a.addModel({ providerId: "anth", upstreamModel: "claude-opus-5-5", id: "op" })).model).toMatchObject({ pricingSource: "catalog", pricing: { input: 5 } });
    expect((await a.addModel({ providerId: "anth", upstreamModel: "claude-new-1-20261101", id: "nw" })).model).toMatchObject({ pricingSource: "catalog", pricing: { input: 1 } });
    expect((await a.addModel({ providerId: "anth", upstreamModel: "claude-sonnet-5-5", id: "pr" })).model).toMatchObject({ pricingSource: "preset", pricing: { input: 2 } });
    expect((await a.addModel({ providerId: "anth", upstreamModel: "claude-unknown", id: "no" })).model).toMatchObject({ pricingSource: "" });
  });

  it("reaches personal providers from the same snapshot; only an admin may start it", async () => {
    await provider("u-mine000009", { owner: userId, presetId: "anthropic", baseUrl: "https://api.anthropic.com" });
    const own = await model("u-mine000009", "claude-opus-5-5");
    await setCatalog(doc({ "claude-opus-5-5": { input: 5, output: 25 } }));
    expect(await row(own)).toMatchObject({ pricing: { input: 5 }, pricingSource: "catalog" });
    expect(await code(ts.as(user.accessToken).providers.refreshModelPrices({}))).toBe(Code.PermissionDenied);
    // One fetch for everyone, none for reading.
    const fetched = up.requests.length;
    await ts.as(user.accessToken).providers.listProviders({});
    await ts.as(user.accessToken).gateway.listModels({});
    expect(up.requests.length).toBe(fetched);
  });

  it("leaves custom providers, aggregators and plans alone, however the names match", async () => {
    await provider("cust");
    await provider("router", { baseUrl: "https://openrouter.ai/api/v1", type: "openai-chat", presetId: "openrouter" });
    await provider("planp", { presetId: "zai-coding", baseUrl: "https://api.z.ai/api/anthropic" });
    await provider("cn", { presetId: "moonshot-cn", baseUrl: "https://api.moonshot.cn/v1", type: "openai-chat" });
    await provider("ds", { presetId: "deepseek", baseUrl: "https://api.deepseek.com", type: "openai-chat" });
    // Nothing a list would say: this provider is not asked, it has no models with a list price.
    const ids = [await model("cust", "claude-opus-5-5"), await model("planp", "glm-5.3"), await model("cn", "kimi-k3"), await model("ds", "deepseek-flash")];
    const kept = await model("cust", "claude-sonnet-5-5", { pricing: { input: 1, output: 1 } });
    const everything = { input: 7, output: 7 };
    await setCatalog(
      doc({ "claude-opus-5-5": everything, "claude-sonnet-5-5": everything, "glm-5.3": everything, "kimi-k3": everything, "deepseek-flash": everything }, {
        zai: { models: { "glm-5.3": { cost: everything } } },
        "zai-coding-plan": { models: { "glm-5.3": { cost: everything } } },
        "moonshotai-cn": { models: { "kimi-k3": { cost: everything } } },
        deepseek: { models: { "deepseek-flash": { cost: everything } } },
      }),
    );
    for (const id of ids) expect((await row(id)).pricing, id).toBeNull();
    expect((await row(kept)).pricing).toEqual({ input: 1, output: 1 });
  });

  it("leaves a price by hand alone, also none, and a hold never lands on one", async () => {
    await provider("anth", { presetId: "anthropic" });
    const a = ts.as(admin.accessToken).providers;
    await a.addModel({ providerId: "anth", upstreamModel: "claude-opus-5-5", id: "hand" });
    await a.addModel({ providerId: "anth", upstreamModel: "claude-sonnet-5-5", id: "none" });
    const pricing = { input: 3, output: 3, cacheRead: 0, cacheWrite: 0 };
    expect((await a.setModelPricing({ modelId: "hand", pricing })).model).toMatchObject({ pricingSource: "hand", pricingSetByHand: true });
    expect((await a.setModelPricing({ modelId: "none" })).model).toMatchObject({ pricingSource: "hand", pricingSetByHand: true });
    await setCatalog(doc({ "claude-opus-5-5": { input: 900, output: 900 }, "claude-sonnet-5-5": { input: 1, output: 1 } }));
    expect(await row("hand")).toMatchObject({ pricing: { input: 3, output: 3 }, priceChangeHeld: null });
    expect(await row("none")).toMatchObject({ pricing: null, priceChangeHeld: null });

    // A model marked by hand from before the source column, and one created over the admin API.
    const old = await model("anth", "claude-opus-5-5", { pricing: { input: 3, output: 3 }, byHand: true });
    await applyModelPrices();
    expect((await row(old)).pricing).toEqual({ input: 3, output: 3 });
    expect((await a.listProviders({})).providers.find((p) => p.id === "anth")!.models.find((m) => m.id === old)!.pricingSource).toBe("hand");
  });

  it("treats a price given through the admin API as by hand", async () => {
    await provider("anth", { presetId: "anthropic" });
    const res = await fetch(`${ts.url}/api/admin/models`, {
      method: "POST",
      headers: { authorization: `Bearer ${admin.accessToken}`, "content-type": "application/json" },
      body: JSON.stringify({ id: "api", provider_id: "anth", upstream_model: "claude-opus-5-5", pricing: { input: 3, output: 3 } }),
    });
    expect(res.status).toBeLessThan(300);
    await setCatalog(doc({ "claude-opus-5-5": { input: 4, output: 4 } }));
    expect(await row("api")).toMatchObject({ pricing: { input: 3, output: 3 }, pricingSource: "hand" });
  });

  it("holds a catalog change past the guard, with an Accept under SetModelPricing's rules", async () => {
    await provider("anth", { presetId: "anthropic" });
    await provider("u-mine000009", { owner: userId, presetId: "anthropic", baseUrl: "https://api.anthropic.com" });
    const a = ts.as(admin.accessToken).providers;
    const u = ts.as(user.accessToken).providers;
    await a.addModel({ providerId: "anth", upstreamModel: "claude-opus-5-5", id: "op" });
    const own = await model("u-mine000009", "claude-opus-5-5", { pricing: OPUS, source: "preset" });

    expect(await code(a.acceptModelPriceChange({ modelId: "op" }))).toBe(Code.InvalidArgument);

    // Up.
    const r = await setCatalog(doc({ "claude-opus-5-5": { input: 400, output: 2000 } }));
    expect(r.held).toBe(2);
    expect(await row("op")).toMatchObject({ pricing: OPUS, pricingSource: "preset", priceChangeHeld: { proposed: { input: 400, output: 2000 }, source: "catalog" } });
    const shown = (await a.listProviders({})).providers.find((p) => p.id === "anth")!.models[0]!;
    expect(shown).toMatchObject({ pricing: { input: 4 }, priceChangeHeld: { proposed: { input: 400, output: 2000 }, source: "catalog" } });
    expect(shown.priceChangeHeld!.heldAt).not.toBe("");

    // The same proposal again is no new hold.
    expect((await setCatalog(doc({ "claude-opus-5-5": { input: 400, output: 2000 } }))).held).toBe(0);

    // Access: as for SetModelPricing.
    expect(await code(u.acceptModelPriceChange({ modelId: "op" }))).toBe(Code.PermissionDenied);
    expect(await code(a.acceptModelPriceChange({ modelId: own }))).toBe(Code.NotFound);
    expect((await u.acceptModelPriceChange({ modelId: own })).model).toMatchObject({ pricing: { input: 400 }, pricingSource: "catalog" });
    const accepted = (await a.acceptModelPriceChange({ modelId: "op" })).model!;
    expect(accepted).toMatchObject({ pricing: { input: 400, output: 2000 }, pricingSource: "catalog", pricingSetByHand: false });
    expect(accepted.priceChangeHeld).toBeUndefined();

    // Down.
    expect((await setCatalog(doc({ "claude-opus-5-5": { input: 1, output: 5 } }))).held).toBe(2);
    expect(await row("op")).toMatchObject({ pricing: { input: 400 }, priceChangeHeld: { proposed: { input: 1, output: 5 } } });

    // A hand price settles it.
    expect((await a.setModelPricing({ modelId: "op", pricing: { input: 2, output: 2, cacheRead: 0, cacheWrite: 0 } })).model!.priceChangeHeld).toBeUndefined();
    expect((await row("op")).priceChangeHeld).toBeNull();
  });
});

describe("the provider's own list", () => {
  const LIST = (prompt: string, completion: string) => ({ data: [{ id: "listed", pricing: { prompt, completion } }] });

  it("goes over the catalog and updates a price, with the guard both ways and to zero", async () => {
    await provider("anth", { presetId: "anthropic" });
    await setCatalog(doc({ listed: { input: 5, output: 5 } }));
    const a = ts.as(admin.accessToken).providers;
    await a.addModel({ providerId: "anth", upstreamModel: "listed", id: "l" });
    expect(await row("l")).toMatchObject({ pricing: { input: 5 }, pricingSource: "catalog" });

    const discover = (prompt: string, completion: string) => {
      up.enqueue({ json: { ...LIST(prompt, completion), has_more: false } });
      return a.discoverModels({ providerId: "anth" });
    };
    await discover("0.000002", "0.000002");
    expect(await row("l")).toMatchObject({ pricing: { input: 2, output: 2 }, pricingSource: "list" });

    // The catalog no longer goes over it, also when it changes.
    await setCatalog(doc({ listed: { input: 3, output: 3 } }));
    expect((await row("l")).pricing).toEqual({ input: 2, output: 2 });

    await discover("0.001", "0.001");
    expect(await row("l")).toMatchObject({ pricing: { input: 2, output: 2 }, priceChangeHeld: { proposed: { input: 1000, output: 1000 }, source: "list" } });
    await discover("0.0000001", "0.0000001");
    expect((await row("l")).priceChangeHeld).toMatchObject({ proposed: { input: 0.1, output: 0.1 } });
    await discover("0", "0");
    expect(await row("l")).toMatchObject({ pricing: { input: 2, output: 2 }, priceChangeHeld: { proposed: { input: 0, output: 0 }, source: "list" } });
    await discover("0.000003", "0.000003");
    expect(await row("l")).toMatchObject({ pricing: { input: 3, output: 3 }, priceChangeHeld: null, pricingSource: "list" });
  });

  it("prices a model added from it as a list's, and a list that fails or says nothing changes nothing", async () => {
    await provider("cust", { type: "openai-chat" });
    const a = ts.as(admin.accessToken).providers;
    up.enqueue({ json: LIST("0.000001", "0.000002") });
    await a.discoverModels({ providerId: "cust" });
    expect((await a.addModel({ providerId: "cust", upstreamModel: "listed", id: "l" })).model).toMatchObject({ pricingSource: "list", pricing: { input: 1, output: 2 } });
    up.enqueue({ json: { data: [{ id: "listed" }] } });
    await a.discoverModels({ providerId: "cust" });
    expect((await row("l")).pricing).toEqual({ input: 1, output: 2 });
    up.enqueue({ status: 500, json: {} });
    expect(await code(a.discoverModels({ providerId: "cust" }))).not.toBe(0);
    expect((await row("l")).pricing).toEqual({ input: 1, output: 2 });
  });

  it("is asked by the refresh for global providers, never with a personal key", async () => {
    await provider("glob", { type: "openai-chat" });
    await provider("u-mine000009", { owner: userId, type: "openai-chat", baseUrl: "https://example.invalid/v1" });
    const g = await model("glob", "listed", { pricing: { input: 1, output: 1 }, source: "list" });
    const own = await model("u-mine000009", "listed", { pricing: { input: 1, output: 1 }, source: "list" });
    up.fallback = (q) => (q.url === "/api.json" ? { json: doc({ a: { input: 1, output: 1 } }) } : { json: LIST("0.000002", "0.000002") });
    const r = await refresh();
    expect(r).toMatchObject({ ok: true, updated: 1 });
    expect((await row(g)).pricing).toEqual({ input: 2, output: 2 });
    expect((await row(own)).pricing).toEqual({ input: 1, output: 1 });
  });

  it("reads xAI's prices in its documented units, from its own endpoint", async () => {
    // The example of docs.x.ai/developers/rest-api-reference/inference/models.
    const example = { id: "grok-420-reasoning", aliases: ["grok-420"], prompt_text_token_price: 20_000, cached_prompt_text_token_price: 5000, completion_text_token_price: 80_000, long_context_threshold: 128_000, prompt_text_token_price_long_context: 40_000, cached_prompt_text_token_price_long_context: 0, completion_text_token_price_long_context: 160_000 };
    const prices = xaiListedPrices({ models: [example, { id: "grok-free", prompt_text_token_price: 0, completion_text_token_price: 0 }, { id: "flat", prompt_text_token_price: 10_000, completion_text_token_price: 20_000, long_context_threshold: 0 }] });
    const want = { input: 2, output: 8, cache_read: 0.5, long_context: { above_tokens: 127_999, input: 4, output: 16, cache_read: 0.5 } };
    expect(prices.get("grok-420-reasoning")).toEqual(want);
    expect(prices.get("grok-420")).toEqual(want);
    expect(prices.has("grok-free")).toBe(false);
    expect(prices.get("flat")).toEqual({ input: 1, output: 2 });

    // A test key against the mock; without a key it is not asked.
    await provider("xai-k", { presetId: "xai", type: "openai-responses" });
    const a = ts.as(admin.accessToken).providers;
    await a.addModel({ providerId: "xai-k", upstreamModel: "grok-4.7", id: "gk" });
    expect((await row("gk")).pricingSource).toBe("preset");
    await a.discoverModels({ providerId: "xai-k" });
    expect(up.requests.some((q) => q.url.endsWith("/language-models"))).toBe(false);
    await a.updateProvider({ id: "xai-k", apiKey: "test-key" });
    up.fallback = () => ({ json: { models: [{ id: "grok-4.7", prompt_text_token_price: 30_000, completion_text_token_price: 60_000, long_context_threshold: 200_000 }] } });
    await a.discoverModels({ providerId: "xai-k" });
    expect(up.requests.some((q) => q.url.endsWith("/language-models"))).toBe(true);
    expect(await row("gk")).toMatchObject({ pricing: { input: 3, output: 6 }, pricingSource: "list" });
    // Its failing costs nothing.
    up.fallback = () => ({ status: 500, json: {} });
    await a.discoverModels({ providerId: "xai-k" });
    expect((await row("gk")).pricing).toMatchObject({ input: 3 });
  });
});

describe("the snapshot", () => {
  const FIRST = doc({ "claude-opus-5-5": { input: 5, output: 25 } });

  it("survives a failed fetch, a bad status and a bad document; prices stay", async () => {
    await provider("anth", { presetId: "anthropic" });
    const m = await model("anth", "claude-opus-5-5", { pricing: OPUS, source: "preset" });
    const first = await setCatalog(FIRST);
    for (const bad of [() => catalogServes({ error: "nope" }), () => catalogServes({}, 503), () => catalogServes({ anthropic: { models: "gone" } }), () => catalogServes([]), () => { up.fallback = () => ({ sse: "<html>not json" }); }]) {
      bad();
      const r = await refresh();
      expect(r.ok).toBe(false);
      expect(r.message).toContain("keeping the last one");
      expect(r.priceCatalog!.updatedAt).toBe(first.priceCatalog!.updatedAt);
      expect((await row(m)).pricing).toEqual({ input: 5, output: 25 });
    }
    // A new model still gets the last good price.
    catalogServes({}, 500);
    const added = (await ts.as(admin.accessToken).providers.addModel({ providerId: "anth", upstreamModel: "claude-opus-5-5", id: "later" })).model!;
    expect(added).toMatchObject({ pricingSource: "catalog", pricing: { input: 5 } });
  });

  it("keeps a provider the new document lacks", async () => {
    await provider("anth", { presetId: "anthropic" });
    await setCatalog(FIRST);
    await setCatalog({ openai: { models: { "gpt-x": { cost: { input: 1, output: 1 } } } } });
    const m = await model("anth", "claude-opus-5-5");
    await applyModelPrices();
    expect((await row(m)).pricing).toEqual({ input: 5, output: 25 });
  });

  it("with none yet, the preset's prices stay", async () => {
    await provider("anth", { presetId: "anthropic" });
    const m = await model("anth", "claude-opus-5-5");
    catalogServes({}, 500);
    expect((await refresh()).ok).toBe(false);
    expect(await row(m)).toMatchObject({ pricing: OPUS, pricingSource: "preset" });
    expect((await ts.as(admin.accessToken).providers.listProviders({})).priceCatalog).toMatchObject({ enabled: true, updatedAt: "" });
  });

  it("survives a restart, with nothing fetched", async () => {
    await provider("anth", { presetId: "anthropic" });
    const m = await model("anth", "claude-opus-5-5");
    await setCatalog(FIRST);
    const fetched = up.requests.length;
    await closeDb();
    await ensureDb();
    const { bootstrap } = await import("../src/bootstrap.ts");
    await bootstrap();
    const later = await model("anth", "claude-opus-5-5");
    await applyModelPrices();
    expect((await row(m)).pricing).toEqual({ input: 5, output: 25 });
    expect((await row(later)).pricing).toEqual({ input: 5, output: 25 });
    expect(up.requests.length).toBe(fetched);
  });

  it("is fetched again only once it is a day old", async () => {
    await setCatalog(FIRST);
    const count = () => up.requests.filter((q) => q.url === "/api.json").length;
    const fetched = count();
    await refreshIfStale(Date.now() + 3_600_000);
    expect(count()).toBe(fetched);
    await refreshIfStale(Date.now() + 86_400_000 + 1000);
    expect(count()).toBe(fetched + 1);
  });

  it("is off with the switch: nothing fetched, nothing applied, the preset's prices stay", async () => {
    await provider("anth", { presetId: "anthropic" });
    await setCatalog(FIRST);
    process.env.HARNESS_MODEL_PRICES = "off";
    resetConfig();
    const fetched = up.requests.length;
    const r = await refresh();
    expect(r).toMatchObject({ ok: false, priceCatalog: { enabled: false, updatedAt: "" } });
    expect(r.message).toContain("off");
    await refreshIfStale(Date.now() + 2 * 86_400_000);
    expect(up.requests.length).toBe(fetched);
    const m = await model("anth", "claude-opus-5-5");
    await applyModelPrices();
    expect(await row(m)).toMatchObject({ pricing: OPUS, pricingSource: "preset" });
  });
});

describe("spend", () => {
  it("keeps what past records recorded when the price changes; later requests pay the new one", async () => {
    await provider("anth", { presetId: "anthropic" });
    const a = ts.as(admin.accessToken);
    await a.providers.addModel({ providerId: "anth", upstreamModel: "claude-opus-5-5", id: "billed" });
    up.fallback = (q) => (q.url === "/api.json" ? { json: doc({ "claude-opus-5-5": { input: 8, output: 40 } }) } : { sse: anthropicThinkingText });
    const stream = async () => {
      const req = { model: "billed", maxTokens: 100, messages: [{ role: Role.USER, content: [{ block: { case: "text" as const, value: { text: "hi" } } }] }] };
      for await (const _ of a.gateway.streamMessage(req)) {
        // drain
      }
    };
    await stream();
    const [before] = await getDb().select().from(schema.usageRecord).where(eq(schema.usageRecord.modelId, "billed"));
    expect(before!.costUsd).toBeGreaterThan(0);
    expect(before!.priced).toBe(true);
    const usage = { ...emptyUsage(), input_tokens: before!.inputTokens, output_tokens: before!.outputTokens, cache_read_input_tokens: before!.cacheReadTokens, cache_creation_input_tokens: before!.cacheWriteTokens };
    expect(before!.costUsd).toBe(computeCost(usage, OPUS));

    expect((await refresh()).ok).toBe(true);
    await stream();
    const after = await getDb().select().from(schema.usageRecord).where(eq(schema.usageRecord.modelId, "billed"));
    expect(after.find((r) => r.id === before!.id)!.costUsd).toBe(before!.costUsd);
    expect(after.find((r) => r.id !== before!.id)!.costUsd).toBe(computeCost(usage, { input: 8, output: 40 }));
    // The reports add up what the records hold.
    const { report } = await a.usage.getMyUsage({});
    expect(report!.totals!.costUsd).toBeCloseTo(after.reduce((n, r) => n + r.costUsd, 0), 8);
    await getDb().delete(schema.usageRecord);
  });

  it("leaves a plan provider unpriced", async () => {
    await provider("planp", { presetId: "zai-coding", baseUrl: "https://api.z.ai/api/anthropic" });
    const m = await model("planp", "glm-5.3");
    await setCatalog(doc({ "glm-5.3": { input: 1, output: 1 } }, { zai: { models: { "glm-5.3": { cost: { input: 1, output: 1 } } } } }));
    await applyModelPrices();
    expect(await row(m)).toMatchObject({ pricing: null, pricingSource: null });
    expect(await code(ts.as(admin.accessToken).providers.acceptModelPriceChange({ modelId: m }))).toBe(Code.InvalidArgument);
  });
});
