/**
 * Always-on hosts (proto/harness/relay/v1/hosts.proto; docs/design/hosts.md has the design and the numbered decisions).
 *
 * The server queues work, decides who may use what, and mints credentials. Nothing runs here. Claims of one host are
 * serialized with an in-process lock, which is enough for one replica, as for the relay.
 */
import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf";
import { and, asc, desc, eq, inArray, lt, sql } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import { AutoFixJob_Kind, AutoFixJobSchema, DeliverAutoFixJobRequestSchema, type AutoFixJob } from "../gen/harness/agent/v1/auto_fix_pb.ts";
import { PermissionMode } from "../gen/harness/agent/v1/agent_pb.ts";
import {
  AgentHostIsolation,
  AgentHostScope,
  HostWorkKind,
  HostWorkLanding,
  HostWorkState,
  type AgentHostSchema,
  type CreateAgentHostRequest,
  type HostWorkSchema,
  type RegisterAgentHostRequest,
  type ReportAgentHostRequest,
  type StartHostWorkRequest,
  type UpdateAgentHostRequest,
  type UpdateHostWorkRequest,
} from "../gen/harness/relay/v1/hosts_pb.ts";
import { can, need } from "../auth/roles.ts";
import { Mutex } from "../lib/async.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { createHostDevice, revokeDevice, rotateDeviceToken, type Principal } from "../services/devices.ts";
import { serverPolicyFor } from "../services/managed-policy.ts";
import { getStoredPr, prOf, type EventRow } from "../vcs/events.ts";
import { jobToProto, onUnclaimedMention } from "../vcs/pipeline.ts";
import { hostIdentity, moveSessionLink, openPullRequestAs } from "../vcs/service.ts";
import { parseRepoKey } from "../vcs/util.ts";
import { AUTOFIX_JOB_TTL_S, queueServerCommand, sessionMachineOnline, withdrawServerCommand } from "./service.ts";

type Init<T extends Parameters<typeof create>[0]> = MessageInitShape<T>;
type HostRow = typeof schema.agentHost.$inferSelect;
type DeviceRow = typeof schema.agentHostDevice.$inferSelect;
type WorkRow = typeof schema.hostWork.$inferSelect;

const DEFAULT_MAX_SESSIONS = 4;
const MAX_MAX_SESSIONS = 1000;
/** How long work waits for its host. */
const WORK_TTL_MS = 24 * 3_600_000;
const MAX_QUEUED_PER_USER = 100;
const MAX_ATTEMPTS = 3;
const MAX_POLL_S = 25;
/** How long a claim lasts without an update. */
const leaseMs = () => envNumber("HARNESS_HOST_LEASE_MS", 5 * 60_000);
/** A device that reported within this window counts as up; its reported load counts too. */
const reportFreshMs = () => envNumber("HARNESS_HOST_REPORT_FRESH_MS", 90_000);
/** How long a session's machine is away before its pending jobs go to a host. */
const awayGraceMs = () => envNumber("HARNESS_HOST_AWAY_GRACE_S", 120) * 1000;

function envNumber(name: string, def: number): number {
  const n = Number(process.env[name] ?? "");
  return Number.isFinite(n) && n > 0 ? n : def;
}

const encodeJob = (j: AutoFixJob) => Buffer.from(toBinary(AutoFixJobSchema, j)).toString("base64");
const decodeJob = (s: string) => fromBinary(AutoFixJobSchema, new Uint8Array(Buffer.from(s, "base64")));

// --- in-process wake-ups and locks ---------------------------------------------------------------------

const waiters = new Map<string, Set<() => void>>();
const hostLocks = new Map<string, Mutex>();

function wake(hostId: string): void {
  for (const fn of waiters.get(hostId) ?? []) fn();
}

/** Resolves when something happened on the host, or after ms, or when the call ends. */
function waitForChange(hostId: string, ms: number, signal: AbortSignal | undefined): Promise<void> {
  return new Promise((resolve) => {
    const set = waiters.get(hostId) ?? new Set();
    waiters.set(hostId, set);
    const done = () => {
      clearTimeout(timer);
      set.delete(done);
      signal?.removeEventListener("abort", done);
      if (!set.size) waiters.delete(hostId);
      resolve();
    };
    const timer = setTimeout(done, ms);
    set.add(done);
    signal?.addEventListener("abort", done);
    if (signal?.aborted) done();
  });
}

function withHostLock<T>(hostId: string, fn: () => Promise<T>): Promise<T> {
  let m = hostLocks.get(hostId);
  if (!m) hostLocks.set(hostId, (m = new Mutex()));
  return m.run(fn);
}

// --- lookups and access --------------------------------------------------------------------------------

async function hostOf(id: string): Promise<HostRow | undefined> {
  const [row] = await getDb().select().from(schema.agentHost).where(eq(schema.agentHost.id, id));
  return row;
}

async function inGroup(userId: string, groupId: string): Promise<boolean> {
  if (!groupId) return false;
  const m = schema.directoryGroupMember;
  const [row] = await getDb().select({ id: m.userId }).from(m).where(and(eq(m.groupId, groupId), eq(m.userId, userId)));
  return !!row;
}

/** Whether the user's work may run on this host. */
async function mayUse(userId: string, host: HostRow): Promise<boolean> {
  return host.scope === "team" ? inGroup(userId, host.groupId) : host.ownerUserId === userId;
}

/** A host the caller may use; NOT_FOUND for anyone else's, so ids do not leak. */
async function usableHost(p: Principal, hostId: string): Promise<HostRow> {
  if (!hostId) throw new ApiError("invalid_request", "host_id is required");
  const host = await hostOf(hostId);
  if (!host || !(await mayUse(p.user.id, host))) throw new ApiError("not_found", `Host ${hostId} not found`);
  return host;
}

/** The host of the calling device, for HostAgentService. */
async function callingDevice(p: Principal): Promise<{ device: DeviceRow; host: HostRow }> {
  if (!p.deviceId) throw new ApiError("permission", "Hosts must authenticate with a device token");
  const [row] = await getDb().select().from(schema.agentHostDevice).where(eq(schema.agentHostDevice.deviceId, p.deviceId));
  const host = row && (await hostOf(row.hostId));
  if (!row || !host) throw new ApiError("failed_precondition", "This device is not registered as a host; call RegisterAgentHost first");
  return { device: row, host };
}

function isFresh(at: Date | null | undefined, now = Date.now()): boolean {
  return !!at && now - at.getTime() < reportFreshMs();
}

/** Why work can't start on this host (docs/design/hosts.md, decisions 7 and 20); "" when it can. */
async function unavailableReason(host: HostRow, user: Pick<Principal["user"], "role">): Promise<string> {
  if (host.draining) return "This host is draining and takes no new work";
  if (host.scope === "team" && host.isolation !== "uid") {
    return host.isolation ? "This team host can't separate its users (it needs a daemon with --shared, as root on Linux), so it takes no work" : "This team host has not started yet";
  }
  const policy = await serverPolicyFor(user);
  if (policy.sandboxRequired && host.isolation && !host.sandboxDriver) return "Your organization requires the command sandbox, and this host has no sandbox driver";
  if (policy.personalHostsDisabled && host.scope === "user") return "Your organization allows only hosts registered by an admin";
  return "";
}

// --- views ---------------------------------------------------------------------------------------------

async function hostViews(rows: HostRow[], p: Principal, withWorkers: boolean): Promise<Init<typeof AgentHostSchema>[]> {
  if (!rows.length) return [];
  const db = getDb();
  const ids = rows.map((r) => r.id);
  const devices = await db.select().from(schema.agentHostDevice).where(inArray(schema.agentHostDevice.hostId, ids));
  const queued = await db
    .select({ hostId: schema.hostWork.hostId, n: sql<number>`count(*)::int` })
    .from(schema.hostWork)
    .where(and(inArray(schema.hostWork.hostId, ids), eq(schema.hostWork.state, HostWorkState.QUEUED)))
    .groupBy(schema.hostWork.hostId);
  const [away] = await db.select().from(schema.agentHostAway).where(eq(schema.agentHostAway.userId, p.user.id));
  const userIds = [...new Set([...rows.map((r) => r.ownerUserId), ...devices.map((d) => d.userId)])];
  const users = await db.select({ id: schema.user.id, name: schema.user.name }).from(schema.user).where(inArray(schema.user.id, userIds));
  const names = new Map(users.map((u) => [u.id, u.name]));
  const groupIds = [...new Set(rows.map((r) => r.groupId).filter(Boolean))];
  const groups = groupIds.length ? await db.select({ id: schema.directoryGroup.id, name: schema.directoryGroup.name }).from(schema.directoryGroup).where(inArray(schema.directoryGroup.id, groupIds)) : [];
  const groupNames = new Map(groups.map((g) => [g.id, g.name]));
  const now = Date.now();
  const out: Init<typeof AgentHostSchema>[] = [];
  for (const h of rows) {
    const own = devices.filter((d) => d.hostId === h.id);
    const mine = await mayUse(p.user.id, h);
    out.push({
      id: h.id,
      name: h.name,
      scope: h.scope === "team" ? AgentHostScope.TEAM : AgentHostScope.USER,
      ownerUserId: h.ownerUserId,
      ownerName: names.get(h.ownerUserId) ?? "",
      groupId: h.groupId,
      groupName: groupNames.get(h.groupId) ?? "",
      online: isFresh(h.lastSeenAt, now),
      draining: h.draining,
      unavailableReason: mine ? await unavailableReason(h, p.user) : "",
      maxSessions: h.maxSessions,
      maxSessionsPerUser: h.maxSessionsPerUser,
      workingSessions: own.reduce((n, d) => n + (isFresh(d.reportedAt, now) ? d.workingSessions : 0), 0),
      queuedWork: queued.find((q) => q.hostId === h.id)?.n ?? 0,
      isolation: h.isolation === "uid" ? AgentHostIsolation.UID : h.isolation === "none" ? AgentHostIsolation.NONE : AgentHostIsolation.UNSPECIFIED,
      sandboxDriver: h.sandboxDriver,
      os: h.os,
      arch: h.arch,
      clientVersion: h.clientVersion,
      cpuCount: h.cpuCount,
      loadAverage: h.loadAverage,
      diskFreeBytes: BigInt(h.diskFreeBytes),
      allowedRepositories: Array.isArray(h.allowedRepositories) ? (h.allowedRepositories as string[]) : [],
      awayHost: away ? away.hostId === h.id : h.scope === "user" && h.ownerUserId === p.user.id,
      lastSeenAt: h.lastSeenAt?.toISOString() ?? "",
      createdAt: h.createdAt.toISOString(),
      workers: withWorkers
        ? own
            .filter((d) => d.role !== "supervisor")
            .map((d) => ({ userId: d.userId, userName: names.get(d.userId) ?? "", deviceId: d.deviceId, online: isFresh(d.reportedAt, now), workingSessions: isFresh(d.reportedAt, now) ? d.workingSessions : 0 }))
        : [],
    });
  }
  return out;
}

async function hostName(hostId: string): Promise<string> {
  return (await hostOf(hostId))?.name ?? "";
}

function workView(w: WorkRow, name: string, redact = false): Init<typeof HostWorkSchema> {
  return {
    id: w.id,
    hostId: w.hostId,
    hostName: name,
    userId: w.userId,
    kind: w.kind,
    state: w.state,
    repositoryKey: w.repositoryKey,
    baseRef: w.baseRef,
    branch: w.branch,
    landing: w.landing,
    freshClone: w.freshClone,
    prompt: redact ? "" : w.prompt,
    title: redact ? "" : w.title,
    model: w.model,
    effort: w.effort,
    permissionMode: w.permissionMode,
    job: !redact && w.job ? decodeJob(w.job) : undefined,
    forkSessionId: w.forkSessionId,
    sessionId: w.sessionId,
    pullRequestUrl: w.pullRequestUrl,
    error: w.error,
    attempts: w.attempts,
    createdAt: w.createdAt.toISOString(),
    updatedAt: w.updatedAt.toISOString(),
  };
}

// --- HostsService ----------------------------------------------------------------------------------------

export async function listAgentHosts(p: Principal, all: boolean): Promise<Init<typeof AgentHostSchema>[]> {
  const db = getDb();
  if (all) {
    need(p.user, "hosts.read");
    return hostViews(await db.select().from(schema.agentHost).orderBy(asc(schema.agentHost.name)), p, true);
  }
  const m = schema.directoryGroupMember;
  const groups = (await db.select({ id: m.groupId }).from(m).where(eq(m.userId, p.user.id))).map((g) => g.id);
  const rows = await db.select().from(schema.agentHost).orderBy(asc(schema.agentHost.name));
  return hostViews(rows.filter((h) => (h.scope === "team" ? groups.includes(h.groupId) : h.ownerUserId === p.user.id)), p, false);
}

function cleanLimit(n: number, what: string): number {
  if (!Number.isInteger(n) || n < 0 || n > MAX_MAX_SESSIONS) throw new ApiError("invalid_request", `${what} must be between 0 and ${MAX_MAX_SESSIONS}`);
  return n;
}

function cleanPatterns(list: string[]): string[] {
  const out = list.map((s) => s.trim().toLowerCase()).filter(Boolean);
  for (const r of out) {
    if (r.length > 300 || !/^[a-z0-9.:_-]+(\/[a-z0-9._*-]+)+$/.test(r)) throw new ApiError("invalid_request", `${r} is not a repository pattern like github.com/acme/*`);
  }
  return out;
}

/** Whether a repository key matches one of a host's patterns; a * stands for one path segment or, at the end, the rest. */
export function matchesRepository(patterns: string[], key: string): boolean {
  return patterns.some((pat) => {
    const re = pat
      .split("/")
      .map((seg, i, all) => (seg === "*" && i === all.length - 1 ? ".+" : seg.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*")))
      .join("/");
    return new RegExp(`^${re}$`).test(key);
  });
}

async function checkGroupExists(id: string): Promise<void> {
  const [g] = await getDb().select({ id: schema.directoryGroup.id }).from(schema.directoryGroup).where(eq(schema.directoryGroup.id, id));
  if (!g) throw new ApiError("not_found", "Group not found");
}

export async function createAgentHost(p: Principal, req: CreateAgentHostRequest): Promise<{ host: Init<typeof AgentHostSchema>; deviceToken: string }> {
  const name = req.name.trim();
  if (!name || name.length > 100) throw new ApiError("invalid_request", "A host's name has 1 to 100 characters");
  const team = req.scope === AgentHostScope.TEAM;
  if (team) {
    need(p.user, "hosts.write");
    if (!req.groupId) throw new ApiError("invalid_request", "A team host needs group_id");
    await checkGroupExists(req.groupId);
  } else if ((await serverPolicyFor(p.user)).personalHostsDisabled) {
    throw new ApiError("permission", "Your organization allows only hosts registered by an admin");
  }
  if (req.allowedRepositories.length) need(p.user, "hosts.write");
  const now = new Date();
  const id = newId("ahs");
  const device = await createHostDevice(p.user.id, name, team ? "host_supervisor" : "host", "harness-client daemon");
  const db = getDb();
  const [row] = await db
    .insert(schema.agentHost)
    .values({
      id,
      name,
      scope: team ? "team" : "user",
      ownerUserId: p.user.id,
      groupId: team ? req.groupId : "",
      maxSessions: cleanLimit(req.maxSessions, "max_sessions") || DEFAULT_MAX_SESSIONS,
      maxSessionsPerUser: cleanLimit(req.maxSessionsPerUser, "max_sessions_per_user"),
      allowedRepositories: cleanPatterns(req.allowedRepositories),
      createdAt: now,
    })
    .returning();
  await db.insert(schema.agentHostDevice).values({ deviceId: device.deviceId, hostId: id, userId: p.user.id, role: team ? "supervisor" : "personal" });
  return { host: (await hostViews([row!], p, false))[0]!, deviceToken: device.deviceToken };
}

/** Owner or `hosts.write`; NOT_FOUND otherwise. */
async function manageableHost(p: Principal, id: string): Promise<HostRow> {
  const host = await hostOf(id);
  if (!host || (host.ownerUserId !== p.user.id && !can(p.user, "hosts.write"))) throw new ApiError("not_found", `Host ${id} not found`);
  return host;
}

export async function updateAgentHost(p: Principal, req: UpdateAgentHostRequest): Promise<Init<typeof AgentHostSchema>> {
  const host = await manageableHost(p, req.id);
  const set: Partial<HostRow> = {};
  if (req.name !== undefined) {
    const name = req.name.trim();
    if (!name || name.length > 100) throw new ApiError("invalid_request", "A host's name has 1 to 100 characters");
    set.name = name;
  }
  if (req.draining !== undefined) set.draining = req.draining;
  const admin = can(p.user, "hosts.write");
  if (req.maxSessions !== undefined) {
    if (host.scope === "team") need(p.user, "hosts.write");
    set.maxSessions = cleanLimit(req.maxSessions, "max_sessions") || DEFAULT_MAX_SESSIONS;
  }
  if (req.maxSessionsPerUser !== undefined) {
    if (host.scope === "team") need(p.user, "hosts.write");
    set.maxSessionsPerUser = cleanLimit(req.maxSessionsPerUser, "max_sessions_per_user");
  }
  if (req.groupId !== undefined) {
    need(p.user, "hosts.write");
    if (host.scope !== "team") throw new ApiError("failed_precondition", "Only a team host has a group");
    await checkGroupExists(req.groupId);
    set.groupId = req.groupId;
  }
  if (req.setAllowedRepositories) {
    if (!admin) need(p.user, "hosts.write");
    set.allowedRepositories = cleanPatterns(req.allowedRepositories);
  }
  const db = getDb();
  const [row] = Object.keys(set).length ? await db.update(schema.agentHost).set(set).where(eq(schema.agentHost.id, host.id)).returning() : [host];
  wake(host.id);
  return (await hostViews([row!], p, admin))[0]!;
}

export async function deleteAgentHost(p: Principal, id: string): Promise<void> {
  const host = await manageableHost(p, id);
  const db = getDb();
  const devices = await db.select().from(schema.agentHostDevice).where(eq(schema.agentHostDevice.hostId, id));
  for (const d of devices) await revokeDevice(d.deviceId);
  await db.delete(schema.agentHost).where(eq(schema.agentHost.id, host.id));
  wake(id);
}

export async function startHostWork(p: Principal, req: StartHostWorkRequest): Promise<Init<typeof HostWorkSchema>> {
  const host = await usableHost(p, req.hostId);
  const reason = await unavailableReason(host, p.user);
  if (reason) throw new ApiError("failed_precondition", reason);
  if (req.permissionMode === PermissionMode.BYPASS_PERMISSIONS) throw new ApiError("permission", "bypassPermissions cannot be enabled remotely");
  if (!req.prompt.trim()) throw new ApiError("invalid_request", "prompt is required");
  const repo = parseRepoKey(req.repositoryKey);
  const db = getDb();
  if (req.idempotencyKey) {
    const [same] = await db.select().from(schema.hostWork).where(and(eq(schema.hostWork.userId, p.user.id), eq(schema.hostWork.idempotencyKey, req.idempotencyKey)));
    if (same) return workView(same, host.name);
  }
  const [{ n } = { n: 0 }] = await db
    .select({ n: sql<number>`count(*)::int` })
    .from(schema.hostWork)
    .where(and(eq(schema.hostWork.userId, p.user.id), eq(schema.hostWork.state, HostWorkState.QUEUED)));
  if (n >= MAX_QUEUED_PER_USER) throw new ApiError("failed_precondition", `You have ${n} sessions waiting for a host already`);
  const row = await insertWork({
    hostId: host.id,
    userId: p.user.id,
    kind: HostWorkKind.NEW_SESSION,
    repositoryKey: repo.key,
    baseRef: req.baseRef.trim(),
    landing: req.landing,
    freshClone: req.freshClone,
    prompt: req.prompt,
    title: req.title.trim().slice(0, 200),
    model: req.model,
    effort: req.effort,
    permissionMode: req.permissionMode,
    idempotencyKey: req.idempotencyKey || null,
  });
  return workView(row, host.name);
}

type WorkInsert = Omit<typeof schema.hostWork.$inferInsert, "id" | "state" | "createdAt" | "updatedAt" | "expiresAt">;

async function insertWork(v: WorkInsert): Promise<WorkRow> {
  const now = new Date();
  const [row] = await getDb()
    .insert(schema.hostWork)
    .values({ ...v, id: newId("hwk"), state: HostWorkState.QUEUED, createdAt: now, updatedAt: now, expiresAt: new Date(now.getTime() + WORK_TTL_MS) })
    .returning();
  wake(v.hostId);
  return row!;
}

/** The caller's work, or work on a host they administer (without what was asked). */
export async function getHostWork(p: Principal, id: string): Promise<Init<typeof HostWorkSchema>> {
  const [row] = await getDb().select().from(schema.hostWork).where(eq(schema.hostWork.id, id));
  if (!row) throw new ApiError("not_found", `Work ${id} not found`);
  if (row.userId === p.user.id) return workView(row, await hostName(row.hostId));
  if (can(p.user, "hosts.read")) return workView(row, await hostName(row.hostId), true);
  throw new ApiError("not_found", `Work ${id} not found`);
}

export async function listHostWork(p: Principal, hostId: string, limit: number): Promise<Init<typeof HostWorkSchema>[]> {
  const n = Math.min(Math.max(limit || 50, 1), 200);
  const w = schema.hostWork;
  const db = getDb();
  if (hostId && can(p.user, "hosts.read")) {
    const rows = await db.select().from(w).where(eq(w.hostId, hostId)).orderBy(desc(w.createdAt)).limit(n);
    const name = await hostName(hostId);
    return rows.map((r) => workView(r, name, r.userId !== p.user.id));
  }
  const rows = await db
    .select()
    .from(w)
    .where(hostId ? and(eq(w.userId, p.user.id), eq(w.hostId, hostId)) : eq(w.userId, p.user.id))
    .orderBy(desc(w.createdAt))
    .limit(n);
  const names = new Map<string, string>();
  for (const id of new Set(rows.map((r) => r.hostId))) names.set(id, await hostName(id));
  return rows.map((r) => workView(r, names.get(r.hostId) ?? ""));
}

const TERMINAL: readonly number[] = [HostWorkState.DONE, HostWorkState.FAILED, HostWorkState.CANCELLED, HostWorkState.EXPIRED];

export async function cancelHostWork(p: Principal, id: string): Promise<Init<typeof HostWorkSchema>> {
  const db = getDb();
  const [row] = await db.select().from(schema.hostWork).where(eq(schema.hostWork.id, id));
  const host = row && (await hostOf(row.hostId));
  if (!row || !host || (row.userId !== p.user.id && host.ownerUserId !== p.user.id && !can(p.user, "hosts.write"))) throw new ApiError("not_found", `Work ${id} not found`);
  if (TERMINAL.includes(row.state)) return workView(row, host.name, row.userId !== p.user.id);
  const [next] = await db.update(schema.hostWork).set({ state: HostWorkState.CANCELLED, updatedAt: new Date() }).where(eq(schema.hostWork.id, id)).returning();
  wake(host.id);
  return workView(next!, host.name, row.userId !== p.user.id);
}

export async function setAwayHost(p: Principal, hostId: string): Promise<void> {
  if (hostId) await usableHost(p, hostId);
  const now = new Date();
  await getDb()
    .insert(schema.agentHostAway)
    .values({ userId: p.user.id, hostId, updatedAt: now })
    .onConflictDoUpdate({ target: schema.agentHostAway.userId, set: { hostId, updatedAt: now } });
}

export async function claimScheduleRun(p: Principal, scheduleId: string, dueAt: string): Promise<boolean> {
  if (!scheduleId || !dueAt) throw new ApiError("invalid_request", "schedule_id and due_at are required");
  const db = getDb();
  const c = schema.scheduleRunClaim;
  const deviceId = p.deviceId ?? "";
  const now = new Date();
  const won = await db.insert(c).values({ userId: p.user.id, scheduleId, dueAt, deviceId, createdAt: now }).onConflictDoNothing().returning({ id: c.scheduleId });
  if (won.length) {
    await db.delete(c).where(lt(c.createdAt, new Date(now.getTime() - 30 * 86_400_000)));
    return true;
  }
  const [row] = await db.select({ deviceId: c.deviceId }).from(c).where(and(eq(c.userId, p.user.id), eq(c.scheduleId, scheduleId), eq(c.dueAt, dueAt)));
  return row?.deviceId === deviceId;
}

// --- HostAgentService ------------------------------------------------------------------------------------

function isolationName(i: AgentHostIsolation): string {
  return i === AgentHostIsolation.UID ? "uid" : "none";
}

/** Decision 9: only a token made for a host turns into one. */
async function adoptDevice(p: Principal, req: RegisterAgentHostRequest): Promise<{ device: DeviceRow; host: HostRow }> {
  const db = getDb();
  const [dev] = await db.select().from(schema.device).where(eq(schema.device.id, p.deviceId!));
  if (!dev || dev.clientName !== "harness-client daemon") {
    throw new ApiError("failed_precondition", "This token was not made for a host: run `harness-client daemon login`, or add the host in the app, and use the token it gives");
  }
  if ((await serverPolicyFor(p.user)).personalHostsDisabled) throw new ApiError("permission", "Your organization allows only hosts registered by an admin");
  const id = newId("ahs");
  const [host] = await db
    .insert(schema.agentHost)
    .values({ id, name: req.name.trim().slice(0, 100) || dev.name, scope: "user", ownerUserId: p.user.id, maxSessions: req.maxSessions > 0 ? Math.min(req.maxSessions, MAX_MAX_SESSIONS) : DEFAULT_MAX_SESSIONS, createdAt: new Date() })
    .returning();
  const [device] = await db.insert(schema.agentHostDevice).values({ deviceId: dev.id, hostId: id, userId: p.user.id, role: "personal" }).returning();
  await db.update(schema.device).set({ kind: "host" }).where(eq(schema.device.id, dev.id));
  return { device: device!, host: host! };
}

export async function registerAgentHost(p: Principal, req: RegisterAgentHostRequest): Promise<{ host: Init<typeof AgentHostSchema>; supervisor: boolean }> {
  if (!p.deviceId) throw new ApiError("permission", "Hosts must authenticate with a device token");
  const db = getDb();
  const [known] = await db.select().from(schema.agentHostDevice).where(eq(schema.agentHostDevice.deviceId, p.deviceId));
  const { device, host } = known ? await callingDevice(p) : await adoptDevice(p, req);
  const now = new Date();
  let row = host;
  // A worker speaks for itself, not for the machine: its supervisor says what the machine is.
  if (device.role !== "worker") {
    const set: Partial<HostRow> = {
      isolation: isolationName(req.isolation),
      sandboxDriver: req.sandboxDriver.slice(0, 50),
      os: req.os.slice(0, 50),
      arch: req.arch.slice(0, 50),
      clientVersion: req.clientVersion.slice(0, 100),
      cpuCount: Math.max(0, req.cpuCount),
      lastSeenAt: now,
    };
    if (req.maxSessions > 0) set.maxSessions = Math.min(req.maxSessions, MAX_MAX_SESSIONS);
    [row] = (await db.update(schema.agentHost).set(set).where(eq(schema.agentHost.id, host.id)).returning()) as [HostRow];
  }
  await db.update(schema.agentHostDevice).set({ reportedAt: now, workingSessions: 0 }).where(eq(schema.agentHostDevice.deviceId, device.deviceId));
  wake(host.id);
  return { host: (await hostViews([row], p, false))[0]!, supervisor: device.role === "supervisor" };
}

export async function reportAgentHost(p: Principal, req: ReportAgentHostRequest): Promise<{ draining: boolean; maxSessions: number }> {
  const { device, host } = await callingDevice(p);
  const now = new Date();
  const db = getDb();
  await db.update(schema.agentHostDevice).set({ workingSessions: Math.max(0, req.workingSessions), reportedAt: now }).where(eq(schema.agentHostDevice.deviceId, device.deviceId));
  const set: Partial<HostRow> = { lastSeenAt: now };
  if (device.role !== "worker") {
    set.loadAverage = req.loadAverage;
    set.diskFreeBytes = Number(req.diskFreeBytes);
  }
  await db.update(schema.agentHost).set(set).where(eq(schema.agentHost.id, host.id));
  await handOverAwayJobs(host).catch((err) => console.error("[harness] handing over away jobs failed", err));
  wake(host.id);
  return { draining: host.draining, maxSessions: host.maxSessions };
}

/** Puts back work whose daemon went quiet, and ends what waited too long. */
async function sweepWork(hostId: string): Promise<void> {
  const db = getDb();
  const w = schema.hostWork;
  const now = new Date();
  await db.update(w).set({ state: HostWorkState.EXPIRED, updatedAt: now }).where(and(eq(w.hostId, hostId), eq(w.state, HostWorkState.QUEUED), lt(w.expiresAt, now)));
  const lost = await db.select().from(w).where(and(eq(w.hostId, hostId), eq(w.state, HostWorkState.CLAIMED), lt(w.leaseUntil, now)));
  for (const r of lost) {
    const failed = r.attempts >= MAX_ATTEMPTS;
    await db
      .update(w)
      .set(failed ? { state: HostWorkState.FAILED, error: "The host lost this work several times", updatedAt: now } : { state: HostWorkState.QUEUED, deviceId: "", leaseUntil: null, updatedAt: now })
      .where(and(eq(w.id, r.id), eq(w.state, HostWorkState.CLAIMED)));
  }
}

async function tryClaim(device: DeviceRow, host: HostRow): Promise<WorkRow | undefined> {
  return withHostLock(host.id, async () => {
    await sweepWork(host.id);
    const db = getDb();
    const w = schema.hostWork;
    const [fresh] = await db.select().from(schema.agentHost).where(eq(schema.agentHost.id, host.id));
    if (!fresh || fresh.draining) return undefined;
    const devices = await db.select().from(schema.agentHostDevice).where(eq(schema.agentHostDevice.hostId, host.id));
    const claimed = await db.select({ userId: w.userId }).from(w).where(and(eq(w.hostId, host.id), eq(w.state, HostWorkState.CLAIMED)));
    const now = Date.now();
    const total = devices.reduce((n, d) => n + (isFresh(d.reportedAt, now) ? d.workingSessions : 0), 0) + claimed.length;
    if (total >= fresh.maxSessions) return undefined;
    if (fresh.maxSessionsPerUser > 0) {
      const mine = devices.filter((d) => d.userId === device.userId).reduce((n, d) => n + (isFresh(d.reportedAt, now) ? d.workingSessions : 0), 0) + claimed.filter((c) => c.userId === device.userId).length;
      if (mine >= fresh.maxSessionsPerUser) return undefined;
    }
    const [next] = await db.select().from(w).where(and(eq(w.hostId, host.id), eq(w.userId, device.userId), eq(w.state, HostWorkState.QUEUED))).orderBy(asc(w.createdAt)).limit(1);
    if (!next) return undefined;
    const [row] = await db
      .update(w)
      .set({ state: HostWorkState.CLAIMED, deviceId: device.deviceId, attempts: next.attempts + 1, leaseUntil: new Date(now + leaseMs()), updatedAt: new Date(now) })
      .where(and(eq(w.id, next.id), eq(w.state, HostWorkState.QUEUED)))
      .returning();
    return row;
  });
}

export async function claimHostWork(p: Principal, waitSeconds: number, signal?: AbortSignal): Promise<{ work?: Init<typeof HostWorkSchema>; gitName: string; gitEmail: string }> {
  const { device, host } = await callingDevice(p);
  if (device.role === "supervisor") throw new ApiError("failed_precondition", "A supervisor runs workers; its workers claim the work");
  const deadline = Date.now() + Math.min(Math.max(waitSeconds, 0), MAX_POLL_S) * 1000;
  for (;;) {
    const row = await tryClaim(device, host);
    if (row) return { work: workView(row, host.name), gitName: p.user.name, gitEmail: p.user.email };
    const left = deadline - Date.now();
    if (left <= 0 || signal?.aborted) return { gitName: "", gitEmail: "" };
    await waitForChange(host.id, left, signal);
  }
}

export async function updateHostWork(p: Principal, req: UpdateHostWorkRequest): Promise<{ cancelled: boolean }> {
  const { device } = await callingDevice(p);
  const db = getDb();
  const [row] = await db.select().from(schema.hostWork).where(eq(schema.hostWork.id, req.workId));
  if (!row || row.deviceId !== device.deviceId) throw new ApiError("not_found", `Work ${req.workId} not found`);
  if (row.state === HostWorkState.CANCELLED) return { cancelled: true };
  const now = new Date();
  const set: Partial<WorkRow> = { updatedAt: now, leaseUntil: new Date(now.getTime() + leaseMs()) };
  if (req.state === HostWorkState.CLAIMED || req.state === HostWorkState.RUNNING || req.state === HostWorkState.DONE || req.state === HostWorkState.FAILED) {
    if (!TERMINAL.includes(row.state)) set.state = req.state;
  } else if (req.state !== HostWorkState.UNSPECIFIED) {
    throw new ApiError("invalid_request", "A host can only report CLAIMED, RUNNING, DONE or FAILED");
  }
  if (req.sessionId) set.sessionId = req.sessionId;
  if (req.branch) set.branch = req.branch;
  if (req.pullRequestUrl) set.pullRequestUrl = req.pullRequestUrl;
  if (req.error) set.error = req.error.slice(0, 2000);
  await db.update(schema.hostWork).set(set).where(eq(schema.hostWork.id, row.id));
  if (set.state === HostWorkState.RUNNING && row.state !== HostWorkState.RUNNING) {
    // Counted from now, not from the daemon's next report.
    await db.update(schema.agentHostDevice).set({ workingSessions: sql`${schema.agentHostDevice.workingSessions} + 1` }).where(eq(schema.agentHostDevice.deviceId, device.deviceId));
  }
  const sessionId = req.sessionId || row.sessionId;
  if (row.forkSessionId && sessionId) await moveSessionLink(row.userId, row.forkSessionId, sessionId).catch(() => {});
  wake(row.hostId);
  return { cancelled: false };
}

/** A device of a host that runs sessions: the only kind that gets git credentials. */
async function workingDevice(p: Principal): Promise<{ device: DeviceRow; host: HostRow; allowed: string[] }> {
  const { device, host } = await callingDevice(p);
  if (device.role === "supervisor") throw new ApiError("permission", "A supervisor holds no repository credentials");
  return { device, host, allowed: Array.isArray(host.allowedRepositories) ? (host.allowedRepositories as string[]) : [] };
}

const GIT_TOKEN_TTL_MS = 30 * 60_000;

export async function issueGitCredentials(p: Principal, repositoryKey: string): Promise<{ cloneUrl: string; username: string; password: string; expiresAt: string; asUser: boolean }> {
  const { allowed } = await workingDevice(p);
  const key = parseRepoKey(repositoryKey).key;
  const id = await hostIdentity(p.user.id, key, matchesRepository(allowed, key));
  const base = id.conn.baseUrl.replace(/\/+$/, "");
  const github = id.conn.type === 1;
  return {
    cloneUrl: `${base}/${id.repo.path}.git`,
    username: github ? "x-access-token" : "oauth2",
    password: id.token,
    expiresAt: new Date(Date.now() + GIT_TOKEN_TTL_MS).toISOString(),
    asUser: id.asUser,
  };
}

export async function openHostPullRequest(p: Principal, req: { repositoryKey: string; headBranch: string; baseBranch: string; title: string; body: string; draft: boolean; sessionId: string }): Promise<{ url: string; number: bigint }> {
  const { allowed } = await workingDevice(p);
  if (!req.headBranch || !req.baseBranch || !req.title) throw new ApiError("invalid_request", "head_branch, base_branch and title are required");
  const key = parseRepoKey(req.repositoryKey).key;
  const pr = await openPullRequestAs(p.user.id, key, matchesRepository(allowed, key), req);
  return { url: pr.url, number: BigInt(pr.number) };
}

/** Which members should have a worker: those with work, and those who had a worker before (their schedules, reopened sessions). */
async function wantedWorkers(host: HostRow): Promise<{ workers: { userId: string; deviceId: string }[]; stopUserIds: string[] }> {
  const db = getDb();
  const members = (await db.select({ id: schema.directoryGroupMember.userId }).from(schema.directoryGroupMember).where(eq(schema.directoryGroupMember.groupId, host.groupId))).map((m) => m.id);
  const devices = (await db.select().from(schema.agentHostDevice).where(and(eq(schema.agentHostDevice.hostId, host.id), eq(schema.agentHostDevice.role, "worker"))));
  const w = schema.hostWork;
  const working = await db
    .selectDistinct({ userId: w.userId })
    .from(w)
    .where(and(eq(w.hostId, host.id), inArray(w.state, [HostWorkState.QUEUED, HostWorkState.CLAIMED, HostWorkState.RUNNING])));
  const stopUserIds: string[] = [];
  const workers: { userId: string; deviceId: string }[] = [];
  const canWork = host.isolation === "uid";
  for (const d of devices) {
    if (!members.includes(d.userId)) {
      // Access was removed: the token goes now, whatever becomes of the process.
      stopUserIds.push(d.userId);
      await revokeDevice(d.deviceId);
      await db.delete(schema.agentHostDevice).where(eq(schema.agentHostDevice.deviceId, d.deviceId));
    } else if (canWork) {
      workers.push({ userId: d.userId, deviceId: d.deviceId });
    }
  }
  if (canWork) {
    for (const { userId } of working) {
      if (members.includes(userId) && !workers.some((x) => x.userId === userId)) workers.push({ userId, deviceId: "" });
    }
  }
  return { workers, stopUserIds };
}

export async function listWantedWorkers(p: Principal, waitSeconds: number, signal?: AbortSignal): Promise<{ workers: { userId: string; deviceId: string }[]; stopUserIds: string[] }> {
  const { device, host } = await callingDevice(p);
  if (device.role !== "supervisor") throw new ApiError("permission", "Only a team host's supervisor runs workers");
  const before = await wantedWorkers(host);
  const seen = JSON.stringify(before.workers.map((w) => w.userId).sort());
  const deadline = Date.now() + Math.min(Math.max(waitSeconds, 0), MAX_POLL_S) * 1000;
  // Return early only when something changed; a supervisor's next call starts from the answer.
  for (;;) {
    const left = deadline - Date.now();
    if (left <= 0 || signal?.aborted || before.stopUserIds.length) return before;
    await waitForChange(host.id, left, signal);
    const [fresh] = await getDb().select().from(schema.agentHost).where(eq(schema.agentHost.id, host.id));
    if (!fresh) return { workers: [], stopUserIds: before.workers.map((w) => w.userId) };
    const now = await wantedWorkers(fresh);
    if (now.stopUserIds.length || JSON.stringify(now.workers.map((w) => w.userId).sort()) !== seen) return now;
  }
}

export async function issueWorkerToken(p: Principal, userId: string): Promise<{ deviceId: string; deviceToken: string }> {
  const { device, host } = await callingDevice(p);
  if (device.role !== "supervisor") throw new ApiError("permission", "Only a team host's supervisor runs workers");
  // Decision 7: a host that can't separate users gets no tokens for them.
  if (host.isolation !== "uid") throw new ApiError("failed_precondition", "This host can't separate its users, so it runs no workers");
  if (!(await inGroup(userId, host.groupId))) throw new ApiError("not_found", "That user is not a member of this host's team");
  const db = getDb();
  const [known] = await db
    .select()
    .from(schema.agentHostDevice)
    .where(and(eq(schema.agentHostDevice.hostId, host.id), eq(schema.agentHostDevice.userId, userId), eq(schema.agentHostDevice.role, "worker")));
  if (known) return { deviceId: known.deviceId, deviceToken: await rotateDeviceToken(known.deviceId) };
  const made = await createHostDevice(userId, host.name, "host_worker", "harness-client daemon worker");
  await db.insert(schema.agentHostDevice).values({ deviceId: made.deviceId, hostId: host.id, userId, role: "worker" });
  return made;
}

// --- away work -------------------------------------------------------------------------------------------

/** The host that takes a user's work while their machines are away: their choice, else their only personal host. */
async function awayHostOf(userId: string): Promise<HostRow | undefined> {
  const db = getDb();
  const [choice] = await db.select().from(schema.agentHostAway).where(eq(schema.agentHostAway.userId, userId));
  if (choice) {
    if (!choice.hostId) return undefined;
    const host = await hostOf(choice.hostId);
    return host && (await mayUse(userId, host)) ? host : undefined;
  }
  const own = await db.select().from(schema.agentHost).where(and(eq(schema.agentHost.ownerUserId, userId), eq(schema.agentHost.scope, "user")));
  return own.length === 1 ? own[0] : undefined;
}

const LIVE_WORK: number[] = [HostWorkState.QUEUED, HostWorkState.CLAIMED, HostWorkState.RUNNING];

/** Decisions 16 and 18: pending jobs of this host's people whose session's machine has been away. */
async function handOverAwayJobs(host: HostRow): Promise<void> {
  const db = getDb();
  const j = schema.vcsAutofixJob;
  let users: string[];
  if (host.scope === "user") users = [host.ownerUserId];
  else users = (await db.select({ id: schema.agentHostAway.userId }).from(schema.agentHostAway).where(eq(schema.agentHostAway.hostId, host.id))).map((r) => r.id);
  if (!users.length) return;
  const cutoff = new Date(Date.now() - awayGraceMs());
  const jobs = await db.select().from(j).where(and(inArray(j.userId, users), eq(j.status, "pending"), lt(j.updatedAt, cutoff))).orderBy(asc(j.createdAt)).limit(20);
  for (const job of jobs) {
    if (await sessionMachineOnline(job.sessionId)) continue;
    const target = await awayHostOf(job.userId);
    if (target?.id !== host.id || (await unavailableReason(host, { role: "user" }))) continue;
    const proto = jobToProto(job);
    if (!proto.headBranch) continue; // nothing to check out yet
    const sessionOnHost = await liveWorkOn(job.userId, job.repositoryKey, proto.headBranch);
    if (sessionOnHost?.sessionId) {
      // A session on this branch already runs on a host: the job goes there.
      const payload = { case: "deliverAutoFixJob" as const, value: create(DeliverAutoFixJobRequestSchema, { sessionId: sessionOnHost.sessionId, job: proto }) };
      if ((await queueServerCommand(job.userId, sessionOnHost.sessionId, payload, `afx:${job.id}:host`, AUTOFIX_JOB_TTL_S)) !== "queued") continue;
    } else if (!sessionOnHost) {
      const s = await db.query.syncSession.findFirst({ where: eq(schema.syncSession.id, job.sessionId) });
      await insertWork({
        hostId: host.id,
        userId: job.userId,
        kind: HostWorkKind.AUTO_FIX,
        repositoryKey: job.repositoryKey,
        branch: proto.headBranch,
        baseRef: proto.baseBranch,
        landing: HostWorkLanding.BRANCH,
        title: `Follow up on pull request #${job.prNumber}`,
        job: encodeJob(proto),
        forkSessionId: s?.id ?? "",
        idempotencyKey: `afx:${job.id}`,
      });
    } else {
      continue; // a session for this branch is on its way; the job waits for it
    }
    await db.update(j).set({ status: "handed_off", updatedAt: new Date() }).where(eq(j.id, job.id));
    await withdrawServerCommand(job.userId, `afx:${job.id}`);
  }
}

async function liveWorkOn(userId: string, repositoryKey: string, branch: string): Promise<WorkRow | undefined> {
  const w = schema.hostWork;
  const [row] = await getDb()
    .select()
    .from(w)
    .where(and(eq(w.userId, userId), eq(w.repositoryKey, repositoryKey), eq(w.branch, branch), inArray(w.state, LIVE_WORK)))
    .orderBy(desc(w.createdAt))
    .limit(1);
  return row;
}

/** Decision 17: an @mention nobody's session is on, from someone this server knows. */
async function takeMention(row: EventRow): Promise<void> {
  const detail = (row.detail ?? {}) as Record<string, unknown>;
  const author = String(detail.author ?? "").toLowerCase();
  if (!author || !row.connectionId) return;
  const db = getDb();
  const t = schema.vcsUserToken;
  const known = await db
    .select({ userId: t.userId })
    .from(t)
    .where(and(eq(t.connectionId, row.connectionId), eq(t.state, "connected"), sql`lower(${t.login}) = ${author}`));
  if (known.length !== 1) return;
  const userId = known[0]!.userId;
  const host = await awayHostOf(userId);
  if (!host) return;
  const stored = await getStoredPr(row.repositoryKey, row.prNumber);
  const pr = stored ? prOf(stored) : undefined;
  const headBranch = pr?.headBranch || row.headBranch;
  if (!headBranch) return;
  const ids = Array.isArray(detail.comment_ids) ? (detail.comment_ids as string[]) : [];
  const job = create(AutoFixJobSchema, {
    id: newId("afx"),
    kind: AutoFixJob_Kind.MENTION,
    repositoryKey: row.repositoryKey,
    prNumber: BigInt(row.prNumber),
    prUrl: pr?.url ?? "",
    headBranch,
    baseBranch: pr?.baseBranch ?? "",
    headSha: pr?.headSha ?? "",
    comments: [{ author: String(detail.author ?? ""), body: String(detail.body ?? ""), path: String(detail.path ?? ""), line: Number(detail.line ?? 0), url: String(detail.url ?? "") }],
  });
  const live = await liveWorkOn(userId, row.repositoryKey, headBranch);
  if (live?.sessionId) {
    const payload = { case: "deliverAutoFixJob" as const, value: create(DeliverAutoFixJobRequestSchema, { sessionId: live.sessionId, job }) };
    await queueServerCommand(userId, live.sessionId, payload, `mention:${ids[0] ?? job.id}`, AUTOFIX_JOB_TTL_S);
    return;
  }
  await insertWork({
    hostId: host.id,
    userId,
    kind: HostWorkKind.MENTION,
    repositoryKey: row.repositoryKey,
    branch: headBranch,
    baseRef: pr?.baseBranch ?? "",
    landing: HostWorkLanding.BRANCH,
    title: `Mentioned on pull request #${row.prNumber}`,
    job: encodeJob(job),
    idempotencyKey: `mention:${ids[0] ?? job.id}`,
  }).catch((err: unknown) => {
    // The same comment again (a redelivered webhook) hits the unique key.
    if (!String(err).includes("host_work_idem_idx")) throw err;
  });
}

onUnclaimedMention(takeMention);

/** Test hook. */
export function resetHostState(): void {
  waiters.clear();
  hostLocks.clear();
}
