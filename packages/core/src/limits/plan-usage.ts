/**
 * Plan usage adapters: a subscription plan's windows read from the service's own API, for plans that answer with
 * the inference key and send no usage headers (limits/headers.ts covers those that do). Picked by host, like the
 * balance adapters (limits/balance.ts):
 *
 *   - Kimi Code (api.kimi.com, api.kimi.ai): GET <base>/v1/usages, the endpoint the official kimi-cli reads for its
 *     /usage command. Not documented. As answered in November 2026: `limits[]` holds the rolling windows with counts
 *     (`window: {duration, timeUnit}`, `detail: {limit, used, remaining, resetTime}`), and `usages` a map of all
 *     windows as fractions (`limit_5h`, `limit_month_total`, `limit_month_code`: `{used_ratio, reset_time}`).
 *     kimi-cli also reads a single `usage` quota (`{limit, used | remaining}`), kept here. A window with counts wins
 *     over the same window as a fraction. Fields are read as loosely as kimi-cli reads them.
 *
 * Results (a failure too) are cached per provider for PLAN_USAGE_TTL_MS and concurrent reads share one request. A
 * failure gives no windows, and the header snapshot (if any) is shown instead (limits/service.ts). Keys go in the
 * Authorization header only.
 */
import type { FetchFn, BalanceProvider } from "./balance.ts";
import { parseReset, type LimitWindow } from "./headers.ts";
import { guardedFetch } from "../lib/ssrf.ts";

export interface PlanUsage {
  windows: LimitWindow[];
  /** RFC 3339, UTC. */
  updated_at: string;
  source: string;
  error?: string;
}

interface PlanUsageAdapter {
  id: string;
  matches(host: string): boolean;
  fetch(p: BalanceProvider, f: FetchFn, now: Date): Promise<PlanUsage>;
}

export const PLAN_USAGE_TTL_MS = 60_000;
const TIMEOUT_MS = 10_000;

const defaultFetch: FetchFn = async (url, init) => {
  const res = await guardedFetch(url, { method: "GET", headers: init.headers, signal: init.signal, allowPrivateNetwork: init.allowPrivateNetwork });
  return { ok: res.ok, status: res.status, json: () => res.json() };
};

class HttpError extends Error {}

function hostOf(baseUrl: string): string {
  try {
    return new URL(baseUrl).hostname.toLowerCase();
  } catch {
    return "";
  }
}

function n(v: unknown): number | undefined {
  if (typeof v === "number" && Number.isFinite(v)) return v;
  if (typeof v === "string" && v.trim() !== "" && Number.isFinite(Number(v))) return Number(v);
  return undefined;
}

function obj(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined;
}

function clamp(p: number): number {
  return Math.round(Math.min(100, Math.max(0, p)) * 100) / 100;
}

/** A window length as the label the apps show: "5h", "weekly", "30m", "1d". */
export function durationLabel(duration: number, unit: string): string | undefined {
  if (!(duration > 0)) return undefined;
  const u = unit.toUpperCase();
  let minutes: number;
  if (u.includes("MINUTE")) minutes = duration;
  else if (u.includes("HOUR")) minutes = duration * 60;
  else if (u.includes("DAY")) minutes = duration * 1440;
  else if (u.includes("WEEK")) minutes = duration * 10080;
  else if (u.includes("SECOND") || u === "") minutes = duration / 60;
  else return undefined;
  if (minutes === 10080) return "weekly";
  if (minutes % 1440 === 0) return `${minutes / 1440}d`;
  if (minutes % 60 === 0) return `${minutes / 60}h`;
  return `${Math.round(minutes)}m`;
}

function resetOf(d: Record<string, unknown>, now: number): string {
  for (const k of ["reset_at", "resetAt", "reset_time", "resetTime"]) {
    const v = d[k];
    if (typeof v === "string" || typeof v === "number") {
      const r = parseReset(String(v), now);
      if (r) return r;
    }
  }
  for (const k of ["reset_in", "resetIn", "ttl"]) {
    const s = n(d[k]);
    if (s !== undefined && s > 0) return new Date(now + s * 1000).toISOString();
  }
  return "";
}

function kimiWindow(d: Record<string, unknown>, label: string, now: number): LimitWindow | undefined {
  const limit = n(d.limit);
  let used = n(d.used);
  let remaining = n(d.remaining);
  if (used === undefined && remaining !== undefined && limit !== undefined) used = limit - remaining;
  if (remaining === undefined && used !== undefined && limit !== undefined) remaining = limit - used;
  if (limit === undefined || used === undefined) return undefined;
  const w: LimitWindow = { label, used_percent: limit > 0 ? clamp((used / limit) * 100) : 0, limit, resets_at: resetOf(d, now), plan: true };
  if (remaining !== undefined) w.remaining = Math.max(0, remaining);
  return w;
}

/** A `usages` key ("limit_5h", "limit_month_total", "limit_month_code") as the label the apps show. */
export function kimiUsageLabel(key: string): string {
  const name = key.replace(/^limits?_/, "");
  const m = /^(month|week|day)(?:_(.+))?$/.exec(name);
  if (!m) return name;
  const period = m[1] === "month" ? "monthly" : m[1] === "week" ? "weekly" : "daily";
  const sub = m[2];
  if (!sub || sub === "total") return period;
  return `${period} (${sub.charAt(0).toUpperCase()}${sub.slice(1)})`;
}

/** The windows a Kimi Code /usages answer describes: counted windows first, then the fraction-only ones. */
export function parseKimiUsages(j: Record<string, unknown>, now = Date.now()): LimitWindow[] {
  const out: LimitWindow[] = [];
  const limits = Array.isArray(j.limits) ? j.limits : [];
  limits.forEach((raw, i) => {
    const item = obj(raw);
    if (!item) return;
    const detail = obj(item.detail) ?? item;
    const win = obj(item.window) ?? {};
    const duration = n(win.duration ?? item.duration ?? detail.duration);
    const unit = String(win.timeUnit ?? item.timeUnit ?? detail.timeUnit ?? "");
    const named = [item.name, item.title, item.scope, detail.name, detail.title].find((v) => typeof v === "string" && v.trim() !== "") as string | undefined;
    const label = (duration !== undefined ? durationLabel(duration, unit) : undefined) ?? named ?? `limit ${i + 1}`;
    const w = kimiWindow(detail, label, now);
    if (w) out.push(w);
  });
  const usage = obj(j.usage);
  if (usage) {
    const w = kimiWindow(usage, "weekly", now);
    if (w && !out.some((o) => o.label === "weekly")) out.push(w);
  }
  const usages = obj(j.usages) ?? {};
  for (const [key, raw] of Object.entries(usages)) {
    const d = obj(raw);
    const ratio = d && n(d.used_ratio ?? d.usedRatio);
    if (!d || ratio === undefined) continue;
    const label = kimiUsageLabel(key);
    if (out.some((o) => o.label === label)) continue;
    out.push({ label, used_percent: clamp(ratio > 1 ? ratio : ratio * 100), resets_at: resetOf(d, now), plan: true });
  }
  return out;
}

const kimiCode: PlanUsageAdapter = {
  id: "kimi-code",
  matches: (h) => h === "api.kimi.com" || h === "api.kimi.ai",
  async fetch(p, f, now) {
    // Presets carry ".../coding"; kimi-cli's own base is ".../coding/v1".
    const base = p.baseUrl.replace(/\/+$/, "").replace(/\/v1$/, "");
    const res = await f(`${base}/v1/usages`, {
      headers: { Authorization: `Bearer ${p.apiKey ?? ""}`, Accept: "application/json" },
      signal: AbortSignal.timeout(TIMEOUT_MS),
      allowPrivateNetwork: p.allowPrivateNetwork,
    });
    if (!res.ok) throw new HttpError(res.status === 401 || res.status === 403 ? `The key was rejected (HTTP ${res.status})` : `HTTP ${res.status}`);
    const j = obj(await res.json());
    if (!j) throw new HttpError("Unexpected response");
    return { windows: parseKimiUsages(j, now.getTime()), updated_at: now.toISOString(), source: "kimi:/coding/v1/usages" };
  },
};

const ADAPTERS: PlanUsageAdapter[] = [kimiCode];

/** The adapter for a provider's base URL, if its plan reports usage through an API. */
export function planUsageAdapterFor(baseUrl: string): string | undefined {
  const host = hostOf(baseUrl);
  return ADAPTERS.find((a) => a.matches(host))?.id;
}

interface CacheEntry {
  at: number;
  fingerprint: string;
  value: Promise<PlanUsage>;
}

const cache = new Map<string, CacheEntry>();

function fingerprint(p: BalanceProvider): string {
  const k = p.apiKey ?? "";
  return `${p.baseUrl}|${k.length}|${k.slice(-4)}`;
}

/**
 * The provider's plan windows, cached for PLAN_USAGE_TTL_MS (failures too); undefined when its service has no usage
 * API or it has no key.
 */
export function getPlanUsage(p: BalanceProvider, f: FetchFn = defaultFetch, now = Date.now()): Promise<PlanUsage> | undefined {
  const host = hostOf(p.baseUrl);
  const adapter = ADAPTERS.find((a) => a.matches(host));
  if (!adapter || !p.apiKey) return undefined;
  const fp = fingerprint(p);
  const hit = cache.get(p.id);
  if (hit && hit.fingerprint === fp && now - hit.at < PLAN_USAGE_TTL_MS) return hit.value;
  const at = new Date(now);
  const value = adapter.fetch(p, f, at).catch(
    (err: unknown): PlanUsage => ({
      windows: [],
      updated_at: at.toISOString(),
      source: adapter.id,
      error: err instanceof HttpError ? err.message : "The provider could not be reached",
    }),
  );
  cache.set(p.id, { at: now, fingerprint: fp, value });
  return value;
}

export function clearPlanUsageCache(providerId?: string): void {
  if (providerId === undefined) cache.clear();
  else cache.delete(providerId);
}
