/**
 * UsageService (GetMyUsage / GetUsage) and the cost policy: only priced models on metered providers carry a
 * cost; models without a price and plan providers (subscription / coding plan presets) count tokens only.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { Role } from "../src/gen/harness/llm/v1/content_pb.ts";
import { UsageBucket } from "../src/gen/harness/llm/v1/usage_pb.ts";
import { getDb, schema } from "../src/db/index.ts";
import { resolveRange } from "../src/services/usage.ts";
import { anthropicThinkingText } from "./fixtures/upstream.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let up: MockUpstream;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let user: Awaited<ReturnType<typeof deviceLogin>>;

const PRICING = { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 };
// anthropicThinkingText usage: 120 in, 42 out, 1000 cache read, 50 cache write
const COST = (120 * 3 + 42 * 15 + 1000 * 0.3 + 50 * 3.75) / 1e6;
const TOKENS = 120 + 42 + 1000 + 50;

beforeAll(async () => {
  up = await new MockUpstream().start();
  up.fallback = () => ({ sse: anthropicThinkingText });
  ts = await startTestServer();
  admin = await deviceLogin(ts);
  user = await createUserAndLogin(ts, admin.cookie, "usage-user@test.local");
  const api = (m: string, p: string, b?: unknown) => adminApi(ts.url, admin.accessToken, m, p, b);
  // "a": a metered custom provider; "plan": the same upstream, marked as the Z.ai Coding Plan preset.
  for (const id of ["a", "plan"]) {
    expect((await api("POST", "/api/admin/providers", { id, type: "anthropic", base_url: up.url, allow_private_network: true, display_name: id === "a" ? "Acme" : "" })).status).toBe(201);
  }
  await getDb().update(schema.provider).set({ presetId: "zai-coding" }).where(eq(schema.provider.id, "plan"));
  const models = [
    { id: "priced", provider_id: "a", upstream_model: "claude-up", display_name: "Priced Model", pricing: PRICING },
    { id: "free", provider_id: "a", upstream_model: "free-up" },
    { id: "glm", provider_id: "plan", upstream_model: "glm-up", pricing: PRICING },
  ];
  for (const m of models) expect((await api("POST", "/api/admin/models", m)).status).toBe(201);
});

afterAll(async () => {
  await ts.close();
  await up.stop();
});

beforeEach(async () => {
  await getDb().delete(schema.usageRecord);
});

async function stream(token: string, model: string) {
  const req = { model, maxTokens: 100, messages: [{ role: Role.USER, content: [{ block: { case: "text" as const, value: { text: "hi" } } }] }] };
  let cost: number | undefined;
  for await (const ev of ts.as(token).gateway.streamMessage(req)) {
    if (ev.event.case === "messageStop") cost = ev.event.value.costUsd;
  }
  return cost;
}

let seq = 0;
/** Inserts a usage record directly (fixed timestamps, legacy rows). */
async function record(r: { userId: string; modelId: string; providerId: string; at: string; tokens?: number; cost?: number; priced?: boolean | null; requestClass?: string | null }) {
  await getDb()
    .insert(schema.usageRecord)
    .values({
      id: `usg_t${++seq}`,
      userId: r.userId,
      modelId: r.modelId,
      providerId: r.providerId,
      upstreamModel: `${r.modelId}-up`,
      requestClass: r.requestClass === undefined ? "main" : r.requestClass,
      inputTokens: r.tokens ?? 100,
      outputTokens: 10,
      costUsd: r.cost ?? 0,
      priced: r.priced === undefined ? (r.cost ?? 0) > 0 : r.priced,
      status: "ok",
      createdAt: new Date(r.at),
    });
}

const RANGE = { from: "2026-10-01T00:00:00Z", to: "2026-10-31T00:00:00Z" };

describe("cost policy at record time", () => {
  it("bills priced models on metered providers only", async () => {
    expect(await stream(user.accessToken, "priced")).toBeCloseTo(COST, 10);
    expect(await stream(user.accessToken, "free")).toBe(0);
    // a priced model on a plan provider costs nothing
    expect(await stream(user.accessToken, "glm")).toBe(0);
    const rows = await getDb().select().from(schema.usageRecord);
    const by = Object.fromEntries(rows.map((r) => [r.modelId, r]));
    expect(by.priced).toMatchObject({ priced: true });
    expect(by.priced!.costUsd).toBeCloseTo(COST, 10);
    expect(by.free).toMatchObject({ priced: false, costUsd: 0 });
    expect(by.glm).toMatchObject({ priced: false, costUsd: 0 });

    const { report } = await ts.as(user.accessToken).usage.getMyUsage({});
    expect(report!.totals!.requests).toBe(3n);
    expect(report!.totals!.totalTokens).toBe(BigInt(3 * TOKENS));
    expect(report!.totals!.costUsd).toBeCloseTo(COST, 10);
    expect(report!.totals!.unpricedTokens).toBe(BigInt(2 * TOKENS));
    const models = Object.fromEntries(report!.models.map((m) => [m.key, m]));
    expect(models.priced).toMatchObject({ displayName: "Priced Model", providerId: "a", providerName: "Acme", priced: true });
    expect(models.free).toMatchObject({ priced: false, providerName: "Acme" });
    expect(models.glm).toMatchObject({ priced: false, providerName: "Z.ai Coding Plan" });
    expect(models.glm!.totals!.costUsd).toBe(0);
  });

  it("reports provider billing in presets and providers", async () => {
    const { presets } = await ts.as(admin.accessToken).providers.listPresets({});
    const billing = Object.fromEntries(presets.map((p) => [p.id, p.billing]));
    expect(billing).toMatchObject({ "zai-coding": "plan", "kimi-code": "plan", "kimi-code-global": "plan", zai: "metered", openrouter: "metered", brave: "" });
    const { providers } = await ts.as(admin.accessToken).providers.listProviders({});
    expect(Object.fromEntries(providers.map((p) => [p.id, p.billing]))).toMatchObject({ a: "metered", plan: "plan" });
  });
});

describe("GetMyUsage", () => {
  it("aggregates the caller's records by bucket, model and request class", async () => {
    const me = user.user!.id;
    await record({ userId: me, modelId: "priced", providerId: "a", at: "2026-10-02T10:00:00Z", tokens: 1000, cost: 0.5 });
    await record({ userId: me, modelId: "priced", providerId: "a", at: "2026-10-02T23:00:00Z", tokens: 2000, cost: 0.25, requestClass: "subagent" });
    await record({ userId: me, modelId: "free", providerId: "a", at: "2026-10-05T12:00:00Z", tokens: 500, requestClass: null });
    // outside the range, and another user's
    await record({ userId: me, modelId: "priced", providerId: "a", at: "2026-09-30T23:59:59Z", cost: 9 });
    await record({ userId: admin.user!.id, modelId: "priced", providerId: "a", at: "2026-10-02T10:00:00Z", cost: 9 });

    const { report } = await ts.as(user.accessToken).usage.getMyUsage({ range: RANGE });
    const r = report!;
    expect(r.from).toBe("2026-10-01T00:00:00.000Z");
    expect(r.bucket).toBe(UsageBucket.DAY);
    expect(r.timeZone).toBe("UTC");
    expect(r.totals).toMatchObject({ requests: 3n, inputTokens: 3500n, outputTokens: 30n, totalTokens: 3530n, unpricedTokens: 510n });
    expect(r.totals!.costUsd).toBeCloseTo(0.75, 10);
    // every day of the range, empty ones included
    expect(r.buckets).toHaveLength(30);
    expect(r.buckets[0]!.start).toBe("2026-10-01T00:00:00.000Z");
    const oct2 = r.buckets[1]!;
    expect(oct2.start).toBe("2026-10-02T00:00:00.000Z");
    expect(oct2.totals).toMatchObject({ requests: 2n, totalTokens: 3020n });
    expect(oct2.models.map((m) => m.modelKey)).toEqual(["priced"]);
    expect(r.buckets[4]!.models.map((m) => [m.modelKey, m.totals!.totalTokens])).toEqual([["free", 510n]]);
    expect(r.buckets[2]!.totals!.requests).toBe(0n);

    expect(r.models.map((m) => [m.key, m.priced])).toEqual([
      ["priced", true],
      ["free", false],
    ]);
    expect(r.requestClasses.map((c) => [c.requestClass, c.totals!.totalTokens])).toEqual([
      ["subagent", 2010n],
      ["main", 1010n],
      ["other", 510n],
    ]);
  });

  it("buckets by hour for short ranges and in the requested time zone", async () => {
    const me = user.user!.id;
    await record({ userId: me, modelId: "free", providerId: "a", at: "2026-10-02T10:30:00Z" });
    const hourly = (await ts.as(user.accessToken).usage.getMyUsage({ range: { from: "2026-10-02T00:00:00Z", to: "2026-10-03T00:00:00Z" } })).report!;
    expect(hourly.bucket).toBe(UsageBucket.HOUR);
    expect(hourly.buckets).toHaveLength(24);
    expect(hourly.buckets[10]!.totals!.requests).toBe(1n);

    // 23:00 UTC on Oct 2 is Oct 3 in Berlin (UTC+2): local midnights are 22:00 UTC
    await record({ userId: me, modelId: "free", providerId: "a", at: "2026-10-02T23:00:00Z" });
    const berlin = (await ts.as(user.accessToken).usage.getMyUsage({ range: { ...RANGE, timeZone: "Europe/Berlin" } })).report!;
    expect(berlin.buckets[0]!.start).toBe("2026-09-30T22:00:00.000Z");
    const oct3 = berlin.buckets.find((b) => b.start === "2026-10-02T22:00:00.000Z")!;
    expect(oct3.totals!.requests).toBe(1n);
    // the DST change (Oct 25) keeps buckets at local midnight
    expect(berlin.buckets.at(-1)!.start).toBe("2026-10-30T23:00:00.000Z");
  });

  it("counts legacy records by their cost, never for plan providers", async () => {
    const me = user.user!.id;
    await record({ userId: me, modelId: "priced", providerId: "a", at: "2026-10-03T00:00:00Z", cost: 0.1, priced: null });
    await record({ userId: me, modelId: "free", providerId: "a", at: "2026-10-03T00:00:00Z", cost: 0, priced: null });
    await record({ userId: me, modelId: "glm", providerId: "plan", at: "2026-10-03T00:00:00Z", cost: 0.2, priced: null });
    const r = (await ts.as(user.accessToken).usage.getMyUsage({ range: RANGE })).report!;
    expect(r.totals!.costUsd).toBeCloseTo(0.1, 10);
    expect(r.totals!.unpricedTokens).toBe(220n);
    expect(Object.fromEntries(r.models.map((m) => [m.key, m.priced]))).toEqual({ priced: true, free: false, glm: false });
  });

  it("validates the range", async () => {
    const call = (range: object) => ts.as(user.accessToken).usage.getMyUsage({ range });
    for (const range of [
      { from: "nope" },
      { from: "2026-10-02T00:00:00Z", to: "2026-10-01T00:00:00Z" },
      { from: "2025-01-01T00:00:00Z", to: "2026-10-01T00:00:00Z" },
      { ...RANGE, bucket: UsageBucket.HOUR },
      { ...RANGE, timeZone: "Mars/Olympus" },
    ]) {
      await expect(call(range)).rejects.toMatchObject({ code: Code.InvalidArgument });
    }
    const def = resolveRange({}, new Date("2026-11-03T12:00:00Z"));
    expect(def).toMatchObject({ bucket: "day", time_zone: "UTC" });
    expect(def.from.toISOString()).toBe("2026-10-04T12:00:00.000Z");
  });
});

describe("GetUsage", () => {
  it("is admin only", async () => {
    const err = await ts
      .as(user.accessToken)
      .usage.getUsage({})
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ConnectError);
    expect((err as ConnectError).code).toBe(Code.PermissionDenied);
  });

  it("reports everyone, per user, or one user", async () => {
    const me = user.user!.id;
    const adminId = admin.user!.id;
    await record({ userId: me, modelId: "priced", providerId: "a", at: "2026-10-02T10:00:00Z", tokens: 100, cost: 0.1 });
    await record({ userId: adminId, modelId: "priced", providerId: "a", at: "2026-10-03T10:00:00Z", tokens: 5000, cost: 0.2 });
    await record({ userId: adminId, modelId: "glm", providerId: "plan", at: "2026-10-04T10:00:00Z", tokens: 1000 });

    const all = await ts.as(admin.accessToken).usage.getUsage({ range: RANGE });
    expect(all.report!.totals!.requests).toBe(3n);
    expect(all.report!.totals!.costUsd).toBeCloseTo(0.3, 10);
    expect(all.users.map((u) => [u.userId, u.totals!.totalTokens])).toEqual([
      [adminId, 6020n],
      [me, 110n],
    ]);
    expect(all.users[0]).toMatchObject({ email: "admin@test.local", lastActiveAt: "2026-10-04T10:00:00.000Z" });
    expect(all.users[0]!.totals!.unpricedTokens).toBe(1010n);
    expect(all.users[1]).toMatchObject({ email: "usage-user@test.local", name: "usage-user" });

    const one = await ts.as(admin.accessToken).usage.getUsage({ range: RANGE, userId: me });
    expect(one.report!.totals!.requests).toBe(1n);
    expect(one.users.map((u) => u.userId)).toEqual([me]);
  });
});
