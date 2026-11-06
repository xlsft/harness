/** The webhook of a scheduled task: a post becomes a trigger document in the owner's feed, for the machine that runs it. */
import { createHash } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { DocumentKind, type Document } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let A: TestServer["sync"];
let B: TestServer["sync"];

beforeAll(async () => {
  ts = await startTestServer();
  const admin = await deviceLogin(ts);
  const alice = await createUserAndLogin(ts, admin.cookie, "sched-alice@test.local");
  const bob = await createUserAndLogin(ts, admin.cookie, "sched-bob@test.local");
  A = ts.as(alice.accessToken).sync;
  B = ts.as(bob.accessToken).sync;
});

afterAll(async () => {
  await ts.close();
});

const hash = (s: string) => createHash("sha256").update(s).digest("hex");

async function schedule(key: string, secret: string, extra: Record<string, unknown> = {}): Promise<Document> {
  const task = { id: key, prompt: "go", deviceId: "dev_1", trigger: { kind: "webhook" }, webhookSecretSha256: hash(secret), ...extra };
  const r = await A.putDocument({ ref: { scope: Scope.USER, kind: DocumentKind.SCHEDULE, key }, content: JSON.stringify(task) });
  if (r.result.case !== "document") throw new Error("expected a document");
  return r.result.value;
}

const post = (token: string, body = "{}") => fetch(`${ts.url}/api/schedules/webhook/${token}`, { method: "POST", body });

async function triggers(c: TestServer["sync"]) {
  const r = await c.listChanges({ sinceVersion: 0n, kinds: [DocumentKind.SCHEDULE_TRIGGER] });
  return r.changes.flatMap((ch) => (ch.change.case === "document" ? [ch.change.value] : []));
}

describe("schedule webhook", () => {
  it("turns a post into a trigger document of the owner, for the machine that runs it", async () => {
    const doc = await schedule("sch_ok", "s3cret");
    const res = await post(`${doc.id}.s3cret`, '{"event":"push"}');
    expect(res.status).toBe(202);
    const list = await triggers(A);
    expect(list).toHaveLength(1);
    expect(JSON.parse(list[0]!.content)).toMatchObject({ scheduleId: "sch_ok", deviceId: "dev_1", source: "webhook", payload: '{"event":"push"}' });
    expect(await triggers(B)).toHaveLength(0);
    // The machine that ran it deletes it.
    await A.deleteDocument({ id: list[0]!.id, baseVersion: list[0]!.version });
    expect(await triggers(A)).toHaveLength(0);
  });

  it("answers the same for a wrong secret, an unknown id and another kind of schedule", async () => {
    const doc = await schedule("sch_wrong", "right");
    expect((await post(`${doc.id}.wrong`)).status).toBe(404);
    expect((await post("doc_none.right")).status).toBe(404);
    expect((await post("no-dot")).status).toBe(404);
    const timed = await schedule("sch_cron", "right", { trigger: { kind: "cron" } });
    expect((await post(`${timed.id}.right`)).status).toBe(404);
    expect(await triggers(A)).toHaveLength(0);
  });

  it("refuses while the schedule is paused, and bodies that are too large", async () => {
    const paused = await schedule("sch_paused", "s", { paused: true });
    expect((await post(`${paused.id}.s`)).status).toBe(409);
    const doc = await schedule("sch_big", "s");
    expect((await post(`${doc.id}.s`, "x".repeat((64 << 10) + 1))).status).toBe(413);
  });

  it("limits how fast a schedule can be fired", async () => {
    const doc = await schedule("sch_rate", "s");
    const codes: number[] = [];
    for (let i = 0; i < 40; i++) {
      const res = await post(`${doc.id}.s`);
      codes.push(res.status);
      if (res.status === 429) {
        expect(res.headers.get("retry-after")).toBeTruthy();
        break;
      }
    }
    expect(codes.at(-1)).toBe(429);
    for (const t of await triggers(A)) await A.deleteDocument({ id: t.id, baseVersion: t.version });
  });

  it("keeps schedule documents at user scope", async () => {
    await expect(A.putDocument({ ref: { scope: Scope.ORG, kind: DocumentKind.SCHEDULE, key: "x" }, content: "{}" })).rejects.toThrow();
    await expect(A.putDocument({ ref: { scope: Scope.USER, kind: DocumentKind.SCHEDULE, key: "bad" }, content: "not json" })).rejects.toThrow();
  });
});
