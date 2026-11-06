/**
 * Connect implementation of harness.llm.v1.ProviderLimitsService (src/limits/service.ts), plus the proto
 * conversions of the visibility policies that AdminService serves (rpc/admin.ts).
 */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { ProviderScope } from "../gen/harness/llm/v1/providers_pb.ts";
import {
  LimitsAudience,
  ProviderLimitsService,
  type LimitsVisibility,
  type ProviderLimits as ProviderLimitsPb,
  type ProviderLimitsPolicy as ProviderLimitsPolicyPb,
} from "../gen/harness/llm/v1/limits_pb.ts";
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { ProviderLimitsSchema, ProviderLimitsPolicySchema } from "../gen/harness/llm/v1/limits_pb.ts";
import type { LimitWindow } from "../limits/headers.ts";
import { getProviderLimits, setProviderBalanceEstimate, setProviderBudget, type ProviderLimits } from "../limits/service.ts";
import type { Audience, ProviderPolicy, Visibility } from "../limits/policy.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

function windowToProto(w: LimitWindow) {
  return { label: w.label, usedPercent: w.used_percent, remaining: w.remaining, limit: w.limit, resetsAt: w.resets_at };
}

export function limitsToProto(l: ProviderLimits): MessageInitShape<typeof ProviderLimitsSchema> {
  return {
    providerId: l.provider_id,
    providerName: l.provider_name,
    scope: l.scope === "personal" ? ProviderScope.PERSONAL : ProviderScope.GLOBAL,
    billing: l.billing,
    balance: l.balance
      ? {
          currency: l.balance.currency,
          balance: l.balance.balance,
          used: l.balance.used,
          total: l.balance.total,
          label: l.balance.label,
          updatedAt: l.balance.updated_at,
          source: l.balance.source,
          error: l.balance.error ?? "",
          estimated: l.balance.estimated ?? false,
          estimatedSince: l.balance.estimated_since ?? "",
        }
      : undefined,
    spend: l.spend
      ? { currency: l.spend.currency, spent: l.spend.spent, budget: l.spend.budget, periodStart: l.spend.period_start, partial: l.spend.partial }
      : undefined,
    windows: l.windows.map(windowToProto),
    windowsUpdatedAt: l.windows_updated_at,
    windowsSource: l.windows_source,
    balanceEntry: l.balance_entry,
    rateWindows: l.rate_windows.map(windowToProto),
  };
}

const AUDIENCE_TO_PROTO: Record<Audience, LimitsAudience> = {
  everyone: LimitsAudience.EVERYONE,
  selected: LimitsAudience.SELECTED_USERS,
  admins: LimitsAudience.ADMINS_ONLY,
};

function audienceFromProto(a: LimitsAudience): Audience {
  if (a === LimitsAudience.SELECTED_USERS) return "selected";
  if (a === LimitsAudience.ADMINS_ONLY) return "admins";
  return "everyone";
}

export function policyToProto(p: ProviderPolicy): MessageInitShape<typeof ProviderLimitsPolicySchema> {
  const v = (x: Visibility) => ({ audience: AUDIENCE_TO_PROTO[x.audience], userIds: x.user_ids });
  return { providerId: p.provider_id, balance: v(p.balance), limits: v(p.limits) };
}

export function policyFromProto(p: ProviderLimitsPolicyPb | undefined) {
  const v = (x: LimitsVisibility | undefined) => (x ? { audience: audienceFromProto(x.audience), user_ids: x.userIds } : undefined);
  return { provider_id: p?.providerId ?? "", balance: v(p?.balance), limits: v(p?.limits) };
}

export type { ProviderLimitsPb };

export function registerProviderLimitsService(router: ConnectRouter): void {
  router.service(ProviderLimitsService, {
    getProviderLimits: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      const res = await getProviderLimits(user, { provider_ids: req.providerIds, model_id: req.modelId });
      return { providers: res.providers.map(limitsToProto), model: res.model ? limitsToProto(res.model) : undefined };
    }),

    setProviderBudget: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      return { limits: limitsToProto(await setProviderBudget(user, req.providerId, req.monthlyUsd)) };
    }),

    setProviderBalanceEstimate: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      return { limits: limitsToProto(await setProviderBalanceEstimate(user, req.providerId, req.amount, req.asOf || undefined)) };
    }),
  });
}
