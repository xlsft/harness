/**
 * SearXNG provider: the default, free, key-less search backend. In the single-container image it runs
 * next to core on 127.0.0.1 (see docs/PLAN.md §1.7); HARNESS_SEARXNG_URL points at it or at an external
 * instance. The instance must have the JSON output format enabled (`search.formats: [html, json]`).
 */
import { guardedFetch } from "../lib/ssrf.ts";
import { ApiError } from "../lib/errors.ts";
import type { SearchHit, SearchProvider, SearchQuery } from "./types.ts";

interface SearxngResult {
  title?: string;
  url?: string;
  content?: string;
  publishedDate?: string | null;
}

export interface SearxngOptions {
  /** Base URL, e.g. http://127.0.0.1:8888 */
  baseUrl: string;
  /** Request timeout in ms (default 10 s). */
  timeoutMs?: number;
  /** SearXNG safesearch level: 0 off, 1 moderate (default), 2 strict. */
  safeSearch?: 0 | 1 | 2;
}

export class SearxngProvider implements SearchProvider {
  readonly id = "searxng";
  readonly displayName = "SearXNG";
  private readonly base: string;
  private readonly timeoutMs: number;
  private readonly safeSearch: number;

  constructor(opts: SearxngOptions) {
    this.base = opts.baseUrl.replace(/\/+$/, "");
    this.timeoutMs = opts.timeoutMs ?? 10_000;
    this.safeSearch = opts.safeSearch ?? 1;
  }

  async search(q: SearchQuery): Promise<SearchHit[]> {
    const url = new URL(`${this.base}/search`);
    // A single allowed domain is pushed down as a site: operator; the service still post-filters.
    const query = q.allowedDomains.length === 1 ? `${q.query} site:${q.allowedDomains[0]}` : q.query;
    url.searchParams.set("q", query);
    url.searchParams.set("format", "json");
    url.searchParams.set("safesearch", String(this.safeSearch));
    url.searchParams.set("language", "all");

    const signal = q.signal ? AbortSignal.any([q.signal, AbortSignal.timeout(this.timeoutMs)]) : AbortSignal.timeout(this.timeoutMs);
    let res;
    try {
      // The SearXNG URL is operator configuration (usually loopback), so private addresses are allowed.
      res = await guardedFetch(url.toString(), { method: "GET", headers: { accept: "application/json" }, signal, allowPrivateNetwork: true });
    } catch (err) {
      if (err instanceof ApiError) throw err;
      throw new ApiError("upstream", `SearXNG is unreachable: ${(err as Error).message}`);
    }
    if (res.status === 403) {
      await res.body?.cancel();
      throw new ApiError("upstream", "SearXNG refused the JSON format; enable `json` in search.formats");
    }
    if (res.status === 429) {
      await res.body?.cancel();
      throw new ApiError("rate_limit", "SearXNG is rate limiting requests", { retryAfterMs: 5_000 });
    }
    if (!res.ok) {
      await res.body?.cancel();
      throw new ApiError("upstream", `SearXNG returned HTTP ${res.status}`, { upstreamStatus: res.status });
    }
    const body = (await res.json()) as { results?: SearxngResult[] };
    const hits: SearchHit[] = [];
    for (const r of body.results ?? []) {
      if (!r.url || !/^https?:\/\//i.test(r.url)) continue;
      hits.push({
        title: (r.title ?? r.url).trim(),
        url: r.url,
        snippet: (r.content ?? "").trim(),
        ...(r.publishedDate ? { publishedAt: toRfc3339(r.publishedDate) } : {}),
      });
    }
    return hits;
  }

  async healthy(signal?: AbortSignal): Promise<boolean> {
    try {
      const s = signal ? AbortSignal.any([signal, AbortSignal.timeout(3_000)]) : AbortSignal.timeout(3_000);
      const res = await guardedFetch(`${this.base}/healthz`, { method: "GET", signal: s, allowPrivateNetwork: true });
      await res.body?.cancel();
      return res.ok;
    } catch {
      return false;
    }
  }
}

function toRfc3339(value: string): string | undefined {
  // SearXNG emits naive ISO timestamps; treat a missing zone as UTC instead of server-local time.
  const zoned = /([zZ]|[+-]\d{2}:?\d{2})$/.test(value) || !value.includes("T") ? value : `${value}Z`;
  const d = new Date(zoned);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}
