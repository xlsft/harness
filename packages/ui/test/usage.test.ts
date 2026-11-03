import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { UsageBucket, UsageReportSchema } from '~/gen/harness/llm/v1/usage_pb'
import { rangePb, reportView } from '~/lib/usage/connect'
import { MockUsageApi } from '~/lib/usage/mock'
import type { UsageTotals } from '~/lib/usage/types'

const NOW = Date.parse('2026-11-03T12:00:00Z')

describe('usage views', () => {
  it('maps the report to plain numbers', () => {
    const v = reportView(create(UsageReportSchema, {
      from: '2026-10-01T00:00:00.000Z',
      bucket: UsageBucket.HOUR,
      totals: { requests: 3n, totalTokens: 1200n, costUsd: 0.5, unpricedTokens: 200n },
      buckets: [{ start: '2026-10-01T00:00:00.000Z', models: [{ modelKey: 'm', totals: { requests: 1n } }] }],
      models: [{ key: 'm', displayName: 'M', priced: false }]
    }))
    expect(v).toMatchObject({ bucket: 'hour', totals: { requests: 3, totalTokens: 1200, costUsd: 0.5, unpricedTokens: 200, inputTokens: 0 } })
    expect(v.buckets[0]!.models[0]).toEqual({ modelKey: 'm', totals: expect.objectContaining({ requests: 1 }) })
    expect(v.buckets[0]!.totals.requests).toBe(0)
    expect(v.models[0]).toMatchObject({ key: 'm', priced: false })
    expect(reportView(undefined).buckets).toEqual([])
  })

  it('sends the browser time zone unless one is given', () => {
    expect(rangePb({ bucket: 'day', timeZone: 'Europe/Berlin' })).toEqual({ from: '', to: '', bucket: UsageBucket.DAY, timeZone: 'Europe/Berlin' })
    expect(rangePb().bucket).toBe(UsageBucket.UNSPECIFIED)
    expect(rangePb().timeZone).not.toBe('')
  })
})

describe('MockUsageApi', () => {
  const api = new MockUsageApi(true, 'usr_me', () => NOW)
  const sum = (ts: UsageTotals[]) => ts.reduce((n, t) => n + t.totalTokens, 0)

  it('reports 30 days of buckets whose sums match the totals', async () => {
    const r = await api.getMyUsage()
    expect(r.bucket).toBe('day')
    expect(r.buckets.length).toBeGreaterThanOrEqual(30)
    expect(r.totals.requests).toBeGreaterThan(100)
    expect(sum(r.buckets.map(b => b.totals))).toBe(r.totals.totalTokens)
    expect(sum(r.models.map(m => m.totals))).toBe(r.totals.totalTokens)
    expect(sum(r.requestClasses.map(c => c.totals))).toBe(r.totals.totalTokens)
    expect(r.models).toHaveLength(4)
    // plan and unpriced models carry tokens only
    for (const m of r.models) {
      expect(m.priced).toBe(m.totals.costUsd > 0)
      if (!m.priced) expect(m.totals.unpricedTokens).toBe(m.totals.totalTokens)
    }
    expect(r.totals.unpricedTokens).toBe(sum(r.models.filter(m => !m.priced).map(m => m.totals)))
  })

  it('uses hourly buckets for short ranges and validates ranges', async () => {
    const r = await api.getMyUsage({ from: new Date(NOW - 86_400_000).toISOString(), to: new Date(NOW).toISOString() })
    expect(r.bucket).toBe('hour')
    expect(r.buckets).toHaveLength(24)
    await expect(api.getMyUsage({ bucket: 'hour' })).rejects.toThrow(/7 days/)
  })

  it('reports users for admins only, sorted by tokens', async () => {
    const all = await api.getUsage()
    expect(all.users.map(u => u.userId)).toEqual(['usr_me', 'usr_ada'])
    expect(sum(all.users.map(u => u.totals))).toBe(all.report.totals.totalTokens)
    const older = await api.getUsage({ from: new Date(NOW - 80 * 86_400_000).toISOString(), to: new Date(NOW - 50 * 86_400_000).toISOString() })
    expect(older.users.map(u => u.userId)).toContain('usr_bob')
    const one = await api.getUsage(undefined, 'usr_ada')
    expect(one.users.map(u => u.userId)).toEqual(['usr_ada'])
    await expect(new MockUsageApi(false).getUsage()).rejects.toThrow(/Admin/)
  })
})
