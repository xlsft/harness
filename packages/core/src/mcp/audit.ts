/** Audit log of sensitive MCP operations. Entries never contain secret values or tool arguments. */
import { ensureDb, getDb, schema } from "../db/index.ts";
import { newId } from "../lib/ids.ts";

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
