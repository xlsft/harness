/**
 * Projects, boards and issues over the real routes, against a fake YouTrack: who sees what, inputs that pull once, outputs that
 * fail visibly, moves by two people, and the provider's behaviour without admin rights. docs/design/tracker.md.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { BoardScope, CardOutputStatus, CardPriority, CardRelationKind, ExternalState } from "../src/gen/harness/library/v1/boards_pb.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { ProjectLevel, ProjectScope, ProjectShareSubject } from "../src/gen/harness/library/v1/projects_pb.ts";
import { TrackerAuthMethod, TrackerConnectionScope } from "../src/gen/harness/library/v1/tracker_connections_pb.ts";
import { runBoardSyncOnce, wakeOutputsOnMove } from "../src/services/board-sync.ts";
import { tickerFrom } from "../src/services/projects.ts";
import { YouTrack } from "../src/tracker/youtrack.ts";
import { TrackerError, freeTextTerms } from "../src/tracker/provider.ts";
import { FakeYouTrack } from "./helpers/fake-youtrack.ts";
import { createUserAndLogin, deviceLogin, signIn, startTestServer, ADMIN, type TestServer } from "./helpers/server.ts";

async function expectCode(p: Promise<unknown>, code: Code) {
  const err = await p.then(
    () => undefined,
    (e: unknown) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  expect((err as ConnectError).code).toBe(code);
}

async function until(cond: () => Promise<boolean>): Promise<void> {
  const end = Date.now() + 10_000;
  while (!(await cond())) {
    if (Date.now() > end) throw new Error("condition not met in time");
    await new Promise((r) => setImmediate(r));
  }
}

describe("issues", () => {
  let ts: TestServer;
  let admin: ReturnType<TestServer["as"]>;
  let alice: ReturnType<TestServer["as"]>;
  let bob: ReturnType<TestServer["as"]>;
  let carol: ReturnType<TestServer["as"]>;
  let bobId: string;
  let carolId: string;
  let cookie: string;
  const yt = new FakeYouTrack();
  let conn: string;
  let seq = 0;
  const ticker = () => `T${++seq}X`;

  const adminConn = { scope: TrackerConnectionScope.ADMIN, id: "" };

  beforeAll(async () => {
    await yt.start();
    ts = await startTestServer({ HARNESS_BOARD_SYNC: "off" });
    admin = ts.as((await deviceLogin(ts)).accessToken);
    cookie = await signIn(ts.url, ADMIN.email, ADMIN.password);
    alice = ts.as((await createUserAndLogin(ts, cookie, "alice@issues.test")).accessToken);
    bob = ts.as((await createUserAndLogin(ts, cookie, "bob@issues.test")).accessToken);
    carol = ts.as((await createUserAndLogin(ts, cookie, "carol@issues.test")).accessToken);
    bobId = (await bob.auth.getMe({})).user!.id;
    carolId = (await carol.auth.getMe({})).user!.id;
    const c = await admin.trackers.putTrackerConnection({
      connection: { provider: "youtrack", name: "Company", baseUrl: yt.url, auth: TrackerAuthMethod.TOKEN, secret: yt.token, enabled: true, allowPrivateNetwork: true, knowledgeEnabled: true },
    });
    conn = c.connection!.id;
    adminConn.id = conn;
  }, 120_000);

  afterAll(async () => {
    await ts?.close();
    await yt.close();
  });

  beforeEach(() => {
    yt.issues = [];
    yt.articles = [];
    yt.comments = [];
    yt.failures = [];
    yt.regularUser = false;
    yt.ignoreArticleQuery = false;
  });

  const newProject = async (c: typeof alice, scope = ProjectScope.PERSONAL) => (await c.projects.createProject({ scope, metadata: { ticker: ticker(), name: "P" } })).project!;
  const newBoard = async (c: typeof alice, projectId: string, scope = BoardScope.PERSONAL, name = "B") => (await c.boards.createBoard({ projectId, scope, name })).board!;

  describe("connections", () => {
    it("are managed by admins, and neither secrets nor details reach anyone else", async () => {
      await expectCode(alice.trackers.putTrackerConnection({ connection: { provider: "youtrack", name: "x", baseUrl: yt.url, auth: TrackerAuthMethod.TOKEN, secret: "t", enabled: true, allowPrivateNetwork: true } }), Code.PermissionDenied);
      const list = await alice.trackers.listTrackerConnections({});
      const seen = list.connections.find((c) => c.id === conn)!;
      expect(seen.name).toBe("Company");
      expect(seen.baseUrl).toBe("");
      expect(seen.hasSecret).toBe(false);
      expect(JSON.stringify(list)).not.toContain("perm:good");
      const asAdmin = (await admin.trackers.listTrackerConnections({})).connections.find((c) => c.id === conn)!;
      expect(asAdmin.baseUrl).toBe(yt.url);
      expect(asAdmin.hasSecret).toBe(true);
      expect(JSON.stringify(asAdmin)).not.toContain("perm:good");
      await expectCode(alice.trackers.testTrackerConnection({ id: conn }), Code.PermissionDenied);
    });

    it("test without saving, and report failure as an answer", async () => {
      const good = await admin.trackers.testTrackerConnection({ id: conn });
      expect(good.ok).toBe(true);
      expect(good.login).toBe("alice");
      const bad = await admin.trackers.testTrackerConnection({ draft: { provider: "youtrack", baseUrl: yt.url, auth: TrackerAuthMethod.TOKEN, secret: "perm:wrong", allowPrivateNetwork: true, enabled: true } });
      expect(bad.ok).toBe(false);
      expect(bad.error).toContain("401");
      expect(bad.error).not.toContain("perm:wrong");
      const pw = await admin.trackers.testTrackerConnection({ draft: { provider: "youtrack", baseUrl: yt.url, auth: TrackerAuthMethod.PASSWORD, login: "alice", secret: "s3cret", allowPrivateNetwork: true, enabled: true } });
      expect(pw.ok).toBe(true);
      const wrong = await admin.trackers.testTrackerConnection({ draft: { provider: "youtrack", baseUrl: yt.url, auth: TrackerAuthMethod.PASSWORD, login: "alice", secret: "nope", allowPrivateNetwork: true, enabled: true } });
      expect(wrong.ok).toBe(false);
    });

    it("run the query of an input's form", async () => {
      yt.issues = [{ id: "D-1", project: "D", summary: "One", stage: "Backlog" }, { id: "D-2", project: "D", summary: "Two", stage: "Review" }];
      const r = await alice.trackers.searchConnectionIssues({ connectionId: conn, query: "Stage: Backlog" });
      expect(r.issues.map((i) => i.id)).toEqual(["D-1"]);
    });

    it("list a project's fields for a regular user from recent issues", async () => {
      yt.issues = [{ id: "D-1", project: "D", summary: "One", stage: "Backlog" }];
      yt.regularUser = true;
      const r = await alice.trackers.listConnectionFields({ connectionId: conn, project: "D" });
      expect(r.fields.find((f) => f.name === "Stage")?.values).toEqual(["Backlog", "Develop", "Review"]);
    });

    it("authorize over OAuth with PKCE, keeping the client's state sealed", async () => {
      const c = (await admin.trackers.putTrackerConnection({ connection: { provider: "youtrack", name: "OAuth", baseUrl: yt.url, auth: TrackerAuthMethod.OAUTH, oauthClientId: "cli", enabled: true, allowPrivateNetwork: true } })).connection!;
      expect((await admin.trackers.testTrackerConnection({ id: c.id })).ok).toBe(false);
      const { authorizeUrl } = await admin.trackers.startTrackerOAuth({ connectionId: c.id });
      const u = new URL(authorizeUrl);
      expect(u.searchParams.get("code_challenge_method")).toBe("S256");
      expect(u.searchParams.get("access_type")).toBe("offline");
      yt.oauthCodes.set("code1", "x");
      const res = await fetch(`${ts.url}/api/tracker/oauth/callback?code=code1&state=${encodeURIComponent(u.searchParams.get("state")!)}`);
      expect(res.status).toBe(200);
      expect((await admin.trackers.testTrackerConnection({ id: c.id })).ok).toBe(true);
      expect((await fetch(`${ts.url}/api/tracker/oauth/callback?code=x&state=forged`)).status).toBe(400);
      await admin.trackers.deleteTrackerConnection({ id: c.id });
    });
  });

  describe("projects", () => {
    it("exist only for those holding a level, admins included", async () => {
      const p = await newProject(alice);
      expect(p.level).toBe(ProjectLevel.MANAGE);
      await expectCode(bob.projects.getProject({ id: p.id }), Code.NotFound);
      await expectCode(admin.projects.getProject({ id: p.id }), Code.NotFound);
      expect((await admin.projects.listProjects({})).projects.map((x) => x.id)).not.toContain(p.id);
      await expectCode(bob.projects.updateProject({ id: p.id, name: "x" }), Code.NotFound);
      await expectCode(alice.projects.createProject({ scope: ProjectScope.GLOBAL, metadata: { ticker: "NOPE", name: "x" } }), Code.PermissionDenied);
      await expectCode(alice.projects.createProject({ scope: ProjectScope.PERSONAL, metadata: { ticker: "bad ticker", name: "x" } }), Code.InvalidArgument);
    });

    it("enforce view, edit and manage per user, group, role and everyone", async () => {
      const p = await newProject(alice);
      const board = await newBoard(alice, p.id, BoardScope.SHARED);
      const card = (await alice.boards.createCard({ boardId: board.id, title: "Shared work" })).card!;

      await alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.USER, subjectId: bobId, level: ProjectLevel.VIEW }] });
      expect((await bob.boards.getCard({ id: card.id })).card!.title).toBe("Shared work");
      await expectCode(bob.boards.updateCard({ id: card.id, title: "x" }), Code.PermissionDenied);
      await expectCode(bob.boards.moveCard({ id: card.id, stageId: board.stages[1]!.id }), Code.PermissionDenied);
      await expectCode(bob.boards.commentCard({ id: card.id, text: "hi" }), Code.PermissionDenied);
      await expectCode(bob.boards.createCard({ boardId: board.id, title: "x" }), Code.PermissionDenied);
      await expectCode(bob.boards.commentCard({ id: card.id, text: "hi", onExternal: true }), Code.FailedPrecondition);
      await expectCode(carol.boards.getCard({ id: card.id }), Code.NotFound);
      expect((await bob.projects.getProject({ id: p.id })).project!.shares).toEqual([]);

      await alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.USER, subjectId: bobId, level: ProjectLevel.EDIT }] });
      await bob.boards.moveCard({ id: card.id, stageId: board.stages[1]!.id });
      await expectCode(bob.boards.updateBoard({ id: board.id, name: "x", stages: board.stages }), Code.PermissionDenied);
      await expectCode(bob.projects.setProjectShares({ id: p.id, shares: [] }), Code.PermissionDenied);

      await alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.USER, subjectId: bobId, level: ProjectLevel.MANAGE }] });
      await bob.boards.updateBoard({ id: board.id, name: "Renamed", stages: board.stages });
      await bob.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.EVERYONE, level: ProjectLevel.VIEW }] });
      expect((await carol.boards.getCard({ id: card.id })).card!.id).toBe(card.id);

      await alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.ROLE, subjectId: "admin", level: ProjectLevel.VIEW }] });
      expect((await admin.projects.getProject({ id: p.id })).project!.level).toBe(ProjectLevel.VIEW);
      await expectCode(carol.projects.getProject({ id: p.id }), Code.NotFound);

      const g = await admin.directory.createGroup({ name: `g${++seq}` });
      await admin.directory.setGroupMembers({ id: g.group!.id, userIds: [carolId] });
      await alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.GROUP, subjectId: g.group!.id, level: ProjectLevel.EDIT }] });
      expect((await carol.projects.getProject({ id: p.id })).project!.level).toBe(ProjectLevel.EDIT);
      const members = (await alice.projects.listProjectMembers({ id: p.id })).members.map((m) => m.userId);
      expect(members).toContain(carolId);
      await expectCode(alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.USER, subjectId: "nobody", level: ProjectLevel.VIEW }] }), Code.InvalidArgument);
    });

    it("default global projects to everyone at edit, with admins managing", async () => {
      const p = (await admin.projects.createProject({ scope: ProjectScope.GLOBAL, metadata: { ticker: ticker(), name: "Company" } })).project!;
      expect((await alice.projects.getProject({ id: p.id })).project!.level).toBe(ProjectLevel.EDIT);
      await expectCode(alice.projects.deleteProject({ id: p.id }), Code.PermissionDenied);
      await admin.projects.deleteProject({ id: p.id });
    });

    it("map onto a provider project with local values over its own", async () => {
      yt.issues = [{ id: "DEMO-1", project: "DEMO", summary: "One", stage: "Backlog" }];
      const listed = await alice.projects.listProviderProjects({ connectionId: conn });
      const prov = listed.projects.find((x) => x.key === "DEMO")!;
      expect(prov.metadata!.name).toBe("Project DEMO");
      const p = (await alice.projects.createProject({ scope: ProjectScope.PERSONAL, connection: adminConn, externalKey: "DEMO", provider: prov.metadata, metadata: {} })).project!;
      expect(p.metadata!.ticker).toBe("DEMO");
      expect(p.metadata!.name).toBe("Project DEMO");
      expect(p.local!.name).toBe("");
      const changed = (await alice.projects.updateProject({ id: p.id, name: "Mine", color: "#112233" })).project!;
      expect(changed.metadata!.name).toBe("Mine");
      expect(changed.provider!.name).toBe("Project DEMO");
      const back = (await alice.projects.updateProject({ id: p.id, name: "" })).project!;
      expect(back.metadata!.name).toBe("Project DEMO");
      await expectCode(alice.projects.createProject({ scope: ProjectScope.PERSONAL, connection: adminConn, externalKey: "DEMO", provider: prov.metadata, metadata: {} }), Code.FailedPrecondition);
      await expectCode(admin.projects.createProject({ scope: ProjectScope.GLOBAL, connection: { scope: TrackerConnectionScope.DEVICE, id: "tc_x" }, externalKey: "K", provider: { name: "K", ticker: "K2" }, metadata: {} }), Code.InvalidArgument);
      await alice.projects.deleteProject({ id: p.id });
    });

    it("make a first project on demand, once", async () => {
      const first = (await carol.projects.ensureDefaultProject({})).project!;
      expect(first.metadata!.ticker).toBe("MY");
      expect((await carol.projects.ensureDefaultProject({})).project!.id).toBe(first.id);
      expect((await carol.boards.listBoards({ projectId: first.id })).boards).toHaveLength(1);
    });

    it("cut a provider's name down to a ticker", () => {
      expect(tickerFrom("demo-1")).toBe("DEMO1");
      expect(tickerFrom("9a")).toBe("AX");
      expect(tickerFrom("")).toBe("PX");
      expect(tickerFrom("averylongprojectname")).toBe("AVERYLONGP");
    });
  });

  describe("issues as such", () => {
    it("get keys from the ticker and can be found by them", async () => {
      const p = await newProject(alice);
      const b = await newBoard(alice, p.id);
      const one = (await alice.boards.createCard({ boardId: b.id, title: "One" })).card!;
      const two = (await alice.boards.createCard({ boardId: b.id, title: "Two" })).card!;
      expect(one.key).toBe(`${p.metadata!.ticker}-1`);
      expect(two.key).toBe(`${p.metadata!.ticker}-2`);
      expect((await alice.boards.getCard({ id: two.key })).card!.id).toBe(two.id);
      await expectCode(bob.boards.getCard({ id: two.key }), Code.NotFound);
      await alice.boards.deleteCard({ id: two.id });
      expect((await alice.boards.createCard({ boardId: b.id, title: "Three" })).card!.key).toBe(`${p.metadata!.ticker}-3`);
    });

    it("carry priority, labels, assignees, due dates and history", async () => {
      const p = await newProject(alice);
      await alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.USER, subjectId: bobId, level: ProjectLevel.EDIT }] });
      const b = await newBoard(alice, p.id, BoardScope.SHARED);
      const c = (await alice.boards.createCard({ boardId: b.id, title: "Work", priority: CardPriority.HIGH, labels: ["ui", "ui", " bug "], assigneeIds: ["me"], dueAt: "2030-01-02T00:00:00Z" })).card!;
      expect(c.priority).toBe(CardPriority.HIGH);
      expect(c.labels).toEqual(["ui", "bug"]);
      expect(c.assigneeIds).toHaveLength(1);
      const up = (await bob.boards.updateCard({ id: c.id, priority: CardPriority.URGENT, setAssignees: true, assigneeIds: [bobId], setLabels: true, labels: ["x"], dueAt: "" })).card!;
      expect(up.dueAt).toBe("");
      await bob.boards.commentCard({ id: c.id, text: "On it" });
      const read = await alice.boards.getCard({ id: c.id });
      expect(read.comments.map((x) => x.text)).toEqual(["On it"]);
      expect(read.comments[0]!.authorName).toBeTruthy();
      expect(read.card!.commentCount).toBe(1);
      expect(read.activity.map((a) => a.kind)).toEqual(expect.arrayContaining(["created", "priority", "assigned", "labeled", "due", "commented"]));
      await expectCode(carol.boards.deleteCardComment({ id: read.comments[0]!.id }), Code.NotFound);
      await alice.boards.deleteCardComment({ id: read.comments[0]!.id });
      expect((await alice.boards.getCard({ id: c.id })).card!.commentCount).toBe(0);
    });

    it("relate to each other, without revealing what the caller can't see", async () => {
      const p = await newProject(alice);
      await alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.USER, subjectId: bobId, level: ProjectLevel.VIEW }] });
      const shared = await newBoard(alice, p.id, BoardScope.SHARED);
      const own = await newBoard(alice, p.id, BoardScope.PERSONAL);
      const a = (await alice.boards.createCard({ boardId: shared.id, title: "A" })).card!;
      const b = (await alice.boards.createCard({ boardId: shared.id, title: "B" })).card!;
      const secret = (await alice.boards.createCard({ boardId: own.id, title: "Secret" })).card!;
      const r = (await alice.boards.relateCard({ id: a.id, kind: CardRelationKind.BLOCKED_BY, other: b.key })).card!;
      expect(r.relations.map((x) => [x.kind, x.key])).toEqual([[CardRelationKind.BLOCKED_BY, b.key]]);
      expect((await alice.boards.getCard({ id: b.id })).card!.relations[0]!.kind).toBe(CardRelationKind.BLOCKS);
      await expectCode(alice.boards.relateCard({ id: b.id, kind: CardRelationKind.BLOCKED_BY, other: a.id }), Code.FailedPrecondition);
      await expectCode(alice.boards.relateCard({ id: a.id, kind: CardRelationKind.RELATES, other: a.id }), Code.InvalidArgument);
      await alice.boards.relateCard({ id: a.id, kind: CardRelationKind.RELATES, other: secret.id });
      expect((await alice.boards.getCard({ id: a.id })).card!.relations).toHaveLength(2);
      expect((await bob.boards.getCard({ id: a.id })).card!.relations.map((x) => x.key)).toEqual([b.key]);
      expect((await alice.boards.listCards({ filter: { projectId: p.id, blocked: true } })).cards.map((x) => x.id)).toEqual([a.id]);
      await alice.boards.relateCard({ id: a.id, kind: CardRelationKind.BLOCKED_BY, other: b.id, remove: true });
      expect((await alice.boards.listCards({ filter: { projectId: p.id, blocked: true } })).cards).toEqual([]);
    });

    it("filter, sort and keep saved views per user", async () => {
      const p = await newProject(alice);
      const b = await newBoard(alice, p.id);
      const low = (await alice.boards.createCard({ boardId: b.id, title: "Small thing", priority: CardPriority.LOW, labels: ["a"] })).card!;
      const top = (await alice.boards.createCard({ boardId: b.id, title: "Big thing", priority: CardPriority.URGENT, labels: ["a", "b"], assigneeIds: ["me"], dueAt: "2030-01-01T00:00:00Z" })).card!;
      const list = async (filter: object) => (await alice.boards.listCards({ filter: { projectId: p.id, ...filter } })).cards.map((c) => c.id);
      expect(await list({ query: "big" })).toEqual([top.id]);
      expect(await list({ query: top.key })).toEqual([top.id]);
      expect(await list({ labels: ["a", "b"] })).toEqual([top.id]);
      expect(await list({ assigneeIds: ["me"] })).toEqual([top.id]);
      expect(await list({ unassigned: true })).toEqual([low.id]);
      expect(await list({ priorities: [CardPriority.URGENT] })).toEqual([top.id]);
      expect(await list({ dueBefore: "2031-01-01T00:00:00Z" })).toEqual([top.id]);
      expect(await list({ sort: "priority" })).toEqual([top.id, low.id]);
      const v = (await alice.boards.saveView({ name: "Urgent", filter: { projectId: p.id, priorities: [CardPriority.URGENT] } })).view!;
      expect((await alice.boards.listSavedViews({})).views[0]!.filter!.priorities).toEqual([CardPriority.URGENT]);
      expect((await bob.boards.listSavedViews({})).views).toEqual([]);
      await expectCode(bob.boards.saveView({ id: v.id, name: "x" }), Code.NotFound);
      await bob.boards.deleteSavedView({ id: v.id });
      expect((await alice.boards.listSavedViews({})).views).toHaveLength(1);
      await alice.boards.deleteSavedView({ id: v.id });
    });

    it("keep personal boards to their owner, in lists too", async () => {
      const p = await newProject(alice);
      await alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.USER, subjectId: bobId, level: ProjectLevel.MANAGE }] });
      const own = await newBoard(alice, p.id, BoardScope.PERSONAL);
      const c = (await alice.boards.createCard({ boardId: own.id, title: "Mine alone" })).card!;
      await expectCode(bob.boards.getBoard({ id: own.id }), Code.NotFound);
      await expectCode(bob.boards.getCard({ id: c.id }), Code.NotFound);
      expect((await bob.boards.listCards({ filter: { projectId: p.id } })).cards).toEqual([]);
      expect((await bob.boards.listBoards({ projectId: p.id })).boards).toEqual([]);
    });

    it("tell a session where to start, and link its PR", async () => {
      const p = await newProject(alice);
      const b = await newBoard(alice, p.id);
      const c = (await alice.boards.createCard({ boardId: b.id, title: "Fix the upload!", description: "Details" })).card!;
      const s = await alice.boards.prepareCardSession({ id: c.key });
      expect(s.branch).toBe(`issue/${c.key.toLowerCase()}-fix-the-upload`);
      expect(s.prompt).toContain(c.key);
      expect(s.projectId).toBe(p.id);
      await alice.boards.linkCard({ id: c.id, sessionId: "s-1", branch: s.branch });
      const r = await alice.boards.linkSessionPullRequest({ sessionId: "s-1", prUrl: "https://example.test/pr/1" });
      expect(r.cardIds).toEqual([c.id]);
      const after = (await alice.boards.getCard({ id: c.id })).card!;
      expect(after.prUrls).toEqual(["https://example.test/pr/1"]);
      expect(after.branch).toBe(s.branch);
      expect((await bob.boards.linkSessionPullRequest({ sessionId: "s-1", prUrl: "https://example.test/pr/2" })).cardIds).toEqual([]);
    });
  });

  describe("boards", () => {
    it("follow the owner's example: queue from YouTrack, internal progress, done sets a stage", async () => {
      const p = (await admin.projects.createProject({ scope: ProjectScope.GLOBAL, metadata: { ticker: ticker(), name: "Company" } })).project!;
      const board = (
        await admin.boards.createBoard({
          projectId: p.id,
          scope: BoardScope.SHARED,
          name: "Flow",
          stages: [
            { name: "Queue", inputs: [{ connection: adminConn, query: "Stage: Backlog" }] },
            { name: "In progress" },
            { name: "Done", outputs: [{ connection: adminConn, fields: [{ name: "Stage", value: "Review" }], comment: "Finished" }] },
          ],
        })
      ).board!;
      const [queue, progress, done] = board.stages as [(typeof board.stages)[0], (typeof board.stages)[0], (typeof board.stages)[0]];
      yt.issues = [{ id: "D-1", project: "D", summary: "One", stage: "Backlog" }, { id: "D-2", project: "D", summary: "Two", stage: "Review" }];

      await runBoardSyncOnce();
      let cards = (await alice.boards.getBoard({ id: board.id })).cards;
      expect(cards.map((c) => [c.key, c.stageId])).toEqual([["D-1", queue.id]]);
      const card = cards[0]!;
      expect(card.external!.state).toBe(ExternalState.LINKED);
      expect(card.external!.url).toBe(`${yt.url}/issue/D-1`);
      expect(yt.issues[0]!.stage).toBe("Backlog");

      // Moving out does not bring it back, nor does polling again.
      await alice.boards.moveCard({ id: card.id, stageId: progress.id });
      await alice.boards.syncBoard({ id: board.id });
      cards = (await alice.boards.getBoard({ id: board.id })).cards;
      expect(cards.map((c) => c.stageId)).toEqual([progress.id]);
      expect(yt.issues[0]!.stage).toBe("Backlog");

      // Deleting it does not either: once means once.
      const other = (await alice.boards.createCard({ boardId: board.id, title: "No issue" })).card!;
      await alice.boards.moveCard({ id: other.id, stageId: done.id });
      expect((await alice.boards.getCard({ id: other.id })).card!.output).toBeUndefined();

      // Entering the stage applies the output.
      wakeOutputsOnMove();
      await alice.boards.moveCard({ id: card.id, stageId: done.id });
      await until(async () => (await alice.boards.getCard({ id: card.id })).card!.output?.status === CardOutputStatus.DONE);
      expect(yt.issues[0]!.stage).toBe("Review");
      expect(yt.comments).toEqual([{ issue: "D-1", text: "Finished" }]);
      await admin.projects.deleteProject({ id: p.id });
    });

    it("make failures visible and retryable, and retry transient ones", async () => {
      const p = await newProject(alice);
      const board = (
        await alice.boards.createBoard({
          projectId: p.id,
          scope: BoardScope.PERSONAL,
          name: "F",
          stages: [{ name: "A", inputs: [{ connection: adminConn, query: "Stage: Backlog" }] }, { name: "B", outputs: [{ connection: adminConn, fields: [{ name: "Stage", value: "Nonsense" }] }] }, { name: "C" }],
        })
      ).board!;
      yt.issues = [{ id: "E-1", project: "E", summary: "One", stage: "Backlog" }];
      await runBoardSyncOnce();
      const card = (await alice.boards.getBoard({ id: board.id })).cards[0]!;
      wakeOutputsOnMove();
      await alice.boards.moveCard({ id: card.id, stageId: board.stages[1]!.id });
      await until(async () => (await alice.boards.getCard({ id: card.id })).card!.output?.status === CardOutputStatus.FAILED);
      const failed = (await alice.boards.getCard({ id: card.id })).card!.output!;
      expect(failed.error).toContain("Nonsense");
      expect(yt.issues[0]!.stage).toBe("Backlog");

      // Fix the stage, retry from the card: the copied value still fails, so it is what the move said that counts.
      await alice.boards.retryCardOutput({ cardId: card.id });
      await until(async () => (await alice.boards.getCard({ id: card.id })).card!.output?.status === CardOutputStatus.FAILED);
      await expectCode(bob.boards.retryCardOutput({ cardId: card.id }), Code.NotFound);

      // Leaving the stage takes the failure off the card.
      await alice.boards.moveCard({ id: card.id, stageId: board.stages[2]!.id });
      expect((await alice.boards.getCard({ id: card.id })).card!.output).toBeUndefined();

      // A transient error stays pending with the error showing, and is tried again later.
      yt.failures = [500, 500, 500, 500, 500];
      await alice.boards.moveCard({ id: card.id, stageId: board.stages[1]!.id });
      await until(async () => {
        const o = (await alice.boards.getCard({ id: card.id })).card!.output;
        return o?.status === CardOutputStatus.PENDING && o.attempts === 1;
      });
      expect((await alice.boards.getCard({ id: card.id })).card!.output!.error).toContain("500");
    });

    it("shows a failing poll on the input, and refuses device connections on shared boards", async () => {
      const p = await newProject(alice);
      await expectCode(
        alice.boards.createBoard({ projectId: p.id, scope: BoardScope.SHARED, name: "x", stages: [{ name: "A", inputs: [{ connection: { scope: TrackerConnectionScope.DEVICE, id: "tc_1" }, query: "q" }] }] }),
        Code.InvalidArgument,
      );
      await expectCode(alice.boards.createBoard({ projectId: p.id, scope: BoardScope.PERSONAL, name: "x", stages: [{ name: "A", inputs: [{ connection: adminConn, query: " " }] }] }), Code.InvalidArgument);
      await expectCode(alice.boards.createBoard({ projectId: p.id, scope: BoardScope.PERSONAL, name: "x", stages: [{ name: "A", outputs: [{ connection: adminConn }] }] }), Code.InvalidArgument);
      const board = (await alice.boards.createBoard({ projectId: p.id, scope: BoardScope.PERSONAL, name: "x", stages: [{ name: "A", inputs: [{ connection: adminConn, query: "Stage: Backlog" }] }] })).board!;
      yt.failures = [404];
      await alice.boards.syncBoard({ id: board.id });
      const input = (await alice.boards.getBoard({ id: board.id })).board!.stages[0]!.inputs[0]!;
      expect(input.lastError).toContain("404");
      expect(input.lastPolledAt).not.toBe("");
      expect(input.intervalSeconds).toBe(300);
    });

    it("lets the first of two moves win", async () => {
      const p = await newProject(alice);
      await alice.projects.setProjectShares({ id: p.id, shares: [{ subject: ProjectShareSubject.USER, subjectId: bobId, level: ProjectLevel.EDIT }] });
      const board = await newBoard(alice, p.id, BoardScope.SHARED);
      const c = (await alice.boards.createCard({ boardId: board.id, title: "Race" })).card!;
      await alice.boards.moveCard({ id: c.id, stageId: board.stages[1]!.id, expectedVersion: c.version });
      await expectCode(bob.boards.moveCard({ id: c.id, stageId: board.stages[2]!.id, expectedVersion: c.version }), Code.FailedPrecondition);
      expect((await bob.boards.getCard({ id: c.id })).card!.stageId).toBe(board.stages[1]!.id);
      await bob.boards.moveCard({ id: c.id, stageId: board.stages[2]!.id });
      expect((await alice.boards.getCard({ id: c.id })).card!.stageId).toBe(board.stages[2]!.id);
    });

    it("orders cards between neighbours", async () => {
      const p = await newProject(alice);
      const board = await newBoard(alice, p.id);
      const s = board.stages[0]!.id;
      const a = (await alice.boards.createCard({ boardId: board.id, title: "a" })).card!;
      const b = (await alice.boards.createCard({ boardId: board.id, title: "b" })).card!;
      const c = (await alice.boards.createCard({ boardId: board.id, title: "c" })).card!;
      await alice.boards.moveCard({ id: c.id, stageId: s, beforeCardId: b.id });
      await alice.boards.moveCard({ id: a.id, stageId: s });
      expect((await alice.boards.getBoard({ id: board.id })).cards.map((x) => x.title)).toEqual(["c", "b", "a"]);
    });

    it("keeps cards when a stage goes, and takes them to the first one", async () => {
      const p = await newProject(alice);
      const board = await newBoard(alice, p.id);
      const c = (await alice.boards.createCard({ boardId: board.id, title: "x", stageId: board.stages[2]!.id })).card!;
      const up = (await alice.boards.updateBoard({ id: board.id, name: "New", stages: board.stages.slice(0, 2) })).board!;
      expect(up.stages).toHaveLength(2);
      expect((await alice.boards.getCard({ id: c.id })).card!.stageId).toBe(board.stages[0]!.id);
      await alice.boards.deleteBoard({ id: board.id });
      await expectCode(alice.boards.getCard({ id: c.id }), Code.NotFound);
    });

    it("notices an issue that disappeared, and when it comes back; a failing connection says nothing about the issue", async () => {
      const p = await newProject(alice);
      const board = await newBoard(alice, p.id);
      const c = (await alice.boards.createCard({ boardId: board.id, title: "Linked" })).card!;
      yt.issues = [{ id: "M-1", project: "M", summary: "Theirs", stage: "Backlog" }];
      await alice.boards.linkCard({ id: c.id, connection: adminConn, externalId: "M-1", externalUrl: `${yt.url}/issue/M-1` });
      const { getDb, schema } = await import("../src/db/index.ts");
      const age = () => getDb().update(schema.boardCard).set({ externalCheckedAt: new Date(0) });
      const state = async () => (await alice.boards.getCard({ id: c.id })).card!.external!.state;
      await age();
      yt.failures = [500, 500, 500, 500];
      await runBoardSyncOnce();
      expect(await state()).toBe(ExternalState.LINKED);
      yt.failures = [];
      yt.issues = [];
      await age();
      await runBoardSyncOnce();
      expect(await state()).toBe(ExternalState.MISSING);
      yt.issues = [{ id: "M-1", project: "M", summary: "Theirs", stage: "Backlog" }];
      await age();
      await runBoardSyncOnce();
      expect(await state()).toBe(ExternalState.LINKED);
      // A title edited here stays; the live issue is still shown.
      expect((await alice.boards.getCard({ id: c.id })).card!.title).toBe("Linked");
      expect((await alice.boards.getCard({ id: c.id })).issue!.title).toBe("Theirs");
    });

    it("gives a client only its owner's device work, and takes reports only for it", async () => {
      const dev = { scope: TrackerConnectionScope.DEVICE, id: "tc_dev1" };
      const p = await newProject(alice);
      const board = (
        await alice.boards.createBoard({
          projectId: p.id,
          scope: BoardScope.PERSONAL,
          name: "Dev",
          stages: [{ name: "In", inputs: [{ connection: dev, query: "Stage: Backlog" }] }, { name: "Out", outputs: [{ connection: dev, fields: [{ name: "Stage", value: "Review" }] }] }],
        })
      ).board!;
      expect((await bob.boards.claimDeviceWork({ connectionIds: [dev.id] })).inputs).toEqual([]);
      const claim = await alice.boards.claimDeviceWork({ connectionIds: [dev.id] });
      expect(claim.inputs.map((i) => i.query)).toEqual(["Stage: Backlog"]);
      expect((await alice.boards.claimDeviceWork({ connectionIds: [dev.id] })).inputs).toEqual([]);
      await bob.boards.reportDeviceWork({ inputs: [{ inputId: claim.inputs[0]!.inputId, issues: [{ id: "X-1", title: "stolen", url: "https://x" }] }] });
      expect((await alice.boards.getBoard({ id: board.id })).cards).toEqual([]);
      const rep = await alice.boards.reportDeviceWork({ inputs: [{ inputId: claim.inputs[0]!.inputId, issues: [{ id: "V-1", title: "From VPN", url: "https://vpn/issue/V-1" }] }] });
      expect(rep.newCards).toBe(1);
      const card = (await alice.boards.getBoard({ id: board.id })).cards[0]!;
      expect(card.key).toBe("V-1");

      await alice.boards.moveCard({ id: card.id, stageId: board.stages[1]!.id });
      await runBoardSyncOnce();
      expect((await alice.boards.getCard({ id: card.id })).card!.output!.status).toBe(CardOutputStatus.PENDING);
      const out = (await alice.boards.claimDeviceWork({ connectionIds: [dev.id] })).outputs;
      expect(out.map((o) => [o.externalId, o.fields[0]!.value])).toEqual([["V-1", "Review"]]);
      expect((await alice.boards.getCard({ id: card.id })).card!.output!.status).toBe(CardOutputStatus.RUNNING);
      await bob.boards.reportDeviceWork({ outputs: [{ outputId: out[0]!.outputId, error: "nope" }] });
      expect((await alice.boards.getCard({ id: card.id })).card!.output!.status).toBe(CardOutputStatus.RUNNING);
      await alice.boards.reportDeviceWork({ outputs: [{ outputId: out[0]!.outputId, error: "Denied" }] });
      expect((await alice.boards.getCard({ id: card.id })).card!.output!.error).toBe("Denied");

      await alice.boards.forgetDeviceConnection({ connectionId: dev.id });
      const after = await alice.boards.getBoard({ id: board.id });
      expect(after.board!.stages[0]!.inputs).toEqual([]);
      expect(after.cards[0]!.external!.state).toBe(ExternalState.DISCONNECTED);
    });

    it("loses what used a connection when it goes, and stops on a disabled one", async () => {
      const c2 = (await admin.trackers.putTrackerConnection({ connection: { provider: "youtrack", name: "Temp", baseUrl: yt.url, auth: TrackerAuthMethod.TOKEN, secret: yt.token, enabled: true, allowPrivateNetwork: true } })).connection!;
      const ref = { scope: TrackerConnectionScope.ADMIN, id: c2.id };
      const p = await newProject(alice);
      const board = (await alice.boards.createBoard({ projectId: p.id, scope: BoardScope.PERSONAL, name: "T", stages: [{ name: "A", inputs: [{ connection: ref, query: "Stage: Backlog" }] }] })).board!;
      yt.issues = [{ id: "Z-1", project: "Z", summary: "z", stage: "Backlog" }];
      await admin.trackers.putTrackerConnection({ connection: { id: c2.id, provider: "youtrack", name: "Temp", baseUrl: yt.url, auth: TrackerAuthMethod.TOKEN, enabled: false, allowPrivateNetwork: true } });
      await alice.boards.syncBoard({ id: board.id });
      expect((await alice.boards.getBoard({ id: board.id })).board!.stages[0]!.inputs[0]!.lastError).toContain("disabled");
      await admin.trackers.putTrackerConnection({ connection: { id: c2.id, provider: "youtrack", name: "Temp", baseUrl: yt.url, auth: TrackerAuthMethod.TOKEN, enabled: true, allowPrivateNetwork: true } });
      await alice.boards.syncBoard({ id: board.id });
      expect((await alice.boards.getBoard({ id: board.id })).cards).toHaveLength(1);
      await admin.trackers.deleteTrackerConnection({ id: c2.id });
      const after = await alice.boards.getBoard({ id: board.id });
      expect(after.board!.stages[0]!.inputs).toEqual([]);
      expect(after.cards[0]!.external!.state).toBe(ExternalState.DISCONNECTED);
    });
  });

  describe("knowledge from a provider", () => {
    it("searches live, says the source, reads by id and warns about a failing source", async () => {
      yt.articles = [{ id: "HELP-A-1", project: "HELP", summary: "Deploying", content: "Run the canary rollout\nThen wait" }, { id: "HELP-A-2", project: "HELP", summary: "Other", content: "Nothing here" }];
      await admin.knowledge.createKnowledgeDocument({ scope: Scope.ORG, title: "Internal", content: "Canary rollout internal notes" });
      const r = await alice.knowledge.searchKnowledge({ query: "canary" });
      expect(r.hits.map((h) => h.source!.name)).toEqual(["Internal, for everyone", "Company"]);
      const hit = r.hits.find((h) => h.source!.id === conn)!;
      expect(hit.documentId).toBe(`conn:${conn}:HELP-A-1`);
      expect(hit.snippet).toContain("canary rollout");
      expect(hit.url).toBe(`${yt.url}/articles/HELP-A-1`);
      const doc = await alice.knowledge.getKnowledgeDocument({ id: hit.documentId });
      expect(doc.content).toContain("Then wait");
      expect(doc.document!.editable).toBe(false);
      await expectCode(alice.knowledge.updateKnowledgeDocument({ id: hit.documentId, title: "x" }), Code.NotFound);

      // Nothing of it is stored.
      yt.articles = [];
      await expectCode(alice.knowledge.getKnowledgeDocument({ id: hit.documentId }), Code.Unavailable);

      yt.failures = [500, 500, 500, 500];
      const w = await alice.knowledge.searchKnowledge({ query: "canary" });
      expect(w.hits.map((h) => h.source!.id)).toEqual(["internal:org"]);
      expect(w.warnings[0]!.source!.id).toBe(conn);
      expect(w.warnings[0]!.message).not.toContain("500");
    });

    it("filters what an instance gives back when it ignores the query", async () => {
      yt.ignoreArticleQuery = true;
      yt.articles = [{ id: "A-1", project: "A", summary: "Zebra", content: "stripes" }, { id: "A-2", project: "A", summary: "Lion", content: "mane" }];
      const r = await alice.knowledge.searchKnowledge({ query: "zebra" });
      expect(r.hits.filter((h) => h.source!.id === conn).map((h) => h.title)).toEqual(["Zebra"]);
    });

    it("lists sources and browses a tree, and a source switched off is gone", async () => {
      yt.articles = [{ id: "H-1", project: "H", summary: "Top", content: "t" }, { id: "H-2", project: "H", summary: "Child", content: "c", parent: "H-1" }];
      const sources = (await alice.knowledge.listKnowledgeSources({})).sources;
      expect(sources.map((s) => s.ref!.id)).toEqual(["internal:org", "internal:user", conn]);
      expect(sources[0]!.writable).toBe(false);
      expect(sources[1]!.writable).toBe(true);
      const top = await alice.knowledge.browseKnowledgeSource({ sourceId: conn });
      expect(top.documents.map((d) => [d.title, d.hasChildren])).toEqual([["Top", true]]);
      const kids = await alice.knowledge.browseKnowledgeSource({ sourceId: conn, parentId: "H-1" });
      expect(kids.documents.map((d) => d.title)).toEqual(["Child"]);
      await admin.trackers.putTrackerConnection({ connection: { id: conn, provider: "youtrack", name: "Company", baseUrl: yt.url, auth: TrackerAuthMethod.TOKEN, enabled: true, allowPrivateNetwork: true, knowledgeEnabled: false } });
      expect((await alice.knowledge.listKnowledgeSources({})).sources.map((s) => s.ref!.id)).not.toContain(conn);
      await expectCode(alice.knowledge.getKnowledgeDocument({ id: `conn:${conn}:H-1` }), Code.NotFound);
      await admin.trackers.putTrackerConnection({ connection: { id: conn, provider: "youtrack", name: "Company", baseUrl: yt.url, auth: TrackerAuthMethod.TOKEN, enabled: true, allowPrivateNetwork: true, knowledgeEnabled: true } });
    });
  });

  describe("the provider", () => {
    const client = () => new YouTrack({ baseUrl: `${yt.url}/`, auth: "token", credentials: { token: yt.token }, allowPrivateNetwork: true, retryDelaysMs: [1, 1] });

    it("retries what did not take, but not a write that may have", async () => {
      yt.issues = [{ id: "R-1", project: "R", summary: "r", stage: "Backlog" }];
      yt.failures = [503];
      expect((await client().search("x", { limit: 5 })).issues).toHaveLength(1);
      yt.failures = [502];
      await expect(client().comment("R-1", "once")).rejects.toThrow(/502/);
      expect(yt.comments).toEqual([]);
    });

    it("classifies errors and leaves deleted comments out", async () => {
      await expect(client().get("NO-1")).rejects.toMatchObject({ kind: "not_found" });
      expect(new TrackerError("auth", "x").kind).toBe("auth");
      yt.issues = [{ id: "R-1", project: "R", summary: "r", stage: "Backlog" }];
      yt.comments = [{ issue: "R-1", text: "live" }, { issue: "R-1", text: "gone", deleted: true }];
      expect((await client().get("R-1")).comments.map((c) => c.text)).toEqual(["live"]);
    });

    it("takes plain words out of a query in the provider's syntax", () => {
      expect(freeTextTerms('project: HELP canary "exact phrase" #Unresolved tag:x')).toEqual(["exact phrase", "canary"]);
    });
  });
});
