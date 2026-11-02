/**
 * Web search providers behind harness.search.v1.SearchService (the WebSearch tool).
 *
 * A provider turns a query into plain results. Domain filters are applied by the service after the
 * provider returns, so providers only need to support them as a best-effort hint.
 */

export interface SearchQuery {
  query: string;
  /** Suffix-matched; at most one of allowedDomains / blockedDomains is non-empty. */
  allowedDomains: string[];
  blockedDomains: string[];
  maxResults: number;
  signal?: AbortSignal;
}

export interface SearchHit {
  title: string;
  url: string;
  snippet: string;
  /** RFC 3339, when known. */
  publishedAt?: string;
}

export interface SearchProvider {
  readonly id: string;
  readonly displayName: string;
  search(q: SearchQuery): Promise<SearchHit[]>;
  /** Cheap liveness check for status pages; must not throw. */
  healthy(signal?: AbortSignal): Promise<boolean>;
}
