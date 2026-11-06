/**
 * Tool catalog for ToolSearch (GetToolCatalog): tool names, descriptions and schemas of every enabled
 * server visible to the caller, as a frozen, versioned snapshot.
 *
 * - Remote servers: the tool list fetched through the proxy, cached per (server, user) in mcp_tool_cache
 *   and refreshed when older than HARNESS_MCP_TOOLS_TTL_MS (default 5 min) or fetched with another
 *   config_hash; a failed refresh falls back to the last list. stdio servers: the latest list reported
 *   by one of the caller's devices (preferring reports for the current config_hash).
 * - version = "cat_" + SHA-256 over (user, project_key, servers with their tools) — content-addressed, so it
 *   changes exactly when a tool list (or the server set) changes. Refresh timestamps are not part of it.
 * - Each version is stored once (mcp_catalog_snapshot) and returned unchanged on later fetches. Snapshots
 *   stay fetchable for HARNESS_MCP_CATALOG_RETENTION_DAYS (>= 7, default 7) after their last use.
 */
import { createHash } from "node:crypto";
import { and, eq, gt, inArray, lt } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { AuthState, Transport } from "../gen/harness/mcp/v1/mcp_pb.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal } from "../services/devices.ts";
import { canonicalJson, REMOTE_TRANSPORTS, toolVisible } from "./config.ts";
import { reportedTools } from "./devices.ts";
import { authStatuses } from "./oauth.ts";
import { fetchAllTools, type ToolData, type UpstreamMeta } from "./proxy.ts";
import { listServers, rowConfig, type ServerView } from "./registry.ts";

export interface CatalogServerData {
  serverId: string;
  name: string;
  transport: Transport;
  alwaysLoad: boolean;
  tools: ToolData[];
  toolsUpdatedAt: string;
  /** remote.requires_user_interaction: every call needs an explicit approval. */
  requiresUserInteraction: boolean;
  instructions: string;
  hasPrompts: boolean;
  hasResources: boolean;
}

export interface CatalogData {
  version: string;
  servers: CatalogServerData[];
  createdAt: string;
}

function intEnv(name: string, def: number, min = 1): number {
  const n = Number(process.env[name] ?? "");
  return Number.isFinite(n) && n >= min ? n : def;
}

export const retentionMs = () => Math.max(7, intEnv("HARNESS_MCP_CATALOG_RETENTION_DAYS", 7, 7)) * 86_400_000;
const toolsTtlMs = () => intEnv("HARNESS_MCP_TOOLS_TTL_MS", 300_000);
const fetchTimeoutMs = () => intEnv("HARNESS_MCP_CATALOG_FETCH_TIMEOUT_MS", 10_000);
const TOUCH_EVERY_MS = 3_600_000;

let lastPurge = 0;

/** Deletes snapshots unused for longer than the retention. Returns the number deleted. */
export async function purgeCatalogSnapshots(now = Date.now()): Promise<number> {
  await ensureDb();
  const gone = await getDb()
    .delete(schema.mcpCatalogSnapshot)
    .where(lt(schema.mcpCatalogSnapshot.lastUsedAt, new Date(now - retentionMs())))
    .returning({ v: schema.mcpCatalogSnapshot.version });
  return gone.length;
}

function maybePurge(): void {
  const now = Date.now();
  if (now - lastPurge < 3_600_000) return;
  lastPurge = now;
  purgeCatalogSnapshots(now).catch((err) => console.error("[harness] catalog purge failed", err));
}

type RemoteTools = { tools: ToolData[]; at: Date | undefined; meta: UpstreamMeta };

async function remoteTools(p: Principal, views: ServerView[]): Promise<Map<string, RemoteTools>> {
  const out = new Map<string, RemoteTools>();
  if (!views.length) return out;
  const cached = await getDb()
    .select()
    .from(schema.mcpToolCache)
    .where(and(eq(schema.mcpToolCache.userId, p.user.id), inArray(schema.mcpToolCache.serverId, views.map((v) => v.row.id))));
  const statuses = await authStatuses(p.user, views);
  await Promise.all(
    views.map(async (v) => {
      const c = cached.find((x) => x.serverId === v.row.id);
      const fresh = c && c.configHash === v.row.configHash && Date.now() - c.fetchedAt.getTime() < toolsTtlMs();
      const usable = (x: typeof c): RemoteTools => (x ? { tools: x.tools as ToolData[], at: x.fetchedAt, meta: (x.meta ?? {}) as UpstreamMeta } : { tools: [], at: undefined, meta: {} });
      if (fresh) {
        out.set(v.row.id, usable(c));
        return;
      }
      const st = statuses.get(v.row.id)?.state;
      if (st !== AuthState.CONNECTED && st !== AuthState.NOT_REQUIRED) {
        out.set(v.row.id, c && c.configHash === v.row.configHash ? usable(c) : usable(undefined));
        return;
      }
      try {
        const { tools, meta } = await fetchAllTools(p.user, v, AbortSignal.timeout(fetchTimeoutMs()));
        out.set(v.row.id, { tools, meta, at: new Date() });
      } catch {
        // Unreachable right now: serve the last list we have.
        out.set(v.row.id, usable(c));
      }
    }),
  );
  return out;
}

async function buildCurrent(p: Principal, projectKey: string): Promise<{ version: string; servers: CatalogServerData[] }> {
  const scopes = projectKey ? [Scope.USER, Scope.ORG, Scope.PROJECT] : [Scope.USER, Scope.ORG];
  const views = await listServers(p, { scopes, projectKey });
  const remote = await remoteTools(p, views.filter((v) => REMOTE_TRANSPORTS.has(v.row.transport)));
  const stdioIds = views.filter((v) => !REMOTE_TRANSPORTS.has(v.row.transport)).map((v) => v.row.id);
  const reported = await reportedTools(p.user.id, stdioIds);
  const servers: CatalogServerData[] = views.map((v) => {
    let tools: ToolData[] = [];
    let at: Date | undefined;
    let meta: UpstreamMeta = {};
    if (REMOTE_TRANSPORTS.has(v.row.transport)) {
      const r = remote.get(v.row.id);
      tools = r?.tools ?? [];
      at = r?.at;
      meta = r?.meta ?? {};
    } else {
      const reports = (reported.get(v.row.id) ?? []).sort((a, b) => b.at.getTime() - a.at.getTime());
      const best = reports.find((r) => r.configHash === v.row.configHash) ?? reports[0];
      tools = best?.tools ?? [];
      at = best?.at;
    }
    return {
      serverId: v.row.id,
      name: v.row.name,
      transport: v.row.transport,
      alwaysLoad: v.row.alwaysLoad,
      // allowed_tools and denied_tools: a hidden tool is not in the catalog.
      tools: tools.filter((t) => toolVisible(rowConfig(v.row), t.name)),
      toolsUpdatedAt: at?.toISOString() ?? "",
      requiresUserInteraction: !!rowConfig(v.row).remote?.requiresUserInteraction,
      instructions: meta.instructions ?? "",
      hasPrompts: !!meta.hasPrompts,
      hasResources: !!meta.hasResources,
    };
  });
  servers.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : a.serverId < b.serverId ? -1 : 1));
  const identity = servers.map(({ toolsUpdatedAt: _, ...rest }) => rest);
  const digest = createHash("sha256")
    .update(`harness-mcp-catalog-v1\0${p.user.id}\0${projectKey}\0${canonicalJson(identity)}`)
    .digest("hex");
  return { version: `cat_${digest.slice(0, 40)}`, servers };
}

/** Stored snapshot servers (older snapshots predate requiresUserInteraction and the upstream's own description). */
function snapshotServers(v: unknown): CatalogServerData[] {
  return (Array.isArray(v) ? (v as CatalogServerData[]) : []).map((s) => ({
    ...s,
    requiresUserInteraction: !!s.requiresUserInteraction,
    instructions: s.instructions ?? "",
    hasPrompts: !!s.hasPrompts,
    hasResources: !!s.hasResources,
  }));
}

export async function getToolCatalog(
  p: Principal,
  req: { version: string; ifNoneMatch: string; projectKey: string },
): Promise<{ notModified: boolean; catalog?: CatalogData }> {
  await ensureDb();
  maybePurge();
  const db = getDb();
  const cs = schema.mcpCatalogSnapshot;
  const now = new Date();
  if (req.version) {
    const snap = await db.query.mcpCatalogSnapshot.findFirst({
      where: and(eq(cs.version, req.version), eq(cs.userId, p.user.id), gt(cs.lastUsedAt, new Date(now.getTime() - retentionMs()))),
    });
    if (!snap) throw new ApiError("not_found", "Unknown or expired catalog version; fetch the current catalog");
    if (now.getTime() - snap.lastUsedAt.getTime() > TOUCH_EVERY_MS) await db.update(cs).set({ lastUsedAt: now }).where(eq(cs.version, snap.version));
    return { notModified: false, catalog: { version: snap.version, servers: snapshotServers(snap.servers), createdAt: snap.createdAt.toISOString() } };
  }
  if (req.projectKey && req.projectKey.length > 300) throw new ApiError("invalid_request", "project_key is too long");
  const cur = await buildCurrent(p, req.projectKey);
  const [snap] = await db
    .insert(cs)
    .values({ version: cur.version, userId: p.user.id, projectKey: req.projectKey, servers: cur.servers, createdAt: now, lastUsedAt: now })
    .onConflictDoUpdate({ target: cs.version, set: { lastUsedAt: now } })
    .returning();
  if (req.ifNoneMatch && req.ifNoneMatch === cur.version) return { notModified: true };
  return { notModified: false, catalog: { version: snap!.version, servers: snapshotServers(snap!.servers), createdAt: snap!.createdAt.toISOString() } };
}
