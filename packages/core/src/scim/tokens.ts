/** Tokens and settings of SCIM provisioning. A token is shown once; only its hash is kept. */
import { asc, eq } from "drizzle-orm";
import { cleanMappings, type RoleMapping } from "../auth/roles.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { ApiError } from "../lib/errors.ts";
import { newId, newToken, sha256 } from "../lib/ids.ts";

const KEY = "scim_settings";

export interface ScimSettings {
  role_mappings: RoleMapping[];
}

export const scimBaseUrl = () => `${config().publicUrl}/scim/v2`;

export async function getScimSettings(): Promise<ScimSettings> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, KEY) });
  return { role_mappings: cleanMappings((r?.value as Partial<ScimSettings> | undefined)?.role_mappings) };
}

export async function setScimSettings(input: ScimSettings): Promise<ScimSettings> {
  await ensureDb();
  const value: ScimSettings = { role_mappings: cleanMappings(input.role_mappings) };
  const now = new Date();
  await getDb()
    .insert(schema.setting)
    .values({ key: KEY, value, updatedAt: now })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value, updatedAt: now } });
  return value;
}

export interface ScimTokenView {
  id: string;
  name: string;
  created_at: string;
  last_used_at: string;
}

const view = (r: typeof schema.scimToken.$inferSelect): ScimTokenView => ({
  id: r.id,
  name: r.name,
  created_at: r.createdAt.toISOString(),
  last_used_at: r.lastUsedAt?.toISOString() ?? "",
});

export async function listScimTokens(): Promise<ScimTokenView[]> {
  await ensureDb();
  return (await getDb().select().from(schema.scimToken).orderBy(asc(schema.scimToken.createdAt))).map(view);
}

export async function createScimToken(name: string): Promise<{ token: ScimTokenView; value: string }> {
  await ensureDb();
  const n = name.trim();
  if (!n || n.length > 100) throw new ApiError("invalid_request", "A token needs a name of up to 100 characters");
  const value = newToken("hsc");
  const [row] = await getDb().insert(schema.scimToken).values({ id: newId("sct"), name: n, tokenHash: sha256(value), createdAt: new Date() }).returning();
  return { token: view(row!), value };
}

export async function revokeScimToken(id: string): Promise<void> {
  await ensureDb();
  await getDb().delete(schema.scimToken).where(eq(schema.scimToken.id, id));
}

/** The token's row when `value` is a live token. */
export async function authenticateScim(value: string): Promise<{ id: string; name: string } | undefined> {
  if (!value.startsWith("hsc_")) return undefined;
  await ensureDb();
  const db = getDb();
  const hash = sha256(value);
  const row = await db.query.scimToken.findFirst({ where: eq(schema.scimToken.tokenHash, hash) });
  if (!row) return undefined;
  // Throttled: a provisioning run makes many calls.
  if (!row.lastUsedAt || Date.now() - row.lastUsedAt.getTime() > 60_000) {
    await db.update(schema.scimToken).set({ lastUsedAt: new Date() }).where(eq(schema.scimToken.id, row.id));
  }
  return { id: row.id, name: row.name };
}
