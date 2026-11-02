/** Connect implementation of harness.search.v1.SearchService: proto <-> src/search/service.ts. */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { SearchService } from "../gen/harness/search/v1/search_pb.ts";
import { searchRegistry } from "../search/service.ts";
import { checkClientVersion, requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

export function registerSearchService(router: ConnectRouter): void {
  router.service(SearchService, {
    search: guard(async (req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      await requirePrincipal(ctx);
      const out = await searchRegistry().search({
        query: req.query,
        allowedDomains: req.allowedDomains,
        blockedDomains: req.blockedDomains,
        maxResults: req.maxResults,
        provider: req.provider,
        signal: ctx.signal,
      });
      return {
        provider: out.provider,
        results: out.results.map((r) => ({ title: r.title, url: r.url, snippet: r.snippet, publishedAt: r.publishedAt ?? "" })),
      };
    }),
    listProviders: guard(async (_req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      await requirePrincipal(ctx);
      const reg = searchRegistry();
      const providers = await reg.status(ctx.signal);
      return { defaultProvider: reg.defaultProvider, providers };
    }),
  });
}
