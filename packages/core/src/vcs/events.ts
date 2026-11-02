/**
 * VcsEvent storage and delivery, PR state, and session linking.
 *
 * - Events (webhooks, device snapshots) are rows of vcs_event with a bigserial seq; the public id is
 *   "vev_<seq>". They are kept 7 days (purged hourly) and published to an in-process bus after insert.
 * - Sessions are linked to an event by repository_key plus head branch: explicit links (LinkSession,
 *   vcs_session_link, also by PR number) and synced session metadata (sync_session.repo + info.gitBranch).
 * - vcs_pull_request keeps the last known state of every PR/MR (and the checks of its head commit), the
 *   baseline for diffs (device snapshots, conflict transitions) and for events without a full PR payload.
 */
import { and, asc, eq, gt, inArray, lt, max, or, sql } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { PullRequest_State } from "../gen/harness/agent/v1/agent_pb.ts";
import { Mutex } from "../lib/async.ts";
import type { CheckData, PrData } from "./types.ts";

export type EventRow = typeof schema.vcsEvent.$inferSelect;
export type StoredPrRow = typeof schema.vcsPullRequest.$inferSelect;

export interface SessionRef {
  sessionId: string;
  userId: string;
}

export interface EventInput {
  kind: number;
  source: number;
  connectionId?: string;
  deliveryId?: string;
  repositoryKey: string;
  pr?: PrData;
  prNumber?: number;
  headBranch?: string;
  detail?: Record<string, unknown>;
  occurredAt?: string;
  actorUserId?: string;
  /** Restrict to these sessions instead of linking by repo/branch (auto-fix follow-ups). */
  sessions?: SessionRef[];
}

export const EVENT_RETENTION_MS = 7 * 86_400_000;
const PR_RETENTION_MS = 30 * 86_400_000;

export const eventId = (seq: number) => `vev_${seq}`;

export function parseEventId(id: string): number {
  const m = /^vev_(\d+)$/.exec(id);
  return m ? Number(m[1]) : NaN;
}

// --- background work (auto-fix, conflict re-checks) ------------------------------------------------

const pending = new Set<Promise<unknown>>();

/** Runs work after the response (webhooks must answer quickly); errors are logged. */
export function background(work: () => Promise<unknown>): void {
  const p = work()
    .catch((err) => console.error("[harness] vcs background task failed", err))
    .finally(() => pending.delete(p));
  pending.add(p);
}

/** Resolves when no background work is pending (tests). */
export async function vcsIdle(): Promise<void> {
  while (pending.size) await Promise.allSettled([...pending]);
}

// --- bus -----------------------------------------------------------------------------------------

type Listener = (row: EventRow) => void;
const listeners = new Set<Listener>();

export function subscribeEvents(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function publish(row: EventRow): void {
  for (const l of listeners) {
    try {
      l(row);
    } catch (err) {
      console.error("[harness] vcs listener failed", err);
    }
  }
}

// --- linking -------------------------------------------------------------------------------------

/** Sessions linked to a repo + head branch (or PR number): explicit links and synced session metadata. */
export async function linkedSessions(repositoryKey: string, headBranch: string, prNumber: number): Promise<SessionRef[]> {
  await ensureDb();
  const db = getDb();
  const l = schema.vcsSessionLink;
  const conds = [];
  if (headBranch) conds.push(eq(l.branch, headBranch));
  if (prNumber > 0) conds.push(eq(l.prNumber, prNumber));
  const out = new Map<string, SessionRef>();
  if (conds.length) {
    const rows = await db
      .select({ sessionId: l.sessionId, userId: l.userId })
      .from(l)
      .where(and(eq(l.repositoryKey, repositoryKey), or(...conds)));
    for (const r of rows) out.set(r.sessionId, r);
  }
  if (headBranch) {
    const s = schema.syncSession;
    const rows = await db
      .select({ sessionId: s.id, userId: s.userId })
      .from(s)
      .where(and(eq(s.repo, repositoryKey), sql`${s.info}->>'gitBranch' = ${headBranch}`));
    // An explicit link to another repo/branch overrides the metadata link.
    const explicit = rows.length
      ? await db.select().from(l).where(inArray(l.sessionId, rows.map((r) => r.sessionId)))
      : [];
    for (const r of rows) {
      const e = explicit.find((x) => x.sessionId === r.sessionId);
      if (e?.repositoryKey && (e.repositoryKey !== repositoryKey || (e.branch && e.branch !== headBranch))) continue;
      out.set(r.sessionId, r);
    }
  }
  return [...out.values()].sort((a, b) => (a.sessionId < b.sessionId ? -1 : 1));
}

// --- PR state ------------------------------------------------------------------------------------

export async function getStoredPr(repositoryKey: string, number: number): Promise<StoredPrRow | undefined> {
  await ensureDb();
  return getDb().query.vcsPullRequest.findFirst({
    where: and(eq(schema.vcsPullRequest.repositoryKey, repositoryKey), eq(schema.vcsPullRequest.number, number)),
  });
}

export async function storedPrs(repositoryKey: string, f: { headBranch?: string; headSha?: string; baseBranch?: string }): Promise<StoredPrRow[]> {
  await ensureDb();
  const p = schema.vcsPullRequest;
  const rows = await getDb()
    .select()
    .from(p)
    .where(
      and(
        eq(p.repositoryKey, repositoryKey),
        f.headBranch ? eq(p.headBranch, f.headBranch) : undefined,
        f.headSha ? eq(p.headSha, f.headSha) : undefined,
        f.baseBranch ? eq(p.baseBranch, f.baseBranch) : undefined,
      ),
    )
    .orderBy(asc(p.number));
  return rows;
}

export function prOf(row: StoredPrRow): PrData {
  return row.data as PrData;
}

export function checksOf(row: StoredPrRow | undefined): CheckData[] {
  return Array.isArray(row?.checks) ? (row!.checks as CheckData[]) : [];
}

/** Upserts the PR state; returns the previous row. */
export async function savePr(pr: PrData, o: { source: number; connectionId?: string; checks?: CheckData[]; reportedBy?: string }): Promise<StoredPrRow | undefined> {
  const prev = await getStoredPr(pr.repositoryKey, pr.number);
  const values = {
    connectionId: o.connectionId ?? prev?.connectionId ?? "",
    source: o.source,
    headBranch: pr.headBranch,
    headSha: pr.headSha ?? prev?.headSha ?? "",
    baseBranch: pr.baseBranch,
    data: pr,
    checks: o.checks ?? checksOf(prev),
    reportedBy: o.reportedBy ?? "",
    updatedAt: new Date(),
  };
  await getDb()
    .insert(schema.vcsPullRequest)
    .values({ repositoryKey: pr.repositoryKey, number: pr.number, ...values })
    .onConflictDoUpdate({ target: [schema.vcsPullRequest.repositoryKey, schema.vcsPullRequest.number], set: values });
  return prev;
}

/** The stored PR merged with a newer partial view (webhook payloads lack review state and checks). */
export function mergePr(prev: PrData | undefined, next: PrData): PrData {
  if (!prev) return next;
  return {
    ...prev,
    ...next,
    reviewState: next.reviewState === "none" && prev.reviewState !== "none" ? prev.reviewState : next.reviewState,
    checks: next.checks ?? prev.checks,
    author: next.author || prev.author,
    reviewers: next.reviewers ?? prev.reviewers,
    nodeId: next.nodeId || prev.nodeId,
  };
}

// --- events --------------------------------------------------------------------------------------

/** Serializes insert + publish, so events are committed and published in seq order (WatchEvents cursors). */
const writeMutex = new Mutex();

/** Stores events (linking sessions) and publishes them. Returns the stored rows in order. */
export async function storeEvents(inputs: EventInput[]): Promise<EventRow[]> {
  if (!inputs.length) return [];
  await ensureDb();
  maybePurge();
  return writeMutex.run(() => insertEvents(inputs));
}

async function insertEvents(inputs: EventInput[]): Promise<EventRow[]> {
  const rows: EventRow[] = [];
  for (const e of inputs) {
    const headBranch = e.headBranch ?? e.pr?.headBranch ?? "";
    const prNumber = e.prNumber ?? e.pr?.number ?? 0;
    const sessions = e.sessions ?? (await linkedSessions(e.repositoryKey, headBranch, prNumber));
    const now = new Date();
    const occurred = e.occurredAt ? new Date(e.occurredAt) : now;
    const [row] = await getDb()
      .insert(schema.vcsEvent)
      .values({
        kind: e.kind,
        source: e.source,
        connectionId: e.connectionId ?? "",
        deliveryId: e.deliveryId ?? "",
        repositoryKey: e.repositoryKey,
        prNumber,
        headBranch,
        pullRequest: e.pr ?? null,
        sessions,
        actorUserId: e.actorUserId ?? "",
        detail: e.detail ?? {},
        occurredAt: Number.isFinite(occurred.getTime()) ? occurred : now,
        createdAt: now,
      })
      .returning();
    rows.push(row!);
  }
  for (const r of rows) publish(r);
  return rows;
}

/** Highest stored seq (0 when none); read under the write lock so nothing is in flight below it. */
export async function headSeq(): Promise<number> {
  await ensureDb();
  return writeMutex.run(async () => {
    const [r] = await getDb().select({ v: max(schema.vcsEvent.seq) }).from(schema.vcsEvent);
    return r?.v ?? 0;
  });
}

export async function eventsAfter(seq: number, limit: number): Promise<EventRow[]> {
  await ensureDb();
  return getDb().select().from(schema.vcsEvent).where(gt(schema.vcsEvent.seq, seq)).orderBy(asc(schema.vcsEvent.seq)).limit(limit);
}

export function sessionsOf(row: EventRow): SessionRef[] {
  return Array.isArray(row.sessions) ? (row.sessions as SessionRef[]) : [];
}

/** Records a webhook delivery; false when it was already processed (idempotency). */
export async function claimDelivery(connectionId: string, deliveryId: string): Promise<boolean> {
  await ensureDb();
  const r = await getDb()
    .insert(schema.vcsDelivery)
    .values({ key: `${connectionId}:${deliveryId}`, createdAt: new Date() })
    .onConflictDoNothing()
    .returning({ key: schema.vcsDelivery.key });
  return r.length > 0;
}

export async function releaseDelivery(connectionId: string, deliveryId: string): Promise<void> {
  await getDb().delete(schema.vcsDelivery).where(eq(schema.vcsDelivery.key, `${connectionId}:${deliveryId}`));
}

// --- retention -----------------------------------------------------------------------------------

export async function purgeVcs(now = Date.now()): Promise<{ events: number; deliveries: number; prs: number }> {
  await ensureDb();
  const db = getDb();
  const cutoff = new Date(now - EVENT_RETENTION_MS);
  const events = await db.delete(schema.vcsEvent).where(lt(schema.vcsEvent.createdAt, cutoff)).returning({ seq: schema.vcsEvent.seq });
  const deliveries = await db.delete(schema.vcsDelivery).where(lt(schema.vcsDelivery.createdAt, cutoff)).returning({ k: schema.vcsDelivery.key });
  const p = schema.vcsPullRequest;
  const prs = await db
    .delete(p)
    .where(and(lt(p.updatedAt, new Date(now - PR_RETENTION_MS)), sql`(${p.data}->>'state')::int in (${PullRequest_State.MERGED}, ${PullRequest_State.CLOSED})`))
    .returning({ n: p.number });
  return { events: events.length, deliveries: deliveries.length, prs: prs.length };
}

let lastPurge = 0;
function maybePurge(): void {
  const now = Date.now();
  if (now - lastPurge < 3_600_000) return;
  lastPurge = now;
  purgeVcs(now).catch((err) => console.error("[harness] vcs purge failed", err));
}
