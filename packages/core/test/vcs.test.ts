/**
 * VcsService with a GitHub App against a fake GHES: admin-only connections and SSRF checks, the manifest
 * flow, user OAuth (PKCE, refresh), token choice, PR create / merge / auto-merge, checks and log tails,
 * webhooks (signatures, mapping, idempotency, session linking, PR badges, auto-fix), WatchEvents and
 * device snapshots (diffing, server wins).
 */
import { randomUUID } from "node:crypto";
import { toJsonString } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { asc, eq, gt } from "drizzle-orm";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { PullRequest_State } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { ErrorInfoSchema, ErrorType } from "../src/gen/harness/common/v1/errors_pb.ts";
import {
  CheckState,
  GetUserAuthStatusResponse_State,
  ProviderType,
  PutConnectionResponseSchema,
  VcsEvent_Kind,
  type Connection,
  type WatchEventsResponse,
} from "../src/gen/harness/vcs/v1/vcs_pb.ts";
import { vcsIdle } from "../src/vcs/events.ts";
import { githubSignature } from "../src/vcs/webhooks.ts";
import { startFakeGitHub, type FakeGitHub } from "./helpers/fake-hosts.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let gh: FakeGitHub;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let alice: Awaited<ReturnType<typeof deviceLogin>>;
let bob: Awaited<ReturnType<typeof deviceLogin>>;
let ADM: TestServer["vcs"];
let A: TestServer["vcs"];
let B: TestServer["vcs"];
let conn: Connection;
let REPO: string;

beforeAll(async () => {
  ts = await startTestServer({ HARNESS_VCS_HEARTBEAT_MS: "150", HARNESS_VCS_CONFLICT_RECHECK_MS: "0" });
  gh = await startFakeGitHub();
  REPO = `127.0.0.1/acme/widgets`;
  admin = await deviceLogin(ts);
  alice = await createUserAndLogin(ts, admin.cookie, "alice@test.local");
  bob = await createUserAndLogin(ts, admin.cookie, "bob@test.local");
  ADM = ts.as(admin.accessToken).vcs;
  A = ts.as(alice.accessToken).vcs;
  B = ts.as(bob.accessToken).vcs;
});

afterAll(async () => {
  await ts.close();
  await gh.close();
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

let deliverySeq = 0;
async function webhook(event: string, payload: unknown, opts: { delivery?: string; secret?: string; signature?: string } = {}) {
  const raw = JSON.stringify(payload);
  const res = await fetch(`${ts.url}/api/vcs/github/webhook`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-github-event": event,
      "x-github-delivery": opts.delivery ?? `d-${++deliverySeq}`,
      "x-github-hook-installation-target-id": String(gh.app.id),
      "x-hub-signature-256": opts.signature ?? githubSignature(opts.secret ?? gh.app.webhookSecret, raw),
    },
    body: raw,
  });
  const body = await res.json();
  await vcsIdle();
  return { status: res.status, body };
}

async function eventsAfter(seq: number) {
  return getDb().select().from(schema.vcsEvent).where(gt(schema.vcsEvent.seq, seq)).orderBy(asc(schema.vcsEvent.seq));
}

async function lastSeq(): Promise<number> {
  const rows = await getDb().select({ seq: schema.vcsEvent.seq }).from(schema.vcsEvent);
  return rows.reduce((m, r) => Math.max(m, r.seq), 0);
}

const repository = () => ({ full_name: "acme/widgets", html_url: `${gh.url}/acme/widgets` });

describe("connections", () => {
  it("are admin-only, keep secrets write-only and pass the SSRF guard", async () => {
    await expectError(A.listConnections({}), Code.PermissionDenied);
    await expectError(A.putConnection({ type: ProviderType.GITHUB, baseUrl: gh.url, allowPrivateNetwork: true }), Code.PermissionDenied);
    await expectError(A.getGitHubAppManifest({ baseUrl: gh.url }), Code.PermissionDenied);
    await expectError(A.deleteConnection({ id: "vcs_x" }), Code.PermissionDenied);

    // private and metadata addresses need allow_private_network
    await expectError(ADM.putConnection({ type: ProviderType.GITLAB, baseUrl: "http://127.0.0.1:9" }), Code.PermissionDenied);
    await expectError(ADM.putConnection({ type: ProviderType.GITLAB, baseUrl: "http://169.254.169.254" }), Code.PermissionDenied);
    await expectError(ADM.putConnection({ type: ProviderType.GH, baseUrl: "https://93.184.215.14" }), Code.InvalidArgument);

    const secret = "glpat-super-secret-value";
    const r = await ADM.putConnection({ type: ProviderType.GITLAB, baseUrl: "https://93.184.215.14/", accessToken: secret, webhookSecret: "hook-secret", appId: "app-1" });
    expect(toJsonString(PutConnectionResponseSchema, r)).not.toContain(secret);
    expect(r.connection!.id).toMatch(/^vcs_/);
    expect(r.connection!.baseUrl).toBe("https://93.184.215.14");
    expect(r.connection!.appId).toBe("app-1");
    const row = await getDb().query.vcsConnection.findFirst({ where: eq(schema.vcsConnection.id, r.connection!.id) });
    expect(row!.secretsEnc).not.toContain(secret);
    expect(row!.clientId).toBe("app-1");
    // the same host cannot be covered twice
    await expectError(ADM.putConnection({ type: ProviderType.GITLAB, baseUrl: "https://93.184.215.14" }), Code.Aborted, ErrorType.CONFLICT);
    await ADM.deleteConnection({ id: r.connection!.id });
    await expectError(ADM.deleteConnection({ id: r.connection!.id }), Code.NotFound);
  });

  it("registers the GitHub App through the manifest flow", async () => {
    const m = await ADM.getGitHubAppManifest({ baseUrl: gh.url, appName: "harness test" });
    const manifest = JSON.parse(m.manifestJson);
    expect(manifest.hook_attributes.url).toBe(`${ts.url}/api/vcs/github/webhook`);
    expect(manifest.redirect_url).toBe(`${ts.url}/api/vcs/github/manifest/callback`);
    expect(manifest.callback_urls).toEqual([`${ts.url}/api/vcs/oauth/callback`]);
    expect(manifest.setup_url).toBe(`${ts.url}/api/vcs/github/setup`);
    expect(Object.keys(manifest.default_permissions).sort()).toEqual(["actions", "checks", "contents", "issues", "metadata", "pull_requests", "statuses"]);
    expect(manifest.default_events).toEqual(expect.arrayContaining(["pull_request", "check_run", "check_suite", "status", "pull_request_review", "pull_request_review_comment", "issue_comment", "push"]));
    expect(m.postUrl.startsWith(`${gh.url}/settings/apps/new?state=`)).toBe(true);
    const org = await ADM.getGitHubAppManifest({ baseUrl: gh.url, organization: "acme" });
    expect(org.postUrl.startsWith(`${gh.url}/organizations/acme/settings/apps/new`)).toBe(true);

    // GitHub redirects the admin's browser back with a one-time code
    const code = gh.manifestCode();
    const res = await fetch(`${ts.url}/api/vcs/github/manifest/callback?code=${code}&state=${encodeURIComponent(m.state)}`, { redirect: "manual" });
    expect(res.status).toBe(303);
    expect(res.headers.get("location")).toBe(`${gh.url}/github-apps/harness-test/installations/new`);
    expect(gh.seen.some((s) => s.method === "POST" && s.path === `/api/v3/app-manifests/${code}/conversions`)).toBe(true);

    // completing the same flow again returns the same connection; a foreign state is unknown
    const again = await ADM.completeGitHubAppManifest({ code, state: m.state });
    conn = again.connection!;
    expect(conn.appId).toBe(String(gh.app.id));
    expect(conn.appSlug).toBe("harness-test");
    expect(conn.type).toBe(ProviderType.GITHUB);
    await expectError(ADM.completeGitHubAppManifest({ code, state: "vgm_nope" }), Code.NotFound);
    await expectError(A.completeGitHubAppManifest({ code, state: m.state }), Code.PermissionDenied);

    const list = await ADM.listConnections({});
    const c = list.connections.find((x) => x.id === conn.id)!;
    expect(c.healthy).toBe(true);
    expect(c.lastError).toBe("");
    expect(c.webhooksOk).toBe(false);
    expect(c.installUrl).toContain("/github-apps/harness-test/installations/new");
    // the health check signed an app JWT that the fake verified
    expect(gh.seen.some((s) => s.path === "/api/v3/app" && s.auth.startsWith("Bearer ey"))).toBe(true);
    const row = await getDb().query.vcsConnection.findFirst({ where: eq(schema.vcsConnection.id, conn.id) });
    expect(row!.secretsEnc).not.toContain("PRIVATE KEY");
    expect(row!.allowPrivateNetwork).toBe(true);
  });
});

describe("user authorization", () => {
  it("runs the GitHub user OAuth flow with PKCE and refreshes expiring tokens", async () => {
    gh.userTokenTtl = 60; // inside the refresh margin: the first use refreshes
    const start = await A.startUserAuth({ connectionId: conn.id });
    expect(start.authorizationUrl.startsWith(`${gh.url}/login/oauth/authorize?`)).toBe(true);
    expect(new URL(start.authorizationUrl).searchParams.get("code_challenge_method")).toBe("S256");
    expect((await A.getUserAuthStatus({ connectionId: conn.id, flowId: start.flowId })).state).toBe(GetUserAuthStatusResponse_State.PENDING);
    expect((await A.getUserAuthStatus({ connectionId: conn.id })).state).toBe(GetUserAuthStatusResponse_State.NOT_CONNECTED);

    const auth = await fetch(start.authorizationUrl, { redirect: "manual" });
    expect(auth.status).toBe(302);
    const callback = auth.headers.get("location")!;
    expect(callback.startsWith(`${ts.url}/api/vcs/oauth/callback?`)).toBe(true);
    const page = await fetch(callback);
    expect(page.status).toBe(200);
    expect(await page.text()).toContain("Connected");
    expect((await fetch(callback)).status).toBe(400);
    expect(gh.tokenRequests.at(-1)!.code_verifier).toBeTruthy();

    const st = await A.getUserAuthStatus({ connectionId: conn.id, flowId: start.flowId });
    expect(st.state).toBe(GetUserAuthStatusResponse_State.CONNECTED);
    expect(st.login).toBe("alice-gh");
    const tok = await getDb().query.vcsUserToken.findFirst({ where: eq(schema.vcsUserToken.connectionId, conn.id) });
    expect(tok!.tokensEnc).not.toContain("ghu_");

    const repos = await A.listRepositories({ connectionId: conn.id });
    expect(repos.repositories.map((r) => r.key)).toEqual([REPO]);
    expect(gh.tokenRequests.some((t) => t.grant_type === "refresh_token")).toBe(true);
    gh.userTokenTtl = 0;
    // another user's flow id is not visible
    await expectError(B.getUserAuthStatus({ connectionId: conn.id, flowId: start.flowId }), Code.NotFound);
  });
});

describe("pull requests", () => {
  let sid: string;

  it("creates, auto-merges and merges as the user and updates the session badge", async () => {
    sid = await session(alice.accessToken, REPO, "feature-x");
    const created = await A.createPullRequest({ repositoryKey: REPO, headBranch: "feature-x", baseBranch: "main", title: "Add x", body: "body", sessionId: sid });
    const pr = created.pullRequest!;
    expect(pr.number).toBe(1n);
    expect(pr.state).toBe(PullRequest_State.OPEN);
    expect(pr.repositoryKey).toBe(REPO);
    expect(pr.headBranch).toBe("feature-x");
    expect(pr.author).toBe("alice-gh");
    const post = gh.seen.find((s) => s.method === "POST" && s.path === "/api/v3/repos/acme/widgets/pulls")!;
    expect(post.auth).toMatch(/^Bearer ghu_alice-gh_/);
    expect((await badge(alice.accessToken, sid))?.number).toBe(1);

    await A.setAutoMerge({ repositoryKey: REPO, number: 1n, enabled: true, method: "squash" });
    const call = gh.graphqlCalls.at(-1)!;
    expect(call.query).toContain("enablePullRequestAutoMerge");
    expect(call.variables).toEqual({ id: "PR_node_1", method: "SQUASH" });
    expect((await A.getPullRequest({ repositoryKey: REPO, number: 1n })).pullRequest!.autoMergeEnabled).toBe(true);
    await A.setAutoMerge({ repositoryKey: REPO, number: 1n, enabled: false });
    expect(gh.graphqlCalls.at(-1)!.query).toContain("disablePullRequestAutoMerge");
    await expectError(A.setAutoMerge({ repositoryKey: REPO, number: 1n, enabled: true, method: "octopus" }), Code.InvalidArgument);

    const merged = await A.mergePullRequest({ repositoryKey: REPO, number: 1n, method: "squash" });
    expect(merged.pullRequest!.state).toBe(PullRequest_State.MERGED);
    expect(gh.pulls.get(1).merge_method_used).toBe("squash");
    expect((await badge(alice.accessToken, sid))?.state).toBe(PullRequest_State.MERGED);

    // a client upload without pull_request keeps the server-set badge
    await ts.as(alice.accessToken).sync.upsertSession({ session: { info: { id: sid, title: "renamed", cwd: "/w", repo: REPO, gitBranch: "feature-x" } } });
    expect((await badge(alice.accessToken, sid))?.number).toBe(1);
  });

  it("chooses tokens: user token to act, installation token for reads by session owners and admins", async () => {
    await expectError(B.getPullRequest({ repositoryKey: REPO, number: 1n }), Code.FailedPrecondition, ErrorType.NEEDS_AUTH);
    await expectError(B.createPullRequest({ repositoryKey: REPO, headBranch: "b", baseBranch: "main", title: "t" }), Code.FailedPrecondition, ErrorType.NEEDS_AUTH);
    await expectError(B.listRepositories({ connectionId: conn.id }), Code.FailedPrecondition, ErrorType.NEEDS_AUTH);
    await expectError(B.getPullRequest({ repositoryKey: "nohost.example/a/b", number: 1n }), Code.FailedPrecondition, ErrorType.FAILED_PRECONDITION);
    await expectError(B.getPullRequest({ repositoryKey: "not a key", number: 1n }), Code.InvalidArgument);

    // bob works on the repository: reads go through the installation token, writes still need his token
    await session(bob.accessToken, REPO, "");
    const got = await B.getPullRequest({ repositoryKey: REPO, number: 1n });
    expect(got.pullRequest!.state).toBe(PullRequest_State.MERGED);
    expect(gh.seen.filter((s) => s.path === "/api/v3/repos/acme/widgets/pulls/1").at(-1)!.auth).toMatch(/^Bearer ghs_inst_/);
    await expectError(B.postComment({ repositoryKey: REPO, number: 1n, body: "hi" }), Code.FailedPrecondition, ErrorType.NEEDS_AUTH);

    // admins without a user token list the installation's repositories
    const repos = await ADM.listRepositories({ connectionId: conn.id });
    expect(repos.repositories.map((r) => r.key)).toEqual([REPO]);
    expect((await getDb().select().from(schema.vcsInstallation)).map((i) => i.accountLogin)).toEqual(["acme"]);
  });

  it("posts comments and replies, and lists them with resolution", async () => {
    gh.reviewComments.set(1, [{ id: 501, user: { login: "rev" }, body: "nit", path: "src/a.ts", line: 3, html_url: "u", created_at: "2026-01-01T00:00:00Z", resolved: true }]);
    const c = await A.postComment({ repositoryKey: REPO, number: 1n, body: "thanks" });
    expect(c.comment!.id).toMatch(/^c:\d+$/);
    const reply = await A.postComment({ repositoryKey: REPO, number: 1n, body: "fixed", inReplyTo: "r:501" });
    expect(reply.comment!.id).toMatch(/^r:\d+$/);
    expect(gh.seen.some((s) => s.path === "/api/v3/repos/acme/widgets/pulls/1/comments/501/replies")).toBe(true);
    const all = await A.listReviewComments({ repositoryKey: REPO, number: 1n });
    expect(all.comments.find((x) => x.id === "r:501")!.resolved).toBe(true);
    const open = await A.listReviewComments({ repositoryKey: REPO, number: 1n, unresolvedOnly: true });
    expect(open.comments.map((x) => x.id)).not.toContain("r:501");
    expect(open.comments.length).toBe(all.comments.length - 1);
  });

  it("combines check runs and statuses and tails Actions logs through the redirect", async () => {
    gh.addPull({ number: 2, head: "feature-y", sha: "sha-y", title: "Feature y" });
    gh.checkRuns.set("sha-y", [
      {
        id: 901,
        name: "test",
        status: "completed",
        conclusion: "failure",
        html_url: `${gh.url}/acme/widgets/runs/901`,
        started_at: "2026-01-01T00:00:00Z",
        completed_at: "2026-01-01T00:01:30Z",
        output: { title: "2 tests failed", summary: "see log" },
        app: { name: "GitHub Actions" },
      },
      { id: 902, name: "lint", status: "in_progress", html_url: "x", app: { name: "GitHub Actions" } },
    ]);
    gh.statuses.set("sha-y", [{ id: 31, context: "ci/legacy", state: "success", target_url: "t", description: "ok" }]);
    const r = await A.listChecks({ repositoryKey: REPO, number: 2n });
    expect(r.checks.map((c) => c.id).sort()).toEqual(["cr:901", "cr:902", "st:31"]);
    expect(r.summary).toMatchObject({ state: CheckState.FAILURE, total: 3, failed: 1, running: 1, passed: 1 });
    const failed = r.checks.find((c) => c.id === "cr:901")!;
    expect(failed.durationMs).toBe(90_000n);
    expect(failed.summary).toContain("2 tests failed");
    expect(failed.workflow).toBe("GitHub Actions");

    const log = await A.getCheckLog({ repositoryKey: REPO, checkId: "cr:901", maxBytes: 1000 });
    expect(log.truncated).toBe(true);
    expect(Buffer.byteLength(log.log)).toBeLessThanOrEqual(1000);
    expect(log.log.endsWith("Error: tests failed")).toBe(true);
    // the blob storage hop carried no credentials
    expect(gh.blobSeen.at(-1)!.auth).toBe("");
    expect(gh.blobSeen.at(-1)!.path).toBe("/logs/901?sig=signed");
    await expectError(A.getCheckLog({ repositoryKey: REPO, checkId: "st:31" }), Code.InvalidArgument);

    const pr = await A.getPullRequest({ repositoryKey: REPO, number: 2n });
    expect(pr.pullRequest!.checks).toMatchObject({ state: CheckState.FAILURE, total: 3 });
    expect(pr.pullRequest!.reviewState).toBe("none");
  });
});

describe("webhooks", () => {
  let s2: string;
  let base: number;

  beforeAll(async () => {
    s2 = await session(alice.accessToken, REPO, "feature-y");
    await A.setAutoFix({ sessionId: s2, ciFailures: true, reviewComments: true, conflicts: true });
    await expectError(B.setAutoFix({ sessionId: s2, ciFailures: true }), Code.NotFound);
    base = await lastSeq();
  });

  it("verifies signatures and is idempotent by delivery id", async () => {
    expect((await webhook("ping", { zen: "hi" }, { secret: "wrong" })).status).toBe(401);
    expect((await webhook("ping", { zen: "hi" }, { signature: "sha256=00" })).status).toBe(401);
    const res = await fetch(`${ts.url}/api/vcs/github/webhook`, { method: "POST", body: "{}" });
    expect(res.status).toBe(400);
    expect((await webhook("ping", { zen: "hi" })).status).toBe(202);
    const row = await getDb().query.vcsConnection.findFirst({ where: eq(schema.vcsConnection.id, conn.id) });
    expect(row!.lastWebhookAt).not.toBeNull();
    expect((await ADM.listConnections({})).connections.find((c) => c.id === conn.id)!.webhooksOk).toBe(true);
  });

  it("maps pull_request events, links sessions by repo + branch and sets the PR badge", async () => {
    const payload = { action: "opened", pull_request: gh.pulls.get(2), repository: repository(), sender: { login: "alice-gh" } };
    const r = await webhook("pull_request", payload, { delivery: "dup-1" });
    expect(r.status).toBe(202);
    expect(r.body.events).toBe(1);
    const dup = await webhook("pull_request", payload, { delivery: "dup-1" });
    expect(dup).toEqual({ status: 200, body: { ok: true, duplicate: true } });
    const evs = await eventsAfter(base);
    const opened = evs.find((e) => e.kind === VcsEvent_Kind.PR_OPENED)!;
    expect(opened.repositoryKey).toBe(REPO);
    expect(opened.source).toBe(ProviderType.GITHUB);
    expect(opened.sessions).toEqual([{ sessionId: s2, userId: expect.any(String) }]);
    expect(evs.filter((e) => e.deliveryId === "dup-1")).toHaveLength(1);
    const b = await badge(alice.accessToken, s2);
    expect(b?.number).toBe(2);
    expect(b?.state).toBe(PullRequest_State.OPEN);
    expect(b?.url).toBe(`${gh.url}/acme/widgets/pull/2`);
  });

  it("maps check_run failures and enqueues auto-fix jobs with a log excerpt", async () => {
    const before = await lastSeq();
    const cr = { ...gh.checkRuns.get("sha-y")![0], head_sha: "sha-y", pull_requests: [{ number: 2, head: { ref: "feature-y", sha: "sha-y" }, base: { ref: "main" } }] };
    expect((await webhook("check_run", { action: "completed", check_run: cr, repository: repository() })).status).toBe(202);
    const evs = await eventsAfter(before);
    const failed = evs.filter((e) => e.kind === VcsEvent_Kind.CHECK_FAILED);
    expect(failed).toHaveLength(2); // the webhook event and the auto-fix follow-up
    expect((failed[0]!.detail as any).checks[0].id).toBe("cr:901");
    const follow = failed[1]!.detail as any;
    expect(follow.auto_fix.session_id).toBe(s2);
    expect(follow.auto_fix.log_excerpts["cr:901"]).toContain("Error: tests failed");

    const jobs = await fetch(`${ts.url}/api/vcs/sessions/${s2}/autofix-jobs`, { headers: { authorization: `Bearer ${alice.accessToken}` } }).then((r) => r.json());
    expect(jobs.jobs).toHaveLength(1);
    expect(jobs.jobs[0]).toMatchObject({ session_id: s2, kind: VcsEvent_Kind.CHECK_FAILED, status: "pending", pr_number: 2 });
    expect(jobs.jobs[0].payload.failed_checks[0]).toMatchObject({ id: "cr:901", name: "test" });
    const denied = await fetch(`${ts.url}/api/vcs/sessions/${s2}/autofix-jobs`, { headers: { authorization: `Bearer ${bob.accessToken}` } });
    expect(denied.status).toBe(404);

    // check_suite completion carries the summary of the stored checks
    const suite = { action: "completed", check_suite: { head_sha: "sha-y", head_branch: "feature-y", conclusion: "failure", pull_requests: [{ number: 2 }] }, repository: repository() };
    await webhook("check_suite", suite);
    const done = (await eventsAfter(before)).find((e) => e.kind === VcsEvent_Kind.CHECKS_COMPLETED)!;
    expect((done.pullRequest as any).checks.state).toBe(CheckState.FAILURE);
  });

  it("maps reviews, review comments, conversation comments, statuses and installations", async () => {
    const before = await lastSeq();
    const pr2 = gh.pulls.get(2);
    await webhook("pull_request_review", { action: "submitted", review: { id: 9, state: "CHANGES_REQUESTED", user: { login: "rev" }, body: "no" }, pull_request: pr2, repository: repository() });
    await webhook("pull_request_review_comment", {
      action: "created",
      comment: { id: 777, body: "rename this", path: "src/x.ts", line: 4, user: { login: "rev" }, html_url: "h" },
      pull_request: pr2,
      repository: repository(),
    });
    await webhook("issue_comment", { action: "created", issue: { number: 2, pull_request: { html_url: "x" } }, comment: { id: 778, body: "lgtm?", user: { login: "rev" } }, repository: repository() });
    await webhook("issue_comment", { action: "created", issue: { number: 3 }, comment: { id: 779 }, repository: repository() }); // not a PR
    await webhook("status", { id: 32, sha: "sha-y", context: "ci/ext", state: "error", description: "boom", target_url: "t", branches: [{ name: "feature-y" }], repository: repository() });
    await webhook("installation", { action: "created", installation: { id: 88, account: { login: "Other-Org", type: "Organization" } } });
    const evs = await eventsAfter(before);
    const kinds = evs.map((e) => e.kind);
    const review = evs.find((e) => e.kind === VcsEvent_Kind.REVIEW_SUBMITTED)!;
    expect((review.detail as any).state).toBe("changes_requested");
    expect((review.pullRequest as any).reviewState).toBe("changes_requested");
    const comments = evs.filter((e) => e.kind === VcsEvent_Kind.REVIEW_COMMENT && !(e.detail as any).auto_fix);
    expect(comments.map((e) => (e.detail as any).comment_ids[0])).toEqual(["r:777", "c:778"]);
    expect(comments.every((e) => e.prNumber === 2 && e.headBranch === "feature-y")).toBe(true);
    // review comments trigger auto-fix (flag on): follow-ups for each comment
    expect(evs.filter((e) => e.kind === VcsEvent_Kind.REVIEW_COMMENT && (e.detail as any).auto_fix)).toHaveLength(2);
    expect(kinds).toContain(VcsEvent_Kind.CHECK_FAILED);
    const statusEv = evs.find((e) => e.kind === VcsEvent_Kind.CHECK_FAILED && !(e.detail as any).auto_fix)!;
    expect((statusEv.detail as any).checks[0].id).toBe("st:32");
    const inst = await getDb().select().from(schema.vcsInstallation).where(eq(schema.vcsInstallation.installationId, 88));
    expect(inst[0]!.accountLogin).toBe("other-org");
  });

  it("detects conflicts after a push to the base branch", async () => {
    const before = await lastSeq();
    Object.assign(gh.pulls.get(2), { mergeable: false, mergeable_state: "dirty" });
    const r = await webhook("push", { ref: "refs/heads/main", before: "a", after: "b", commits: [{}], repository: repository(), pusher: { name: "x" } });
    expect(r.status).toBe(202);
    const evs = await eventsAfter(before);
    expect(evs.map((e) => e.kind)).toEqual(expect.arrayContaining([VcsEvent_Kind.PUSH, VcsEvent_Kind.CONFLICT_DETECTED]));
    const conflict = evs.find((e) => e.kind === VcsEvent_Kind.CONFLICT_DETECTED && !(e.detail as any).auto_fix)!;
    expect(conflict.prNumber).toBe(2);
    expect(conflict.sessions).toEqual([{ sessionId: s2, userId: expect.any(String) }]);
    // a push to the PR's head branch links to its session
    await webhook("push", { ref: "refs/heads/feature-y", before: "sha-y", after: "sha-y2", commits: [{}], repository: repository() });
    const push = (await eventsAfter(before)).filter((e) => e.kind === VcsEvent_Kind.PUSH).at(-1)!;
    expect(push.headBranch).toBe("feature-y");
    expect(push.sessions).toHaveLength(1);
  });
});

async function collectWatch(token: string, req: Record<string, unknown>, until: (evs: WatchEventsResponse[]) => boolean, during?: () => Promise<void>) {
  const ac = new AbortController();
  const out: WatchEventsResponse[] = [];
  const timer = setTimeout(() => ac.abort(), 8000);
  let started = false;
  try {
    for await (const ev of ts.as(token).vcs.watchEvents(req, { signal: ac.signal })) {
      out.push(ev);
      if (ev.event.case === "caughtUp" && !started && during) {
        started = true;
        void during();
      }
      if (until(out)) break;
    }
  } catch (err) {
    if (!ac.signal.aborted) throw err;
  } finally {
    clearTimeout(timer);
    ac.abort();
  }
  return out;
}

describe("WatchEvents", () => {
  it("catches up after after_id, then streams live events and heartbeats, filtered by visibility", async () => {
    const all = await eventsAfter(0);
    const aliceEvents = await collectWatch(
      alice.accessToken,
      { afterId: "vev_0" },
      (evs) => evs.some((e) => e.event.case === "heartbeat") && evs.some((e) => e.event.case === "vcsEvent" && e.event.value.kind === VcsEvent_Kind.PR_UPDATED),
      async () => {
        await webhook("pull_request", { action: "edited", pull_request: { ...gh.pulls.get(2), title: "Feature y (edited)" }, repository: repository() });
      },
    );
    const caught = aliceEvents.findIndex((e) => e.event.case === "caughtUp");
    expect(caught).toBeGreaterThan(0);
    const catchUp = aliceEvents.slice(0, caught).map((e) => (e.event.case === "vcsEvent" ? e.event.value : undefined)!);
    // alice has a token with access to the repo: she sees every event of it
    expect(catchUp.map((e) => e.id)).toEqual(all.map((r) => `vev_${r.seq}`));
    const live = aliceEvents.slice(caught + 1).find((e) => e.event.case === "vcsEvent")!;
    if (live.event.case !== "vcsEvent") throw new Error("expected an event");
    expect(live.event.value.kind).toBe(VcsEvent_Kind.PR_UPDATED);
    expect(live.event.value.pullRequest!.title).toBe("Feature y (edited)");
    expect(live.event.value.source).toBe(ProviderType.GITHUB);
    expect(JSON.parse(live.event.value.detailJson).action).toBe("edited");
    expect(live.event.value.sessionIds.length).toBe(1);

    // resume after the live event: nothing to catch up
    const resumed = await collectWatch(alice.accessToken, { afterId: live.event.value.id }, (evs) => evs.some((e) => e.event.case === "caughtUp"));
    expect(resumed.map((e) => e.event.case)).toEqual(["caughtUp"]);

    // bob has no token and no linked session: nothing
    const bobs = await collectWatch(bob.accessToken, { afterId: "vev_0" }, (evs) => evs.some((e) => e.event.case === "caughtUp"));
    expect(bobs.map((e) => e.event.case)).toEqual(["caughtUp"]);

    // admins see everything, including the session ids
    const adm = await collectWatch(admin.accessToken, { afterId: "vev_0" }, (evs) => evs.some((e) => e.event.case === "caughtUp"));
    expect(adm.filter((e) => e.event.case === "vcsEvent").length).toBe(all.length + 1);

    // filters
    const other = await collectWatch(admin.accessToken, { afterId: "vev_0", repositoryKeys: ["github.com/x/y"] }, (evs) => evs.some((e) => e.event.case === "caughtUp"));
    expect(other.map((e) => e.event.case)).toEqual(["caughtUp"]);
    await expectError(collectWatch(alice.accessToken, { afterId: "bogus" }, () => true), Code.InvalidArgument);
  });

  it("purges events after 7 days", async () => {
    const { purgeVcs } = await import("../src/vcs/events.ts");
    const n = (await eventsAfter(0)).length;
    expect((await purgeVcs(Date.now())).events).toBe(0);
    expect((await purgeVcs(Date.now() + 8 * 86_400_000)).events).toBe(n);
  });
});

describe("device providers", () => {
  const KEY = "ghe.corp.example/team/svc";
  const prBase = { repositoryKey: KEY, number: 7n, title: "Fix", url: "https://ghe.corp.example/team/svc/pull/7", headBranch: "fix-1", baseBranch: "main", author: "alice", reviewState: "none" };

  it("stores reported providers", async () => {
    await A.reportDeviceProviders({ providers: [{ type: ProviderType.GH, version: "gh version 2.80.0", hosts: [{ host: "GHE.corp.example", login: "alice" }] }] });
    const rows = await getDb().select().from(schema.vcsDeviceProvider);
    expect(rows).toHaveLength(1);
    expect(rows[0]!.hosts).toEqual([{ host: "ghe.corp.example", login: "alice" }]);
    await expectError(A.reportDeviceProviders({ providers: [{ type: ProviderType.GITHUB }] }), Code.InvalidArgument);
  });

  it("diffs snapshots into events with source GH, links sessions, and lets the server win", async () => {
    const s3 = await session(alice.accessToken, KEY, "fix-1");
    const key = `${KEY}#7`;
    const running = { id: "gh:1", name: "build", state: CheckState.RUNNING, url: "u", workflow: "ci" };
    const r1 = await A.reportDeviceSnapshot({ provider: ProviderType.GH, pullRequests: [{ ...prBase, state: PullRequest_State.OPEN }], checks: { [key]: { checks: [running] } } });
    expect(r1.eventsEmitted).toBe(1);
    expect((await badge(alice.accessToken, s3))?.number).toBe(7);
    const r2 = await A.reportDeviceSnapshot({ provider: ProviderType.GH, pullRequests: [{ ...prBase, state: PullRequest_State.OPEN }], checks: { [key]: { checks: [running] } } });
    expect(r2.eventsEmitted).toBe(0);
    const before = await lastSeq();
    const r3 = await A.reportDeviceSnapshot({
      provider: ProviderType.GH,
      pullRequests: [{ ...prBase, state: PullRequest_State.OPEN }],
      checks: { [key]: { checks: [{ ...running, state: CheckState.FAILURE }] } },
    });
    expect(r3.eventsEmitted).toBe(2);
    const evs = await eventsAfter(before);
    expect(evs.map((e) => e.kind).sort()).toEqual([VcsEvent_Kind.CHECKS_COMPLETED, VcsEvent_Kind.CHECK_FAILED].sort());
    expect(evs.every((e) => e.source === ProviderType.GH && e.sessions && (e.sessions as any[])[0].sessionId === s3)).toBe(true);
    const r4 = await A.reportDeviceSnapshot({ provider: ProviderType.GH, pullRequests: [{ ...prBase, state: PullRequest_State.MERGED }] });
    expect(r4.eventsEmitted).toBe(1);
    expect((await badge(alice.accessToken, s3))?.state).toBe(PullRequest_State.MERGED);

    // a host covered by a server connection is ignored
    const covered = await A.reportDeviceSnapshot({ provider: ProviderType.GH, pullRequests: [{ ...prBase, repositoryKey: REPO, number: 99n, state: PullRequest_State.OPEN }] });
    expect(covered.eventsEmitted).toBe(0);
    expect(await getDb().query.vcsPullRequest.findFirst({ where: eq(schema.vcsPullRequest.number, 99) })).toBeUndefined();
    await expectError(A.reportDeviceSnapshot({ provider: ProviderType.GITHUB }), Code.InvalidArgument);

    // the reporter sees its device events; bob does not
    const bobs = await collectWatch(bob.accessToken, { afterId: `vev_${before}` }, (x) => x.some((e) => e.event.case === "caughtUp"));
    expect(bobs.map((e) => e.event.case)).toEqual(["caughtUp"]);
    const alices = await collectWatch(alice.accessToken, { afterId: `vev_${before}`, sessionIds: [s3] }, (x) => x.some((e) => e.event.case === "caughtUp"));
    expect(alices.filter((e) => e.event.case === "vcsEvent").length).toBe(3);
  });

  it("links sessions explicitly and revokes user authorization", async () => {
    const s4 = await session(alice.accessToken, "", "");
    const linked = await A.linkSession({ sessionId: s4, repositoryKey: REPO, branch: "feature-y" });
    expect(linked.pullRequest!.number).toBe(2n);
    expect((await badge(alice.accessToken, s4))?.number).toBe(2);
    await expectError(B.linkSession({ sessionId: s4, repositoryKey: REPO, branch: "x" }), Code.NotFound);
    await A.revokeUserAuth({ connectionId: conn.id });
    expect((await A.getUserAuthStatus({ connectionId: conn.id })).state).toBe(GetUserAuthStatusResponse_State.NOT_CONNECTED);
    // the explicit link still resolves from the stored PR state without a token
    const again = await A.linkSession({ sessionId: s4, repositoryKey: REPO, branch: "feature-y" });
    expect(again.pullRequest!.number).toBe(2n);
  });
});
