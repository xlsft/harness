/**
 * The connected chat apps (admin), the bots people bring for themselves (each owner only), and the people linked to them
 * (each user, for themselves). Secrets are write-only and stored as one AES-256-GCM blob per app or bot (HKDF purpose
 * "chat-secrets", AAD "chat:<provider>:secrets" for the organization's app, "chat:<provider>:bot:<id>" for a personal bot).
 */
import { need } from "../auth/roles.ts";
import { randomBytes } from "node:crypto";
import { and, eq, lt } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { ChatDelivery, ChatProvider as ChatProviderId } from "../gen/harness/chat/v1/chat_pb.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { newId, sha256 } from "../lib/ids.ts";
import { serverPolicyFor } from "../services/managed-policy.ts";
import type { Principal, PublicUser } from "../services/devices.ts";
import { slack } from "./slack.ts";
import { telegram, validToken } from "./telegram.ts";
import type { ChatProvider } from "./types.ts";

export type AppRow = typeof schema.chatApp.$inferSelect;
export type BotRow = typeof schema.chatBot.$inferSelect;
export type IdentityRow = typeof schema.chatIdentity.$inferSelect;

export interface AppSecrets {
  botToken?: string;
  /** Slack: the app's signing secret. Telegram: the webhook's secret token, made here. */
  signingSecret?: string;
}

/** The app key of the organization's app in routes ("" in chat_thread.app_key). */
export const ORG_KEY = "org";

/** An app or a personal bot, as the service uses it. */
export interface ChatAppCtx {
  provider: number;
  /** "" for the organization's app, else the chat_bot id. */
  key: string;
  /** The personal bot's owner; "" for the organization's app. */
  ownerUserId: string;
  enabled: boolean;
  matchByEmail: boolean;
  teamId: string;
  botUserId: string;
  delivery: number;
  secrets: AppSecrets;
}

const LINK_CODE_TTL_MS = 15 * 60_000;

const key = () => deriveKey("chat-secrets");
const aad = (provider: number) => `chat:${provider}:secrets`;
const botAad = (provider: number, id: string) => `chat:${provider}:bot:${id}`;

export function providerOf(id: number): ChatProvider {
  if (id === ChatProviderId.SLACK) return slack;
  if (id === ChatProviderId.TELEGRAM) return telegram;
  throw new ApiError("invalid_request", "Only Slack and Telegram are supported");
}

export function appSecrets(row: AppRow): AppSecrets {
  if (!row.secretsEnc) return {};
  return JSON.parse(decryptSecret(row.secretsEnc, aad(row.provider), key())) as AppSecrets;
}

function botSecrets(row: BotRow): AppSecrets {
  return JSON.parse(decryptSecret(row.secretsEnc, botAad(row.provider, row.id), key())) as AppSecrets;
}

function requireAdmin(p: Principal): void {
  need(p.user, "connections.write");
}

export async function getAppRow(provider: number): Promise<AppRow | undefined> {
  await ensureDb();
  return getDb().query.chatApp.findFirst({ where: eq(schema.chatApp.provider, provider) });
}

async function getBotRow(id: string): Promise<BotRow | undefined> {
  await ensureDb();
  return getDb().query.chatBot.findFirst({ where: eq(schema.chatBot.id, id) });
}

async function myBotRow(userId: string, provider: number): Promise<BotRow | undefined> {
  await ensureDb();
  return getDb().query.chatBot.findFirst({ where: and(eq(schema.chatBot.userId, userId), eq(schema.chatBot.provider, provider)) });
}

/** The organization's app ("" or "org") or a personal bot (its id), with its secrets. */
export async function loadApp(provider: number, appKey: string): Promise<ChatAppCtx | undefined> {
  if (!appKey || appKey === ORG_KEY) {
    const row = await getAppRow(provider);
    if (!row?.secretsEnc) return undefined;
    return {
      provider,
      key: "",
      ownerUserId: "",
      enabled: row.enabled,
      matchByEmail: row.matchByEmail,
      teamId: row.teamId,
      botUserId: row.botUserId,
      delivery: row.delivery,
      secrets: appSecrets(row),
    };
  }
  const bot = await getBotRow(appKey);
  if (!bot || bot.provider !== provider) return undefined;
  return {
    provider,
    key: bot.id,
    ownerUserId: bot.userId,
    enabled: bot.enabled,
    // A personal bot is its owner's: nobody is linked to it by email.
    matchByEmail: false,
    teamId: bot.botUserId,
    botUserId: bot.botUserId,
    delivery: bot.delivery,
    secrets: botSecrets(bot),
  };
}

/** Every Telegram app and bot (server start). Off ones keep receiving updates, to answer that they are off. */
export async function telegramApps(): Promise<ChatAppCtx[]> {
  await ensureDb();
  const out: ChatAppCtx[] = [];
  const org = await loadApp(ChatProviderId.TELEGRAM, "");
  if (org) out.push(org);
  const bots = await getDb().select().from(schema.chatBot).where(eq(schema.chatBot.provider, ChatProviderId.TELEGRAM));
  for (const b of bots) {
    const ctx = await loadApp(ChatProviderId.TELEGRAM, b.id).catch(() => undefined);
    if (ctx) out.push(ctx);
  }
  return out;
}

export function requestUrl(provider: number, appKey = ""): string {
  if (provider === ChatProviderId.TELEGRAM) return `${config().publicUrl}/api/chat/telegram/webhook/${appKey || ORG_KEY}`;
  return `${config().publicUrl}/api/chat/${providerOf(provider).name.toLowerCase()}/events`;
}

/** What a Telegram bot uses: the choice, or a webhook exactly when Telegram can reach the server (HTTPS). */
export function activeDelivery(chosen: number): ChatDelivery {
  if (chosen === ChatDelivery.WEBHOOK || chosen === ChatDelivery.POLLING) return chosen;
  return config().publicUrl.startsWith("https://") ? ChatDelivery.WEBHOOK : ChatDelivery.POLLING;
}

export async function chatAppsDisabledByPolicy(role: PublicUser["role"]): Promise<boolean> {
  return (await serverPolicyFor({ role })).chatAppsDisabled;
}

export async function personalBotsDisabledByPolicy(role: PublicUser["role"]): Promise<boolean> {
  const p = await serverPolicyFor({ role });
  return p.chatAppsDisabled || p.personalChatBotsDisabled;
}

/** Starts or stops what feeds a Telegram app or bot its updates. Loaded lazily: the delivery module needs the service. */
async function syncDelivery(provider: number, appKey: string, removed?: AppSecrets): Promise<void> {
  if (provider !== ChatProviderId.TELEGRAM) return;
  const d = await import("./telegram-delivery.ts");
  await d.syncTelegramDelivery(appKey, removed);
}

export interface AppView {
  provider: number;
  configured: boolean;
  enabled: boolean;
  teamId: string;
  teamName: string;
  botUserId: string;
  requestUrl: string;
  matchByEmail: boolean;
  lastEventAt: string;
  lastError: string;
  disabledByPolicy: boolean;
  delivery: number;
  activeDelivery: number;
}

async function view(provider: number, row: AppRow | undefined): Promise<AppView> {
  const telegramApp = provider === ChatProviderId.TELEGRAM;
  return {
    provider,
    configured: !!row?.secretsEnc,
    enabled: row?.enabled ?? false,
    teamId: row?.teamId ?? "",
    teamName: row?.teamName ?? "",
    botUserId: row?.botUserId ?? "",
    requestUrl: requestUrl(provider),
    matchByEmail: row?.matchByEmail ?? !telegramApp,
    lastEventAt: row?.lastEventAt?.toISOString() ?? "",
    lastError: row?.lastError ?? "",
    disabledByPolicy: await chatAppsDisabledByPolicy("user"),
    delivery: telegramApp ? (row?.delivery ?? ChatDelivery.UNSPECIFIED) : ChatDelivery.UNSPECIFIED,
    activeDelivery: telegramApp && row?.secretsEnc ? activeDelivery(row.delivery) : ChatDelivery.UNSPECIFIED,
  };
}

export async function getChatApp(p: Principal, provider: number): Promise<AppView> {
  requireAdmin(p);
  providerOf(provider);
  return view(provider, await getAppRow(provider));
}

export interface PutAppInput {
  provider: number;
  botToken: string;
  signingSecret: string;
  enabled: boolean;
  matchByEmail: boolean;
  delivery?: number;
}

const newWebhookSecret = () => randomBytes(32).toString("hex");

/** Saves the app; a new or changed token is checked with the provider first. */
export async function putChatApp(p: Principal, input: PutAppInput): Promise<AppView> {
  requireAdmin(p);
  const prov = providerOf(input.provider);
  const isTelegram = input.provider === ChatProviderId.TELEGRAM;
  const cur = await getAppRow(input.provider);
  const secrets: AppSecrets = cur ? appSecrets(cur) : {};
  const token = input.botToken.trim();
  const signing = input.signingSecret.trim();
  if (token && isTelegram && !validToken(token)) throw new ApiError("invalid_request", "That is not a Telegram bot token (it looks like 123456:ABC-DEF...)");
  if (token) secrets.botToken = token;
  if (isTelegram) {
    // The webhook's secret token is the server's own; a new bot token gets a new one.
    if (token || !secrets.signingSecret) secrets.signingSecret = newWebhookSecret();
  } else if (signing) secrets.signingSecret = signing;
  if (!secrets.botToken || !secrets.signingSecret) throw new ApiError("invalid_request", isTelegram ? "A bot token is required" : "A bot token and a signing secret are required");
  let who = { teamId: cur?.teamId ?? "", teamName: cur?.teamName ?? "", botUserId: cur?.botUserId ?? "" };
  if (token || !cur?.teamId) who = await prov.client(secrets as Record<string, string>).whoami();
  if (isTelegram) {
    const taken = await getDb().query.chatBot.findFirst({ where: and(eq(schema.chatBot.provider, input.provider), eq(schema.chatBot.botUserId, who.botUserId)) });
    if (taken) throw new ApiError("conflict", "Someone connected this bot as their own; use another bot for the organization");
  }
  const now = new Date();
  const fields = {
    enabled: input.enabled,
    matchByEmail: isTelegram ? false : input.matchByEmail,
    teamId: who.teamId,
    teamName: who.teamName,
    botUserId: who.botUserId,
    secretsEnc: encryptSecret(JSON.stringify(secrets), aad(input.provider), key()),
    delivery: isTelegram ? (input.delivery ?? ChatDelivery.UNSPECIFIED) : ChatDelivery.UNSPECIFIED,
    lastError: "",
    updatedAt: now,
    updatedBy: p.user.id,
  };
  await getDb()
    .insert(schema.chatApp)
    .values({ provider: input.provider, createdAt: now, ...fields })
    .onConflictDoUpdate({ target: schema.chatApp.provider, set: fields });
  await syncDelivery(input.provider, "");
  return view(input.provider, await getAppRow(input.provider));
}

export async function deleteChatApp(p: Principal, provider: number): Promise<void> {
  requireAdmin(p);
  providerOf(provider);
  await ensureDb();
  const rows = await getDb().delete(schema.chatApp).where(eq(schema.chatApp.provider, provider)).returning();
  if (rows[0]?.secretsEnc) await syncDelivery(provider, "", appSecrets(rows[0])).catch(() => {});
}

export async function noteAppEvent(provider: number, error = "", appKey = ""): Promise<void> {
  const set = error ? { lastError: error.slice(0, 300) } : { lastEventAt: new Date(), lastError: "" };
  if (appKey && appKey !== ORG_KEY) await getDb().update(schema.chatBot).set(set).where(eq(schema.chatBot.id, appKey));
  else await getDb().update(schema.chatApp).set(set).where(eq(schema.chatApp.provider, provider));
}

// --- personal bots -------------------------------------------------------------------------------

export interface BotView {
  provider: number;
  id: string;
  enabled: boolean;
  botUserId: string;
  botUsername: string;
  delivery: number;
  activeDelivery: number;
  requestUrl: string;
  lastEventAt: string;
  lastError: string;
}

function botView(b: BotRow): BotView {
  return {
    provider: b.provider,
    id: b.id,
    enabled: b.enabled,
    botUserId: b.botUserId,
    botUsername: b.botUsername,
    delivery: b.delivery,
    activeDelivery: activeDelivery(b.delivery),
    requestUrl: requestUrl(b.provider, b.id),
    lastEventAt: b.lastEventAt?.toISOString() ?? "",
    lastError: b.lastError,
  };
}

function requireTelegram(provider: number): void {
  if (provider !== ChatProviderId.TELEGRAM) throw new ApiError("invalid_request", "Only Telegram bots can be personal");
}

export async function getMyBot(p: Principal, provider: number): Promise<{ bot?: BotView; allowed: boolean }> {
  requireTelegram(provider);
  const row = await myBotRow(p.user.id, provider);
  return { bot: row ? botView(row) : undefined, allowed: !(await personalBotsDisabledByPolicy(p.user.role)) };
}

export async function putMyBot(p: Principal, input: { provider: number; botToken: string; enabled: boolean; delivery: number }): Promise<BotView> {
  requireTelegram(input.provider);
  if (await personalBotsDisabledByPolicy(p.user.role)) throw new ApiError("permission", "Your organization's policy turned personal chat bots off");
  const cur = await myBotRow(p.user.id, input.provider);
  const token = input.botToken.trim();
  if (!cur && !token) throw new ApiError("invalid_request", "A bot token is required");
  if (token && !validToken(token)) throw new ApiError("invalid_request", "That is not a Telegram bot token (it looks like 123456:ABC-DEF...)");
  const secrets: AppSecrets = cur ? botSecrets(cur) : {};
  let who = { botUserId: cur?.botUserId ?? "", username: cur?.botUsername ?? "" };
  if (token) {
    secrets.botToken = token;
    secrets.signingSecret = newWebhookSecret();
    const w = await providerOf(input.provider).client(secrets as Record<string, string>).whoami();
    who = { botUserId: w.botUserId, username: w.teamName };
    const org = await getAppRow(input.provider);
    if (org?.botUserId === who.botUserId) throw new ApiError("conflict", "That is the organization's bot");
    const other = await getDb().query.chatBot.findFirst({ where: and(eq(schema.chatBot.provider, input.provider), eq(schema.chatBot.botUserId, who.botUserId)) });
    if (other && other.userId !== p.user.id) throw new ApiError("conflict", "Someone else connected this bot");
  }
  const id = cur?.id ?? newId("cbt");
  const now = new Date();
  const fields = {
    enabled: input.enabled,
    botUserId: who.botUserId,
    botUsername: who.username,
    delivery: input.delivery,
    secretsEnc: encryptSecret(JSON.stringify(secrets), botAad(input.provider, id), key()),
    lastError: "",
    updatedAt: now,
  };
  if (cur) await getDb().update(schema.chatBot).set(fields).where(eq(schema.chatBot.id, id));
  else await getDb().insert(schema.chatBot).values({ id, userId: p.user.id, provider: input.provider, createdAt: now, ...fields });
  await syncDelivery(input.provider, id);
  return botView((await getBotRow(id))!);
}

export async function deleteMyBot(p: Principal, provider: number): Promise<void> {
  requireTelegram(provider);
  const rows = await getDb()
    .delete(schema.chatBot)
    .where(and(eq(schema.chatBot.userId, p.user.id), eq(schema.chatBot.provider, provider)))
    .returning();
  const row = rows[0];
  if (!row) throw new ApiError("not_found", "You have no bot connected");
  await syncDelivery(provider, row.id, botSecrets(row)).catch(() => {});
  // Links made through the bot go with it.
  await getDb()
    .delete(schema.chatIdentity)
    .where(and(eq(schema.chatIdentity.provider, provider), eq(schema.chatIdentity.teamId, row.botUserId), eq(schema.chatIdentity.userId, p.user.id)));
}

// --- identities ----------------------------------------------------------------------------------

export async function listMyIdentities(p: Principal): Promise<{ identities: IdentityRow[]; available: boolean; providers: number[]; telegramBotUsername: string }> {
  await ensureDb();
  const identities = await getDb().select().from(schema.chatIdentity).where(eq(schema.chatIdentity.userId, p.user.id));
  const providers: number[] = [];
  let telegramBotUsername = "";
  if (!(await chatAppsDisabledByPolicy(p.user.role))) {
    const slackApp = await getAppRow(ChatProviderId.SLACK);
    if (slackApp?.secretsEnc && slackApp.enabled) providers.push(ChatProviderId.SLACK);
    const tg = await getAppRow(ChatProviderId.TELEGRAM);
    if (tg?.secretsEnc && tg.enabled) telegramBotUsername = tg.teamName;
    const mine = await myBotRow(p.user.id, ChatProviderId.TELEGRAM);
    const personal = !!mine?.enabled && !(await personalBotsDisabledByPolicy(p.user.role));
    if (telegramBotUsername || personal) providers.push(ChatProviderId.TELEGRAM);
  }
  return { identities, available: providers.length > 0, providers, telegramBotUsername };
}

/** "ABCD-EFGH": 40 bits from an alphabet without look-alikes. */
function newCode(): string {
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const bytes = randomBytes(8);
  let s = "";
  for (let i = 0; i < 8; i++) s += alphabet[bytes[i]! % alphabet.length];
  return `${s.slice(0, 4)}-${s.slice(4)}`;
}

export function normalizeCode(code: string): string {
  const c = code.toUpperCase().replace(/[^A-Z0-9]/g, "");
  return c.length === 8 ? `${c.slice(0, 4)}-${c.slice(4)}` : "";
}

export async function startLink(p: Principal, provider: number, personalBot = false): Promise<{ code: string; expiresAt: Date; deepLink: string }> {
  providerOf(provider);
  await ensureDb();
  const db = getDb();
  const now = new Date();
  await db.delete(schema.chatLinkCode).where(lt(schema.chatLinkCode.expiresAt, now));
  // One live code per person: a new one replaces the last.
  await db.delete(schema.chatLinkCode).where(and(eq(schema.chatLinkCode.userId, p.user.id), eq(schema.chatLinkCode.provider, provider)));
  const code = newCode();
  const expiresAt = new Date(now.getTime() + LINK_CODE_TTL_MS);
  await db.insert(schema.chatLinkCode).values({ codeHash: sha256(code), provider, userId: p.user.id, expiresAt, createdAt: now });
  let deepLink = "";
  if (provider === ChatProviderId.TELEGRAM) {
    const username = personalBot ? (await myBotRow(p.user.id, provider))?.botUsername : (await getAppRow(provider))?.teamName;
    if (username) deepLink = `https://t.me/${encodeURIComponent(username)}?start=${code}`;
  }
  return { code, expiresAt, deepLink };
}

/** Uses a code: single use, unexpired. Returns the account it links to. */
export async function redeemCode(provider: number, code: string): Promise<string | undefined> {
  const norm = normalizeCode(code);
  if (!norm) return undefined;
  const rows = await getDb()
    .delete(schema.chatLinkCode)
    .where(and(eq(schema.chatLinkCode.codeHash, sha256(norm)), eq(schema.chatLinkCode.provider, provider)))
    .returning();
  const row = rows[0];
  if (!row || row.expiresAt.getTime() < Date.now()) return undefined;
  return row.userId;
}

export async function linkIdentity(input: { provider: number; teamId: string; externalUserId: string; userId: string; displayName: string; via: "directory" | "code" }): Promise<void> {
  const now = new Date();
  const fields = { userId: input.userId, displayName: input.displayName.slice(0, 200), linkedVia: input.via, linkedAt: now };
  await getDb()
    .insert(schema.chatIdentity)
    .values({ provider: input.provider, teamId: input.teamId, externalUserId: input.externalUserId, ...fields })
    .onConflictDoUpdate({ target: [schema.chatIdentity.provider, schema.chatIdentity.teamId, schema.chatIdentity.externalUserId], set: fields });
}

export async function identityOf(provider: number, teamId: string, externalUserId: string): Promise<IdentityRow | undefined> {
  return getDb().query.chatIdentity.findFirst({
    where: and(eq(schema.chatIdentity.provider, provider), eq(schema.chatIdentity.teamId, teamId), eq(schema.chatIdentity.externalUserId, externalUserId)),
  });
}

export async function unlinkIdentity(p: Principal, provider: number, teamId: string, externalUserId: string): Promise<void> {
  await ensureDb();
  const rows = await getDb()
    .delete(schema.chatIdentity)
    .where(
      and(
        eq(schema.chatIdentity.provider, provider),
        eq(schema.chatIdentity.teamId, teamId),
        eq(schema.chatIdentity.externalUserId, externalUserId),
        eq(schema.chatIdentity.userId, p.user.id),
      ),
    )
    .returning();
  if (!rows.length) throw new ApiError("not_found", "No such linked identity");
}
