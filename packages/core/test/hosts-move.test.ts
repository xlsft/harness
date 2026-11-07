/**
 * Moving a session to an always-on host and best-of-N attempts (docs/design/hosts.md, decisions 21 to 33): the move's
 * states and its credentials, the attempt groups, their limits, their places in the queue, and discarding.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { randomUUID } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { PermissionMode } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { AgentHostIsolation, AgentHostScope, AttemptState, HostWorkKind, HostWorkLanding, HostWorkState } from "../src/gen/harness/relay/v1/hosts_pb.ts";
import { attemptLimits } from "../src/relay/attempts.ts";
import { startFakeGitHub, type FakeGitHub } from "./helpers/fake-hosts.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let gh: FakeGitHub;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let alice: Awaited<ReturnType<typeof deviceLogin>>;
let bob: Awaited<ReturnType<typeof deviceLogin>>;
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

async function daemonClient(deviceToken: string) {
  const { accessToken } = await ts.auth.refreshAccessToken({ deviceToken });
  return ts.as(accessToken);
}

const daemonInfo = { name: "", isolation: AgentHostIsolation.NONE, sandboxDriver: "linux", os: "linux", arch: "amd64", clientVersion: "test", cpuCount: 8 };

beforeAll(async () => {
  ts = await startTestServer({ HARNESS_HOST_LEASE_MS: "8000" });
  gh = await startFakeGitHub();
  admin = await deviceLogin(ts);
  alice = await createUserAndLogin(ts, admin.cookie, "alice@test.local");
  bob = await createUserAndLogin(ts, admin.cookie, "bob@test.local");
  const ADM = ts.as(admin.accessToken).vcs;
  const m = await ADM.getGitHubAppManifest({ baseUrl: gh.url, appName: "harness test" });
  const done = await ADM.completeGitHubAppManifest({ code: gh.manifestCode(), state: m.state });
  expect(done.connection?.id).toBeTruthy();
  const start = await ts.as(alice.accessToken).vcs.startUserAuth({ connectionId: done.connection!.id });
  const auth = await fetch(start.authorizationUrl, { redirect: "manual" });
  await fetch(auth.headers.get("location")!);
});

afterAll(async () => {
  await ts.close();
  await gh.close();
});

describe("moving a session", () => {
  let hostId: string;
  let token: string;
  const sid = randomUUID();

  beforeAll(async () => {
    const made = await ts.as(alice.accessToken).hosts.createAgentHost({ name: "alice-move", scope: AgentHostScope.USER, maxSessions: 4 });
    hostId = made.host!.id;
    token = made.deviceToken;
    await (await daemonClient(token)).hostAgent.registerAgentHost(daemonInfo);
  });

  it("needs a harness/ branch and a transcript of the caller's on the server", async () => {
    const A = ts.as(alice.accessToken).hosts;
    const move = { hostId, repositoryKey: REPO, kind: HostWorkKind.MOVE, baseRef: "main", forkSessionId: sid };
    await expectError(A.startHostWork({ ...move, branch: "main" }), Code.InvalidArgument);
    await expectError(A.startHostWork({ ...move, branch: "harness/../main" }), Code.InvalidArgument);
    await expectError(A.startHostWork({ ...move, branch: `harness/${sid}` }), Code.FailedPrecondition);
    // bob's session is not alice's to move.
    const bobs = randomUUID();
    await ts.as(bob.accessToken).sync.upsertSession({ session: { info: { id: bobs, title: "b", cwd: "/w", repo: REPO } } });
    await expectError(A.startHostWork({ ...move, branch: `harness/${bobs}`, forkSessionId: bobs }), Code.FailedPrecondition);
    await expectError(A.startHostWork({ ...move, branch: `harness/${sid}`, permissionMode: PermissionMode.BYPASS_PERMISSIONS }), Code.PermissionDenied);
    await expectError(A.startHostWork({ hostId, repositoryKey: REPO, prompt: "x", kind: HostWorkKind.ATTEMPT }), Code.InvalidArgument);
  });

  it("waits for its push, with credentials for that one move, then goes to the queue", async () => {
    const A = ts.as(alice.accessToken).hosts;
    await ts.as(alice.accessToken).sync.upsertSession({ session: { info: { id: sid, title: "moving", cwd: "/w", repo: REPO, gitBranch: "feature" } } });
    const work = (await A.startHostWork({ hostId, repositoryKey: REPO, kind: HostWorkKind.MOVE, baseRef: "main", branch: `harness/${sid}`, forkSessionId: sid, permissionMode: PermissionMode.AUTO })).work!;
    expect(work).toMatchObject({ state: HostWorkState.AWAITING_PUSH, kind: HostWorkKind.MOVE, branch: `harness/${sid}`, forkSessionId: sid, landing: HostWorkLanding.BRANCH });
    const D = (await daemonClient(token)).hostAgent;
    // Nobody claims a move before its branch is up.
    expect((await D.claimHostWork({ waitSeconds: 0 })).work).toBeUndefined();
    await expectError(ts.as(bob.accessToken).hostAgent.issueMoveCredentials({ workId: work.id }), Code.NotFound);
    await expectError(ts.as(bob.accessToken).hostAgent.queueMovedWork({ workId: work.id }), Code.NotFound);
    // The laptop is no host, and still gets them for its own move: the user's own account.
    const cred = (await ts.as(alice.accessToken).hostAgent.issueMoveCredentials({ workId: work.id })).credentials!;
    expect(cred).toMatchObject({ username: "x-access-token", asUser: true, cloneUrl: `${gh.url}/acme/widgets.git` });
    expect(cred.password).toMatch(/^ghu_alice-gh_/);
    const queued = (await ts.as(alice.accessToken).hostAgent.queueMovedWork({ workId: work.id, head: "abc" })).work!;
    expect(queued.state).toBe(HostWorkState.QUEUED);
    // Once queued, no more credentials for the laptop.
    await expectError(ts.as(alice.accessToken).hostAgent.issueMoveCredentials({ workId: work.id }), Code.FailedPrecondition);
    const claimed = (await D.claimHostWork({ waitSeconds: 0 })).work!;
    expect(claimed).toMatchObject({ id: work.id, kind: HostWorkKind.MOVE, branch: `harness/${sid}`, forkSessionId: sid, prompt: "", permissionMode: PermissionMode.AUTO });
    await D.updateHostWork({ workId: work.id, state: HostWorkState.RUNNING, sessionId: "host-session" });
    expect((await A.getHostWork({ id: work.id })).work).toMatchObject({ state: HostWorkState.RUNNING, sessionId: "host-session" });
    await D.updateHostWork({ workId: work.id, state: HostWorkState.DONE });
  });

  it("a push that fails cancels the move", async () => {
    const A = ts.as(alice.accessToken).hosts;
    const work = (await A.startHostWork({ hostId, repositoryKey: REPO, kind: HostWorkKind.MOVE, baseRef: "main", branch: `harness/${sid}-2`, forkSessionId: sid })).work!;
    await A.cancelHostWork({ id: work.id });
    await expectError(ts.as(alice.accessToken).hostAgent.queueMovedWork({ workId: work.id }), Code.FailedPrecondition);
  });
});

describe("best-of-N", () => {
  let hostId: string;
  let token: string;

  beforeAll(async () => {
    const made = await ts.as(alice.accessToken).hosts.createAgentHost({ name: "alice-attempts", scope: AgentHostScope.USER, maxSessions: 8 });
    hostId = made.host!.id;
    token = made.deviceToken;
    await (await daemonClient(token)).hostAgent.registerAgentHost(daemonInfo);
  });

  it("applies the policy's caps to the user's choices", () => {
    const none = { maxAttempts: 0, maxParallelAttempts: 0, attemptBudgetUsd: 0 };
    expect(attemptLimits(none, { count: 3, parallel: 0, budgetUsd: 0 })).toEqual({ count: 3, parallel: 3, budgetUsd: 0 });
    expect(attemptLimits(none, { count: 5, parallel: 2, budgetUsd: 1.5 })).toEqual({ count: 5, parallel: 2, budgetUsd: 1.5 });
    expect(() => attemptLimits(none, { count: 6, parallel: 0, budgetUsd: 0 })).toThrow();
    expect(() => attemptLimits(none, { count: 1, parallel: 0, budgetUsd: 0 })).toThrow();
    const capped = { maxAttempts: 3, maxParallelAttempts: 1, attemptBudgetUsd: 2 };
    expect(() => attemptLimits(capped, { count: 4, parallel: 0, budgetUsd: 0 })).toThrow(/2 to 3/);
    expect(attemptLimits(capped, { count: 3, parallel: 3, budgetUsd: 0 })).toEqual({ count: 3, parallel: 1, budgetUsd: 2 });
    expect(attemptLimits(capped, { count: 2, parallel: 0, budgetUsd: 0.5 })).toEqual({ count: 2, parallel: 1, budgetUsd: 0.5 });
  });

  it("queues a host's attempts, one place at a time, and shows them only to their owner", async () => {
    const P = ts.as(admin.accessToken).policy;
    await P.setManagedPolicy({ policy: { maxAttempts: 3, maxParallelAttempts: 1, attemptBudgetUsd: 4 } });
    const A = ts.as(alice.accessToken).hosts;
    await expectError(A.startAttempts({ hostId, count: 4, prompt: "fix it", repositoryKey: REPO }), Code.InvalidArgument);
    await expectError(A.startAttempts({ hostId, count: 2, prompt: "fix it", repositoryKey: REPO, permissionMode: PermissionMode.BYPASS_PERMISSIONS }), Code.PermissionDenied);
    const group = (await A.startAttempts({ hostId, count: 3, prompt: "fix it", title: "Fix", repositoryKey: REPO, testCommand: "make test", budgetUsd: 10 })).group!;
    await P.setManagedPolicy({ policy: {} });
    expect(group).toMatchObject({ parallel: 1, budgetUsd: 4, keptIndex: -1, testCommand: "make test" });
    expect(group.attempts.map((a) => a.state)).toEqual([AttemptState.PENDING, AttemptState.PENDING, AttemptState.PENDING]);
    await expectError(ts.as(bob.accessToken).hosts.getAttemptGroup({ id: group.id }), Code.NotFound);
    await expectError(ts.as(admin.accessToken).hosts.getAttemptGroup({ id: group.id }), Code.NotFound);
    expect((await ts.as(bob.accessToken).hosts.listAttemptGroups({})).groups).toHaveLength(0);

    const D = (await daemonClient(token)).hostAgent;
    const first = (await D.claimHostWork({ waitSeconds: 0 })).work!;
    expect(first).toMatchObject({ kind: HostWorkKind.ATTEMPT, attemptGroupId: group.id, attemptIndex: 0, testCommand: "make test", budgetUsd: 4, landing: HostWorkLanding.BRANCH, title: "Fix (1/3)" });
    // One at a time: the next waits while the first works.
    expect((await D.claimHostWork({ waitSeconds: 0 })).work).toBeUndefined();
    await D.updateHostWork({ workId: first.id, state: HostWorkState.RUNNING, sessionId: "s0", branch: `harness/${first.id}` });
    await A.reportAttempt({ groupId: group.id, index: 0, state: AttemptState.IDLE, check: { command: "make test", exitCode: 1, output: "x".repeat(20_000) } });
    const second = (await D.claimHostWork({ waitSeconds: 0 })).work!;
    expect(second.attemptIndex).toBe(1);
    await D.updateHostWork({ workId: second.id, state: HostWorkState.RUNNING, sessionId: "s1", branch: `harness/${second.id}` });

    const shown = (await A.getAttemptGroup({ id: group.id })).group!;
    expect(shown.attempts[0]).toMatchObject({ state: AttemptState.IDLE, sessionId: "s0", branch: `harness/${first.id}` });
    expect(shown.attempts[0]!.check).toMatchObject({ groupId: group.id, index: 0, exitCode: 1 });
    expect(shown.attempts[0]!.check!.output.length).toBe(8 * 1024);
    expect(shown.attempts[1]).toMatchObject({ state: AttemptState.RUNNING, sessionId: "s1" });
    expect(shown.attempts[2]!.state).toBe(AttemptState.PENDING);

    // Keeping marks one; discarding needs one kept, and the user's word.
    await expectError(A.discardAttempts({ groupId: group.id }), Code.FailedPrecondition);
    expect((await A.keepAttempt({ groupId: group.id, index: 0 })).group!.keptIndex).toBe(0);
    const after = (await A.discardAttempts({ groupId: group.id })).group!;
    // The one still queued just goes; the one with a session waits for its daemon.
    expect(after.attempts.map((a) => a.state)).toEqual([AttemptState.IDLE, AttemptState.DISCARDING, AttemptState.DISCARDED]);
    expect(after.attempts[0]!.kept).toBe(true);
    // A waiting claim answers at once, with no work, so that the daemon reports now.
    const asked = Date.now();
    expect((await D.claimHostWork({ waitSeconds: 20 })).work).toBeUndefined();
    expect(Date.now() - asked).toBeLessThan(5000);
    const report = await D.reportAgentHost({ workingSessions: 1 });
    expect(report.discardWorkIds).toEqual([second.id]);
    // A late report does not bring it back.
    await A.reportAttempt({ groupId: group.id, index: 1, state: AttemptState.IDLE });
    expect((await A.getAttemptGroup({ id: group.id })).group!.attempts[1]!.state).toBe(AttemptState.DISCARDING);
    await D.updateHostWork({ workId: second.id, discarded: true });
    expect((await A.getAttemptGroup({ id: group.id })).group!.attempts[1]!.state).toBe(AttemptState.DISCARDED);
    expect((await D.reportAgentHost({ workingSessions: 0 })).discardWorkIds).toEqual([]);
    await expectError(A.keepAttempt({ groupId: group.id, index: 2 }), Code.FailedPrecondition);
  });

  it("a local group is filled in by its client, which also removes what is discarded", async () => {
    const A = ts.as(alice.accessToken).hosts;
    const group = (await A.startAttempts({ count: 2, prompt: "try", cwd: "/w/repo", idempotencyKey: "k1" })).group!;
    expect(group).toMatchObject({ hostId: "", cwd: "/w/repo", parallel: 2 });
    expect(group.deviceId).toBeTruthy();
    expect((await A.startAttempts({ count: 2, prompt: "try", cwd: "/w/repo", idempotencyKey: "k1" })).group!.id).toBe(group.id);
    await A.reportAttempt({ groupId: group.id, index: 1, state: AttemptState.RUNNING, sessionId: "local-1", branch: "harness/attempt-1" });
    await expectError(A.reportAttempt({ groupId: group.id, index: 2, state: AttemptState.RUNNING }), Code.NotFound);
    await expectError(ts.as(bob.accessToken).hosts.reportAttempt({ groupId: group.id, index: 0, state: AttemptState.IDLE }), Code.NotFound);
    await A.keepAttempt({ groupId: group.id, index: 1 });
    await expectError(A.discardAttempts({ groupId: group.id }), Code.FailedPrecondition);
    await A.reportAttempt({ groupId: group.id, index: 0, state: AttemptState.DISCARDED });
    const shown = (await A.getAttemptGroup({ id: group.id })).group!;
    expect(shown.attempts.map((a) => a.state)).toEqual([AttemptState.DISCARDED, AttemptState.RUNNING]);
    expect((await A.listAttemptGroups({})).groups[0]!.id).toBe(group.id);
  });
});
