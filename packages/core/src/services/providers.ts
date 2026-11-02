/** Provider registry (admin). API keys and headers are encrypted at rest (AES-256-GCM). */
import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { decryptSecret, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { assertResolvesPublic } from "../lib/ssrf.ts";

export const PROVIDER_TYPES = ["anthropic", "openai-chat"] as const;
export type ProviderType = (typeof PROVIDER_TYPES)[number];

/** Decrypted provider, only for use inside the gateway. Never serialize. */
export interface ProviderRuntime {
  id: string;
  type: ProviderType;
  baseUrl: string;
  apiKey?: string;
  headers: Record<string, string>;
  allowPrivateNetwork: boolean;
  enabled: boolean;
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

type Row = typeof schema.provider.$inferSelect;

const ID_RE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

export function publicProvider(r: Row) {
  let headerNames: string[] = [];
  if (r.headersEnc) headerNames = Object.keys(JSON.parse(decryptSecret(r.headersEnc, `${r.id}:headers`)));
  return {
    id: r.id,
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

function validateHeaders(h: unknown): Record<string, string> {
  if (h === null || h === undefined) return {};
  if (typeof h !== "object" || Array.isArray(h)) throw new ApiError("invalid_request", "headers must be an object");
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(h)) {
    if (!/^[A-Za-z0-9-]+$/.test(k) || typeof v !== "string") {
      throw new ApiError("invalid_request", `Invalid header ${k}`);
    }
    out[k] = v;
  }
  return out;
}

function hint(key: string): string {
  return key.length > 8 ? `…${key.slice(-4)}` : "…";
}

export async function listProviders() {
  await ensureDb();
  const rows = await getDb().select().from(schema.provider).orderBy(schema.provider.createdAt);
  return rows.map(publicProvider);
}

export async function getProvider(id: string) {
  await ensureDb();
  const r = await getDb().query.provider.findFirst({ where: eq(schema.provider.id, id) });
  if (!r) throw new ApiError("not_found", `Provider ${id} not found`);
  return publicProvider(r);
}

export async function createProvider(input: ProviderInput) {
  await ensureDb();
  const id = input.id ?? newId("prv");
  if (!ID_RE.test(id)) throw new ApiError("invalid_request", "id must match [a-z0-9][a-z0-9._-]*");
  if (!PROVIDER_TYPES.includes(input.type as ProviderType)) {
    throw new ApiError("invalid_request", `type must be one of ${PROVIDER_TYPES.join(", ")}`);
  }
  if (!input.base_url) throw new ApiError("invalid_request", "base_url is required");
  const allowPrivate = input.allow_private_network === true;
  await assertResolvesPublic(input.base_url, allowPrivate);
  const db = getDb();
  if (await db.query.provider.findFirst({ where: eq(schema.provider.id, id) })) {
    throw new ApiError("invalid_request", `Provider ${id} already exists`, { status: 409 });
  }
  const headers = validateHeaders(input.headers);
  const now = new Date();
  const [row] = await db
    .insert(schema.provider)
    .values({
      id,
      type: input.type!,
      displayName: input.display_name ?? null,
      baseUrl: input.base_url.replace(/\/+$/, ""),
      apiKeyEnc: input.api_key ? encryptSecret(input.api_key, `${id}:api_key`) : null,
      apiKeyHint: input.api_key ? hint(input.api_key) : null,
      headersEnc: Object.keys(headers).length ? encryptSecret(JSON.stringify(headers), `${id}:headers`) : null,
      allowPrivateNetwork: allowPrivate,
      enabled: input.enabled ?? true,
      createdAt: now,
      updatedAt: now,
    })
    .returning();
  return publicProvider(row!);
}

export async function updateProvider(id: string, input: ProviderInput) {
  await ensureDb();
  const db = getDb();
  const cur = await db.query.provider.findFirst({ where: eq(schema.provider.id, id) });
  if (!cur) throw new ApiError("not_found", `Provider ${id} not found`);
  const patch: Partial<Row> = { updatedAt: new Date() };
  if (input.type !== undefined) {
    if (!PROVIDER_TYPES.includes(input.type as ProviderType)) throw new ApiError("invalid_request", "invalid type");
    patch.type = input.type;
  }
  if (input.display_name !== undefined) patch.displayName = input.display_name;
  const allowPrivate = input.allow_private_network ?? cur.allowPrivateNetwork;
  if (input.allow_private_network !== undefined) patch.allowPrivateNetwork = allowPrivate;
  if (input.base_url !== undefined || input.allow_private_network === false) {
    const url = input.base_url ?? cur.baseUrl;
    await assertResolvesPublic(url, allowPrivate);
    patch.baseUrl = url.replace(/\/+$/, "");
  }
  if (input.api_key !== undefined) {
    patch.apiKeyEnc = input.api_key ? encryptSecret(input.api_key, `${id}:api_key`) : null;
    patch.apiKeyHint = input.api_key ? hint(input.api_key) : null;
  }
  if (input.headers !== undefined) {
    const headers = validateHeaders(input.headers);
    patch.headersEnc = Object.keys(headers).length ? encryptSecret(JSON.stringify(headers), `${id}:headers`) : null;
  }
  if (input.enabled !== undefined) patch.enabled = input.enabled;
  const [row] = await db.update(schema.provider).set(patch).where(eq(schema.provider.id, id)).returning();
  return publicProvider(row!);
}

export async function deleteProvider(id: string) {
  await ensureDb();
  const db = getDb();
  const used = await db.query.model.findFirst({ where: eq(schema.model.providerId, id) });
  if (used) throw new ApiError("invalid_request", `Provider is used by model ${used.id}`, { status: 409 });
  const res = await db.delete(schema.provider).where(eq(schema.provider.id, id)).returning();
  if (res.length === 0) throw new ApiError("not_found", `Provider ${id} not found`);
  return { deleted: true };
}

export async function loadProviderRuntime(id: string): Promise<ProviderRuntime> {
  await ensureDb();
  const r = await getDb().query.provider.findFirst({ where: eq(schema.provider.id, id) });
  if (!r) throw new ApiError("internal", `Provider ${id} is missing`);
  return {
    id: r.id,
    type: r.type as ProviderType,
    baseUrl: r.baseUrl,
    apiKey: r.apiKeyEnc ? decryptSecret(r.apiKeyEnc, `${r.id}:api_key`) : undefined,
    headers: r.headersEnc ? JSON.parse(decryptSecret(r.headersEnc, `${r.id}:headers`)) : {},
    allowPrivateNetwork: r.allowPrivateNetwork,
    enabled: r.enabled,
  };
}
