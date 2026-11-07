/** Connect implementation of harness.relay.v1.InboxService: proto <-> src/relay/inbox.ts. */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { InboxService } from "../gen/harness/relay/v1/inbox_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { listInbox, markInboxRead, watchInbox } from "../relay/inbox.ts";
import { requirePrincipal } from "./context.ts";
import { guard, toConnectError } from "./errors.ts";

export function registerInboxService(router: ConnectRouter): void {
  router.service(InboxService, {
    listInbox: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      if (!req.request) throw new ApiError("invalid_request", "request is required");
      return { response: await listInbox(p, req.request) };
    }),

    async *watchInbox(_req, ctx: HandlerContext) {
      try {
        const p = await requirePrincipal(ctx);
        for await (const response of watchInbox(p, ctx.signal)) yield { response };
      } catch (err) {
        throw toConnectError(err);
      }
    },

    markInboxRead: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await markInboxRead(p, req.request?.sessionIds ?? [], req.request?.read ?? true);
      return {};
    }),
  });
}
