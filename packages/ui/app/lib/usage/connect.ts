// UsageApi over harness.llm.v1.UsageService, on the same connect transport (and agent token) as AgentService:
// the client forwards these calls to the server (packages/client/server/coreproxy.go).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import {
  UsageBucket,
  UsageService,
  type UsageReport as UsageReportPb,
  type UsageTotals as UsageTotalsPb
} from '~/gen/harness/llm/v1/usage_pb'
import type { UsageApi, UsageRangeInput, UsageReport, UsageTotals } from './types'
import { browserTimeZone, emptyUsageTotals } from './types'

export function totalsView(t: UsageTotalsPb | undefined): UsageTotals {
  if (!t) return emptyUsageTotals()
  return {
    requests: Number(t.requests),
    inputTokens: Number(t.inputTokens),
    outputTokens: Number(t.outputTokens),
    cacheReadTokens: Number(t.cacheReadTokens),
    cacheWriteTokens: Number(t.cacheWriteTokens),
    totalTokens: Number(t.totalTokens),
    costUsd: t.costUsd,
    unpricedTokens: Number(t.unpricedTokens)
  }
}

export function reportView(r: UsageReportPb | undefined): UsageReport {
  return {
    from: r?.from ?? '',
    to: r?.to ?? '',
    bucket: r?.bucket === UsageBucket.HOUR ? 'hour' : 'day',
    timeZone: r?.timeZone ?? '',
    totals: totalsView(r?.totals),
    buckets: (r?.buckets ?? []).map(b => ({
      start: b.start,
      totals: totalsView(b.totals),
      models: b.models.map(m => ({ modelKey: m.modelKey, totals: totalsView(m.totals) }))
    })),
    models: (r?.models ?? []).map(m => ({
      key: m.key,
      modelId: m.modelId,
      displayName: m.displayName,
      upstreamModel: m.upstreamModel,
      providerId: m.providerId,
      providerName: m.providerName,
      priced: m.priced,
      totals: totalsView(m.totals)
    })),
    requestClasses: (r?.requestClasses ?? []).map(c => ({ requestClass: c.requestClass, totals: totalsView(c.totals) }))
  }
}

export function rangePb(r: UsageRangeInput = {}) {
  return {
    from: r.from ?? '',
    to: r.to ?? '',
    bucket: r.bucket === 'hour' ? UsageBucket.HOUR : r.bucket === 'day' ? UsageBucket.DAY : UsageBucket.UNSPECIFIED,
    timeZone: r.timeZone ?? browserTimeZone()
  }
}

export class ConnectUsageApi implements UsageApi {
  private client: Client<typeof UsageService>

  constructor(transport: Transport) {
    this.client = createClient(UsageService, transport)
  }

  async getMyUsage(range?: UsageRangeInput) {
    return reportView((await this.client.getMyUsage({ range: rangePb(range) })).report)
  }

  async getUsage(range?: UsageRangeInput, userId = '') {
    const r = await this.client.getUsage({ range: rangePb(range), userId })
    return {
      report: reportView(r.report),
      users: r.users.map(u => ({
        userId: u.userId,
        name: u.name,
        username: u.username,
        email: u.email,
        totals: totalsView(u.totals),
        lastActiveAt: u.lastActiveAt
      }))
    }
  }
}
