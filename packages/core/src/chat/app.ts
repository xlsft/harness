/**
 * The connected chat app (admin) and the people linked to it (each user, for themselves). Secrets are write-only and
 * stored as one AES-256-GCM blob per app (HKDF purpose "chat-secrets", AAD "chat:<provider>:secrets").
 */
import { randomBytes } from "node:crypto";
import { and, eq, lt } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { ChatProvider as ChatProviderId } from "../gen/harness/chat/v1/chat_pb.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { sha256 } from "../lib/ids.ts";
import { serverPolicyFor } from "../services/managed-policy.ts";
import type { Principal, PublicUser } from "../services/devices.ts";
import { slack } from "./slack.ts";
import type { ChatProvider } from "./types.ts";

export type AppRow = typeof schema.chatApp.$inferSelect;
export type IdentityRow = typeof schema.chatIdentity.$inferSelect;

export interface AppSecrets {
  botToken?: string;
  signingSecret?: string;
}

const LINK_CODE_TTL_MS = 15 * 60_000;

const key = () => deriveKey("chat-secrets");
const aad = (provider: number) => `chat:${provider}:secrets`;

export function providerOf(id: number): ChatProvider {
  if (id === ChatProviderId.SLACK) return slack;
  throw new ApiError("invalid_request", "Only Slack is supported");
}

export function appSecrets(row: AppRow): AppSecrets {
  if (!row.secretsEnc) return {};
  return JSON.parse(decryptSecret(row.secretsEnc, aad(row.provider), key())) as AppSecrets;
}

function requireAdmin(p: Principal): void {
  if (p.user.role !== "admin") throw new ApiError("permission", "Only admins can manage chat apps");
}

export async function getAppRow(provider: number): Promise<AppRow | undefined> {
  await ensureDb();
  return getDb().query.chatApp.findFirst({ where: eq(schema.chatApp.provider, provider) });
}

export function requestUrl(provider: number): string {
  return `${config().publicUrl}/api/chat/${providerOf(provider).name.toLowerCase()}/events`;
}

export async function chatAppsDisabledByPolicy(role: PublicUser["role"]): Promise<boolean> {
  return (await serverPolicyFor({ role })).chatAppsDisabled;
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
}

async function view(provider: number, row: AppRow | undefined): Promise<AppView> {
  return {
    provider,
    configured: !!row?.secretsEnc,
    enabled: row?.enabled ?? false,
    teamId: row?.teamId ?? "",
    teamName: row?.teamName ?? "",
    botUserId: row?.botUserId ?? "",
    requestUrl: requestUrl(provider),
    matchByEmail: row?.matchByEmail ?? true,
    lastEventAt: row?.lastEventAt?.toISOString() ?? "",
    lastError: row?.lastError ?? "",
    disabledByPolicy: await chatAppsDisabledByPolicy("user"),
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
}

/** Saves the app; a new or changed token is checked with the provider first. */
export async function putChatApp(p: Principal, input: PutAppInput): Promise<AppView> {
  requireAdmin(p);
  const prov = providerOf(input.provider);
  const cur = await getAppRow(input.provider);
  const secrets: AppSecrets = cur ? appSecrets(cur) : {};
  const token = input.botToken.trim();
  const signing = input.signingSecret.trim();
  if (token) secrets.botToken = token;
  if (signing) secrets.signingSecret = signing;
  if (!secrets.botToken || !secrets.signingSecret) throw new ApiError("invalid_request", "A bot token and a signing secret are required");
  let who = { teamId: cur?.teamId ?? "", teamName: cur?.teamName ?? "", botUserId: cur?.botUserId ?? "" };
  if (token || !cur?.teamId) who = await prov.client(secrets as Record<string, string>).whoami();
  const now = new Date();
  const fields = {
    enabled: input.enabled,
    matchByEmail: input.matchByEmail,
    teamId: who.teamId,
    teamName: who.teamName,
    botUserId: who.botUserId,
    secretsEnc: encryptSecret(JSON.stringify(secrets), aad(input.provider), key()),
    lastError: "",
    updatedAt: now,
    updatedBy: p.user.id,
  };
  await getDb()
    .insert(schema.chatApp)
    .values({ provider: input.provider, createdAt: now, ...fields })
    .onConflictDoUpdate({ target: schema.chatApp.provider, set: fields });
  return view(input.provider, await getAppRow(input.provider));
}

export async function deleteChatApp(p: Principal, provider: number): Promise<void> {
  requireAdmin(p);
  providerOf(provider);
  await ensureDb();
  await getDb().delete(schema.chatApp).where(eq(schema.chatApp.provider, provider));
}

export async function noteAppEvent(provider: number, error = ""): Promise<void> {
  await getDb()
    .update(schema.chatApp)
    .set(error ? { lastError: error.slice(0, 300) } : { lastEventAt: new Date(), lastError: "" })
    .where(eq(schema.chatApp.provider, provider));
}

// --- identities ----------------------------------------------------------------------------------

export async function listMyIdentities(p: Principal): Promise<{ identities: IdentityRow[]; available: boolean }> {
  await ensureDb();
  const identities = await getDb().select().from(schema.chatIdentity).where(eq(schema.chatIdentity.userId, p.user.id));
  const app = await getAppRow(ChatProviderId.SLACK);
  const available = !!app?.secretsEnc && app.enabled && !(await chatAppsDisabledByPolicy(p.user.role));
  return { identities, available };
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

export async function startLink(p: Principal, provider: number): Promise<{ code: string; expiresAt: Date }> {
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
  return { code, expiresAt };
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
