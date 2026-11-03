/**
 * Provider registry. API keys and headers are encrypted at rest (AES-256-GCM).
 *
 * Two scopes:
 *   - global providers (owner_user_id null): managed by admins, their models are offered to everyone
 *     (subject to each model's `roles`);
 *   - personal providers (owner_user_id = a user): added by any signed-in user with their own key; only the
 *     owner sees them, their models and their key hint, and only the owner's requests can use them.
 *
 * Personal providers get a generated id (`u-<random>`, the prefix is reserved: global ids cannot start with
 * it), so ids never collide across users and an id says nothing about another user's providers. They cannot
 * reach private networks (allow_private_network is admin-only), since requests run on the server.
 * Anything that does not belong to the caller's scope answers "not found", never "forbidden", so the
 * existence of another user's provider does not leak.
 *
 * Keys are write-only everywhere: no API (user or admin) returns a key or anything decryptable; admins see
 * a user's providers with `api_key_set` only and may delete them, but cannot use them (the gateway, slots
 * and the decision chain only accept global providers for anyone but the owner). Personal secrets are
 * encrypted with a per-user data key (secretKey below). This protects against database dumps and against
 * an admin account taken over through the API; it does not protect against someone holding both the
 * server secret (HARNESS_SECRET) and the database.
 */
import { randomBytes } from "node:crypto";
import { and, asc, eq, isNull, or } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { assertResolvesPublic } from "../lib/ssrf.ts";
import type { PublicUser } from "./devices.ts";

export const PROVIDER_TYPES = ["anthropic", "openai-chat", "openai-responses"] as const;
export type ProviderType = (typeof PROVIDER_TYPES)[number];

/** Id prefix of personal providers (reserved for them). */
export const PERSONAL_PREFIX = "u-";

/** Decrypted provider, only for use inside the gateway. Never serialize. */
export interface ProviderRuntime {
  id: string;
  type: ProviderType;
  baseUrl: string;
  apiKey?: string;
  headers: Record<string, string>;
  allowPrivateNetwork: boolean;
  enabled: boolean;
  /** Null for a global provider. */
  ownerUserId: string | null;
}

export interface ProviderInput {
  id?: string;
  type?: string;
  display_name?: string | null;
  base_url?: string;
  api_key?: string | null;
  headers?: Record<string, string> | null;
  allow_private_network?: boolean;
  enabled?: boolean;
}

/** Whose providers an operation works on: the global ones (admin) or one user's personal ones. */
export type ProviderOwner = { userId: string } | null;

type Row = typeof schema.provider.$inferSelect;

const ID_RE = /^[a-z0-9][a-z0-9._-]{0,63}$/;
/** Headers a provider may not set: they would change how the request is routed or framed. */
const RESERVED_HEADERS = new Set(["host", "content-length", "transfer-encoding", "connection", "upgrade", "te", "trailer", "proxy-authorization"]);
const MAX_PERSONAL_PROVIDERS = 20;

export function publicProvider(r: Row) {
  let headerNames: string[] = [];
  if (r.headersEnc) headerNames = Object.keys(JSON.parse(decryptSecret(r.headersEnc, `${r.id}:headers`, secretKey(r.ownerUserId))));
  return {
    id: r.id,
    scope: r.ownerUserId ? ("personal" as const) : ("global" as const),
    type: r.type,
    display_name: r.displayName,
    base_url: r.baseUrl,
    api_key_set: !!r.apiKeyEnc,
    api_key_hint: r.apiKeyHint,
    header_names: headerNames,
    allow_private_network: r.allowPrivateNetwork,
    enabled: r.enabled,
    created_at: r.createdAt.toISOString(),
    updated_at: r.updatedAt.toISOString(),
  };
}
export type PublicProvider = ReturnType<typeof publicProvider>;

function validateHeaders(h: unknown): Record<string, string> {
  if (h === null || h === undefined) return {};
  if (typeof h !== "object" || Array.isArray(h)) throw new ApiError("invalid_request", "headers must be an object");
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(h)) {
    if (!/^[A-Za-z0-9-]+$/.test(k) || typeof v !== "string" || RESERVED_HEADERS.has(k.toLowerCase())) {
      throw new ApiError("invalid_request", `Invalid header ${k}`);
    }
    out[k] = v;
  }
  return out;
}

/**
 * Encryption key of a provider's secrets: the server-wide provider key for global providers, a per-user data
 * key (HKDF of HARNESS_SECRET with the owner's id) for personal ones, so a personal row only decrypts as that
 * user's and cannot be moved to another user or scope. With the AAD (`<provider id>:api_key`) a ciphertext
 * is bound to its row as well.
 */
function secretKey(ownerUserId: string | null | undefined): Buffer {
  return deriveKey(ownerUserId ? `provider-key:${ownerUserId}` : "provider-secrets");
}

function hint(key: string): string {
  return key.length > 8 ? `…${key.slice(-4)}` : "…";
}

/** `u-` plus 10 random lowercase letters and digits. */
function personalId(): string {
  const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789";
  return PERSONAL_PREFIX + [...randomBytes(10)].map((b) => alphabet[b % alphabet.length]).join("");
}

/** Condition selecting the rows of one scope. */
function scopeCond(owner: ProviderOwner) {
  return owner ? eq(schema.provider.ownerUserId, owner.userId) : isNull(schema.provider.ownerUserId);
}

async function findInScope(id: string, owner: ProviderOwner): Promise<Row> {
  await ensureDb();
  const r = await getDb().query.provider.findFirst({ where: and(eq(schema.provider.id, id), scopeCond(owner)) });
  if (!r) throw new ApiError("not_found", `Provider ${id} not found`);
  return r;
}

/** Global providers (admin API). */
export async function listProviders() {
  await ensureDb();
  const rows = await getDb().select().from(schema.provider).where(isNull(schema.provider.ownerUserId)).orderBy(asc(schema.provider.createdAt));
  return rows.map(publicProvider);
}

/**
 * Providers a user can see: the global ones (all of them for admins, enabled ones for everyone else),
 * then the user's own.
 */
export async function listProvidersForUser(user: PublicUser) {
  await ensureDb();
  const rows = await getDb()
    .select()
    .from(schema.provider)
    .where(or(isNull(schema.provider.ownerUserId), eq(schema.provider.ownerUserId, user.id)))
    .orderBy(asc(schema.provider.createdAt));
  const visible = rows.filter((r) => r.ownerUserId || r.enabled || user.role === "admin");
  return [...visible.filter((r) => !r.ownerUserId), ...visible.filter((r) => r.ownerUserId)].map(publicProvider);
}

/** One user's personal providers (admin API: shows key_set / hint only, like every view). */
export async function listUserProviders(userId: string) {
  await ensureDb();
  const rows = await getDb().select().from(schema.provider).where(eq(schema.provider.ownerUserId, userId)).orderBy(asc(schema.provider.createdAt));
  return rows.map(publicProvider);
}

/** A global provider (owner null) or one of the owner's personal providers. */
export async function getProvider(id: string, owner: ProviderOwner = null) {
  return publicProvider(await findInScope(id, owner));
}

/**
 * The provider row a user may read (global and enabled, global for admins, or their own) and whether they
 * may change it (admins: global ones; everyone: their own). Anything else is "not found".
 */
export async function providerForUser(id: string, user: PublicUser): Promise<{ row: Row; owner: ProviderOwner; writable: boolean }> {
  await ensureDb();
  const r = await getDb().query.provider.findFirst({ where: eq(schema.provider.id, id) });
  if (r && r.ownerUserId === user.id) return { row: r, owner: { userId: user.id }, writable: true };
  if (r && !r.ownerUserId && (r.enabled || user.role === "admin")) return { row: r, owner: null, writable: user.role === "admin" };
  throw new ApiError("not_found", `Provider ${id} not found`);
}

export async function createProvider(input: ProviderInput, owner: ProviderOwner = null) {
  await ensureDb();
  const db = getDb();
  let id: string;
  if (owner) {
    if (input.id !== undefined && input.id !== "") throw new ApiError("invalid_request", "Personal providers get a generated id");
    if (input.allow_private_network) throw new ApiError("invalid_request", "Only global providers may reach private networks");
    const count = (await db.select({ id: schema.provider.id }).from(schema.provider).where(scopeCond(owner))).length;
    if (count >= MAX_PERSONAL_PROVIDERS) throw new ApiError("invalid_request", `At most ${MAX_PERSONAL_PROVIDERS} personal providers`);
    id = personalId();
  } else {
    if (!input.id) throw new ApiError("invalid_request", "id is required");
    id = input.id;
    if (!ID_RE.test(id)) throw new ApiError("invalid_request", "id must match [a-z0-9][a-z0-9._-]*");
    if (id.startsWith(PERSONAL_PREFIX)) throw new ApiError("invalid_request", `Ids starting with "${PERSONAL_PREFIX}" are reserved for personal providers`);
  }
  if (!PROVIDER_TYPES.includes(input.type as ProviderType)) {
    throw new ApiError("invalid_request", `type must be one of ${PROVIDER_TYPES.join(", ")}`);
  }
  if (!input.base_url) throw new ApiError("invalid_request", "base_url is required");
  const allowPrivate = !owner && input.allow_private_network === true;
  await assertResolvesPublic(input.base_url, allowPrivate);
  if (await db.query.provider.findFirst({ where: eq(schema.provider.id, id) })) {
    throw new ApiError("invalid_request", `Provider ${id} already exists`, { status: 409 });
  }
  const headers = validateHeaders(input.headers);
  const now = new Date();
  const [row] = await db
    .insert(schema.provider)
    .values({
      id,
      ownerUserId: owner?.userId ?? null,
      type: input.type!,
      displayName: input.display_name || null,
      baseUrl: input.base_url.replace(/\/+$/, ""),
      apiKeyEnc: input.api_key ? encryptSecret(input.api_key, `${id}:api_key`, secretKey(owner?.userId)) : null,
      apiKeyHint: input.api_key ? hint(input.api_key) : null,
      headersEnc: Object.keys(headers).length ? encryptSecret(JSON.stringify(headers), `${id}:headers`, secretKey(owner?.userId)) : null,
      allowPrivateNetwork: allowPrivate,
      enabled: input.enabled ?? true,
      createdAt: now,
      updatedAt: now,
    })
    .returning();
  return publicProvider(row!);
}

export async function updateProvider(id: string, input: ProviderInput, owner: ProviderOwner = null) {
  const cur = await findInScope(id, owner);
  if (owner && input.allow_private_network) throw new ApiError("invalid_request", "Only global providers may reach private networks");
  const patch: Partial<Row> = { updatedAt: new Date() };
  if (input.type !== undefined) {
    if (!PROVIDER_TYPES.includes(input.type as ProviderType)) throw new ApiError("invalid_request", "invalid type");
    patch.type = input.type;
  }
  if (input.display_name !== undefined) patch.displayName = input.display_name || null;
  const allowPrivate = input.allow_private_network ?? cur.allowPrivateNetwork;
  if (input.allow_private_network !== undefined) patch.allowPrivateNetwork = allowPrivate;
  if (input.base_url !== undefined || input.allow_private_network === false) {
    const url = input.base_url ?? cur.baseUrl;
    await assertResolvesPublic(url, allowPrivate);
    patch.baseUrl = url.replace(/\/+$/, "");
  }
  if (input.api_key !== undefined) {
    patch.apiKeyEnc = input.api_key ? encryptSecret(input.api_key, `${id}:api_key`, secretKey(cur.ownerUserId)) : null;
    patch.apiKeyHint = input.api_key ? hint(input.api_key) : null;
  }
  if (input.headers !== undefined) {
    const headers = validateHeaders(input.headers);
    patch.headersEnc = Object.keys(headers).length ? encryptSecret(JSON.stringify(headers), `${id}:headers`, secretKey(cur.ownerUserId)) : null;
  }
  if (input.enabled !== undefined) patch.enabled = input.enabled;
  const [row] = await getDb().update(schema.provider).set(patch).where(eq(schema.provider.id, id)).returning();
  return publicProvider(row!);
}

/**
 * Deletes a provider. A global provider still used by catalog models is refused (the admin removes or moves
 * them first); a personal provider takes its models with it.
 */
export async function deleteProvider(id: string, owner: ProviderOwner = null) {
  await findInScope(id, owner);
  const db = getDb();
  if (!owner) {
    const used = await db.query.model.findFirst({ where: eq(schema.model.providerId, id) });
    if (used) throw new ApiError("invalid_request", `Provider is used by model ${used.id}`, { status: 409 });
  }
  await db.delete(schema.provider).where(eq(schema.provider.id, id));
  return { deleted: true };
}

/**
 * Removes a provider's secrets (API key, header values) from text that may reach clients or logs, such as
 * an upstream error body that echoes the key back.
 */
export function redactSecrets(text: string, p: Pick<ProviderRuntime, "apiKey" | "headers">): string {
  let out = text;
  for (const s of [p.apiKey, ...Object.values(p.headers)]) {
    if (s && s.length >= 6) out = out.split(s).join("[redacted]");
  }
  return out;
}

export async function loadProviderRuntime(id: string): Promise<ProviderRuntime> {
  await ensureDb();
  const r = await getDb().query.provider.findFirst({ where: eq(schema.provider.id, id) });
  if (!r) throw new ApiError("internal", `Provider ${id} is missing`);
  return {
    id: r.id,
    type: r.type as ProviderType,
    baseUrl: r.baseUrl,
    apiKey: r.apiKeyEnc ? decryptSecret(r.apiKeyEnc, `${r.id}:api_key`, secretKey(r.ownerUserId)) : undefined,
    headers: r.headersEnc ? JSON.parse(decryptSecret(r.headersEnc, `${r.id}:headers`, secretKey(r.ownerUserId))) : {},
    allowPrivateNetwork: r.allowPrivateNetwork,
    enabled: r.enabled,
    ownerUserId: r.ownerUserId,
  };
}
