/**
 * Usage records as CSV. Everyone may export their own; those who may read usage (auth/roles.ts) may export everyone's, with
 * records on somebody's personal provider reduced to the tokens: which model and what it cost is that user's business.
 */
import { and, desc, eq, gte, inArray, lt, type SQL } from "drizzle-orm";
import { CSV_MAX_BYTES, CSV_MAX_ROWS, csvRow } from "../audit/log.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";

export interface UsageExportInput {
  from?: string;
  to?: string;
  user_id?: string;
  group_id?: string;
}

const HEADER = ["time", "user_id", "email", "name", "session_id", "provider", "model", "upstream_model", "request_class", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "status", "duration_ms"];
const DEFAULT_SPAN_MS = 30 * 86_400_000;

function time(v: string | undefined, field: string): Date | undefined {
  if (!v) return undefined;
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) throw new ApiError("invalid_request", `${field} must be an RFC 3339 timestamp`);
  return d;
}

/** `viewerId`: whose personal records may be shown in full. */
export async function usageCsv(input: UsageExportInput, viewerId: string): Promise<{ csv: string; rows: number; truncated: boolean }> {
  await ensureDb();
  const db = getDb();
  const to = time(input.to, "to") ?? new Date();
  const from = time(input.from, "from") ?? new Date(to.getTime() - DEFAULT_SPAN_MS);
  const ur = schema.usageRecord;
  const conds: SQL[] = [gte(ur.createdAt, from), lt(ur.createdAt, to)];
  if (input.user_id) conds.push(eq(ur.userId, input.user_id));
  if (input.group_id) {
    const members = await db.select({ id: schema.directoryGroupMember.userId }).from(schema.directoryGroupMember).where(eq(schema.directoryGroupMember.groupId, input.group_id));
    if (!members.length) return { csv: csvRow(HEADER), rows: 0, truncated: false };
    conds.push(inArray(ur.userId, members.map((m) => m.id)));
  }
  const rows = await db
    .select({ r: ur, email: schema.user.email, name: schema.user.name, owner: schema.provider.ownerUserId, providerName: schema.provider.displayName })
    .from(ur)
    .leftJoin(schema.user, eq(schema.user.id, ur.userId))
    .leftJoin(schema.provider, eq(schema.provider.id, ur.providerId))
    .where(and(...conds))
    .orderBy(desc(ur.createdAt))
    .limit(CSV_MAX_ROWS + 1);

  let out = csvRow(HEADER);
  let n = 0;
  for (const { r, email, name, owner, providerName } of rows) {
    if (n >= CSV_MAX_ROWS) return { csv: out, rows: n, truncated: true };
    const hidden = !!owner && r.userId !== viewerId;
    const line = csvRow([
      r.createdAt.toISOString(),
      r.userId,
      email ?? "",
      name ?? "",
      hidden ? "" : (r.sessionId ?? ""),
      hidden ? "personal" : (providerName || r.providerId || ""),
      hidden ? "" : r.modelId,
      hidden ? "" : (r.upstreamModel ?? ""),
      hidden ? "" : (r.requestClass ?? ""),
      r.inputTokens,
      r.outputTokens,
      r.cacheReadTokens,
      r.cacheWriteTokens,
      hidden ? "" : r.costUsd,
      r.status,
      r.durationMs,
    ]);
    if (out.length + line.length > CSV_MAX_BYTES) return { csv: out, rows: n, truncated: true };
    out += line;
    n++;
  }
  return { csv: out, rows: n, truncated: false };
}
