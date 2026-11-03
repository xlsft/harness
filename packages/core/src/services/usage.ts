/**
 * Usage records of the LLM gateway (written by services/gateway.ts recordUsage): the admin JSON list and the
 * aggregated reports of harness.llm.v1.UsageService (the settings screens' Usage tab).
 *
 * Cost policy: a record carries a cost only when it was billed at a catalog price, i.e. a priced model on a
 * metered (pay-as-you-go) provider (gateway/cost.ts billedPricing). Records of models without a price and of
 * subscription / coding plan providers (services/presets.ts billing "plan") count tokens only: they add nothing
 * to cost_usd and their tokens are reported as unpriced_tokens, so "$0" and "unknown" stay apart. Records from
 * before `usage_record.priced` existed are taken as priced when they carry a cost and their provider is not a
 * plan (which also drops the cost such a record may have stored).
 *
 * Aggregation runs in SQL (grouped by bucket, model and request class, plus a per-user query for admins);
 * the small grouped result is rolled up into totals here. Buckets start at midnight / the full hour in the
 * requested time zone.
 */
import { and, desc, eq, gte, inArray, lt, sql, type SQL } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { billingOf, findPreset } from "./presets.ts";

/** Recent usage records (admin JSON API). */
export async function listUsage(opts: { userId?: string; limit?: number }) {
  await ensureDb();
  const limit = Math.min(Math.max(1, Number.isFinite(opts.limit) ? opts.limit! : 100), 1000);
  const q = getDb().select().from(schema.usageRecord);
  const rows = await (opts.userId ? q.where(eq(schema.usageRecord.userId, opts.userId)) : q)
    .orderBy(desc(schema.usageRecord.createdAt))
    .limit(limit);
  return {
    usage: rows.map((r) => ({
      id: r.id,
      user_id: r.userId,
      device_id: r.deviceId,
      model: r.modelId,
      provider: r.providerId,
      upstream_model: r.upstreamModel,
      request_class: r.requestClass,
      session_id: r.sessionId,
      input_tokens: r.inputTokens,
      output_tokens: r.outputTokens,
      cache_read_input_tokens: r.cacheReadTokens,
      cache_creation_input_tokens: r.cacheWriteTokens,
      cost_usd: r.costUsd,
      priced: r.priced,
      retries: r.retries,
      status: r.status,
      stop_reason: r.stopReason,
      error_type: r.errorType,
      duration_ms: r.durationMs,
      created_at: r.createdAt.toISOString(),
    })),
  };
}

// --- Reports ----------------------------------------------------------------------------------------

export type UsageBucketSize = "hour" | "day";

export interface UsageRangeInput {
  /** RFC 3339; empty: `to` minus 30 days. */
  from?: string;
  /** RFC 3339; empty: now. */
  to?: string;
  /** Empty: hours for ranges up to 2 days, days otherwise. */
  bucket?: UsageBucketSize;
  /** IANA time zone of the bucket boundaries; empty: UTC. */
  time_zone?: string;
}

export interface ResolvedRange {
  from: Date;
  to: Date;
  bucket: UsageBucketSize;
  time_zone: string;
}

export interface UsageTotals {
  requests: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  total_tokens: number;
  cost_usd: number;
  unpriced_tokens: number;
}

export interface UsageBucketPoint {
  /** RFC 3339, UTC. */
  start: string;
  totals: UsageTotals;
  models: { model_key: string; totals: UsageTotals }[];
}

export interface UsageModel {
  key: string;
  model_id: string;
  display_name: string;
  upstream_model: string;
  provider_id: string;
  provider_name: string;
  priced: boolean;
  totals: UsageTotals;
}

export interface UsageReport {
  from: string;
  to: string;
  bucket: UsageBucketSize;
  time_zone: string;
  totals: UsageTotals;
  buckets: UsageBucketPoint[];
  models: UsageModel[];
  request_classes: { request_class: string; totals: UsageTotals }[];
}

export interface UsageUser {
  user_id: string;
  name: string;
  username: string;
  email: string;
  totals: UsageTotals;
  last_active_at: string;
}

const HOUR_MS = 3_600_000;
const DAY_MS = 24 * HOUR_MS;
const DEFAULT_SPAN_MS = 30 * DAY_MS;
const MAX_SPAN_MS = 400 * DAY_MS;
/** Ranges up to this long get hourly buckets by default. */
const AUTO_HOUR_SPAN_MS = 2 * DAY_MS;
const MAX_HOUR_SPAN_MS = 7 * DAY_MS;
/** request_class of records without one. */
const OTHER_CLASS = "other";

function parseTime(v: string | undefined, field: string): Date | undefined {
  if (!v) return undefined;
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) throw new ApiError("invalid_request", `${field} must be an RFC 3339 timestamp`);
  return d;
}

function validTimeZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

/** Applies the defaults and limits of UsageRange. */
export function resolveRange(input: UsageRangeInput, now = new Date()): ResolvedRange {
  const to = parseTime(input.to, "to") ?? now;
  const from = parseTime(input.from, "from") ?? new Date(to.getTime() - DEFAULT_SPAN_MS);
  const span = to.getTime() - from.getTime();
  if (span <= 0) throw new ApiError("invalid_request", "from must be before to");
  if (span > MAX_SPAN_MS) throw new ApiError("invalid_request", "The range is limited to 400 days");
  const bucket = input.bucket ?? (span <= AUTO_HOUR_SPAN_MS ? "hour" : "day");
  if (bucket === "hour" && span > MAX_HOUR_SPAN_MS) throw new ApiError("invalid_request", "Hourly buckets are limited to ranges of 7 days");
  const time_zone = input.time_zone?.trim() || "UTC";
  if (!validTimeZone(time_zone)) throw new ApiError("invalid_request", `Unknown time zone ${time_zone}`);
  return { from, to, bucket, time_zone };
}

export function emptyTotals(): UsageTotals {
  return { requests: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 0, cost_usd: 0, unpriced_tokens: 0 };
}

function addTotals(into: UsageTotals, t: UsageTotals): void {
  into.requests += t.requests;
  into.input_tokens += t.input_tokens;
  into.output_tokens += t.output_tokens;
  into.cache_read_tokens += t.cache_read_tokens;
  into.cache_write_tokens += t.cache_write_tokens;
  into.total_tokens += t.total_tokens;
  into.cost_usd += t.cost_usd;
  into.unpriced_tokens += t.unpriced_tokens;
}

/** Sums of floats drift (0.1 + 0.2); costs are kept to 1e-8 USD like gateway/cost.ts. */
function roundCost(t: UsageTotals): UsageTotals {
  t.cost_usd = Math.round(t.cost_usd * 1e8) / 1e8;
  return t;
}

const ur = schema.usageRecord;
const tokensSql = sql`(${ur.inputTokens} + ${ur.outputTokens} + ${ur.cacheReadTokens} + ${ur.cacheWriteTokens})`;

/** Whether a record carries a cost (see the file comment for records from before the `priced` column). */
function pricedSql(planProviderIds: string[]): SQL {
  return sql`coalesce(${ur.priced}, ${ur.costUsd} > 0 and not coalesce(${inArray(ur.providerId, planProviderIds)}, false))`;
}

/** Aggregate columns shared by every query (bigint sums come back as strings or bigints, see toTotals). */
function totalsColumns(priced: SQL) {
  return {
    requests: sql<string>`count(*)`,
    input: sql<string>`coalesce(sum(${ur.inputTokens}), 0)`,
    output: sql<string>`coalesce(sum(${ur.outputTokens}), 0)`,
    cacheRead: sql<string>`coalesce(sum(${ur.cacheReadTokens}), 0)`,
    cacheWrite: sql<string>`coalesce(sum(${ur.cacheWriteTokens}), 0)`,
    cost: sql<string>`coalesce(sum(case when ${priced} then ${ur.costUsd} else 0 end), 0)`,
    unpriced: sql<string>`coalesce(sum(case when ${priced} then 0 else ${tokensSql} end), 0)`,
    pricedRequests: sql<string>`count(*) filter (where ${priced})`,
  };
}

type TotalsRow = { requests: unknown; input: unknown; output: unknown; cacheRead: unknown; cacheWrite: unknown; cost: unknown; unpriced: unknown };

function toTotals(r: TotalsRow): UsageTotals {
  const t = {
    requests: Number(r.requests),
    input_tokens: Number(r.input),
    output_tokens: Number(r.output),
    cache_read_tokens: Number(r.cacheRead),
    cache_write_tokens: Number(r.cacheWrite),
    total_tokens: 0,
    cost_usd: Number(r.cost),
    unpriced_tokens: Number(r.unpriced),
  };
  t.total_tokens = t.input_tokens + t.output_tokens + t.cache_read_tokens + t.cache_write_tokens;
  return t;
}

/** Ids of providers billed as a plan, and every provider's name, for the report. */
async function providerInfo() {
  const rows = await getDb()
    .select({ id: schema.provider.id, presetId: schema.provider.presetId, kind: schema.provider.kind, type: schema.provider.type, baseUrl: schema.provider.baseUrl, displayName: schema.provider.displayName })
    .from(schema.provider);
  const plan: string[] = [];
  const names = new Map<string, string>();
  for (const r of rows) {
    if (billingOf(r) === "plan") plan.push(r.id);
    names.set(r.id, r.displayName || (r.presetId ? findPreset(r.presetId)?.display_name : undefined) || r.id);
  }
  return { plan, names };
}

function rangeWhere(range: ResolvedRange, userId: string | undefined): SQL {
  const conds = [gte(ur.createdAt, range.from), lt(ur.createdAt, range.to)];
  if (userId) conds.push(eq(ur.userId, userId));
  return and(...conds)!;
}

/** Bucket start of a record: the local midnight / full hour in the range's time zone, as an instant. */
function bucketSql(range: ResolvedRange): SQL {
  // The unit comes from a closed set; the time zone is validated and bound as a parameter.
  return sql`(date_trunc(${sql.raw(`'${range.bucket}'`)}, ${ur.createdAt} at time zone ${range.time_zone}) at time zone ${range.time_zone})`;
}

/** Every bucket start of the range, in order (empty buckets included). */
async function bucketStarts(range: ResolvedRange): Promise<number[]> {
  const unit = sql.raw(`'${range.bucket}'`);
  const step = sql.raw(`interval '1 ${range.bucket}'`);
  const tz = range.time_zone;
  // Local timestamps step by whole days / hours (so days stay at local midnight across DST changes).
  const res = (await getDb().execute(
    sql`select (g at time zone ${tz}) as start from generate_series(date_trunc(${unit}, ${range.from}::timestamptz at time zone ${tz}), (${range.to}::timestamptz - interval '1 microsecond') at time zone ${tz}, ${step}) as g`,
  )) as unknown as { rows: { start: Date | string }[] };
  return res.rows.map((r) => new Date(r.start).getTime());
}

/**
 * The usage report of one user (`userId`) or everyone (undefined). Callers check access: the caller's own
 * usage for everyone, any user or all of them for admins only.
 */
export async function usageReport(input: UsageRangeInput, userId: string | undefined, now = new Date()): Promise<UsageReport> {
  const range = resolveRange(input, now);
  await ensureDb();
  const db = getDb();
  const { plan, names } = await providerInfo();
  const priced = pricedSql(plan);
  const bucket = bucketSql(range);

  const [rows, starts] = await Promise.all([
    db
      .select({
        bucket: sql<Date | string>`${bucket}`,
        modelId: ur.modelId,
        requestClass: ur.requestClass,
        ...totalsColumns(priced),
        providerId: sql<string | null>`(array_agg(${ur.providerId} order by ${ur.createdAt} desc))[1]`,
        upstreamModel: sql<string | null>`(array_agg(${ur.upstreamModel} order by ${ur.createdAt} desc))[1]`,
        lastAt: sql<Date | string>`max(${ur.createdAt})`,
      })
      .from(ur)
      .where(rangeWhere(range, userId))
      // Positional: the bucket expression binds the time zone, so repeating it would not match the select list.
      .groupBy(sql`1`, sql`2`, sql`3`),
    bucketStarts(range),
  ]);

  const totals = emptyTotals();
  const byBucket = new Map<number, { totals: UsageTotals; models: Map<string, UsageTotals> }>();
  for (const s of starts) byBucket.set(s, { totals: emptyTotals(), models: new Map() });
  const byModel = new Map<string, { totals: UsageTotals; pricedRequests: number; providerId: string; upstreamModel: string; lastAt: number }>();
  const byClass = new Map<string, UsageTotals>();

  for (const r of rows) {
    const t = toTotals(r);
    addTotals(totals, t);

    const at = new Date(r.bucket).getTime();
    let b = byBucket.get(at);
    if (!b) byBucket.set(at, (b = { totals: emptyTotals(), models: new Map() }));
    addTotals(b.totals, t);
    const bm = b.models.get(r.modelId) ?? emptyTotals();
    addTotals(bm, t);
    b.models.set(r.modelId, bm);

    const last = new Date(r.lastAt).getTime();
    let m = byModel.get(r.modelId);
    if (!m) byModel.set(r.modelId, (m = { totals: emptyTotals(), pricedRequests: 0, providerId: "", upstreamModel: "", lastAt: -Infinity }));
    addTotals(m.totals, t);
    m.pricedRequests += Number(r.pricedRequests);
    if (last > m.lastAt) {
      m.lastAt = last;
      m.providerId = r.providerId ?? "";
      m.upstreamModel = r.upstreamModel ?? "";
    }

    const cls = r.requestClass || OTHER_CLASS;
    const c = byClass.get(cls) ?? emptyTotals();
    addTotals(c, t);
    byClass.set(cls, c);
  }

  const modelIds = [...byModel.keys()];
  const catalog = modelIds.length
    ? await db.select({ id: schema.model.id, displayName: schema.model.displayName }).from(schema.model).where(inArray(schema.model.id, modelIds))
    : [];
  const displayNames = new Map(catalog.map((m) => [m.id, m.displayName]));

  const byTokens = <T extends { totals: UsageTotals }>(a: T, b: T) => b.totals.total_tokens - a.totals.total_tokens || b.totals.requests - a.totals.requests;
  const models: UsageModel[] = [...byModel.entries()]
    .map(([id, m]) => ({
      key: id,
      model_id: id,
      display_name: displayNames.get(id) || m.upstreamModel || id,
      upstream_model: m.upstreamModel,
      provider_id: m.providerId,
      provider_name: m.providerId ? (names.get(m.providerId) ?? m.providerId) : "",
      priced: m.pricedRequests > 0,
      totals: roundCost(m.totals),
    }))
    .sort(byTokens);
  const order = new Map(models.map((m, i) => [m.key, i]));

  return {
    from: range.from.toISOString(),
    to: range.to.toISOString(),
    bucket: range.bucket,
    time_zone: range.time_zone,
    totals: roundCost(totals),
    buckets: [...byBucket.entries()]
      .sort(([a], [b]) => a - b)
      .map(([at, b]) => ({
        start: new Date(at).toISOString(),
        totals: roundCost(b.totals),
        models: [...b.models.entries()]
          .map(([key, t]) => ({ model_key: key, totals: roundCost(t) }))
          .sort((a, b) => order.get(a.model_key)! - order.get(b.model_key)!),
      })),
    models,
    request_classes: [...byClass.entries()].map(([request_class, t]) => ({ request_class, totals: roundCost(t) })).sort(byTokens),
  };
}

/** Per-user totals over the range (admins), by total tokens, largest first. */
export async function usageByUser(input: UsageRangeInput, userId: string | undefined, now = new Date()): Promise<UsageUser[]> {
  const range = resolveRange(input, now);
  await ensureDb();
  const { plan } = await providerInfo();
  const u = schema.user;
  const rows = await getDb()
    .select({
      userId: ur.userId,
      name: u.name,
      username: u.username,
      email: u.email,
      ...totalsColumns(pricedSql(plan)),
      lastAt: sql<Date | string>`max(${ur.createdAt})`,
    })
    .from(ur)
    .innerJoin(u, eq(u.id, ur.userId))
    .where(rangeWhere(range, userId))
    .groupBy(ur.userId, u.name, u.username, u.email);
  return rows
    .map((r) => ({
      user_id: r.userId,
      name: r.name,
      username: r.username ?? "",
      email: r.email,
      totals: roundCost(toTotals(r)),
      last_active_at: new Date(r.lastAt).toISOString(),
    }))
    .sort((a, b) => b.totals.total_tokens - a.totals.total_tokens || b.totals.requests - a.totals.requests);
}
