/**
 * What happens to new VcsEvents: store + publish (events.ts), update the PR badge of linked sessions
 * (sync_session info.pull_request, so every device sees it), and auto-fix.
 *
 * Auto-fix: on CHECK_FAILED / REVIEW_COMMENT / CONFLICT_DETECTED for a linked session whose flag is on
 * (SetAutoFix), an auto-fix job is recorded (failed checks with a log excerpt from GetCheckLog, comment ids,
 * the conflicting branches) and a follow-up VcsEvent of the same kind with detail.auto_fix is emitted to that
 * session, so a connected client can act on it.
 *
 * TODO(relay): deliver jobs into the live session through the relay, or spawn a session on a daemon host
 * when none is live (VcsService.SetAutoFix contract). Until the relay exists, jobs stay "pending"; the relay
 * subscribes with onAutoFixJob() and marks them delivered with markAutoFixJob().
 */
import { and, desc, eq, inArray } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ProviderType, VcsEvent_Kind } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal } from "../services/devices.ts";
import { setSessionPullRequest } from "../services/sessions.ts";
import { systemClient, userClient } from "./clients.ts";
import { connectionForHost } from "./connections.ts";
import { background, sessionsOf, storeEvents, type EventInput, type EventRow, type SessionRef } from "./events.ts";
import type { PrData } from "./types.ts";
import { parseRepoKey } from "./util.ts";

export type AutoFixJobRow = typeof schema.vcsAutofixJob.$inferSelect;

/** Log excerpt size per failed check in an auto-fix job. */
const LOG_EXCERPT_BYTES = 16 * 1024;
const MAX_LOGS = 3;

export async function publishEvents(inputs: EventInput[]): Promise<EventRow[]> {
  const rows = await storeEvents(inputs);
  for (const r of rows) {
    const pr = r.pullRequest as PrData | null;
    if (!pr?.url || !pr.number) continue;
    for (const s of sessionsOf(r)) await setSessionPullRequest(s.sessionId, { url: pr.url, number: pr.number, state: pr.state });
  }
  if (rows.some((r) => AUTOFIX_KINDS.has(r.kind) && !(r.detail as Record<string, unknown>)?.auto_fix && sessionsOf(r).length)) {
    background(() => autoFix(rows));
  }
  return rows;
}

/** Updates the PR badge of the given sessions (LinkSession, CreatePullRequest). */
export async function updateBadges(sessions: SessionRef[], pr: PrData): Promise<void> {
  for (const s of sessions) await setSessionPullRequest(s.sessionId, { url: pr.url, number: pr.number, state: pr.state });
}

// --- auto-fix ------------------------------------------------------------------------------------

const AUTOFIX_KINDS = new Set<number>([VcsEvent_Kind.CHECK_FAILED, VcsEvent_Kind.REVIEW_COMMENT, VcsEvent_Kind.CONFLICT_DETECTED]);

type JobHook = (job: AutoFixJobRow) => void | Promise<void>;
const jobHooks = new Set<JobHook>();

/** Internal hook for the relay (not built yet): called for every new or updated pending job. */
export function onAutoFixJob(fn: JobHook): () => void {
  jobHooks.add(fn);
  return () => jobHooks.delete(fn);
}

export async function markAutoFixJob(id: string, status: "delivered" | "failed"): Promise<void> {
  await getDb().update(schema.vcsAutofixJob).set({ status, updatedAt: new Date() }).where(eq(schema.vcsAutofixJob.id, id));
}

function flagFor(kind: number, link: typeof schema.vcsSessionLink.$inferSelect): boolean {
  if (kind === VcsEvent_Kind.CHECK_FAILED) return link.autoFixCi;
  if (kind === VcsEvent_Kind.REVIEW_COMMENT) return link.autoFixReviews;
  return link.autoFixConflicts;
}

interface FailedCheck {
  id: string;
  name: string;
  url?: string;
  summary?: string;
}

async function logExcerpts(row: EventRow, userId: string, checks: FailedCheck[]): Promise<Record<string, string>> {
  const out: Record<string, string> = {};
  // Device-provider events: the server cannot reach the host; the client fetches logs through gh/glab.
  if (row.source !== ProviderType.GITHUB && row.source !== ProviderType.GITLAB) return out;
  const repo = parseRepoKey(row.repositoryKey);
  const conn = await connectionForHost(repo.host);
  if (!conn) return out;
  const client = systemClient(conn) ?? (await userClient(conn, userId));
  if (!client) return out;
  for (const c of checks.slice(0, MAX_LOGS)) {
    try {
      const r = await client.getCheckLog(repo, c.id, LOG_EXCERPT_BYTES);
      out[c.id] = r.log;
    } catch (err) {
      out[c.id] = `(log unavailable: ${err instanceof ApiError ? err.message : String(err)})`;
    }
  }
  return out;
}

async function autoFix(rows: EventRow[]): Promise<void> {
  await ensureDb();
  const db = getDb();
  for (const row of rows) {
    if (!AUTOFIX_KINDS.has(row.kind)) continue;
    const detail = (row.detail ?? {}) as Record<string, unknown>;
    if (detail.auto_fix) continue;
    const sessions = sessionsOf(row);
    if (!sessions.length) continue;
    const links = await db.select().from(schema.vcsSessionLink).where(inArray(schema.vcsSessionLink.sessionId, sessions.map((s) => s.sessionId)));
    for (const s of sessions) {
      const link = links.find((l) => l.sessionId === s.sessionId);
      if (!link || !flagFor(row.kind, link)) continue;
      const pr = row.pullRequest as PrData | null;
      const headSha = pr?.headSha ?? String(detail.sha ?? "");
      const payload: Record<string, unknown> = { repository_key: row.repositoryKey, pr_number: row.prNumber, head_branch: row.headBranch, pr_url: pr?.url ?? "" };
      if (row.kind === VcsEvent_Kind.CHECK_FAILED) {
        const failed = (Array.isArray(detail.checks) ? detail.checks : []) as FailedCheck[];
        payload.failed_checks = failed;
        payload.log_excerpts = await logExcerpts(row, s.userId, failed);
      } else if (row.kind === VcsEvent_Kind.REVIEW_COMMENT) {
        payload.comment_ids = Array.isArray(detail.comment_ids) ? detail.comment_ids : [];
        payload.comments = [{ author: detail.author ?? "", body: detail.body ?? "", path: detail.path ?? "", line: detail.line ?? 0, url: detail.url ?? "" }];
      } else {
        payload.base_branch = pr?.baseBranch ?? "";
      }
      const job = await upsertJob(row, s, headSha, payload);
      for (const h of jobHooks) await Promise.resolve(h(job)).catch((err) => console.error("[harness] auto-fix hook failed", err));
      await publishEvents([
        {
          kind: row.kind,
          source: row.source,
          connectionId: row.connectionId,
          repositoryKey: row.repositoryKey,
          pr: pr ?? undefined,
          prNumber: row.prNumber,
          headBranch: row.headBranch,
          sessions: [s],
          actorUserId: row.actorUserId,
          detail: { ...detail, auto_fix: { job_id: job.id, session_id: s.sessionId, status: job.status, ...(job.payload as object) } },
        },
      ]);
    }
  }
}

/** A pending job for the same session, kind and head commit absorbs new failures / comments. */
async function upsertJob(row: EventRow, s: SessionRef, headSha: string, payload: Record<string, unknown>): Promise<AutoFixJobRow> {
  const db = getDb();
  const j = schema.vcsAutofixJob;
  const now = new Date();
  const [cur] = await db
    .select()
    .from(j)
    .where(and(eq(j.sessionId, s.sessionId), eq(j.kind, row.kind), eq(j.headSha, headSha), eq(j.status, "pending"), eq(j.prNumber, row.prNumber)))
    .orderBy(desc(j.createdAt))
    .limit(1);
  if (cur && headSha) {
    const prev = (cur.payload ?? {}) as Record<string, unknown>;
    const merged: Record<string, unknown> = { ...prev, ...payload };
    for (const k of ["failed_checks", "comment_ids", "comments"]) {
      if (Array.isArray(prev[k]) || Array.isArray(payload[k])) {
        const all = [...((prev[k] as unknown[]) ?? []), ...((payload[k] as unknown[]) ?? [])];
        merged[k] = all.filter((x, i) => all.findIndex((y) => JSON.stringify(y) === JSON.stringify(x)) === i);
      }
    }
    if (prev.log_excerpts || payload.log_excerpts) merged.log_excerpts = { ...(prev.log_excerpts as object), ...(payload.log_excerpts as object) };
    const [r] = await db.update(j).set({ payload: merged, eventSeq: row.seq, updatedAt: now }).where(eq(j.id, cur.id)).returning();
    return r!;
  }
  const [r] = await db
    .insert(j)
    .values({
      id: newId("afx"),
      sessionId: s.sessionId,
      userId: s.userId,
      eventSeq: row.seq,
      kind: row.kind,
      repositoryKey: row.repositoryKey,
      prNumber: row.prNumber,
      headSha,
      status: "pending",
      payload,
      createdAt: now,
      updatedAt: now,
    })
    .returning();
  return r!;
}

/** Auto-fix jobs of a session (owner only), newest first. */
export async function listAutoFixJobs(p: Principal, sessionId: string, limit = 50): Promise<AutoFixJobRow[]> {
  await ensureDb();
  const s = await getDb().query.syncSession.findFirst({ where: eq(schema.syncSession.id, sessionId) });
  if (!s || s.userId !== p.user.id) throw new ApiError("not_found", `Session ${sessionId} not found`);
  return getDb()
    .select()
    .from(schema.vcsAutofixJob)
    .where(eq(schema.vcsAutofixJob.sessionId, sessionId))
    .orderBy(desc(schema.vcsAutofixJob.createdAt))
    .limit(Math.min(Math.max(limit, 1), 200));
}
