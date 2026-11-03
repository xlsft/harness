/**
 * Keyed web search APIs behind SearchService: one adapter per provider type (services/presets.ts lists the
 * presets). Each turns a query into plain hits and maps the API's errors; the request shapes follow the
 * APIs' own references (November 2026):
 *   brave       GET  api.search.brave.com/res/v1/web/search  X-Subscription-Token   web.results[].{title,url,description}
 *   tavily      POST api.tavily.com/search                   Bearer                 results[].{title,url,content,published_date,favicon}
 *   exa         POST api.exa.ai/search                       x-api-key              results[].{title,url,highlights,text,publishedDate,favicon}
 *   kagi        POST kagi.com/api/v1/search                  Bearer                 data.search[].{title,url,snippet,time}
 *   perplexity  POST api.perplexity.ai/search                Bearer                 results[].{title,url,snippet,date}
 * Domain filters are pushed down where the API has them; the service post-filters either way.
 *
 * Error messages never carry the key: every message goes through redactSecrets, and upstream bodies are cut
 * to a short excerpt.
 */
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import type { SearchType } from "../services/presets.ts";
import { redactSecrets } from "../services/providers.ts";
import { SearxngProvider } from "./searxng.ts";
import type { SearchHit, SearchProvider, SearchQuery } from "./types.ts";

export const DEFAULT_TIMEOUT_MS = 15_000;

/** What an adapter needs from a stored provider (decrypted; never serialize). */
export interface SearchProviderConfig {
  id: string;
  displayName: string;
  type: SearchType;
  baseUrl: string;
  apiKey?: string;
  options: Record<string, string>;
  allowPrivateNetwork: boolean;
  timeoutMs?: number;
}

/** Builds the adapter of a stored search provider. */
export function createSearchProvider(cfg: SearchProviderConfig): SearchProvider {
  if (cfg.type === "searxng") {
    const safe = { off: 0, moderate: 1, strict: 2 }[cfg.options.safe_search ?? ""] as 0 | 1 | 2 | undefined;
    return new SearxngProvider({
      id: cfg.id,
      displayName: cfg.displayName,
      baseUrl: cfg.baseUrl,
      timeoutMs: cfg.timeoutMs,
      safeSearch: safe,
      language: cfg.options.language,
      allowPrivateNetwork: cfg.allowPrivateNetwork,
    });
  }
  return new ApiSearchProvider(cfg);
}

type Json = Record<string, any>;

interface Built {
  url: string;
  method: "GET" | "POST";
  headers: Record<string, string>;
  body?: Json;
}

class ApiSearchProvider implements SearchProvider {
  readonly id: string;
  readonly displayName: string;

  constructor(private readonly cfg: SearchProviderConfig) {
    this.id = cfg.id;
    this.displayName = cfg.displayName;
  }

  async search(q: SearchQuery): Promise<SearchHit[]> {
    if (!this.cfg.apiKey) throw new ApiError("failed_precondition", `${this.displayName} has no API key`);
    const req = this.build(q, this.cfg.apiKey);
    const body = await this.fetchJson(req, q.signal);
    return this.parse(body).filter((h) => /^https?:\/\//i.test(h.url));
  }

  /** Keyed APIs are not probed (a probe costs a query): healthy means it can be called. */
  async healthy(): Promise<boolean> {
    return !!this.cfg.apiKey;
  }

  private build(q: SearchQuery, key: string): Built {
    const o = this.cfg.options;
    const n = q.maxResults;
    const json = { "content-type": "application/json", accept: "application/json" };
    switch (this.cfg.type) {
      case "brave": {
        const url = new URL(this.cfg.baseUrl);
        // Operators go in q; a single allowed domain becomes site:.
        url.searchParams.set("q", q.allowedDomains.length === 1 ? `${q.query} site:${q.allowedDomains[0]}` : q.query);
        url.searchParams.set("count", String(Math.min(n, 20)));
        if (o.safe_search) url.searchParams.set("safesearch", o.safe_search);
        if (o.country) url.searchParams.set("country", o.country);
        if (o.language) url.searchParams.set("search_lang", o.language);
        return { url: url.toString(), method: "GET", headers: { accept: "application/json", "x-subscription-token": key } };
      }
      case "tavily":
        return {
          url: this.cfg.baseUrl,
          method: "POST",
          headers: { ...json, authorization: `Bearer ${key}` },
          body: {
            query: q.query,
            max_results: Math.min(n, 20),
            include_favicon: true,
            ...(o.safe_search === "strict" ? { safe_search: true } : {}),
            ...(q.allowedDomains.length ? { include_domains: q.allowedDomains } : {}),
            ...(q.blockedDomains.length ? { exclude_domains: q.blockedDomains } : {}),
          },
        };
      case "exa":
        return {
          url: this.cfg.baseUrl,
          method: "POST",
          headers: { ...json, "x-api-key": key },
          body: {
            query: q.query,
            numResults: Math.min(n, 100),
            type: "auto",
            contents: { highlights: { maxCharacters: 400 } },
            ...(o.country ? { userLocation: o.country } : {}),
            ...(q.allowedDomains.length ? { includeDomains: q.allowedDomains } : {}),
            ...(q.blockedDomains.length ? { excludeDomains: q.blockedDomains } : {}),
          },
        };
      case "kagi": {
        const filters: Json = {};
        if (o.safe_search) filters.safe_search = o.safe_search === "strict";
        if (o.country) filters.region = o.country;
        const lens: Json = {};
        if (q.allowedDomains.length) lens.sites_included = q.allowedDomains;
        if (q.blockedDomains.length) lens.sites_excluded = q.blockedDomains;
        return {
          url: this.cfg.baseUrl,
          method: "POST",
          headers: { ...json, authorization: `Bearer ${key}` },
          body: {
            query: q.query,
            limit: n,
            ...(Object.keys(filters).length ? { filters } : {}),
            ...(Object.keys(lens).length ? { lens } : {}),
          },
        };
      }
      case "perplexity":
        return {
          url: this.cfg.baseUrl,
          method: "POST",
          headers: { ...json, authorization: `Bearer ${key}` },
          body: {
            query: q.query,
            max_results: Math.min(n, 20),
            ...(o.country ? { country: o.country } : {}),
            ...(o.language && o.language !== "all" ? { search_language_filter: [o.language.slice(0, 2)] } : {}),
            ...(q.allowedDomains.length ? { search_domain_filter: q.allowedDomains.slice(0, 20) } : {}),
          },
        };
      default:
        throw new ApiError("internal", `Unknown search provider type ${this.cfg.type}`);
    }
  }

  private parse(body: Json): SearchHit[] {
    const hit = (title: unknown, url: unknown, snippet: unknown, published?: unknown, favicon?: unknown): SearchHit => {
      const u = typeof url === "string" ? url : "";
      const out: SearchHit = { title: str(title).trim() || u, url: u, snippet: str(snippet).trim() };
      const at = typeof published === "string" ? toRfc3339(published) : undefined;
      if (at) out.publishedAt = at;
      if (typeof favicon === "string" && /^https:\/\//i.test(favicon)) out.favicon = favicon;
      return out;
    };
    switch (this.cfg.type) {
      case "brave":
        return list(body.web?.results).map((r) => hit(r.title, r.url, stripTags(str(r.description))));
      case "tavily":
        return list(body.results).map((r) => hit(r.title, r.url, r.content, r.published_date, r.favicon));
      case "exa":
        return list(body.results).map((r) => {
          const highlights = Array.isArray(r.highlights) ? r.highlights.filter((h: unknown) => typeof h === "string").join(" … ") : "";
          return hit(r.title, r.url, highlights || str(r.text).slice(0, 400), r.publishedDate, r.favicon);
        });
      case "kagi":
        return list(body.data?.search).map((r) => hit(r.title, r.url, r.snippet, r.time));
      case "perplexity":
        return list(body.results).map((r) => hit(r.title, r.url, r.snippet, r.date));
      default:
        return [];
    }
  }

  private async fetchJson(req: Built, outer?: AbortSignal): Promise<Json> {
    const name = this.displayName;
    const timeout = AbortSignal.timeout(this.cfg.timeoutMs ?? DEFAULT_TIMEOUT_MS);
    const signal = outer ? AbortSignal.any([outer, timeout]) : timeout;
    const scrub = (text: string) => redactSecrets(text, { apiKey: this.cfg.apiKey, headers: {} });
    let res;
    try {
      res = await guardedFetch(req.url, {
        method: req.method,
        headers: req.headers,
        ...(req.body ? { body: JSON.stringify(req.body) } : {}),
        signal,
        allowPrivateNetwork: this.cfg.allowPrivateNetwork,
      });
    } catch (err) {
      if (err instanceof ApiError) throw new ApiError(err.type, scrub(err.message), { upstreamStatus: err.upstreamStatus });
      if (timeout.aborted) throw new ApiError("upstream", `${name} did not answer in time`);
      throw new ApiError("upstream", scrub(`${name} is unreachable: ${(err as Error).message}`));
    }
    if (!res.ok) {
      const text = await res.text().catch(() => "");
      const detail = errorDetail(text);
      const suffix = detail ? `: ${scrub(detail)}` : "";
      const status = res.status;
      if (status === 401 || status === 403) throw new ApiError("upstream", `${name} rejected the API key (HTTP ${status})${suffix}`, { upstreamStatus: status });
      if (status === 402 || status === 432 || status === 433) {
        throw new ApiError("quota_exceeded", `${name}: the plan's credits or limits are used up (HTTP ${status})${suffix}`, { upstreamStatus: status });
      }
      if (status === 429) {
        const after = Number(res.headers.get("retry-after"));
        throw new ApiError("rate_limit", `${name} is rate limiting requests`, { upstreamStatus: status, retryAfterMs: Number.isFinite(after) && after > 0 ? after * 1000 : 5_000 });
      }
      throw new ApiError("upstream", `${name} returned HTTP ${status}${suffix}`, { upstreamStatus: status });
    }
    try {
      return (await res.json()) as Json;
    } catch {
      throw new ApiError("upstream", `${name} returned a response that is not JSON`);
    }
  }
}

function list(v: unknown): Json[] {
  return Array.isArray(v) ? v.filter((x): x is Json => !!x && typeof x === "object") : [];
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

/** Brave marks query terms with <strong>; the tool wants plain text. */
function stripTags(s: string): string {
  return s.replace(/<\/?[a-z][^>]*>/gi, "");
}

/** A short, human part of an upstream error body ({error}, {message}, {detail}), at most 200 characters. */
function errorDetail(text: string): string {
  let msg = "";
  try {
    const j = JSON.parse(text) as Json;
    const pick = (v: unknown): string => {
      if (typeof v === "string") return v;
      if (Array.isArray(v)) return v.map(pick).filter(Boolean).join("; ");
      if (v && typeof v === "object") return pick((v as Json).message ?? (v as Json).error ?? (v as Json).msg ?? (v as Json).detail);
      return "";
    };
    msg = pick(j.error ?? j.message ?? j.detail);
  } catch {
    msg = /^\s*</.test(text) ? "" : text;
  }
  msg = msg.replace(/\s+/g, " ").trim();
  return msg.length > 200 ? `${msg.slice(0, 200)}…` : msg;
}

export function toRfc3339(value: string): string | undefined {
  // A naive timestamp is UTC, not server-local time.
  const zoned = /([zZ]|[+-]\d{2}:?\d{2})$/.test(value) || !value.includes("T") ? value : `${value}Z`;
  const d = new Date(zoned);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}
