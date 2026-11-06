/**
 * Provider limits building blocks without a server: rate-limit header parsing (limits/headers.ts) and the
 * balance adapters with a mocked fetch (limits/balance.ts). No real API calls.
 */
import { beforeEach, describe, expect, it } from "vitest";
import { BALANCE_TTL_MS, balanceAdapterFor, clearBalanceCache, getBalance, type FetchFn } from "../src/limits/balance.ts";
import { captureRateLimits, forgetRateLimits, parseDurationMs, parseRateLimitHeaders, parseReset, windowSnapshot } from "../src/limits/headers.ts";

const NOW = Date.parse("2026-11-03T12:00:00Z");

describe("rate-limit headers", () => {
  it("parses Go-style durations and reset forms", () => {
    expect(parseDurationMs("6m0s")).toBe(360_000);
    expect(parseDurationMs("20ms")).toBe(20);
    expect(parseDurationMs("1h2m3.5s")).toBe(3_723_500);
    expect(parseDurationMs("1s")).toBe(1000);
    expect(parseDurationMs("soon")).toBeUndefined();
    expect(parseReset("2026-11-03T12:01:00Z", NOW)).toBe("2026-11-03T12:01:00.000Z");
    expect(parseReset("1793750400", NOW)).toBe(new Date(1793750400 * 1000).toISOString());
    expect(parseReset("1793750400000", NOW)).toBe(new Date(1793750400000).toISOString());
    expect(parseReset("30", NOW)).toBe("2026-11-03T12:00:30.000Z");
    expect(parseReset("6m0s", NOW)).toBe("2026-11-03T12:06:00.000Z");
    expect(parseReset("", NOW)).toBe("");
    expect(parseReset("garbage", NOW)).toBe("");
  });

  it("reads Anthropic API key windows", () => {
    const h = new Headers({
      "anthropic-ratelimit-requests-limit": "50",
      "anthropic-ratelimit-requests-remaining": "40",
      "anthropic-ratelimit-requests-reset": "2026-11-03T12:00:30Z",
      "anthropic-ratelimit-tokens-limit": "100000",
      "anthropic-ratelimit-tokens-remaining": "25000",
      "anthropic-ratelimit-tokens-reset": "2026-11-03T12:00:10Z",
      "anthropic-ratelimit-input-tokens-limit": "80000",
      "anthropic-ratelimit-input-tokens-remaining": "80000",
      "anthropic-ratelimit-output-tokens-limit": "20000",
      "anthropic-ratelimit-output-tokens-remaining": "5000",
    });
    const w = parseRateLimitHeaders(h, NOW);
    expect(w).toEqual([
      { label: "requests/min", used_percent: 20, limit: 50, remaining: 40, resets_at: "2026-11-03T12:00:30.000Z" },
      { label: "tokens/min", used_percent: 75, limit: 100000, remaining: 25000, resets_at: "2026-11-03T12:00:10.000Z" },
      { label: "input tokens/min", used_percent: 0, limit: 80000, remaining: 80000, resets_at: "" },
      { label: "output tokens/min", used_percent: 75, limit: 20000, remaining: 5000, resets_at: "" },
    ]);
  });

  it("reads Anthropic unified plan windows (fractions, epoch resets, model-specific weeks)", () => {
    const h = new Headers({
      "anthropic-ratelimit-unified-status": "allowed",
      "anthropic-ratelimit-unified-5h-utilization": "0.63",
      "anthropic-ratelimit-unified-5h-reset": "1793750400",
      "anthropic-ratelimit-unified-7d-utilization": "0.21",
      "anthropic-ratelimit-unified-7d-reset": "1794182400",
      "anthropic-ratelimit-unified-7d_opus-utilization": "0.5",
    });
    const w = parseRateLimitHeaders(h, NOW);
    expect(w).toEqual([
      { label: "5h", used_percent: 63, resets_at: new Date(1793750400_000).toISOString(), plan: true },
      { label: "weekly", used_percent: 21, resets_at: new Date(1794182400_000).toISOString(), plan: true },
      { label: "weekly (Opus)", used_percent: 50, resets_at: "", plan: true },
    ]);
  });

  it("reads OpenAI-compatible and OpenRouter windows", () => {
    const openai = parseRateLimitHeaders(
      new Headers({
        "x-ratelimit-limit-requests": "500",
        "x-ratelimit-remaining-requests": "499",
        "x-ratelimit-reset-requests": "120ms",
        "x-ratelimit-limit-tokens": "30000",
        "x-ratelimit-remaining-tokens": "15000",
        "x-ratelimit-reset-tokens": "6m0s",
      }),
      NOW,
    );
    expect(openai).toEqual([
      { label: "requests/min", used_percent: 0.2, limit: 500, remaining: 499, resets_at: "2026-11-03T12:00:00.120Z" },
      { label: "tokens/min", used_percent: 50, limit: 30000, remaining: 15000, resets_at: "2026-11-03T12:06:00.000Z" },
    ]);
    const openrouter = parseRateLimitHeaders(new Headers({ "x-ratelimit-limit": "20", "x-ratelimit-remaining": "5", "x-ratelimit-reset": "1793750400000" }), NOW);
    expect(openrouter).toEqual([{ label: "requests", used_percent: 75, limit: 20, remaining: 5, resets_at: new Date(1793750400000).toISOString() }]);
    expect(parseRateLimitHeaders(new Headers({ "content-type": "text/event-stream" }), NOW)).toEqual([]);
  });

  it("keeps the latest snapshot per provider and merges windows by label", () => {
    forgetRateLimits();
    captureRateLimits("p", new Headers({ "anthropic-ratelimit-unified-5h-utilization": "0.1", "anthropic-ratelimit-unified-7d-utilization": "0.2" }), NOW);
    captureRateLimits("p", new Headers({ "anthropic-ratelimit-unified-5h-utilization": "0.3" }), NOW + 1000);
    captureRateLimits("p", new Headers({ "content-type": "application/json" }), NOW + 2000);
    const snap = windowSnapshot("p")!;
    expect(snap.updated_at).toBe(new Date(NOW + 1000).toISOString());
    expect(snap.windows.map((w) => [w.label, w.used_percent])).toEqual([
      ["5h", 30],
      ["weekly", 20],
    ]);
    expect(windowSnapshot("other")).toBeUndefined();
    forgetRateLimits("p");
    expect(windowSnapshot("p")).toBeUndefined();
  });
});

type Route = Record<string, { status?: number; json: unknown } | Error>;

function mockFetch(routes: Route) {
  const calls: { url: string; auth: string }[] = [];
  const f: FetchFn = async (url, init) => {
    calls.push({ url, auth: init.headers.Authorization! });
    const r = routes[url];
    if (!r) return { ok: false, status: 404, json: async () => ({}) };
    if (r instanceof Error) throw r;
    const status = r.status ?? 200;
    return { ok: status < 300, status, json: async () => r.json };
  };
  return { f, calls };
}

const KEY = "sk-secret-key-1234";
const prov = (id: string, baseUrl: string) => ({ id, baseUrl, apiKey: KEY, allowPrivateNetwork: false });

describe("balance adapters", () => {
  beforeEach(() => clearBalanceCache());

  it("picks adapters by host", () => {
    expect(balanceAdapterFor("https://openrouter.ai/api/v1")).toBe("openrouter");
    expect(balanceAdapterFor("https://api.deepseek.com")).toBe("deepseek");
    expect(balanceAdapterFor("https://api.moonshot.ai/v1")).toBe("moonshot");
    expect(balanceAdapterFor("https://api.moonshot.cn/v1")).toBe("moonshot");
    expect(balanceAdapterFor("https://api.openai.com/v1")).toBeUndefined();
    expect(balanceAdapterFor("https://api.anthropic.com")).toBeUndefined();
    expect(getBalance({ ...prov("x", "https://api.openai.com/v1") })).toBeUndefined();
    expect(getBalance({ ...prov("x", "https://openrouter.ai/api/v1"), apiKey: undefined })).toBeUndefined();
  });

  it("OpenRouter: key limit, capped by the account credits", async () => {
    const { f, calls } = mockFetch({
      "https://openrouter.ai/api/v1/key": { json: { data: { label: "k", limit: 20, usage: 7.6, limit_remaining: 12.4 } } },
      "https://openrouter.ai/api/v1/credits": { json: { data: { total_credits: 50, total_usage: 10 } } },
    });
    const b = await getBalance(prov("or", "https://openrouter.ai/api/v1"), f, NOW)!;
    expect(b).toEqual({
      currency: "USD",
      balance: 12.4,
      used: 7.6,
      total: 20,
      label: "key limit",
      updated_at: new Date(NOW).toISOString(),
      source: "openrouter:/api/v1/key+/api/v1/credits",
    });
    expect(calls.every((c) => c.auth === `Bearer ${KEY}`)).toBe(true);

    clearBalanceCache();
    const low = mockFetch({
      "https://openrouter.ai/api/v1/key": { json: { data: { limit: 20, usage: 7.6, limit_remaining: 12.4 } } },
      "https://openrouter.ai/api/v1/credits": { json: { data: { total_credits: 10, total_usage: 7 } } },
    });
    expect((await getBalance(prov("or", "https://openrouter.ai/api/v1"), low.f, NOW)!).balance).toBe(3);
  });

  it("OpenRouter: account credits without a key limit; credits refused for a non-management key", async () => {
    const credits = mockFetch({
      "https://openrouter.ai/api/v1/key": { json: { data: { limit: null, usage: 3 } } },
      "https://openrouter.ai/api/v1/credits": { json: { data: { total_credits: 25, total_usage: 12.5 } } },
    });
    expect(await getBalance(prov("a", "https://openrouter.ai/api/v1"), credits.f, NOW)!).toMatchObject({ balance: 12.5, used: 12.5, total: 25, label: "credits", source: "openrouter:/api/v1/credits" });

    const refused = mockFetch({
      "https://openrouter.ai/api/v1/key": { json: { data: { limit: null, usage: 3 } } },
      "https://openrouter.ai/api/v1/credits": { status: 403, json: { error: "management key required" } },
    });
    const b = await getBalance(prov("b", "https://openrouter.ai/api/v1"), refused.f, NOW)!;
    expect(b).toMatchObject({ used: 3, label: "key usage", source: "openrouter:/api/v1/key" });
    expect(b.balance).toBeUndefined();
    expect(b.error).toBeUndefined();
  });

  it("DeepSeek: /user/balance, USD preferred", async () => {
    const { f, calls } = mockFetch({
      "https://api.deepseek.com/user/balance": {
        json: {
          is_available: true,
          balance_infos: [
            { currency: "CNY", total_balance: "110.00", granted_balance: "10.00", topped_up_balance: "100.00" },
            { currency: "USD", total_balance: "15.50", granted_balance: "0.00", topped_up_balance: "15.50" },
          ],
        },
      },
    });
    const b = await getBalance(prov("ds", "https://api.deepseek.com/v1"), f, NOW)!;
    expect(b).toMatchObject({ currency: "USD", balance: 15.5, label: "credits", source: "deepseek:/user/balance" });
    expect(calls.map((c) => c.url)).toEqual(["https://api.deepseek.com/user/balance"]);
  });

  it("Moonshot: /v1/users/me/balance, CNY on the .cn platform", async () => {
    const body = { code: 0, data: { available_balance: 49.58894, voucher_balance: 46.58893, cash_balance: 3.00001 }, scode: "0x0", status: true };
    const ai = mockFetch({ "https://api.moonshot.ai/v1/users/me/balance": { json: body } });
    expect(await getBalance(prov("ms", "https://api.moonshot.ai/v1"), ai.f, NOW)!).toMatchObject({ currency: "USD", balance: 49.58894, source: "moonshot:/v1/users/me/balance" });
    const cn = mockFetch({ "https://api.moonshot.cn/v1/users/me/balance": { json: body } });
    expect((await getBalance(prov("mscn", "https://api.moonshot.cn/v1"), cn.f, NOW)!).currency).toBe("CNY");
  });

  it("caches for the TTL (failures too), shares concurrent reads and never leaks the key", async () => {
    let n = 0;
    const f: FetchFn = async () => {
      n++;
      throw new Error(`connect failed for ${KEY}`);
    };
    const p = prov("ds", "https://api.deepseek.com");
    const [a, b] = await Promise.all([getBalance(p, f, NOW)!, getBalance(p, f, NOW + 10)!]);
    expect(n).toBe(1);
    expect(a).toBe(b);
    expect(a.error).toBe("The provider could not be reached");
    expect(JSON.stringify(a)).not.toContain(KEY);
    await getBalance(p, f, NOW + BALANCE_TTL_MS - 1);
    expect(n).toBe(1);
    await getBalance(p, f, NOW + BALANCE_TTL_MS);
    expect(n).toBe(2);
    // A changed key starts over.
    await getBalance({ ...p, apiKey: "sk-other-key-9999" }, f, NOW + BALANCE_TTL_MS + 1);
    expect(n).toBe(3);

    clearBalanceCache();
    const rejected = mockFetch({ "https://api.deepseek.com/user/balance": { status: 401, json: {} } });
    expect((await getBalance(p, rejected.f, NOW)!).error).toBe("The key was rejected (HTTP 401)");
  });
});
