/**
 * Rate-limit windows from the response headers the gateway already receives (no extra requests):
 *
 *   - Anthropic API keys: anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens}-{limit,remaining,reset}
 *     (reset: RFC 3339), all per minute;
 *   - Anthropic plans (OAuth / subscription): anthropic-ratelimit-unified-<window>-utilization (a 0..1 fraction)
 *     and -<window>-reset (unix seconds), e.g. 5h, 7d, 7d_opus, 7d_sonnet;
 *   - OpenAI-compatible: x-ratelimit-{limit,remaining,reset}-{requests,tokens} (reset: a Go-style duration such
 *     as "6m0s" or "20ms"), and the suffix-less x-ratelimit-{limit,remaining,reset} (OpenRouter: reset in epoch
 *     milliseconds).
 *
 * The latest snapshot per provider lives in this process's memory (lost on restart). Each new response
 * replaces the windows it carries and keeps the others, so a response without plan headers does not wipe the
 * plan windows. Snapshots of personal providers are only ever read for their owner (limits/service.ts).
 */

export interface LimitWindow {
  label: string;
  /** 0..100. */
  used_percent: number;
  remaining?: number;
  limit?: number;
  /** RFC 3339, UTC; empty when unknown. */
  resets_at: string;
  /** A subscription plan's usage (the unified headers), which is shown; the rest are rate limits, which are not (limits/service.ts). */
  plan?: boolean;
}

export interface WindowSnapshot {
  windows: LimitWindow[];
  /** RFC 3339, UTC. */
  updated_at: string;
}

type HeaderSource = { get(name: string): string | null; forEach?(cb: (value: string, key: string) => void): void };

function num(v: string | null | undefined): number | undefined {
  if (v === null || v === undefined || v.trim() === "") return undefined;
  const n = Number(v);
  return Number.isFinite(n) ? n : undefined;
}

const DURATION_RE = /^(?:(\d+(?:\.\d+)?)h)?(?:(\d+(?:\.\d+)?)m(?!s))?(?:(\d+(?:\.\d+)?)s)?(?:(\d+(?:\.\d+)?)ms)?$/;

/** A Go-style duration ("1h2m3.5s", "6m0s", "20ms", "1s") in milliseconds; undefined when it is not one. */
export function parseDurationMs(v: string): number | undefined {
  const s = v.trim();
  if (!s) return undefined;
  const m = DURATION_RE.exec(s);
  if (!m || m.slice(1).every((x) => x === undefined)) return undefined;
  const [h, min, sec, ms] = m.slice(1).map((x) => (x === undefined ? 0 : Number(x)));
  return ((h! * 60 + min!) * 60 + sec!) * 1000 + ms!;
}

/**
 * A reset value as an RFC 3339 instant: RFC 3339 / HTTP dates, epoch seconds or milliseconds, plain seconds
 * from now (small numbers) and Go-style durations from now.
 */
export function parseReset(v: string | null | undefined, now = Date.now()): string {
  if (!v) return "";
  const s = v.trim();
  if (!s) return "";
  const n = num(s);
  if (n !== undefined) {
    if (n > 1e12) return new Date(n).toISOString();
    if (n > 1e9) return new Date(n * 1000).toISOString();
    return new Date(now + n * 1000).toISOString();
  }
  const d = parseDurationMs(s);
  if (d !== undefined) return new Date(now + d).toISOString();
  const t = Date.parse(s);
  return Number.isNaN(t) ? "" : new Date(t).toISOString();
}

function countWindow(label: string, limit: number | undefined, remaining: number | undefined, reset: string): LimitWindow | undefined {
  if (limit === undefined && remaining === undefined) return undefined;
  const used = limit !== undefined && limit > 0 && remaining !== undefined ? ((limit - remaining) / limit) * 100 : 0;
  const w: LimitWindow = { label, used_percent: clamp(used), resets_at: reset };
  if (limit !== undefined) w.limit = limit;
  if (remaining !== undefined) w.remaining = remaining;
  return w;
}

function clamp(p: number): number {
  return Math.round(Math.min(100, Math.max(0, p)) * 100) / 100;
}

const UNIFIED_RE = /^anthropic-ratelimit-unified-(.+)-utilization$/;

/** A unified window name ("5h", "7d", "7d_opus") as the label the apps show. */
export function unifiedLabel(name: string): string {
  if (name === "5h") return "5h";
  if (name === "7d") return "weekly";
  const m = /^7d[_-](.+)$/.exec(name);
  if (m) return `weekly (${m[1]!.charAt(0).toUpperCase()}${m[1]!.slice(1)})`;
  return name;
}

/** The rate-limit windows a response's headers describe (empty when it has none). */
export function parseRateLimitHeaders(h: HeaderSource, now = Date.now()): LimitWindow[] {
  const out: LimitWindow[] = [];
  const push = (w: LimitWindow | undefined) => w && out.push(w);

  // Anthropic plans: unified windows.
  const names: string[] = [];
  if (h.forEach) {
    h.forEach((_v, key) => {
      const m = UNIFIED_RE.exec(key.toLowerCase());
      if (m) names.push(m[1]!);
    });
  } else {
    names.push("5h", "7d");
  }
  for (const name of names.sort()) {
    const u = num(h.get(`anthropic-ratelimit-unified-${name}-utilization`));
    if (u === undefined) continue;
    out.push({ label: unifiedLabel(name), used_percent: clamp(u > 1 ? u : u * 100), resets_at: parseReset(h.get(`anthropic-ratelimit-unified-${name}-reset`), now), plan: true });
  }

  // Anthropic API keys.
  for (const [kind, label] of [
    ["requests", "requests/min"],
    ["tokens", "tokens/min"],
    ["input-tokens", "input tokens/min"],
    ["output-tokens", "output tokens/min"],
  ] as const) {
    const p = `anthropic-ratelimit-${kind}`;
    push(countWindow(label, num(h.get(`${p}-limit`)), num(h.get(`${p}-remaining`)), parseReset(h.get(`${p}-reset`), now)));
  }

  // OpenAI-compatible.
  for (const [kind, label] of [
    ["requests", "requests/min"],
    ["tokens", "tokens/min"],
  ] as const) {
    if (out.some((w) => w.label === label)) continue;
    push(countWindow(label, num(h.get(`x-ratelimit-limit-${kind}`)), num(h.get(`x-ratelimit-remaining-${kind}`)), parseReset(h.get(`x-ratelimit-reset-${kind}`), now)));
  }
  // Suffix-less (OpenRouter): a request window.
  push(countWindow("requests", num(h.get("x-ratelimit-limit")), num(h.get("x-ratelimit-remaining")), parseReset(h.get("x-ratelimit-reset"), now)));
  return out;
}

const snapshots = new Map<string, WindowSnapshot>();

/** Records the windows of a response that passed through the gateway for this provider. */
export function captureRateLimits(providerId: string, headers: HeaderSource, now = Date.now()): void {
  let windows: LimitWindow[];
  try {
    windows = parseRateLimitHeaders(headers, now);
  } catch {
    return;
  }
  if (!windows.length) return;
  const prev = snapshots.get(providerId)?.windows ?? [];
  const labels = new Set(windows.map((w) => w.label));
  snapshots.set(providerId, { windows: [...windows, ...prev.filter((w) => !labels.has(w.label))], updated_at: new Date(now).toISOString() });
}

export function windowSnapshot(providerId: string): WindowSnapshot | undefined {
  return snapshots.get(providerId);
}

/** Drops a provider's snapshot (a deleted provider, a changed key) or all of them (tests). */
export function forgetRateLimits(providerId?: string): void {
  if (providerId === undefined) snapshots.clear();
  else snapshots.delete(providerId);
}
