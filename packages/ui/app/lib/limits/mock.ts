// LimitsApi for the mock transport: an OpenRouter key with $12.40 left of a $20 limit, an Anthropic plan at 63% of its
// 5h window and 21% of its weekly window, and OpenAI with $3.10 spent this month of a $25 budget, which takes an entered
// balance. Model ids map to these providers by prefix; budgets, estimates and policies live in memory.

import type { LimitsApi, ProviderLimitsPolicy, ProviderLimitsResult, ProviderLimitsView } from './types'
import { EVERYONE } from './types'

const HOUR = 3_600_000
// What the mock's requests "cost" since any estimate was entered.
const SPENT_SINCE_ENTERED = 0.01

function monthStart(now: Date): string {
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1)).toISOString()
}

export function mockProviderLimits(now = new Date()): ProviderLimitsView[] {
  const at = now.toISOString()
  const period = monthStart(now)
  return [
    {
      providerId: 'openrouter',
      providerName: 'OpenRouter',
      scope: 'global',
      billing: 'metered',
      balance: { currency: 'USD', balance: 12.4, used: 7.6, total: 20, label: 'key limit', updatedAt: at, source: 'openrouter:/api/v1/key+/api/v1/credits' },
      spend: { currency: 'USD', spent: 7.6, periodStart: period, partial: false },
      windows: [],
      windowsUpdatedAt: '',
      balanceEntry: false
    },
    {
      providerId: 'anthropic',
      providerName: 'Anthropic',
      scope: 'global',
      billing: 'plan',
      spend: { currency: 'USD', spent: 0, periodStart: period, partial: true },
      windows: [
        { label: '5h', usedPercent: 63, resetsAt: new Date(now.getTime() + 2 * HOUR).toISOString() },
        { label: 'weekly', usedPercent: 21, resetsAt: new Date(now.getTime() + 4 * 24 * HOUR).toISOString() }
      ],
      windowsUpdatedAt: at,
      balanceEntry: false
    },
    {
      providerId: 'openai',
      providerName: 'OpenAI',
      scope: 'global',
      billing: 'metered',
      spend: { currency: 'USD', spent: 3.1, budget: 25, periodStart: period, partial: false },
      windows: [],
      windowsUpdatedAt: '',
      balanceEntry: true
    }
  ]
}

function providerOfModel(modelId: string): string | undefined {
  const id = modelId.toLowerCase()
  if (id.startsWith('claude')) return 'anthropic'
  if (id.startsWith('gpt') || /^o\d/.test(id)) return 'openai'
  if (id.startsWith('openrouter') || id.includes('/')) return 'openrouter'
  return undefined
}

export class MockLimitsApi implements LimitsApi {
  private budgets = new Map<string, number | undefined>()
  private policies = new Map<string, ProviderLimitsPolicy>()
  private estimates = new Map<string, { amount: number, since: string }>()

  private current(): ProviderLimitsView[] {
    return mockProviderLimits().map((l) => {
      const out = { ...l }
      if (l.spend && this.budgets.has(l.providerId)) out.spend = { ...l.spend, budget: this.budgets.get(l.providerId) }
      const e = this.estimates.get(l.providerId)
      if (e) {
        out.balance = {
          currency: 'USD', balance: Math.round((e.amount - SPENT_SINCE_ENTERED) * 1e6) / 1e6, used: SPENT_SINCE_ENTERED, total: e.amount, label: 'estimate',
          updatedAt: new Date().toISOString(), source: 'harness:usage_records', estimated: true, estimatedSince: e.since
        }
      }
      return out
    })
  }

  async getProviderLimits(opts: { modelId?: string, providerIds?: string[] } = {}): Promise<ProviderLimitsResult> {
    const all = this.current()
    const providers = opts.providerIds?.length ? all.filter(l => opts.providerIds!.includes(l.providerId)) : all
    const pid = opts.modelId ? providerOfModel(opts.modelId) : undefined
    return { providers, model: all.find(l => l.providerId === pid) }
  }

  async setProviderBudget(providerId: string, monthlyUsd: number | undefined): Promise<ProviderLimitsView> {
    if (monthlyUsd !== undefined && (!Number.isFinite(monthlyUsd) || monthlyUsd < 0)) throw new Error('monthly_usd must be a non-negative number')
    const found = this.current().find(l => l.providerId === providerId)
    if (!found) throw new Error(`Provider ${providerId} not found`)
    this.budgets.set(providerId, monthlyUsd)
    return this.current().find(l => l.providerId === providerId)!
  }

  async setBalanceEstimate(providerId: string, amount: number | undefined, asOf?: string): Promise<ProviderLimitsView> {
    const found = this.current().find(l => l.providerId === providerId)
    if (!found) throw new Error(`Provider ${providerId} not found`)
    if (amount === undefined) {
      this.estimates.delete(providerId)
    } else {
      if (!found.balanceEntry) throw new Error('This provider reports its own balance, or is a subscription plan')
      if (!Number.isFinite(amount) || amount < 0) throw new Error('amount must be a non-negative number')
      this.estimates.set(providerId, { amount, since: asOf ?? new Date().toISOString() })
    }
    return this.current().find(l => l.providerId === providerId)!
  }

  async listPolicies(): Promise<ProviderLimitsPolicy[]> {
    return mockProviderLimits().map(l => this.policies.get(l.providerId) ?? { providerId: l.providerId, balance: { ...EVERYONE }, limits: { ...EVERYONE } })
  }

  async setPolicy(policy: ProviderLimitsPolicy): Promise<ProviderLimitsPolicy> {
    if (!mockProviderLimits().some(l => l.providerId === policy.providerId)) throw new Error(`Provider ${policy.providerId} not found`)
    const clean = (v: ProviderLimitsPolicy['balance']) => ({ audience: v.audience, userIds: v.audience === 'selected' ? [...new Set(v.userIds)] : [] })
    const p = { providerId: policy.providerId, balance: clean(policy.balance), limits: clean(policy.limits) }
    this.policies.set(p.providerId, p)
    return p
  }
}
