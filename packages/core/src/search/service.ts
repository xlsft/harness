/**
 * Transport-agnostic search service behind harness.search.v1.SearchService.
 *
 * Providers are registered here; SearXNG is the default and, for now, the only one. Configuration:
 *   HARNESS_SEARXNG_URL       SearXNG base URL (default http://127.0.0.1:8888, the in-container instance)
 *   HARNESS_SEARCH_PROVIDER   default provider id (default "searxng")
 */
import { ApiError } from "../lib/errors.ts";
import { SearxngProvider } from "./searxng.ts";
import type { SearchHit, SearchProvider } from "./types.ts";

export const DEFAULT_MAX_RESULTS = 10;
export const MAX_RESULTS_CAP = 25;
const MAX_QUERY_LENGTH = 500;

export interface SearchInput {
  query: string;
  allowedDomains?: string[];
  blockedDomains?: string[];
  maxResults?: number;
  provider?: string;
  signal?: AbortSignal;
}

export interface SearchOutput {
  provider: string;
  results: SearchHit[];
}

export interface ProviderStatus {
  id: string;
  displayName: string;
  healthy: boolean;
}

export class SearchRegistry {
  private readonly providers = new Map<string, SearchProvider>();

  constructor(
    providers: SearchProvider[],
    readonly defaultProvider: string,
  ) {
    for (const p of providers) this.providers.set(p.id, p);
    if (!this.providers.has(defaultProvider)) throw new Error(`default search provider "${defaultProvider}" is not registered`);
  }

  get(id?: string): SearchProvider {
    const p = this.providers.get(id || this.defaultProvider);
    if (!p) throw new ApiError("invalid_request", `Unknown search provider "${id}"`);
    return p;
  }

  async search(input: SearchInput): Promise<SearchOutput> {
    const query = input.query.trim();
    if (!query) throw new ApiError("invalid_request", "query is required");
    if (query.length > MAX_QUERY_LENGTH) throw new ApiError("invalid_request", `query is longer than ${MAX_QUERY_LENGTH} characters`);
    const allowed = normalizeDomains(input.allowedDomains);
    const blocked = normalizeDomains(input.blockedDomains);
    if (allowed.length && blocked.length) {
      throw new ApiError("invalid_request", "allowed_domains and blocked_domains cannot be combined");
    }
    const max = clampResults(input.maxResults);
    const provider = this.get(input.provider);
    const hits = await provider.search({ query, allowedDomains: allowed, blockedDomains: blocked, maxResults: max, signal: input.signal });
    return { provider: provider.id, results: filterAndDedupe(hits, allowed, blocked).slice(0, max) };
  }

  async status(signal?: AbortSignal): Promise<ProviderStatus[]> {
    return Promise.all(
      [...this.providers.values()].map(async (p) => ({ id: p.id, displayName: p.displayName, healthy: await p.healthy(signal) })),
    );
  }
}

let registry: SearchRegistry | undefined;

/** Process-wide registry built from environment variables. */
export function searchRegistry(): SearchRegistry {
  registry ??= new SearchRegistry(
    [new SearxngProvider({ baseUrl: process.env.HARNESS_SEARXNG_URL || "http://127.0.0.1:8888" })],
    process.env.HARNESS_SEARCH_PROVIDER || "searxng",
  );
  return registry;
}

/** For tests. */
export function resetSearchRegistry(r?: SearchRegistry): void {
  registry = r;
}

function clampResults(n: number | undefined): number {
  if (!n || n <= 0) return DEFAULT_MAX_RESULTS;
  return Math.min(n, MAX_RESULTS_CAP);
}

export function normalizeDomains(domains: string[] | undefined): string[] {
  const out = new Set<string>();
  for (const raw of domains ?? []) {
    let d = raw.trim().toLowerCase();
    if (!d) continue;
    d = d.replace(/^[a-z][a-z0-9+.-]*:\/\//, "").replace(/\/.*$/, "").replace(/^\*\./, "").replace(/^\.+|\.+$/g, "");
    if (!/^[a-z0-9.-]+$/.test(d)) throw new ApiError("invalid_request", `Invalid domain "${raw}"`);
    out.add(d);
  }
  return [...out];
}

function hostMatches(host: string, domain: string): boolean {
  return host === domain || host.endsWith(`.${domain}`);
}

export function filterAndDedupe(hits: SearchHit[], allowed: string[], blocked: string[]): SearchHit[] {
  const seen = new Set<string>();
  const out: SearchHit[] = [];
  for (const h of hits) {
    let url: URL;
    try {
      url = new URL(h.url);
    } catch {
      continue;
    }
    const host = url.hostname.toLowerCase();
    if (allowed.length && !allowed.some((d) => hostMatches(host, d))) continue;
    if (blocked.some((d) => hostMatches(host, d))) continue;
    url.hash = "";
    const key = url.toString();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(h);
  }
  return out;
}
