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
 * Two groups: "llm" providers serve the gateway's models; "classifier" providers are auto-mode decision
 * services (a typed decision model or a policy webhook) that the server-wide decision chain uses, so they are
 * global only and serve no chat models; "search" providers are web search APIs (SearchService), global or personal
 * like LLM providers, with a few plain-text options (result count, safe search, country, language) that their
 * preset declares. A provider may come from a preset (services/presets.ts): the user
 * gives only what the preset asks for and the rest comes from the preset.
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
import {
  BUILTIN_SEARCH_ID,
  CLASSIFIER_TYPES,
  LLM_TYPES,
  SEARCH_TYPES,
  billingOf,
  findPreset,
  groupOfType,
  presetOf,
  searchOptionSpecs,
  type Billing,
  type PresetOption,
  type ProviderGroup,
} from "./presets.ts";

/** Types of LLM providers (the gateway adapters). */
export const PROVIDER_TYPES = LLM_TYPES;
export type ProviderType = (typeof PROVIDER_TYPES)[number];

/** Id prefix of personal providers (reserved for them). */
export const PERSONAL_PREFIX = "u-";

/** Decrypted provider, only for use inside the gateway. Never serialize. */
export interface ProviderRuntime {
  id: string;
  /** A classifier provider's type ("decision-model", "decision-http") never reaches the gateway. */
  type: ProviderType;
  kind: ProviderGroup;
  baseUrl: string;
  apiKey?: string;
  headers: Record<string, string>;
  allowPrivateNetwork: boolean;
  enabled: boolean;
  /** Null for a global provider. */
  ownerUserId: string | null;
  /** Search providers: their options (validated plain text). */
  options: Record<string, string>;
  /** LLM providers: a "plan" provider's requests never cost money (services/presets.ts). */
  billing: Billing;
}

export interface ProviderInput {
  id?: string;
  /** Connect a preset: type, kind, name and base URL default to the preset's. */
  preset_id?: string;
  /** Custom providers: "llm" (default) or "classifier". */
  kind?: string;
  type?: string;
  display_name?: string | null;
  base_url?: string;
  api_key?: string | null;
  headers?: Record<string, string> | null;
  allow_private_network?: boolean;
  enabled?: boolean;
  /** Search providers: replaces all options. */
  options?: Record<string, string> | null;
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
    group: r.kind as ProviderGroup,
    preset_id: presetOf(r)?.id ?? null,
    billing: billingOf(r),
    type: r.type,
    display_name: r.displayName,
    base_url: r.baseUrl,
    api_key_set: !!r.apiKeyEnc,
    api_key_hint: r.apiKeyHint,
    header_names: headerNames,
    allow_private_network: r.allowPrivateNetwork,
    enabled: r.enabled,
    options: (r.options as Record<string, string> | null) ?? {},
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

const COUNTRY_RE = /^[A-Za-z]{2}$/;
const LANGUAGE_RE = /^(all|[A-Za-z]{2,3}(-[A-Za-z0-9]{2,4})?)$/;

/** A search provider's options, checked against what its preset declares; empty values are dropped. */
export function validateOptions(specs: PresetOption[], input: Record<string, string> | null | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [name, raw] of Object.entries(input ?? {})) {
    const spec = specs.find((o) => o.name === name);
    if (!spec) throw new ApiError("invalid_request", `Unknown option ${name}`);
    const v = typeof raw === "string" ? raw.trim() : "";
    if (!v) continue;
    if (spec.kind === "number") {
      const n = Number(v);
      if (!Number.isInteger(n) || n < (spec.min ?? 1) || n > (spec.max ?? 100)) {
        throw new ApiError("invalid_request", `${spec.label} must be a whole number from ${spec.min ?? 1} to ${spec.max ?? 100}`);
      }
      out[name] = String(n);
    } else if (spec.kind === "select") {
      if (!spec.choices?.some((c) => c.value === v)) throw new ApiError("invalid_request", `${spec.label} must be one of ${spec.choices?.map((c) => c.value).join(", ")}`);
      out[name] = v;
    } else if (name === "country") {
      if (!COUNTRY_RE.test(v)) throw new ApiError("invalid_request", "Country must be a two-letter code");
      out[name] = v.toUpperCase();
    } else if (name === "language") {
      if (!LANGUAGE_RE.test(v)) throw new ApiError("invalid_request", "Language must be a code such as en or pt-BR");
      out[name] = v === "all" ? v : v.toLowerCase();
    } else {
      out[name] = v.slice(0, 64);
    }
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

/** Lowercase slug for a derived global id ("My Router!" -> "my-router"). */
function slug(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/^[^a-z0-9]+|-+$/g, "")
    .slice(0, 48);
}

/** A free global id based on `base` (base, base-2, base-3, ...). */
async function freeGlobalId(base: string): Promise<string> {
  const db = getDb();
  let root = slug(base) || "provider";
  if (root.startsWith(PERSONAL_PREFIX)) root = `p${root}`;
  if (root === BUILTIN_SEARCH_ID) root = `${root}-1`;
  for (let n = 1; n < 100; n++) {
    const id = n === 1 ? root : `${root}-${n}`;
    if (!(await db.query.provider.findFirst({ where: eq(schema.provider.id, id) }))) return id;
  }
  throw new ApiError("invalid_request", "Choose an id");
}

/** Types a custom provider of each group may use (search: SearXNG-compatible instances only). */
const CUSTOM_SEARCH_TYPES = ["searxng"] as const;

function checkType(kind: ProviderGroup, type: string | undefined, custom = false): string {
  const allowed: readonly string[] = kind === "classifier" ? CLASSIFIER_TYPES : kind === "search" ? (custom ? CUSTOM_SEARCH_TYPES : SEARCH_TYPES) : LLM_TYPES;
  if (!type || !allowed.includes(type)) throw new ApiError("invalid_request", `type must be one of ${allowed.join(", ")}`);
  return type;
}

export async function createProvider(input: ProviderInput, owner: ProviderOwner = null) {
  await ensureDb();
  const db = getDb();
  const preset = input.preset_id ? findPreset(input.preset_id) : undefined;
  if (input.preset_id && !preset) throw new ApiError("invalid_request", `Unknown preset ${input.preset_id}`);
  if (preset?.builtin) throw new ApiError("invalid_request", `${preset.display_name} is built in: there is nothing to connect`);
  let kind: ProviderGroup;
  let type: string;
  let baseUrl = input.base_url?.trim();
  if (preset) {
    kind = preset.group;
    type = preset.type;
    // The URL is the preset's unless the preset asks for it (a local server).
    if (!preset.fields.some((f) => f.name === "base_url") || !baseUrl) baseUrl = preset.base_url;
    if (preset.fields.some((f) => f.name === "api_key" && f.required) && !input.api_key) {
      throw new ApiError("invalid_request", `${preset.display_name} needs an API key`);
    }
  } else {
    if (input.kind && input.kind !== "llm" && input.kind !== "classifier" && input.kind !== "search") {
      throw new ApiError("invalid_request", 'group must be "llm", "classifier" or "search"');
    }
    kind = (input.kind as ProviderGroup | undefined) || groupOfType(input.type ?? "") || "llm";
    type = checkType(kind, input.type, true);
  }
  if (kind === "classifier" && owner) throw new ApiError("invalid_request", "Classifier providers are server-wide: only admins add them, globally");

  let id: string;
  if (owner) {
    if (input.id !== undefined && input.id !== "") throw new ApiError("invalid_request", "Personal providers get a generated id");
    if (input.allow_private_network) throw new ApiError("invalid_request", "Only global providers may reach private networks");
    const count = (await db.select({ id: schema.provider.id }).from(schema.provider).where(scopeCond(owner))).length;
    if (count >= MAX_PERSONAL_PROVIDERS) throw new ApiError("invalid_request", `At most ${MAX_PERSONAL_PROVIDERS} personal providers`);
    id = personalId();
  } else {
    id = input.id || (await freeGlobalId(preset?.id || input.display_name || type));
    if (!ID_RE.test(id)) throw new ApiError("invalid_request", "id must match [a-z0-9][a-z0-9._-]*");
    if (id.startsWith(PERSONAL_PREFIX)) throw new ApiError("invalid_request", `Ids starting with "${PERSONAL_PREFIX}" are reserved for personal providers`);
    if (id === BUILTIN_SEARCH_ID) throw new ApiError("invalid_request", `The id "${BUILTIN_SEARCH_ID}" is reserved for the built-in search`);
  }
  if (!baseUrl) throw new ApiError("invalid_request", "base_url is required");
  // A local preset connected globally may reach the private network it is meant for.
  const allowPrivate = !owner && (input.allow_private_network === true || !!preset?.local);
  await assertResolvesPublic(baseUrl, allowPrivate);
  if (await db.query.provider.findFirst({ where: eq(schema.provider.id, id) })) {
    throw new ApiError("invalid_request", `Provider ${id} already exists`, { status: 409 });
  }
  const headers = validateHeaders(input.headers);
  const options = kind === "search" ? validateOptions(searchOptionSpecs({ presetId: preset?.id ?? null, kind, type, baseUrl }), input.options) : {};
  if (kind !== "search" && input.options && Object.keys(input.options).length) throw new ApiError("invalid_request", "Only search providers have options");
  const now = new Date();
  const [row] = await db
    .insert(schema.provider)
    .values({
      id,
      ownerUserId: owner?.userId ?? null,
      kind,
      type,
      presetId: preset?.id ?? null,
      displayName: input.display_name || preset?.display_name || null,
      baseUrl: baseUrl.replace(/\/+$/, ""),
      apiKeyEnc: input.api_key ? encryptSecret(input.api_key, `${id}:api_key`, secretKey(owner?.userId)) : null,
      apiKeyHint: input.api_key ? hint(input.api_key) : null,
      headersEnc: Object.keys(headers).length ? encryptSecret(JSON.stringify(headers), `${id}:headers`, secretKey(owner?.userId)) : null,
      allowPrivateNetwork: allowPrivate,
      enabled: input.enabled ?? true,
      options: Object.keys(options).length ? options : null,
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
  if (input.type !== undefined) patch.type = checkType(cur.kind as ProviderGroup, input.type, !presetOf(cur));
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
  if (input.options !== undefined) {
    if (cur.kind !== "search") throw new ApiError("invalid_request", "Only search providers have options");
    const options = validateOptions(searchOptionSpecs({ ...cur, type: patch.type ?? cur.type }), input.options);
    patch.options = Object.keys(options).length ? options : null;
  }
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
    kind: r.kind as ProviderGroup,
    baseUrl: r.baseUrl,
    apiKey: r.apiKeyEnc ? decryptSecret(r.apiKeyEnc, `${r.id}:api_key`, secretKey(r.ownerUserId)) : undefined,
    headers: r.headersEnc ? JSON.parse(decryptSecret(r.headersEnc, `${r.id}:headers`, secretKey(r.ownerUserId))) : {},
    allowPrivateNetwork: r.allowPrivateNetwork,
    enabled: r.enabled,
    ownerUserId: r.ownerUserId,
    options: (r.options as Record<string, string> | null) ?? {},
    billing: billingOf(r),
  };
}
