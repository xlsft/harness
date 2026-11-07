/**
 * Cloud sessions (docs/design/cloud-sessions.md): sessions in one-off containers on runner nodes, which are agent hosts of
 * scope "cloud". The server keeps a cloud_session row per session and decides; a runner reports its containers in
 * PollCloudRunner and is told what to start and what to remove (decision 4). The container's daemon is a device of its own
 * (kind host_cloud) that takes only its session's work (decision 3).
 */
import { type MessageInitShape } from "@bufbuild/protobuf";
import { createHash } from "node:crypto";
import { and, desc, eq, gt, inArray, sql } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import { CloudEnvironmentState, PermissionMode } from "../gen/harness/agent/v1/agent_pb.ts";
import {
  HostWorkKind,
  HostWorkLanding,
  HostWorkState,
  type CloudContainerRemovalSchema,
  type CloudContainerSpecSchema,
  type CloudEgressSchema,
  type CloudRunnerConfig,
  type CloudRunnerConfigSchema,
  type CloudRunnerStatsSchema,
  type CloudSessionSchema,
  type PollCloudRunnerRequest,
  type StartHostWorkRequest,
  type UpdateCloudSessionRequest,
} from "../gen/harness/relay/v1/hosts_pb.ts";
import { can, need } from "../auth/roles.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { createHostDevice, rotateDeviceToken, type Principal, type PublicUser } from "../services/devices.ts";
import { serverPolicyFor } from "../services/managed-policy.ts";
import { parseRepoKey } from "../vcs/util.ts";
import { setCloudWake } from "./service.ts";
import { hostOf, inGroup, insertWork, isFresh, waitForChange, wake, type DeviceRow, type HostRow, type WorkRow } from "./hosts.ts";

type Init<T extends Parameters<typeof import("@bufbuild/protobuf").create>[0]> = MessageInitShape<T>;
export type CloudRow = typeof schema.cloudSession.$inferSelect;

const S = CloudEnvironmentState;
/** States with a container, or one on its way. */
export const LIVE_CLOUD: number[] = [S.STARTING, S.RUNNING, S.IDLE];
const MAX_POLL_S = 25;
const MAX_START_ATTEMPTS = 3;
const GiB = 1024 ** 3;

/** A container that has not registered by then is started again (decision 5). */
const startTimeoutMs = () => envNumber("HARNESS_CLOUD_START_TIMEOUT_MS", 10 * 60_000);

function envNumber(name: string, def: number): number {
  const n = Number(process.env[name] ?? "");
  return Number.isFinite(n) && n > 0 ? n : def;
}

// --- runner configuration ------------------------------------------------------------------------------

export interface RunnerConfig {
  image: string;
  allowedImages: string[];
  runtime: string;
  cpuMillis: number;
  memoryBytes: number;
  diskBytes: number;
  pids: number;
  idleTimeoutSeconds: number;
  maxLifetimeSeconds: number;
  cacheMaxBytes: number;
  cacheScope: "user" | "runner";
  storageOptSize: boolean;
}

export const DEFAULT_IMAGE = "harness-agent";

/** What a runner's stored configuration means, with the defaults filled in. */
export function configOf(host: Pick<HostRow, "cloudConfig">): RunnerConfig {
  const c = (host.cloudConfig && typeof host.cloudConfig === "object" ? host.cloudConfig : {}) as Partial<RunnerConfig>;
  const pos = (v: unknown, def: number) => (typeof v === "number" && v > 0 ? v : def);
  return {
    image: typeof c.image === "string" && c.image ? c.image : DEFAULT_IMAGE,
    allowedImages: Array.isArray(c.allowedImages) ? c.allowedImages.filter((s): s is string => typeof s === "string") : [],
    runtime: typeof c.runtime === "string" ? c.runtime : "",
    cpuMillis: pos(c.cpuMillis, 2000),
    memoryBytes: pos(c.memoryBytes, 4 * GiB),
    diskBytes: pos(c.diskBytes, 20 * GiB),
    pids: pos(c.pids, 1024),
    idleTimeoutSeconds: pos(c.idleTimeoutSeconds, 3600),
    maxLifetimeSeconds: pos(c.maxLifetimeSeconds, 86_400),
    cacheMaxBytes: pos(c.cacheMaxBytes, 10 * GiB),
    cacheScope: c.cacheScope === "runner" ? "runner" : "user",
    storageOptSize: c.storageOptSize === true,
  };
}

export function configToProto(c: RunnerConfig): Init<typeof CloudRunnerConfigSchema> {
  return {
    image: c.image,
    allowedImages: c.allowedImages,
    runtime: c.runtime,
    cpuMillis: c.cpuMillis,
    memoryBytes: BigInt(c.memoryBytes),
    diskBytes: BigInt(c.diskBytes),
    pids: c.pids,
    idleTimeoutSeconds: c.idleTimeoutSeconds,
    maxLifetimeSeconds: c.maxLifetimeSeconds,
    cacheMaxBytes: BigInt(c.cacheMaxBytes),
    cacheScope: c.cacheScope,
    storageOptSize: c.storageOptSize,
  };
}

const IMAGE = /^[A-Za-z0-9][A-Za-z0-9._\/:@-]{0,254}$/;

function checkRange(n: number, min: number, max: number, what: string): number {
  if (!Number.isFinite(n) || n < min || n > max) throw new ApiError("invalid_request", `${what} must be between ${min} and ${max}`);
  return n;
}

/** The JSON stored for a runner; zero values are left out and mean the defaults. */
export function cleanConfig(c: CloudRunnerConfig | undefined): Partial<RunnerConfig> {
  if (!c) return {};
  const out: Partial<RunnerConfig> = {};
  for (const img of [c.image, ...c.allowedImages]) {
    if (img && !IMAGE.test(img)) throw new ApiError("invalid_request", `${img} is not an image reference`);
  }
  if (c.image) out.image = c.image;
  const allowed = [...new Set(c.allowedImages.map((s) => s.trim()).filter(Boolean))];
  if (allowed.length) out.allowedImages = allowed;
  if (c.runtime) {
    if (!/^[a-z0-9._-]{1,40}$/.test(c.runtime)) throw new ApiError("invalid_request", "runtime is a name like runsc");
    out.runtime = c.runtime;
  }
  if (c.cpuMillis) out.cpuMillis = checkRange(c.cpuMillis, 100, 256_000, "cpu_millis");
  if (c.memoryBytes) out.memoryBytes = checkRange(Number(c.memoryBytes), 128 * 1024 ** 2, 1024 * GiB, "memory_bytes");
  if (c.diskBytes) out.diskBytes = checkRange(Number(c.diskBytes), GiB, 10 * 1024 * GiB, "disk_bytes");
  if (c.pids) out.pids = checkRange(c.pids, 64, 1_000_000, "pids");
  if (c.idleTimeoutSeconds) out.idleTimeoutSeconds = checkRange(c.idleTimeoutSeconds, 60, 30 * 86_400, "idle_timeout_seconds");
  if (c.maxLifetimeSeconds) out.maxLifetimeSeconds = checkRange(c.maxLifetimeSeconds, 600, 90 * 86_400, "max_lifetime_seconds");
  if (c.cacheMaxBytes) out.cacheMaxBytes = checkRange(Number(c.cacheMaxBytes), 128 * 1024 ** 2, 100 * 1024 * GiB, "cache_max_bytes");
  if (c.cacheScope) {
    if (c.cacheScope !== "user" && c.cacheScope !== "runner") throw new ApiError("invalid_request", 'cache_scope is "user" or "runner"');
    out.cacheScope = c.cacheScope;
  }
  if (c.storageOptSize) out.storageOptSize = true;
  return out;
}

// --- egress ----------------------------------------------------------------------------------------------

/** Package registries: the sandbox's default hosts, which a cloud session gets unless the policy says otherwise. */
export const DEFAULT_EGRESS = [
  "registry.npmjs.org",
  "registry.yarnpkg.com",
  "pypi.org",
  "files.pythonhosted.org",
  "proxy.golang.org",
  "sum.golang.org",
  "index.crates.io",
  "static.crates.io",
  "crates.io",
  "github.com",
  "api.github.com",
  "codeload.github.com",
  "objects.githubusercontent.com",
  "raw.githubusercontent.com",
];

/** Hosts of the server's code host connections, so that the daemon's git and the agent's reads of public code get there. */
async function codeHostEgress(): Promise<string[]> {
  const rows = await getDb().select({ baseUrl: schema.vcsConnection.baseUrl }).from(schema.vcsConnection);
  const out: string[] = [];
  for (const r of rows) {
    try {
      const u = new URL(r.baseUrl);
      const port = u.port ? `:${u.port}` : "";
      out.push(u.hostname.toLowerCase() + port);
      if (u.hostname === "github.com") out.push("api.github.com");
    } catch {
      // not a URL: nothing to allow
    }
  }
  return out;
}

async function egressFor(userId: string): Promise<string[]> {
  const policy = await serverPolicyFor(await roleOf(userId));
  const base = policy.cloud.egressAllow ?? [...DEFAULT_EGRESS, ...(await codeHostEgress())];
  return [...new Set(base)];
}

async function roleOf(userId: string): Promise<Pick<PublicUser, "role">> {
  const [u] = await getDb().select({ role: schema.user.role }).from(schema.user).where(eq(schema.user.id, userId));
  return { role: (u?.role ?? "user") as PublicUser["role"] };
}

// --- lookups ---------------------------------------------------------------------------------------------

async function cloudOf(id: string): Promise<CloudRow | undefined> {
  const [row] = await getDb().select().from(schema.cloudSession).where(eq(schema.cloudSession.id, id));
  return row;
}

/** The cloud session whose container signs in with this device. */
export async function cloudByDevice(deviceId: string): Promise<CloudRow | undefined> {
  if (!deviceId) return undefined;
  const [row] = await getDb().select().from(schema.cloudSession).where(eq(schema.cloudSession.deviceId, deviceId));
  return row;
}

/** The container's daemon as a host device: it has no agent_host_device row (decision 3). */
export function cloudDevice(row: CloudRow): DeviceRow {
  return { deviceId: row.deviceId, hostId: row.hostId, userId: row.userId, role: "cloud", workingSessions: row.workingSessions, reportedAt: row.reportedAt };
}

async function setCloud(id: string, set: Partial<CloudRow>, onlyIn?: number[]): Promise<CloudRow | undefined> {
  const c = schema.cloudSession;
  const where = onlyIn ? and(eq(c.id, id), inArray(c.state, onlyIn)) : eq(c.id, id);
  const [row] = await getDb()
    .update(c)
    .set({ ...set, updatedAt: new Date() })
    .where(where)
    .returning();
  return row;
}

/** Cloud sessions of these work rows, by work id. */
export async function cloudOfWork(workIds: string[]): Promise<Map<string, CloudRow>> {
  if (!workIds.length) return new Map();
  const rows = await getDb().select().from(schema.cloudSession).where(inArray(schema.cloudSession.workId, workIds));
  return new Map(rows.map((r) => [r.workId, r]));
}

// --- who may use which runner ----------------------------------------------------------------------------

/** A runner is for the members of its group, or for everyone when it has none. */
export async function mayUseRunner(userId: string, host: HostRow): Promise<boolean> {
  return !host.groupId || inGroup(userId, host.groupId);
}

async function usableRunners(userId: string): Promise<HostRow[]> {
  const rows = await getDb().select().from(schema.agentHost).where(eq(schema.agentHost.scope, "cloud"));
  const out: HostRow[] = [];
  for (const h of rows) if (!h.draining && (await mayUseRunner(userId, h))) out.push(h);
  return out;
}

async function liveCount(hostIds: string[]): Promise<Map<string, number>> {
  if (!hostIds.length) return new Map();
  const c = schema.cloudSession;
  const rows = await getDb()
    .select({ hostId: c.hostId, n: sql<number>`count(*)::int` })
    .from(c)
    .where(and(inArray(c.hostId, hostIds), inArray(c.state, LIVE_CLOUD)))
    .groupBy(c.hostId);
  return new Map(rows.map((r) => [r.hostId, r.n]));
}

/** The runner with the most room among those the user may use: preferred if it has room, online ones before the rest. */
async function pickRunner(userId: string, prefer?: string): Promise<HostRow | undefined> {
  const runners = await usableRunners(userId);
  if (!runners.length) return undefined;
  const load = await liveCount(runners.map((r) => r.id));
  const room = (h: HostRow) => h.maxSessions - (load.get(h.id) ?? 0);
  const now = Date.now();
  const ranked = [...runners].sort((a, b) => {
    const online = Number(isFresh(b.lastSeenAt, now)) - Number(isFresh(a.lastSeenAt, now));
    return online || room(b) - room(a);
  });
  const preferred = prefer ? ranked.find((h) => h.id === prefer && room(h) > 0 && isFresh(h.lastSeenAt, now)) : undefined;
  return preferred ?? ranked[0];
}

/** Decision 13: containers that exist, per user and per group. Throws when over. */
async function checkQuota(userId: string): Promise<void> {
  const policy = (await serverPolicyFor(await roleOf(userId))).cloud;
  if (policy.disabled) throw new ApiError("permission", "Your organization has turned off cloud sessions");
  const c = schema.cloudSession;
  const db = getDb();
  if (policy.maxSessionsPerUser > 0) {
    const [{ n } = { n: 0 }] = await db
      .select({ n: sql<number>`count(*)::int` })
      .from(c)
      .where(and(eq(c.userId, userId), inArray(c.state, LIVE_CLOUD)));
    if (n >= policy.maxSessionsPerUser) throw new ApiError("failed_precondition", `You have ${n} cloud sessions running, the most your organization allows; one becomes free when it has been idle for a while`);
  }
  if (policy.maxSessionsPerGroup > 0) {
    const m = schema.directoryGroupMember;
    const groups = (await db.select({ id: m.groupId }).from(m).where(eq(m.userId, userId))).map((g) => g.id);
    for (const g of groups) {
      const members = (await db.select({ id: m.userId }).from(m).where(eq(m.groupId, g))).map((r) => r.id);
      const [{ n } = { n: 0 }] = await db
        .select({ n: sql<number>`count(*)::int` })
        .from(c)
        .where(and(inArray(c.userId, members), inArray(c.state, LIVE_CLOUD)));
      if (n >= policy.maxSessionsPerGroup) throw new ApiError("failed_precondition", `Your team has ${n} cloud sessions running, the most your organization allows`);
    }
  }
}

/** Why the caller can't start a cloud session on this runner; "" when they can. */
export async function runnerUnavailableReason(host: HostRow, user: Pick<PublicUser, "role">): Promise<string> {
  if (host.draining) return "This runner is draining and takes no new sessions";
  if ((await serverPolicyFor(user)).cloud.disabled) return "Your organization has turned off cloud sessions";
  return "";
}

// --- starting --------------------------------------------------------------------------------------------

export async function startCloudWork(p: Principal, req: StartHostWorkRequest, host: HostRow | undefined): Promise<{ work: WorkRow; cloud: CloudRow; host: HostRow }> {
  const policy = (await serverPolicyFor(p.user)).cloud;
  if (policy.disabled) throw new ApiError("permission", "Your organization has turned off cloud sessions");
  if (req.permissionMode === PermissionMode.BYPASS_PERMISSIONS && !policy.bypassPermissionsAllowed) {
    throw new ApiError("permission", "bypassPermissions is not allowed in cloud sessions by your organization's policy");
  }
  if (!req.prompt.trim()) throw new ApiError("invalid_request", "prompt is required");
  const repo = parseRepoKey(req.repositoryKey);
  const db = getDb();
  if (req.idempotencyKey) {
    const [same] = await db.select().from(schema.hostWork).where(and(eq(schema.hostWork.userId, p.user.id), eq(schema.hostWork.idempotencyKey, req.idempotencyKey)));
    const cloud = same && (await cloudOfWork([same.id])).get(same.id);
    const runner = same && (await hostOf(same.hostId));
    if (same && cloud && runner) return { work: same, cloud, host: runner };
  }
  const runner = host ?? (await pickRunner(p.user.id));
  if (!runner) throw new ApiError("failed_precondition", "No cloud runner is available to you; an admin registers one in the settings");
  const reason = await runnerUnavailableReason(runner, p.user);
  if (reason) throw new ApiError("failed_precondition", reason);
  const config = configOf(runner);
  const image = req.image.trim() || config.image;
  if (image !== config.image && !config.allowedImages.includes(image)) throw new ApiError("invalid_request", `${image} is not one of this runner's images`);
  await checkQuota(p.user.id);
  const work = await insertWork({
    hostId: runner.id,
    userId: p.user.id,
    kind: HostWorkKind.NEW_SESSION,
    repositoryKey: repo.key,
    baseRef: req.baseRef.trim(),
    // Decision 9: the branch is where the session's state lives.
    landing: req.landing === HostWorkLanding.NONE ? HostWorkLanding.BRANCH : req.landing,
    freshClone: true,
    prompt: req.prompt,
    title: req.title.trim().slice(0, 200),
    model: req.model,
    effort: req.effort,
    permissionMode: req.permissionMode,
    idempotencyKey: req.idempotencyKey || null,
  });
  const now = new Date();
  const [cloud] = await db
    .insert(schema.cloudSession)
    .values({ id: newId("cls"), hostId: runner.id, userId: p.user.id, workId: work.id, state: S.STARTING, image, repositoryKey: repo.key, startingSince: now, createdAt: now, updatedAt: now })
    .onConflictDoNothing()
    .returning();
  wake(runner.id);
  return { work, cloud: cloud ?? (await cloudOfWork([work.id])).get(work.id)!, host: runner };
}

/**
 * Decision 7: a relay command for a cloud session's device. A session without a container is wanted again; false when the
 * device is not a cloud session's (the relay decides as before). Throws when the session can't come back.
 */
export async function wakeCloudDevice(deviceId: string): Promise<boolean> {
  const row = await cloudByDevice(deviceId);
  if (!row) return false;
  if (row.state === S.ENDED) throw new ApiError("failed_precondition", "This cloud session has ended");
  if (LIVE_CLOUD.includes(row.state)) return true;
  await checkQuota(row.userId);
  const runner = await pickRunner(row.userId, row.hostId);
  if (!runner) throw new ApiError("failed_precondition", "No cloud runner is available to you to start this session again");
  const now = new Date();
  const next = await setCloud(row.id, { state: S.STARTING, hostId: runner.id, containerId: "", attempts: 0, error: "", startingSince: now, claimed: false }, [S.REMOVED, S.FAILED]);
  if (next && runner.id !== row.hostId) await getDb().update(schema.hostWork).set({ hostId: runner.id }).where(eq(schema.hostWork.id, row.workId));
  wake(runner.id);
  if (row.hostId !== runner.id) wake(row.hostId);
  return true;
}

/** The work was cancelled, failed or finished: the session never comes back. */
export async function endCloudForWork(workId: string, state: number = S.ENDED, error = ""): Promise<void> {
  const row = (await cloudOfWork([workId])).get(workId);
  if (!row || row.state === S.ENDED) return;
  await setCloud(row.id, { state, error: error.slice(0, 2000) || row.error });
  wake(row.hostId);
}

// --- the container's daemon ------------------------------------------------------------------------------

/** RegisterAgentHost from a container: it is up. */
export async function registerCloudDevice(row: CloudRow): Promise<CloudRow> {
  const now = new Date();
  const set: Partial<CloudRow> = { registeredAt: now, reportedAt: now, claimed: false, attempts: 0, workingSessions: 0, error: "", lastActiveAt: now };
  if (row.state === S.STARTING || row.state === S.REMOVED || row.state === S.FAILED) set.state = S.IDLE;
  if (!row.startedAt || (row.startingSince && row.startedAt < row.startingSince)) set.startedAt = now;
  return (await setCloud(row.id, set)) ?? row;
}

/** ReportAgentHost from a container: RUNNING while something works, IDLE from the moment nothing does. */
export async function reportCloudDevice(row: CloudRow, working: number): Promise<void> {
  const now = new Date();
  const set: Partial<CloudRow> = { reportedAt: now, workingSessions: Math.max(0, working) };
  if (working > 0) {
    set.lastActiveAt = now;
    if (row.state === S.IDLE || row.state === S.STARTING) set.state = S.RUNNING;
  } else if (row.state === S.RUNNING) {
    set.state = S.IDLE;
    set.lastActiveAt = now;
  }
  await setCloud(row.id, set);
}

/** ClaimHostWork from a container: its own work, once per container (decision 8). */
export async function claimCloudWork(row: CloudRow, leaseMs: number): Promise<WorkRow | undefined> {
  if (row.claimed || !LIVE_CLOUD.includes(row.state)) return undefined;
  const db = getDb();
  const w = schema.hostWork;
  const [work] = await db.select().from(w).where(eq(w.id, row.workId));
  if (!work) return undefined;
  if ([HostWorkState.DONE, HostWorkState.FAILED, HostWorkState.CANCELLED, HostWorkState.EXPIRED].includes(work.state)) {
    await endCloudForWork(work.id, work.state === HostWorkState.FAILED ? S.FAILED : S.ENDED);
    return undefined;
  }
  const now = new Date();
  const fresh = work.state === HostWorkState.QUEUED || work.state === HostWorkState.CLAIMED;
  const [taken] = await db
    .update(w)
    .set({
      deviceId: row.deviceId,
      hostId: row.hostId,
      updatedAt: now,
      ...(fresh ? { state: HostWorkState.CLAIMED, attempts: work.attempts + 1, leaseUntil: new Date(now.getTime() + leaseMs) } : {}),
    })
    .where(eq(w.id, work.id))
    .returning();
  await setCloud(row.id, { claimed: true });
  return taken;
}

/** UpdateHostWork of a cloud session's work. */
export async function onCloudWorkUpdated(work: WorkRow, state: number, sessionId: string, error: string): Promise<void> {
  const row = (await cloudOfWork([work.id])).get(work.id);
  if (!row) return;
  if (sessionId && row.sessionId !== sessionId) await setCloud(row.id, { sessionId });
  if (state === HostWorkState.FAILED) await endCloudForWork(work.id, S.FAILED, error);
  else if (state === HostWorkState.DONE) await endCloudForWork(work.id);
}

// --- the runner ------------------------------------------------------------------------------------------

/** A hash of the user names their caches on a runner; "runner" when they are the runner's (decision 15). */
function cacheKey(config: RunnerConfig, userId: string): string {
  return config.cacheScope === "runner" ? "runner" : "u" + createHash("sha256").update(userId).digest("hex").slice(0, 16);
}

interface Plan {
  start: Init<typeof CloudContainerSpecSchema>[];
  remove: Init<typeof CloudContainerRemovalSchema>[];
  egress: Init<typeof CloudEgressSchema>[];
}

/** Decision 4: what the runner's containers should be, from what it has. */
async function reconcile(host: HostRow, req: Pick<PollCloudRunnerRequest, "containers" | "creating">): Promise<Plan> {
  const db = getDb();
  const c = schema.cloudSession;
  const config = configOf(host);
  const now = Date.now();
  const plan: Plan = { start: [], remove: [], egress: [] };
  const reportedIds = req.containers.map((x) => x.cloudSessionId).filter(Boolean);
  const known = new Map((reportedIds.length ? await db.select().from(c).where(inArray(c.id, reportedIds)) : []).map((r) => [r.id, r]));
  const seen = new Set<string>();
  for (const box of req.containers) {
    const row = known.get(box.cloudSessionId);
    const drop = async (reason: string, set?: Partial<CloudRow>) => {
      plan.remove.push({ cloudSessionId: box.cloudSessionId, containerId: box.containerId, reason });
      if (row && set) await setCloud(row.id, set);
    };
    if (!row || row.hostId !== host.id || !LIVE_CLOUD.includes(row.state)) {
      await drop(row ? "the session no longer wants it" : "unknown to the server");
      continue;
    }
    if ((row.containerId && box.containerId !== row.containerId) || seen.has(row.id)) {
      await drop("another container has this session");
      continue;
    }
    seen.add(row.id);
    if (box.diskBytes > 0n) await setCloud(row.id, { diskBytes: Number(box.diskBytes) });
    if (!row.containerId && row.state === S.STARTING) await setCloud(row.id, { containerId: box.containerId });
    const idleFor = row.lastActiveAt ? now - row.lastActiveAt.getTime() : 0;
    const age = row.startedAt ? now - row.startedAt.getTime() : 0;
    if (row.state === S.IDLE && idleFor > config.idleTimeoutSeconds * 1000) {
      await drop("idle", { state: S.REMOVED, containerId: "", workingSessions: 0 });
    } else if (row.startedAt && age > config.maxLifetimeSeconds * 1000 && row.state !== S.STARTING) {
      await drop("its lifetime is over", { state: S.REMOVED, containerId: "", workingSessions: 0 });
    } else if (!config.storageOptSize && box.diskBytes > BigInt(config.diskBytes)) {
      await drop("over its disk limit", { state: S.FAILED, containerId: "", workingSessions: 0, error: "The session's container went over its disk limit and was removed" });
    } else if (row.state === S.STARTING && (now - (row.startingSince?.getTime() ?? now) > startTimeoutMs() || (!box.running && now - Date.parse(box.createdAt || new Date(now).toISOString()) > 60_000))) {
      // Created, but its daemon never registered (or exited first): another try, three in all.
      const attempts = row.attempts + 1;
      await drop("it did not start", attempts >= MAX_START_ATTEMPTS ? { state: S.FAILED, containerId: "", attempts, error: row.error || "The session's container did not start" } : { containerId: "", attempts, startingSince: new Date(now) });
    } else if (!box.running && row.state !== S.STARTING) {
      // The daemon in it exited: the container is of no more use; the next message starts another.
      await drop("stopped", { state: S.REMOVED, containerId: "", workingSessions: 0 });
    }
  }
  const creating = new Set(req.creating);
  const rows = await db.select().from(c).where(and(eq(c.hostId, host.id), inArray(c.state, LIVE_CLOUD))).orderBy(c.startingSince);
  const removed = new Set(plan.remove.map((r) => r.cloudSessionId));
  let used = req.containers.filter((x) => !removed.has(x.cloudSessionId)).length + creating.size;
  for (const row of rows) {
    if (seen.has(row.id) && !removed.has(row.id)) continue;
    if (creating.has(row.id)) continue;
    if (removed.has(row.id)) continue;
    if (row.state !== S.STARTING) {
      // It had a container that is gone, and nobody asked for that: the engine or a person removed it.
      await setCloud(row.id, { state: S.REMOVED, containerId: "", workingSessions: 0 });
      continue;
    }
    let attempts = row.attempts;
    if (row.containerId) attempts++; // started, then gone before it registered
    if (now - (row.startingSince?.getTime() ?? now) > startTimeoutMs()) attempts++; // never came up
    if (attempts >= MAX_START_ATTEMPTS) {
      await setCloud(row.id, { state: S.FAILED, containerId: "", attempts, error: row.error || "The session's container did not start" });
      continue;
    }
    if (attempts !== row.attempts) await setCloud(row.id, { containerId: "", attempts, startingSince: new Date(now) });
    if (used >= host.maxSessions) continue;
    used++;
    plan.start.push({ cloudSessionId: row.id, image: row.image || config.image, cacheKey: cacheKey(config, row.userId) });
  }
  // Starting containers need their allowlists before they register; running ones keep theirs.
  const want = await db.select().from(c).where(and(eq(c.hostId, host.id), inArray(c.state, LIVE_CLOUD)));
  const lists = new Map<string, string[]>();
  for (const row of want) {
    if (removed.has(row.id)) continue;
    if (!lists.has(row.userId)) lists.set(row.userId, await egressFor(row.userId));
    plan.egress.push({ cloudSessionId: row.id, allow: lists.get(row.userId)! });
  }
  return plan;
}

async function callingRunner(device: DeviceRow, host: HostRow): Promise<HostRow> {
  if (device.role !== "runner" || host.scope !== "cloud") throw new ApiError("permission", "Only a runner of cloud sessions does this");
  return host;
}

export async function pollCloudRunner(device: DeviceRow, host: HostRow, req: PollCloudRunnerRequest, signal?: AbortSignal) {
  await callingRunner(device, host);
  const db = getDb();
  await db.update(schema.agentHost).set({ lastSeenAt: new Date() }).where(eq(schema.agentHost.id, host.id));
  await db.update(schema.agentHostDevice).set({ reportedAt: new Date(), workingSessions: req.containers.length }).where(eq(schema.agentHostDevice.deviceId, device.deviceId));
  const deadline = Date.now() + Math.min(Math.max(req.waitSeconds, 0), MAX_POLL_S) * 1000;
  for (;;) {
    const fresh = (await hostOf(host.id)) ?? host;
    const plan = await reconcile(fresh, req);
    const left = deadline - Date.now();
    if (plan.start.length || plan.remove.length || left <= 0 || signal?.aborted) {
      return { config: configToProto(configOf(fresh)), maxSessions: fresh.maxSessions, ...plan };
    }
    await waitForChange(host.id, left, signal);
  }
}

export async function issueCloudSessionToken(device: DeviceRow, host: HostRow, id: string): Promise<{ deviceId: string; deviceToken: string }> {
  await callingRunner(device, host);
  const row = await cloudOf(id);
  if (!row || row.hostId !== host.id || row.state !== S.STARTING) throw new ApiError("not_found", `Cloud session ${id} is not starting on this runner`);
  if (row.deviceId) return { deviceId: row.deviceId, deviceToken: await rotateDeviceToken(row.deviceId) };
  const made = await createHostDevice(row.userId, `cloud ${row.repositoryKey.split("/").pop() ?? ""}`.trim(), "host_cloud", "harness-client daemon cloud");
  await setCloud(row.id, { deviceId: made.deviceId });
  return made;
}

export async function updateCloudSession(device: DeviceRow, host: HostRow, req: UpdateCloudSessionRequest): Promise<{ remove: boolean }> {
  await callingRunner(device, host);
  const row = await cloudOf(req.cloudSessionId);
  if (!row || row.hostId !== host.id) return { remove: true };
  const error = req.error.slice(0, 2000);
  switch (req.state) {
    case S.STARTING:
      if (row.state !== S.STARTING) return { remove: true };
      await setCloud(row.id, { containerId: req.containerId, starts: row.starts + 1, startedAt: new Date() });
      return { remove: false };
    case S.FAILED: {
      const attempts = row.attempts + 1;
      await setCloud(row.id, attempts >= MAX_START_ATTEMPTS ? { state: S.FAILED, containerId: "", attempts, error } : { containerId: "", attempts, error }, [S.STARTING]);
      wake(host.id);
      return { remove: true };
    }
    case S.REMOVED:
      if (row.containerId === req.containerId || !row.containerId) {
        await setCloud(row.id, { containerId: "", workingSessions: 0, ...(LIVE_CLOUD.includes(row.state) && row.state !== S.STARTING ? { state: S.REMOVED } : {}) });
      }
      return { remove: true };
  }
  throw new ApiError("invalid_request", "A runner reports STARTING, FAILED or REMOVED");
}

// --- views -----------------------------------------------------------------------------------------------

export async function runnerStats(hostIds: string[]): Promise<Map<string, Init<typeof CloudRunnerStatsSchema>>> {
  const out = new Map<string, Init<typeof CloudRunnerStatsSchema>>();
  if (!hostIds.length) return out;
  const c = schema.cloudSession;
  const dayAgo = new Date(Date.now() - 86_400_000);
  const rows = await getDb()
    .select({ hostId: c.hostId, state: c.state, n: sql<number>`count(*)::int` })
    .from(c)
    .where(and(inArray(c.hostId, hostIds), sql`(${inArray(c.state, LIVE_CLOUD)} or ${gt(c.updatedAt, dayAgo)})`))
    .groupBy(c.hostId, c.state);
  for (const id of hostIds) out.set(id, { starting: 0, running: 0, idle: 0, removed: 0, failed: 0 });
  for (const r of rows) {
    const s = out.get(r.hostId)!;
    if (r.state === S.STARTING) s.starting = r.n;
    else if (r.state === S.RUNNING) s.running = r.n;
    else if (r.state === S.IDLE) s.idle = r.n;
    else if (r.state === S.REMOVED) s.removed = r.n;
    else if (r.state === S.FAILED) s.failed = r.n;
  }
  return out;
}

async function sessionViews(rows: CloudRow[]): Promise<Init<typeof CloudSessionSchema>[]> {
  const db = getDb();
  const users = rows.length ? await db.select({ id: schema.user.id, name: schema.user.name }).from(schema.user).where(inArray(schema.user.id, [...new Set(rows.map((r) => r.userId))])) : [];
  const hosts = rows.length ? await db.select({ id: schema.agentHost.id, name: schema.agentHost.name }).from(schema.agentHost).where(inArray(schema.agentHost.id, [...new Set(rows.map((r) => r.hostId))])) : [];
  const userNames = new Map(users.map((u) => [u.id, u.name]));
  const hostNames = new Map(hosts.map((h) => [h.id, h.name]));
  return rows.map((r) => ({
    id: r.id,
    hostId: r.hostId,
    hostName: hostNames.get(r.hostId) ?? "",
    userId: r.userId,
    userName: userNames.get(r.userId) ?? "",
    workId: r.workId,
    sessionId: r.sessionId,
    state: r.state,
    containerId: r.containerId,
    image: r.image,
    repositoryKey: r.repositoryKey,
    error: r.error,
    diskBytes: BigInt(r.diskBytes),
    starts: r.starts,
    startedAt: r.startedAt?.toISOString() ?? "",
    lastActiveAt: r.lastActiveAt?.toISOString() ?? "",
    createdAt: r.createdAt.toISOString(),
    updatedAt: r.updatedAt.toISOString(),
  }));
}

export async function listCloudSessions(p: Principal, hostId: string, limit: number): Promise<Init<typeof CloudSessionSchema>[]> {
  const n = Math.min(Math.max(limit || 100, 1), 500);
  const c = schema.cloudSession;
  const db = getDb();
  if (hostId && can(p.user, "hosts.read")) {
    return sessionViews(await db.select().from(c).where(eq(c.hostId, hostId)).orderBy(desc(c.updatedAt)).limit(n));
  }
  const where = hostId ? and(eq(c.userId, p.user.id), eq(c.hostId, hostId)) : eq(c.userId, p.user.id);
  return sessionViews(await db.select().from(c).where(where).orderBy(desc(c.updatedAt)).limit(n));
}

export async function stopCloudSession(p: Principal, id: string, end: boolean): Promise<Init<typeof CloudSessionSchema>> {
  const row = await cloudOf(id);
  if (!row || (row.userId !== p.user.id && !can(p.user, "hosts.write"))) throw new ApiError("not_found", `Cloud session ${id} not found`);
  if (end) {
    await endCloudForWork(row.workId);
    await getDb()
      .update(schema.hostWork)
      .set({ state: HostWorkState.DONE, updatedAt: new Date() })
      .where(and(eq(schema.hostWork.id, row.workId), inArray(schema.hostWork.state, [HostWorkState.QUEUED, HostWorkState.CLAIMED, HostWorkState.RUNNING])));
  } else if (LIVE_CLOUD.includes(row.state)) {
    // The runner's next poll sees an unwanted container and removes it.
    await setCloud(row.id, { state: S.REMOVED, workingSessions: 0 });
  }
  wake(row.hostId);
  return (await sessionViews([(await cloudOf(id))!]))[0]!;
}

export function requireAdminForRunner(p: Principal): void {
  need(p.user, "hosts.write");
}

setCloudWake(wakeCloudDevice);
