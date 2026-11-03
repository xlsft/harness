/** SyncService: versioned documents (CAS, tombstones, feed, watch) and session sync. */
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { SessionInfoSchema, SessionStatus } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { ErrorInfoSchema, ErrorType } from "../src/gen/harness/common/v1/errors_pb.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { DocumentKind, type Document, type PutDocumentRequestSchema, type WatchChangesResponse } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { purgeTombstones } from "../src/services/sync.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let alice: Awaited<ReturnType<typeof deviceLogin>>;
let bob: Awaited<ReturnType<typeof deviceLogin>>;
let A: TestServer["sync"];
let B: TestServer["sync"];
let ADM: TestServer["sync"];

beforeAll(async () => {
  ts = await startTestServer({ HARNESS_SYNC_HEARTBEAT_MS: "60" });
  admin = await deviceLogin(ts);
  alice = await createUserAndLogin(ts, admin.cookie, "alice@test.local");
  bob = await createUserAndLogin(ts, admin.cookie, "bob@test.local");
  A = ts.as(alice.accessToken).sync;
  B = ts.as(bob.accessToken).sync;
  ADM = ts.as(admin.accessToken).sync;
});

afterAll(async () => {
  await ts.close();
});

async function expectError(p: Promise<unknown>, code: Code, type?: ErrorType) {
  const err = await p.then(
    () => undefined,
    (e) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  expect((err as ConnectError).code).toBe(code);
  if (type !== undefined) expect((err as ConnectError).findDetails(ErrorInfoSchema)[0]?.type).toBe(type);
  return err as ConnectError;
}

type PutInit = MessageInitShape<typeof PutDocumentRequestSchema>;
const userRef = (kind: DocumentKind, key = "user") => ({ scope: Scope.USER, kind, key });

async function putDoc(c: TestServer["sync"], req: PutInit): Promise<Document> {
  const r = await c.putDocument(req);
  if (r.result.case !== "document") throw new Error(`expected document, got ${r.result.case}`);
  return r.result.value;
}

async function head(c: TestServer["sync"]): Promise<bigint> {
  let since = 0n;
  for (;;) {
    const r = await c.listChanges({ sinceVersion: since, pageSize: 1000 });
    since = r.nextVersion;
    if (!r.hasMore) return since;
  }
}

describe("documents: CAS writes", () => {
  it("creates, updates with base_version and reports conflicts as responses", async () => {
    const v1 = await putDoc(A, { ref: userRef(DocumentKind.SETTINGS), content: '{"model":"a"}', baseVersion: 0n });
    expect(v1.id).toMatch(/^doc_/);
    expect(v1.version).toBeGreaterThan(0n);
    expect(v1.contentHash).toMatch(/^[0-9a-f]{64}$/);
    expect(v1.source).toBe(`client:${alice.deviceId}`);
    expect(v1.updatedBy).toBe(alice.user!.id);
    expect(v1.scope).toBe(Scope.USER);

    // create-only on an existing document -> conflict with the current document
    const c1 = await A.putDocument({ ref: userRef(DocumentKind.SETTINGS), content: '{"model":"b"}', baseVersion: 0n });
    expect(c1.result.case).toBe("conflict");
    expect(c1.result.case === "conflict" && c1.result.value.current.case === "document" && c1.result.value.current.value.version).toBe(v1.version);

    // JSON content is stored byte-exact
    const exact = '{ "model" : "b",\n  "x": [1,2] }';
    const v2 = await putDoc(A, { ref: userRef(DocumentKind.SETTINGS), content: exact, baseVersion: v1.version, source: "web" });
    expect(v2.version).toBeGreaterThan(v1.version);
    expect(v2.id).toBe(v1.id);
    expect(v2.content).toBe(exact);
    expect(v2.source).toBe("web");

    // stale base_version -> conflict carrying the current state
    const c2 = await A.putDocument({ ref: userRef(DocumentKind.SETTINGS), content: "{}", baseVersion: v1.version });
    expect(c2.result.case === "conflict" && c2.result.value.current.case === "document" && c2.result.value.current.value.version).toBe(v2.version);

    // identical content is a no-op (no new version)
    const same = await putDoc(A, { ref: userRef(DocumentKind.SETTINGS), content: exact });
    expect(same.version).toBe(v2.version);

    // unconditional write
    const v3 = await putDoc(A, { ref: userRef(DocumentKind.SETTINGS), content: '{"model":"c"}' });
    expect(v3.version).toBeGreaterThan(v2.version);

    const got = await A.getDocument({ selector: { case: "ref", value: userRef(DocumentKind.SETTINGS) } });
    expect(got.document!.version).toBe(v3.version);
    expect((await A.getDocument({ selector: { case: "id", value: v1.id } })).document!.content).toBe('{"model":"c"}');
  });

  it("validates refs, content and permissions", async () => {
    const bad = (req: PutInit, code = Code.InvalidArgument) => expectError(A.putDocument(req), code);
    await bad({ ref: { scope: Scope.USER, kind: DocumentKind.POLICY, key: "default" }, content: "{}" });
    await bad({ ref: userRef(DocumentKind.SETTINGS), content: "not json" });
    await bad({ ref: userRef(DocumentKind.SETTINGS), content: "[1]" });
    await bad({ ref: userRef(DocumentKind.SETTINGS, "other"), content: "{}" });
    await bad({ ref: userRef(DocumentKind.PERMISSION_RULES), content: '{"allow":"Bash(*)"}' });
    await bad({ ref: { scope: Scope.PROJECT, kind: DocumentKind.MEMORY, key: "user" }, content: "x" });
    await bad({ ref: { scope: Scope.PROJECT, kind: DocumentKind.MEMORY, key: "user", projectKey: "GitHub.com/X/Y" }, content: "x" });
    await bad({ ref: { scope: Scope.USER, kind: DocumentKind.MEMORY, key: "user", projectKey: "github.com/x/y" }, content: "x" });
    await bad({ ref: userRef(DocumentKind.AGENT, "a"), content: "x", files: [{ path: "f", content: new Uint8Array([1]), executable: false }] });
    await bad({ ref: userRef(DocumentKind.MCP_SERVER, "m"), content: "" });
    await bad({ ref: { scope: Scope.UNSPECIFIED, kind: DocumentKind.AGENT, key: "a" }, content: "x" });
    await bad({ ref: userRef(DocumentKind.AGENT, ""), content: "x" });
    await bad({ ref: userRef(DocumentKind.AGENT, "a"), content: "x", targetRoles: ["user"] });
    await bad({ ref: { scope: Scope.ORG, kind: DocumentKind.SETTINGS, key: "user" }, content: "{}" }, Code.PermissionDenied);
    await expectError(
      A.putDocument({ ref: { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default" }, content: "{}" }),
      Code.PermissionDenied,
      ErrorType.PERMISSION_DENIED,
    );
    const ok = await putDoc(A, { ref: { scope: Scope.PROJECT, kind: DocumentKind.MEMORY, key: "user", projectKey: "github.com/xlsft/harness" }, content: "# notes" });
    expect(ok.projectKey).toBe("github.com/xlsft/harness");
    expect(ok.scope).toBe(Scope.PROJECT);
  });

  it("supports RULE documents, MEMORY files and the restricted SETTINGS permissions object", async () => {
    const rule = await putDoc(A, { ref: userRef(DocumentKind.RULE, "frontend/react.md"), content: "---\npaths: [\"src/**/*.tsx\"]\n---\nUse hooks.\n" });
    expect(rule.kind).toBe(DocumentKind.RULE);
    for (const key of ["../x.md", "/abs.md", "notes.txt"]) {
      await expectError(A.putDocument({ ref: userRef(DocumentKind.RULE, key), content: "x" }), Code.InvalidArgument);
    }
    const mem = await putDoc(A, {
      ref: { scope: Scope.PROJECT, kind: DocumentKind.MEMORY, key: "user", projectKey: "github.com/xlsft/memtest" },
      content: "# MEMORY\n- [build](build.md)\n",
      files: [{ path: "build.md", content: new TextEncoder().encode("run pnpm build"), executable: false }],
    });
    expect(mem.files.map((f) => f.path)).toEqual(["build.md"]);
    const settings = await putDoc(A, {
      ref: userRef(DocumentKind.SETTINGS),
      content: '{"permissions":{"defaultMode":"acceptEdits","additionalDirectories":["/tmp"],"disableBypassPermissionsMode":"disable"}}',
    });
    expect(settings.content).toContain("acceptEdits");
    await expectError(A.putDocument({ ref: userRef(DocumentKind.SETTINGS), content: '{"permissions":{"allow":["Bash(ls)"]}}' }), Code.InvalidArgument);
    await expectError(
      A.putDocument({ ref: { scope: Scope.PROJECT, kind: DocumentKind.SETTINGS, key: "user", projectKey: "github.com/x/y" }, content: "{}" }),
      Code.InvalidArgument,
    );
  });

  it("stores skill files and hashes them in canonical order", async () => {
    const f1 = { path: "scripts/run.sh", content: new TextEncoder().encode("#!/bin/sh\necho hi\n"), executable: true };
    const f2 = { path: "README.txt", content: new Uint8Array([0, 255, 10]), executable: false };
    const s1 = await putDoc(A, { ref: userRef(DocumentKind.SKILL, "deploy"), content: "---\nname: deploy\n---\n", files: [f1, f2] });
    expect(s1.files.map((f) => f.path)).toEqual(["README.txt", "scripts/run.sh"]);
    expect(s1.files[1]!.executable).toBe(true);
    expect([...s1.files[0]!.content]).toEqual([0, 255, 10]);
    const s2 = await putDoc(B, { ref: userRef(DocumentKind.SKILL, "deploy"), content: "---\nname: deploy\n---\n", files: [f2, f1] });
    expect(s2.contentHash).toBe(s1.contentHash);
    const s3 = await putDoc(B, { ref: userRef(DocumentKind.SKILL, "deploy"), content: "---\nname: deploy\n---\n", files: [f2, { ...f1, executable: false }] });
    expect(s3.contentHash).not.toBe(s1.contentHash);
    for (const path of ["../x", "/abs", "a//b", "a/./b", "a\\b"]) {
      await expectError(A.putDocument({ ref: userRef(DocumentKind.SKILL, "bad"), content: "", files: [{ path, content: new Uint8Array(), executable: false }] }), Code.InvalidArgument);
    }
  });

  it("assigns strictly increasing global versions under concurrency", async () => {
    const docs = await Promise.all(
      Array.from({ length: 20 }, (_, i) => putDoc(i % 2 ? A : B, { ref: userRef(DocumentKind.COMMAND, `cmd-${i}`), content: `# ${i}` })),
    );
    const versions = docs.map((d) => d.version);
    expect(new Set(versions).size).toBe(20);
  });
});

describe("documents: visibility and feed", () => {
  it("separates users, distributes org documents and hides role-targeted ones", async () => {
    const secret = await putDoc(A, { ref: userRef(DocumentKind.AGENT, "private-agent"), content: "alice only" });
    await expectError(B.getDocument({ selector: { case: "id", value: secret.id } }), Code.NotFound, ErrorType.NOT_FOUND);
    // the same (kind, key) for another user is a separate document
    const bobs = await putDoc(B, { ref: userRef(DocumentKind.AGENT, "private-agent"), content: "bob's", baseVersion: 0n });
    expect(bobs.id).not.toBe(secret.id);

    const orgSettings = await putDoc(ADM, { ref: { scope: Scope.ORG, kind: DocumentKind.SETTINGS, key: "user" }, content: '{"effort":"high"}' });
    const adminPolicy = await putDoc(ADM, {
      ref: { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "admins" },
      content: '{"deny":[]}',
      targetRoles: ["admin", "admin"],
    });
    expect(adminPolicy.targetRoles).toEqual(["admin"]);

    const feed = await B.listChanges({ sinceVersion: 0n, pageSize: 1000 });
    const docs = feed.changes.filter((c) => c.change.case === "document").map((c) => c.change.value as Document);
    const tombs = feed.changes.filter((c) => c.change.case === "tombstone").map((c) => c.change.value);
    expect(docs.some((d) => d.id === secret.id)).toBe(false);
    expect(docs.some((d) => d.id === bobs.id)).toBe(true);
    expect(docs.some((d) => d.id === orgSettings.id)).toBe(true);
    // not targeted at "user": a tombstone, never the content
    expect(tombs.some((t) => t?.id === adminPolicy.id)).toBe(true);
    expect(docs.some((d) => d.id === adminPolicy.id)).toBe(false);
    await expectError(B.getDocument({ selector: { case: "id", value: adminPolicy.id } }), Code.NotFound);
    expect((await ADM.getDocument({ selector: { case: "id", value: adminPolicy.id } })).document!.content).toBe('{"deny":[]}');
    await expectError(B.deleteDocument({ id: orgSettings.id }), Code.PermissionDenied);

    // versions ascending, each document once
    const versions = feed.changes.map((c) => c.change.value!.version);
    expect([...versions].sort((a, b) => (a < b ? -1 : 1))).toEqual(versions);
    expect(new Set(feed.changes.map((c) => c.change.value!.id)).size).toBe(feed.changes.length);
  });

  it("pages through the feed and filters by kind", async () => {
    const all = await A.listChanges({ sinceVersion: 0n, pageSize: 1000 });
    expect(all.hasMore).toBe(false);
    const seen: string[] = [];
    let since = 0n;
    for (let i = 0; i < 100; i++) {
      const page = await A.listChanges({ sinceVersion: since, pageSize: 2 });
      expect(page.changes.length).toBeLessThanOrEqual(2);
      seen.push(...page.changes.map((c) => c.change.value!.id));
      expect(page.nextVersion).toBeGreaterThanOrEqual(since);
      since = page.nextVersion;
      if (!page.hasMore) break;
    }
    expect(seen).toEqual(all.changes.map((c) => c.change.value!.id));
    expect(since).toBe(all.nextVersion);

    const skills = await A.listChanges({ sinceVersion: 0n, kinds: [DocumentKind.SKILL] });
    expect(skills.changes.length).toBeGreaterThan(0);
    expect(skills.changes.every((c) => c.change.value!.kind === DocumentKind.SKILL)).toBe(true);

    // nothing new: empty page, cursor stays at the head
    const empty = await A.listChanges({ sinceVersion: all.nextVersion });
    expect(empty.changes).toEqual([]);
    expect(empty.nextVersion).toBe(all.nextVersion);
  });
});

describe("documents: deletes and tombstones", () => {
  it("deletes with CAS, keeps tombstones and allows re-creation", async () => {
    const d = await putDoc(A, { ref: userRef(DocumentKind.OUTPUT_STYLE, "terse"), content: "be terse" });
    await expectError(A.deleteDocument({ id: d.id, baseVersion: 0n }), Code.InvalidArgument);
    const stale = await A.deleteDocument({ id: d.id, baseVersion: d.version - 1n });
    expect(stale.result.case).toBe("conflict");
    const before = await head(A);
    const del = await A.deleteDocument({ id: d.id, baseVersion: d.version });
    expect(del.result.case).toBe("tombstone");
    const tomb = del.result.value as { version: bigint; deletedAt: string; id: string };
    expect(tomb.version).toBeGreaterThan(d.version);
    expect(tomb.deletedAt).toMatch(/Z$/);

    const changes = await A.listChanges({ sinceVersion: before });
    expect(changes.changes.map((c) => [c.change.case, c.change.value!.id])).toEqual([["tombstone", d.id]]);
    await expectError(A.getDocument({ selector: { case: "id", value: d.id } }), Code.NotFound);

    // deleting again without base_version is idempotent; with one it conflicts with the tombstone
    expect((await A.deleteDocument({ id: d.id })).result.case).toBe("tombstone");
    const c = await A.deleteDocument({ id: d.id, baseVersion: d.version });
    expect(c.result.case === "conflict" && c.result.value.current.case).toBe("tombstone");
    // writing on top of the deleted version conflicts with the tombstone; base 0 re-creates
    const pc = await A.putDocument({ ref: userRef(DocumentKind.OUTPUT_STYLE, "terse"), content: "again", baseVersion: d.version });
    expect(pc.result.case === "conflict" && pc.result.value.current.case).toBe("tombstone");
    const again = await putDoc(A, { ref: userRef(DocumentKind.OUTPUT_STYLE, "terse"), content: "again", baseVersion: 0n });
    expect(again.id).toBe(d.id);
    await expectError(B.deleteDocument({ id: d.id }), Code.NotFound);
    await expectError(A.deleteDocument({ id: "doc_missing" }), Code.NotFound);
  });

  it("continues has_more scans with page tokens that never require a resync", async () => {
    const cursor = await head(A);
    for (let i = 0; i < 3; i++) await putDoc(A, { ref: userRef(DocumentKind.AGENT, `scan-${i}`), content: `${i}` });
    const p1 = await A.listChanges({ sinceVersion: cursor, pageSize: 1 });
    expect(p1.hasMore).toBe(true);
    expect(p1.nextPageToken).not.toBe("");
    // tombstones below the continuation point get purged mid-scan
    const t = await putDoc(A, { ref: userRef(DocumentKind.AGENT, "scan-tomb"), content: "t" });
    await A.deleteDocument({ id: t.id });
    expect(await purgeTombstones(Date.now() + 40 * 86_400_000)).toBeGreaterThan(0);
    expect((await A.listChanges({ sinceVersion: p1.nextVersion, pageSize: 1 })).resyncRequired).toBe(true);
    const seen = [...p1.changes.map((c) => c.change.value!.key)];
    let token = p1.nextPageToken;
    while (token) {
      const p = await A.listChanges({ pageToken: token, pageSize: 1 });
      expect(p.resyncRequired).toBe(false);
      seen.push(...p.changes.map((c) => c.change.value!.key));
      token = p.hasMore ? p.nextPageToken : "";
      if (!p.hasMore) expect(p.nextPageToken).toBe("");
    }
    expect(seen).toEqual(["scan-0", "scan-1", "scan-2"]);
    await expectError(A.listChanges({ pageToken: "bogus" }), Code.InvalidArgument);
    // tokens are bound to the caller
    await expectError(B.listChanges({ pageToken: p1.nextPageToken }), Code.InvalidArgument);
  });

  it("purges old tombstones and requires a resync for older cursors", async () => {
    const cursor = await head(A);
    const d = await putDoc(A, { ref: userRef(DocumentKind.SSH_HOST, "box"), content: '{"host":"box.local"}' });
    await A.deleteDocument({ id: d.id });
    expect(await purgeTombstones(Date.now() + 29 * 86_400_000)).toBe(0); // within retention
    expect(await purgeTombstones(Date.now() + 31 * 86_400_000)).toBeGreaterThan(0);
    const r = await A.listChanges({ sinceVersion: cursor });
    expect(r.resyncRequired).toBe(true);
    expect(r.changes).toEqual([]);
    expect((await A.listChanges({ sinceVersion: 0n })).resyncRequired).toBe(false);
    // a cursor ahead of the server (restored database) also needs a resync
    expect((await A.listChanges({ sinceVersion: (await head(A)) + 1000n })).resyncRequired).toBe(true);
  });
});

describe("documents: batch", () => {
  it("returns one result per item, in order", async () => {
    const existing = await putDoc(A, { ref: userRef(DocumentKind.AGENT, "batch-existing"), content: "x" });
    const r = await A.batchPutDocuments({
      items: [
        { ref: userRef(DocumentKind.AGENT, "batch-1"), content: "one", source: "import:claude" },
        { ref: userRef(DocumentKind.AGENT, "batch-existing"), content: "y", baseVersion: 0n },
        { ref: userRef(DocumentKind.SETTINGS), content: "{broken" },
        { ref: { scope: Scope.ORG, kind: DocumentKind.AGENT, key: "x" }, content: "x" },
      ],
    });
    expect(r.results.map((x) => x.result.case)).toEqual(["document", "conflict", "error", "error"]);
    expect(r.results[0]!.result.case === "document" && r.results[0]!.result.value.source).toBe("import:claude");
    expect(r.results[1]!.result.case === "conflict" && r.results[1]!.result.value.current.value!.version).toBe(existing.version);
    expect(r.results[2]!.result.value).toMatchObject({ code: "invalid_argument" });
    expect(r.results[3]!.result.value).toMatchObject({ code: "permission_denied" });
    await expectError(A.batchPutDocuments({ items: Array.from({ length: 501 }, () => ({ ref: userRef(DocumentKind.AGENT, "x"), content: "" })) }), Code.InvalidArgument);
  });
});

describe("WatchChanges", () => {
  async function watch(c: TestServer["sync"], sinceVersion: bigint, kinds: DocumentKind[] = []) {
    const ac = new AbortController();
    const events: WatchChangesResponse["event"][] = [];
    let ended = false;
    let error: unknown;
    const done = (async () => {
      try {
        for await (const r of c.watchChanges({ sinceVersion, kinds }, { signal: ac.signal })) events.push(r.event);
      } catch (e) {
        if (!ac.signal.aborted) error = e;
      } finally {
        ended = true;
      }
    })();
    const until = async (pred: () => boolean, ms = 3000) => {
      const t0 = Date.now();
      while (!pred()) {
        if (Date.now() - t0 > ms) throw new Error(`timeout; events: ${JSON.stringify(events.map((e) => e.case))}`);
        await new Promise((r) => setTimeout(r, 10));
      }
    };
    return { events, until, stop: async () => (ac.abort(), done), get ended() { return ended; }, get error() { return error; } };
  }

  it("catches up, then streams live changes (own writes echoed), with heartbeats", async () => {
    const start = await head(A);
    const pre = await putDoc(A, { ref: userRef(DocumentKind.COMMAND, "watch-pre"), content: "pre" });
    const w = await watch(A, start);
    await w.until(() => w.events.some((e) => e.case === "caughtUp"));
    const idx = w.events.findIndex((e) => e.case === "caughtUp");
    expect(w.events.slice(0, idx).map((e) => e.case === "change" && e.value.change.value!.id)).toEqual([pre.id]);
    const caughtUp = w.events[idx]!.value as { version: bigint };
    expect(caughtUp.version).toBeGreaterThanOrEqual(pre.version);

    // live: own write is echoed; another user's private write is not; org writes are
    const mine = await putDoc(A, { ref: userRef(DocumentKind.COMMAND, "watch-live"), content: "live" });
    await putDoc(B, { ref: userRef(DocumentKind.COMMAND, "watch-bob"), content: "bob" });
    const org = await putDoc(ADM, { ref: { scope: Scope.ORG, kind: DocumentKind.COMMAND, key: "watch-org" }, content: "org" });
    await w.until(() => w.events.some((e) => e.case === "change" && e.value.change.value!.id === org.id));
    const live = w.events.slice(idx + 1).filter((e) => e.case === "change").map((e) => (e.value as { change: { value: { id: string } } }).change.value.id);
    expect(live).toEqual([mine.id, org.id]);

    // idle: heartbeats carry the head
    const n = w.events.length;
    await w.until(() => w.events.slice(n).some((e) => e.case === "heartbeat"));
    const hb = w.events.slice(n).find((e) => e.case === "heartbeat")!.value as { version: bigint };
    expect(hb.version).toBeGreaterThanOrEqual(org.version);

    // a delete arrives as a tombstone
    await A.deleteDocument({ id: mine.id });
    await w.until(() => w.events.some((e) => e.case === "change" && e.value.change.case === "tombstone" && e.value.change.value.id === mine.id));
    await w.stop();
    expect(w.error).toBeUndefined();
  });

  it("filters by kind and ends with ResyncRequired for purged cursors", async () => {
    const start = await head(A);
    const w = await watch(A, start, [DocumentKind.MEMORY]);
    await w.until(() => w.events.some((e) => e.case === "caughtUp"));
    await putDoc(A, { ref: userRef(DocumentKind.COMMAND, "not-memory"), content: "x" });
    const mem = await putDoc(A, { ref: userRef(DocumentKind.MEMORY), content: "# memory" });
    await w.until(() => w.events.some((e) => e.case === "change"));
    expect(w.events.filter((e) => e.case === "change").map((e) => (e.value as { change: { value: { id: string } } }).change.value.id)).toEqual([mem.id]);
    await w.stop();

    const old = await watch(A, 1n);
    await old.until(() => old.ended);
    expect(old.events.map((e) => e.case)).toEqual(["resyncRequired"]);
  });

  it("requires authentication", async () => {
    await expectError(
      (async () => {
        for await (const _ of ts.sync.watchChanges({ sinceVersion: 0n })) {
          /* drain */
        }
      })(),
      Code.Unauthenticated,
    );
  });
});

describe("sessions", () => {
  const sid = "6f1c3c2e-7d1b-4b8a-9a43-0d5a4b0e2f11";
  const S_API = "a0000000-0000-4000-8000-000000000001";
  const S_WEB = "a0000000-0000-4000-8000-000000000002";
  const S_OLD = "a0000000-0000-4000-8000-000000000003";
  const S_BOB = "b0000000-0000-4000-8000-000000000004";
  const info = (over: Record<string, unknown> = {}) =>
    create(SessionInfoSchema, { id: sid, title: "Fix login test", cwd: "/home/alice/app", repo: "github.com/alice/app", model: "claude-t", status: SessionStatus.WORKING, gitBranch: "main", ...over });

  it("upserts metadata with update paths and enforces ownership", async () => {
    await expectError(A.upsertSession({ session: { info: create(SessionInfoSchema, {}) } }), Code.InvalidArgument);
    const r = await A.upsertSession({ session: { info: info(), promptBundleVersion: "pb1" } });
    const s = r.session!;
    expect(s.info).toMatchObject({ id: sid, title: "Fix login test", repo: "github.com/alice/app", deviceId: alice.deviceId, deviceName: "test-box" });
    expect(s.info!.updatedAt).toMatch(/Z$/);
    expect(s.promptBundleVersion).toBe("pb1");
    expect(s.lastSeq).toBe(0n);
    expect(s.createdAt).toMatch(/Z$/);

    // a frontend renames without clobbering client-owned fields
    const renamed = await A.upsertSession({ session: { info: create(SessionInfoSchema, { id: sid, title: "Renamed" }) }, updatePaths: ["info.title"] });
    expect(renamed.session!.info).toMatchObject({ title: "Renamed", cwd: "/home/alice/app", model: "claude-t", status: SessionStatus.WORKING });
    expect(renamed.session!.promptBundleVersion).toBe("pb1");
    const archived = await A.upsertSession({ session: { info: create(SessionInfoSchema, { id: sid, archived: true }), promptBundleVersion: "pb2" }, updatePaths: ["info.archived", "prompt_bundle_version"] });
    expect(archived.session!.info).toMatchObject({ title: "Renamed", archived: true });
    expect(archived.session!.promptBundleVersion).toBe("pb2");
    await A.upsertSession({ session: { info: create(SessionInfoSchema, { id: sid }) }, updatePaths: ["info.archived"] });

    await expectError(A.upsertSession({ session: { info: info() }, updatePaths: ["info.nope"] }), Code.InvalidArgument);
    // SessionInfo paths need the "info." prefix; ids must be UUID v4
    await expectError(A.upsertSession({ session: { info: info() }, updatePaths: ["title"] }), Code.InvalidArgument);
    await expectError(A.upsertSession({ session: { info: info({ id: "ses_123" }) } }), Code.InvalidArgument);
    await expectError(B.upsertSession({ session: { info: info() } }), Code.PermissionDenied);
    await expectError(B.getSession({ sessionId: sid }), Code.NotFound);
    expect((await A.getSession({ sessionId: sid })).session!.info!.title).toBe("Renamed");
  });

  it("appends transcript entries idempotently and byte-exact, with server seq", async () => {
    await expectError(A.appendSessionEvents({ sessionId: "0b5a2c1e-1111-4222-8333-444455556666", entries: [] }), Code.NotFound);
    const line = (uuid: string, text: string) => `{"type":"user","uuid":"${uuid}","message":{"role":"user","content":[{"type":"text","text":"${text}"}]},  "pad":"ü"}`;
    const before = (await A.getSession({ sessionId: sid })).session!.lastActivityAt;
    await new Promise((r) => setTimeout(r, 5));
    const r1 = await A.appendSessionEvents({
      sessionId: sid,
      entries: [
        { uuid: "u1", type: "user", timestamp: "2026-11-02T10:00:00Z", entryJson: line("u1", "the frobnicator is broken") },
        { uuid: "u2", parentUuid: "u1", type: "assistant", entryJson: '{"type":"assistant","uuid":"u2","message":{"content":"Looking."}}' },
        { uuid: "u2", parentUuid: "u1", type: "assistant", entryJson: "dup in batch" },
      ],
    });
    expect(r1).toMatchObject({ accepted: 2, duplicates: 1, lastSeq: 2n });
    const r2 = await A.appendSessionEvents({ sessionId: sid, entries: [{ uuid: "u2", entryJson: "{}" }, { uuid: "u3", type: "summary", entryJson: '{"type":"summary","summary":"Debugging"}' }] });
    expect(r2).toMatchObject({ accepted: 1, duplicates: 1, lastSeq: 3n });
    const s = (await A.getSession({ sessionId: sid })).session!;
    expect(s.lastSeq).toBe(3n);
    expect(Date.parse(s.lastActivityAt)).toBeGreaterThan(Date.parse(before));

    const t = await A.getSessionTranscript({ sessionId: sid });
    expect(t.hasMore).toBe(false);
    expect(t.entries.map((e) => [e.seq, e.uuid])).toEqual([
      [1n, "u1"],
      [2n, "u2"],
      [3n, "u3"],
    ]);
    expect(t.entries[0]!.entryJson).toBe(line("u1", "the frobnicator is broken"));
    expect(t.entries[1]).toMatchObject({ parentUuid: "u1", type: "assistant" });
    expect(t.entries[0]!.timestamp).toBe("2026-11-02T10:00:00Z");

    const p1 = await A.getSessionTranscript({ sessionId: sid, afterSeq: 1n, pageSize: 1 });
    expect(p1.entries.map((e) => e.uuid)).toEqual(["u2"]);
    expect(p1.hasMore).toBe(true);
    await expectError(B.getSessionTranscript({ sessionId: sid }), Code.NotFound);

    // subagent transcripts share the session but are deduplicated and paged separately
    const sub = await A.appendSessionEvents({
      sessionId: sid,
      entries: [
        { uuid: "u1", agentId: "agent-7", type: "user", entryJson: '{"uuid":"u1","agent":"7"}' },
        { uuid: "s2", agentId: "agent-7", type: "assistant", entryJson: '{"uuid":"s2"}' },
        { uuid: "u1", agentId: "agent-7", entryJson: "dup" },
      ],
    });
    expect(sub).toMatchObject({ accepted: 2, duplicates: 1, lastSeq: 5n });
    const subT = await A.getSessionTranscript({ sessionId: sid, agentId: "agent-7" });
    expect(subT.entries.map((x) => [x.seq, x.uuid, x.agentId])).toEqual([
      [4n, "u1", "agent-7"],
      [5n, "s2", "agent-7"],
    ]);
    expect(subT.entries[0]!.entryJson).toBe('{"uuid":"u1","agent":"7"}');
    expect((await A.getSessionTranscript({ sessionId: sid })).entries.map((x) => x.uuid)).toEqual(["u1", "u2", "u3"]);
    expect((await A.getSessionTranscript({ sessionId: sid, agentId: "agent-7", afterSeq: 4n })).entries.map((x) => x.uuid)).toEqual(["s2"]);
    await expectError(A.appendSessionEvents({ sessionId: sid, entries: [{ uuid: "", entryJson: "{}" }] }), Code.InvalidArgument);
    await expectError(B.appendSessionEvents({ sessionId: sid, entries: [{ uuid: "x", entryJson: "{}" }] }), Code.NotFound);
  });

  it("lists sessions with filters, search and paging", async () => {
    const mk = async (id: string, over: Record<string, unknown>) => A.upsertSession({ session: { info: create(SessionInfoSchema, { id, ...over }) } });
    await mk(S_API, { title: "Add API rate limits", cwd: "/srv/api", repo: "github.com/alice/api", status: SessionStatus.IDLE, deviceId: "dev_other" });
    await mk(S_WEB, { title: "Polish 100% layout", cwd: "/srv/web", repo: "github.com/alice/web", status: SessionStatus.NEEDS_INPUT });
    await mk(S_OLD, { title: "Archived thing", cwd: "/srv/web", repo: "github.com/alice/web", archived: true });
    await B.upsertSession({ session: { info: create(SessionInfoSchema, { id: S_BOB, title: "Bob's frobnicator" }) } });

    const ids = async (req: Parameters<typeof A.listSessions>[0]) => (await A.listSessions(req)).sessions.map((s) => s.info!.id);
    // most recently active first (sid got transcript entries most recently -> but s-* were created after)
    expect((await ids({})).sort()).toEqual([sid, S_API, S_WEB].sort());
    expect(await ids({ includeArchived: true })).toContain(S_OLD);
    expect(await ids({ repo: "github.com/alice/web" })).toEqual([S_WEB]);
    expect(await ids({ cwd: "/srv/api" })).toEqual([S_API]);
    expect(await ids({ deviceId: "dev_other" })).toEqual([S_API]);
    expect(await ids({ statuses: [SessionStatus.IDLE, SessionStatus.NEEDS_INPUT] })).toEqual(expect.arrayContaining([S_API, S_WEB]));
    expect(await ids({ statuses: [SessionStatus.ERROR] })).toEqual([]);
    expect(await ids({ query: "RATE limits" })).toEqual([S_API]);
    expect(await ids({ query: "/srv/web" })).toEqual([S_WEB]);
    expect(await ids({ query: "frobnicator" })).toEqual([sid]); // transcript text; Bob's session is not visible
    expect(await ids({ query: "100%" })).toEqual([S_WEB]); // LIKE wildcards are escaped
    expect(await ids({ query: "%" })).toEqual([S_WEB]);

    // activity order + paging
    await A.appendSessionEvents({ sessionId: S_API, entries: [{ uuid: "a1", entryJson: "{}" }] });
    const p1 = await A.listSessions({ pageSize: 2 });
    expect(p1.sessions[0]!.info!.id).toBe(S_API);
    expect(p1.nextPageToken).not.toBe("");
    const p2 = await A.listSessions({ pageSize: 2, pageToken: p1.nextPageToken });
    expect(p2.nextPageToken).toBe("");
    expect([...p1.sessions, ...p2.sessions].map((s) => s.info!.id).sort()).toEqual([sid, S_API, S_WEB].sort());
    await expectError(A.listSessions({ pageToken: "garbage" }), Code.InvalidArgument);
  });

  it("keeps per-user state (pinned, unread, archived, title) without clobbering client fields", async () => {
    const S_STATE = "c0000000-0000-4000-8000-000000000005";
    await A.upsertSession({ session: { info: info({ id: S_STATE, title: "AI title", status: SessionStatus.IDLE }) } });
    const r = await A.upsertSession({
      session: { info: create(SessionInfoSchema, { id: S_STATE, pinned: true, unread: true, title: "My name" }) },
      updatePaths: ["info.pinned", "info.unread", "info.title"],
    });
    expect(r.session!.info).toMatchObject({ pinned: true, unread: true, title: "My name", cwd: "/home/alice/app", model: "claude-t" });
    // a later client upload of its own fields keeps the user state
    await A.upsertSession({ session: { info: create(SessionInfoSchema, { id: S_STATE, status: SessionStatus.WORKING, model: "claude-u" }) }, updatePaths: ["info.status", "info.model"] });
    const listed = (await A.listSessions({ includeArchived: true })).sessions.find((s) => s.info!.id === S_STATE)!;
    expect(listed.info).toMatchObject({ pinned: true, unread: true, title: "My name", status: SessionStatus.WORKING, model: "claude-u" });
    // archive hides it from the default list; unarchive brings it back
    await A.upsertSession({ session: { info: create(SessionInfoSchema, { id: S_STATE, archived: true }) }, updatePaths: ["info.archived"] });
    expect((await A.listSessions({})).sessions.map((s) => s.info!.id)).not.toContain(S_STATE);
    await A.upsertSession({ session: { info: create(SessionInfoSchema, { id: S_STATE, archived: false, unread: false }) }, updatePaths: ["info.archived", "info.unread"] });
    const back = (await A.listSessions({})).sessions.find((s) => s.info!.id === S_STATE)!;
    expect(back.info).toMatchObject({ archived: false, unread: false, pinned: true });
    // another user can neither see nor change it
    expect((await B.listSessions({ includeArchived: true })).sessions.map((s) => s.info!.id)).not.toContain(S_STATE);
    await expectError(B.upsertSession({ session: { info: create(SessionInfoSchema, { id: S_STATE, pinned: false }) }, updatePaths: ["info.pinned"] }), Code.PermissionDenied);
    expect((await A.getSession({ sessionId: S_STATE })).session!.info!.pinned).toBe(true);
  });

  it("deletes a session with its transcript, only for its owner", async () => {
    const S_DEL = "d0000000-0000-4000-8000-000000000006";
    await A.upsertSession({ session: { info: info({ id: S_DEL, title: "Doomed" }) } });
    await A.appendSessionEvents({ sessionId: S_DEL, entries: [{ uuid: "d1", entryJson: '{"type":"user","message":{"content":"zyzzyva"}}' }] });
    await expectError(A.deleteSession({ sessionId: "" }), Code.InvalidArgument);
    await expectError(B.deleteSession({ sessionId: S_DEL }), Code.NotFound);
    expect((await A.getSession({ sessionId: S_DEL })).session!.info!.title).toBe("Doomed");

    await A.deleteSession({ sessionId: S_DEL });
    await expectError(A.getSession({ sessionId: S_DEL }), Code.NotFound);
    await expectError(A.getSessionTranscript({ sessionId: S_DEL }), Code.NotFound);
    expect((await A.listSessions({ query: "zyzzyva", includeArchived: true })).sessions).toEqual([]);
    await expectError(A.deleteSession({ sessionId: S_DEL }), Code.NotFound);
    // the id is free again: a client re-upload recreates it from scratch
    const again = await A.upsertSession({ session: { info: info({ id: S_DEL }) } });
    expect(again.session!.lastSeq).toBe(0n);
  });
});
