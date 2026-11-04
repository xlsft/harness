/**
 * RelayService: epochs and supersede, seq dedup and gaps, FlushGate ordering, command redelivery / ack /
 * expiry, held calls and queries, spawn roots, bypass rejection, presence and access rules.
 */
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { randomUUID } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { PermissionMode, SessionEventSchema, SessionStatus, type SessionEvent } from "../src/gen/harness/agent/v1/agent_pb.ts";
import {
  CommandAck_Outcome,
  DeliveryState,
  PresenceState,
  type OpenHostStreamResponse,
  type SubscribeSessionResponse,
} from "../src/gen/harness/relay/v1/relay_pb.ts";
import { ResolveSessionProposalRequest_Action, SessionNote_Kind } from "../src/gen/harness/agent/v1/session_links_pb.ts";
import { isUnderRoot } from "../src/relay/service.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

type Relay = TestServer["relay"];

let ts: TestServer;
let A1: Relay; // alice, laptop
let A2: Relay; // alice, desktop (second device)
let B: Relay; // bob

beforeAll(async () => {
  ts = await startTestServer({ HARNESS_RELAY_HEARTBEAT_MS: "300", HARNESS_RELAY_ACK_WAIT_MS: "1500", HARNESS_RELAY_SPAWN_WAIT_MS: "1500" });
  const admin = await deviceLogin(ts);
  const alice = await createUserAndLogin(ts, admin.cookie, "alice@test.local", "alice-password-1");
  const alice2 = await deviceLogin(ts, "alice@test.local", "alice-password-1");
  const bob = await createUserAndLogin(ts, admin.cookie, "bob@test.local");
  A1 = ts.as(alice.accessToken).relay;
  A2 = ts.as(alice2.accessToken).relay;
  B = ts.as(bob.accessToken).relay;
});

afterAll(async () => {
  await ts.close();
});

async function expectError(p: Promise<unknown>, code: Code) {
  const err = await p.then(
    () => undefined,
    (e) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  expect((err as ConnectError).code).toBe(code);
  return err as ConnectError;
}

/** Reads a server stream in the background; next() waits for the next item (or fails after a timeout). */
class Reader<T> {
  private items: T[] = [];
  private waiters: (() => void)[] = [];
  error: unknown;
  done = false;
  readonly ctl = new AbortController();

  constructor(open: (signal: AbortSignal) => AsyncIterable<T>) {
    void (async () => {
      try {
        for await (const it of open(this.ctl.signal)) this.push(it);
      } catch (err) {
        this.error = err;
      } finally {
        this.done = true;
        this.wake();
      }
    })();
  }

  private push(it: T) {
    this.items.push(it);
    this.wake();
  }

  private wake() {
    const w = this.waiters;
    this.waiters = [];
    for (const f of w) f();
  }

  async next(timeoutMs = 3000): Promise<T> {
    const deadline = Date.now() + timeoutMs;
    while (!this.items.length) {
      if (this.done) throw new Error(`stream ended: ${String(this.error)}`);
      const left = deadline - Date.now();
      if (left <= 0) throw new Error("timed out waiting for a stream item");
      await new Promise<void>((r) => {
        const t = setTimeout(r, left);
        this.waiters.push(() => {
          clearTimeout(t);
          r();
        });
      });
    }
    return this.items.shift()!;
  }

  /** Next item matching `pred`, skipping others. */
  async until(pred: (t: T) => boolean, timeoutMs = 3000): Promise<T> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const it = await this.next(Math.max(1, deadline - Date.now()));
      if (pred(it)) return it;
    }
  }

  /** Items currently buffered (without waiting). */
  drain(): T[] {
    const out = this.items;
    this.items = [];
    return out;
  }

  close() {
    this.ctl.abort();
  }
}

const hostStream = (c: Relay, hostId: string) => new Reader<OpenHostStreamResponse>((signal) => c.openHostStream({ hostId }, { signal }));
const viewerStream = (c: Relay, sessionId: string, afterSeq = 0n, viewerId = "") =>
  new Reader<SubscribeSessionResponse>((signal) => c.subscribeSession({ sessionId, afterSeq, viewerId }, { signal }));

const isCommand = (r: OpenHostStreamResponse) => r.event.case === "command";

let instances = 0;
async function newHost(c: Relay, extra: { daemon?: boolean; allowedRoots?: string[] } = {}) {
  const r = await c.registerHost({ instanceId: `inst-${++instances}-${randomUUID()}`, clientVersion: "test", ...extra });
  return r.hostId;
}

async function newSession(c: Relay, hostId: string, id = randomUUID()) {
  const r = await c.registerSession({ hostId, session: { id, cwd: "/work/proj", title: "t", status: SessionStatus.IDLE } });
  return { id, epoch: r.epoch, ackedSeq: r.ackedSeq };
}

function ev(sessionId: string, seq: number, text = `e${seq}`): { uuid: string; event: SessionEvent } {
  return {
    uuid: `u-${sessionId}-${seq}`,
    event: create(SessionEventSchema, { seq: BigInt(seq), sessionId, event: { case: "notice", value: { text } } }),
  };
}

function delta(sessionId: string, afterSeq: number, text: string) {
  return {
    uuid: randomUUID(),
    event: create(SessionEventSchema, { seq: BigInt(afterSeq), sessionId, event: { case: "assistantDelta", value: { messageId: "m", delta: { case: "text", value: text } } } }),
  };
}

const range = (a: number, b: number) => Array.from({ length: b - a + 1 }, (_, i) => a + i);

describe("hosts", () => {
  it("registers idempotently per instance id and lists only own hosts", async () => {
    const instanceId = `inst-${randomUUID()}`;
    const h1 = await A1.registerHost({ instanceId, clientVersion: "1" });
    const h2 = await A1.registerHost({ instanceId, clientVersion: "2" });
    expect(h1.hostId).toMatch(/^hst_/);
    expect(h2.hostId).toBe(h1.hostId);
    expect(h1.heartbeatIntervalSeconds).toBeGreaterThan(0);
    // another device of the same user with the same instance id is another host
    expect((await A2.registerHost({ instanceId })).hostId).not.toBe(h1.hostId);

    const hosts = (await A1.listHosts({})).hosts;
    expect(hosts.map((h) => h.hostId)).toContain(h1.hostId);
    expect(hosts.find((h) => h.hostId === h1.hostId)!.online).toBe(true);
    expect((await B.listHosts({})).hosts.map((h) => h.hostId)).not.toContain(h1.hostId);
    await expectError(A1.registerHost({ instanceId: "" }), Code.InvalidArgument);
  });

  it("sends heartbeats on the host stream and closes a replaced stream", async () => {
    const hostId = await newHost(A1);
    const s1 = hostStream(A1, hostId);
    expect((await s1.next()).event.case).toBe("heartbeat");
    const s2 = hostStream(A1, hostId);
    await expect(s1.next()).rejects.toThrow(/stream ended/);
    expect((s1.error as ConnectError).code).toBe(Code.Aborted);
    expect((await s2.next()).event.case).toBe("heartbeat");
    s2.close();
  });

  it("leaves tunnels unimplemented", async () => {
    await expectError(A1.listTunnels({}), Code.Unimplemented);
    await expectError(A1.registerTunnel({}), Code.Unimplemented);
  });
});

describe("epochs", () => {
  it("increments the epoch on every registration and supersedes the previous owner", async () => {
    const hA = await newHost(A1);
    const hB = await newHost(A2);
    const streamA = hostStream(A1, hA);
    const s = await newSession(A1, hA);
    expect(s.epoch).toBe(1n);
    expect(s.ackedSeq).toBe(0n);
    expect((await A1.publishEvents({ hostId: hA, sessionId: s.id, epoch: 1n, events: [ev(s.id, 1), ev(s.id, 2)] })).ackedSeq).toBe(2n);

    // the desktop takes over: new epoch, acked_seq tells it where to continue
    const taken = await A2.registerSession({ hostId: hB, session: { id: s.id, cwd: "/work/proj" } });
    expect(taken.epoch).toBe(2n);
    expect(taken.ackedSeq).toBe(2n);
    const sup = await streamA.until((r) => r.event.case === "sessionSuperseded");
    expect(sup.event.value).toMatchObject({ sessionId: s.id, oldEpoch: 1n, newEpoch: 2n });

    // the old epoch can no longer publish, ack or unregister
    await expectError(A1.publishEvents({ hostId: hA, sessionId: s.id, epoch: 1n, events: [ev(s.id, 3)] }), Code.FailedPrecondition);
    await expectError(A1.unregisterSession({ hostId: hA, sessionId: s.id, epoch: 1n }), Code.FailedPrecondition);
    // a heartbeat with the old epoch reports the missed supersede
    const hb = await A1.heartbeat({ hostId: hA, sessions: [{ sessionId: s.id, epoch: 1n, status: SessionStatus.IDLE }] });
    expect(hb.superseded).toHaveLength(1);
    expect(hb.superseded[0]).toMatchObject({ sessionId: s.id, newEpoch: 2n });

    // the new owner continues from acked_seq + 1
    expect((await A2.publishEvents({ hostId: hB, sessionId: s.id, epoch: 2n, events: [ev(s.id, 3)] })).ackedSeq).toBe(3n);
    // re-registering on the same host bumps the epoch again without a supersede notice to itself
    expect((await A2.registerSession({ hostId: hB, session: { id: s.id } })).epoch).toBe(3n);
    await A2.unregisterSession({ hostId: hB, sessionId: s.id, epoch: 3n });
    streamA.close();
  });
});

describe("events", () => {
  it("deduplicates by seq and uuid and does not advance acked_seq past a gap", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    const pub = (events: ReturnType<typeof ev>[]) => A1.publishEvents({ hostId: h, sessionId: s.id, epoch: s.epoch, events });

    expect(await pub([ev(s.id, 1), ev(s.id, 2)])).toMatchObject({ ackedSeq: 2n, duplicates: 0 });
    expect(await pub([ev(s.id, 2), ev(s.id, 3)])).toMatchObject({ ackedSeq: 3n, duplicates: 1 });
    // a gap: stored, but acked_seq stays at 3
    expect(await pub([ev(s.id, 5), ev(s.id, 6)])).toMatchObject({ ackedSeq: 3n, duplicates: 0 });
    // resent gap events are duplicates
    expect(await pub([ev(s.id, 5)])).toMatchObject({ ackedSeq: 3n, duplicates: 1 });
    // filling the gap advances over everything stored
    expect(await pub([ev(s.id, 4)])).toMatchObject({ ackedSeq: 6n, duplicates: 0 });
    // same uuid under another seq is a duplicate (and does not fill seq 7)
    expect(await pub([{ uuid: `u-${s.id}-6`, event: ev(s.id, 7).event }])).toMatchObject({ ackedSeq: 6n, duplicates: 1 });
    // deltas are deduplicated by uuid and never stored
    const d = delta(s.id, 6, "x");
    expect(await pub([d as never, d as never])).toMatchObject({ ackedSeq: 6n, duplicates: 1 });

    // the replay is the contiguous history in order
    const v = viewerStream(A1, s.id);
    const seqs: number[] = [];
    for (;;) {
      const r = await v.next();
      if (r.event.case === "flushComplete") {
        expect(r.event.value.lastSeq).toBe(6n);
        break;
      }
      if (r.event.case === "sessionEvent") seqs.push(Number(r.event.value.seq));
    }
    expect(seqs).toEqual(range(1, 6));
    v.close();

    await expectError(pub([{ uuid: "x", event: create(SessionEventSchema, { seq: 0n, event: { case: "notice", value: {} } }) }]), Code.InvalidArgument);
    await expectError(pub([{ uuid: "y", event: create(SessionEventSchema, { seq: 9n, sessionId: randomUUID(), event: { case: "notice", value: {} } }) }]), Code.InvalidArgument);
  });

  it("FlushGate: history, FlushComplete, then live without gaps or duplicates while the host publishes", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    const pub = (events: { uuid: string; event: SessionEvent }[]) => A1.publishEvents({ hostId: h, sessionId: s.id, epoch: s.epoch, events });
    await pub(range(1, 40).map((n) => ev(s.id, n)));

    // keep publishing (with deltas and some resends) while viewers subscribe
    const publishing = (async () => {
      for (let n = 41; n <= 200; n += 8) {
        const batch = range(n, n + 7).flatMap((k) => [ev(s.id, k), delta(s.id, k, `d${k}`)]);
        await pub([ev(s.id, n - 1), ...batch]); // the overlap is skipped as a duplicate
      }
    })();
    const viewers = [0n, 10n, 39n].map((after) => ({ after, r: viewerStream(A1, s.id, after) }));
    await publishing;

    for (const { after, r } of viewers) {
      const durable: number[] = [];
      let flushed = false;
      let flushAt = -1;
      for (;;) {
        const m = await r.next();
        if (m.event.case === "flushComplete") {
          expect(flushed).toBe(false);
          flushed = true;
          flushAt = Number(m.event.value.lastSeq);
          expect(flushAt).toBe(durable.length ? durable[durable.length - 1] : Number(after));
          continue;
        }
        if (m.event.case !== "sessionEvent") continue;
        const e = m.event.value;
        if (e.event.case === "assistantDelta") {
          // deltas only after FlushComplete, right after their durable event
          expect(flushed).toBe(true);
          expect(Number(e.seq)).toBe(durable[durable.length - 1]);
          continue;
        }
        durable.push(Number(e.seq));
        if (Number(e.seq) === 200) break;
      }
      expect(flushed).toBe(true);
      expect(durable).toEqual(range(Number(after) + 1, 200));
      r.close();
    }
  });
});

describe("commands", () => {
  it("queues for an offline host, redelivers until acked, and deduplicates by idempotency key", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    const msg = { sessionId: s.id, uuid: randomUUID(), content: [{ block: { case: "text" as const, value: { text: "hi" } } }] };
    const sent = await A1.sendUserMessage({ request: msg, options: { idempotencyKey: `k-${s.id}`, frontend: "web" } });
    expect(sent.result!.commandId).toMatch(/^cmd_/);
    expect(sent.result!.delivery).toBe(DeliveryState.QUEUED);
    const again = await A1.sendUserMessage({ request: msg, options: { idempotencyKey: `k-${s.id}` } });
    expect(again.result!.commandId).toBe(sent.result!.commandId);

    // the host comes online: the queued command is delivered with the current epoch
    let st = hostStream(A1, h);
    const c1 = (await st.until(isCommand)).event.value as never as { commandId: string; epoch: bigint; sessionId: string; senderFrontend: string; payload: { case: string } };
    expect(c1).toMatchObject({ commandId: sent.result!.commandId, epoch: s.epoch, sessionId: s.id, senderFrontend: "web" });
    expect(c1.payload.case).toBe("sendUserMessage");
    st.close();

    // not acked: redelivered on the next stream
    st = hostStream(A1, h);
    expect(((await st.until(isCommand)).event.value as { commandId: string }).commandId).toBe(c1.commandId);
    await A1.ackCommands({ hostId: h, acks: [{ commandId: c1.commandId, outcome: CommandAck_Outcome.OK }] });
    // a second ack is ignored (first one wins)
    await A1.ackCommands({ hostId: h, acks: [{ commandId: c1.commandId, outcome: CommandAck_Outcome.FAILED }] });
    st.close();

    st = hostStream(A1, h);
    expect((await st.next()).event.case).toBe("heartbeat"); // nothing to redeliver
    // live commands arrive on the open stream; wait_for_ack returns the ack
    const held = A1.interrupt({ request: { sessionId: s.id }, options: { waitForAck: true } });
    const c2 = (await st.until(isCommand)).event.value as { commandId: string; payload: { case: string } };
    expect(c2.payload.case).toBe("interrupt");
    await A1.ackCommands({ hostId: h, acks: [{ commandId: c2.commandId, outcome: CommandAck_Outcome.OK }] });
    const r = await held;
    expect(r.result!.delivery).toBe(DeliveryState.ACKED);
    expect(r.result!.ack!.outcome).toBe(CommandAck_Outcome.OK);
    // the idempotent retry now reports the ack
    expect((await A1.sendUserMessage({ request: msg, options: { idempotencyKey: `k-${s.id}` } })).result!.delivery).toBe(DeliveryState.ACKED);
    st.close();
  });

  it("carries a session note: waits for an offline host far past a command's limit, and refuses a closed session", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    const note = { sessionId: s.id, note: { id: `note_${s.id}`, kind: SessionNote_Kind.MESSAGE, fromId: "parent", text: "hello there" } };
    // The caller's ttl neither shortens nor extends it.
    const sent = await A2.sendSessionNote({ request: note, options: { idempotencyKey: note.note.id, ttlSeconds: 1 } });
    expect(sent.result!.delivery).toBe(DeliveryState.QUEUED);
    const again = await A2.sendSessionNote({ request: note, options: { idempotencyKey: note.note.id } });
    expect(again.result!.commandId).toBe(sent.result!.commandId);
    await new Promise((r) => setTimeout(r, 1100));

    // Another user cannot reach it.
    await expectError(B.sendSessionNote({ request: note }), Code.NotFound);

    // The host reconnects: it arrives as its own payload, not as a user message.
    const st = hostStream(A1, h);
    const c = (await st.until(isCommand)).event.value as { commandId: string; expiresAt: string; payload: { case: string; value: { note: { text: string } } } };
    expect(c.payload.case).toBe("deliverSessionNote");
    expect(c.payload.value.note.text).toBe("hello there");
    expect(Date.parse(c.expiresAt) - Date.now()).toBeGreaterThan(25 * 24 * 3600 * 1000);
    await A1.ackCommands({ hostId: h, acks: [{ commandId: c.commandId, outcome: CommandAck_Outcome.OK }] });
    st.close();

    // Closed on its machine: the caller leaves it in the user's document instead.
    await A1.unregisterSession({ hostId: h, sessionId: s.id, epoch: s.epoch });
    await expectError(A2.sendSessionNote({ request: { ...note, note: { ...note.note, id: "note_2" } } }), Code.FailedPrecondition);
  });

  it("starts a proposal from another device: held for the host's ack", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    const st = hostStream(A1, h);
    const held = A2.resolveSessionProposal({ request: { sessionId: s.id, proposalId: "prop_1", action: ResolveSessionProposalRequest_Action.START } });
    const c = (await st.until(isCommand)).event.value as { commandId: string; payload: { case: string; value: { proposalId: string } } };
    expect(c.payload.case).toBe("resolveSessionProposal");
    expect(c.payload.value.proposalId).toBe("prop_1");
    await A1.ackCommands({ hostId: h, acks: [{ commandId: c.commandId, outcome: CommandAck_Outcome.OK }] });
    const r = await held;
    expect(r.result!.delivery).toBe(DeliveryState.ACKED);
    st.close();
  });

  it("moves queued commands to the new owner after a takeover", async () => {
    const hA = await newHost(A1);
    const hB = await newHost(A2);
    const s = await newSession(A1, hA);
    const q = await A1.setModel({ request: { sessionId: s.id, model: "m2" } });
    const stB = hostStream(A2, hB);
    await A2.registerSession({ hostId: hB, session: { id: s.id } });
    const c = (await stB.until(isCommand)).event.value as { commandId: string; epoch: bigint };
    expect(c.commandId).toBe(q.result!.commandId);
    expect(c.epoch).toBe(2n);
    // the old owner can no longer ack it
    await A1.ackCommands({ hostId: hA, acks: [{ commandId: c.commandId, outcome: CommandAck_Outcome.OK }] });
    const r = await A1.setModel({ request: { sessionId: s.id, model: "m2" }, options: { idempotencyKey: undefined } });
    expect(r.result!.delivery).toBe(DeliveryState.QUEUED);
    stB.close();
  });

  it("expires commands that were not acked in time", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    const r = await A1.setEffort({ request: { sessionId: s.id }, options: { ttlSeconds: 1, waitForAck: true } });
    expect(r.result!.delivery).toBe(DeliveryState.EXPIRED);
    const awake = await A1.setKeepAwake({ request: { sessionId: s.id, enabled: true }, options: { ttlSeconds: 1, waitForAck: true } });
    expect(awake.result!.delivery).toBe(DeliveryState.EXPIRED);
    // never delivered once expired
    const st = hostStream(A1, h);
    expect((await st.next()).event.case).toBe("heartbeat");
    st.close();
    await expectError(A1.setEffort({ request: { sessionId: s.id }, options: { ttlSeconds: -1 } }), Code.InvalidArgument);
  });

  it("holds permission answers for the host ack (first answer wins)", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    const st = hostStream(A1, h);
    const answer = A1.respondToPermission({ request: { sessionId: s.id, requestId: "perm_1", decision: 1 } });
    const c = (await st.until(isCommand)).event.value as { commandId: string };
    await A1.ackCommands({ hostId: h, acks: [{ commandId: c.commandId, outcome: CommandAck_Outcome.ALREADY_RESOLVED }] });
    const r = await answer;
    expect(r.result!.delivery).toBe(DeliveryState.ACKED);
    expect(r.result!.ack!.outcome).toBe(CommandAck_Outcome.ALREADY_RESOLVED);
    // host offline: the call returns QUEUED after the wait
    st.close();
    const late = await A1.answerQuestion({ request: { sessionId: s.id, requestId: "q_1" } });
    expect(late.result!.delivery).toBe(DeliveryState.QUEUED);
  });

  it("answers queries with the host result, UNAVAILABLE when the host is silent", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    await expectError(A1.suggestFiles({ request: { sessionId: s.id, query: "ma" } }), Code.Unavailable);

    const st = hostStream(A1, h);
    const pending = A1.suggestFiles({ request: { sessionId: s.id, query: "ma" } });
    const c = (await st.until((r) => isCommand(r) && (r.event.value as { payload: { case: string } }).payload.case === "suggestFiles")).event.value as {
      commandId: string;
    };
    await A1.ackCommands({
      hostId: h,
      acks: [{ commandId: c.commandId, outcome: CommandAck_Outcome.OK, result: { case: "suggestFiles", value: { paths: ["main.go"] } } }],
    });
    expect((await pending).response!.paths).toEqual(["main.go"]);

    const failing = A1.listTasks({ request: { sessionId: s.id } });
    const c2 = (await st.until((r) => isCommand(r) && (r.event.value as { payload: { case: string } }).payload.case === "listTasks")).event.value as {
      commandId: string;
    };
    await A1.ackCommands({ hostId: h, acks: [{ commandId: c2.commandId, outcome: CommandAck_Outcome.FAILED, errorMessage: "boom" }] });
    expect((await expectError(failing, Code.FailedPrecondition)).message).toContain("boom");

    // the Agents pane of a remote-controlled session
    const stopping = A1.stopSubagent({ request: { sessionId: s.id, agentId: "a1" } });
    const c3 = (await st.until((r) => isCommand(r) && (r.event.value as { payload: { case: string } }).payload.case === "stopSubagent")).event.value as { commandId: string };
    await A1.ackCommands({ hostId: h, acks: [{ commandId: c3.commandId, outcome: CommandAck_Outcome.OK, result: { case: "stopSubagent", value: { agent: { id: "a1" } } } }] });
    expect((await stopping).response!.agent!.id).toBe("a1");
    const reading = A1.getSubagentTranscript({ request: { sessionId: s.id, agentId: "a1" } });
    const c4 = (await st.until((r) => isCommand(r) && (r.event.value as { payload: { case: string } }).payload.case === "getSubagentTranscript")).event.value as { commandId: string };
    await A1.ackCommands({ hostId: h, acks: [{ commandId: c4.commandId, outcome: CommandAck_Outcome.OK, result: { case: "getSubagentTranscript", value: { items: [{ text: "words" }] } } }] });
    expect((await reading).response!.items[0]!.text).toBe("words");
    st.close();
  });

  it("rejects bypassPermissions remotely", async () => {
    const h = await newHost(A1, { daemon: true, allowedRoots: ["/work"] });
    const s = await newSession(A1, h);
    await expectError(A1.setPermissionMode({ request: { sessionId: s.id, mode: PermissionMode.BYPASS_PERMISSIONS } }), Code.PermissionDenied);
    await expectError(
      A1.spawnSession({ hostId: h, spawn: { create: { cwd: "/work/x", permissionMode: PermissionMode.BYPASS_PERMISSIONS } } }),
      Code.PermissionDenied,
    );
    // other modes go through
    expect((await A1.setPermissionMode({ request: { sessionId: s.id, mode: PermissionMode.ACCEPT_EDITS } })).result!.delivery).toBe(DeliveryState.QUEUED);
  });

  it("refuses commands for unregistered sessions", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    await A1.unregisterSession({ hostId: h, sessionId: s.id, epoch: s.epoch });
    await expectError(A1.interrupt({ request: { sessionId: s.id } }), Code.FailedPrecondition);
    await expectError(A1.interrupt({ request: { sessionId: "" } }), Code.InvalidArgument);
  });
});

describe("spawn", () => {
  it("spawns only on daemon hosts under allowed roots", async () => {
    const plain = await newHost(A1);
    await expectError(A1.spawnSession({ hostId: plain, spawn: { create: { cwd: "/work/a" } } }), Code.FailedPrecondition);

    const d = await newHost(A1, { daemon: true, allowedRoots: ["/work"] });
    await expectError(A1.spawnSession({ hostId: d, spawn: { create: { cwd: "/etc" } } }), Code.PermissionDenied);
    await expectError(A1.spawnSession({ hostId: d, spawn: { create: { cwd: "/work/../etc" } } }), Code.PermissionDenied);
    await expectError(A1.spawnSession({ hostId: d, spawn: { create: { cwd: "relative" } } }), Code.InvalidArgument);
    await expectError(B.spawnSession({ hostId: d, spawn: { create: { cwd: "/work/a" } } }), Code.NotFound);

    const st = hostStream(A1, d);
    await st.next(); // heartbeat or nothing yet; make sure the stream is open
    const sid = randomUUID();
    const pending = A1.spawnSession({ hostId: d, spawn: { create: { cwd: "/work/a" } } });
    const c = (await st.until(isCommand)).event.value as { commandId: string; sessionId: string; epoch: bigint; payload: { case: string } };
    expect(c).toMatchObject({ sessionId: "", epoch: 0n });
    expect(c.payload.case).toBe("spawnSession");
    await A1.ackCommands({
      hostId: d,
      acks: [{ commandId: c.commandId, outcome: CommandAck_Outcome.OK, result: { case: "spawnSession", value: { session: { id: sid, cwd: "/work/a" } } } }],
    });
    expect((await pending).session!.id).toBe(sid);
    st.close();
  });

  it("checks roots by path segments", () => {
    expect(isUnderRoot("/work/a/b", "/work")).toBe(true);
    expect(isUnderRoot("/work", "/work/")).toBe(true);
    expect(isUnderRoot("/workshop", "/work")).toBe(false);
    expect(isUnderRoot("C:\\src\\x", "C:\\src")).toBe(true);
    expect(isUnderRoot("C:\\srcx", "C:\\src")).toBe(false);
  });
});

describe("presence and host state", () => {
  it("tracks viewers and focus, and reports the owning host", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    const v = viewerStream(A1, s.id, 0n, "tab-1");
    const host = await v.until((r) => r.event.case === "hostState");
    expect(host.event.value).toMatchObject({ hostId: h, online: true, epoch: 1n });
    const p1 = await v.until((r) => r.event.case === "presence" && (r.event.value as { viewers: unknown[] }).viewers.length === 1);
    expect((p1.event.value as { viewers: { viewerId: string; state: PresenceState }[] }).viewers[0]).toMatchObject({ viewerId: "tab-1", state: PresenceState.UNFOCUSED });

    await A1.updatePresence({ sessionId: s.id, viewerId: "tab-1", state: PresenceState.FOCUSED });
    const focused = (r: SubscribeSessionResponse) =>
      r.event.case === "presence" && r.event.value.viewers.length === 1 && r.event.value.viewers[0]!.state === PresenceState.FOCUSED;
    expect((await v.until(focused)).event.case).toBe("presence");

    // a takeover by another host shows up as a new host state
    const h2 = await newHost(A2);
    await A2.registerSession({ hostId: h2, session: { id: s.id } });
    const hs = await v.until((r) => r.event.case === "hostState" && (r.event.value as { epoch: bigint }).epoch === 2n);
    expect((hs.event.value as { hostId: string }).hostId).toBe(h2);
    expect((await v.until((r) => r.event.case === "heartbeat", 2000)).event.case).toBe("heartbeat");
    v.close();
  });

  it("lists sessions of offline hosts as waiting for connection", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    // age the host past three heartbeats (300 ms each in this suite)
    await new Promise((r) => setTimeout(r, 1000));
    const host = (await A1.listHosts({})).hosts.find((x) => x.hostId === h)!;
    expect(host.online).toBe(false);
    expect(host.sessions.find((x) => x.id === s.id)!.status).toBe(SessionStatus.WAITING_FOR_CONNECTION);
    await A1.heartbeat({ hostId: h, sessions: [{ sessionId: s.id, epoch: s.epoch, status: SessionStatus.WORKING }] });
    const back = (await A1.listHosts({})).hosts.find((x) => x.hostId === h)!;
    expect(back.online).toBe(true);
    expect(back.sessions.find((x) => x.id === s.id)!.status).toBe(SessionStatus.WORKING);
  });
});

describe("access", () => {
  it("hides other users' hosts and sessions behind NOT_FOUND", async () => {
    const h = await newHost(A1);
    const s = await newSession(A1, h);
    await A1.publishEvents({ hostId: h, sessionId: s.id, epoch: s.epoch, events: [ev(s.id, 1)] });

    const v = viewerStream(B, s.id);
    await expect(v.next()).rejects.toThrow(/stream ended/);
    expect((v.error as ConnectError).code).toBe(Code.NotFound);
    await expectError(B.sendUserMessage({ request: { sessionId: s.id, content: [] } }), Code.NotFound);
    await expectError(B.suggestFiles({ request: { sessionId: s.id } }), Code.NotFound);
    await expectError(B.updatePresence({ sessionId: s.id, viewerId: "x", state: PresenceState.FOCUSED }), Code.NotFound);
    // host-side calls on someone else's host or session
    const bh = await newHost(B);
    await expectError(B.registerSession({ hostId: bh, session: { id: s.id } }), Code.NotFound);
    await expectError(B.publishEvents({ hostId: h, sessionId: s.id, epoch: s.epoch, events: [] }), Code.NotFound);
    const bs = hostStream(B, h);
    await expect(bs.next()).rejects.toThrow(/stream ended/);
    expect((bs.error as ConnectError).code).toBe(Code.NotFound);
    // a second device of the same user cannot act as this host either
    await expectError(A2.publishEvents({ hostId: h, sessionId: s.id, epoch: s.epoch, events: [] }), Code.NotFound);
    expect((await B.listHosts({})).hosts.flatMap((x) => x.sessions.map((y) => y.id))).not.toContain(s.id);
  });
});
