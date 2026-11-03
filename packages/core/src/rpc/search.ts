/**
 * Connect implementation of harness.search.v1.SearchService: proto <-> src/search/resolve.ts. Each query runs on
 * the caller's effective search provider (their own choice, else the server default).
 */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { SearchService } from "../gen/harness/search/v1/search_pb.ts";
import { searchForUser, searchStatusForUser } from "../search/resolve.ts";
import { checkClientVersion, requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

export function registerSearchService(router: ConnectRouter): void {
  router.service(SearchService, {
    search: guard(async (req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      const { user } = await requirePrincipal(ctx);
      const out = await searchForUser(user, {
        query: req.query,
        allowedDomains: req.allowedDomains,
        blockedDomains: req.blockedDomains,
        maxResults: req.maxResults,
        provider: req.provider,
        signal: ctx.signal,
      });
      return {
        provider: out.provider,
        results: out.results.map((r) => ({ title: r.title, url: r.url, snippet: r.snippet, publishedAt: r.publishedAt ?? "", faviconUrl: r.favicon ?? "" })),
      };
    }),
    listProviders: guard(async (_req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      const { user } = await requirePrincipal(ctx);
      return searchStatusForUser(user, ctx.signal);
    }),
  });
}
