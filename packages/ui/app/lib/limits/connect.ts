// LimitsApi on the same connect transport (and agent token) as AgentService: GetProviderLimits is an
// AgentService method the client forwards; budgets (ProviderLimitsService) and the visibility policies
// (AdminService) go through the client's core proxy (packages/client/server/coreproxy.go).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import { AdminService } from '~/gen/harness/auth/v1/admin_pb'
import {
  LimitsAudience as LimitsAudiencePb,
  ProviderLimitsService,
  type LimitsVisibility as LimitsVisibilityPb,
  type ProviderLimits as ProviderLimitsPb,
  type ProviderLimitsPolicy as ProviderLimitsPolicyPb
} from '~/gen/harness/llm/v1/limits_pb'
import { ProviderScope } from '~/gen/harness/llm/v1/providers_pb'
import type { LimitsApi, LimitsAudience, LimitsVisibility, ProviderLimitsPolicy, ProviderLimitsResult, ProviderLimitsView } from './types'
import { EVERYONE } from './types'

export function limitsView(l: ProviderLimitsPb): ProviderLimitsView {
  return {
    providerId: l.providerId,
    providerName: l.providerName,
    scope: l.scope === ProviderScope.PERSONAL ? 'personal' : 'global',
    billing: l.billing,
    balance: l.balance
      ? {
          currency: l.balance.currency,
          balance: l.balance.balance,
          used: l.balance.used,
          total: l.balance.total,
          label: l.balance.label,
          updatedAt: l.balance.updatedAt,
          source: l.balance.source,
          error: l.balance.error || undefined
        }
      : undefined,
    spend: l.spend
      ? { currency: l.spend.currency, spent: l.spend.spent, budget: l.spend.budget, periodStart: l.spend.periodStart, partial: l.spend.partial }
      : undefined,
    windows: l.windows.map(w => ({ label: w.label, usedPercent: w.usedPercent, remaining: w.remaining, limit: w.limit, resetsAt: w.resetsAt })),
    windowsUpdatedAt: l.windowsUpdatedAt
  }
}

const AUDIENCE_PB: Record<LimitsAudience, LimitsAudiencePb> = {
  everyone: LimitsAudiencePb.EVERYONE,
  selected: LimitsAudiencePb.SELECTED_USERS,
  admins: LimitsAudiencePb.ADMINS_ONLY
}

function visibilityView(v: LimitsVisibilityPb | undefined): LimitsVisibility {
  if (!v) return { ...EVERYONE }
  const audience: LimitsAudience = v.audience === LimitsAudiencePb.SELECTED_USERS ? 'selected' : v.audience === LimitsAudiencePb.ADMINS_ONLY ? 'admins' : 'everyone'
  return { audience, userIds: audience === 'selected' ? [...v.userIds] : [] }
}

export function policyView(p: ProviderLimitsPolicyPb | undefined): ProviderLimitsPolicy {
  return { providerId: p?.providerId ?? '', balance: visibilityView(p?.balance), limits: visibilityView(p?.limits) }
}

export function policyPb(p: ProviderLimitsPolicy) {
  const v = (x: LimitsVisibility) => ({ audience: AUDIENCE_PB[x.audience], userIds: x.audience === 'selected' ? x.userIds : [] })
  return { providerId: p.providerId, balance: v(p.balance), limits: v(p.limits) }
}

export class ConnectLimitsApi implements LimitsApi {
  private agent: Client<typeof AgentService>
  private limits: Client<typeof ProviderLimitsService>
  private admin: Client<typeof AdminService>

  constructor(transport: Transport) {
    this.agent = createClient(AgentService, transport)
    this.limits = createClient(ProviderLimitsService, transport)
    this.admin = createClient(AdminService, transport)
  }

  async getProviderLimits(opts: { modelId?: string, providerIds?: string[] } = {}): Promise<ProviderLimitsResult> {
    const res = await this.agent.getProviderLimits({ modelId: opts.modelId ?? '', providerIds: opts.providerIds ?? [] })
    return { providers: res.providers.map(limitsView), model: res.model ? limitsView(res.model) : undefined }
  }

  async setProviderBudget(providerId: string, monthlyUsd: number | undefined): Promise<ProviderLimitsView> {
    const res = await this.limits.setProviderBudget({ providerId, monthlyUsd })
    if (!res.limits) throw new Error('the server returned no limits')
    return limitsView(res.limits)
  }

  async listPolicies(): Promise<ProviderLimitsPolicy[]> {
    return (await this.admin.listProviderLimitsPolicies({})).policies.map(policyView)
  }

  async setPolicy(policy: ProviderLimitsPolicy): Promise<ProviderLimitsPolicy> {
    return policyView((await this.admin.setProviderLimitsPolicy({ policy: policyPb(policy) })).policy)
  }
}
