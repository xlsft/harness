/**
 * VcsService with a GitLab connection against a fake self-managed GitLab: OAuth with PKCE, MR create /
 * merge / merge-when-pipeline-succeeds, discussions, pipelines and job traces, the access-token fallback,
 * and webhooks (X-Gitlab-Token, mapping, idempotency, auto-fix with the job trace).
 */
import { randomUUID } from "node:crypto";
import { Code, ConnectError } from "@connectrpc/connect";
import { asc, gt } from "drizzle-orm";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { PullRequest_State } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { ErrorInfoSchema, ErrorType } from "../src/gen/harness/common/v1/errors_pb.ts";
import { CheckState, GetUserAuthStatusResponse_State, ProviderType, VcsEvent_Kind, type Connection } from "../src/gen/harness/vcs/v1/vcs_pb.ts";
import { vcsIdle } from "../src/vcs/events.ts";
import { startFakeGitLab, type FakeGitLab } from "./helpers/fake-hosts.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let gl: FakeGitLab;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let alice: Awaited<ReturnType<typeof deviceLogin>>;
let bob: Awaited<ReturnType<typeof deviceLogin>>;
let ADM: TestServer["vcs"];
let A: TestServer["vcs"];
let B: TestServer["vcs"];
let conn: Connection;
const REPO = "localhost/acme/sub/app";
const HOOK_SECRET = "gl-hook-secret";

beforeAll(async () => {
  ts = await startTestServer({ HARNESS_VCS_CONFLICT_RECHECK_MS: "0" });
  gl = await startFakeGitLab("localhost");
  admin = await deviceLogin(ts);
  alice = await createUserAndLogin(ts, admin.cookie, "alice@test.local");
  bob = await createUserAndLogin(ts, admin.cookie, "bob@test.local");
  ADM = ts.as(admin.accessToken).vcs;
  A = ts.as(alice.accessToken).vcs;
  B = ts.as(bob.accessToken).vcs;
});

afterAll(async () => {
  await ts.close();
  await gl.close();
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

async function session(token: string, repo: string, gitBranch: string): Promise<string> {
  const id = randomUUID();
  await ts.as(token).sync.upsertSession({ session: { info: { id, title: "s", cwd: "/w", repo, gitBranch } } });
  return id;
}

async function badge(token: string, sessionId: string) {
  return (await ts.as(token).sync.getSession({ sessionId })).session!.info!.pullRequest;
}

let seq = 0;
async function webhook(payload: unknown, opts: { token?: string; uuid?: string } = {}) {
  const res = await fetch(`${ts.url}/api/vcs/gitlab/webhook`, {
    method: "POST",
    headers: { "content-type": "application/json", "x-gitlab-token": opts.token ?? HOOK_SECRET, "x-gitlab-event-uuid": opts.uuid ?? `u-${++seq}` },
    body: JSON.stringify(payload),
  });
  const body = await res.json();
  await vcsIdle();
  return { status: res.status, body };
}

async function lastSeq(): Promise<number> {
  const rows = await getDb().select({ seq: schema.vcsEvent.seq }).from(schema.vcsEvent);
  return rows.reduce((m, r) => Math.max(m, r.seq), 0);
}

async function eventsAfter(s: number) {
  return getDb().select().from(schema.vcsEvent).where(gt(schema.vcsEvent.seq, s)).orderBy(asc(schema.vcsEvent.seq));
}

const project = () => ({ path_with_namespace: "acme/sub/app", web_url: `${gl.url}/acme/sub/app` });

describe("GitLab", () => {
  it("connects with an OAuth application, an access token and a webhook secret", async () => {
    const r = await ADM.putConnection({
      type: ProviderType.GITLAB,
      baseUrl: gl.url,
      displayName: "Self-managed",
      appId: gl.clientId,
      clientSecret: gl.clientSecret,
      webhookSecret: HOOK_SECRET,
      accessToken: gl.accessToken,
      allowPrivateNetwork: true,
    });
    conn = r.connection!;
    expect(conn.appId).toBe(gl.clientId);
    const list = await ADM.listConnections({});
    expect(list.connections.find((c) => c.id === conn.id)).toMatchObject({ healthy: true, lastError: "", displayName: "Self-managed" });
    // the health check used the access token
    expect(gl.seen.some((s) => s.path === "/api/v4/user" && s.auth === `Bearer ${gl.accessToken}`)).toBe(true);
    // updating without secrets keeps them; an empty string removes one
    await ADM.putConnection({ id: conn.id, displayName: "GitLab", allowPrivateNetwork: true });
    await ADM.putConnection({ id: conn.id, allowPrivateNetwork: true, accessToken: "" });
    const row1 = await getDb().query.vcsConnection.findFirst();
    expect(row1!.secretsEnc).not.toBe("");
    await ADM.putConnection({ id: conn.id, allowPrivateNetwork: true, accessToken: gl.accessToken });
    await expectError(ADM.putConnection({ id: conn.id, type: ProviderType.GITHUB, allowPrivateNetwork: true }), Code.InvalidArgument);
  });

  it("authorizes users with OAuth + PKCE", async () => {
    const start = await A.startUserAuth({ connectionId: conn.id });
    const u = new URL(start.authorizationUrl);
    expect(u.origin + u.pathname).toBe(`${gl.url}/oauth/authorize`);
    expect(u.searchParams.get("response_type")).toBe("code");
    expect(u.searchParams.get("scope")).toBe("api");
    expect(u.searchParams.get("code_challenge_method")).toBe("S256");
    const auth = await fetch(start.authorizationUrl, { redirect: "manual" });
    const page = await fetch(auth.headers.get("location")!);
    expect(page.status).toBe(200);
    expect(gl.tokenRequests.at(-1)).toMatchObject({ grant_type: "authorization_code", client_id: gl.clientId });
    expect(gl.tokenRequests.at(-1)!.code_verifier).toBeTruthy();
    const st = await A.getUserAuthStatus({ connectionId: conn.id, flowId: start.flowId });
    expect(st).toMatchObject({ state: GetUserAuthStatusResponse_State.CONNECTED, login: "alice-gl" });
    const repos = await A.listRepositories({ connectionId: conn.id });
    expect(repos.repositories.map((r) => r.key)).toEqual([REPO]);
  });

  it("creates, auto-merges and merges merge requests as the user", async () => {
    const sid = await session(alice.accessToken, REPO, "feat-1");
    const created = await A.createPullRequest({ repositoryKey: REPO, headBranch: "feat-1", baseBranch: "main", title: "Feature", draft: true, sessionId: sid });
    expect(created.pullRequest).toMatchObject({ number: 1n, state: PullRequest_State.DRAFT, draft: true, author: "alice-gl", reviewState: "review_required" });
    expect(gl.mrs.get(1).title).toBe("Draft: Feature");
    expect(gl.seen.find((s) => s.method === "POST" && s.path.endsWith("/merge_requests"))!.auth).toMatch(/^Bearer glu_/);
    expect((await badge(alice.accessToken, sid))?.number).toBe(1);

    await A.setAutoMerge({ repositoryKey: REPO, number: 1n, enabled: true, method: "squash" });
    expect(gl.mrs.get(1)).toMatchObject({ merge_when_pipeline_succeeds: true, squash: true });
    expect((await A.getPullRequest({ repositoryKey: REPO, number: 1n })).pullRequest!.autoMergeEnabled).toBe(true);
    await A.setAutoMerge({ repositoryKey: REPO, number: 1n, enabled: false });
    expect(gl.mrs.get(1).merge_when_pipeline_succeeds).toBe(false);

    await expectError(A.mergePullRequest({ repositoryKey: REPO, number: 1n, method: "rebase" }), Code.InvalidArgument);
    const merged = await A.mergePullRequest({ repositoryKey: REPO, number: 1n, method: "merge" });
    expect(merged.pullRequest!.state).toBe(PullRequest_State.MERGED);
    expect((await badge(alice.accessToken, sid))?.state).toBe(PullRequest_State.MERGED);
  });

  it("posts discussions and replies", async () => {
    const c = await A.postComment({ repositoryKey: REPO, number: 1n, body: "first" });
    expect(c.comment!.id).toMatch(/^[0-9a-f]{40}\/\d+$/);
    const reply = await A.postComment({ repositoryKey: REPO, number: 1n, body: "reply", inReplyTo: c.comment!.id });
    expect(reply.comment!.id.split("/")[0]).toBe(c.comment!.id.split("/")[0]);
    const list = await A.listReviewComments({ repositoryKey: REPO, number: 1n });
    expect(list.comments.map((x) => x.body)).toEqual(["first", "reply"]);
    expect(list.comments[0]!.url).toContain("#note_");
    await expectError(A.postComment({ repositoryKey: REPO, number: 1n, body: "x", inReplyTo: "../../x" }), Code.InvalidArgument);
  });

  it("maps pipelines and jobs to checks and tails job traces", async () => {
    gl.addMr({ iid: 2, source: "feat-2", sha: "glsha-2", pipelineId: 55 });
    gl.jobs.set(55, [
      { id: 801, name: "rspec", stage: "test", status: "failed", web_url: "j1", duration: 12.5, allow_failure: false, failure_reason: "script_failure" },
      { id: 802, name: "lint", stage: "test", status: "success", duration: 3 },
      { id: 803, name: "flaky", stage: "test", status: "failed", allow_failure: true },
      { id: 804, name: "deploy", stage: "deploy", status: "created" },
    ]);
    const r = await A.listChecks({ repositoryKey: REPO, number: 2n });
    expect(r.summary).toMatchObject({ state: CheckState.FAILURE, total: 4, failed: 1, passed: 1, running: 1 });
    const rspec = r.checks.find((c) => c.id === "job:801")!;
    expect(rspec).toMatchObject({ name: "rspec", workflow: "test", durationMs: 12500n, state: CheckState.FAILURE, summary: "script_failure" });
    expect(r.checks.find((c) => c.id === "job:803")!.state).toBe(CheckState.NEUTRAL);
    const bySha = await A.listChecks({ repositoryKey: REPO, sha: "glsha-2" });
    expect(bySha.checks).toHaveLength(4);
    const log = await A.getCheckLog({ repositoryKey: REPO, checkId: "job:801", maxBytes: 100 });
    expect(log.truncated).toBe(true);
    expect(log.log.endsWith("Job 801 failed: exit code 1\n")).toBe(true);
    expect(log.log.length).toBeLessThanOrEqual(100);
  });

  it("falls back to the access token for session owners, for reads and writes", async () => {
    await expectError(B.getPullRequest({ repositoryKey: REPO, number: 2n }), Code.FailedPrecondition, ErrorType.NEEDS_AUTH);
    await session(bob.accessToken, REPO, "");
    expect((await B.getPullRequest({ repositoryKey: REPO, number: 2n })).pullRequest!.number).toBe(2n);
    const posted = await B.postComment({ repositoryKey: REPO, number: 2n, body: "from bob" });
    expect(posted.comment!.author).toBe("group_bot");
    expect(gl.seen.filter((s) => s.method === "POST").at(-1)!.auth).toBe(`Bearer ${gl.accessToken}`);
  });

  it("verifies X-Gitlab-Token, maps hooks to events and runs auto-fix with the job trace", async () => {
    const sid = await session(alice.accessToken, REPO, "feat-2");
    await A.setAutoFix({ sessionId: sid, ciFailures: true, reviewComments: false, conflicts: true });
    const before = await lastSeq();
    expect((await webhook({ object_kind: "push" }, { token: "wrong" })).status).toBe(401);

    const mr = { ...gl.mrs.get(2), url: gl.mrs.get(2).web_url, action: "open", last_commit: { id: "glsha-2" } };
    const opened = await webhook({ object_kind: "merge_request", user: { username: "alice-gl" }, project: project(), object_attributes: mr }, { uuid: "same" });
    expect(opened.status).toBe(202);
    expect((await webhook({ object_kind: "merge_request", user: { username: "alice-gl" }, project: project(), object_attributes: mr }, { uuid: "same" })).body.duplicate).toBe(true);
    expect((await badge(alice.accessToken, sid))?.number).toBe(2);

    await webhook({
      object_kind: "build",
      build_id: 801,
      build_name: "rspec",
      build_stage: "test",
      build_status: "failed",
      build_allow_failure: false,
      build_failure_reason: "script_failure",
      sha: "glsha-2",
      ref: "feat-2",
      project: project(),
    });
    await webhook({
      object_kind: "pipeline",
      object_attributes: { id: 55, status: "failed", sha: "glsha-2", ref: "feat-2" },
      merge_request: { iid: 2 },
      builds: gl.jobs.get(55),
      project: project(),
    });
    await webhook({
      object_kind: "note",
      user: { username: "rev" },
      project: project(),
      object_attributes: { id: 31, discussion_id: "d".repeat(40), noteable_type: "MergeRequest", note: "please fix", url: "n", position: { new_path: "a.rb", new_line: 9 } },
      merge_request: { iid: 2, source_branch: "feat-2" },
    });
    await webhook({ object_kind: "merge_request", user: { username: "rev" }, project: project(), object_attributes: { ...mr, action: "approved" } });
    await webhook({ object_kind: "merge_request", user: { username: "x" }, project: project(), object_attributes: { ...mr, action: "update", detailed_merge_status: "conflict", merge_status: "cannot_be_merged" } });
    await webhook({ object_kind: "push", ref: "refs/heads/feat-2", before: "a", after: "glsha-3", total_commits_count: 1, user_username: "alice", project: project() });

    const evs = await eventsAfter(before);
    const plain = evs.filter((e) => !(e.detail as any).auto_fix);
    expect(plain.map((e) => e.kind)).toEqual([
      VcsEvent_Kind.PR_OPENED,
      VcsEvent_Kind.CHECK_FAILED,
      VcsEvent_Kind.CHECKS_COMPLETED,
      VcsEvent_Kind.REVIEW_COMMENT,
      VcsEvent_Kind.REVIEW_SUBMITTED,
      VcsEvent_Kind.PR_UPDATED,
      VcsEvent_Kind.CONFLICT_DETECTED,
      VcsEvent_Kind.PUSH,
    ]);
    expect(plain.every((e) => e.source === ProviderType.GITLAB && e.repositoryKey === REPO)).toBe(true);
    expect(plain.every((e) => (e.sessions as any[]).some((s) => s.sessionId === sid))).toBe(true);
    const done = plain.find((e) => e.kind === VcsEvent_Kind.CHECKS_COMPLETED)!;
    expect((done.detail as any).failed_jobs.map((j: any) => j.id)).toEqual(["job:801"]);
    expect((done.pullRequest as any).checks.failed).toBe(1);
    expect((plain.find((e) => e.kind === VcsEvent_Kind.REVIEW_COMMENT)!.detail as any).comment_ids).toEqual([`${"d".repeat(40)}/31`]);
    expect((plain.find((e) => e.kind === VcsEvent_Kind.REVIEW_SUBMITTED)!.pullRequest as any).reviewState).toBe("approved");

    // auto-fix: CI (with the job trace fetched through the access token) and conflicts; review comments are off
    const fixes = evs.filter((e) => (e.detail as any).auto_fix);
    expect(fixes.map((e) => e.kind)).toEqual([VcsEvent_Kind.CHECK_FAILED, VcsEvent_Kind.CONFLICT_DETECTED]);
    expect((fixes[0]!.detail as any).auto_fix.log_excerpts["job:801"]).toContain("Job 801 failed");
    expect(fixes.every((e) => (e.sessions as any[]).length === 1 && (e.sessions as any[])[0].sessionId === sid)).toBe(true);
    const jobs = await getDb().select().from(schema.vcsAutofixJob);
    expect(jobs.filter((j) => j.sessionId === sid).map((j) => j.kind).sort()).toEqual([VcsEvent_Kind.CHECK_FAILED, VcsEvent_Kind.CONFLICT_DETECTED].sort());
  });

  it("revokes user authorization", async () => {
    await A.revokeUserAuth({ connectionId: conn.id });
    expect((await A.getUserAuthStatus({ connectionId: conn.id })).state).toBe(GetUserAuthStatusResponse_State.NOT_CONNECTED);
    expect(gl.seen.some((s) => s.path === "/oauth/revoke")).toBe(true);
    await ADM.deleteConnection({ id: conn.id });
    expect(await getDb().select().from(schema.vcsUserToken)).toHaveLength(0);
  });
});
