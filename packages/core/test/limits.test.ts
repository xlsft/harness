/**
 * ProviderLimitsService end to end: windows captured from gateway responses, this month's spend and budgets,
 * the per-provider balance / limits visibility policies (AdminService), and personal providers that only their
 * owner ever sees. No real API calls: the upstream is a local mock.
 */
import { Code, ConnectError, createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { inArray } from "drizzle-orm";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { Role } from "../src/gen/harness/llm/v1/content_pb.ts";
import { LimitsAudience, ProviderLimitsService } from "../src/gen/harness/llm/v1/limits_pb.ts";
import { ProviderScope } from "../src/gen/harness/llm/v1/providers_pb.ts";
import { forgetRateLimits } from "../src/limits/headers.ts";
import { anthropicThinkingText } from "./fixtures/upstream.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let up: MockUpstream;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let user: Awaited<ReturnType<typeof deviceLogin>>;
let other: Awaited<ReturnType<typeof deviceLogin>>;
let userId: string;
let otherId: string;

function limitsClient(token: string): Client<typeof ProviderLimitsService> {
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

const PLAN_HEADERS = {
  "anthropic-ratelimit-unified-5h-utilization": "0.63",
  "anthropic-ratelimit-unified-5h-reset": "1793750400",
  "anthropic-ratelimit-unified-7d-utilization": "0.21",
  "anthropic-ratelimit-unified-7d-reset": "1794182400",
};

async function stream(token: string, model: string) {
  const req = { model, maxTokens: 100, messages: [{ role: Role.USER, content: [{ block: { case: "text" as const, value: { text: "hi" } } }] }] };
  for await (const _ of ts.as(token).gateway.streamMessage(req)) {
    // drain
  }
}

async function code(p: Promise<unknown>) {
  try {
    await p;
    return 0;
  } catch (e) {
    return ConnectError.from(e).code;
  }
}

beforeAll(async () => {
  up = await new MockUpstream().start();
  up.fallback = () => ({ sse: anthropicThinkingText, headers: PLAN_HEADERS });
  ts = await startTestServer();
  admin = await deviceLogin(ts);
  user = await createUserAndLogin(ts, admin.cookie, "limits-user@test.local");
  other = await createUserAndLogin(ts, admin.cookie, "limits-other@test.local");
  userId = (await ts.as(user.accessToken).auth.getMe({})).user!.id;
  otherId = (await ts.as(other.accessToken).auth.getMe({})).user!.id;
  const api = (m: string, p: string, b?: unknown) => adminApi(ts.url, admin.accessToken, m, p, b);
  expect((await api("POST", "/api/admin/providers", { id: "acme", type: "anthropic", base_url: up.url, allow_private_network: true, display_name: "Acme" })).status).toBe(201);
  expect((await api("POST", "/api/admin/models", { id: "m1", provider_id: "acme", upstream_model: "claude-up", pricing: { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 } })).status).toBe(201);
  // A personal provider of `user` (inserted directly: personal providers cannot reach the local mock).
  const now = new Date();
  await getDb().insert(schema.provider).values({ id: "u-mine000001", ownerUserId: userId, kind: "llm", type: "anthropic", baseUrl: "https://api.example.com", enabled: true, createdAt: now, updatedAt: now });
});

afterAll(async () => {
  await ts.close();
  await up.stop();
});

beforeEach(async () => {
  await getDb().delete(schema.usageRecord);
  await getDb().delete(schema.setting).where(inArray(schema.setting.key, ["provider_limits_policy", "provider_budgets"]));
  forgetRateLimits();
});

async function record(uid: string, providerId: string, cost: number, at = new Date()) {
  await getDb()
    .insert(schema.usageRecord)
    .values({ id: `usg_${Math.random().toString(36).slice(2)}`, userId: uid, modelId: "m1", providerId, inputTokens: 1, costUsd: cost, priced: true, status: "ok", createdAt: at });
}

describe("GetProviderLimits", () => {
  it("captures plan windows from gateway responses and reports this month's spend", async () => {
    await stream(user.accessToken, "m1");
    await record(otherId, "acme", 1.5);
    await record(otherId, "acme", 100, new Date(Date.UTC(2020, 0, 1)));
    const res = await limitsClient(user.accessToken).getProviderLimits({ modelId: "m1" });
    const acme = res.providers.find((p) => p.providerId === "acme")!;
    expect(acme.providerName).toBe("Acme");
    expect(acme.scope).toBe(ProviderScope.GLOBAL);
    expect(acme.windows.map((w) => [w.label, w.usedPercent])).toEqual([
      ["5h", 63],
      ["weekly", 21],
    ]);
    expect(acme.windows[0]!.resetsAt).toBe(new Date(1793750400_000).toISOString());
    expect(acme.windowsSource).toBe("response_headers");
    expect(acme.spend!.spent).toBeGreaterThan(1.5);
    expect(acme.spend!.spent).toBeLessThan(2);
    expect(acme.balance).toBeUndefined();
    expect(res.model?.providerId).toBe("acme");
    // The user's own personal provider is listed for them.
    expect(res.providers.find((p) => p.providerId === "u-mine000001")?.scope).toBe(ProviderScope.PERSONAL);
    // An unknown model is no error.
    expect((await limitsClient(user.accessToken).getProviderLimits({ modelId: "nope" })).model).toBeUndefined();
  });

  it("never shows a personal provider's limits to anyone but its owner, admins included", async () => {
    await record(userId, "u-mine000001", 4);
    const mine = (await limitsClient(user.accessToken).getProviderLimits({})).providers.find((p) => p.providerId === "u-mine000001")!;
    expect(mine.spend!.spent).toBe(4);
    for (const token of [admin.accessToken, other.accessToken]) {
      const res = await limitsClient(token).getProviderLimits({ providerIds: ["u-mine000001"] });
      expect(res.providers).toEqual([]);
    }
    expect(await code(ts.as(admin.accessToken).admin.setProviderLimitsPolicy({ policy: { providerId: "u-mine000001" } }))).toBe(Code.NotFound);
    const policies = await ts.as(admin.accessToken).admin.listProviderLimitsPolicies({});
    expect(policies.policies.map((p) => p.providerId)).toEqual(["acme"]);
    // Only the owner sets its budget.
    expect(await code(limitsClient(admin.accessToken).setProviderBudget({ providerId: "u-mine000001", monthlyUsd: 5 }))).toBe(Code.NotFound);
    expect((await limitsClient(user.accessToken).setProviderBudget({ providerId: "u-mine000001", monthlyUsd: 5 })).limits!.spend!.budget).toBe(5);
  });

  it("applies the per-provider balance and limits policies server-side; admins always see both", async () => {
    await stream(user.accessToken, "m1");
    const adminApiClient = ts.as(admin.accessToken).admin;
    expect(await code(ts.as(user.accessToken).admin.listProviderLimitsPolicies({}))).toBe(Code.PermissionDenied);
    const set = await adminApiClient.setProviderLimitsPolicy({
      policy: { providerId: "acme", balance: { audience: LimitsAudience.ADMINS_ONLY }, limits: { audience: LimitsAudience.SELECTED_USERS, userIds: [otherId] } },
    });
    expect(set.policy!.limits!.userIds).toEqual([otherId]);
    const seen = async (token: string) => (await limitsClient(token).getProviderLimits({ providerIds: ["acme"] })).providers[0]!;
    const u = await seen(user.accessToken);
    expect(u.spend).toBeUndefined();
    expect(u.windows).toEqual([]);
    const o = await seen(other.accessToken);
    expect(o.spend).toBeDefined();
    expect(o.windows.length).toBe(2);
    const a = await seen(admin.accessToken);
    expect(a.spend).toBeDefined();
    expect(a.windows.length).toBe(2);
    // Back to everyone.
    await adminApiClient.setProviderLimitsPolicy({ policy: { providerId: "acme" } });
    expect((await seen(user.accessToken)).spend).toBeDefined();
    expect((await adminApiClient.listProviderLimitsPolicies({})).policies[0]!.balance!.audience).toBe(LimitsAudience.EVERYONE);
  });

  it("sets budgets: admins for server providers only", async () => {
    expect(await code(limitsClient(user.accessToken).setProviderBudget({ providerId: "acme", monthlyUsd: 25 }))).toBe(Code.PermissionDenied);
    expect(await code(limitsClient(admin.accessToken).setProviderBudget({ providerId: "acme", monthlyUsd: -1 }))).toBe(Code.InvalidArgument);
    const res = await limitsClient(admin.accessToken).setProviderBudget({ providerId: "acme", monthlyUsd: 25 });
    expect(res.limits!.spend!.budget).toBe(25);
    expect((await limitsClient(user.accessToken).getProviderLimits({ providerIds: ["acme"] })).providers[0]!.spend!.budget).toBe(25);
    const cleared = await limitsClient(admin.accessToken).setProviderBudget({ providerId: "acme" });
    expect(cleared.limits!.spend!.budget).toBeUndefined();
  });
});
