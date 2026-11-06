/**
 * Spend limits (USD), checked by the gateway before calling upstream:
 *   - per user, per group and per role: daily and monthly (calendar day / month in UTC);
 *   - per session: total spend of one RequestMetadata.session_id.
 *
 * Config lives in the `setting` table under "quotas" and is edited through BillingService (Settings, Users) and the
 * admin JSON API (`/api/admin/quotas`). Effective limit of a user for each period: their own value if set, otherwise the
 * strictest value of their groups, otherwise the value for their role. The session budget is `session_usd` from the
 * config, falling back to HARNESS_SESSION_BUDGET_USD. A limit of 0 blocks all spending; unset = unlimited.
 *
 * Spend is the sum of recorded usage (src/services/gateway.ts recordUsage) on the organization's providers. A personal
 * provider is the user's own money and their own business: it counts towards no quota, and admins do not see it here. A
 * request that starts under the limit may overshoot it; the next one is rejected.
 */
import { and, eq, gte, sql, type SQL } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import type { PublicUser } from "./devices.ts";
import { groupsOfUser } from "./groups.ts";

export interface SpendLimits {
  daily_usd?: number;
  monthly_usd?: number;
  /** Object store quota in bytes (src/services/objects.ts), not a spend limit, but set the same way. */
  objects_bytes?: number;
}

export interface QuotaConfig {
  users: Record<string, SpendLimits>;
  roles: Record<string, SpendLimits>;
  /** Absent while no group has limits. */
  groups?: Record<string, SpendLimits>;
  session_usd?: number;
}

const KEY = "quotas";

function emptyConfig(): QuotaConfig {
  return { users: {}, roles: {} };
}

function validLimit(v: unknown, field: string): number | undefined {
  if (v === undefined || v === null) return undefined;
  if (typeof v !== "number" || !Number.isFinite(v) || v < 0) {
    throw new ApiError("invalid_request", `${field} must be a non-negative number or null`);
  }
  return v;
}

function validLimits(v: unknown, field: string): SpendLimits | undefined {
  if (v === undefined || v === null) return undefined;
  if (typeof v !== "object" || Array.isArray(v)) throw new ApiError("invalid_request", `${field} must be an object`);
  const o = v as Record<string, unknown>;
  for (const k of Object.keys(o)) {
    if (k !== "daily_usd" && k !== "monthly_usd" && k !== "objects_bytes") throw new ApiError("invalid_request", `Unknown field ${field}.${k}`);
  }
  const out: SpendLimits = {};
  const d = validLimit(o.daily_usd, `${field}.daily_usd`);
  const m = validLimit(o.monthly_usd, `${field}.monthly_usd`);
  if (d !== undefined) out.daily_usd = d;
  if (m !== undefined) out.monthly_usd = m;
  const b = o.objects_bytes === undefined || o.objects_bytes === null || (typeof o.objects_bytes === "number" && o.objects_bytes < 0 && Number.isFinite(o.objects_bytes)) ? (o.objects_bytes as number | undefined | null) ?? undefined : validLimit(o.objects_bytes, `${field}.objects_bytes`);
  if (b !== undefined) out.objects_bytes = b;
  return Object.keys(out).length ? out : undefined;
}

function validMap(v: unknown, field: string): Record<string, SpendLimits> {
  if (v === undefined || v === null) return {};
  if (typeof v !== "object" || Array.isArray(v)) throw new ApiError("invalid_request", `${field} must be an object`);
  const out: Record<string, SpendLimits> = {};
  for (const [k, lim] of Object.entries(v)) {
    const l = validLimits(lim, `${field}.${k}`);
    if (l) out[k] = l;
  }
  return out;
}

export async function getQuotaConfig(): Promise<QuotaConfig> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, KEY) });
  return { ...emptyConfig(), ...((r?.value as QuotaConfig | undefined) ?? {}) };
}

/** Replaces the whole config (PUT semantics). */
export async function setQuotaConfig(input: unknown): Promise<QuotaConfig> {
  if (!input || typeof input !== "object" || Array.isArray(input)) throw new ApiError("invalid_request", "Body must be an object");
  const o = input as Record<string, unknown>;
  for (const k of Object.keys(o)) {
    if (!["users", "roles", "groups", "session_usd"].includes(k)) throw new ApiError("invalid_request", `Unknown field ${k}`);
  }
  const cfg: QuotaConfig = { users: validMap(o.users, "users"), roles: validMap(o.roles, "roles") };
  const groups = validMap(o.groups, "groups");
  if (Object.keys(groups).length) cfg.groups = groups;
  const s = validLimit(o.session_usd, "session_usd");
  if (s !== undefined) cfg.session_usd = s;
  await ensureDb();
  const now = new Date();
  await getDb()
    .insert(schema.setting)
    .values({ key: KEY, value: cfg, updatedAt: now })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value: cfg, updatedAt: now } });
  return cfg;
}

/** Updates one user's or one role's limits (null removes them). */
export async function patchQuotaConfig(input: unknown): Promise<QuotaConfig> {
  if (!input || typeof input !== "object" || Array.isArray(input)) throw new ApiError("invalid_request", "Body must be an object");
  const o = input as Record<string, unknown>;
  const cur = await getQuotaConfig();
  const next: QuotaConfig = { users: { ...cur.users }, roles: { ...cur.roles }, groups: { ...cur.groups } };
  if (cur.session_usd !== undefined) next.session_usd = cur.session_usd;
  for (const k of Object.keys(o)) {
    if (!["users", "roles", "groups", "session_usd"].includes(k)) throw new ApiError("invalid_request", `Unknown field ${k}`);
  }
  for (const group of ["users", "roles", "groups"] as const) {
    const v = o[group];
    if (v === undefined) continue;
    if (!v || typeof v !== "object" || Array.isArray(v)) throw new ApiError("invalid_request", `${group} must be an object`);
    for (const [id, lim] of Object.entries(v)) {
      const l = validLimits(lim, `${group}.${id}`);
      const into = (next[group] ??= {});
      if (l) into[id] = l;
      else delete into[id];
    }
  }
  if ("session_usd" in o) {
    const s = validLimit(o.session_usd, "session_usd");
    if (s === undefined) delete next.session_usd;
    else next.session_usd = s;
  }
  return setQuotaConfig(next);
}

/** The strictest of the groups' values for one field. */
function strictest(cfg: QuotaConfig, groupIds: string[], field: "daily_usd" | "monthly_usd"): { value: number; groupId: string } | undefined {
  let best: { value: number; groupId: string } | undefined;
  for (const id of groupIds) {
    const v = cfg.groups?.[id]?.[field];
    if (v !== undefined && (!best || v < best.value)) best = { value: v, groupId: id };
  }
  return best;
}

export function effectiveLimits(cfg: QuotaConfig, user: Pick<PublicUser, "id" | "role">, groupIds: string[] = []): SpendLimits {
  const u = cfg.users[user.id] ?? {};
  const r = cfg.roles[user.role] ?? {};
  const out: SpendLimits = {};
  const d = u.daily_usd ?? strictest(cfg, groupIds, "daily_usd")?.value ?? r.daily_usd;
  const m = u.monthly_usd ?? strictest(cfg, groupIds, "monthly_usd")?.value ?? r.monthly_usd;
  if (d !== undefined) out.daily_usd = d;
  if (m !== undefined) out.monthly_usd = m;
  return out;
}

/** Which level gives a user's limit: "user", "group:<name>", "role" or "". */
export function limitSource(cfg: QuotaConfig, user: Pick<PublicUser, "id" | "role">, groups: { id: string; name: string }[], field: "daily_usd" | "monthly_usd"): string {
  if (cfg.users[user.id]?.[field] !== undefined) return "user";
  const g = strictest(cfg, groups.map((x) => x.id), field);
  if (g) return `group:${groups.find((x) => x.id === g.groupId)?.name ?? g.groupId}`;
  return cfg.roles[user.role]?.[field] !== undefined ? "role" : "";
}

/** Group ids of a user, only when any group has limits at all (the gateway asks for every request). */
async function quotaGroupIds(cfg: QuotaConfig, userId: string): Promise<string[]> {
  if (!Object.keys(cfg.groups ?? {}).length) return [];
  return (await groupsOfUser(userId)).map((g) => g.id);
}

export function sessionBudget(cfg: QuotaConfig): number | undefined {
  if (cfg.session_usd !== undefined) return cfg.session_usd;
  const env = process.env.HARNESS_SESSION_BUDGET_USD;
  if (env === undefined || env === "") return undefined;
  const n = Number(env);
  return Number.isFinite(n) && n >= 0 ? n : undefined;
}

export function periodStarts(now = new Date()): { day: Date; month: Date } {
  return {
    day: new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())),
    month: new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1)),
  };
}

async function sumCost(...conds: SQL[]): Promise<number> {
  // sum(double precision) is double precision; Number() also covers drivers that return it as a string.
  const notPersonal = sql`not exists (select 1 from ${schema.provider} where ${schema.provider.id} = ${schema.usageRecord.providerId} and ${schema.provider.ownerUserId} is not null)`;
  const [r] = await getDb()
    .select({ total: sql<number>`coalesce(sum(${schema.usageRecord.costUsd}), 0)` })
    .from(schema.usageRecord)
    .where(and(...conds, notPersonal));
  return Number(r?.total ?? 0);
}

export interface SpendStatus {
  daily_usd: number;
  monthly_usd: number;
  limits: SpendLimits;
}

export async function spendStatus(user: PublicUser, now = new Date()): Promise<SpendStatus> {
  await ensureDb();
  const { day, month } = periodStarts(now);
  const [daily, monthly] = await Promise.all([
    sumCost(eq(schema.usageRecord.userId, user.id), gte(schema.usageRecord.createdAt, day)),
    sumCost(eq(schema.usageRecord.userId, user.id), gte(schema.usageRecord.createdAt, month)),
  ]);
  const cfg = await getQuotaConfig();
  return { daily_usd: daily, monthly_usd: monthly, limits: effectiveLimits(cfg, user, await quotaGroupIds(cfg, user.id)) };
}

const fmt = (n: number) => `$${n.toFixed(2)}`;

/** Throws quota_exceeded (RESOURCE_EXHAUSTED, do not retry) when a limit is already used up. */
export async function enforceQuotas(user: PublicUser, sessionId: string | undefined): Promise<void> {
  const cfg = await getQuotaConfig();
  const limits = effectiveLimits(cfg, user, await quotaGroupIds(cfg, user.id));
  const budget = sessionId ? sessionBudget(cfg) : undefined;
  if (limits.daily_usd === undefined && limits.monthly_usd === undefined && budget === undefined) return;

  const { day, month } = periodStarts();
  if (limits.daily_usd !== undefined) {
    const spent = await sumCost(eq(schema.usageRecord.userId, user.id), gte(schema.usageRecord.createdAt, day));
    if (spent >= limits.daily_usd) {
      throw new ApiError("quota_exceeded", `Daily spend limit reached (${fmt(spent)} of ${fmt(limits.daily_usd)}); it resets at 00:00 UTC`);
    }
  }
  if (limits.monthly_usd !== undefined) {
    const spent = await sumCost(eq(schema.usageRecord.userId, user.id), gte(schema.usageRecord.createdAt, month));
    if (spent >= limits.monthly_usd) {
      throw new ApiError("quota_exceeded", `Monthly spend limit reached (${fmt(spent)} of ${fmt(limits.monthly_usd)})`);
    }
  }
  if (budget !== undefined && sessionId) {
    const spent = await sumCost(eq(schema.usageRecord.userId, user.id), eq(schema.usageRecord.sessionId, sessionId));
    if (spent >= budget) {
      throw new ApiError("quota_exceeded", `Session budget reached (${fmt(spent)} of ${fmt(budget)}); start a new session to continue`);
    }
  }
}

/** The user's object store quota in bytes: their own value, else their role's, else `fallback` (negative: unlimited). */
export async function objectQuotaFor(user: Pick<PublicUser, "id" | "role">, fallback: number): Promise<number> {
  const cfg = await getQuotaConfig();
  const own = cfg.users[user.id]?.objects_bytes;
  if (own !== undefined) return own;
  const inGroups = (await quotaGroupIds(cfg, user.id)).map((id) => cfg.groups?.[id]?.objects_bytes).filter((v): v is number => v !== undefined);
  // Strictest, with a negative value meaning unlimited.
  if (inGroups.length) return inGroups.reduce((a, b) => (a < 0 ? b : b < 0 ? a : Math.min(a, b)));
  return cfg.roles[user.role]?.objects_bytes ?? fallback;
}
