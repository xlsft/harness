/** Connect implementation of harness.chat.v1.ChatService: proto <-> src/chat/app.ts. */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { ChatProvider, ChatService } from "../gen/harness/chat/v1/chat_pb.ts";
import { deleteChatApp, deleteMyBot, getChatApp, getMyBot, listMyIdentities, putChatApp, putMyBot, startLink, unlinkIdentity, type IdentityRow } from "../chat/app.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

/** Unspecified means Slack, the first provider. */
const providerOf = (p: ChatProvider) => (p === ChatProvider.UNSPECIFIED ? ChatProvider.SLACK : p);
/** Personal bots exist for Telegram only: unspecified means Telegram there. */
const botProviderOf = (p: ChatProvider) => (p === ChatProvider.UNSPECIFIED ? ChatProvider.TELEGRAM : p);

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
        app: await putChatApp(p, {
          provider: providerOf(req.provider),
          botToken: req.botToken,
          signingSecret: req.signingSecret,
          enabled: req.enabled,
          matchByEmail: req.matchByEmail,
          delivery: req.delivery,
        }),
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
      return { identities: r.identities.map(identityToProto), available: r.available, availableProviders: r.providers, telegramBotUsername: r.telegramBotUsername };
    }),
    startChatLink: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await startLink(p, providerOf(req.provider), req.personalBot);
      return { code: r.code, expiresAt: r.expiresAt.toISOString(), deepLink: r.deepLink };
    }),
    unlinkChatIdentity: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await unlinkIdentity(p, providerOf(req.provider), req.teamId, req.externalUserId);
      return {};
    }),
    getMyChatBot: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return getMyBot(p, botProviderOf(req.provider));
    }),
    putMyChatBot: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { bot: await putMyBot(p, { provider: botProviderOf(req.provider), botToken: req.botToken, enabled: req.enabled, delivery: req.delivery }) };
    }),
    deleteMyChatBot: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await deleteMyBot(p, botProviderOf(req.provider));
      return {};
    }),
  });
}
