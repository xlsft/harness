import { desc, eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";

/** Recent usage records (admin). Aggregations and quotas come in M1. */
export async function listUsage(opts: { userId?: string; limit?: number }) {
  await ensureDb();
  const limit = Math.min(Math.max(1, Number.isFinite(opts.limit) ? opts.limit! : 100), 1000);
  const q = getDb().select().from(schema.usageRecord);
  const rows = await (opts.userId ? q.where(eq(schema.usageRecord.userId, opts.userId)) : q)
    .orderBy(desc(schema.usageRecord.createdAt))
    .limit(limit);
  return {
    usage: rows.map((r) => ({
      id: r.id,
      user_id: r.userId,
      device_id: r.deviceId,
      model: r.modelId,
      provider: r.providerId,
      upstream_model: r.upstreamModel,
      request_class: r.requestClass,
      session_id: r.sessionId,
      input_tokens: r.inputTokens,
      output_tokens: r.outputTokens,
      cache_read_input_tokens: r.cacheReadTokens,
      cache_creation_input_tokens: r.cacheWriteTokens,
      cost_usd: r.costUsd,
      retries: r.retries,
      status: r.status,
      stop_reason: r.stopReason,
      error_type: r.errorType,
      duration_ms: r.durationMs,
      created_at: r.createdAt.toISOString(),
    })),
  };
}
