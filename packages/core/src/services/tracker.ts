/**
 * The task tracker: a user's own tasks across sessions and machines, for its screen and the TaskTracker tool. Every query
 * here is by owner: a task of someone else's does not exist for the caller, whatever their role.
 */
import { and, desc, eq, ilike, inArray, or, sql } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal } from "./devices.ts";

export type TrackerStatusName = "todo" | "in_progress" | "blocked" | "done" | "cancelled";
export type TrackerPriorityName = "low" | "normal" | "high";
export type TrackerRow = typeof schema.trackerTask.$inferSelect;

const TITLE_MAX = 300;
const DESCRIPTION_MAX = 64 << 10;
const PROJECT_MAX = 300;
const MAX_LINKS = 100;
const CLOSED: readonly string[] = ["done", "cancelled"];

function checkTitle(title: string): string {
  const t = title.trim();
  if (!t) throw new ApiError("invalid_request", "title is required");
  if (t.length > TITLE_MAX) throw new ApiError("invalid_request", `Titles have at most ${TITLE_MAX} characters`);
  return t;
}

function checkDescription(text: string): string {
  if (text.length > DESCRIPTION_MAX) throw new ApiError("invalid_request", `Descriptions have at most ${DESCRIPTION_MAX} characters`);
  return text;
}

function checkProject(project: string): string {
  if (project.length > PROJECT_MAX) throw new ApiError("invalid_request", `Projects have at most ${PROJECT_MAX} characters`);
  return project.trim();
}

/** Only the caller's own tasks may block a task, and a task never itself. */
async function checkBlockedBy(owner: string, ids: string[], self?: string): Promise<string[]> {
  const list = [...new Set(ids)];
  if (self && list.includes(self)) throw new ApiError("invalid_request", "A task cannot block itself");
  if (!list.length) return [];
  if (list.length > MAX_LINKS) throw new ApiError("invalid_request", `A task has at most ${MAX_LINKS} blockers`);
  const t = schema.trackerTask;
  const found = await getDb().select({ id: t.id }).from(t).where(and(eq(t.ownerId, owner), inArray(t.id, list)));
  const known = new Set(found.map((r) => r.id));
  const missing = list.filter((id) => !known.has(id));
  if (missing.length) throw new ApiError("invalid_request", `No such task: ${missing.join(", ")}`);
  return list;
}

function withSession(list: unknown, sessionId: string): string[] {
  const ids = Array.isArray(list) ? (list as string[]) : [];
  if (!sessionId || ids.includes(sessionId)) return ids;
  return [...ids, sessionId].slice(-MAX_LINKS);
}

export async function createTrackerTask(
  p: Principal,
  input: { title: string; description?: string; status?: TrackerStatusName; priority?: TrackerPriorityName; project?: string; session_id?: string; blocked_by?: string[] },
): Promise<TrackerRow> {
  await ensureDb();
  const now = new Date();
  const status = input.status ?? "todo";
  const row: TrackerRow = {
    id: newId("task"),
    ownerId: p.user.id,
    title: checkTitle(input.title),
    description: checkDescription(input.description ?? ""),
    status,
    priority: input.priority ?? "normal",
    project: checkProject(input.project ?? ""),
    sessionIds: withSession([], input.session_id ?? ""),
    blockedBy: await checkBlockedBy(p.user.id, input.blocked_by ?? []),
    createdAt: now,
    updatedAt: now,
    closedAt: CLOSED.includes(status) ? now : null,
  };
  await getDb().insert(schema.trackerTask).values(row);
  return row;
}

export async function listTrackerTasks(p: Principal, input: { statuses?: TrackerStatusName[]; project?: string; query?: string; limit?: number }): Promise<TrackerRow[]> {
  await ensureDb();
  const t = schema.trackerTask;
  const conds = [eq(t.ownerId, p.user.id)];
  if (input.statuses?.length) conds.push(inArray(t.status, input.statuses));
  if (input.project) conds.push(eq(t.project, input.project));
  const q = input.query?.trim();
  if (q) {
    const pat = `%${q.replace(/[\\%_]/g, "\\$&")}%`;
    conds.push(or(ilike(t.title, pat), ilike(t.description, pat))!);
  }
  const limit = Math.min(Math.max(input.limit || 200, 1), 500);
  // Open work first, the most urgent on top; what is closed follows, newest first.
  const closed = sql`case when ${t.status} in ('done', 'cancelled') then 1 else 0 end`;
  const rank = sql`case ${t.priority} when 'high' then 0 when 'normal' then 1 else 2 end`;
  return getDb().select().from(t).where(and(...conds)).orderBy(closed, rank, desc(t.updatedAt)).limit(limit);
}

export async function getTrackerTask(p: Principal, id: string): Promise<TrackerRow> {
  if (!id) throw new ApiError("invalid_request", "id is required");
  await ensureDb();
  const t = schema.trackerTask;
  const row = await getDb().query.trackerTask.findFirst({ where: and(eq(t.id, id), eq(t.ownerId, p.user.id)) });
  if (!row) throw new ApiError("not_found", `Task ${id} not found`);
  return row;
}

export async function updateTrackerTask(
  p: Principal,
  id: string,
  patch: { title?: string; description?: string; status?: TrackerStatusName; priority?: TrackerPriorityName; project?: string; session_id?: string; blocked_by?: string[] },
): Promise<TrackerRow> {
  const row = await getTrackerTask(p, id);
  const now = new Date();
  const set: Partial<TrackerRow> = { updatedAt: now };
  if (patch.title !== undefined) set.title = checkTitle(patch.title);
  if (patch.description !== undefined) set.description = checkDescription(patch.description);
  if (patch.priority !== undefined) set.priority = patch.priority;
  if (patch.project !== undefined) set.project = checkProject(patch.project);
  if (patch.status !== undefined) {
    set.status = patch.status;
    const closed = CLOSED.includes(patch.status);
    set.closedAt = closed ? (CLOSED.includes(row.status) ? row.closedAt : now) : null;
  }
  if (patch.blocked_by !== undefined) set.blockedBy = await checkBlockedBy(p.user.id, patch.blocked_by, id);
  if (patch.session_id) set.sessionIds = withSession(row.sessionIds, patch.session_id);
  const t = schema.trackerTask;
  const [updated] = await getDb().update(t).set(set).where(and(eq(t.id, id), eq(t.ownerId, p.user.id))).returning();
  return updated!;
}

export async function deleteTrackerTask(p: Principal, id: string): Promise<void> {
  await getTrackerTask(p, id);
  const t = schema.trackerTask;
  const db = getDb();
  await db.delete(t).where(and(eq(t.id, id), eq(t.ownerId, p.user.id)));
  // Nothing keeps waiting for a task that is gone.
  await db
    .update(t)
    .set({ blockedBy: sql`${t.blockedBy} - ${id}` })
    .where(and(eq(t.ownerId, p.user.id), sql`${t.blockedBy} ? ${id}`));
}
