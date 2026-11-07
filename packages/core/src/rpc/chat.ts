/** Connect implementation of harness.chat.v1.ChatService: proto <-> src/chat/app.ts. */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { ChatProvider, ChatService } from "../gen/harness/chat/v1/chat_pb.ts";
import { deleteChatApp, getChatApp, listMyIdentities, putChatApp, startLink, unlinkIdentity, type IdentityRow } from "../chat/app.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

/** Unspecified means Slack, the only provider so far. */
const providerOf = (p: ChatProvider) => (p === ChatProvider.UNSPECIFIED ? ChatProvider.SLACK : p);

const identityToProto = (r: IdentityRow) => ({
  provider: r.provider,
  teamId: r.teamId,
  externalUserId: r.externalUserId,
  displayName: r.displayName,
  linkedVia: r.linkedVia,
  linkedAt: r.linkedAt.toISOString(),
});

export function registerChatService(router: ConnectRouter): void {
  router.service(ChatService, {
    getChatApp: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { app: await getChatApp(p, providerOf(req.provider)) };
    }),
    putChatApp: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return {
        app: await putChatApp(p, { provider: providerOf(req.provider), botToken: req.botToken, signingSecret: req.signingSecret, enabled: req.enabled, matchByEmail: req.matchByEmail }),
      };
    }),
    deleteChatApp: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await deleteChatApp(p, providerOf(req.provider));
      return {};
    }),
    listMyChatIdentities: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await listMyIdentities(p);
      return { identities: r.identities.map(identityToProto), available: r.available };
    }),
    startChatLink: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await startLink(p, providerOf(req.provider));
      return { code: r.code, expiresAt: r.expiresAt.toISOString() };
    }),
    unlinkChatIdentity: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await unlinkIdentity(p, providerOf(req.provider), req.teamId, req.externalUserId);
      return {};
    }),
  });
}
