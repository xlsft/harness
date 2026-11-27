/**
 * The inbox on the server (docs/design/coordinator.md): one relay_inbox row per relay session, folded from the events
 * its host publishes (inbox-fold.ts), listed by group with paging, watched live per user, read on one device and read on
 * all. Every query is scoped to the caller: no admin path reads these rows.
 */
import { create, fromJson, toJson, type JsonValue, type MessageInitShape } from "@bufbuild/protobuf";
import { and, asc, desc, eq, gt, inArray, isNull, or, sql, type SQL } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import type { SessionEvent, SessionInfo } from "../gen/harness/agent/v1/agent_pb.ts";
import { SessionEventSchema, SessionStatus } from "../gen/harness/agent/v1/agent_pb.ts";
import {
  InboxCountSchema,
  InboxGroup,
  InboxItemSchema,
  type InboxCount,
  type InboxItem,
  type ListInboxRequest,
  type ListInboxResponseSchema,
  type WatchInboxResponseSchema,
} from "../gen/harness/agent/v1/inbox_pb.ts";
import { AsyncQueue } from "../lib/async.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal } from "../services/devices.ts";
import { relayBus } from "./bus.ts";
import { foldEvent, groupOf, type FoldScratch } from "./inbox-fold.ts";

type Row = typeof schema.relayInbox.$inferSelect;
type Init<T extends Parameters<typeof create>[0]> = MessageInitShape<T>;

/** A closed session stays listed this long after it last needed attention, while unread. */
export const CLOSED_KEEP_MS = 14 * 24 * 3600 * 1000;
/** Project runs are listed this long after they last changed. */
const RUN_KEEP_MS = 30 * 24 * 3600 * 1000;
/** Events older than this are taken as replayed history (see foldInbox). */
const REPLAY_AGE_MS = 10 * 60 * 1000;
const DEFAULT_PAGE = 50;
const MAX_PAGE = 200;
/** A watcher this many batches behind is told to list again. */
const MAX_BACKLOG = 500;
/** Missed heartbeats after which a host without a stream is offline (as in service.ts). */
const MISSED_HEARTBEATS = 3;

const GROUPS = [InboxGroup.NEEDS_INPUT, InboxGroup.FAILED, InboxGroup.FINISHED, InboxGroup.RUNNING];

function itemOf(row: Row): InboxItem {
  return fromJson(InboxItemSchema, row.state as JsonValue, { ignoreUnknownFields: true });
}

function isUnread(row: Pick<Row, "attentionAt" | "readAt">): boolean {
  return !!row.attentionAt && (!row.readAt || row.attentionAt.getTime() > row.readAt.getTime());
}

export interface InboxUpdate {
  /** The session's info (RegisterSession) or the latest SessionUpdated. */
  info?: SessionInfo;
  /** Events in seq order. */
  events?: SessionEvent[];
  /** false: the session was unregistered; true: it was registered again. */
  active?: boolean;
  /** The status a heartbeat reported. */
  status?: SessionStatus;
}

/**
 * Folds an update of one relay session into its row. Called with the session's relay lock held (service.ts), so the
 * updates of one session never interleave.
 */
export async function foldInbox(userId: string, sessionId: string, hostId: string, u: InboxUpdate): Promise<void> {
  const db = getDb();
  const [row] = await db.select().from(schema.relayInbox).where(eq(schema.relayInbox.sessionId, sessionId));
  if (row && row.userId !== userId) return;
  const item = row ? itemOf(row) : create(InboxItemSchema, { sessionId, open: true });
  const scratch: FoldScratch = { ...((row?.scratch as FoldScratch | undefined) ?? {}) };
  const before = row ? JSON.stringify(row.state) : "";
  let attention = row?.attentionAt ?? null;
  const prevAttention = item.attentionAt;

  if (u.active === true) item.open = true;
  if (u.info) {
    foldEvent(item, scratch, create(SessionEventSchema, { event: { case: "sessionUpdated", value: u.info } }));
  }
  for (const ev of u.events ?? []) foldEvent(item, scratch, ev);
  if (u.status !== undefined && u.status !== SessionStatus.UNSPECIFIED) item.working = u.status === SessionStatus.WORKING;
  if (u.active === false) {
    foldEvent(item, scratch, create(SessionEventSchema, { event: { case: "sessionEnded", value: { reason: "unregistered" } } }));
  }
  if (item.attentionAt && item.attentionAt !== prevAttention) {
    // Server time for what happens now, as read markers are server time and clocks of machines drift. An event much
    // older than that is history a resumed session replays: it keeps its own time, so it doesn't read as new.
    const t = new Date(item.attentionAt).getTime();
    attention = Number.isFinite(t) && Date.now() - t > REPLAY_AGE_MS ? new Date(t) : new Date();
  }
  item.group = groupOf(item, scratch);
  // Per-read fields are computed at list time.
  item.unread = false;
  item.deviceOnline = undefined;
  item.remote = false;
  const state = toJson(InboxItemSchema, item) as object;
  if (row && JSON.stringify(state) === before && JSON.stringify(scratch) === JSON.stringify(row.scratch) && u.active === undefined) return;
  const fields = {
    hostId: hostId || row?.hostId || "",
    grp: item.group,
    state,
    scratch: scratch as object,
    projectRun: !!item.projectRun,
    active: u.active ?? row?.active ?? true,
    attentionAt: attention,
    updatedAt: new Date(),
  };
  await db
    .insert(schema.relayInbox)
    .values({ sessionId, userId, ...fields })
    .onConflictDoUpdate({ target: schema.relayInbox.sessionId, set: fields });
  relayBus().publishInbox(userId, [sessionId]);
}

/** Online state of the caller's hosts, by host id. */
async function hostsOnline(userId: string): Promise<Map<string, boolean>> {
  const rows = await getDb()
    .select({ id: schema.relayHost.id, lastSeenAt: schema.relayHost.lastSeenAt })
    .from(schema.relayHost)
    .where(eq(schema.relayHost.userId, userId));
  const hb = Math.min(Number(process.env.HARNESS_RELAY_HEARTBEAT_MS) || 20_000, 20_000);
  const now = Date.now();
  return new Map(rows.map((h) => [h.id, now - h.lastSeenAt.getTime() < hb * MISSED_HEARTBEATS + 5_000]));
}

/** The item as the caller sees it now. */
function present(row: Row, online: Map<string, boolean>): InboxItem {
  const item = itemOf(row);
  item.group = row.grp as InboxGroup;
  item.unread = isUnread(row);
  item.open = row.active && item.open;
  item.remote = true;
  const on = online.get(row.hostId);
  if (on !== undefined) item.deviceOnline = on;
  return item;
}

/** Rows the inbox lists: something needed, and an open session or an unread one that closed lately. */
function visible(userId: string): SQL {
  const r = schema.relayInbox;
  const since = new Date(Date.now() - CLOSED_KEEP_MS);
  return and(
    eq(r.userId, userId),
    sql`${r.grp} <> 0`,
    or(eq(r.active, true), and(gt(r.attentionAt, since), or(isNull(r.readAt), sql`${r.attentionAt} > ${r.readAt}`))),
  )!;
}

const unreadExpr = sql<boolean>`(${schema.relayInbox.attentionAt} is not null and (${schema.relayInbox.readAt} is null or ${schema.relayInbox.attentionAt} > ${schema.relayInbox.readAt}))`;

export async function inboxCounts(userId: string): Promise<InboxCount[]> {
  const r = schema.relayInbox;
  const rows = await getDb()
    .select({ grp: r.grp, total: sql<number>`count(*)`, unread: sql<number>`sum(case when ${unreadExpr} then 1 else 0 end)` })
    .from(r)
    .where(visible(userId))
    .groupBy(r.grp);
  const by = new Map(rows.map((x) => [x.grp, x]));
  return GROUPS.map((g) => create(InboxCountSchema, { group: g, total: Number(by.get(g)?.total ?? 0), unread: Number(by.get(g)?.unread ?? 0) }));
}

function pageOffset(token: string): number {
  if (!token) return 0;
  const n = Number(Buffer.from(token, "base64url").toString("utf8"));
  if (!Number.isInteger(n) || n < 0) throw new ApiError("invalid_request", "Invalid page_token");
  return n;
}

/**
 * One page of a group. Offset paging: the order (unread, then attention) moves as items are read, so a cursor would
 * not be stable either; a frontend follows WatchInbox and lists again on resync.
 */
export async function listInbox(p: Principal, req: ListInboxRequest): Promise<Init<typeof ListInboxResponseSchema>> {
  const r = schema.relayInbox;
  const size = Math.min(Math.max(req.pageSize || DEFAULT_PAGE, 1), MAX_PAGE);
  const offset = pageOffset(req.pageToken);
  let where: SQL;
  if (req.projectRunsOnly) {
    where = and(eq(r.userId, p.user.id), eq(r.projectRun, true), gt(r.updatedAt, new Date(Date.now() - RUN_KEEP_MS)))!;
  } else {
    if (!GROUPS.includes(req.group)) throw new ApiError("invalid_request", "group is required");
    where = and(visible(p.user.id), eq(r.grp, req.group))!;
  }
  const rows = await getDb()
    .select()
    .from(r)
    .where(where)
    .orderBy(desc(unreadExpr), sql`${r.attentionAt} desc nulls last`, desc(r.updatedAt), asc(r.sessionId))
    .limit(size + 1)
    .offset(offset);
  const online = await hostsOnline(p.user.id);
  const more = rows.length > size;
  return {
    items: rows.slice(0, size).map((x) => present(x, online)),
    nextPageToken: more ? Buffer.from(String(offset + size)).toString("base64url") : "",
    counts: await inboxCounts(p.user.id),
  };
}

/** The current item of each session, or undefined when it left the inbox. */
async function itemsNow(userId: string, ids: string[]): Promise<Map<string, InboxItem | undefined>> {
  const out = new Map<string, InboxItem | undefined>(ids.map((id) => [id, undefined]));
  if (!ids.length) return out;
  const r = schema.relayInbox;
  const rows = await getDb()
    .select()
    .from(r)
    .where(and(visible(userId), inArray(r.sessionId, ids)));
  const online = await hostsOnline(userId);
  for (const row of rows) out.set(row.sessionId, present(row, online));
  // Coordinator rows leave the groups but stay listed for the project runs screen.
  const runs = await getDb()
    .select()
    .from(r)
    .where(and(eq(r.userId, userId), eq(r.projectRun, true), inArray(r.sessionId, ids)));
  for (const row of runs) if (!out.get(row.sessionId)) out.set(row.sessionId, present(row, online));
  return out;
}

export async function* watchInbox(p: Principal, signal: AbortSignal): AsyncGenerator<Init<typeof WatchInboxResponseSchema>> {
  const queue = new AsyncQueue<string[]>();
  const unsubscribe = relayBus().subscribeInbox(p.user.id, (ids) => queue.push(ids));
  try {
    yield { event: { case: "caughtUp", value: true } };
    const interval = Math.min(Number(process.env.HARNESS_RELAY_HEARTBEAT_MS) || 20_000, 20_000);
    while (!signal.aborted) {
      const ids = await queue.next(interval, signal);
      if (signal.aborted) return;
      if (!ids) {
        yield { event: { case: "heartbeat", value: true } };
        continue;
      }
      if (queue.size > MAX_BACKLOG) {
        while (queue.size) await queue.next(0);
        yield { event: { case: "resync", value: true } };
        continue;
      }
      // Coalesce what is waiting: one change per session.
      const all = new Set(ids);
      while (queue.size) for (const id of (await queue.next(0)) ?? []) all.add(id);
      const now = await itemsNow(p.user.id, [...all]);
      const counts = await inboxCounts(p.user.id);
      for (const [id, item] of now) {
        yield item ? { event: { case: "change", value: { item } }, counts } : { event: { case: "change", value: { removedSessionId: id } }, counts };
      }
    }
  } finally {
    unsubscribe();
  }
}

export async function markInboxRead(p: Principal, ids: string[], read: boolean): Promise<void> {
  const clean = [...new Set(ids.filter((x) => typeof x === "string" && x && x.length <= 128))].slice(0, 500);
  if (!clean.length) return;
  const db = getDb();
  const r = schema.relayInbox;
  const where = and(eq(r.userId, p.user.id), inArray(r.sessionId, clean));
  if (read) await db.update(r).set({ readAt: new Date() }).where(where);
  else await db.update(r).set({ readAt: null }).where(where);
  // The sidebar's unread flag of the same sessions (SessionInfo.unread on the server copy) follows.
  if (read) {
    const s = schema.syncSession;
    await db
      .update(s)
      .set({ info: sql`jsonb_set(${s.info}, '{unread}', 'false'::jsonb, true)` })
      .where(and(eq(s.userId, p.user.id), inArray(s.id, clean)));
  }
  relayBus().publishInbox(p.user.id, clean);
}
