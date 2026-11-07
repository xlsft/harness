/**
 * InboxService and the fold: groups from relay events, answers clearing requests, read state across devices, paging,
 * live changes, project runs, and that nobody but the user sees an item.
 */
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { randomUUID } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { SessionEventSchema, SessionStatus, TurnFinished_Result } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { InboxGroup, InboxItemSchema, InboxRequest_Kind, type WatchInboxResponse } from "../src/gen/harness/agent/v1/inbox_pb.ts";
import { ProjectRunState } from "../src/gen/harness/agent/v1/project_runs_pb.ts";
import { SubagentState } from "../src/gen/harness/agent/v1/subagents_pb.ts";
import { foldEvent, groupOf, inputSummary, type FoldScratch } from "../src/relay/inbox-fold.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

type Clients = ReturnType<TestServer["as"]>;

let ts: TestServer;
let A1: Clients; // alice, laptop
let A2: Clients; // alice, second device
let B: Clients; // bob

beforeAll(async () => {
  ts = await startTestServer({ HARNESS_RELAY_HEARTBEAT_MS: "300" });
  const admin = await deviceLogin(ts);
  const alice = await createUserAndLogin(ts, admin.cookie, "alice-inbox@test.local", "alice-password-1");
  const alice2 = await deviceLogin(ts, "alice-inbox@test.local", "alice-password-1");
  const bob = await createUserAndLogin(ts, admin.cookie, "bob-inbox@test.local");
  A1 = ts.as(alice.accessToken);
  A2 = ts.as(alice2.accessToken);
  B = ts.as(bob.accessToken);
});

afterAll(async () => {
  await ts.close();
});

type Ev = NonNullable<MessageInitShape<typeof SessionEventSchema>["event"]>;
const at = (i: number) => new Date(Date.UTC(2026, 10, 7, 12, 0, i)).toISOString();

function evs(sessionId: string, from: number, events: Ev[]) {
  return events.map((event, i) => ({
    uuid: `u-${sessionId}-${from + i}`,
    event: create(SessionEventSchema, { seq: BigInt(from + i), sessionId, timestamp: at(from + i), event }),
  }));
}

async function session(c: Clients, title = "a session") {
  const { hostId } = await c.relay.registerHost({ instanceId: `inbox-${randomUUID()}`, clientVersion: "test" });
  const id = randomUUID();
  const { epoch } = await c.relay.registerSession({ hostId, session: { id, cwd: "/work/proj", title, status: SessionStatus.IDLE } });
  let seq = 1;
  return {
    id,
    hostId,
    publish: async (...events: Ev[]) => {
      const batch = evs(id, seq, events);
      seq += events.length;
      await c.relay.publishEvents({ hostId, sessionId: id, epoch, events: batch });
    },
    close: () => c.relay.unregisterSession({ hostId, sessionId: id, epoch }),
  };
}

async function list(c: Clients, group: InboxGroup, pageSize = 50, pageToken = "") {
  return (await c.inbox.listInbox({ request: { group, pageSize, pageToken } })).response!;
}

const permission = (requestId: string): Ev => ({ case: "permissionRequest", value: { requestId, toolName: "Bash", inputJson: JSON.stringify({ command: "rm -rf build" }), suggestedRules: ["Bash(rm *)"] } });
const finished = (text: string): Ev[] => [
  { case: "turnStarted", value: {} },
  { case: "assistantMessage", value: { content: [{ block: { case: "text", value: { text } } }] } },
  { case: "turnFinished", value: { result: TurnFinished_Result.SUCCESS, usage: { inputTokens: 100n, outputTokens: 20n }, costUsd: 0.01 } },
];

describe("fold", () => {
  it("groups by what the session needs", () => {
    const item = create(InboxItemSchema, { sessionId: "s" });
    const s: FoldScratch = {};
    const fold = (e: Ev, i = 1) => foldEvent(item, s, create(SessionEventSchema, { timestamp: at(i), event: e }));
    expect(groupOf(item, s)).toBe(InboxGroup.UNSPECIFIED);
    fold({ case: "turnStarted", value: {} });
    expect(groupOf(item, s)).toBe(InboxGroup.RUNNING);
    fold(permission("p1"), 2);
    expect(groupOf(item, s)).toBe(InboxGroup.NEEDS_INPUT);
    expect(item.requests[0]!.summary).toBe("rm -rf build");
    expect(item.attentionAt).toBe(at(2));
    fold({ case: "permissionResolved", value: { requestId: "p1" } });
    expect(groupOf(item, s)).toBe(InboxGroup.RUNNING);
    fold({ case: "turnFinished", value: { result: TurnFinished_Result.ERROR_DURING_EXECUTION, errorMessage: "boom" } }, 3);
    expect(groupOf(item, s)).toBe(InboxGroup.FAILED);
    expect(item.error).toBe("boom");
    for (const e of finished("All done.")) fold(e, 4);
    expect(groupOf(item, s)).toBe(InboxGroup.FINISHED);
    expect(item.report).toBe("All done.");
    expect(item.inputTokens).toBe(100n);
    fold({ case: "subagentUpdated", value: { agent: { id: "a1", name: "explore", state: SubagentState.RUNNING } } });
    expect(groupOf(item, s)).toBe(InboxGroup.RUNNING);
    fold({ case: "subagentUpdated", value: { agent: { id: "a1", name: "explore", state: SubagentState.FINISHED, report: "found it" } } });
    expect(groupOf(item, s)).toBe(InboxGroup.FINISHED);
    fold({ case: "question", value: { requestId: "q1", items: [{ question: "Which one?", header: "Pick", options: [{ label: "A" }, { label: "B" }] }] } });
    expect(item.requests[0]!.questions[0]!.options).toEqual(["A", "B"]);
    fold({ case: "sessionEnded", value: { reason: "exit" } });
    expect(item.requests).toEqual([]);
    expect(item.open).toBe(false);
  });

  it("summarises tool input", () => {
    expect(inputSummary('{"file_path":"/a/b.go","content":"x"}')).toBe("/a/b.go");
    expect(inputSummary("not json")).toBe("not json");
    expect(inputSummary(JSON.stringify({ command: "x".repeat(1000) })).length).toBeLessThanOrEqual(400);
  });

  it("asks the user about a paused project run", () => {
    const item = create(InboxItemSchema, { sessionId: "c" });
    const s: FoldScratch = {};
    foldEvent(item, s, create(SessionEventSchema, { event: { case: "projectRunUpdated", value: { run: { id: "prun_1", state: ProjectRunState.PAUSED, reason: "Budget reached" } } } }));
    expect(groupOf(item, s)).toBe(InboxGroup.NEEDS_INPUT);
    expect(item.requests[0]!.kind).toBe(InboxRequest_Kind.PROJECT_RUN);
    foldEvent(item, s, create(SessionEventSchema, { event: { case: "projectRunUpdated", value: { run: { id: "prun_1", state: ProjectRunState.RUNNING } } } }));
    expect(item.requests).toEqual([]);
  });
});

describe("InboxService", () => {
  it("lists sessions by group, answers clear them, and only the owner sees them", async () => {
    const s = await session(A1, "needs me");
    await s.publish({ case: "turnStarted", value: {} }, permission("req-1"));
    const needs = await list(A1, InboxGroup.NEEDS_INPUT);
    const item = needs.items.find((i) => i.sessionId === s.id)!;
    expect(item.title).toBe("needs me");
    expect(item.requests[0]!.requestId).toBe("req-1");
    expect(item.unread).toBe(true);
    expect(item.deviceOnline).toBe(true);
    expect(needs.counts.find((c) => c.group === InboxGroup.NEEDS_INPUT)!.total).toBeGreaterThanOrEqual(1);

    // Bob sees nothing of Alice's, also not with her session id.
    expect((await list(B, InboxGroup.NEEDS_INPUT)).items.map((i) => i.sessionId)).not.toContain(s.id);
    await B.inbox.markInboxRead({ request: { sessionIds: [s.id], read: true } });
    expect((await list(A1, InboxGroup.NEEDS_INPUT)).items.find((i) => i.sessionId === s.id)!.unread).toBe(true);

    await s.publish({ case: "permissionResolved", value: { requestId: "req-1" } });
    expect((await list(A1, InboxGroup.NEEDS_INPUT)).items.map((i) => i.sessionId)).not.toContain(s.id);
    expect((await list(A1, InboxGroup.RUNNING)).items.map((i) => i.sessionId)).toContain(s.id);
  });

  it("keeps the read state per user across devices, and closed sessions until read", async () => {
    const s = await session(A1, "finished one");
    await s.publish(...finished("Report text"));
    let it = (await list(A2, InboxGroup.FINISHED)).items.find((i) => i.sessionId === s.id)!;
    expect(it.report).toBe("Report text");
    expect(it.unread).toBe(true);
    await A2.inbox.markInboxRead({ request: { sessionIds: [s.id], read: true } });
    it = (await list(A1, InboxGroup.FINISHED)).items.find((i) => i.sessionId === s.id)!;
    expect(it.unread).toBe(false);
    await A1.inbox.markInboxRead({ request: { sessionIds: [s.id], read: false } });
    expect((await list(A1, InboxGroup.FINISHED)).items.find((i) => i.sessionId === s.id)!.unread).toBe(true);

    await s.close();
    it = (await list(A1, InboxGroup.FINISHED)).items.find((i) => i.sessionId === s.id)!;
    expect(it.open).toBe(false);
    await A1.inbox.markInboxRead({ request: { sessionIds: [s.id], read: true } });
    expect((await list(A1, InboxGroup.FINISHED)).items.map((i) => i.sessionId)).not.toContain(s.id);
  });

  it("pages through a group", async () => {
    const ids: string[] = [];
    for (let i = 0; i < 7; i++) {
      const s = await session(A1, `failing ${i}`);
      await s.publish({ case: "turnStarted", value: {} }, { case: "turnFinished", value: { result: TurnFinished_Result.ERROR_MAX_TURNS, errorMessage: `e${i}` } });
      ids.push(s.id);
    }
    const seen: string[] = [];
    let token = "";
    for (let pages = 0; pages < 10; pages++) {
      const page = await list(A1, InboxGroup.FAILED, 3, token);
      expect(page.items.length).toBeLessThanOrEqual(3);
      seen.push(...page.items.map((i) => i.sessionId));
      token = page.nextPageToken;
      if (!token) break;
    }
    for (const id of ids) expect(seen).toContain(id);
    expect(new Set(seen).size).toBe(seen.length);
    await expect(A1.inbox.listInbox({ request: { group: InboxGroup.UNSPECIFIED } })).rejects.toThrow();
  });

  it("streams changes to the user's other devices, not to others", async () => {
    const ctl = new AbortController();
    const got: WatchInboxResponse[] = [];
    const other: WatchInboxResponse[] = [];
    const ctl2 = new AbortController();
    void (async () => {
      try {
        for await (const r of A2.inbox.watchInbox({}, { signal: ctl.signal })) got.push(r.response!);
      } catch {
        /* aborted */
      }
    })();
    void (async () => {
      try {
        for await (const r of B.inbox.watchInbox({}, { signal: ctl2.signal })) other.push(r.response!);
      } catch {
        /* aborted */
      }
    })();
    const wait = async (pred: () => boolean) => {
      for (let i = 0; i < 100 && !pred(); i++) await new Promise((r) => setTimeout(r, 30));
      expect(pred()).toBe(true);
    };
    await wait(() => got.some((r) => r.event.case === "caughtUp") && other.some((r) => r.event.case === "caughtUp"));
    const s = await session(A1, "live");
    await s.publish(permission("live-1"));
    await wait(() => got.some((r) => r.event.case === "change" && r.event.value.item?.sessionId === s.id && r.event.value.item.group === InboxGroup.NEEDS_INPUT));
    await s.publish({ case: "permissionResolved", value: { requestId: "live-1" } });
    await wait(() => got.some((r) => r.event.case === "change" && r.event.value.removedSessionId === s.id));
    expect(other.some((r) => r.event.case === "change")).toBe(false);
    ctl.abort();
    ctl2.abort();
  });

  it("lists project runs of coordinator sessions", async () => {
    const s = await session(A1, "coordinator");
    await s.publish({ case: "projectRunUpdated", value: { run: { id: "prun_x", goal: "ship it", state: ProjectRunState.RUNNING, coordinatorSessionId: s.id } } });
    const runs = (await A1.inbox.listInbox({ request: { projectRunsOnly: true } })).response!;
    expect(runs.items.find((i) => i.sessionId === s.id)?.projectRun?.goal).toBe("ship it");
    expect((await B.inbox.listInbox({ request: { projectRunsOnly: true } })).response!.items.map((i) => i.sessionId)).not.toContain(s.id);
  });
});
