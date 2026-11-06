import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { LimitsAudience, ProviderLimitsPolicySchema, ProviderLimitsSchema } from '~/gen/harness/llm/v1/limits_pb'
import { ProviderScope } from '~/gen/harness/llm/v1/providers_pb'
import { limitsView, policyPb, policyView } from '~/lib/limits/connect'
import { MockLimitsApi } from '~/lib/limits/mock'
import { MockTransport } from '~/lib/transport/mock'

describe('limitsView', () => {
  it('keeps balance, spend and windows side by side and drops empty errors', () => {
    const v = limitsView(create(ProviderLimitsSchema, {
      providerId: 'openrouter',
      providerName: 'OpenRouter',
      scope: ProviderScope.GLOBAL,
      billing: 'metered',
      balance: { currency: 'USD', balance: 12.4, total: 20, used: 7.6, label: 'key limit', error: '' },
      spend: { currency: 'USD', spent: 1, periodStart: '2026-11-01T00:00:00Z' },
      windows: [{ label: 'requests', usedPercent: 10, limit: 20, remaining: 18 }]
    }))
    expect(v.scope).toBe('global')
    expect(v.balance).toMatchObject({ balance: 12.4, total: 20, used: 7.6, label: 'key limit' })
    expect(v.balance!.error).toBeUndefined()
    expect(v.spend).toMatchObject({ spent: 1, budget: undefined })
    expect(v.windows).toEqual([{ label: 'requests', usedPercent: 10, limit: 20, remaining: 18, resetsAt: '' }])
  })

  it('carries an estimate and where one may be entered, and never rate limits', () => {
    const v = limitsView(create(ProviderLimitsSchema, {
      providerId: 'u-1',
      balanceEntry: true,
      balance: { currency: 'USD', balance: 99.99, total: 100, used: 0.01, label: 'estimate', estimated: true, estimatedSince: '2026-11-01T00:00:00Z' },
      windows: [{ label: '5h', usedPercent: 10 }],
      rateWindows: [{ label: 'requests/min', usedPercent: 99 }]
    }))
    expect(v.balanceEntry).toBe(true)
    expect(v.balance).toMatchObject({ balance: 99.99, total: 100, estimated: true, estimatedSince: '2026-11-01T00:00:00Z' })
    expect(v.windows.map(w => w.label)).toEqual(['5h'])
    expect(JSON.stringify(v)).not.toContain('requests/min')
    expect(limitsView(create(ProviderLimitsSchema, { providerId: 'x', balance: { currency: 'USD', balance: 1 } })).balance!.estimated).toBeUndefined()
  })

  it('leaves hidden blocks out', () => {
    const v = limitsView(create(ProviderLimitsSchema, { providerId: 'u-1', scope: ProviderScope.PERSONAL }))
    expect(v.scope).toBe('personal')
    expect(v.balance).toBeUndefined()
    expect(v.spend).toBeUndefined()
    expect(v.windows).toEqual([])
  })
})

describe('policy conversion', () => {
  it('round-trips the two policies; user ids only with selected users', () => {
    const pb = policyPb({ providerId: 'acme', balance: { audience: 'admins', userIds: ['x'] }, limits: { audience: 'selected', userIds: ['usr_1'] } })
    expect(pb.balance).toEqual({ audience: LimitsAudience.ADMINS_ONLY, userIds: [] })
    const back = policyView(create(ProviderLimitsPolicySchema, pb))
    expect(back).toEqual({ providerId: 'acme', balance: { audience: 'admins', userIds: [] }, limits: { audience: 'selected', userIds: ['usr_1'] } })
    expect(policyView(create(ProviderLimitsPolicySchema, { providerId: 'p' })).balance.audience).toBe('everyone')
  })
})

describe('MockLimitsApi', () => {
  it('serves the sample providers and the session model\'s provider', async () => {
    const api = new MockTransport().limits()
    const res = await api.getProviderLimits({ modelId: 'claude-opus-5-5' })
    const by = Object.fromEntries(res.providers.map(p => [p.providerId, p]))
    expect(by.openrouter!.balance).toMatchObject({ balance: 12.4, total: 20, label: 'key limit' })
    expect(by.anthropic!.windows.map(w => [w.label, w.usedPercent])).toEqual([['5h', 63], ['weekly', 21]])
    expect(by.openai!.spend).toMatchObject({ spent: 3.1, budget: 25 })
    expect(res.model?.providerId).toBe('anthropic')
    expect((await api.getProviderLimits({ providerIds: ['openai'] })).providers.map(p => p.providerId)).toEqual(['openai'])
  })

  it('keeps an estimate only where the provider takes one', async () => {
    const api = new MockLimitsApi()
    const set = await api.setBalanceEstimate('openai', 100, '2026-11-01T00:00:00.000Z')
    expect(set.balance).toMatchObject({ balance: 99.99, total: 100, estimated: true, estimatedSince: '2026-11-01T00:00:00.000Z' })
    expect((await api.setBalanceEstimate('openai', 20)).balance).toMatchObject({ balance: 19.99, total: 20 })
    expect((await api.setBalanceEstimate('openai', undefined)).balance).toBeUndefined()
    await expect(api.setBalanceEstimate('openrouter', 1)).rejects.toThrow(/reports its own balance/)
    await expect(api.setBalanceEstimate('anthropic', 1)).rejects.toThrow(/reports its own balance/)
    await expect(api.setBalanceEstimate('openai', -1)).rejects.toThrow(/non-negative/)
  })

  it('keeps budgets and policies', async () => {
    const api = new MockLimitsApi()
    expect((await api.setProviderBudget('openai', 50)).spend!.budget).toBe(50)
    expect((await api.setProviderBudget('openai', undefined)).spend!.budget).toBeUndefined()
    await expect(api.setProviderBudget('openai', -1)).rejects.toThrow()
    const p = await api.setPolicy({ providerId: 'openrouter', balance: { audience: 'admins', userIds: [] }, limits: { audience: 'selected', userIds: ['u1', 'u1'] } })
    expect(p.limits.userIds).toEqual(['u1'])
    const all = await api.listPolicies()
    expect(all.find(x => x.providerId === 'openrouter')!.balance.audience).toBe('admins')
    expect(all.find(x => x.providerId === 'openai')!.balance.audience).toBe('everyone')
  })
})
