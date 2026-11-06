/**
 * Provider limits (harness.llm.v1.ProviderLimitsService): per LLM provider the caller can use, an optional
 * balance (limits/balance.ts: read from the provider, or estimated from an amount entered here), this month's spend
 * from our usage records with an optional budget, and the latest windows from response headers (limits/headers.ts),
 * side by side.
 *
 * Only a plan's usage windows are `windows`, the ones every client shows. Rate limits (requests or tokens per minute) go
 * apart as `rate_windows`, which only the client's usage-limit watch reads (packages/client/usagelimit): it warns on
 * them and names them when a provider refuses a request, and that must not change with what is shown.
 *
 * Visibility: global providers are filtered by their two policies (limits/policy.ts; admins see everything);
 * personal providers are only ever listed for their owner (listProvidersForUser never returns another user's),
 * and their spend counts the owner's own records only. Hidden blocks are left out, not flagged.
 */
import { and, eq, gte, inArray, or, sql } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import type { PublicUser } from "../services/devices.ts";
import { resolveModelForUser } from "../services/models.ts";
import { billingOf } from "../services/presets.ts";
import { listProvidersForUser, loadProviderRuntime, providerForUser, type PublicProvider } from "../services/providers.ts";
import { balanceAdapterFor, estimatedBalance, getBalance, type Balance, type FetchFn } from "./balance.ts";
import { windowSnapshot, type LimitWindow } from "./headers.ts";
import { allowed, loadBalanceEstimates, loadBudgets, loadPolicies, policyOf, setBalanceEstimate, setBudget } from "./policy.ts";

export interface Spend {
  currency: "USD";
  spent: number;
  budget?: number;
  period_start: string;
  partial: boolean;
}

export interface ProviderLimits {
  provider_id: string;
  provider_name: string;
  scope: "global" | "personal";
  billing: string;
  balance?: Balance;
  spend?: Spend;
  windows: LimitWindow[];
  windows_updated_at: string;
  windows_source: string;
  /** Rate limits: not for showing (see above). */
  rate_windows: LimitWindow[];
  /** The caller may enter a starting balance. */
  balance_entry: boolean;
}

export interface LimitsQuery {
  provider_ids?: string[];
  model_id?: string;
}

export interface LimitsDeps {
  fetch?: FetchFn;
  now?: Date;
}

export function monthStart(now: Date): Date {
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1));
}

/** This month's cost and unpriced-token presence per provider; personal providers count the user's records only. */
async function monthSpend(user: PublicUser, providers: PublicProvider[], since: Date): Promise<Map<string, { cost: number; partial: boolean }>> {
  const out = new Map<string, { cost: number; partial: boolean }>();
  const globalIds = providers.filter((p) => p.scope === "global").map((p) => p.id);
  const personalIds = providers.filter((p) => p.scope === "personal").map((p) => p.id);
  if (!globalIds.length && !personalIds.length) return out;
  const u = schema.usageRecord;
  const scopes = [];
  if (globalIds.length) scopes.push(inArray(u.providerId, globalIds));
  if (personalIds.length) scopes.push(and(inArray(u.providerId, personalIds), eq(u.userId, user.id)));
  await ensureDb();
  const rows = await getDb()
    .select({
      providerId: u.providerId,
      cost: sql<number>`coalesce(sum(${u.costUsd}), 0)`,
      unpriced: sql<number>`count(*) filter (where ${u.priced} = false)`,
    })
    .from(u)
    .where(and(gte(u.createdAt, since), or(...scopes)))
    .groupBy(u.providerId);
  for (const r of rows) if (r.providerId) out.set(r.providerId, { cost: Number(r.cost), partial: Number(r.unpriced) > 0 });
  return out;
}

/** A metered provider whose service has no balance API takes an entered amount instead. */
function takesEstimate(p: { billing: string; base_url: string }): boolean {
  return p.billing !== "plan" && !balanceAdapterFor(p.base_url);
}

/** What requests on the provider cost since `since`; on a personal provider only its owner's records count. */
async function spendSince(p: PublicProvider, user: PublicUser, since: Date): Promise<number> {
  const u = schema.usageRecord;
  await ensureDb();
  const [r] = await getDb()
    .select({ cost: sql<number>`coalesce(sum(${u.costUsd}), 0)` })
    .from(u)
    .where(and(eq(u.providerId, p.id), gte(u.createdAt, since), p.scope === "personal" ? eq(u.userId, user.id) : undefined));
  return Number(r?.cost ?? 0);
}

async function build(user: PublicUser, providers: PublicProvider[], deps: LimitsDeps): Promise<ProviderLimits[]> {
  const now = deps.now ?? new Date();
  const since = monthStart(now);
  const [policies, budgets, estimates] = await Promise.all([loadPolicies(), loadBudgets(), loadBalanceEstimates()]);
  const visible = providers.map((p) => {
    // Personal providers reach this function only for their owner.
    if (p.scope === "personal") return { p, balance: true, limits: true };
    const pol = policyOf(policies, p.id);
    return { p, balance: allowed(pol.balance, user), limits: allowed(pol.limits, user) };
  });
  const spend = await monthSpend(
    user,
    visible.filter((v) => v.limits).map((v) => v.p),
    since,
  );
  return Promise.all(
    visible.map(async ({ p, balance, limits }): Promise<ProviderLimits> => {
      const out: ProviderLimits = {
        provider_id: p.id,
        provider_name: p.display_name || p.preset_id || p.id,
        scope: p.scope,
        billing: p.billing,
        windows: [],
        windows_updated_at: "",
        windows_source: "",
        rate_windows: [],
        balance_entry: takesEstimate(p) && (p.scope === "personal" || user.role === "admin"),
      };
      if (balance && p.api_key_set && p.enabled) {
        const rt = await loadProviderRuntime(p.id);
        const b = getBalance(rt, deps.fetch, now.getTime());
        if (b) out.balance = await b;
      }
      const entered = estimates[p.id];
      if (balance && entered && takesEstimate(p)) {
        out.balance = estimatedBalance(entered, await spendSince(p, user, new Date(entered.since)), now);
      }
      if (limits) {
        const s = spend.get(p.id);
        out.spend = { currency: "USD", spent: Math.round((s?.cost ?? 0) * 1e6) / 1e6, period_start: since.toISOString(), partial: s?.partial ?? false };
        if (budgets[p.id] !== undefined) out.spend.budget = budgets[p.id];
        const snap = windowSnapshot(p.id);
        if (snap) {
          out.windows = snap.windows.filter((w) => w.plan);
          out.rate_windows = snap.windows.filter((w) => !w.plan);
          if (out.windows.length) {
            out.windows_updated_at = snap.updated_at;
            out.windows_source = "response_headers";
          }
        }
      }
      return out;
    }),
  );
}

export async function getProviderLimits(user: PublicUser, q: LimitsQuery = {}, deps: LimitsDeps = {}): Promise<{ providers: ProviderLimits[]; model?: ProviderLimits }> {
  const all = (await listProvidersForUser(user)).filter((p) => p.group === "llm");
  const wanted = q.provider_ids?.length ? all.filter((p) => q.provider_ids!.includes(p.id)) : all;
  let modelProvider: PublicProvider | undefined;
  if (q.model_id) {
    try {
      const m = await resolveModelForUser(q.model_id, user);
      modelProvider = all.find((p) => p.id === m.providerId);
    } catch {
      // Unknown or hidden model: no `model`.
    }
  }
  const set = modelProvider && !wanted.some((p) => p.id === modelProvider!.id) ? [...wanted, modelProvider] : wanted;
  const limits = await build(user, set, deps);
  return {
    providers: limits.filter((l) => wanted.some((p) => p.id === l.provider_id)),
    model: modelProvider ? limits.find((l) => l.provider_id === modelProvider!.id) : undefined,
  };
}

/** Admins for global providers, the owner for a personal one. */
export async function setProviderBudget(user: PublicUser, providerId: string, monthlyUsd: number | undefined, deps: LimitsDeps = {}): Promise<ProviderLimits> {
  const { row, writable } = await providerForUser(providerId, user);
  if (row.kind !== "llm") throw new ApiError("not_found", `Provider ${providerId} not found`);
  if (!writable) throw new ApiError("permission", "Only admins set the budget of a server provider");
  await setBudget(providerId, monthlyUsd);
  const res = await getProviderLimits(user, { provider_ids: [providerId] }, deps);
  return res.providers[0]!;
}

/**
 * Sets (undefined: clears) the amount a provider's estimated balance starts from, in USD, as of `asOf` or now. Editing
 * it resets the base, as after a top-up. Admins for global providers, the owner for a personal one; a provider that can
 * tell its own balance, or a plan, takes none.
 */
export async function setProviderBalanceEstimate(user: PublicUser, providerId: string, amount: number | undefined, asOf: string | undefined, deps: LimitsDeps = {}): Promise<ProviderLimits> {
  const { row, writable } = await providerForUser(providerId, user);
  if (row.kind !== "llm") throw new ApiError("not_found", `Provider ${providerId} not found`);
  if (!writable) throw new ApiError("permission", "Only admins set the balance of a server provider");
  if (amount === undefined) {
    await setBalanceEstimate(providerId, undefined);
  } else {
    if (!takesEstimate({ billing: billingOf(row), base_url: row.baseUrl })) {
      throw new ApiError("invalid_request", "This provider reports its own balance, or is a subscription plan");
    }
    if (!Number.isFinite(amount) || amount < 0) throw new ApiError("invalid_request", "amount must be a non-negative number");
    const now = deps.now ?? new Date();
    const since = asOf ? new Date(asOf) : now;
    if (Number.isNaN(since.getTime())) throw new ApiError("invalid_request", "as_of must be an RFC 3339 timestamp");
    if (since.getTime() > now.getTime()) throw new ApiError("invalid_request", "as_of must not be in the future");
    await setBalanceEstimate(providerId, { amount, since: since.toISOString() });
  }
  const res = await getProviderLimits(user, { provider_ids: [providerId] }, deps);
  return res.providers[0]!;
}
