/**
 * Best-of-N (proto/harness/relay/v1/hosts.proto; docs/design/hosts.md, decisions 28 to 33): a group of attempts of one
 * prompt. On a host each attempt is host work of kind ATTEMPT, queued and claimed like any; locally the calling client makes
 * the sessions and reports them here. A group is its owner's alone.
 */
import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf";
import { and, desc, eq, inArray, sql } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import { AttemptCheckSchema, PermissionMode, type AttemptCheck } from "../gen/harness/agent/v1/agent_pb.ts";
import {
  AttemptState,
  HostWorkKind,
  HostWorkLanding,
  HostWorkState,
  type AttemptGroupSchema,
  type AttemptSchema,
  type ReportAttemptRequest,
  type StartAttemptsRequest,
} from "../gen/harness/relay/v1/hosts_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal } from "../services/devices.ts";
import { serverPolicyFor } from "../services/managed-policy.ts";
import { parseRepoKey } from "../vcs/util.ts";
import { hostName, insertWork, unavailableReason, usableHost, wake } from "./hosts.ts";

type Init<T extends Parameters<typeof create>[0]> = MessageInitShape<T>;
type GroupRow = typeof schema.attemptGroup.$inferSelect;
type AttemptRow = typeof schema.attempt.$inferSelect;
type WorkRow = typeof schema.hostWork.$inferSelect;

/** At most this many attempts, whatever the policy says. */
export const MAX_ATTEMPTS = 5;
const MIN_ATTEMPTS = 2;
const MAX_CHECK_OUTPUT = 8 * 1024;
const MAX_GROUPS_LISTED = 50;

const encodeCheck = (c: AttemptCheck) => Buffer.from(toBinary(AttemptCheckSchema, c)).toString("base64");
const decodeCheck = (s: string) => (s ? fromBinary(AttemptCheckSchema, new Uint8Array(Buffer.from(s, "base64"))) : undefined);

const DONE_ATTEMPT: readonly number[] = [AttemptState.DISCARDING, AttemptState.DISCARDED];

/** The caller's own group; NOT_FOUND for anyone else's, admins included (decision 33). */
async function ownGroup(p: Principal, id: string): Promise<GroupRow> {
  if (!id) throw new ApiError("invalid_request", "group_id is required");
  const [row] = await getDb().select().from(schema.attemptGroup).where(eq(schema.attemptGroup.id, id));
  if (!row || row.userId !== p.user.id) throw new ApiError("not_found", `Attempt group ${id} not found`);
  return row;
}

/** What one attempt's work row says, folded into the attempt: a host's attempt whose work ended early failed. */
function attemptView(a: AttemptRow, work: WorkRow | undefined, kept: boolean): Init<typeof AttemptSchema> {
  let state = a.state;
  let error = a.error;
  if (work && !DONE_ATTEMPT.includes(state)) {
    if (work.state === HostWorkState.FAILED || work.state === HostWorkState.EXPIRED || work.state === HostWorkState.CANCELLED) {
      state = AttemptState.FAILED;
      error ||= work.error || (work.state === HostWorkState.EXPIRED ? "Waited too long for the host" : "Cancelled");
    }
  }
  return {
    index: a.index,
    state,
    workId: a.workId,
    sessionId: a.sessionId || work?.sessionId || "",
    branch: a.branch || work?.branch || "",
    error,
    check: decodeCheck(a.check),
    kept,
    updatedAt: a.updatedAt.toISOString(),
  };
}

async function groupView(g: GroupRow): Promise<Init<typeof AttemptGroupSchema>> {
  const db = getDb();
  const rows = await db.select().from(schema.attempt).where(eq(schema.attempt.groupId, g.id)).orderBy(schema.attempt.index);
  const workIds = rows.map((r) => r.workId).filter(Boolean);
  const works = workIds.length ? await db.select().from(schema.hostWork).where(inArray(schema.hostWork.id, workIds)) : [];
  return {
    id: g.id,
    prompt: g.prompt,
    title: g.title,
    hostId: g.hostId,
    hostName: g.hostId ? await hostName(g.hostId) : "",
    deviceId: g.deviceId,
    cwd: g.cwd,
    repositoryKey: g.repositoryKey,
    baseRef: g.baseRef,
    model: g.model,
    effort: g.effort,
    permissionMode: g.permissionMode,
    testCommand: g.testCommand,
    parallel: g.parallel,
    budgetUsd: g.budgetUsd,
    keptIndex: g.keptIndex,
    attempts: rows.map((a) => attemptView(a, works.find((w) => w.id === a.workId), a.index === g.keptIndex)),
    createdAt: g.createdAt.toISOString(),
    updatedAt: g.updatedAt.toISOString(),
  };
}

/** Decision 29: what applies once the policy capped the user's choices. */
export function attemptLimits(
  policy: { maxAttempts: number; maxParallelAttempts: number; attemptBudgetUsd: number },
  want: { count: number; parallel: number; budgetUsd: number },
): { count: number; parallel: number; budgetUsd: number } {
  const most = Math.min(MAX_ATTEMPTS, policy.maxAttempts || MAX_ATTEMPTS);
  if (!Number.isInteger(want.count) || want.count < MIN_ATTEMPTS || want.count > most) {
    throw new ApiError("invalid_request", most < MAX_ATTEMPTS ? `Your organization allows ${MIN_ATTEMPTS} to ${most} attempts` : `A group has ${MIN_ATTEMPTS} to ${MAX_ATTEMPTS} attempts`);
  }
  if (!Number.isFinite(want.parallel) || want.parallel < 0) throw new ApiError("invalid_request", "parallel can't be negative");
  if (!Number.isFinite(want.budgetUsd) || want.budgetUsd < 0) throw new ApiError("invalid_request", "budget_usd can't be negative");
  let parallel = Math.min(Math.floor(want.parallel) || want.count, want.count);
  if (policy.maxParallelAttempts > 0) parallel = Math.min(parallel, policy.maxParallelAttempts);
  let budgetUsd = want.budgetUsd;
  if (policy.attemptBudgetUsd > 0) budgetUsd = budgetUsd > 0 ? Math.min(budgetUsd, policy.attemptBudgetUsd) : policy.attemptBudgetUsd;
  return { count: want.count, parallel: Math.max(1, parallel), budgetUsd };
}

export async function startAttempts(p: Principal, req: StartAttemptsRequest): Promise<Init<typeof AttemptGroupSchema>> {
  if (!req.prompt.trim()) throw new ApiError("invalid_request", "prompt is required");
  if (req.permissionMode === PermissionMode.BYPASS_PERMISSIONS) throw new ApiError("permission", "bypassPermissions cannot be used for attempts");
  const testCommand = req.testCommand.trim();
  if (testCommand.length > 2000) throw new ApiError("invalid_request", "The test command has at most 2000 characters");
  const limits = attemptLimits(await serverPolicyFor(p.user), { count: req.count, parallel: req.parallel, budgetUsd: req.budgetUsd });
  const db = getDb();
  const g = schema.attemptGroup;
  if (req.idempotencyKey) {
    const [same] = await db.select().from(g).where(and(eq(g.userId, p.user.id), eq(g.idempotencyKey, req.idempotencyKey)));
    if (same) return groupView(same);
  }
  let hostId = "";
  let repositoryKey = "";
  if (req.hostId) {
    const host = await usableHost(p, req.hostId);
    const reason = await unavailableReason(host, p.user);
    if (reason) throw new ApiError("failed_precondition", reason);
    hostId = host.id;
    repositoryKey = parseRepoKey(req.repositoryKey).key;
  } else {
    // Local worktrees: the calling client runs them, so it must be one.
    if (!p.deviceId) throw new ApiError("failed_precondition", "Local attempts are started by a client");
    if (!req.cwd.trim()) throw new ApiError("invalid_request", "cwd is required for local attempts");
    repositoryKey = req.repositoryKey ? parseRepoKey(req.repositoryKey).key : "";
  }
  const now = new Date();
  const id = newId("atg");
  const [group] = await db
    .insert(g)
    .values({
      id,
      userId: p.user.id,
      hostId,
      deviceId: hostId ? "" : p.deviceId!,
      cwd: hostId ? "" : req.cwd.trim(),
      prompt: req.prompt,
      title: req.title.trim().slice(0, 200),
      repositoryKey,
      baseRef: req.baseRef.trim(),
      model: req.model,
      effort: req.effort,
      permissionMode: req.permissionMode,
      testCommand,
      parallel: limits.parallel,
      budgetUsd: limits.budgetUsd,
      idempotencyKey: req.idempotencyKey || null,
      createdAt: now,
      updatedAt: now,
    })
    .returning();
  for (let i = 0; i < limits.count; i++) {
    let workId = "";
    if (hostId) {
      const work = await insertWork({
        hostId,
        userId: p.user.id,
        kind: HostWorkKind.ATTEMPT,
        repositoryKey,
        baseRef: req.baseRef.trim(),
        landing: HostWorkLanding.BRANCH,
        prompt: req.prompt,
        title: `${req.title.trim().slice(0, 180) || req.prompt.trim().split("\n")[0]!.slice(0, 180)} (${i + 1}/${limits.count})`,
        model: req.model,
        effort: req.effort,
        permissionMode: req.permissionMode,
        attemptGroupId: id,
        attemptIndex: i,
        testCommand,
        budgetUsd: limits.budgetUsd,
        idempotencyKey: `${id}:${i}`,
      });
      workId = work.id;
    }
    await db.insert(schema.attempt).values({ groupId: id, index: i, state: AttemptState.PENDING, workId, updatedAt: now });
  }
  if (hostId) wake(hostId);
  return groupView(group!);
}

export async function getAttemptGroup(p: Principal, id: string): Promise<Init<typeof AttemptGroupSchema>> {
  return groupView(await ownGroup(p, id));
}

export async function listAttemptGroups(p: Principal, limit: number): Promise<Init<typeof AttemptGroupSchema>[]> {
  const n = Math.min(Math.max(limit || 20, 1), MAX_GROUPS_LISTED);
  const rows = await getDb().select().from(schema.attemptGroup).where(eq(schema.attemptGroup.userId, p.user.id)).orderBy(desc(schema.attemptGroup.createdAt)).limit(n);
  const out: Init<typeof AttemptGroupSchema>[] = [];
  for (const r of rows) out.push(await groupView(r));
  return out;
}

async function attemptOf(group: GroupRow, index: number): Promise<AttemptRow> {
  const [row] = await getDb().select().from(schema.attempt).where(and(eq(schema.attempt.groupId, group.id), eq(schema.attempt.index, index)));
  if (!row) throw new ApiError("not_found", `Attempt ${index + 1} of ${group.id} not found`);
  return row;
}

const REPORTABLE: readonly number[] = [AttemptState.PENDING, AttemptState.RUNNING, AttemptState.IDLE, AttemptState.FAILED, AttemptState.DISCARDED];

/** From the client or host that runs the attempt. A discarded attempt stays so. */
export async function reportAttempt(p: Principal, req: ReportAttemptRequest): Promise<void> {
  const group = await ownGroup(p, req.groupId);
  const row = await attemptOf(group, req.index);
  if (row.state === AttemptState.DISCARDED) return;
  const set: Partial<AttemptRow> = { updatedAt: new Date() };
  if (req.state !== AttemptState.UNSPECIFIED) {
    if (!REPORTABLE.includes(req.state)) throw new ApiError("invalid_request", "Not a state to report");
    // What was discarded can only become gone.
    if (row.state !== AttemptState.DISCARDING || req.state === AttemptState.DISCARDED) set.state = req.state;
  }
  if (req.sessionId) set.sessionId = req.sessionId.slice(0, 200);
  if (req.branch) set.branch = req.branch.slice(0, 250);
  if (req.error) set.error = req.error.slice(0, 2000);
  if (req.check) {
    const check = create(AttemptCheckSchema, { ...req.check, groupId: group.id, index: row.index, output: req.check.output.slice(-MAX_CHECK_OUTPUT), command: req.check.command.slice(0, 2000) });
    set.check = encodeCheck(check);
  }
  await getDb().update(schema.attempt).set(set).where(and(eq(schema.attempt.groupId, group.id), eq(schema.attempt.index, row.index)));
  await getDb().update(schema.attemptGroup).set({ updatedAt: new Date() }).where(eq(schema.attemptGroup.id, group.id));
  // An attempt that finished its turn frees its place for the next one (decision 29).
  if (group.hostId) wake(group.hostId);
}

export async function keepAttempt(p: Principal, groupId: string, index: number): Promise<Init<typeof AttemptGroupSchema>> {
  const group = await ownGroup(p, groupId);
  const row = await attemptOf(group, index);
  if (DONE_ATTEMPT.includes(row.state)) throw new ApiError("failed_precondition", "That attempt was discarded");
  const [next] = await getDb().update(schema.attemptGroup).set({ keptIndex: index, updatedAt: new Date() }).where(eq(schema.attemptGroup.id, group.id)).returning();
  return groupView(next!);
}

/** Decision 32: the others go. A host's still queued are cancelled; those with a session are handed to their daemon. */
export async function discardAttempts(p: Principal, groupId: string): Promise<Init<typeof AttemptGroupSchema>> {
  const group = await ownGroup(p, groupId);
  if (group.keptIndex < 0) throw new ApiError("failed_precondition", "Keep one attempt first");
  if (!group.hostId) throw new ApiError("failed_precondition", "Local attempts are removed by the client that runs them");
  const db = getDb();
  const rows = await db.select().from(schema.attempt).where(eq(schema.attempt.groupId, group.id));
  const now = new Date();
  for (const a of rows) {
    if (a.index === group.keptIndex || DONE_ATTEMPT.includes(a.state)) continue;
    const [work] = a.workId ? await db.select().from(schema.hostWork).where(eq(schema.hostWork.id, a.workId)) : [];
    let state: AttemptState = AttemptState.DISCARDING;
    if (!work || work.state === HostWorkState.QUEUED || work.state === HostWorkState.EXPIRED || work.state === HostWorkState.CANCELLED || (work.state === HostWorkState.FAILED && !work.sessionId)) {
      // Nothing on the host to remove: no session was made, or the daemon dropped its checkout when it failed.
      if (work && work.state === HostWorkState.QUEUED) await db.update(schema.hostWork).set({ state: HostWorkState.CANCELLED, updatedAt: now }).where(and(eq(schema.hostWork.id, work.id), eq(schema.hostWork.state, HostWorkState.QUEUED)));
      state = AttemptState.DISCARDED;
    } else {
      await db.update(schema.hostWork).set({ discardRequested: true, updatedAt: now }).where(eq(schema.hostWork.id, work.id));
    }
    await db.update(schema.attempt).set({ state, updatedAt: now }).where(and(eq(schema.attempt.groupId, group.id), eq(schema.attempt.index, a.index)));
  }
  await db.update(schema.attemptGroup).set({ updatedAt: now }).where(eq(schema.attemptGroup.id, group.id));
  wake(group.hostId);
  return groupView((await ownGroup(p, group.id))!);
}

/** Whether another attempt of the group may start: fewer than `parallel` of them are claimed or at work. */
export async function attemptHasPlace(groupId: string): Promise<boolean> {
  const db = getDb();
  const [g] = await db.select({ parallel: schema.attemptGroup.parallel }).from(schema.attemptGroup).where(eq(schema.attemptGroup.id, groupId));
  if (!g) return true;
  const a = schema.attempt;
  const w = schema.hostWork;
  const [{ n } = { n: 0 }] = await db
    .select({ n: sql<number>`count(*)::int` })
    .from(a)
    .innerJoin(w, eq(w.id, a.workId))
    .where(and(eq(a.groupId, groupId), eq(a.state, AttemptState.RUNNING), inArray(w.state, [HostWorkState.CLAIMED, HostWorkState.RUNNING])));
  return n < g.parallel;
}

/** Mirrors a host attempt's work row on its attempt: session, branch, and the state given. */
export async function setAttemptFromWork(work: Pick<WorkRow, "attemptGroupId" | "attemptIndex" | "sessionId" | "branch">, v: { state?: AttemptState; error?: string }): Promise<void> {
  const db = getDb();
  const [row] = await db.select().from(schema.attempt).where(and(eq(schema.attempt.groupId, work.attemptGroupId), eq(schema.attempt.index, work.attemptIndex)));
  if (!row) return;
  const set: Partial<AttemptRow> = { updatedAt: new Date() };
  if (work.sessionId) set.sessionId = work.sessionId;
  if (work.branch) set.branch = work.branch;
  if (v.error) set.error = v.error.slice(0, 2000);
  if (v.state !== undefined && row.state !== AttemptState.DISCARDED && (row.state !== AttemptState.DISCARDING || v.state === AttemptState.DISCARDED)) {
    // A claim after a lost lease does not undo an attempt that already finished its turn.
    if (!(v.state === AttemptState.RUNNING && row.state === AttemptState.IDLE)) set.state = v.state;
  }
  await db.update(schema.attempt).set(set).where(and(eq(schema.attempt.groupId, row.groupId), eq(schema.attempt.index, row.index)));
}
