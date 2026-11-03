// Provider limits: how much money or quota each LLM provider has left (harness.llm.v1.ProviderLimitsService).
// A provider may carry a balance (credit read from its API), this month's spend with an optional budget, and
// rate-limit windows from response headers, side by side; the server leaves out what the user may not see.
// GetProviderLimits goes through the client (AgentService.GetProviderLimits); budgets and the admin visibility
// policies through the client's core proxy.

export interface ProviderBalanceView {
  // ISO 4217 ("USD", "CNY").
  currency: string
  // What is left (the key's remaining limit, or the account's remaining credit).
  balance?: number
  // Spent from `total`.
  used?: number
  // The cap: the key limit or the credits bought.
  total?: number
  // "key limit", "credits" or "key usage".
  label: string
  updatedAt: string
  source: string
  // The last read failed; the amounts are empty then.
  error?: string
}

export interface ProviderSpendView {
  currency: string
  // This calendar month (UTC), from the server's usage records.
  spent: number
  budget?: number
  periodStart: string
  // Some requests carried no price (plans count tokens only), so `spent` is a lower bound.
  partial: boolean
}

export interface LimitWindowView {
  // "5h", "weekly", "requests/min", "tokens/min", ...
  label: string
  // 0..100.
  usedPercent: number
  remaining?: number
  limit?: number
  // RFC 3339; empty when unknown.
  resetsAt: string
}

export interface ProviderLimitsView {
  providerId: string
  providerName: string
  scope: 'global' | 'personal'
  billing: 'metered' | 'plan' | string
  balance?: ProviderBalanceView
  spend?: ProviderSpendView
  windows: LimitWindowView[]
  windowsUpdatedAt: string
}

export interface ProviderLimitsResult {
  providers: ProviderLimitsView[]
  // The provider behind the requested model id (the current session's provider).
  model?: ProviderLimitsView
}

// Who may see one block of a server provider's limits (admins always may).
export type LimitsAudience = 'everyone' | 'selected' | 'admins'

export interface LimitsVisibility {
  audience: LimitsAudience
  // With 'selected'.
  userIds: string[]
}

// A server provider's two independent policies. Personal providers have none: only their owner sees them.
export interface ProviderLimitsPolicy {
  providerId: string
  // Who sees the balance.
  balance: LimitsVisibility
  // Who sees the spend and the rate-limit windows.
  limits: LimitsVisibility
}

export interface LimitsApi {
  // Limits of the providers the user can use; with `modelId`, also its provider as `model`.
  getProviderLimits(opts?: { modelId?: string, providerIds?: string[] }): Promise<ProviderLimitsResult>
  // Sets (or with undefined clears) a provider's monthly budget in USD: admins for server providers, the owner
  // for a personal one.
  setProviderBudget(providerId: string, monthlyUsd: number | undefined): Promise<ProviderLimitsView>
  // Admins: every server LLM provider's visibility policies.
  listPolicies(): Promise<ProviderLimitsPolicy[]>
  // Admins: replaces one server provider's policies.
  setPolicy(policy: ProviderLimitsPolicy): Promise<ProviderLimitsPolicy>
}

export const EVERYONE: LimitsVisibility = { audience: 'everyone', userIds: [] }
