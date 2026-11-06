/** Audit log of sensitive MCP operations. Entries never contain secret values or tool arguments. */
import { and, desc, eq, lt, or } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal } from "../services/devices.ts";

export interface AuditEntry {
  userId: string;
  deviceId?: string;
  serverId: string;
  action: "stdio_launch" | "call_tool" | "oauth_completed" | "oauth_revoked";
  configHash?: string;
  detail?: Record<string, unknown>;
}

/** With `required`, a failed write throws (used where an unaudited operation must not proceed). */
export async function audit(e: AuditEntry, required = false): Promise<void> {
  try {
    await ensureDb();
    await getDb()
      .insert(schema.mcpAudit)
      .values({
        id: newId("mau"),
        userId: e.userId,
        deviceId: e.deviceId ?? null,
        serverId: e.serverId,
        action: e.action,
        configHash: e.configHash ?? "",
        detail: e.detail ?? {},
        createdAt: new Date(),
      });
  } catch (err) {
    if (required) throw err;
    // Auditing must not break the operation it records, but a failure is worth a log line.
    console.error("[harness] mcp audit write failed", err);
  }
}

export interface AuditView {
  id: string;
  createdAt: string;
  userId: string;
  userName: string;
  deviceId: string;
  serverId: string;
  serverName: string;
  action: string;
  configHash: string;
  tool: string;
  isError: boolean;
  durationMs: number;
  errorMessage: string;
  sessionId: string;
}

const MAX_ERROR_CHARS = 500;

function encodeCursor(createdAt: Date, id: string): string {
  return Buffer.from(`${createdAt.toISOString()}|${id}`).toString("base64url");
}

function decodeCursor(cursor: string): { at: Date; id: string } {
  const text = Buffer.from(cursor, "base64url").toString();
  const i = text.indexOf("|");
  const at = new Date(text.slice(0, i));
  if (i < 0 || Number.isNaN(at.getTime())) throw new ApiError("invalid_request", "Invalid cursor");
  return { at, id: text.slice(i + 1) };
}

/** The audit log, newest first. Admins only: it says who used which server when. */
export async function listAudit(
  p: Principal,
  f: { serverId: string; userId: string; action: string; limit: number; cursor: string },
): Promise<{ entries: AuditView[]; nextCursor: string }> {
  if (p.user.role !== "admin") throw new ApiError("permission", "Only admins can read the MCP audit log");
  await ensureDb();
  const a = schema.mcpAudit;
  const limit = f.limit > 0 ? Math.min(f.limit, 200) : 50;
  const conds = [];
  if (f.serverId) conds.push(eq(a.serverId, f.serverId));
  if (f.userId) conds.push(eq(a.userId, f.userId));
  if (f.action) conds.push(eq(a.action, f.action));
  if (f.cursor) {
    const c = decodeCursor(f.cursor);
    conds.push(or(lt(a.createdAt, c.at), and(eq(a.createdAt, c.at), lt(a.id, c.id))));
  }
  const rows = await getDb()
    .select({ row: a, userName: schema.user.name, serverName: schema.mcpServer.name })
    .from(a)
    .leftJoin(schema.user, eq(schema.user.id, a.userId))
    .leftJoin(schema.mcpServer, eq(schema.mcpServer.id, a.serverId))
    .where(conds.length ? and(...conds) : undefined)
    .orderBy(desc(a.createdAt), desc(a.id))
    .limit(limit + 1);
  const page = rows.slice(0, limit);
  const entries = page.map(({ row, userName, serverName }) => {
    // Only named fields of the detail leave the table.
    const d = (row.detail ?? {}) as Record<string, unknown>;
    return {
      id: row.id,
      createdAt: row.createdAt.toISOString(),
      userId: row.userId,
      userName: userName ?? "",
      deviceId: row.deviceId ?? "",
      serverId: row.serverId,
      serverName: serverName ?? "",
      action: row.action,
      configHash: row.configHash,
      tool: typeof d.tool === "string" ? d.tool : "",
      isError: d.isError === true || typeof d.error === "string",
      durationMs: typeof d.ms === "number" ? d.ms : 0,
      errorMessage: typeof d.error === "string" ? d.error.slice(0, MAX_ERROR_CHARS) : "",
      sessionId: typeof d.sessionId === "string" ? d.sessionId : "",
    };
  });
  const last = page.at(-1);
  return { entries, nextCursor: rows.length > limit && last ? encodeCursor(last.row.createdAt, last.row.id) : "" };
}
