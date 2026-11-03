// UsageApi: what the Settings > Usage tab needs from harness.llm.v1.UsageService (tokens, requests and cost over
// a time range, by bucket, model and request class; for admins also by user). The client forwards these calls
// to the server with the user's credentials. Two implementations: connect.ts (the real service through the
// client) and mock.ts (generated data, for the mock transport).
//
// Cost policy: only pay-as-you-go providers' priced models carry a cost. A model without a price, or any model
// of a subscription / coding plan provider, counts tokens only: show no cost for it (unknown, not $0). Such
// tokens are reported as `unpricedTokens`; a `costUsd` is complete only while `unpricedTokens` is 0.

export type UsageBucketSize = 'hour' | 'day'

export interface UsageRangeInput {
  // RFC 3339. Omitted `to`: now; omitted `from`: 30 days before `to`. At most 400 days.
  from?: string
  to?: string
  // Omitted: hours for ranges up to 2 days, days otherwise. Hours are allowed for up to 7 days.
  bucket?: UsageBucketSize
  // IANA zone the buckets start in; omitted: the browser's.
  timeZone?: string
}

// totalTokens = input + output + cache read + cache write.
export interface UsageTotals {
  requests: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  totalTokens: number
  costUsd: number
  // Tokens of requests without a cost (no price, or a plan provider).
  unpricedTokens: number
}

export interface UsageBucketModel {
  // UsageModel.key.
  modelKey: string
  totals: UsageTotals
}

export interface UsageBucketPoint {
  // RFC 3339 (UTC) start of the bucket: local midnight or the full hour in the report's time zone.
  start: string
  totals: UsageTotals
  // Models with requests in the bucket, in the order of UsageReport.models (for stacked bars).
  models: UsageBucketModel[]
}

export interface UsageModel {
  key: string
  modelId: string
  displayName: string
  upstreamModel: string
  providerId: string
  providerName: string
  // false: show no cost (unknown). true with totals.unpricedTokens > 0: the cost covers part of its tokens.
  priced: boolean
  totals: UsageTotals
}

export interface UsageRequestClass {
  // 'main' | 'subagent' | 'compaction' | 'auxiliary' (titles, classifier, summaries) | 'other'.
  requestClass: string
  totals: UsageTotals
}

export interface UsageReport {
  // The range served (after defaults), RFC 3339 UTC, and the bucket size used.
  from: string
  to: string
  bucket: UsageBucketSize
  timeZone: string
  totals: UsageTotals
  // Every bucket of the range in order, empty ones included.
  buckets: UsageBucketPoint[]
  // Largest first by total tokens.
  models: UsageModel[]
  requestClasses: UsageRequestClass[]
}

export interface UsageUser {
  userId: string
  name: string
  username: string
  email: string
  totals: UsageTotals
  // RFC 3339: the last request in the range.
  lastActiveAt: string
}

export interface AdminUsageReport {
  report: UsageReport
  // Users with requests in the range, largest first by total tokens.
  users: UsageUser[]
}

export interface UsageApi {
  // The caller's own usage.
  getMyUsage(range?: UsageRangeInput): Promise<UsageReport>
  // Admin only (PERMISSION_DENIED otherwise): everyone's usage, or one user's, with the per-user table.
  getUsage(range?: UsageRangeInput, userId?: string): Promise<AdminUsageReport>
}

export function emptyUsageTotals(): UsageTotals {
  return { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, totalTokens: 0, costUsd: 0, unpricedTokens: 0 }
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}
