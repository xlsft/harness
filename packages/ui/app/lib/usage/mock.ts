// UsageApi for the mock transport: 90 days of generated requests (the same every run) by three users on four
// models, aggregated like the server does: two priced Anthropic models, a Z.ai Coding Plan model (a plan:
// tokens only) and an OpenRouter model without a price. Buckets start in the browser's time zone whatever
// `timeZone` asks for.

import type { AdminUsageReport, UsageApi, UsageBucketSize, UsageModel, UsageRangeInput, UsageReport, UsageTotals, UsageUser } from './types'
import { browserTimeZone, emptyUsageTotals } from './types'

const HOUR = 3_600_000
const DAY = 24 * HOUR
const HISTORY_DAYS = 90

interface MockModel {
  id: string
  displayName: string
  upstreamModel: string
  providerId: string
  providerName: string
  // USD per 1M tokens; null: tokens only.
  price: { input: number, output: number, cacheRead: number, cacheWrite: number } | null
  // Requests per working hour of a heavy user.
  rate: number
  classes: string[]
}

const MODELS: MockModel[] = [
  { id: 'claude-opus-5-5', displayName: 'Claude Opus 5.5', upstreamModel: 'claude-opus-5-5', providerId: 'anthropic', providerName: 'Anthropic', price: { input: 5, output: 25, cacheRead: 0.5, cacheWrite: 6.25 }, rate: 6, classes: ['main', 'main', 'main', 'subagent', 'compaction'] },
  { id: 'claude-haiku-5-5', displayName: 'Claude Haiku 5.5', upstreamModel: 'claude-haiku-5-5', providerId: 'anthropic', providerName: 'Anthropic', price: { input: 1, output: 5, cacheRead: 0.1, cacheWrite: 1.25 }, rate: 4, classes: ['auxiliary', 'auxiliary', 'subagent'] },
  { id: 'glm-5.3', displayName: 'GLM-5.3', upstreamModel: 'glm-5.3', providerId: 'zai-coding', providerName: 'Z.ai Coding Plan', price: null, rate: 3, classes: ['main', 'subagent'] },
  { id: 'openrouter-x1/qwen/qwen3.5-coder', displayName: 'Qwen3.5 Coder', upstreamModel: 'qwen/qwen3.5-coder', providerId: 'openrouter-x1', providerName: 'OpenRouter', price: null, rate: 1.5, classes: ['main'] }
]

interface MockUser {
  id: string
  name: string
  username: string
  email: string
  // Share of a heavy user's activity, and the day (days ago) the activity stopped.
  activity: number
  stoppedDaysAgo: number
}

// The same people as the mock Users section (lib/users/mock.ts).
const USERS: MockUser[] = [
  { id: 'usr_me', name: 'You', username: 'you', email: 'you@example.com', activity: 1, stoppedDaysAgo: 0 },
  { id: 'usr_ada', name: 'Ada Lovelace', username: 'ada', email: 'ada@example.com', activity: 0.45, stoppedDaysAgo: 0 },
  { id: 'usr_bob', name: 'Bob', username: '', email: 'bob@example.com', activity: 0.3, stoppedDaysAgo: 40 }
]

interface MockRecord {
  at: number
  userId: string
  modelId: string
  requestClass: string
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  cost: number
  priced: boolean
}

// mulberry32: a small seeded generator, so the data is the same on every load.
function random(seed: number) {
  return () => {
    seed = (seed + 0x6D2B79F5) | 0
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function generate(now: number): MockRecord[] {
  const rnd = random(20261103)
  const between = (lo: number, hi: number) => Math.round(lo + rnd() * (hi - lo))
  const out: MockRecord[] = []
  const end = Math.floor(now / HOUR) * HOUR
  for (let at = end - HISTORY_DAYS * DAY; at < end; at += HOUR) {
    const d = new Date(at)
    const weekend = d.getDay() === 0 || d.getDay() === 6
    const working = d.getHours() >= 9 && d.getHours() < 20
    const load = (working ? 1 : 0.06) * (weekend ? 0.25 : 1)
    for (const u of USERS) {
      if (at > now - u.stoppedDaysAgo * DAY) continue
      for (const m of MODELS) {
        // A Poisson-ish count: the expected rate, rounded at random.
        const expected = m.rate * u.activity * load
        let n = Math.floor(expected)
        if (rnd() < expected - n) n++
        for (let i = 0; i < n; i++) {
          const input = between(800, 18_000)
          const output = between(150, 3_500)
          const cacheRead = rnd() < 0.85 ? between(8_000, 140_000) : 0
          const cacheWrite = rnd() < 0.4 ? between(500, 9_000) : 0
          const p = m.price
          const cost = p ? (input * p.input + output * p.output + cacheRead * p.cacheRead + cacheWrite * p.cacheWrite) / 1e6 : 0
          out.push({
            at: at + Math.floor(rnd() * HOUR),
            userId: u.id,
            modelId: m.id,
            requestClass: m.classes[Math.floor(rnd() * m.classes.length)]!,
            input,
            output,
            cacheRead,
            cacheWrite,
            cost,
            priced: !!p
          })
        }
      }
    }
  }
  return out.sort((a, b) => a.at - b.at)
}

function add(t: UsageTotals, r: MockRecord) {
  const tokens = r.input + r.output + r.cacheRead + r.cacheWrite
  t.requests++
  t.inputTokens += r.input
  t.outputTokens += r.output
  t.cacheReadTokens += r.cacheRead
  t.cacheWriteTokens += r.cacheWrite
  t.totalTokens += tokens
  if (r.priced) t.costUsd += r.cost
  else t.unpricedTokens += tokens
}

function rounded(t: UsageTotals): UsageTotals {
  return { ...t, costUsd: Math.round(t.costUsd * 1e8) / 1e8 }
}

const byTokens = <T extends { totals: UsageTotals }>(a: T, b: T) => b.totals.totalTokens - a.totals.totalTokens || b.totals.requests - a.totals.requests

function bucketStart(at: number, bucket: UsageBucketSize) {
  const d = new Date(at)
  if (bucket === 'hour') d.setMinutes(0, 0, 0)
  else d.setHours(0, 0, 0, 0)
  return d.getTime()
}

function nextBucket(at: number, bucket: UsageBucketSize) {
  const d = new Date(at)
  if (bucket === 'hour') return at + HOUR
  d.setDate(d.getDate() + 1)
  return d.getTime()
}

interface Range { from: number, to: number, bucket: UsageBucketSize, timeZone: string }

// The server's defaults and limits (services/usage.ts resolveRange).
function resolve(r: UsageRangeInput, now: number): Range {
  const parse = (v: string | undefined, field: string) => {
    if (!v) return undefined
    const t = Date.parse(v)
    if (Number.isNaN(t)) throw new Error(`${field} must be an RFC 3339 timestamp`)
    return t
  }
  const to = parse(r.to, 'to') ?? now
  const from = parse(r.from, 'from') ?? to - 30 * DAY
  const span = to - from
  if (span <= 0) throw new Error('from must be before to')
  if (span > 400 * DAY) throw new Error('The range is limited to 400 days')
  const bucket = r.bucket ?? (span <= 2 * DAY ? 'hour' : 'day')
  if (bucket === 'hour' && span > 7 * DAY) throw new Error('Hourly buckets are limited to ranges of 7 days')
  return { from, to, bucket, timeZone: r.timeZone || browserTimeZone() }
}

export class MockUsageApi implements UsageApi {
  private generated?: MockRecord[]

  constructor(private readonly admin = true, private readonly selfId = 'usr_me', private readonly now = () => Date.now()) {}

  // Generated on first use, so the mock transport starts without the work.
  private get records() {
    return (this.generated ??= generate(this.now()))
  }

  private report(range: Range, userId?: string): UsageReport {
    const rows = this.records.filter(r => r.at >= range.from && r.at < range.to && (!userId || r.userId === userId))
    const totals = emptyUsageTotals()
    const buckets = new Map<number, { totals: UsageTotals, models: Map<string, UsageTotals> }>()
    for (let at = bucketStart(range.from, range.bucket); at < range.to; at = nextBucket(at, range.bucket)) {
      buckets.set(at, { totals: emptyUsageTotals(), models: new Map() })
    }
    const models = new Map<string, { totals: UsageTotals, priced: number }>()
    const classes = new Map<string, UsageTotals>()
    for (const r of rows) {
      add(totals, r)
      const b = buckets.get(bucketStart(r.at, range.bucket))!
      add(b.totals, r)
      if (!b.models.has(r.modelId)) b.models.set(r.modelId, emptyUsageTotals())
      add(b.models.get(r.modelId)!, r)
      if (!models.has(r.modelId)) models.set(r.modelId, { totals: emptyUsageTotals(), priced: 0 })
      const m = models.get(r.modelId)!
      add(m.totals, r)
      if (r.priced) m.priced++
      if (!classes.has(r.requestClass)) classes.set(r.requestClass, emptyUsageTotals())
      add(classes.get(r.requestClass)!, r)
    }
    const modelList: UsageModel[] = [...models.entries()].map(([id, m]) => {
      const meta = MODELS.find(x => x.id === id)!
      return {
        key: id,
        modelId: id,
        displayName: meta.displayName,
        upstreamModel: meta.upstreamModel,
        providerId: meta.providerId,
        providerName: meta.providerName,
        priced: m.priced > 0,
        totals: rounded(m.totals)
      }
    }).sort(byTokens)
    const order = new Map(modelList.map((m, i) => [m.key, i]))
    return {
      from: new Date(range.from).toISOString(),
      to: new Date(range.to).toISOString(),
      bucket: range.bucket,
      timeZone: range.timeZone,
      totals: rounded(totals),
      buckets: [...buckets.entries()].map(([at, b]) => ({
        start: new Date(at).toISOString(),
        totals: rounded(b.totals),
        models: [...b.models.entries()]
          .map(([modelKey, t]) => ({ modelKey, totals: rounded(t) }))
          .sort((x, y) => order.get(x.modelKey)! - order.get(y.modelKey)!)
      })),
      models: modelList,
      requestClasses: [...classes.entries()].map(([requestClass, t]) => ({ requestClass, totals: rounded(t) })).sort(byTokens)
    }
  }

  async getMyUsage(range?: UsageRangeInput) {
    return this.report(resolve(range ?? {}, this.now()), this.selfId)
  }

  async getUsage(range?: UsageRangeInput, userId = ''): Promise<AdminUsageReport> {
    if (!this.admin) throw new Error('Admin role required')
    const r = resolve(range ?? {}, this.now())
    const users: UsageUser[] = []
    for (const u of USERS) {
      if (userId && u.id !== userId) continue
      const rows = this.records.filter(x => x.userId === u.id && x.at >= r.from && x.at < r.to)
      if (!rows.length) continue
      const totals = emptyUsageTotals()
      for (const x of rows) add(totals, x)
      users.push({ userId: u.id, name: u.name, username: u.username, email: u.email, totals: rounded(totals), lastActiveAt: new Date(rows.at(-1)!.at).toISOString() })
    }
    return { report: this.report(r, userId || undefined), users: users.sort(byTokens) }
  }
}
