/**
 * Cloud sessions (docs/design/cloud-sessions.md): runners, the container lifecycle the server drives through
 * PollCloudRunner, a session's own device, waking a removed session, quotas and bypass from the policy.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { CloudEnvironmentState as S, PermissionMode } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { AgentHostIsolation, AgentHostScope, HostWorkLanding, HostWorkState } from "../src/gen/harness/relay/v1/hosts_pb.ts";
import { wakeCloudDevice } from "../src/relay/cloud.ts";
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

async function deviceClient(deviceToken: string) {
  const { accessToken } = await ts.auth.refreshAccessToken({ deviceToken });
  return ts.as(accessToken);
}

const runnerInfo = { name: "", isolation: AgentHostIsolation.CONTAINER, sandboxDriver: "container", os: "linux", arch: "amd64", clientVersion: "test", cpuCount: 8 };

async function cloudRow(id: string) {
  const [row] = await getDb().select().from(schema.cloudSession).where(eq(schema.cloudSession.id, id));
  return row!;
}

beforeAll(async () => {
  ts = await startTestServer({ HARNESS_HOST_LEASE_MS: "8000" });
  gh = await startFakeGitHub();
  admin = await deviceLogin(ts);
  alice = await createUserAndLogin(ts, admin.cookie, "alice-cloud@test.local");
  bob = await createUserAndLogin(ts, admin.cookie, "bob-cloud@test.local");
  const ADM = ts.as(admin.accessToken).vcs;
  const m = await ADM.getGitHubAppManifest({ baseUrl: gh.url, appName: "harness cloud test" });
  await ADM.completeGitHubAppManifest({ code: gh.manifestCode(), state: m.state });
});

afterAll(async () => {
  await ts.close();
  await gh.close();
});

describe("cloud sessions", () => {
  let runnerToken: string;
  let runnerId: string;
  let workId: string;
  let cloudId: string;
  let sessionToken: string;
  let sessionDevice: string;

  it("an admin registers a runner; its token talks to the host service only", async () => {
    await expectError(ts.as(alice.accessToken).hosts.createAgentHost({ name: "r", scope: AgentHostScope.CLOUD }), Code.PermissionDenied);
    const made = await ts.as(admin.accessToken).hosts.createAgentHost({
      name: "runner-1",
      scope: AgentHostScope.CLOUD,
      maxSessions: 2,
      cloud: { image: "harness-agent:test", allowedImages: ["harness-agent:big"], runtime: "runsc", idleTimeoutSeconds: 120 },
    });
    runnerToken = made.deviceToken;
    runnerId = made.host!.id;
    expect(made.host!.scope).toBe(AgentHostScope.CLOUD);
    expect(made.host!.cloud?.runtime).toBe("runsc");
    expect(made.host!.cloud?.idleTimeoutSeconds).toBe(120);
    expect(made.host!.cloud?.maxLifetimeSeconds).toBe(86_400);
    await expectError(ts.as(admin.accessToken).hosts.createAgentHost({ name: "bad", scope: AgentHostScope.CLOUD, cloud: { image: "bad image" } }), Code.InvalidArgument);
    // Without a group, everyone may use it; the configuration is the admins'.
    const seen = (await ts.as(alice.accessToken).hosts.listAgentHosts({})).hosts.find((h) => h.id === runnerId);
    expect(seen?.scope).toBe(AgentHostScope.CLOUD);
    expect(seen?.cloud).toBeUndefined();
    const R = await deviceClient(runnerToken);
    const reg = await R.hostAgent.registerAgentHost(runnerInfo);
    expect(reg.runner).toBe(true);
    expect(reg.host!.isolation).toBe(AgentHostIsolation.CONTAINER);
    await expectError(R.auth.getMe({}), Code.PermissionDenied);
    await expectError(R.hostAgent.claimHostWork({ waitSeconds: 0 }), Code.FailedPrecondition);
    await expectError(R.hostAgent.issueGitCredentials({ repositoryKey: REPO }), Code.PermissionDenied);
  });

  it("starting a cloud session queues it on a runner; landing NONE becomes BRANCH; bypass is refused", async () => {
    const A = ts.as(alice.accessToken).hosts;
    await expectError(A.startHostWork({ cloud: true, repositoryKey: REPO, prompt: "x", permissionMode: PermissionMode.BYPASS_PERMISSIONS }), Code.PermissionDenied);
    await expectError(A.startHostWork({ cloud: true, repositoryKey: REPO, prompt: "x", image: "other:1" }), Code.InvalidArgument);
    await expectError(A.startHostWork({ hostId: runnerId, repositoryKey: REPO, prompt: "x" }), Code.InvalidArgument);
    const work = (await A.startHostWork({ cloud: true, repositoryKey: REPO, prompt: "build it", landing: HostWorkLanding.NONE })).work!;
    expect(work.cloud).toBe(true);
    expect(work.cloudState).toBe(S.STARTING);
    expect(work.hostId).toBe(runnerId);
    expect(work.landing).toBe(HostWorkLanding.BRANCH);
    workId = work.id;
    cloudId = work.cloudSessionId;
    expect((await A.getHostWork({ id: workId })).work!.cloudSessionId).toBe(cloudId);
    // Only the owner sees it among their own.
    expect((await ts.as(bob.accessToken).hosts.listCloudSessions({})).sessions).toHaveLength(0);
    expect((await A.listCloudSessions({})).sessions.map((s) => s.id)).toEqual([cloudId]);
  });

  it("the runner is told to start it, gets a token for the session's own device and reports the container", async () => {
    const R = (await deviceClient(runnerToken)).hostAgent;
    const plan = await R.pollCloudRunner({ waitSeconds: 0, containers: [] });
    expect(plan.start.map((s) => s.cloudSessionId)).toEqual([cloudId]);
    expect(plan.start[0]!.image).toBe("harness-agent:test");
    expect(plan.start[0]!.cacheKey).toMatch(/^u[0-9a-f]{16}$/);
    expect(plan.config?.runtime).toBe("runsc");
    const egress = plan.egress.find((e) => e.cloudSessionId === cloudId)!.allow;
    expect(egress).toContain("registry.npmjs.org");
    expect(egress).toContain(new URL(gh.url).host);
    // While the runner says it is creating it, it is not asked again.
    expect((await R.pollCloudRunner({ waitSeconds: 0, containers: [], creating: [cloudId] })).start).toHaveLength(0);
    const tok = await R.issueCloudSessionToken({ cloudSessionId: cloudId });
    sessionToken = tok.deviceToken;
    sessionDevice = tok.deviceId;
    await R.updateCloudSession({ cloudSessionId: cloudId, state: S.STARTING, containerId: "c1" });
    const row = await cloudRow(cloudId);
    expect(row.containerId).toBe("c1");
    expect(row.starts).toBe(1);
    expect((await R.pollCloudRunner({ waitSeconds: 0, containers: [{ cloudSessionId: cloudId, containerId: "c1", running: true }] })).remove).toHaveLength(0);
  });

  it("the container's daemon takes its own work once, runs it, and its reports make it RUNNING and IDLE", async () => {
    const C = await deviceClient(sessionToken);
    const reg = await C.hostAgent.registerAgentHost({ ...runnerInfo, isolation: AgentHostIsolation.NONE });
    expect(reg.cloud).toBe(true);
    expect(reg.bypassPermissionsAllowed).toBe(false);
    expect(reg.host!.id).toBe(runnerId);
    expect((await C.auth.getMe({})).user?.email).toBe("alice-cloud@test.local");
    const got = await C.hostAgent.claimHostWork({ waitSeconds: 0 });
    expect(got.work?.id).toBe(workId);
    expect(got.work?.prompt).toBe("build it");
    expect((await C.hostAgent.claimHostWork({ waitSeconds: 0 })).work).toBeUndefined();
    await C.hostAgent.updateHostWork({ workId, state: HostWorkState.RUNNING, sessionId: "s-cloud", branch: `harness/${workId}` });
    await C.hostAgent.reportAgentHost({ workingSessions: 1 });
    expect((await cloudRow(cloudId)).state).toBe(S.RUNNING);
    expect((await cloudRow(cloudId)).sessionId).toBe("s-cloud");
    await C.hostAgent.reportAgentHost({ workingSessions: 0 });
    expect((await cloudRow(cloudId)).state).toBe(S.IDLE);
    // Git credentials come from the connection, as on hosts; alice has no account connected, nor does the runner allow its identity.
    await expectError(C.hostAgent.issueGitCredentials({ repositoryKey: REPO }), Code.FailedPrecondition);
    const all = (await ts.as(admin.accessToken).hosts.listAgentHosts({ all: true })).hosts.find((h) => h.id === runnerId)!;
    expect(all.cloudStats?.idle).toBe(1);
  });

  it("idle past the timeout, the container is removed; a message wakes it on a fresh token, and the work comes back to resume", async () => {
    const R = (await deviceClient(runnerToken)).hostAgent;
    const box = { cloudSessionId: cloudId, containerId: "c1", running: true };
    expect((await R.pollCloudRunner({ waitSeconds: 0, containers: [box] })).remove).toHaveLength(0);
    await getDb().update(schema.cloudSession).set({ lastActiveAt: new Date(Date.now() - 10 * 60_000) }).where(eq(schema.cloudSession.id, cloudId));
    const plan = await R.pollCloudRunner({ waitSeconds: 0, containers: [box] });
    expect(plan.remove).toEqual([expect.objectContaining({ cloudSessionId: cloudId, containerId: "c1", reason: "idle" })]);
    expect((await cloudRow(cloudId)).state).toBe(S.REMOVED);
    expect((await R.updateCloudSession({ cloudSessionId: cloudId, state: S.REMOVED, containerId: "c1" })).remove).toBe(true);
    // Not wanted: nothing to start.
    expect((await R.pollCloudRunner({ waitSeconds: 0, containers: [] })).start).toHaveLength(0);

    expect(await wakeCloudDevice(sessionDevice)).toBe(true);
    expect(await wakeCloudDevice("dev_not_cloud")).toBe(false);
    expect((await cloudRow(cloudId)).state).toBe(S.STARTING);
    const again = await R.pollCloudRunner({ waitSeconds: 0, containers: [] });
    expect(again.start.map((s) => s.cloudSessionId)).toEqual([cloudId]);
    const tok = await R.issueCloudSessionToken({ cloudSessionId: cloudId });
    expect(tok.deviceId).toBe(sessionDevice);
    await expectError(ts.auth.refreshAccessToken({ deviceToken: sessionToken }), Code.Unauthenticated);
    await R.updateCloudSession({ cloudSessionId: cloudId, state: S.STARTING, containerId: "c2" });
    const C = await deviceClient(tok.deviceToken);
    await C.hostAgent.registerAgentHost({ ...runnerInfo, isolation: AgentHostIsolation.NONE });
    const resumed = await C.hostAgent.claimHostWork({ waitSeconds: 0 });
    expect(resumed.work?.id).toBe(workId);
    expect(resumed.work?.sessionId).toBe("s-cloud");
    expect(resumed.work?.state).toBe(HostWorkState.RUNNING);
    expect((await cloudRow(cloudId)).starts).toBe(2);
    sessionToken = tok.deviceToken;
  });

  it("removes what it doesn't know, a second container of a session, and a container past its lifetime", async () => {
    const R = (await deviceClient(runnerToken)).hostAgent;
    const plan = await R.pollCloudRunner({
      waitSeconds: 0,
      containers: [
        { cloudSessionId: "cls_unknown", containerId: "x1", running: true },
        { cloudSessionId: cloudId, containerId: "c2", running: true },
        { cloudSessionId: cloudId, containerId: "old", running: true },
      ],
    });
    expect(plan.remove.map((r) => r.containerId).sort()).toEqual(["old", "x1"]);
    await getDb().update(schema.cloudSession).set({ startedAt: new Date(Date.now() - 2 * 86_400_000) }).where(eq(schema.cloudSession.id, cloudId));
    const late = await R.pollCloudRunner({ waitSeconds: 0, containers: [{ cloudSessionId: cloudId, containerId: "c2", running: true }] });
    expect(late.remove.map((r) => r.reason)).toEqual(["its lifetime is over"]);
    expect((await cloudRow(cloudId)).state).toBe(S.REMOVED);
  });

  it("a long poll wakes for a new session", { timeout: 30_000 }, async () => {
    const R = (await deviceClient(runnerToken)).hostAgent;
    const waiting = R.pollCloudRunner({ waitSeconds: 20, containers: [] });
    const started = Date.now();
    const w = (await ts.as(bob.accessToken).hosts.startHostWork({ cloud: true, repositoryKey: REPO, prompt: "hi" })).work!;
    const plan = await waiting;
    expect(plan.start.map((s) => s.cloudSessionId)).toContain(w.cloudSessionId);
    expect(Date.now() - started).toBeLessThan(10_000);
    // Cancelling ends it for good: the runner is told to remove its container, and a message can't wake it.
    await R.updateCloudSession({ cloudSessionId: w.cloudSessionId, state: S.STARTING, containerId: "b1" });
    await ts.as(bob.accessToken).hosts.cancelHostWork({ id: w.id });
    const gone = await R.pollCloudRunner({ waitSeconds: 0, containers: [{ cloudSessionId: w.cloudSessionId, containerId: "b1", running: true }] });
    expect(gone.remove.map((r) => r.containerId)).toEqual(["b1"]);
    expect((await cloudRow(w.cloudSessionId)).state).toBe(S.ENDED);
  });

  it("capacity, quotas and bypass come from the runner and the policy", async () => {
    const P = ts.as(admin.accessToken).policy;
    const A = ts.as(alice.accessToken).hosts;
    await P.setManagedPolicy({ policy: { cloudMaxSessionsPerUser: 1 } });
    const one = (await A.startHostWork({ cloud: true, repositoryKey: REPO, prompt: "one" })).work!;
    await expectError(A.startHostWork({ cloud: true, repositoryKey: REPO, prompt: "two" }), Code.FailedPrecondition);
    // A removed session over quota can't be woken either.
    await expect(wakeCloudDevice(sessionDevice)).rejects.toThrow(/cloud sessions running/);
    await A.cancelHostWork({ id: one.id });
    await P.setManagedPolicy({ policy: { cloudBypassPermissionsAllowed: true, cloudEgressAllow: ["registry.npmjs.org", "*.example.com"] } });
    const bypass = (await A.startHostWork({ cloud: true, repositoryKey: REPO, prompt: "b", permissionMode: PermissionMode.BYPASS_PERMISSIONS })).work!;
    const R = (await deviceClient(runnerToken)).hostAgent;
    const plan = await R.pollCloudRunner({ waitSeconds: 0, containers: [] });
    expect(plan.egress.find((e) => e.cloudSessionId === bypass.cloudSessionId)?.allow.sort()).toEqual(["*.example.com", "registry.npmjs.org"]);
    await expectError(P.setManagedPolicy({ policy: { cloudEgressAllow: ["http://x"] } }), Code.InvalidArgument);
    await P.setManagedPolicy({ policy: { cloudDisabled: true } });
    await expectError(A.startHostWork({ cloud: true, repositoryKey: REPO, prompt: "x" }), Code.PermissionDenied);
    expect((await A.listAgentHosts({})).hosts.find((h) => h.id === runnerId)!.unavailableReason).toContain("turned off");
    await P.setManagedPolicy({ policy: {} });
    await A.cancelHostWork({ id: bypass.id });
  });

  it("an admin stops a container now; the runner's configuration changes in place", async () => {
    const ADM = ts.as(admin.accessToken).hosts;
    const updated = await ADM.updateAgentHost({ id: runnerId, cloud: { image: "harness-agent:2", idleTimeoutSeconds: 600 } });
    expect(updated.host?.cloud?.image).toBe("harness-agent:2");
    await expectError(ts.as(alice.accessToken).hosts.updateAgentHost({ id: runnerId, cloud: { image: "x" } }), Code.NotFound);
    const listed = await ADM.listCloudSessions({ hostId: runnerId });
    expect(listed.sessions.length).toBeGreaterThan(1);
    expect(listed.sessions.find((s) => s.id === cloudId)?.userName).toBe("alice-cloud");
    expect((await ts.as(bob.accessToken).hosts.listCloudSessions({ hostId: runnerId })).sessions.every((s) => s.userId !== alice.user!.id)).toBe(true);
    const stopped = await ADM.stopCloudSession({ id: cloudId, end: true });
    expect(stopped.session?.state).toBe(S.ENDED);
    await expect(wakeCloudDevice(sessionDevice)).rejects.toThrow(/ended/);
  });
});
