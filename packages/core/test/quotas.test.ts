/** Spend limits: per role, per user, per session; enforced before the upstream is called. */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { ErrorInfoSchema, ErrorType } from "../src/gen/harness/common/v1/errors_pb.ts";
import { Role } from "../src/gen/harness/llm/v1/content_pb.ts";
import { periodStarts } from "../src/services/quotas.ts";
import { anthropicThinkingText } from "./fixtures/upstream.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let up: MockUpstream;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let user: Awaited<ReturnType<typeof deviceLogin>>;

// anthropicThinkingText usage: 120 in, 42 out, 1000 cache read, 50 cache write
const COST = (120 * 3 + 42 * 15 + 1000 * 0.3 + 50 * 3.75) / 1e6;

beforeAll(async () => {
  up = await new MockUpstream().start();
  up.fallback = () => ({ sse: anthropicThinkingText });
  ts = await startTestServer();
  admin = await deviceLogin(ts);
  user = await createUserAndLogin(ts, admin.cookie, "quota-user@test.local");
  const api = (m: string, p: string, b?: unknown) => adminApi(ts.url, admin.accessToken, m, p, b);
  expect((await api("POST", "/api/admin/providers", { id: "a", type: "anthropic", base_url: up.url, allow_private_network: true })).status).toBe(201);
  expect(
    (await api("POST", "/api/admin/models", { id: "m", provider_id: "a", upstream_model: "claude-up", pricing: { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 } })).status,
  ).toBe(201);
});

afterAll(async () => {
  await ts.close();
  await up.stop();
});

beforeEach(async () => {
  up.reset();
  expect((await adminApi(ts.url, admin.accessToken, "PUT", "/api/admin/quotas", {})).status).toBe(200);
  delete process.env.HARNESS_SESSION_BUDGET_USD;
});

const api = (m: string, p: string, b?: unknown) => adminApi(ts.url, admin.accessToken, m, p, b);

async function stream(token: string, sessionId = "") {
  const req = {
    model: "m",
    maxTokens: 100,
    messages: [{ role: Role.USER, content: [{ block: { case: "text" as const, value: { text: "hi" } } }] }],
    metadata: { sessionId },
  };
  try {
    for await (const _ of ts.as(token).gateway.streamMessage(req)) {
      /* drain */
    }
    return undefined;
  } catch (e) {
    return e as ConnectError;
  }
}

function expectQuota(err: ConnectError | undefined, msg: RegExp) {
  expect(err).toBeInstanceOf(ConnectError);
  expect(err!.code).toBe(Code.ResourceExhausted);
  const [info] = err!.findDetails(ErrorInfoSchema);
  expect(info?.type).toBe(ErrorType.QUOTA_EXCEEDED);
  expect(info?.retryAfterMs).toBe(0n);
  expect(err!.rawMessage).toMatch(msg);
}

describe("spend limits", () => {
  it("enforces role limits before calling upstream; user limits override the role", async () => {
    expect(await stream(user.accessToken)).toBeUndefined(); // spends COST
    expect(up.requests).toHaveLength(1);

    const put = await api("PUT", "/api/admin/quotas", { roles: { user: { daily_usd: COST / 2 } } });
    expect(put.body.quotas).toEqual({ users: {}, roles: { user: { daily_usd: COST / 2 } } });
    expectQuota(await stream(user.accessToken), /Daily spend limit/);
    expect(up.requests).toHaveLength(1); // upstream not called
    // admins have their own role and are not affected
    expect(await stream(admin.accessToken)).toBeUndefined();

    // a per-user value replaces the role value for that period
    await api("PATCH", "/api/admin/quotas", { users: { [user.user!.id]: { daily_usd: 100 } } });
    expect(await stream(user.accessToken)).toBeUndefined();

    // monthly limit (0 = blocked)
    await api("PATCH", "/api/admin/quotas", { users: { [user.user!.id]: { daily_usd: 100, monthly_usd: 0 } } });
    expectQuota(await stream(user.accessToken), /Monthly spend limit/);

    // removing the user entry falls back to the role limit again
    const patched = await api("PATCH", "/api/admin/quotas", { users: { [user.user!.id]: null } });
    expect(patched.body.quotas.users).toEqual({});
    expectQuota(await stream(user.accessToken), /Daily/);

    const st = await api("GET", `/api/admin/quotas?user_id=${user.user!.id}`);
    expect(st.status).toBe(200);
    expect(st.body.user.daily_usd).toBeCloseTo(COST * 2, 10);
    expect(st.body.user.monthly_usd).toBeCloseTo(COST * 2, 10);
    expect(st.body.user.limits).toEqual({ daily_usd: COST / 2 });
  });

  it("enforces a per-session budget from config, falling back to HARNESS_SESSION_BUDGET_USD", async () => {
    await api("PUT", "/api/admin/quotas", { session_usd: COST / 2 });
    expect(await stream(admin.accessToken, "ses-1")).toBeUndefined();
    expectQuota(await stream(admin.accessToken, "ses-1"), /Session budget/);
    expect(await stream(admin.accessToken, "ses-2")).toBeUndefined();
    // requests without a session id are not session-limited
    expect(await stream(admin.accessToken)).toBeUndefined();

    await api("PUT", "/api/admin/quotas", {});
    expect(await stream(admin.accessToken, "ses-1")).toBeUndefined();
    process.env.HARNESS_SESSION_BUDGET_USD = String(COST);
    expectQuota(await stream(admin.accessToken, "ses-1"), /Session budget/);
  });

  it("validates the config and requires admin", async () => {
    expect((await api("PUT", "/api/admin/quotas", { roles: { user: { daily_usd: -1 } } })).status).toBe(400);
    expect((await api("PUT", "/api/admin/quotas", { roles: { user: { weekly_usd: 1 } } })).status).toBe(400);
    expect((await api("PUT", "/api/admin/quotas", { bogus: 1 })).status).toBe(400);
    expect((await api("GET", "/api/admin/quotas?user_id=usr_missing")).status).toBe(404);
    expect((await adminApi(ts.url, user.accessToken, "GET", "/api/admin/quotas")).status).toBe(403);
  });

  it("uses UTC calendar periods", () => {
    const { day, month } = periodStarts(new Date("2026-03-15T23:30:00-05:00"));
    expect(day.toISOString()).toBe("2026-03-16T00:00:00.000Z");
    expect(month.toISOString()).toBe("2026-03-01T00:00:00.000Z");
  });
});
