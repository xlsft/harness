/**
 * Always-on hosts (docs/design/hosts.md): who may register and use a host, the work queue and its capacity, a team host's
 * supervisor and workers, narrowed tokens, git credentials from the connection, away jobs and mentions.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { randomUUID } from "node:crypto";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { PermissionMode } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { AgentHostIsolation, AgentHostScope, HostWorkKind, HostWorkState } from "../src/gen/harness/relay/v1/hosts_pb.ts";
import { matchesRepository } from "../src/relay/hosts.ts";
import { vcsIdle } from "../src/vcs/events.ts";
import { startFakeGitHub, type FakeGitHub } from "./helpers/fake-hosts.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";
import { githubSignature } from "../src/vcs/webhooks.ts";

let ts: TestServer;
let gh: FakeGitHub;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let alice: Awaited<ReturnType<typeof deviceLogin>>;
let bob: Awaited<ReturnType<typeof deviceLogin>>;
let carol: Awaited<ReturnType<typeof deviceLogin>>;
let connId: string;
const REPO = "127.0.0.1/acme/widgets";

async function expectError(p: Promise<unknown>, code: Code) {
  const err = await p.then(
    () => undefined,
    (e) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  expect((err as ConnectError).code).toBe(code);
  return err as ConnectError;
}

/** A device token as a daemon uses it. */
async function daemonClient(deviceToken: string) {
  const { accessToken } = await ts.auth.refreshAccessToken({ deviceToken });
  return ts.as(accessToken);
}

const daemonInfo = (isolation: AgentHostIsolation, extra: Record<string, unknown> = {}) => ({ name: "", isolation, sandboxDriver: "linux", os: "linux", arch: "amd64", clientVersion: "test", cpuCount: 8, ...extra });

beforeAll(async () => {
  ts = await startTestServer({ HARNESS_HOST_AWAY_GRACE_S: "0.001", HARNESS_HOST_LEASE_MS: "8000", HARNESS_RELAY_HEARTBEAT_MS: "300" });
  gh = await startFakeGitHub();
  admin = await deviceLogin(ts);
  alice = await createUserAndLogin(ts, admin.cookie, "alice@test.local");
  bob = await createUserAndLogin(ts, admin.cookie, "bob@test.local");
  carol = await createUserAndLogin(ts, admin.cookie, "carol@test.local");
  const ADM = ts.as(admin.accessToken).vcs;
  const m = await ADM.getGitHubAppManifest({ baseUrl: gh.url, appName: "harness test" });
  const done = await ADM.completeGitHubAppManifest({ code: gh.manifestCode(), state: m.state });
  connId = done.connection!.id;
  // alice connects her account on the code host.
  const start = await ts.as(alice.accessToken).vcs.startUserAuth({ connectionId: connId });
  const auth = await fetch(start.authorizationUrl, { redirect: "manual" });
  await fetch(auth.headers.get("location")!);
});

afterAll(async () => {
  await ts.close();
  await gh.close();
});

describe("personal hosts", () => {
  let token: string;
  let hostId: string;

  it("registers from the app, returns its token once and shows only to its owner", async () => {
    const A = ts.as(alice.accessToken).hosts;
    const made = await A.createAgentHost({ name: "alice-box", scope: AgentHostScope.USER, maxSessions: 1 });
    expect(made.deviceToken).toMatch(/^hdt_/);
    token = made.deviceToken;
    hostId = made.host!.id;
    expect((await A.listAgentHosts({})).hosts.map((h) => h.id)).toContain(hostId);
    expect((await ts.as(bob.accessToken).hosts.listAgentHosts({})).hosts.map((h) => h.id)).not.toContain(hostId);
    await expectError(ts.as(bob.accessToken).hosts.startHostWork({ hostId, repositoryKey: REPO, prompt: "x" }), Code.NotFound);
    await expectError(ts.as(bob.accessToken).hosts.listAgentHosts({ all: true }), Code.PermissionDenied);
    const all = await ts.as(admin.accessToken).hosts.listAgentHosts({ all: true });
    expect(all.hosts.find((h) => h.id === hostId)?.ownerName).toBe("alice");
  });

  it("queues work while the host is away, then a claim takes it and the host reports on it", async () => {
    const A = ts.as(alice.accessToken).hosts;
    const key = randomUUID();
    const work = (await A.startHostWork({ hostId, repositoryKey: REPO, prompt: "do it", idempotencyKey: key })).work!;
    expect(work.state).toBe(HostWorkState.QUEUED);
    expect((await A.startHostWork({ hostId, repositoryKey: REPO, prompt: "do it", idempotencyKey: key })).work!.id).toBe(work.id);
    await expectError(A.startHostWork({ hostId, repositoryKey: REPO, prompt: "x", permissionMode: PermissionMode.BYPASS_PERMISSIONS }), Code.PermissionDenied);

    const D = (await daemonClient(token)).hostAgent;
    const reg = await D.registerAgentHost(daemonInfo(AgentHostIsolation.NONE));
    expect(reg.supervisor).toBe(false);
    expect(reg.host!.id).toBe(hostId);
    const first = await D.claimHostWork({ waitSeconds: 0 });
    expect(first.work?.id).toBe(work.id);
    expect(first.work?.prompt).toBe("do it");
    expect(first.gitEmail).toBe("alice@test.local");
    // Capacity 1, and that one is claimed: the next waits.
    const second = (await A.startHostWork({ hostId, repositoryKey: REPO, prompt: "next" })).work!;
    expect((await D.claimHostWork({ waitSeconds: 0 })).work).toBeUndefined();
    await D.updateHostWork({ workId: work.id, state: HostWorkState.RUNNING, sessionId: "s-1", branch: "harness/x" });
    expect((await A.getHostWork({ id: work.id })).work).toMatchObject({ state: HostWorkState.RUNNING, sessionId: "s-1", branch: "harness/x" });
    // Running counts until a report says the session closed.
    expect((await D.claimHostWork({ waitSeconds: 0 })).work).toBeUndefined();
    await D.reportAgentHost({ workingSessions: 0 });
    expect((await D.claimHostWork({ waitSeconds: 0 })).work?.id).toBe(second.id);
    await D.updateHostWork({ workId: second.id, state: HostWorkState.FAILED, error: "boom" });
    expect((await A.getHostWork({ id: second.id })).work).toMatchObject({ state: HostWorkState.FAILED, error: "boom" });
    await D.updateHostWork({ workId: work.id, state: HostWorkState.DONE });
  });

  it("a long poll wakes for new work, and a lost claim goes back to the queue", { timeout: 60_000 }, async () => {
    const D = (await daemonClient(token)).hostAgent;
    const A = ts.as(alice.accessToken).hosts;
    const waiting = D.claimHostWork({ waitSeconds: 20 });
    const started = Date.now();
    const w = (await A.startHostWork({ hostId, repositoryKey: REPO, prompt: "again" })).work!;
    const got = await waiting;
    expect(got.work?.id).toBe(w.id);
    expect(Date.now() - started).toBeLessThan(10_000);
    // The lease here is short: nobody renews it, so the work is claimable again.
    await expect.poll(async () => (await D.claimHostWork({ waitSeconds: 0 })).work?.id, { timeout: 40000, interval: 200 }).toBe(w.id);
    expect((await A.getHostWork({ id: w.id })).work!.attempts).toBe(2);
    const cancelled = (await A.cancelHostWork({ id: w.id })).work!;
    expect(cancelled.state).toBe(HostWorkState.CANCELLED);
    expect((await D.updateHostWork({ workId: w.id, state: HostWorkState.CLAIMED })).cancelled).toBe(true);
  });

  it("host tokens carry no role: an admin's own host administers nothing", async () => {
    const made = await ts.as(admin.accessToken).hosts.createAgentHost({ name: "admin-box", scope: AgentHostScope.USER });
    const C = await daemonClient(made.deviceToken);
    expect(((await C.auth.getMe({})).user?.email)).toBe("admin@test.local");
    await expectError(C.hosts.listAgentHosts({ all: true }), Code.PermissionDenied);
    await ts.as(admin.accessToken).hosts.deleteAgentHost({ id: made.host!.id });
    await expectError(ts.auth.refreshAccessToken({ deviceToken: made.deviceToken }), Code.Unauthenticated);
  });

  it("a laptop's token does not turn into a host, one from `daemon login` does", async () => {
    await expectError(ts.as(bob.accessToken).hostAgent.registerAgentHost(daemonInfo(AgentHostIsolation.NONE)), Code.FailedPrecondition);
    await getDb().update(schema.device).set({ clientName: "harness-client daemon" }).where(eq(schema.device.id, bob.deviceId));
    const reg = await ts.as(bob.accessToken).hostAgent.registerAgentHost(daemonInfo(AgentHostIsolation.NONE, { name: "bob-box", maxSessions: 7 }));
    expect(reg.host).toMatchObject({ name: "bob-box", maxSessions: 7, scope: AgentHostScope.USER });
    await ts.as(bob.accessToken).hosts.deleteAgentHost({ id: reg.host!.id }).catch(() => {});
    bob = await deviceLogin(ts, "bob@test.local", "user-password-1");
  });
});

describe("team hosts", () => {
  let groupId: string;
  let hostId: string;
  let supervisorToken: string;

  it("only admins register them, for a group", async () => {
    await expectError(ts.as(alice.accessToken).hosts.createAgentHost({ name: "t", scope: AgentHostScope.TEAM, groupId: "x" }), Code.PermissionDenied);
    const group = await ts.as(admin.accessToken).directory.createGroup({ name: "team-a" });
    groupId = group.group!.id;
    await ts.as(admin.accessToken).directory.setGroupMembers({ id: groupId, userIds: [alice.user!.id, bob.user!.id] });
    const made = await ts.as(admin.accessToken).hosts.createAgentHost({ name: "team-box", scope: AgentHostScope.TEAM, groupId, maxSessions: 2, maxSessionsPerUser: 1, allowedRepositories: ["127.0.0.1/acme/*"] });
    hostId = made.host!.id;
    supervisorToken = made.deviceToken;
    expect((await ts.as(alice.accessToken).hosts.listAgentHosts({})).hosts.map((h) => h.id)).toContain(hostId);
    expect((await ts.as(carol.accessToken).hosts.listAgentHosts({})).hosts.map((h) => h.id)).not.toContain(hostId);
  });

  it("the supervisor's token reaches nothing but the host service", async () => {
    const S = await daemonClient(supervisorToken);
    await expectError(S.relay.listHosts({}), Code.PermissionDenied);
    await expectError(S.gateway.listModels({}), Code.PermissionDenied);
    await expectError(S.hosts.listAgentHosts({}), Code.PermissionDenied);
  });

  it("takes no work and gets no worker tokens until it proves it separates users", async () => {
    const S = await daemonClient(supervisorToken);
    const A = ts.as(alice.accessToken).hosts;
    await expectError(A.startHostWork({ hostId, repositoryKey: REPO, prompt: "x" }), Code.FailedPrecondition);
    const reg = await S.hostAgent.registerAgentHost(daemonInfo(AgentHostIsolation.NONE));
    expect(reg.supervisor).toBe(true);
    const shown = (await A.listAgentHosts({})).hosts.find((h) => h.id === hostId)!;
    expect(shown.unavailableReason).toContain("can't separate");
    await expectError(A.startHostWork({ hostId, repositoryKey: REPO, prompt: "x" }), Code.FailedPrecondition);
    await expectError(S.hostAgent.issueWorkerToken({ userId: alice.user!.id }), Code.FailedPrecondition);
  });

  it("with uid separation: workers per member, each sees only their own work, limits per user hold", async () => {
    const S = await daemonClient(supervisorToken);
    await S.hostAgent.registerAgentHost(daemonInfo(AgentHostIsolation.UID));
    const A = ts.as(alice.accessToken).hosts;
    const B = ts.as(bob.accessToken).hosts;
    const a1 = (await A.startHostWork({ hostId, repositoryKey: REPO, prompt: "a1" })).work!;
    const a2 = (await A.startHostWork({ hostId, repositoryKey: REPO, prompt: "a2" })).work!;
    const b1 = (await B.startHostWork({ hostId, repositoryKey: REPO, prompt: "b1" })).work!;
    await expectError(ts.as(carol.accessToken).hosts.startHostWork({ hostId, repositoryKey: REPO, prompt: "x" }), Code.NotFound);
    expect((await S.hostAgent.claimHostWork({ waitSeconds: 0 }).catch((e) => e)).code).toBe(Code.FailedPrecondition);

    const wanted = await S.hostAgent.listWantedWorkers({ waitSeconds: 0 });
    expect(wanted.workers.map((w) => w.userId).sort()).toEqual([alice.user!.id, bob.user!.id].sort());
    await expectError(S.hostAgent.issueWorkerToken({ userId: carol.user!.id }), Code.NotFound);
    const aw = await S.hostAgent.issueWorkerToken({ userId: alice.user!.id });
    const bw = await S.hostAgent.issueWorkerToken({ userId: bob.user!.id });
    // The worker is the member, and only a user.
    const AW = await daemonClient(aw.deviceToken);
    const BW = await daemonClient(bw.deviceToken);
    expect((await AW.auth.getMe({})).user?.email).toBe("alice@test.local");
    const areg = await AW.hostAgent.registerAgentHost(daemonInfo(AgentHostIsolation.NONE));
    await BW.hostAgent.registerAgentHost(daemonInfo(AgentHostIsolation.NONE));
    expect(areg.host!.isolation).toBe(AgentHostIsolation.UID); // a worker doesn't speak for the machine

    expect((await AW.hostAgent.claimHostWork({ waitSeconds: 0 })).work?.id).toBe(a1.id);
    expect((await AW.hostAgent.claimHostWork({ waitSeconds: 0 })).work).toBeUndefined(); // one per user
    expect((await BW.hostAgent.claimHostWork({ waitSeconds: 0 })).work?.id).toBe(b1.id);
    await expectError(BW.hostAgent.updateHostWork({ workId: a1.id, state: HostWorkState.DONE }), Code.NotFound);
    await AW.hostAgent.updateHostWork({ workId: a1.id, state: HostWorkState.RUNNING, sessionId: "sa" });
    await BW.hostAgent.updateHostWork({ workId: b1.id, state: HostWorkState.RUNNING, sessionId: "sb" });

    // A viewer of another member's work sees nothing; an admin sees it without the prompt.
    await expectError(B.getHostWork({ id: a1.id }), Code.NotFound);
    const seen = (await ts.as(admin.accessToken).hosts.getHostWork({ id: a1.id })).work!;
    expect(seen.prompt).toBe("");
    expect(seen.sessionId).toBe("sa");
    const list = await ts.as(admin.accessToken).hosts.listAgentHosts({ all: true });
    const host = list.hosts.find((h) => h.id === hostId)!;
    expect(host.workingSessions).toBe(2);
    expect(host.queuedWork).toBe(1);
    expect(host.workers.map((w) => w.userId).sort()).toEqual([alice.user!.id, bob.user!.id].sort());

    // A new token ends the old one.
    const rotated = await S.hostAgent.issueWorkerToken({ userId: bob.user!.id });
    expect(rotated.deviceId).toBe(bw.deviceId);
    await expectError(ts.auth.refreshAccessToken({ deviceToken: bw.deviceToken }), Code.Unauthenticated);

    // Leaving the team stops the worker and kills its token.
    await ts.as(admin.accessToken).directory.setGroupMembers({ id: groupId, userIds: [alice.user!.id] });
    const after = await S.hostAgent.listWantedWorkers({ waitSeconds: 0 });
    expect(after.stopUserIds).toEqual([bob.user!.id]);
    await expectError(ts.auth.refreshAccessToken({ deviceToken: rotated.deviceToken }), Code.Unauthenticated);
    expect((await ts.as(bob.accessToken).hosts.listAgentHosts({})).hosts.map((h) => h.id)).not.toContain(hostId);
    await A.cancelHostWork({ id: a2.id });
  });

  it("gives credentials from the connection: the user's, or its own where an admin allowed it", async () => {
    const S = await daemonClient(supervisorToken);
    await expectError(S.hostAgent.issueGitCredentials({ repositoryKey: REPO }), Code.PermissionDenied);
    await ts.as(admin.accessToken).directory.setGroupMembers({ id: groupId, userIds: [alice.user!.id, bob.user!.id, carol.user!.id] });
    const AW = await daemonClient((await S.hostAgent.issueWorkerToken({ userId: alice.user!.id })).deviceToken);
    const own = await AW.hostAgent.issueGitCredentials({ repositoryKey: REPO });
    expect(own).toMatchObject({ username: "x-access-token", asUser: true, cloneUrl: `${gh.url}/acme/widgets.git` });
    expect(own.password).toMatch(/^ghu_alice-gh_/);
    // carol has no account connected, and the pattern covers this repository: the app's token.
    const CW = await daemonClient((await S.hostAgent.issueWorkerToken({ userId: carol.user!.id })).deviceToken);
    const app = await CW.hostAgent.issueGitCredentials({ repositoryKey: REPO });
    expect(app.asUser).toBe(false);
    expect(app.password).toMatch(/^ghs_inst_/);
    // Outside the pattern she must connect her own.
    await expectError(CW.hostAgent.issueGitCredentials({ repositoryKey: "127.0.0.1/other/thing" }), Code.FailedPrecondition);
    await expectError(CW.hostAgent.issueGitCredentials({ repositoryKey: "nohost.example/a/b" }), Code.FailedPrecondition);
    // A laptop gets none.
    await expectError(ts.as(alice.accessToken).hostAgent.issueGitCredentials({ repositoryKey: REPO }), Code.FailedPrecondition);
  });

  it("opens a pull request once per branch", async () => {
    const S = await daemonClient(supervisorToken);
    const AW = await daemonClient((await S.hostAgent.issueWorkerToken({ userId: alice.user!.id })).deviceToken);
    const req = { repositoryKey: REPO, headBranch: "harness/hwk", baseBranch: "main", title: "Work", body: "b", draft: true };
    const one = await AW.hostAgent.openHostPullRequest(req);
    expect(one.url).toContain("/acme/widgets/pull/");
    const two = await AW.hostAgent.openHostPullRequest(req);
    expect(two.number).toBe(one.number);
  });
});

describe("policy", () => {
  it("can require the sandbox and refuse personal hosts", async () => {
    const P = ts.as(admin.accessToken).policy;
    const made = await ts.as(alice.accessToken).hosts.createAgentHost({ name: "p", scope: AgentHostScope.USER });
    const D = (await daemonClient(made.deviceToken)).hostAgent;
    await D.registerAgentHost(daemonInfo(AgentHostIsolation.NONE, { sandboxDriver: "" }));
    const id = made.host!.id;
    const A = ts.as(alice.accessToken).hosts;
    await P.setManagedPolicy({ policy: { sandboxRequired: true } });
    await expectError(A.startHostWork({ hostId: id, repositoryKey: REPO, prompt: "x" }), Code.FailedPrecondition);
    expect((await A.listAgentHosts({})).hosts.find((h) => h.id === id)!.unavailableReason).toContain("sandbox");
    await D.registerAgentHost(daemonInfo(AgentHostIsolation.NONE));
    const ok = await A.startHostWork({ hostId: id, repositoryKey: REPO, prompt: "x" });
    await A.cancelHostWork({ id: ok.work!.id });
    await P.setManagedPolicy({ policy: { personalHostsDisabled: true } });
    await expectError(A.createAgentHost({ name: "q", scope: AgentHostScope.USER }), Code.PermissionDenied);
    await expectError(A.startHostWork({ hostId: id, repositoryKey: REPO, prompt: "x" }), Code.FailedPrecondition);
    await P.setManagedPolicy({ policy: {} });
    await A.deleteAgentHost({ id });
  });
});

describe("away work", () => {
  let token: string;
  let hostId: string;

  beforeAll(async () => {
    const hosts = await ts.as(alice.accessToken).hosts.listAgentHosts({});
    for (const h of hosts.hosts) if (h.scope === AgentHostScope.USER) await ts.as(alice.accessToken).hosts.deleteAgentHost({ id: h.id });
    const made = await ts.as(alice.accessToken).hosts.createAgentHost({ name: "alice-away", scope: AgentHostScope.USER, maxSessions: 8 });
    token = made.deviceToken;
    hostId = made.host!.id;
    await (await daemonClient(token)).hostAgent.registerAgentHost(daemonInfo(AgentHostIsolation.NONE));
  });

  async function webhook(event: string, payload: unknown) {
    const raw = JSON.stringify(payload);
    const res = await fetch(`${ts.url}/api/vcs/github/webhook`, {
      method: "POST",
      headers: { "content-type": "application/json", "x-github-event": event, "x-github-delivery": randomUUID(), "x-github-hook-installation-target-id": String(gh.app.id), "x-hub-signature-256": githubSignature(gh.app.webhookSecret, raw) },
      body: raw,
    });
    expect(res.status).toBe(202);
    await vcsIdle();
  }

  const repository = () => ({ full_name: "acme/widgets", html_url: `${gh.url}/acme/widgets` });

  it("hands over a job whose machine is away, and withdraws what waited for that machine", async () => {
    gh.addPull({ number: 50, head: "fix-me", sha: "sha-50", title: "Fix" });
    const sid = randomUUID();
    await ts.as(alice.accessToken).sync.upsertSession({ session: { info: { id: sid, title: "s", cwd: "/w", repo: REPO, gitBranch: "fix-me" } } });
    await ts.as(alice.accessToken).vcs.setAutoFix({ sessionId: sid, reviewComments: true });
    // The laptop mirrored this session once and went away.
    const relay = ts.as(alice.accessToken).relay;
    const reg = await relay.registerHost({ instanceId: randomUUID(), clientVersion: "t" });
    await relay.registerSession({ hostId: reg.hostId, session: { id: sid, cwd: "/w" } });
    await getDb().update(schema.relayHost).set({ lastSeenAt: new Date(0) }).where(eq(schema.relayHost.id, reg.hostId));
    await webhook("pull_request_review_comment", {
      action: "created",
      comment: { id: 7001, body: "rename this", user: { login: "rev" }, path: "a.go", line: 3, html_url: "http://x/7001", author_association: "MEMBER", created_at: "2026-01-01T00:00:00Z" },
      pull_request: gh.pulls.get(50),
      repository: repository(),
    });
    const [cmd] = await getDb().select().from(schema.relayCommand).where(eq(schema.relayCommand.sessionId, sid));
    expect(cmd!.state).toBe("queued");

    const D = (await daemonClient(token)).hostAgent;
    await expect
      .poll(
        async () => {
          await D.reportAgentHost({ workingSessions: 0 });
          return (await D.claimHostWork({ waitSeconds: 0 })).work;
        },
        { timeout: 5000, interval: 50 },
      )
      .toMatchObject({ kind: HostWorkKind.AUTO_FIX, branch: "fix-me", repositoryKey: REPO, forkSessionId: sid });
    await getDb().update(schema.hostWork).set({ state: HostWorkState.DONE }).where(eq(schema.hostWork.forkSessionId, sid));
    const [after] = await getDb().select().from(schema.relayCommand).where(eq(schema.relayCommand.sessionId, sid));
    expect(after!.state).toBe("withdrawn");
    const [job] = await getDb().select().from(schema.vcsAutofixJob).where(eq(schema.vcsAutofixJob.sessionId, sid));
    expect(job!.status).toBe("handed_off");
    const hosts = await ts.as(alice.accessToken).hosts.listAgentHosts({});
    expect(hosts.hosts.find((h) => h.id === hostId)?.awayHost).toBe(true);
  });

  it("leaves a job alone while its machine is up, and when away work is turned off", async () => {
    gh.addPull({ number: 51, head: "up", sha: "sha-51", title: "Up" });
    const sid = randomUUID();
    await ts.as(alice.accessToken).sync.upsertSession({ session: { info: { id: sid, title: "s", cwd: "/w", repo: REPO, gitBranch: "up" } } });
    await ts.as(alice.accessToken).vcs.setAutoFix({ sessionId: sid, reviewComments: true });
    const relay = ts.as(alice.accessToken).relay;
    const reg = await relay.registerHost({ instanceId: randomUUID(), clientVersion: "t" });
    await relay.registerSession({ hostId: reg.hostId, session: { id: sid, cwd: "/w" } });
    await webhook("pull_request_review_comment", {
      action: "created",
      comment: { id: 7002, body: "x", user: { login: "rev" }, path: "a.go", line: 3, html_url: "http://x/7002", author_association: "MEMBER", created_at: "2026-01-01T00:00:00Z" },
      pull_request: gh.pulls.get(51),
      repository: repository(),
    });
    const D = (await daemonClient(token)).hostAgent;
    await D.reportAgentHost({ workingSessions: 0 });
    const [job] = await getDb().select().from(schema.vcsAutofixJob).where(eq(schema.vcsAutofixJob.sessionId, sid));
    expect(job!.status).toBe("pending");
    await ts.as(alice.accessToken).hosts.setAwayHost({ hostId: "" });
    await getDb().update(schema.relayHost).set({ lastSeenAt: new Date(0) }).where(eq(schema.relayHost.id, reg.hostId));
    await D.reportAgentHost({ workingSessions: 0 });
    const [still] = await getDb().select().from(schema.vcsAutofixJob).where(eq(schema.vcsAutofixJob.sessionId, sid));
    expect(still!.status).toBe("pending");
    await ts.as(alice.accessToken).hosts.setAwayHost({ hostId });
  });

  it("an @mention on a pull request with no session goes to the commenter's away host", async () => {
    gh.addPull({ number: 52, head: "mentioned", sha: "sha-52", title: "M" });
    await webhook("pull_request", { action: "opened", pull_request: gh.pulls.get(52), repository: repository() });
    const comment = (id: number, login: string, association: string) => ({
      action: "created",
      comment: { id, body: "@harness please look", user: { login }, html_url: `http://x/${id}`, author_association: association, created_at: "2026-01-01T00:00:00Z" },
      issue: { number: 52, title: "M", pull_request: { html_url: "http://x/52" }, user: { login: "x" }, state: "open" },
      repository: repository(),
    });
    await webhook("issue_comment", comment(8001, "stranger", "MEMBER"));
    await webhook("issue_comment", comment(8002, "alice-gh", "NONE"));
    const D = (await daemonClient(token)).hostAgent;
    expect((await D.claimHostWork({ waitSeconds: 0 })).work).toBeUndefined();
    await webhook("issue_comment", comment(8003, "alice-gh", "MEMBER"));
    const got = (await D.claimHostWork({ waitSeconds: 0 })).work;
    expect(got).toMatchObject({ kind: HostWorkKind.MENTION, branch: "mentioned" });
    expect(got!.job!.comments[0]!.body).toContain("please look");
  });
});

describe("schedule claims", () => {
  it("the first device of a user wins a due time", async () => {
    const H = ts.as(alice.accessToken).hosts;
    const due = new Date().toISOString();
    expect((await H.claimScheduleRun({ scheduleId: "sch_1", dueAt: due })).granted).toBe(true);
    expect((await H.claimScheduleRun({ scheduleId: "sch_1", dueAt: due })).granted).toBe(true); // the same device, asking again
    const other = await deviceLogin(ts, "alice@test.local", "user-password-1");
    expect((await ts.as(other.accessToken).hosts.claimScheduleRun({ scheduleId: "sch_1", dueAt: due })).granted).toBe(false);
    expect((await ts.as(other.accessToken).hosts.claimScheduleRun({ scheduleId: "sch_1", dueAt: due + "x" })).granted).toBe(true);
  });
});

describe("repository patterns", () => {
  it("match by segment", () => {
    expect(matchesRepository(["github.com/acme/*"], "github.com/acme/widgets")).toBe(true);
    expect(matchesRepository(["github.com/acme/*"], "github.com/acme/group/deep")).toBe(true);
    expect(matchesRepository(["github.com/acme/w*"], "github.com/acme/widgets")).toBe(true);
    expect(matchesRepository(["github.com/acme/w*"], "github.com/acme/other")).toBe(false);
    expect(matchesRepository(["github.com/acme/*"], "github.com/acmex/widgets")).toBe(false);
    expect(matchesRepository(["github.com/acme/widgets"], "github.com/acme/widgets2")).toBe(false);
    expect(matchesRepository([], "github.com/acme/widgets")).toBe(false);
  });
});
