/**
 * RelayService logic (proto/harness/relay/v1/relay.proto; the header comment there is the spec).
 *
 * Durable state lives in the relay_* tables; live fan-out goes through the RelayBus. Ordering-sensitive
 * writes of one session (RegisterSession, PublishEvents) are serialized with an in-process lock, which is
 * enough for one replica; a multi-replica deployment needs a database lock there as well.
 */
import { posix, win32 } from "node:path";
import { create, fromBinary, fromJson, toBinary, toJson, type JsonValue, type MessageInitShape } from "@bufbuild/protobuf";
import { and, asc, eq, gt, inArray, lt, max, or } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import {
  PermissionMode,
  SessionEventSchema,
  SessionInfoSchema,
  SessionStatus,
  type SessionEvent,
  type SessionInfo,
} from "../gen/harness/agent/v1/agent_pb.ts";
import {
  CommandAck_Outcome,
  CommandAckSchema,
  CommandSchema,
  DeliveryState,
  PresenceState,
  SessionSupersededSchema,
  type Command,
  type CommandAck,
  type CommandOptions,
  CommandOptionsSchema,
  type CommandResultSchema,
  type HostSchema,
  type HostStateSchema,
  type OpenHostStreamResponseSchema,
  type PresenceSchema,
  type SpawnSessionCommand,
  type SubscribeSessionResponseSchema,
} from "../gen/harness/relay/v1/relay_pb.ts";
import { AsyncQueue, Mutex } from "../lib/async.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal } from "../services/devices.ts";
import { relayBus, type HostFeedItem, type SessionFeedItem } from "./bus.ts";

type Init<T extends Parameters<typeof create>[0]> = MessageInitShape<T>;
type HostRow = typeof schema.relayHost.$inferSelect;
type SessionRow = typeof schema.relaySession.$inferSelect;
type CommandRow = typeof schema.relayCommand.$inferSelect;
export type CommandPayload = Command["payload"];

// ---------------------------------------------------------------------------------------------------------
// Limits and timing (env overrides exist for tests)
// ---------------------------------------------------------------------------------------------------------

export const MAX_PUBLISH_EVENTS = 500;
export const MAX_PUBLISH_BYTES = 4 << 20;
const DEFAULT_TTL_S = 600;
const MAX_TTL_S = 24 * 3600;
/** A note from another session waits for its machine far longer than a command someone is watching. */
const SESSION_NOTE_TTL_S = 30 * 24 * 3600;
/** A job of the auto-fix pipeline: after a week the CI run or the comment it is about has moved on. */
export const AUTOFIX_JOB_TTL_S = 7 * 24 * 3600;
const REPLAY_PAGE = 500;
/** Missed heartbeats after which a host without an open stream is offline. */
const MISSED_HEARTBEATS = 3;

function envMs(name: string, def: number): number {
  const n = Number(process.env[name] ?? "");
  return Number.isFinite(n) && n > 0 ? n : def;
}
/** Server heartbeat on streams and the interval recommended to hosts. */
export const heartbeatMs = () => Math.min(envMs("HARNESS_RELAY_HEARTBEAT_MS", 20_000), 20_000);
/** How long held calls (wait_for_ack, permission answers, queries) wait for the host ack. */
const ackWaitMs = () => envMs("HARNESS_RELAY_ACK_WAIT_MS", 10_000);
const spawnWaitMs = () => envMs("HARNESS_RELAY_SPAWN_WAIT_MS", 30_000);

const b64 = (bytes: Uint8Array) => Buffer.from(bytes).toString("base64");
const unb64 = (s: string) => new Uint8Array(Buffer.from(s, "base64"));
const encodeEvent = (e: SessionEvent) => b64(toBinary(SessionEventSchema, e));
const decodeEvent = (s: string) => fromBinary(SessionEventSchema, unb64(s));
const encodeCommand = (c: Command) => b64(toBinary(CommandSchema, c));
const decodeCommand = (s: string) => fromBinary(CommandSchema, unb64(s));
const decodeAck = (s: string) => fromBinary(CommandAckSchema, unb64(s));

const isDelta = (e: SessionEvent) => e.event.case === "assistantDelta";

// ---------------------------------------------------------------------------------------------------------
// In-process state: per-session locks, open host streams, presence, recent delta uuids
// ---------------------------------------------------------------------------------------------------------

const sessionLocks = new Map<string, { mutex: Mutex; users: number }>();

async function withSessionLock<T>(sessionId: string, fn: () => Promise<T>): Promise<T> {
  let entry = sessionLocks.get(sessionId);
  if (!entry) sessionLocks.set(sessionId, (entry = { mutex: new Mutex(), users: 0 }));
  entry.users++;
  try {
    return await entry.mutex.run(fn);
  } finally {
    if (--entry.users === 0) sessionLocks.delete(sessionId);
  }
}

/** host id -> number of open OpenHostStream calls on this replica. */
const openStreams = new Map<string, number>();

interface ViewerState {
  frontend: string;
  state: PresenceState;
  streams: number;
}
/** session id -> viewer id -> state. */
const presence = new Map<string, Map<string, ViewerState>>();

/** Recently seen AssistantDelta uuids per session (deltas are deduplicated by uuid only). */
const recentDeltas = new Map<string, Set<string>>();
const RECENT_DELTAS = 2048;

function seenDelta(sessionId: string, uuid: string): boolean {
  let set = recentDeltas.get(sessionId);
  if (!set) recentDeltas.set(sessionId, (set = new Set()));
  if (set.has(uuid)) return true;
  set.add(uuid);
  if (set.size > RECENT_DELTAS) set.delete(set.values().next().value!);
  return false;
}

// ---------------------------------------------------------------------------------------------------------
// Lookups and access rules
// ---------------------------------------------------------------------------------------------------------

function requireDevice(p: Principal): string {
  if (!p.deviceId) throw new ApiError("permission", "Relay hosts must authenticate with a device token");
  return p.deviceId;
}

/** The caller's own host (same user and device); NOT_FOUND otherwise. */
async function requireOwnHost(p: Principal, hostId: string): Promise<HostRow> {
  const deviceId = requireDevice(p);
  const [row] = await getDb().select().from(schema.relayHost).where(eq(schema.relayHost.id, hostId));
  if (!row || row.userId !== p.user.id || row.deviceId !== deviceId) throw new ApiError("not_found", `Host ${hostId} not found`);
  return row;
}

/** A host of the caller's user (viewer side); NOT_FOUND for anyone else's. */
async function requireUserHost(p: Principal, hostId: string): Promise<HostRow> {
  const [row] = await getDb().select().from(schema.relayHost).where(eq(schema.relayHost.id, hostId));
  if (!row || row.userId !== p.user.id) throw new ApiError("not_found", `Host ${hostId} not found`);
  return row;
}

/** A session of the caller's user; NOT_FOUND for unknown sessions and other users' sessions alike. */
async function requireUserSession(p: Principal, sessionId: string): Promise<SessionRow> {
  if (!sessionId) throw new ApiError("invalid_request", "session_id is required");
  const [row] = await getDb().select().from(schema.relaySession).where(eq(schema.relaySession.id, sessionId));
  if (!row || row.userId !== p.user.id) throw new ApiError("not_found", `Session ${sessionId} not found`);
  return row;
}

/** Host RPCs on a session must come from its current owner with the current epoch. */
function checkOwner(row: SessionRow | undefined, p: Principal, hostId: string, epoch: bigint): SessionRow {
  if (!row || row.userId !== p.user.id) throw new ApiError("not_found", "Session not found");
  if (row.hostId !== hostId || BigInt(row.epoch) !== epoch) {
    throw new ApiError("failed_precondition", `Epoch ${epoch} no longer owns session ${row.id} (current epoch ${row.epoch})`);
  }
  return row;
}

async function deviceName(deviceId: string): Promise<string> {
  const [d] = await getDb().select({ name: schema.device.name }).from(schema.device).where(eq(schema.device.id, deviceId));
  return d?.name ?? "";
}

function isOnline(h: HostRow, now = Date.now()): boolean {
  if ((openStreams.get(h.id) ?? 0) > 0) return true;
  return now - h.lastSeenAt.getTime() < MISSED_HEARTBEATS * heartbeatMs();
}

async function touchHost(hostId: string, extra: Partial<HostRow> = {}): Promise<void> {
  await getDb()
    .update(schema.relayHost)
    .set({ lastSeenAt: new Date(), ...extra })
    .where(eq(schema.relayHost.id, hostId));
}

function sessionInfoOf(row: SessionRow): SessionInfo {
  return fromJson(SessionInfoSchema, row.info as JsonValue, { ignoreUnknownFields: true });
}

// ---------------------------------------------------------------------------------------------------------
// Host side
// ---------------------------------------------------------------------------------------------------------

export interface RegisterHostInput {
  instanceId: string;
  clientVersion: string;
  daemon: boolean;
  allowedRoots: string[];
  tunnels: boolean;
  pty: boolean;
}

export async function registerHost(p: Principal, input: RegisterHostInput): Promise<{ hostId: string; heartbeatIntervalSeconds: number }> {
  const deviceId = requireDevice(p);
  if (!input.instanceId) throw new ApiError("invalid_request", "instance_id is required");
  const roots = input.daemon ? input.allowedRoots.filter(Boolean) : [];
  for (const r of roots) if (!isAbsolutePath(r)) throw new ApiError("invalid_request", `allowed root ${r} is not absolute`);
  const now = new Date();
  const fields = {
    clientVersion: input.clientVersion,
    daemon: input.daemon,
    allowedRoots: roots,
    tunnels: input.tunnels,
    pty: input.pty,
    lastSeenAt: now,
  };
  const db = getDb();
  const [row] = await db
    .insert(schema.relayHost)
    .values({ id: newId("hst"), userId: p.user.id, deviceId, instanceId: input.instanceId, createdAt: now, ...fields })
    .onConflictDoUpdate({ target: [schema.relayHost.deviceId, schema.relayHost.instanceId], set: fields })
    .returning({ id: schema.relayHost.id });
  return { hostId: row!.id, heartbeatIntervalSeconds: Math.max(1, Math.round(heartbeatMs() / 1000)) };
}

/** Unacked, unexpired commands for a host: its host-level commands and those of the sessions it owns. */
async function pendingCommandsForHost(hostId: string): Promise<CommandRow[]> {
  const db = getDb();
  const owned = db
    .select({ id: schema.relaySession.id })
    .from(schema.relaySession)
    .where(and(eq(schema.relaySession.hostId, hostId), eq(schema.relaySession.active, true)));
  // An auto-fix job also reaches a session that is closed on its machine: the machine reopens it.
  const closed = db
    .select({ id: schema.relaySession.id })
    .from(schema.relaySession)
    .where(and(eq(schema.relaySession.hostId, hostId), eq(schema.relaySession.active, false)));
  return db
    .select()
    .from(schema.relayCommand)
    .where(
      and(
        eq(schema.relayCommand.state, "queued"),
        gt(schema.relayCommand.expiresAt, new Date()),
        or(
          and(eq(schema.relayCommand.hostId, hostId), eq(schema.relayCommand.sessionId, "")),
          inArray(schema.relayCommand.sessionId, owned),
          and(eq(schema.relayCommand.kind, "deliverAutoFixJob"), inArray(schema.relayCommand.sessionId, closed)),
        ),
      ),
    )
    .orderBy(asc(schema.relayCommand.ord));
}

async function pendingCommandsForSession(sessionId: string): Promise<CommandRow[]> {
  return getDb()
    .select()
    .from(schema.relayCommand)
    .where(and(eq(schema.relayCommand.sessionId, sessionId), eq(schema.relayCommand.state, "queued"), gt(schema.relayCommand.expiresAt, new Date())))
    .orderBy(asc(schema.relayCommand.ord));
}

/** The wire form of a stored command, routed to `epoch` (0 for host-level commands). */
function commandForDelivery(row: CommandRow, epoch: number): Command {
  const c = decodeCommand(row.command);
  c.epoch = BigInt(epoch);
  return c;
}

async function broadcastHostStateForHost(hostId: string): Promise<void> {
  const rows = await getDb()
    .select({ id: schema.relaySession.id })
    .from(schema.relaySession)
    .where(and(eq(schema.relaySession.hostId, hostId), eq(schema.relaySession.active, true)));
  for (const r of rows) relayBus().publishSession(r.id, { kind: "host" });
}

/**
 * OpenHostStream: replaces any previous stream of the host, redelivers pending commands in order, then
 * streams live commands, supersede notices and heartbeats.
 */
export async function* openHostStream(p: Principal, hostId: string, signal: AbortSignal): AsyncGenerator<Init<typeof OpenHostStreamResponseSchema>> {
  await requireOwnHost(p, hostId);
  const bus = relayBus();
  const streamId = newId("hs");
  const queue = new AsyncQueue<HostFeedItem>();
  // Close the previous stream first, then subscribe, so this stream never sees its own notice.
  bus.publishHost(hostId, { kind: "replaced", streamId });
  const unsubscribe = bus.subscribeHost(hostId, (item) => queue.push(item));
  openStreams.set(hostId, (openStreams.get(hostId) ?? 0) + 1);
  try {
    await touchHost(hostId);
    await broadcastHostStateForHost(hostId);
    // (command id, epoch) pairs already sent: the replay may overlap with live pushes queued meanwhile.
    const sent = new Set<string>();
    const epochs = new Map<string, number>();
    const owned = await getDb()
      .select({ id: schema.relaySession.id, epoch: schema.relaySession.epoch })
      .from(schema.relaySession)
      .where(and(eq(schema.relaySession.hostId, hostId), eq(schema.relaySession.active, true)));
    for (const s of owned) epochs.set(s.id, s.epoch);
    for (const row of await pendingCommandsForHost(hostId)) {
      const cmd = commandForDelivery(row, row.sessionId ? (epochs.get(row.sessionId) ?? 0) : 0);
      sent.add(`${cmd.commandId}/${cmd.epoch}`);
      yield { event: { case: "command", value: cmd } };
    }
    const interval = heartbeatMs();
    let lastSent = Date.now();
    while (!signal.aborted) {
      const item = await queue.next(Math.max(0, interval - (Date.now() - lastSent)), signal);
      if (signal.aborted) return;
      if (!item) {
        await touchHost(hostId);
        yield { event: { case: "heartbeat", value: { serverTime: new Date().toISOString() } } };
        lastSent = Date.now();
        continue;
      }
      if (item.kind === "replaced") {
        if (item.streamId === streamId) continue;
        throw new ApiError("conflict", "A newer host stream replaced this one");
      }
      if (item.kind === "superseded") {
        yield { event: { case: "sessionSuperseded", value: item.superseded } };
      } else {
        const key = `${item.command.commandId}/${item.command.epoch}`;
        if (sent.has(key)) continue;
        sent.add(key);
        yield { event: { case: "command", value: item.command } };
      }
      lastSent = Date.now();
    }
  } finally {
    unsubscribe();
    const n = (openStreams.get(hostId) ?? 1) - 1;
    if (n > 0) openStreams.set(hostId, n);
    else openStreams.delete(hostId);
    // Without a stream the host stays online until it misses heartbeats; tell viewers right away anyway
    // when this was the last stream and the heartbeat is stale.
    void broadcastHostStateForHost(hostId).catch(() => {});
  }
}

/** RegisterSession: takes over (or creates) the session with a new epoch. */
export async function registerSession(p: Principal, hostId: string, info: SessionInfo | undefined): Promise<{ epoch: bigint; ackedSeq: bigint }> {
  const host = await requireOwnHost(p, hostId);
  if (!info?.id) throw new ApiError("invalid_request", "session.id is required");
  const sessionId = info.id;
  if (sessionId.length > 128) throw new ApiError("invalid_request", "session.id is too long");
  return withSessionLock(sessionId, async () => {
    const db = getDb();
    const [prev] = await db.select().from(schema.relaySession).where(eq(schema.relaySession.id, sessionId));
    if (prev && prev.userId !== p.user.id) throw new ApiError("not_found", `Session ${sessionId} not found`);
    const epoch = (prev?.epoch ?? 0) + 1;
    const now = new Date();
    const stored = { ...info, deviceId: info.deviceId || host.deviceId };
    const fields = {
      hostId,
      deviceId: host.deviceId,
      epoch,
      info: toJson(SessionInfoSchema, create(SessionInfoSchema, stored)) as object,
      status: info.status,
      active: true,
      updatedAt: now,
    };
    await db
      .insert(schema.relaySession)
      .values({ id: sessionId, userId: p.user.id, createdAt: now, ackedSeq: 0, ...fields })
      .onConflictDoUpdate({ target: schema.relaySession.id, set: fields });
    const bus = relayBus();
    if (prev && prev.active && prev.hostId !== hostId) {
      bus.publishHost(prev.hostId, {
        kind: "superseded",
        superseded: create(SessionSupersededSchema, {
          sessionId,
          oldEpoch: BigInt(prev.epoch),
          newEpoch: BigInt(epoch),
          newDeviceId: host.deviceId,
        }),
      });
    }
    // Commands still queued for the session now go to the new owner, routed to the new epoch.
    for (const row of await pendingCommandsForSession(sessionId)) {
      bus.publishHost(hostId, { kind: "command", command: commandForDelivery(row, epoch) });
    }
    bus.publishSession(sessionId, { kind: "host" });
    return { epoch: BigInt(epoch), ackedSeq: BigInt(prev?.ackedSeq ?? 0) };
  }).then(async (r) => {
    await notifySessionRegistered(sessionId);
    return r;
  });
}

export async function unregisterSession(p: Principal, hostId: string, sessionId: string, epoch: bigint): Promise<void> {
  await requireOwnHost(p, hostId);
  await withSessionLock(sessionId, async () => {
    const db = getDb();
    const [row] = await db.select().from(schema.relaySession).where(eq(schema.relaySession.id, sessionId));
    checkOwner(row, p, hostId, epoch);
    await db.update(schema.relaySession).set({ active: false, updatedAt: new Date() }).where(eq(schema.relaySession.id, sessionId));
  });
  relayBus().publishSession(sessionId, { kind: "host" });
}

export interface EnvelopeInput {
  uuid: string;
  event?: SessionEvent;
}

/**
 * PublishEvents: stores durable events by seq (deduplicated by seq and uuid), advances acked_seq over the
 * contiguous prefix and fans out every newly contiguous event in seq order. Deltas are fanned out only when
 * the durable event they follow has been fanned out; they are never stored.
 */
export async function publishEvents(
  p: Principal,
  hostId: string,
  sessionId: string,
  epoch: bigint,
  envelopes: EnvelopeInput[],
): Promise<{ ackedSeq: bigint; duplicates: number }> {
  await requireOwnHost(p, hostId);
  if (envelopes.length > MAX_PUBLISH_EVENTS) throw new ApiError("invalid_request", `At most ${MAX_PUBLISH_EVENTS} events per call`);
  for (const env of envelopes) {
    if (!env.uuid) throw new ApiError("invalid_request", "every event needs a uuid");
    if (!env.event) throw new ApiError("invalid_request", "every envelope needs an event");
    if (env.event.sessionId && env.event.sessionId !== sessionId) throw new ApiError("invalid_request", "event.session_id does not match the request");
    if (!isDelta(env.event) && env.event.seq <= 0n) throw new ApiError("invalid_request", "durable events need seq >= 1");
  }
  return withSessionLock(sessionId, async () => {
    const db = getDb();
    const [found] = await db.select().from(schema.relaySession).where(eq(schema.relaySession.id, sessionId));
    const row = checkOwner(found, p, hostId, epoch);
    if (!row.active) throw new ApiError("failed_precondition", `Session ${sessionId} was unregistered`);
    const bus = relayBus();
    let acked = row.ackedSeq;
    let duplicates = 0;
    let latestInfo: SessionInfo | undefined;
    const [{ m } = { m: null }] = await db
      .select({ m: max(schema.relayEvent.seq) })
      .from(schema.relayEvent)
      .where(eq(schema.relayEvent.sessionId, sessionId));
    let maxStored = Number(m ?? 0);

    const fanOut = (ev: SessionEvent) => {
      if (ev.event.case === "sessionUpdated") latestInfo = ev.event.value;
      bus.publishSession(sessionId, { kind: "event", event: ev });
    };
    /** Advances acked over stored events that became contiguous, fanning them out. */
    const advanceFromStore = async () => {
      while (maxStored > acked) {
        const rows = await db
          .select({ seq: schema.relayEvent.seq, event: schema.relayEvent.event })
          .from(schema.relayEvent)
          .where(and(eq(schema.relayEvent.sessionId, sessionId), gt(schema.relayEvent.seq, acked)))
          .orderBy(asc(schema.relayEvent.seq))
          .limit(REPLAY_PAGE);
        let progressed = false;
        for (const r of rows) {
          if (r.seq !== acked + 1) break;
          acked = r.seq;
          progressed = true;
          fanOut(decodeEvent(r.event));
        }
        if (!progressed || rows.length < REPLAY_PAGE) break;
      }
    };

    const now = new Date();
    for (const env of envelopes) {
      const ev = create(SessionEventSchema, env.event!);
      ev.sessionId = sessionId;
      if (isDelta(ev)) {
        if (seenDelta(sessionId, env.uuid)) {
          duplicates++;
          continue;
        }
        // Ordered after the durable event with this seq: deliverable once that one is out.
        if (Number(ev.seq) <= acked) bus.publishSession(sessionId, { kind: "event", event: ev });
        continue;
      }
      const seq = Number(ev.seq);
      if (seq <= acked) {
        duplicates++;
        continue;
      }
      const inserted = await db
        .insert(schema.relayEvent)
        .values({ sessionId, seq, uuid: env.uuid, epoch: Number(epoch), event: encodeEvent(ev), createdAt: now })
        .onConflictDoNothing()
        .returning({ seq: schema.relayEvent.seq });
      if (!inserted.length) duplicates++;
      else maxStored = Math.max(maxStored, seq);
      if (inserted.length && seq === acked + 1) {
        acked = seq;
        fanOut(ev);
      }
      await advanceFromStore();
    }

    const update: Partial<SessionRow> = { updatedAt: now };
    if (acked !== row.ackedSeq) update.ackedSeq = acked;
    if (latestInfo) {
      update.info = toJson(SessionInfoSchema, create(SessionInfoSchema, { ...latestInfo, deviceId: latestInfo.deviceId || row.deviceId })) as object;
      update.status = latestInfo.status;
    }
    await db.update(schema.relaySession).set(update).where(eq(schema.relaySession.id, sessionId));
    return { ackedSeq: BigInt(acked), duplicates };
  });
}

/**
 * AckCommands: records the first ack of each command (later acks are ignored) and wakes held calls. Acks
 * from a host that does not own the command (another host took the session over) are ignored.
 */
export async function ackCommands(p: Principal, hostId: string, acks: CommandAck[]): Promise<void> {
  await requireOwnHost(p, hostId);
  const db = getDb();
  for (const ack of acks) {
    if (!ack.commandId) continue;
    const [row] = await db.select().from(schema.relayCommand).where(eq(schema.relayCommand.id, ack.commandId));
    if (!row || row.userId !== p.user.id || row.state !== "queued") continue;
    if (row.sessionId) {
      const [s] = await db.select({ hostId: schema.relaySession.hostId }).from(schema.relaySession).where(eq(schema.relaySession.id, row.sessionId));
      if (s?.hostId !== hostId) continue;
    } else if (row.hostId !== hostId) {
      continue;
    }
    const updated = await db
      .update(schema.relayCommand)
      .set({ state: "acked", ack: b64(toBinary(CommandAckSchema, ack)), ackedAt: new Date() })
      .where(and(eq(schema.relayCommand.id, row.id), eq(schema.relayCommand.state, "queued")))
      .returning({ id: schema.relayCommand.id });
    if (updated.length) {
      relayBus().publishAck(row.id, ack);
      for (const h of ackHooks) await Promise.resolve(h(decodeCommand(row.command), ack)).catch((err) => console.error("[harness] relay ack hook failed", err));
    }
  }
}

// ---------------------------------------------------------------------------------------------------------
// Commands the server sends by itself (the auto-fix pipeline)
// ---------------------------------------------------------------------------------------------------------

type AckHook = (command: Command, ack: CommandAck) => void | Promise<void>;
type RegisteredHook = (sessionId: string) => void | Promise<void>;
const ackHooks = new Set<AckHook>();
const registeredHooks = new Set<RegisteredHook>();

/** Called for the first ack of every command. */
export function onCommandAcked(fn: AckHook): () => void {
  ackHooks.add(fn);
  return () => ackHooks.delete(fn);
}

/** Called when a session registers with a host (opened or taken over). */
export function onSessionRegistered(fn: RegisteredHook): () => void {
  registeredHooks.add(fn);
  return () => registeredHooks.delete(fn);
}

async function notifySessionRegistered(sessionId: string): Promise<void> {
  for (const h of registeredHooks) await Promise.resolve(h(sessionId)).catch((err) => console.error("[harness] relay session hook failed", err));
}

/**
 * Queues a command of the server itself for a session, whether or not its machine is online or the session open there.
 * The same key replaces the text of a command still waiting. "no_host" means no machine ever mirrored this session: the
 * caller keeps what it has and tries again when one registers it (onSessionRegistered).
 */
export async function queueServerCommand(userId: string, sessionId: string, payload: CommandPayload, key: string, ttlS: number): Promise<"queued" | "no_host"> {
  const db = getDb();
  const [session] = await db.select().from(schema.relaySession).where(eq(schema.relaySession.id, sessionId));
  if (!session || session.userId !== userId) return "no_host";
  const [waiting] = await db
    .select()
    .from(schema.relayCommand)
    .where(and(eq(schema.relayCommand.userId, userId), eq(schema.relayCommand.idempotencyKey, key)));
  if (waiting) {
    if (waiting.state === "queued") {
      const cmd = decodeCommand(waiting.command);
      cmd.payload = payload;
      await db.update(schema.relayCommand).set({ command: encodeCommand(cmd) }).where(and(eq(schema.relayCommand.id, waiting.id), eq(schema.relayCommand.state, "queued")));
    }
    return "queued";
  }
  const p = { user: { id: userId } } as Principal;
  await enqueue(p, { session }, payload, create(CommandOptionsSchema, { idempotencyKey: key }), ttlS * 1000, "server");
  return "queued";
}

export interface HostSessionStateInput {
  sessionId: string;
  epoch: bigint;
  status: SessionStatus;
}

export async function heartbeat(
  p: Principal,
  hostId: string,
  sessions: HostSessionStateInput[],
  lastLocalActivityAt: string,
): Promise<{ superseded: { sessionId: string; oldEpoch: bigint; newEpoch: bigint; newDeviceId: string }[] }> {
  await requireOwnHost(p, hostId);
  const activity = lastLocalActivityAt ? new Date(lastLocalActivityAt) : undefined;
  await touchHost(hostId, activity && !Number.isNaN(activity.getTime()) ? { lastLocalActivityAt: activity } : {});
  const superseded = [];
  const db = getDb();
  const ids = sessions.map((s) => s.sessionId).filter(Boolean);
  const rows = ids.length ? await db.select().from(schema.relaySession).where(inArray(schema.relaySession.id, ids)) : [];
  const byId = new Map(rows.filter((r) => r.userId === p.user.id).map((r) => [r.id, r]));
  for (const s of sessions) {
    const row = byId.get(s.sessionId);
    if (!row) continue;
    if (row.hostId === hostId && BigInt(row.epoch) === s.epoch) {
      if (row.status !== s.status) {
        await db.update(schema.relaySession).set({ status: s.status }).where(eq(schema.relaySession.id, row.id));
      }
    } else if (BigInt(row.epoch) > s.epoch) {
      superseded.push({ sessionId: row.id, oldEpoch: s.epoch, newEpoch: BigInt(row.epoch), newDeviceId: row.deviceId });
    }
  }
  return { superseded };
}

// ---------------------------------------------------------------------------------------------------------
// Viewer side: hosts, subscriptions, presence
// ---------------------------------------------------------------------------------------------------------

/** Hosts seen within this window (or with live sessions) are listed. */
const LIST_HOSTS_WINDOW_MS = 7 * 24 * 3600 * 1000;

export async function listHosts(p: Principal): Promise<Init<typeof HostSchema>[]> {
  const db = getDb();
  const hosts = await db.select().from(schema.relayHost).where(eq(schema.relayHost.userId, p.user.id));
  const sessions = await db
    .select()
    .from(schema.relaySession)
    .where(and(eq(schema.relaySession.userId, p.user.id), eq(schema.relaySession.active, true)));
  const devices = hosts.length
    ? await db
        .select({ id: schema.device.id, name: schema.device.name })
        .from(schema.device)
        .where(inArray(schema.device.id, [...new Set(hosts.map((h) => h.deviceId))]))
    : [];
  const names = new Map(devices.map((d) => [d.id, d.name]));
  const now = Date.now();
  const out: Init<typeof HostSchema>[] = [];
  for (const h of hosts) {
    const online = isOnline(h, now);
    const own = sessions.filter((s) => s.hostId === h.id);
    if (!own.length && !online && now - h.lastSeenAt.getTime() > LIST_HOSTS_WINDOW_MS) continue;
    out.push({
      hostId: h.id,
      deviceId: h.deviceId,
      deviceName: names.get(h.deviceId) ?? "",
      online,
      daemon: h.daemon,
      allowedRoots: Array.isArray(h.allowedRoots) ? (h.allowedRoots as string[]) : [],
      tunnels: h.tunnels,
      pty: h.pty,
      sessions: own.map((s) => {
        const info = sessionInfoOf(s);
        info.status = online ? (s.status as SessionStatus) || info.status : SessionStatus.WAITING_FOR_CONNECTION;
        info.deviceName = info.deviceName || (names.get(h.deviceId) ?? "");
        return info;
      }),
      lastSeenAt: h.lastSeenAt.toISOString(),
    });
  }
  out.sort((a, b) => String(b.lastSeenAt).localeCompare(String(a.lastSeenAt)));
  return out;
}

function presenceOf(sessionId: string): Init<typeof PresenceSchema> {
  const viewers = [...(presence.get(sessionId)?.entries() ?? [])]
    .filter(([, v]) => v.streams > 0)
    .map(([viewerId, v]) => ({ viewerId, frontend: v.frontend, state: v.state }));
  return { viewers };
}

/** Whether a human is looking at the session right now (a FOCUSED viewer); push is suppressed then. */
export function hasFocusedViewer(sessionId: string): boolean {
  return [...(presence.get(sessionId)?.values() ?? [])].some((v) => v.streams > 0 && v.state === PresenceState.FOCUSED);
}

function viewerJoined(sessionId: string, viewerId: string, frontend: string): void {
  let m = presence.get(sessionId);
  if (!m) presence.set(sessionId, (m = new Map()));
  const v = m.get(viewerId);
  if (v) {
    v.streams++;
    if (frontend) v.frontend = frontend;
  } else {
    m.set(viewerId, { frontend, state: PresenceState.UNFOCUSED, streams: 1 });
  }
  relayBus().publishSession(sessionId, { kind: "presence" });
}

function viewerLeft(sessionId: string, viewerId: string): void {
  const m = presence.get(sessionId);
  const v = m?.get(viewerId);
  if (!m || !v) return;
  if (--v.streams <= 0) m.delete(viewerId);
  if (!m.size) presence.delete(sessionId);
  relayBus().publishSession(sessionId, { kind: "presence" });
}

export async function updatePresence(p: Principal, sessionId: string, viewerId: string, state: PresenceState): Promise<void> {
  await requireUserSession(p, sessionId);
  if (!viewerId) throw new ApiError("invalid_request", "viewer_id is required");
  if (state !== PresenceState.FOCUSED && state !== PresenceState.UNFOCUSED) throw new ApiError("invalid_request", "state is required");
  const v = presence.get(sessionId)?.get(viewerId);
  // Presence belongs to an open SubscribeSession stream; updates for unknown viewers are ignored.
  if (!v || v.state === state) return;
  v.state = state;
  relayBus().publishSession(sessionId, { kind: "presence" });
}

async function hostStateOf(sessionId: string): Promise<Init<typeof HostStateSchema>> {
  const db = getDb();
  const [s] = await db.select().from(schema.relaySession).where(eq(schema.relaySession.id, sessionId));
  if (!s) return {};
  const [h] = await db.select().from(schema.relayHost).where(eq(schema.relayHost.id, s.hostId));
  return { hostId: s.hostId, deviceName: await deviceName(s.deviceId), online: !!h && s.active && isOnline(h), epoch: BigInt(s.epoch) };
}

async function loadEvents(sessionId: string, afterSeq: number, uptoSeq: number): Promise<SessionEvent[]> {
  const rows = await getDb()
    .select({ seq: schema.relayEvent.seq, event: schema.relayEvent.event })
    .from(schema.relayEvent)
    .where(and(eq(schema.relayEvent.sessionId, sessionId), gt(schema.relayEvent.seq, afterSeq), lt(schema.relayEvent.seq, uptoSeq + 1)))
    .orderBy(asc(schema.relayEvent.seq))
    .limit(REPLAY_PAGE);
  return rows.map((r) => decodeEvent(r.event));
}

/**
 * SubscribeSession (FlushGate): subscribes to live events first and buffers them, replays stored events in
 * (after_seq, acked_seq], sends FlushComplete, then drains the buffer skipping what the replay covered.
 * Durable events reach the viewer in seq order without gaps or duplicates.
 */
export async function* subscribeSession(
  p: Principal,
  sessionId: string,
  afterSeq: bigint,
  viewerIdIn: string,
  frontend: string,
  signal: AbortSignal,
): AsyncGenerator<Init<typeof SubscribeSessionResponseSchema>> {
  await requireUserSession(p, sessionId);
  const viewerId = viewerIdIn || newId("vwr");
  const queue = new AsyncQueue<SessionFeedItem>();
  const unsubscribe = relayBus().subscribeSession(sessionId, (item) => queue.push(item));
  viewerJoined(sessionId, viewerId, frontend);
  try {
    let last = Number(afterSeq < 0n ? 0n : afterSeq);
    const [s] = await getDb().select({ ackedSeq: schema.relaySession.ackedSeq }).from(schema.relaySession).where(eq(schema.relaySession.id, sessionId));
    const upto = s?.ackedSeq ?? 0;
    while (last < upto) {
      const page = await loadEvents(sessionId, last, upto);
      if (!page.length) break;
      for (const ev of page) {
        if (Number(ev.seq) !== last + 1) break;
        yield { event: { case: "sessionEvent", value: ev } };
        last = Number(ev.seq);
      }
      if (page.length < REPLAY_PAGE) break;
    }
    yield { event: { case: "flushComplete", value: { lastSeq: BigInt(last) } } };
    yield { event: { case: "hostState", value: await hostStateOf(sessionId) } };
    yield { event: { case: "presence", value: presenceOf(sessionId) } };

    const interval = heartbeatMs();
    let lastSent = Date.now();
    while (!signal.aborted) {
      const item = await queue.next(Math.max(0, interval - (Date.now() - lastSent)), signal);
      if (signal.aborted) return;
      if (!item) {
        yield { event: { case: "heartbeat", value: { serverTime: new Date().toISOString() } } };
        lastSent = Date.now();
        continue;
      }
      if (item.kind === "host") {
        yield { event: { case: "hostState", value: await hostStateOf(sessionId) } };
      } else if (item.kind === "presence") {
        yield { event: { case: "presence", value: presenceOf(sessionId) } };
      } else {
        const ev = item.event;
        const seq = Number(ev.seq);
        if (isDelta(ev)) {
          if (seq < last) continue; // belongs before what the viewer already has
          yield { event: { case: "sessionEvent", value: ev } };
        } else {
          if (seq <= last) continue; // covered by the replay
          // Defensive: fill a gap from the store (an event fanned out between subscribe and replay read).
          while (seq > last + 1) {
            const missing = await loadEvents(sessionId, last, seq - 1);
            if (!missing.length || Number(missing[0]!.seq) !== last + 1) break;
            for (const m of missing) {
              if (Number(m.seq) !== last + 1) break;
              yield { event: { case: "sessionEvent", value: m } };
              last = Number(m.seq);
            }
          }
          if (seq !== last + 1) continue;
          yield { event: { case: "sessionEvent", value: ev } };
          last = seq;
        }
      }
      lastSent = Date.now();
    }
  } finally {
    unsubscribe();
    viewerLeft(sessionId, viewerId);
  }
}

// ---------------------------------------------------------------------------------------------------------
// Viewer side: commands
// ---------------------------------------------------------------------------------------------------------

export type HoldMode =
  /** Return once queued (or acked, with wait_for_ack). */
  | "optional"
  /** Always wait up to the ack timeout (permission / question / plan answers). */
  | "implied"
  /** Query: wait for the result; UNAVAILABLE when the host does not answer in time. */
  | "query";

export interface CommandOutcome {
  commandId: string;
  delivery: DeliveryState;
  ack?: CommandAck;
}

function ttlSeconds(opts: CommandOptions | undefined): number {
  const t = opts?.ttlSeconds ?? 0;
  if (t < 0) throw new ApiError("invalid_request", "ttl_seconds must not be negative");
  return t === 0 ? DEFAULT_TTL_S : Math.min(t, MAX_TTL_S);
}

/** Session id carried by the wrapped AgentService request (every session command has one). */
function payloadSessionId(payload: CommandPayload): string {
  const v = payload.value as { sessionId?: string } | undefined;
  return v?.sessionId ?? "";
}

function checkPayload(payload: CommandPayload): void {
  if (!payload.case || !payload.value) throw new ApiError("invalid_request", "request is required");
  if (payload.case === "setPermissionMode" && payload.value.mode === PermissionMode.BYPASS_PERMISSIONS) {
    throw new ApiError("permission", "bypassPermissions cannot be enabled remotely");
  }
  if (payload.case === "spawnSession" && payload.value.create?.permissionMode === PermissionMode.BYPASS_PERMISSIONS) {
    throw new ApiError("permission", "bypassPermissions cannot be enabled remotely");
  }
}

/** Waits for the ack of a command until `deadline` (ms epoch). */
async function waitForAck(commandId: string, deadline: number): Promise<CommandAck | undefined> {
  let resolve!: (a: CommandAck | undefined) => void;
  const done = new Promise<CommandAck | undefined>((r) => (resolve = r));
  const unsubscribe = relayBus().subscribeAck(commandId, (ack) => resolve(ack));
  const timer = setTimeout(() => resolve(undefined), Math.max(0, deadline - Date.now()));
  try {
    // Acked before we subscribed?
    const [row] = await getDb().select({ state: schema.relayCommand.state, ack: schema.relayCommand.ack }).from(schema.relayCommand).where(eq(schema.relayCommand.id, commandId));
    if (row?.state === "acked" && row.ack) return decodeAck(row.ack);
    return await done;
  } finally {
    clearTimeout(timer);
    unsubscribe();
  }
}

interface EnqueueTarget {
  /** Session commands: routed to the session's current owner. */
  session?: SessionRow;
  /** Host-level commands (spawn). */
  hostId?: string;
}

async function enqueue(p: Principal, target: EnqueueTarget, payload: CommandPayload, opts: CommandOptions | undefined, ttlMs: number, frontend: string): Promise<{ row: CommandRow; fresh: boolean }> {
  const db = getDb();
  const key = opts?.idempotencyKey || null;
  if (key) {
    const [existing] = await db.select().from(schema.relayCommand).where(and(eq(schema.relayCommand.userId, p.user.id), eq(schema.relayCommand.idempotencyKey, key)));
    if (existing) return { row: existing, fresh: false };
  }
  const now = new Date();
  const expires = new Date(now.getTime() + ttlMs);
  const id = newId("cmd");
  const sessionId = target.session?.id ?? "";
  const cmd = create(CommandSchema, {
    commandId: id,
    sessionId,
    epoch: 0n,
    senderUserId: p.user.id,
    senderFrontend: opts?.frontend || frontend,
    createdAt: now.toISOString(),
    expiresAt: expires.toISOString(),
    payload,
  });
  const inserted = await db
    .insert(schema.relayCommand)
    .values({
      id,
      userId: p.user.id,
      hostId: target.hostId ?? "",
      sessionId,
      kind: payload.case ?? "",
      command: encodeCommand(cmd),
      idempotencyKey: key,
      createdAt: now,
      expiresAt: expires,
    })
    .onConflictDoNothing()
    .returning();
  if (!inserted.length) {
    // Lost an idempotency race: return the winner.
    const [existing] = await db.select().from(schema.relayCommand).where(and(eq(schema.relayCommand.userId, p.user.id), eq(schema.relayCommand.idempotencyKey, key!)));
    if (!existing) throw new ApiError("internal", "command insert failed");
    return { row: existing, fresh: false };
  }
  const row = inserted[0]!;
  if (target.session) {
    relayBus().publishHost(target.session.hostId, { kind: "command", command: commandForDelivery(row, target.session.epoch) });
  } else if (target.hostId) {
    relayBus().publishHost(target.hostId, { kind: "command", command: commandForDelivery(row, 0) });
  }
  return { row, fresh: true };
}

async function outcomeOf(row: CommandRow, waitUntil: number | undefined): Promise<CommandOutcome> {
  if (row.state === "acked" && row.ack) return { commandId: row.id, delivery: DeliveryState.ACKED, ack: decodeAck(row.ack) };
  const expiresAt = row.expiresAt.getTime();
  if (waitUntil !== undefined) {
    const ack = await waitForAck(row.id, Math.min(waitUntil, expiresAt));
    if (ack) return { commandId: row.id, delivery: DeliveryState.ACKED, ack };
  }
  // A timer can fire a moment early, so a wait that ran to the expiry counts as expired.
  const expired = Date.now() >= expiresAt || (waitUntil !== undefined && waitUntil >= expiresAt);
  return { commandId: row.id, delivery: expired ? DeliveryState.EXPIRED : DeliveryState.QUEUED };
}

/**
 * Queues a session command for the session's owner and optionally holds the call until the host acks.
 * Bypass mode is refused here (PERMISSION_DENIED); the host refuses it again.
 */
export async function sendSessionCommand(
  p: Principal,
  payload: CommandPayload,
  opts: CommandOptions | undefined,
  hold: HoldMode,
  frontend: string,
): Promise<CommandOutcome> {
  checkPayload(payload);
  const session = await requireUserSession(p, payloadSessionId(payload));
  if (!session.active) throw new ApiError("failed_precondition", `Session ${session.id} is no longer remote-controllable`);
  // Query results are only useful to the waiting caller: they expire with the wait.
  const ttlMs = hold === "query" ? ackWaitMs() : (payload.case === "deliverSessionNote" ? SESSION_NOTE_TTL_S : ttlSeconds(opts)) * 1000;
  const { row } = await enqueue(p, { session }, payload, opts, ttlMs, frontend);
  const wait = hold === "optional" ? opts?.waitForAck : true;
  return outcomeOf(row, wait ? Date.now() + ackWaitMs() : undefined);
}

/** Query commands: the host's result, or UNAVAILABLE when it does not answer in time. */
export async function querySession(p: Principal, payload: CommandPayload, frontend: string): Promise<CommandAck> {
  const r = await sendSessionCommand(p, payload, undefined, "query", frontend);
  if (r.delivery !== DeliveryState.ACKED || !r.ack) throw new ApiError("overloaded", "The host did not answer in time (offline or busy)");
  throwOnFailedAck(r.ack);
  return r.ack;
}

function throwOnFailedAck(ack: CommandAck): void {
  if (ack.outcome === CommandAck_Outcome.OK) return;
  const msg = ack.errorMessage || `host returned ${CommandAck_Outcome[ack.outcome]}`;
  if (ack.outcome === CommandAck_Outcome.REJECTED) throw new ApiError("permission", msg);
  throw new ApiError("failed_precondition", msg);
}

export function commandResult(o: CommandOutcome): Init<typeof CommandResultSchema> {
  return { commandId: o.commandId, delivery: o.delivery, ack: o.ack };
}

/** Whether `dir` lies inside `root` (both absolute; POSIX or Windows style). */
export function isUnderRoot(dir: string, root: string): boolean {
  const lib = /^[a-zA-Z]:[\\/]|^\\\\/.test(root) ? win32 : posix;
  const rel = lib.relative(lib.resolve(root), lib.resolve(dir));
  return rel === "" || (!rel.startsWith("..") && !lib.isAbsolute(rel));
}

function isAbsolutePath(p: string): boolean {
  return posix.isAbsolute(p) || win32.isAbsolute(p);
}

/** SpawnSession: daemon hosts only, cwd under an allowed root; held until the host created the session. */
export async function spawnSession(p: Principal, hostId: string, spawn: SpawnSessionCommand | undefined, opts: CommandOptions | undefined, frontend: string): Promise<SessionInfo> {
  if (!spawn?.create) throw new ApiError("invalid_request", "spawn.create is required");
  const payload: CommandPayload = { case: "spawnSession", value: spawn };
  checkPayload(payload);
  const host = await requireUserHost(p, hostId);
  if (!host.daemon) throw new ApiError("failed_precondition", "This host does not accept new sessions (not a daemon)");
  const cwd = spawn.create.cwd;
  if (!cwd || !isAbsolutePath(cwd)) throw new ApiError("invalid_request", "spawn.create.cwd must be an absolute path");
  const roots = Array.isArray(host.allowedRoots) ? (host.allowedRoots as string[]) : [];
  if (!roots.some((r) => isUnderRoot(cwd, r))) throw new ApiError("permission", `${cwd} is not under an allowed root of this host`);
  if (!isOnline(host)) throw new ApiError("overloaded", "The host is offline");
  const wait = spawnWaitMs();
  const { row } = await enqueue(p, { hostId }, payload, opts, wait, frontend);
  const r = await outcomeOf(row, Date.now() + wait);
  if (r.delivery !== DeliveryState.ACKED || !r.ack) throw new ApiError("overloaded", "The host did not create the session in time");
  throwOnFailedAck(r.ack);
  if (r.ack.result.case !== "spawnSession" || !r.ack.result.value.session) throw new ApiError("internal", "The host acked without a session");
  return r.ack.result.value.session;
}

/** Whether a session's machine is up: false for a session no machine mirrors, or one whose machine went away. */
export async function sessionMachineOnline(sessionId: string): Promise<boolean> {
  const db = getDb();
  const [s] = await db.select({ hostId: schema.relaySession.hostId }).from(schema.relaySession).where(eq(schema.relaySession.id, sessionId));
  if (!s) return false;
  const [h] = await db.select().from(schema.relayHost).where(eq(schema.relayHost.id, s.hostId));
  return !!h && isOnline(h);
}

/** Takes back a server command still waiting for its machine, so it can't run as well as what replaces it. */
export async function withdrawServerCommand(userId: string, key: string): Promise<void> {
  await getDb()
    .update(schema.relayCommand)
    .set({ state: "withdrawn" })
    .where(and(eq(schema.relayCommand.userId, userId), eq(schema.relayCommand.idempotencyKey, key), eq(schema.relayCommand.state, "queued")));
}

/** Test hook: drops in-process relay state between test files sharing a module instance. */
export function resetRelayState(): void {
  sessionLocks.clear();
  openStreams.clear();
  presence.clear();
  recentDeltas.clear();
}
