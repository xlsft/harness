/**
 * Who sees a global provider's limits: two independent policies per provider (the balance, and the limits:
 * spend and rate-limit windows), each "everyone" (the default), "selected" users or "admins" only. Admins
 * always see both. Personal providers have no policy at all: only their owner sees their limits, and the admin
 * calls here refuse them as not found.
 *
 * Stored in the `setting` table under "provider_limits_policy"; monthly budgets under "provider_budgets", the starting
 * amounts of estimated balances (limits/service.ts) under "provider_balance_estimates", which the balance policy covers.
 */
import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import type { PublicUser } from "../services/devices.ts";
import { getProvider, listProviders } from "../services/providers.ts";

export type Audience = "everyone" | "selected" | "admins";

export interface Visibility {
  audience: Audience;
  user_ids: string[];
}

export interface ProviderPolicy {
  provider_id: string;
  balance: Visibility;
  limits: Visibility;
}

const POLICY_KEY = "provider_limits_policy";
const BUDGET_KEY = "provider_budgets";
const ESTIMATE_KEY = "provider_balance_estimates";
const MAX_SELECTED = 1000;

type StoredPolicies = Record<string, { balance?: Visibility; limits?: Visibility }>;

const EVERYONE: Visibility = { audience: "everyone", user_ids: [] };

async function readSetting<T>(key: string, fallback: T): Promise<T> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, key) });
  return (r?.value as T | undefined) ?? fallback;
}

async function writeSetting(key: string, value: unknown): Promise<void> {
  await ensureDb();
  const now = new Date();
  await getDb()
    .insert(schema.setting)
    .values({ key, value, updatedAt: now })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value, updatedAt: now } });
}

export async function loadPolicies(): Promise<StoredPolicies> {
  return readSetting<StoredPolicies>(POLICY_KEY, {});
}

export function policyOf(stored: StoredPolicies, providerId: string): ProviderPolicy {
  const p = stored[providerId];
  return { provider_id: providerId, balance: p?.balance ?? EVERYONE, limits: p?.limits ?? EVERYONE };
}

/** Whether `user` passes one visibility policy of a global provider. */
export function allowed(v: Visibility, user: PublicUser): boolean {
  if (user.role === "admin") return true;
  if (v.audience === "admins") return false;
  if (v.audience === "selected") return v.user_ids.includes(user.id);
  return true;
}

function validVisibility(v: Partial<Visibility> | undefined): Visibility {
  if (!v) return EVERYONE;
  const audience = v.audience ?? "everyone";
  if (audience !== "everyone" && audience !== "selected" && audience !== "admins") throw new ApiError("invalid_request", "Unknown audience");
  const ids = audience === "selected" ? [...new Set((v.user_ids ?? []).filter((id) => typeof id === "string" && id))] : [];
  if (ids.length > MAX_SELECTED) throw new ApiError("invalid_request", `At most ${MAX_SELECTED} selected users`);
  return { audience, user_ids: ids };
}

/** Every global LLM provider's policy (admin API). */
export async function listPolicies(): Promise<ProviderPolicy[]> {
  const stored = await loadPolicies();
  return (await listProviders()).filter((p) => p.group === "llm").map((p) => policyOf(stored, p.id));
}

/** Replaces a global LLM provider's policy (admin API). A personal provider is not found. */
export async function setPolicy(input: { provider_id: string; balance?: Partial<Visibility>; limits?: Partial<Visibility> }): Promise<ProviderPolicy> {
  const p = await getProvider(input.provider_id, null);
  if (p.group !== "llm") throw new ApiError("not_found", `Provider ${input.provider_id} not found`);
  const next = { balance: validVisibility(input.balance), limits: validVisibility(input.limits) };
  const stored = await loadPolicies();
  if (next.balance.audience === "everyone" && next.limits.audience === "everyone") delete stored[p.id];
  else stored[p.id] = next;
  await writeSetting(POLICY_KEY, stored);
  return { provider_id: p.id, ...next };
}

export async function loadBudgets(): Promise<Record<string, number>> {
  return readSetting<Record<string, number>>(BUDGET_KEY, {});
}

export async function setBudget(providerId: string, monthlyUsd: number | undefined): Promise<void> {
  if (monthlyUsd !== undefined && (!Number.isFinite(monthlyUsd) || monthlyUsd < 0)) {
    throw new ApiError("invalid_request", "monthly_usd must be a non-negative number");
  }
  const budgets = await loadBudgets();
  if (monthlyUsd === undefined) delete budgets[providerId];
  else budgets[providerId] = monthlyUsd;
  await writeSetting(BUDGET_KEY, budgets);
}

/** What someone entered as a provider's balance: `amount` USD left at `since` (RFC 3339, UTC). */
export interface BalanceEstimate {
  amount: number;
  since: string;
}

export async function loadBalanceEstimates(): Promise<Record<string, BalanceEstimate>> {
  return readSetting<Record<string, BalanceEstimate>>(ESTIMATE_KEY, {});
}

/** Replaces a provider's starting amount, so spending counts from `since` on; undefined clears it. */
export async function setBalanceEstimate(providerId: string, estimate: BalanceEstimate | undefined): Promise<void> {
  const estimates = await loadBalanceEstimates();
  if (estimate === undefined && !(providerId in estimates)) return;
  if (estimate === undefined) delete estimates[providerId];
  else estimates[providerId] = estimate;
  await writeSetting(ESTIMATE_KEY, estimates);
}
