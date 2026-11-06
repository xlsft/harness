/**
 * Artifacts, the knowledge base, the task tracker and transcript search, over the real routes and object store: what
 * each user can see of the others' data is the point of most of these.
 */
import { createHash } from "node:crypto";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { SessionInfoSchema } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { ArtifactKind, ArtifactVisibility, ListArtifactsRequest_Filter as Filter } from "../src/gen/harness/library/v1/artifacts_pb.ts";
import { TrackerPriority, TrackerStatus } from "../src/gen/harness/library/v1/tracker_pb.ts";
import { ObjectKind } from "../src/gen/harness/objects/v1/objects_pb.ts";
import { chunkText, orQuery } from "../src/services/knowledge.ts";
import { allWordsQuery, transcriptSnippet } from "../src/services/sessions.ts";
import { artifactHeaders, artifactKind, artifactType } from "../src/services/artifacts.ts";
import { createUserAndLogin, deviceLogin, signIn, startTestServer, ADMIN, type TestServer } from "./helpers/server.ts";

const sha = (b: Uint8Array) => createHash("sha256").update(b).digest("hex");

async function expectCode(p: Promise<unknown>, code: Code) {
  const err = await p.then(
    () => undefined,
    (e: unknown) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  expect((err as ConnectError).code).toBe(code);
}

describe("library", () => {
  let ts: TestServer;
  let admin: ReturnType<TestServer["as"]>;
  let alice: ReturnType<TestServer["as"]>;
  let bob: ReturnType<TestServer["as"]>;
  let aliceToken: string;
  let bobToken: string;

  beforeAll(async () => {
    ts = await startTestServer();
    const a = await deviceLogin(ts);
    admin = ts.as(a.accessToken);
    const cookie = await signIn(ts.url, ADMIN.email, ADMIN.password);
    const u1 = await createUserAndLogin(ts, cookie, "alice@test.local");
    const u2 = await createUserAndLogin(ts, cookie, "bob@test.local");
    aliceToken = u1.accessToken;
    bobToken = u2.accessToken;
    alice = ts.as(aliceToken);
    bob = ts.as(bobToken);
  }, 120_000);

  afterAll(async () => {
    await ts?.close();
  });

  async function upload(c: ReturnType<TestServer["as"]>, text: string, name: string) {
    const bytes = new TextEncoder().encode(text);
    const sum = sha(bytes);
    const use = { sessionId: "", kind: ObjectKind.ARTIFACT, name, contentType: "application/octet-stream" };
    const s = await c.objects.startUpload({ sha256: sum, size: BigInt(bytes.length), use });
    if (!s.alreadyStored) {
      await c.objects.uploadChunk({ uploadId: s.uploadId, index: 0, data: bytes });
      await c.objects.finishUpload({ uploadId: s.uploadId, use });
    }
    return sum;
  }
  const content = (id: string, token: string, query = "") => fetch(`${ts.url}/api/artifacts/${id}/content${query}`, { headers: { authorization: `Bearer ${token}` } });

  describe("artifacts", () => {
    it("publishes privately, and only the owner sees it until it is shared", async () => {
      const sum = await upload(alice, "<script>document.title='x'</script><h1>Report</h1>", "report.html");
      const res = await alice.artifacts.publishArtifact({ title: "Report", sha256: sum, name: "report.html", sessionId: "s-1" });
      const a = res.artifact!;
      expect(a.visibility).toBe(ArtifactVisibility.PRIVATE);
      expect(a.kind).toBe(ArtifactKind.PAGE);
      expect(a.mine).toBe(true);
      expect(a.current!.number).toBe(1);

      expect((await bob.artifacts.listArtifacts({ filter: Filter.ALL })).artifacts).toEqual([]);
      await expectCode(bob.artifacts.getArtifact({ id: a.id }), Code.NotFound);
      expect((await content(a.id, bobToken)).status).toBe(404);
      await expectCode(bob.artifacts.updateArtifact({ id: a.id, visibility: ArtifactVisibility.SHARED }), Code.NotFound);

      await alice.artifacts.updateArtifact({ id: a.id, visibility: ArtifactVisibility.SHARED });
      const seen = (await bob.artifacts.listArtifacts({ filter: Filter.SHARED })).artifacts;
      expect(seen.map((x) => x.id)).toEqual([a.id]);
      expect(seen[0]!.mine).toBe(false);
      expect(seen[0]!.ownerName).not.toBe("");
      // The session means nothing to anyone else, and is theirs alone.
      expect(seen[0]!.sessionId).toBe("");
      expect((await content(a.id, bobToken)).status).toBe(200);
      await expectCode(bob.artifacts.updateArtifact({ id: a.id, title: "Mine now" }), Code.PermissionDenied);
      await expectCode(bob.artifacts.deleteArtifact({ id: a.id }), Code.PermissionDenied);
      expect((await bob.artifacts.listArtifacts({ filter: Filter.MINE })).artifacts).toEqual([]);

      await alice.artifacts.updateArtifact({ id: a.id, visibility: ArtifactVisibility.PRIVATE });
      expect((await content(a.id, bobToken)).status).toBe(404);
      await alice.artifacts.deleteArtifact({ id: a.id });
    });

    it("serves a page in a sandbox with no way to reach the app or the network", async () => {
      const sum = await upload(alice, "<h1>Sandboxed</h1>", "page.html");
      const { artifact } = await alice.artifacts.publishArtifact({ title: "Page", sha256: sum, name: "page.html" });
      const res = await content(artifact!.id, aliceToken);
      expect(await res.text()).toBe("<h1>Sandboxed</h1>");
      expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
      const csp = res.headers.get("content-security-policy")!;
      expect(csp).toMatch(/^sandbox allow-scripts;/);
      expect(csp).not.toContain("allow-same-origin");
      expect(csp).toContain("connect-src 'none'");
      expect(csp).toContain("default-src 'none'");
      expect(res.headers.get("x-content-type-options")).toBe("nosniff");
      expect(res.headers.get("cache-control")).toContain("no-store");
      expect((await fetch(`${ts.url}/api/artifacts/${artifact!.id}/content`)).status).toBe(401);
      const attached = await content(artifact!.id, aliceToken, "?download=1");
      expect(attached.headers.get("content-disposition")).toMatch(/^attachment/);
      expect(attached.headers.get("content-security-policy")).toMatch(/^sandbox/);
      await alice.artifacts.deleteArtifact({ id: artifact!.id });
    });

    it("decides what a file is by its name, and never renders the unknown", () => {
      expect(artifactType("a.html", "application/octet-stream")).toBe("text/html");
      expect(artifactType("a.md", "")).toBe("text/markdown");
      expect(artifactType("a.go", "application/octet-stream")).toBe("text/plain");
      expect(artifactKind(artifactType("a.bin", "application/x-thing"))).toBe("file");
      const h = artifactHeaders({ contentType: "application/x-thing", name: 'a"\r\nb.bin', sha256: "a".repeat(64) }, false);
      expect(h["content-type"]).toBe("application/octet-stream");
      expect(h["content-disposition"]).toBe('attachment; filename="a___b.bin"');
    });

    it("adds versions, keeps old ones readable, and frees bytes only when nothing uses them", async () => {
      const one = await upload(alice, "version one", "notes.md");
      const { artifact } = await alice.artifacts.publishArtifact({ title: "Notes", sha256: one, name: "notes.md" });
      const id = artifact!.id;
      const same = await alice.artifacts.publishArtifact({ artifactId: id, sha256: one, name: "notes.md" });
      expect(same.unchanged).toBe(true);
      expect(same.artifact!.current!.number).toBe(1);

      const two = await upload(alice, "version two", "notes.md");
      const next = await alice.artifacts.publishArtifact({ artifactId: id, sha256: two, name: "notes.md" });
      expect(next.artifact!.current!.number).toBe(2);
      expect(next.artifact!.title).toBe("Notes");

      const got = await alice.artifacts.getArtifact({ id });
      expect(got.versions.map((v) => v.number)).toEqual([2, 1]);
      expect(got.text).toBe("version two");
      expect((await alice.artifacts.getArtifact({ id, version: 1 })).text).toBe("version one");
      expect(await (await content(id, aliceToken, "?version=1")).text()).toBe("version one");
      await expectCode(alice.artifacts.getArtifact({ id, version: 9 }), Code.NotFound);

      // Someone else's id can't be published over, and bytes they never uploaded can't be claimed.
      await expectCode(bob.artifacts.publishArtifact({ artifactId: id, sha256: two, name: "x.md" }), Code.FailedPrecondition);
      const bobs = await upload(bob, "bob's", "x.md");
      await expectCode(bob.artifacts.publishArtifact({ artifactId: id, sha256: bobs, name: "x.md" }), Code.NotFound);

      // A second artifact with the same bytes keeps them alive when the first goes.
      const twin = await alice.artifacts.publishArtifact({ title: "Twin", sha256: two, name: "twin.md" });
      await alice.artifacts.deleteArtifact({ id });
      expect(await (await content(twin.artifact!.id, aliceToken)).text()).toBe("version two");
      await alice.artifacts.deleteArtifact({ id: twin.artifact!.id });
      await expectCode(alice.objects.statObject({ sha256: two }), Code.NotFound);
    });

    it("needs a title and filters by title and session", async () => {
      const sum = await upload(alice, "x", "x.txt");
      await expectCode(alice.artifacts.publishArtifact({ title: " ", sha256: sum, name: "x.txt" }), Code.InvalidArgument);
      await expectCode(alice.artifacts.publishArtifact({ title: "No bytes", sha256: "0".repeat(64), name: "x.txt" }), Code.FailedPrecondition);
      const a = (await alice.artifacts.publishArtifact({ title: "Alpha page", sha256: sum, name: "x.txt", sessionId: "s-a" })).artifact!;
      const b = (await alice.artifacts.publishArtifact({ title: "Beta page", sha256: sum, name: "y.txt", sessionId: "s-b" })).artifact!;
      expect((await alice.artifacts.listArtifacts({ filter: Filter.MINE, query: "alph" })).artifacts.map((x) => x.id)).toEqual([a.id]);
      expect((await alice.artifacts.listArtifacts({ filter: Filter.MINE, sessionId: "s-b" })).artifacts.map((x) => x.id)).toEqual([b.id]);
      const paged = await alice.artifacts.listArtifacts({ filter: Filter.MINE, pageSize: 1 });
      expect(paged.artifacts).toHaveLength(1);
      expect((await alice.artifacts.listArtifacts({ filter: Filter.MINE, pageSize: 1, pageToken: paged.nextPageToken })).artifacts).toHaveLength(1);
      await alice.artifacts.deleteArtifact({ id: a.id });
      await alice.artifacts.deleteArtifact({ id: b.id });
    });
  });

  describe("knowledge base", () => {
    it("lets only admins write for everyone, and keeps personal documents personal, from admins too", async () => {
      await expectCode(alice.knowledge.createKnowledgeDocument({ scope: Scope.ORG, title: "T", content: "c" }), Code.PermissionDenied);
      await expectCode(alice.knowledge.createKnowledgeDocument({ scope: Scope.PROJECT, title: "T", content: "c" }), Code.InvalidArgument);
      await expectCode(alice.knowledge.createKnowledgeDocument({ scope: Scope.UNSPECIFIED, title: "T", content: "c" }), Code.InvalidArgument);

      const org = (await admin.knowledge.createKnowledgeDocument({ scope: Scope.ORG, title: "Deployment runbook", content: "To deploy the gateway, run the pipeline and watch the canary." })).document!;
      const mine = (await alice.knowledge.createKnowledgeDocument({ scope: Scope.USER, title: "Alice notes", content: "My private deployment checklist for the gateway.", sourceName: "notes.md" })).document!;
      expect(mine.sourceName).toBe("notes.md");

      const hits = await alice.knowledge.searchKnowledge({ query: "how do I deploy the gateway" });
      expect(hits.hits.map((h) => h.documentId).sort()).toEqual([org.id, mine.id].sort());

      const others = await bob.knowledge.searchKnowledge({ query: "deployment checklist gateway" });
      expect(others.hits.map((h) => h.documentId)).toEqual([org.id]);
      const adminHits = await admin.knowledge.searchKnowledge({ query: "private checklist" });
      expect(adminHits.hits.map((h) => h.documentId)).not.toContain(mine.id);

      await expectCode(bob.knowledge.getKnowledgeDocument({ id: mine.id }), Code.NotFound);
      await expectCode(admin.knowledge.getKnowledgeDocument({ id: mine.id }), Code.NotFound);
      await expectCode(admin.knowledge.deleteKnowledgeDocument({ id: mine.id }), Code.NotFound);

      const listed = (await alice.knowledge.listKnowledgeDocuments({})).documents;
      expect(listed.map((d) => [d.id, d.editable]).sort()).toEqual(
        [
          [org.id, false],
          [mine.id, true],
        ].sort(),
      );
      expect((await alice.knowledge.listKnowledgeDocuments({ scope: Scope.USER })).documents.map((d) => d.id)).toEqual([mine.id]);
      await expectCode(alice.knowledge.updateKnowledgeDocument({ id: org.id, title: "x" }), Code.PermissionDenied);
      await expectCode(alice.knowledge.deleteKnowledgeDocument({ id: org.id }), Code.PermissionDenied);

      await admin.knowledge.deleteKnowledgeDocument({ id: org.id });
      await alice.knowledge.deleteKnowledgeDocument({ id: mine.id });
    });

    it("finds stemmed English and exact other languages, and searches the changed text", async () => {
      const d = (await alice.knowledge.createKnowledgeDocument({ scope: Scope.USER, title: "Backups", content: "Backups are restored nightly.\n\nРезервные копии проверяются каждую неделю." })).document!;
      const top = async (q: string) => (await alice.knowledge.searchKnowledge({ query: q })).hits.map((h) => h.snippet)[0] ?? "";
      expect(await top("restoring")).toContain("restored");
      expect(await top("копии")).toContain("Резервные");
      expect((await alice.knowledge.searchKnowledge({ query: "nothing-like-this" })).hits).toEqual([]);
      await expectCode(alice.knowledge.searchKnowledge({ query: " !! " }), Code.InvalidArgument);

      await alice.knowledge.updateKnowledgeDocument({ id: d.id, content: "Snapshots replace the old process." });
      expect((await alice.knowledge.searchKnowledge({ query: "nightly" })).hits).toEqual([]);
      expect(await top("snapshots")).toContain("Snapshots");
      expect((await alice.knowledge.getKnowledgeDocument({ id: d.id })).content).toBe("Snapshots replace the old process.");
      await alice.knowledge.deleteKnowledgeDocument({ id: d.id });
    });

    it("refuses binary and oversized documents", async () => {
      await expectCode(alice.knowledge.createKnowledgeDocument({ scope: Scope.USER, title: "T", content: "a\0b" }), Code.InvalidArgument);
      await expectCode(alice.knowledge.createKnowledgeDocument({ scope: Scope.USER, title: "T", content: "x".repeat((900 << 10) + 1) }), Code.InvalidArgument);
    });

    it("splits long text with overlap and quotes queries", () => {
      const chunks = chunkText(`${"a".repeat(3000)}\n\nshort`);
      expect(chunks.length).toBeGreaterThan(2);
      expect(chunks.every((c) => c.length <= 1200)).toBe(true);
      expect(chunks.at(-1)).toBe("short");
      expect(orQuery("the deploy & it's | !x")).toBe("'deploy' | 's' | 'x'");
      expect(orQuery("the")).toBe("'the'");
      expect(orQuery("  ")).toBe("");
    });
  });

  describe("task tracker", () => {
    it("keeps tasks to their owner", async () => {
      const t = (await alice.tracker.createTrackerTask({ title: "Ship it", description: "Before Friday", project: "core", sessionId: "s-1" })).task!;
      expect(t.status).toBe(TrackerStatus.TODO);
      expect(t.priority).toBe(TrackerPriority.NORMAL);
      expect(t.sessionIds).toEqual(["s-1"]);
      expect((await bob.tracker.listTrackerTasks({})).tasks).toEqual([]);
      expect((await admin.tracker.listTrackerTasks({})).tasks).toEqual([]);
      await expectCode(bob.tracker.getTrackerTask({ id: t.id }), Code.NotFound);
      await expectCode(admin.tracker.updateTrackerTask({ id: t.id, title: "x", blockedBy: [] }), Code.NotFound);
      await expectCode(bob.tracker.deleteTrackerTask({ id: t.id }), Code.NotFound);
      await alice.tracker.deleteTrackerTask({ id: t.id });
    });

    it("tracks status, sessions and blockers across updates", async () => {
      await expectCode(alice.tracker.createTrackerTask({ title: "  " }), Code.InvalidArgument);
      const first = (await alice.tracker.createTrackerTask({ title: "First", priority: TrackerPriority.LOW })).task!;
      const second = (await alice.tracker.createTrackerTask({ title: "Second", priority: TrackerPriority.HIGH, blockedBy: [first.id] })).task!;
      expect(second.blockedBy).toEqual([first.id]);
      await expectCode(alice.tracker.createTrackerTask({ title: "X", blockedBy: ["task_nope"] }), Code.InvalidArgument);
      await expectCode(alice.tracker.updateTrackerTask({ id: second.id, setBlockedBy: true, blockedBy: [second.id] }), Code.InvalidArgument);

      // Bob can't point at Alice's task.
      await expectCode(bob.tracker.createTrackerTask({ title: "Y", blockedBy: [first.id] }), Code.InvalidArgument);

      const started = (await alice.tracker.updateTrackerTask({ id: first.id, status: TrackerStatus.IN_PROGRESS, sessionId: "s-2", blockedBy: [] })).task!;
      expect(started.sessionIds).toEqual(["s-2"]);
      expect(started.closedAt).toBe("");
      const done = (await alice.tracker.updateTrackerTask({ id: first.id, status: TrackerStatus.DONE, sessionId: "s-2", blockedBy: [] })).task!;
      expect(done.sessionIds).toEqual(["s-2"]);
      expect(done.closedAt).not.toBe("");
      const reopened = (await alice.tracker.updateTrackerTask({ id: first.id, status: TrackerStatus.TODO, blockedBy: [] })).task!;
      expect(reopened.closedAt).toBe("");
      await alice.tracker.updateTrackerTask({ id: first.id, status: TrackerStatus.DONE, blockedBy: [] });

      const tasks = (await alice.tracker.listTrackerTasks({})).tasks;
      expect(tasks.map((x) => x.title)).toEqual(["Second", "First"]);
      expect((await alice.tracker.listTrackerTasks({ statuses: [TrackerStatus.DONE] })).tasks.map((x) => x.id)).toEqual([first.id]);
      expect((await alice.tracker.listTrackerTasks({ query: "seco" })).tasks.map((x) => x.id)).toEqual([second.id]);

      // Clearing and deleting blockers.
      await alice.tracker.deleteTrackerTask({ id: first.id });
      expect((await alice.tracker.getTrackerTask({ id: second.id })).task!.blockedBy).toEqual([]);
      await alice.tracker.deleteTrackerTask({ id: second.id });
    });
  });

  describe("transcript search", () => {
    const line = (uuid: string, text: string) => JSON.stringify({ type: "user", uuid, message: { role: "user", content: text } });

    it("finds words in the caller's transcripts only, one hit per session", async () => {
      const s1 = "d0000000-0000-4000-8000-000000000001";
      const s2 = "d0000000-0000-4000-8000-000000000002";
      await alice.sync.upsertSession({ session: { info: create(SessionInfoSchema, { id: s1, title: "Flaky test" }) } });
      await alice.sync.appendSessionEvents({
        sessionId: s1,
        entries: [
          { uuid: "u1", type: "user", entryJson: line("u1", "the kerberos ticket expired during the migration") },
          { uuid: "u2", type: "user", entryJson: line("u2", "kerberos again, renew the ticket") },
        ],
      });
      await bob.sync.upsertSession({ session: { info: create(SessionInfoSchema, { id: s2, title: "Bob" }) } });
      await bob.sync.appendSessionEvents({ sessionId: s2, entries: [{ uuid: "u3", type: "user", entryJson: line("u3", "kerberos for bob") }] });

      const hits = (await alice.transcripts.searchTranscripts({ query: "KERBEROS ticket" })).hits;
      expect(hits).toHaveLength(1);
      expect(hits[0]).toMatchObject({ sessionId: s1, title: "Flaky test" });
      expect(hits[0]!.snippet.toLowerCase()).toContain("kerberos");
      expect(hits[0]!.lastActivityAt).toMatch(/Z$/);

      // Prefixes match, all the words must be there, and nothing is an operator.
      expect((await alice.transcripts.searchTranscripts({ query: "kerb" })).hits).toHaveLength(1);
      expect((await alice.transcripts.searchTranscripts({ query: "kerberos unrelatedword" })).hits).toEqual([]);
      expect((await alice.transcripts.searchTranscripts({ query: "kerberos & !ticket" })).hits).toHaveLength(1);
      await expectCode(alice.transcripts.searchTranscripts({ query: "   " }), Code.InvalidArgument);

      expect((await bob.transcripts.searchTranscripts({ query: "migration" })).hits).toEqual([]);
      await alice.sync.deleteSession({ sessionId: s1 });
      await bob.sync.deleteSession({ sessionId: s2 });
      expect((await alice.transcripts.searchTranscripts({ query: "kerberos" })).hits).toEqual([]);
    });

    it("quotes and cuts", () => {
      expect(allWordsQuery("a'b  c:*")).toBe("'ab':* & 'c':*");
      expect(transcriptSnippet(`${"x ".repeat(100)}needle ${"y ".repeat(200)}`, "NEEDLE up")).toMatch(/^….*needle.*…$/);
      expect(transcriptSnippet("short needle", "needle")).toBe("short needle");
    });
  });
});
