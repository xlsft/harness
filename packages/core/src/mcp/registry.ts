/**
 * MCP server registry (McpService registry RPCs). Transport-agnostic.
 *
 * - Scopes: USER and PROJECT servers belong to their owner (only the owner reads and writes them); ORG
 *   servers are written by admins and visible to everyone or to target roles (admins see all).
 * - Every write takes the next global sync version (services/sync.ts) inside one transaction that also
 *   writes the DOCUMENT_KIND_MCP_SERVER notification row (key = server id) with the same version, so
 *   WatchChanges subscribers learn about it and refetch through ListServers / GetServer.
 * - Secrets are write-only: values are encrypted per server and name, reads return names and has_value.
 */
import { and, asc, eq, inArray, or } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { Transport } from "../gen/harness/mcp/v1/mcp_pb.ts";
import { DocumentKind } from "../gen/harness/sync/v1/sync_pb.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal, PublicUser } from "../services/devices.ts";
import { nextSyncVersion, runSyncWrite, writeNotification, type DocRow, type Tx } from "../services/sync.ts";
import {
  canonicalJson,
  configHash,
  secretRefs,
  urlOrigin,
  validateSecretChanges,
  validateServer,
  type SecretChangesInput,
  type ServerInput,
  type StoredConfig,
} from "./config.ts";

export type ServerRow = typeof schema.mcpServer.$inferSelect;

export interface SecretInfo {
  name: string;
  hasValue: boolean;
  updatedAt: string;
}

export interface ServerView {
  row: ServerRow;
  config: StoredConfig;
  secrets: SecretInfo[];
}

export type UpdateResult = { server: ServerView } | { conflict: ServerView };

/** Key for MCP secrets and tokens (a separate HKDF purpose from provider secrets). */
export function mcpKey(): Buffer {
  return deriveKey("mcp-secrets");
}

export function encryptMcp(plaintext: string, aad: string): string {
  return encryptSecret(plaintext, aad, mcpKey());
}

export function decryptMcp(payload: string, aad: string): string {
  return decryptSecret(payload, aad, mcpKey());
}

const secretAad = (serverId: string, name: string) => `${serverId}:secret:${name}`;

// --- change hooks (proxy pool, catalog) ----------------------------------------------------------

type ChangeHook = (serverId: string) => void;
const hooks = new Set<ChangeHook>();

/** Called after a server was updated or deleted (after commit). */
export function onServerChanged(fn: ChangeHook): () => void {
  hooks.add(fn);
  return () => hooks.delete(fn);
}

function fireChanged(id: string): void {
  for (const h of hooks) {
    try {
      h(id);
    } catch (err) {
      console.error("[harness] mcp change hook failed", err);
    }
  }
}

// --- visibility --------------------------------------------------------------------------------

function roles(row: ServerRow): string[] {
  return Array.isArray(row.targetRoles) ? (row.targetRoles as string[]) : [];
}

export function canSee(row: ServerRow, user: PublicUser): boolean {
  if (row.scope === Scope.ORG) {
    const r = roles(row);
    return user.role === "admin" || r.length === 0 || r.includes(user.role);
  }
  return row.ownerId === user.id;
}

function canWrite(row: ServerRow, user: PublicUser): boolean {
  return row.scope === Scope.ORG ? user.role === "admin" : row.ownerId === user.id;
}

// --- reads -------------------------------------------------------------------------------------

async function secretRows(serverIds: string[]) {
  if (!serverIds.length) return [];
  return getDb()
    .select({ serverId: schema.mcpSecret.serverId, name: schema.mcpSecret.name, updatedAt: schema.mcpSecret.updatedAt })
    .from(schema.mcpSecret)
    .where(inArray(schema.mcpSecret.serverId, serverIds));
}

function secretInfos(config: StoredConfig, stored: { name: string; updatedAt: Date }[]): SecretInfo[] {
  const map = new Map<string, SecretInfo>();
  for (const name of secretRefs(config)) map.set(name, { name, hasValue: false, updatedAt: "" });
  for (const s of stored) map.set(s.name, { name: s.name, hasValue: true, updatedAt: s.updatedAt.toISOString() });
  return [...map.values()].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

export function rowConfig(row: ServerRow): StoredConfig {
  const c = (row.config ?? {}) as StoredConfig;
  return { ...c, overrides: c.overrides ?? [] };
}

async function views(rows: ServerRow[]): Promise<ServerView[]> {
  const secrets = await secretRows(rows.map((r) => r.id));
  return rows.map((row) => {
    const config = rowConfig(row);
    return { row, config, secrets: secretInfos(config, secrets.filter((s) => s.serverId === row.id)) };
  });
}

export interface ListFilter {
  scopes: Scope[];
  includeDisabled: boolean;
  projectKey: string;
}

/** All servers visible to the caller (own user / project servers and visible org servers). */
export async function visibleRows(user: PublicUser, f: Partial<ListFilter> = {}): Promise<ServerRow[]> {
  await ensureDb();
  const s = schema.mcpServer;
  const scopes = f.scopes?.length ? f.scopes : [Scope.USER, Scope.ORG, Scope.PROJECT];
  const conds = [];
  if (scopes.includes(Scope.USER)) conds.push(and(eq(s.scope, Scope.USER), eq(s.ownerId, user.id)));
  if (scopes.includes(Scope.PROJECT)) {
    conds.push(and(eq(s.scope, Scope.PROJECT), eq(s.ownerId, user.id), f.projectKey ? eq(s.projectKey, f.projectKey) : undefined));
  }
  if (scopes.includes(Scope.ORG)) conds.push(eq(s.scope, Scope.ORG));
  if (!conds.length) return [];
  const rows = await getDb()
    .select()
    .from(s)
    .where(and(or(...conds), f.includeDisabled ? undefined : eq(s.enabled, true)))
    .orderBy(asc(s.scope), asc(s.name));
  return rows.filter((r) => canSee(r, user));
}

export async function listServers(p: Principal, f: Partial<ListFilter>): Promise<ServerView[]> {
  return views(await visibleRows(p.user, f));
}

/** The server row if the caller may see it; NOT_FOUND otherwise (existence is not revealed). */
export async function getServerRow(user: PublicUser, id: string): Promise<ServerRow> {
  if (!id) throw new ApiError("invalid_request", "server_id is required");
  await ensureDb();
  const row = await getDb().query.mcpServer.findFirst({ where: eq(schema.mcpServer.id, id) });
  if (!row || !canSee(row, user)) throw new ApiError("not_found", "MCP server not found");
  return row;
}

export async function getServer(p: Principal, id: string): Promise<ServerView> {
  return (await views([await getServerRow(p.user, id)]))[0]!;
}

/** Decrypted secret values of a server. Only for the proxy and IssueStdioLaunch; never serialize. */
export async function loadSecrets(serverId: string): Promise<Map<string, string>> {
  await ensureDb();
  const rows = await getDb().select().from(schema.mcpSecret).where(eq(schema.mcpSecret.serverId, serverId));
  return new Map(rows.map((r) => [r.name, decryptMcp(r.valueEnc, secretAad(serverId, r.name))]));
}

// --- writes ------------------------------------------------------------------------------------

async function applySecrets(tx: Tx, serverId: string, ch: SecretChangesInput, now: Date): Promise<string[]> {
  for (const [name, value] of Object.entries(ch.set)) {
    const valueEnc = encryptMcp(value, secretAad(serverId, name));
    await tx
      .insert(schema.mcpSecret)
      .values({ serverId, name, valueEnc, updatedAt: now })
      .onConflictDoUpdate({ target: [schema.mcpSecret.serverId, schema.mcpSecret.name], set: { valueEnc, updatedAt: now } });
  }
  if (ch.remove.length) {
    await tx.delete(schema.mcpSecret).where(and(eq(schema.mcpSecret.serverId, serverId), inArray(schema.mcpSecret.name, ch.remove)));
  }
  const rows = await tx.select({ name: schema.mcpSecret.name }).from(schema.mcpSecret).where(eq(schema.mcpSecret.serverId, serverId));
  return rows.map((r) => r.name);
}

function notification(row: ServerRow, userId: string, deleted: boolean) {
  return {
    ownerId: row.ownerId,
    scope: row.scope,
    projectKey: row.projectKey,
    kind: DocumentKind.MCP_SERVER,
    key: row.id,
    targetRoles: roles(row),
    userId,
    deleted,
    source: "mcp",
  };
}

async function assertNameFree(tx: Tx, row: { ownerId: string; scope: number; projectKey: string; name: string }, exceptId?: string) {
  const s = schema.mcpServer;
  const [clash] = await tx
    .select({ id: s.id })
    .from(s)
    .where(and(eq(s.ownerId, row.ownerId), eq(s.scope, row.scope), eq(s.projectKey, row.projectKey), eq(s.name, row.name)));
  if (clash && clash.id !== exceptId) throw new ApiError("conflict", `An MCP server named ${row.name} already exists in this scope`);
}

export async function createServer(p: Principal, input: ServerInput | undefined, secretChanges?: SecretChangesInput): Promise<ServerView> {
  const v = validateServer(input, p.user.role === "admin");
  const ch = validateSecretChanges(secretChanges);
  if (ch.remove.length) throw new ApiError("invalid_request", "secrets.remove is not allowed on create");
  const id = newId("mcp");
  const ownerId = v.scope === Scope.ORG ? "" : p.user.id;
  const row = await runSyncWrite(async (tx) => {
    await assertNameFree(tx, { ownerId, scope: v.scope, projectKey: v.projectKey, name: v.name });
    const now = new Date();
    const version = await nextSyncVersion(tx);
    const [inserted] = await tx
      .insert(schema.mcpServer)
      .values({
        id,
        ownerId,
        scope: v.scope,
        projectKey: v.projectKey,
        name: v.name,
        transport: v.transport,
        enabled: v.enabled,
        alwaysLoad: v.alwaysLoad,
        config: v.config,
        targetRoles: v.targetRoles,
        version,
        configHash: "",
        createdAt: now,
        updatedAt: now,
        updatedBy: p.user.id,
      })
      .returning();
    const names = await applySecrets(tx, id, ch, now);
    const hash = configHash(v.transport, v.config, [...names, ...secretRefs(v.config)]);
    const [r] = await tx.update(schema.mcpServer).set({ configHash: hash }).where(eq(schema.mcpServer.id, inserted!.id)).returning();
    const doc = await writeNotification(tx, version, notification(r!, p.user.id, false));
    return { value: r!, publish: [doc] };
  });
  return (await views([row]))[0]!;
}

export async function updateServer(
  p: Principal,
  input: (ServerInput & { id: string }) | undefined,
  secretChanges: SecretChangesInput | undefined,
  baseVersion: number,
): Promise<UpdateResult> {
  if (!input?.id) throw new ApiError("invalid_request", "server.id is required");
  const cur0 = await getServerRow(p.user, input.id);
  if (!canWrite(cur0, p.user)) throw new ApiError("permission", "Only admins can manage org MCP servers");
  // Scope and project are fixed for the lifetime of a server.
  const scope = input.scope || cur0.scope;
  if (scope !== cur0.scope) throw new ApiError("invalid_request", "scope cannot be changed; create a new server");
  const projectKey = cur0.scope === Scope.PROJECT ? input.projectKey || cur0.projectKey : input.projectKey;
  if (cur0.scope === Scope.PROJECT && projectKey !== cur0.projectKey) throw new ApiError("invalid_request", "project_key cannot be changed");
  const v = validateServer({ ...input, scope, projectKey }, p.user.role === "admin");
  const ch = validateSecretChanges(secretChanges);

  const result = await runSyncWrite(async (tx): Promise<{ value: { row: ServerRow; conflict: boolean; changed: boolean }; publish: DocRow[] }> => {
    const s = schema.mcpServer;
    const [cur] = await tx.select().from(s).where(eq(s.id, input.id)).for("update");
    if (!cur) throw new ApiError("not_found", "MCP server not found");
    if (cur.version !== baseVersion) return { value: { row: cur, conflict: true, changed: false }, publish: [] };
    await assertNameFree(tx, { ownerId: cur.ownerId, scope: cur.scope, projectKey: cur.projectKey, name: v.name }, cur.id);
    const now = new Date();
    const names = await applySecrets(tx, cur.id, ch, now);
    const hash = configHash(v.transport, v.config, [...names, ...secretRefs(v.config)]);
    const secretsChanged = Object.keys(ch.set).length > 0 || ch.remove.length > 0;
    const same =
      !secretsChanged &&
      hash === cur.configHash &&
      cur.name === v.name &&
      cur.enabled === v.enabled &&
      cur.alwaysLoad === v.alwaysLoad &&
      canonicalJson(cur.config) === canonicalJson(v.config) &&
      JSON.stringify(roles(cur)) === JSON.stringify(v.targetRoles);
    if (same) return { value: { row: cur, conflict: false, changed: false }, publish: [] };
    const version = await nextSyncVersion(tx);
    // Pointing a server somewhere else drops the admin's private-network exemption.
    const keepPrivate = cur.allowPrivateNetwork && urlOrigin(rowConfig(cur).remote?.url) === urlOrigin(v.config.remote?.url);
    const [row] = await tx
      .update(s)
      .set({
        name: v.name,
        transport: v.transport,
        enabled: v.enabled,
        alwaysLoad: v.alwaysLoad,
        config: v.config,
        targetRoles: v.targetRoles,
        allowPrivateNetwork: keepPrivate,
        version,
        configHash: hash,
        updatedAt: now,
        updatedBy: p.user.id,
      })
      .where(eq(s.id, cur.id))
      .returning();
    const doc = await writeNotification(tx, version, notification(row!, p.user.id, false));
    return { value: { row: row!, conflict: false, changed: true }, publish: [doc] };
  });
  if (result.changed) fireChanged(input.id);
  const view = (await views([result.row]))[0]!;
  return result.conflict ? { conflict: view } : { server: view };
}

/** Returns the current server on a version mismatch (conflict), undefined on success. */
export async function deleteServer(p: Principal, id: string, baseVersion?: number): Promise<ServerView | undefined> {
  const cur0 = await getServerRow(p.user, id);
  if (!canWrite(cur0, p.user)) throw new ApiError("permission", "Only admins can manage org MCP servers");
  const conflict = await runSyncWrite(async (tx): Promise<{ value: ServerRow | undefined; publish: DocRow[] }> => {
    const s = schema.mcpServer;
    const [cur] = await tx.select().from(s).where(eq(s.id, id)).for("update");
    if (!cur) throw new ApiError("not_found", "MCP server not found");
    if (baseVersion !== undefined && cur.version !== baseVersion) return { value: cur, publish: [] };
    const version = await nextSyncVersion(tx);
    // Secrets, tokens, flows, device reports and cached tools go with the server (ON DELETE CASCADE).
    await tx.delete(s).where(eq(s.id, id));
    const doc = await writeNotification(tx, version, notification(cur, p.user.id, true));
    return { value: undefined, publish: [doc] };
  });
  if (conflict) return (await views([conflict]))[0]!;
  fireChanged(id);
  return undefined;
}

/**
 * Announces a change that is not a config write (e.g. the caller's authorization state changed) as a new
 * MCP_SERVER notification version, so the caller's other devices refetch. The server's own version and
 * config_hash stay as they are.
 */
export async function announceServer(serverId: string, userId: string): Promise<void> {
  await runSyncWrite(async (tx) => {
    const [row] = await tx.select().from(schema.mcpServer).where(eq(schema.mcpServer.id, serverId));
    if (!row) return { value: undefined, publish: [] };
    const version = await nextSyncVersion(tx);
    const doc = await writeNotification(tx, version, notification(row, userId, false));
    return { value: undefined, publish: [doc] };
  });
}

/** Admin-only flag outside the RPC contract: allow the proxy to reach private / loopback addresses. */
export async function setAllowPrivateNetwork(id: string, allow: boolean): Promise<ServerRow> {
  await ensureDb();
  const [row] = await getDb()
    .update(schema.mcpServer)
    .set({ allowPrivateNetwork: allow })
    .where(eq(schema.mcpServer.id, id))
    .returning();
  if (!row) throw new ApiError("not_found", "MCP server not found");
  fireChanged(id);
  return row;
}

export function isStdio(row: ServerRow): boolean {
  return row.transport === Transport.STDIO;
}
