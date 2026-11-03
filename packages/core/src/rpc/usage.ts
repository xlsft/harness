/**
 * Connect implementation of harness.llm.v1.UsageService: proto <-> src/services/usage.ts. GetMyUsage reports
 * the caller's own usage; GetUsage (everyone, or one user, with the per-user table) needs the admin role.
 */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { UsageBucket, UsageService, type UsageRange } from "../gen/harness/llm/v1/usage_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { usageByUser, usageReport, type UsageRangeInput, type UsageReport, type UsageTotals } from "../services/usage.ts";
import { isLocalModelId, recordLocalUsage } from "../services/local-models.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

function rangeFromProto(r: UsageRange | undefined): UsageRangeInput {
  if (!r) return {};
  const bucket = r.bucket === UsageBucket.HOUR ? "hour" : r.bucket === UsageBucket.DAY ? "day" : r.bucket === UsageBucket.UNSPECIFIED ? undefined : null;
  if (bucket === null) throw new ApiError("invalid_request", "Unknown bucket size");
  return { from: r.from, to: r.to, bucket, time_zone: r.timeZone };
}

function totalsToProto(t: UsageTotals) {
  return {
    requests: BigInt(t.requests),
    inputTokens: BigInt(t.input_tokens),
    outputTokens: BigInt(t.output_tokens),
    cacheReadTokens: BigInt(t.cache_read_tokens),
    cacheWriteTokens: BigInt(t.cache_write_tokens),
    totalTokens: BigInt(t.total_tokens),
    costUsd: t.cost_usd,
    unpricedTokens: BigInt(t.unpriced_tokens),
  };
}

function reportToProto(r: UsageReport) {
  return {
    from: r.from,
    to: r.to,
    bucket: r.bucket === "hour" ? UsageBucket.HOUR : UsageBucket.DAY,
    timeZone: r.time_zone,
    totals: totalsToProto(r.totals),
    buckets: r.buckets.map((b) => ({
      start: b.start,
      totals: totalsToProto(b.totals),
      models: b.models.map((m) => ({ modelKey: m.model_key, totals: totalsToProto(m.totals) })),
    })),
    models: r.models.map((m) => ({
      key: m.key,
      modelId: m.model_id,
      displayName: m.display_name,
      upstreamModel: m.upstream_model,
      providerId: m.provider_id,
      providerName: m.provider_name,
      priced: m.priced,
      totals: totalsToProto(m.totals),
      local: isLocalModelId(m.model_id),
    })),
    requestClasses: r.request_classes.map((c) => ({ requestClass: c.request_class, totals: totalsToProto(c.totals) })),
  };
}

export function registerUsageService(router: ConnectRouter): void {
  router.service(UsageService, {
    getMyUsage: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      return { report: reportToProto(await usageReport(rangeFromProto(req.range), user.id)) };
    }),

    reportLocalUsage: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const accepted = await recordLocalUsage(
        p,
        req.records.map((r) => ({
          id: r.id,
          model_id: r.modelId,
          provider_id: r.providerId,
          request_class: r.requestClass,
          session_id: r.sessionId,
          agent_id: r.agentId,
          input_tokens: Number(r.inputTokens),
          output_tokens: Number(r.outputTokens),
          cache_read_tokens: Number(r.cacheReadTokens),
          status: r.status,
          stop_reason: r.stopReason,
          duration_ms: Number(r.durationMs),
          created_at: r.createdAt,
        })),
      );
      return { accepted };
    }),

    getUsage: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      if (user.role !== "admin") throw new ApiError("permission", "Admin role required");
      const range = rangeFromProto(req.range);
      const userId = req.userId || undefined;
      // One clock for both queries, so a defaulted `to` is the same instant in the report and the user table.
      const now = new Date();
      const [report, users] = await Promise.all([usageReport(range, userId, now), usageByUser(range, userId, now)]);
      return {
        report: reportToProto(report),
        users: users.map((u) => ({
          userId: u.user_id,
          name: u.name,
          username: u.username,
          email: u.email,
          totals: totalsToProto(u.totals),
          lastActiveAt: u.last_active_at,
        })),
      };
    }),
  });
}
