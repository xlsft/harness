/**
 * Server provider connections (VcsService admin RPCs). Admin-only; secrets are write-only and stored as one
 * AES-256-GCM blob per connection (HKDF purpose "vcs-secrets", AAD = connection id).
 */
import { asc, eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { ProviderType } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { assertResolvesPublic, assertUrlAllowed } from "../lib/ssrf.ts";
import type { Principal } from "../services/devices.ts";
import { hostOfBaseUrl } from "./util.ts";

export type ConnectionRow = typeof schema.vcsConnection.$inferSelect;

export interface ConnSecrets {
  privateKeyPem?: string;
  clientSecret?: string;
  webhookSecret?: string;
  accessToken?: string;
}

/** Webhooks count as working when one arrived within this window. */
export const WEBHOOK_OK_MS = 24 * 3_600_000;

const key = () => deriveKey("vcs-secrets");

export function encryptVcs(plaintext: string, aad: string): string {
  return encryptSecret(plaintext, aad, key());
}

export function decryptVcs(payload: string, aad: string): string {
  return decryptSecret(payload, aad, key());
}

export function connSecrets(row: ConnectionRow): ConnSecrets {
  if (!row.secretsEnc) return {};
  return JSON.parse(decryptVcs(row.secretsEnc, `${row.id}:secrets`)) as ConnSecrets;
}

function encodeSecrets(id: string, s: ConnSecrets): string {
  const clean = Object.fromEntries(Object.entries(s).filter(([, v]) => typeof v === "string" && v !== ""));
  return Object.keys(clean).length ? encryptVcs(JSON.stringify(clean), `${id}:secrets`) : "";
}

export function requireAdmin(p: Principal): void {
  if (p.user.role !== "admin") throw new ApiError("permission", "Only admins can manage code host connections");
}

export function isServerType(t: number): t is ProviderType.GITHUB | ProviderType.GITLAB {
  return t === ProviderType.GITHUB || t === ProviderType.GITLAB;
}

export function providerName(t: number): string {
  return t === ProviderType.GITLAB || t === ProviderType.GLAB ? "GitLab" : "GitHub";
}

/** https://github.com / https://gitlab.com by default; origin + path without a trailing slash. */
export function normalizeBaseUrl(raw: string, type: number): string {
  const v = raw.trim() || (type === ProviderType.GITLAB ? "https://gitlab.com" : "https://github.com");
  let u: URL;
  try {
    u = new URL(v);
  } catch {
    throw new ApiError("invalid_request", "base_url must be an http(s) URL");
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") throw new ApiError("invalid_request", "base_url must be an http(s) URL");
  if (u.username || u.password || u.search || u.hash) throw new ApiError("invalid_request", "base_url must not carry credentials, a query or a fragment");
  const path = u.pathname.replace(/\/+$/, "");
  // api.github.com is the API host of github.com, not a web base URL.
  if (u.hostname === "api.github.com") return "https://github.com";
  return `${u.origin}${path}`;
}

export function isGitHubDotCom(baseUrl: string): boolean {
  return hostOfBaseUrl(baseUrl) === "github.com";
}

export function installUrl(row: ConnectionRow): string {
  if (row.type === ProviderType.GITHUB) {
    if (!row.appSlug) return "";
    return isGitHubDotCom(row.baseUrl) ? `https://github.com/apps/${row.appSlug}/installations/new` : `${row.baseUrl}/github-apps/${row.appSlug}/installations/new`;
  }
  // GitLab: users authorize through StartUserAuth; this is where they review authorized applications.
  return `${row.baseUrl}/-/user_settings/applications`;
}

export function webhookUrl(type: number): string {
  return `${config().publicUrl}/api/vcs/${type === ProviderType.GITLAB ? "gitlab" : "github"}/webhook`;
}

// --- reads ---------------------------------------------------------------------------------------

export async function allConnections(): Promise<ConnectionRow[]> {
  await ensureDb();
  return getDb().select().from(schema.vcsConnection).orderBy(asc(schema.vcsConnection.createdAt));
}

export async function getConnectionRow(id: string): Promise<ConnectionRow> {
  if (!id) throw new ApiError("invalid_request", "connection_id is required");
  await ensureDb();
  const row = await getDb().query.vcsConnection.findFirst({ where: eq(schema.vcsConnection.id, id) });
  if (!row) throw new ApiError("not_found", "Connection not found");
  return row;
}

/** The server connection whose base URL host matches a repository host (server providers win over devices). */
export async function connectionForHost(host: string): Promise<ConnectionRow | undefined> {
  const rows = await allConnections();
  return rows.find((r) => hostOfBaseUrl(r.baseUrl) === host.toLowerCase());
}

// --- writes --------------------------------------------------------------------------------------

export interface PutConnectionInput {
  id: string;
  type: number;
  displayName: string;
  baseUrl: string;
  appId: string;
  privateKeyPem?: string;
  clientId?: string;
  clientSecret?: string;
  webhookSecret?: string;
  accessToken?: string;
  allowPrivateNetwork: boolean;
}

/** Optional write-only field: undefined keeps, "" removes, a value replaces. */
function applySecret(cur: string | undefined, next: string | undefined): string | undefined {
  if (next === undefined) return cur;
  return next === "" ? undefined : next;
}

async function assertHostFree(baseUrl: string, exceptId?: string): Promise<void> {
  const host = hostOfBaseUrl(baseUrl);
  const clash = (await allConnections()).find((r) => r.id !== exceptId && hostOfBaseUrl(r.baseUrl) === host);
  if (clash) throw new ApiError("conflict", `Connection ${clash.id} already covers ${host}`);
}

export async function putConnection(p: Principal, input: PutConnectionInput): Promise<ConnectionRow> {
  requireAdmin(p);
  await ensureDb();
  const cur = input.id ? await getConnectionRow(input.id) : undefined;
  const type = cur ? cur.type : input.type;
  if (cur && input.type && input.type !== cur.type) throw new ApiError("invalid_request", "type cannot be changed");
  if (!isServerType(type)) throw new ApiError("invalid_request", "type must be PROVIDER_TYPE_GITHUB or PROVIDER_TYPE_GITLAB (gh/glab are device providers)");
  const baseUrl = normalizeBaseUrl(input.baseUrl || cur?.baseUrl || "", type);
  assertUrlAllowed(baseUrl, input.allowPrivateNetwork);
  await assertResolvesPublic(baseUrl, input.allowPrivateNetwork);
  await assertHostFree(baseUrl, cur?.id);
  const prev = cur ? connSecrets(cur) : {};
  const secrets: ConnSecrets = {
    privateKeyPem: applySecret(prev.privateKeyPem, input.privateKeyPem),
    clientSecret: applySecret(prev.clientSecret, input.clientSecret),
    webhookSecret: applySecret(prev.webhookSecret, input.webhookSecret),
    accessToken: applySecret(prev.accessToken, input.accessToken),
  };
  if (secrets.privateKeyPem && !/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(secrets.privateKeyPem)) {
    throw new ApiError("invalid_request", "private_key_pem must be a PEM private key");
  }
  const appId = (input.appId || cur?.appId || "").trim();
  // The GitLab "Application ID" is the OAuth client id.
  const clientId = input.clientId !== undefined ? input.clientId : type === ProviderType.GITLAB && input.appId ? input.appId : (cur?.clientId ?? "");
  const now = new Date();
  const id = cur?.id ?? newId("vcs");
  const values = {
    displayName: (input.displayName || cur?.displayName || (type === ProviderType.GITLAB ? "GitLab" : "GitHub")).slice(0, 200),
    baseUrl,
    appId,
    clientId: clientId.trim(),
    secretsEnc: encodeSecrets(id, secrets),
    allowPrivateNetwork: input.allowPrivateNetwork,
    // A changed connection is re-checked on the next ListConnections.
    lastCheckedAt: null,
    updatedAt: now,
    updatedBy: p.user.id,
  };
  if (cur) {
    const [row] = await getDb().update(schema.vcsConnection).set(values).where(eq(schema.vcsConnection.id, id)).returning();
    forgetConnectionCaches(id);
    return row!;
  }
  const [row] = await getDb()
    .insert(schema.vcsConnection)
    .values({ id, type, appSlug: "", createdAt: now, ...values })
    .returning();
  return row!;
}

/** Creates a GitHub App connection from the manifest conversion result. */
export async function insertGitHubAppConnection(
  userId: string,
  c: { baseUrl: string; appId: string; slug: string; name: string; clientId: string; secrets: ConnSecrets; allowPrivateNetwork: boolean },
): Promise<ConnectionRow> {
  await ensureDb();
  await assertHostFree(c.baseUrl);
  const id = newId("vcs");
  const now = new Date();
  const [row] = await getDb()
    .insert(schema.vcsConnection)
    .values({
      id,
      type: ProviderType.GITHUB,
      displayName: c.name || "GitHub",
      baseUrl: c.baseUrl,
      appId: c.appId,
      appSlug: c.slug,
      clientId: c.clientId,
      secretsEnc: encodeSecrets(id, c.secrets),
      allowPrivateNetwork: c.allowPrivateNetwork,
      createdAt: now,
      updatedAt: now,
      updatedBy: userId,
    })
    .returning();
  return row!;
}

export async function deleteConnection(p: Principal, id: string): Promise<void> {
  requireAdmin(p);
  const row = await getConnectionRow(id);
  // Installations, user tokens and OAuth flows go with it (ON DELETE CASCADE).
  await getDb().delete(schema.vcsConnection).where(eq(schema.vcsConnection.id, row.id));
  forgetConnectionCaches(id);
}

export async function recordWebhook(id: string): Promise<void> {
  await getDb().update(schema.vcsConnection).set({ lastWebhookAt: new Date() }).where(eq(schema.vcsConnection.id, id));
}

export async function recordHealth(id: string, healthy: boolean, lastError: string): Promise<void> {
  await getDb()
    .update(schema.vcsConnection)
    .set({ healthy, lastError: lastError.slice(0, 500), lastCheckedAt: new Date() })
    .where(eq(schema.vcsConnection.id, id));
}

// --- caches keyed by connection (installation tokens, access checks) -----------------------------

const cacheHooks = new Set<(id: string) => void>();

export function onConnectionChanged(fn: (id: string) => void): void {
  cacheHooks.add(fn);
}

function forgetConnectionCaches(id: string): void {
  for (const h of cacheHooks) h(id);
}
