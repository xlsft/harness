/**
 * Balance adapters: prepaid credit or key limits read from a provider's own API, for providers that document
 * one. Picked by the provider's host, so a custom provider on the same API works too:
 *
 *   - OpenRouter (openrouter.ai): GET /api/v1/key (the key's limit, usage and limit_remaining; works with any
 *     key) and GET /api/v1/credits (the account's total_credits - total_usage; may need a management key, so a
 *     refusal there is not an error);
 *   - DeepSeek (api.deepseek.com): GET /user/balance;
 *   - Moonshot / Kimi platform (api.moonshot.ai: USD, api.moonshot.cn: CNY): GET /v1/users/me/balance.
 *
 * OpenAI, Anthropic, Gemini, xAI (management keys only), Z.ai and the coding plans have no balance API usable
 * with an inference key: they get spend and windows only (limits/service.ts).
 *
 * Results (a failure too) are cached per provider for BALANCE_TTL_MS and concurrent reads share one request,
 * so a provider is never asked more often. Keys go in the Authorization header only; error messages never
 * carry them.
 */
import { guardedFetch } from "../lib/ssrf.ts";
import type { ProviderRuntime } from "../services/providers.ts";

export interface Balance {
  currency: string;
  balance?: number;
  used?: number;
  total?: number;
  /** "key limit" or "credits". */
  label: string;
  updated_at: string;
  source: string;
  error?: string;
}

export type FetchFn = (url: string, init: { headers: Record<string, string>; signal: AbortSignal; allowPrivateNetwork: boolean }) => Promise<{
  ok: boolean;
  status: number;
  json(): Promise<unknown>;
}>;

export type BalanceProvider = Pick<ProviderRuntime, "id" | "baseUrl" | "apiKey" | "allowPrivateNetwork">;

interface BalanceAdapter {
  id: string;
  matches(host: string): boolean;
  fetch(p: BalanceProvider, f: FetchFn, now: Date): Promise<Balance>;
}

export const BALANCE_TTL_MS = 60_000;
const TIMEOUT_MS = 10_000;

const defaultFetch: FetchFn = async (url, init) => {
  const res = await guardedFetch(url, { method: "GET", headers: init.headers, signal: init.signal, allowPrivateNetwork: init.allowPrivateNetwork });
  return { ok: res.ok, status: res.status, json: () => res.json() };
};

function hostOf(baseUrl: string): string {
  try {
    return new URL(baseUrl).hostname.toLowerCase();
  } catch {
    return "";
  }
}

function originOf(baseUrl: string): string {
  return new URL(baseUrl).origin;
}

function n(v: unknown): number | undefined {
  if (typeof v === "number" && Number.isFinite(v)) return v;
  if (typeof v === "string" && v.trim() !== "" && Number.isFinite(Number(v))) return Number(v);
  return undefined;
}

function round(v: number | undefined): number | undefined {
  return v === undefined ? undefined : Math.round(v * 1e6) / 1e6;
}

class HttpError extends Error {}

async function getJson(f: FetchFn, url: string, p: BalanceProvider): Promise<Record<string, unknown>> {
  const res = await f(url, {
    headers: { Authorization: `Bearer ${p.apiKey ?? ""}`, Accept: "application/json" },
    signal: AbortSignal.timeout(TIMEOUT_MS),
    allowPrivateNetwork: p.allowPrivateNetwork,
  });
  if (!res.ok) throw new HttpError(res.status === 401 || res.status === 403 ? `The key was rejected (HTTP ${res.status})` : `HTTP ${res.status}`);
  const j = await res.json();
  if (!j || typeof j !== "object") throw new HttpError("Unexpected response");
  return j as Record<string, unknown>;
}

const openrouter: BalanceAdapter = {
  id: "openrouter",
  matches: (h) => h === "openrouter.ai" || h.endsWith(".openrouter.ai"),
  async fetch(p, f, now) {
    const base = `${originOf(p.baseUrl)}/api/v1`;
    const [key, credits] = await Promise.allSettled([getJson(f, `${base}/key`, p), getJson(f, `${base}/credits`, p)]);
    const k = key.status === "fulfilled" ? ((key.value.data ?? {}) as Record<string, unknown>) : undefined;
    const c = credits.status === "fulfilled" ? ((credits.value.data ?? {}) as Record<string, unknown>) : undefined;
    const creditsTotal = n(c?.total_credits);
    const creditsUsed = n(c?.total_usage);
    const creditsLeft = creditsTotal !== undefined && creditsUsed !== undefined ? creditsTotal - creditsUsed : undefined;
    const keyLimit = n(k?.limit);
    if (k && keyLimit !== undefined) {
      const keyUsed = n(k.usage);
      let left = n(k.limit_remaining) ?? (keyUsed !== undefined ? keyLimit - keyUsed : undefined);
      // The key cannot spend more than the account holds.
      if (left !== undefined && creditsLeft !== undefined) left = Math.min(left, creditsLeft);
      return {
        currency: "USD",
        balance: round(left),
        used: round(keyUsed),
        total: keyLimit,
        label: "key limit",
        updated_at: now.toISOString(),
        source: c ? "openrouter:/api/v1/key+/api/v1/credits" : "openrouter:/api/v1/key",
      };
    }
    if (creditsLeft !== undefined) {
      return { currency: "USD", balance: round(creditsLeft), used: round(creditsUsed), total: creditsTotal, label: "credits", updated_at: now.toISOString(), source: "openrouter:/api/v1/credits" };
    }
    if (k) {
      // A key without a limit, and the account credits are not readable with it.
      return { currency: "USD", used: round(n(k.usage)), label: "key usage", updated_at: now.toISOString(), source: "openrouter:/api/v1/key" };
    }
    throw key.status === "rejected" ? key.reason : new HttpError("Unexpected response");
  },
};

const deepseek: BalanceAdapter = {
  id: "deepseek",
  matches: (h) => h === "api.deepseek.com",
  async fetch(p, f, now) {
    const j = await getJson(f, `${originOf(p.baseUrl)}/user/balance`, p);
    const infos = Array.isArray(j.balance_infos) ? (j.balance_infos as Record<string, unknown>[]) : [];
    const info = infos.find((i) => i.currency === "USD") ?? infos[0];
    if (!info) throw new HttpError("No balance in the response");
    return {
      currency: typeof info.currency === "string" ? info.currency : "CNY",
      balance: n(info.total_balance),
      label: "credits",
      updated_at: now.toISOString(),
      source: "deepseek:/user/balance",
    };
  },
};

const moonshot: BalanceAdapter = {
  id: "moonshot",
  matches: (h) => h === "api.moonshot.ai" || h === "api.moonshot.cn",
  async fetch(p, f, now) {
    const j = await getJson(f, `${originOf(p.baseUrl)}/v1/users/me/balance`, p);
    const d = (j.data ?? {}) as Record<string, unknown>;
    const available = n(d.available_balance);
    if (available === undefined) throw new HttpError("No balance in the response");
    return {
      currency: hostOf(p.baseUrl).endsWith(".cn") ? "CNY" : "USD",
      balance: available,
      label: "credits",
      updated_at: now.toISOString(),
      source: "moonshot:/v1/users/me/balance",
    };
  },
};

const ADAPTERS: BalanceAdapter[] = [openrouter, deepseek, moonshot];

/** The adapter for a provider's base URL, if its service has a balance API. */
export function balanceAdapterFor(baseUrl: string): string | undefined {
  const host = hostOf(baseUrl);
  return ADAPTERS.find((a) => a.matches(host))?.id;
}

interface CacheEntry {
  at: number;
  /** Cache key part: a changed base URL or key starts over. */
  fingerprint: string;
  value: Promise<Balance>;
}

const cache = new Map<string, CacheEntry>();

function fingerprint(p: BalanceProvider): string {
  // Not the key itself: its length and last characters are enough to notice a change.
  const k = p.apiKey ?? "";
  return `${p.baseUrl}|${k.length}|${k.slice(-4)}`;
}

/**
 * The provider's balance, cached for BALANCE_TTL_MS (failures too); undefined when its service has no balance
 * API or it has no key.
 */
export function getBalance(p: BalanceProvider, f: FetchFn = defaultFetch, now = Date.now()): Promise<Balance> | undefined {
  const host = hostOf(p.baseUrl);
  const adapter = ADAPTERS.find((a) => a.matches(host));
  if (!adapter || !p.apiKey) return undefined;
  const fp = fingerprint(p);
  const hit = cache.get(p.id);
  if (hit && hit.fingerprint === fp && now - hit.at < BALANCE_TTL_MS) return hit.value;
  const at = new Date(now);
  const value = adapter.fetch(p, f, at).catch(
    (err: unknown): Balance => ({
      currency: "",
      label: "",
      updated_at: at.toISOString(),
      source: adapter.id,
      error: err instanceof HttpError ? err.message : "The provider could not be reached",
    }),
  );
  cache.set(p.id, { at: now, fingerprint: fp, value });
  return value;
}

export function clearBalanceCache(providerId?: string): void {
  if (providerId === undefined) cache.clear();
  else cache.delete(providerId);
}
