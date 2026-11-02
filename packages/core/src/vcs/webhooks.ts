/**
 * Webhooks: POST /api/vcs/github/webhook (X-Hub-Signature-256) and POST /api/vcs/gitlab/webhook
 * (X-Gitlab-Token). Verified deliveries are processed once (idempotent by delivery id), mapped to
 * VcsEvents, linked to sessions and published (pipeline.ts).
 *
 * GitHub: pull_request, check_suite, check_run, status, pull_request_review, pull_request_review_comment,
 * issue_comment (on PRs), push, installation / installation_repositories.
 * GitLab: merge_request, pipeline, build (job), note (on MRs), push.
 * Conflicts: a PR becoming conflicted in a pull_request / merge_request payload, and a re-check of open PRs
 * targeting a branch shortly after a push to it.
 */
import { createHmac, timingSafeEqual } from "node:crypto";
import { PullRequest_State } from "../gen/harness/agent/v1/agent_pb.ts";
import { CheckState, ProviderType, VcsEvent_Kind } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { sha256 } from "../lib/ids.ts";
import { systemClient } from "./clients.ts";
import { allConnections, connSecrets, recordWebhook, type ConnectionRow } from "./connections.ts";
import {
  background,
  checksOf,
  claimDelivery,
  getStoredPr,
  mergePr,
  prOf,
  releaseDelivery,
  savePr,
  storedPrs,
  type EventInput,
  type StoredPrRow,
} from "./events.ts";
import { deleteInstallation, upsertInstallation } from "./github-app.ts";
import { githubCheckRun, githubPr, githubRepoKey, githubStatus } from "./github.ts";
import { gitlabJob, gitlabJobState, gitlabMr, gitlabRepoKey } from "./gitlab.ts";
import { publishEvents } from "./pipeline.ts";
import type { CheckData, PrData } from "./types.ts";
import { aggregateReviews, isOpen, isoTime, num, parseRepoKey, str, summarize, upsertCheck } from "./util.ts";

export interface WebhookResult {
  status: number;
  body: Record<string, unknown>;
}

function safeEqual(a: string, b: string): boolean {
  const x = Buffer.from(a);
  const y = Buffer.from(b);
  return x.length === y.length && timingSafeEqual(x, y);
}

export function githubSignature(secret: string, body: string): string {
  return `sha256=${createHmac("sha256", secret).update(body).digest("hex")}`;
}

function conflictRecheckMs(): number {
  const n = Number(process.env.HARNESS_VCS_CONFLICT_RECHECK_MS ?? "");
  return Number.isFinite(n) && n >= 0 ? n : 10_000;
}

async function runDelivery(conn: ConnectionRow, deliveryId: string, map: () => Promise<EventInput[]>): Promise<WebhookResult> {
  await recordWebhook(conn.id);
  if (!(await claimDelivery(conn.id, deliveryId))) return { status: 200, body: { ok: true, duplicate: true } };
  try {
    const inputs = await map();
    const rows = await publishEvents(inputs);
    return { status: 202, body: { ok: true, events: rows.length } };
  } catch (err) {
    // Let the code host redeliver.
    await releaseDelivery(conn.id, deliveryId).catch(() => {});
    throw err;
  }
}

// --- shared --------------------------------------------------------------------------------------

/** The PR as we know it: stored state, else fetched with the system client, else the fallback. */
async function knownPr(conn: ConnectionRow, repoKey: string, number: number, fallback?: () => PrData): Promise<{ pr?: PrData; row?: StoredPrRow }> {
  const row = await getStoredPr(repoKey, number);
  if (row) return { pr: prOf(row), row };
  const client = systemClient(conn);
  if (client) {
    try {
      const pr = await client.getPullRequest(parseRepoKey(repoKey), number);
      await savePr(pr, { source: conn.type, connectionId: conn.id });
      return { pr, row: await getStoredPr(repoKey, number) };
    } catch {
      // fall through
    }
  }
  const pr = fallback?.();
  if (pr) await savePr(pr, { source: conn.type, connectionId: conn.id });
  return { pr, row: pr ? await getStoredPr(repoKey, number) : undefined };
}

/** Applies a check result to the PRs of a commit; returns the PRs with their updated summaries. */
async function applyCheck(conn: ConnectionRow, rows: StoredPrRow[], check: CheckData | undefined, replaceAll?: CheckData[]): Promise<PrData[]> {
  const out: PrData[] = [];
  for (const row of rows) {
    const checks = replaceAll ?? (check ? upsertCheck(checksOf(row), check) : checksOf(row));
    const pr = { ...prOf(row), checks: summarize(checks) };
    await savePr(pr, { source: conn.type, connectionId: conn.id, checks });
    out.push(pr);
  }
  return out;
}

function conflictEvent(base: Partial<EventInput>, prev: PrData | undefined, pr: PrData): EventInput[] {
  if (!pr.hasConflicts || prev?.hasConflicts || !isOpen(pr)) return [];
  return [{ ...base, kind: VcsEvent_Kind.CONFLICT_DETECTED, repositoryKey: pr.repositoryKey, pr, detail: { base_branch: pr.baseBranch, head_branch: pr.headBranch } } as EventInput];
}

/** After a push to `branch`, re-reads open PRs targeting it and reports PRs that became conflicted. */
function scheduleConflictRecheck(conn: ConnectionRow, repoKey: string, branch: string): void {
  const client = systemClient(conn);
  if (!client) return;
  background(async () => {
    const targets = (await storedPrs(repoKey, { baseBranch: branch })).filter((r) => isOpen(prOf(r)));
    if (!targets.length) return;
    await new Promise((r) => setTimeout(r, conflictRecheckMs()));
    const repo = parseRepoKey(repoKey);
    const events: EventInput[] = [];
    for (const row of targets) {
      try {
        const prev = prOf((await getStoredPr(repoKey, row.number)) ?? row);
        const fresh = mergePr(prev, await client.getPullRequest(repo, row.number));
        await savePr(fresh, { source: conn.type, connectionId: conn.id });
        events.push(...conflictEvent({ source: conn.type, connectionId: conn.id }, prev, fresh));
      } catch (err) {
        console.error("[harness] conflict re-check failed", err);
      }
    }
    await publishEvents(events);
  });
}

function branchOf(ref: unknown): string {
  const r = str(ref);
  return r.startsWith("refs/heads/") ? r.slice("refs/heads/".length) : "";
}

// --- GitHub --------------------------------------------------------------------------------------

export async function handleGitHubWebhook(headers: Headers, raw: string): Promise<WebhookResult> {
  const sig = headers.get("x-hub-signature-256") ?? "";
  const event = headers.get("x-github-event") ?? "";
  if (!sig || !event) return { status: 400, body: { error: "missing X-Hub-Signature-256 or X-GitHub-Event" } };
  const target = headers.get("x-github-hook-installation-target-id") ?? "";
  const conns = (await allConnections())
    .filter((c) => c.type === ProviderType.GITHUB)
    .sort((a, b) => Number(b.appId === target) - Number(a.appId === target));
  const conn = conns.find((c) => {
    const secret = connSecrets(c).webhookSecret;
    return !!secret && safeEqual(githubSignature(secret, raw), sig);
  });
  if (!conn) return { status: 401, body: { error: "invalid signature" } };
  let payload: any;
  try {
    payload = JSON.parse(raw);
  } catch {
    return { status: 400, body: { error: "invalid JSON" } };
  }
  const delivery = headers.get("x-github-delivery") || sha256(raw);
  return runDelivery(conn, delivery, () => mapGitHub(conn, event, payload, delivery));
}

export async function mapGitHub(conn: ConnectionRow, event: string, payload: any, deliveryId: string): Promise<EventInput[]> {
  const base = { source: ProviderType.GITHUB, connectionId: conn.id, deliveryId };
  const action = str(payload.action);
  switch (event) {
    case "installation":
    case "installation_repositories": {
      const inst = payload.installation;
      if (!inst?.id) return [];
      if (event === "installation" && action === "deleted") await deleteInstallation(conn.id, num(inst.id));
      else await upsertInstallation(conn.id, { id: num(inst.id), login: str(inst.account?.login), type: str(inst.account?.type), suspended: action === "suspend" || !!inst.suspended_at });
      return [];
    }

    case "pull_request": {
      const repoKey = githubRepoKey(payload.repository);
      const raw = payload.pull_request;
      const prevRow = await getStoredPr(repoKey, num(raw.number));
      const prev = prevRow ? prOf(prevRow) : undefined;
      const pr = mergePr(prev, githubPr(raw, repoKey));
      if (pr.reviewers && Object.keys(pr.reviewers).length) pr.reviewState = aggregateReviews(pr.reviewers, (raw.requested_reviewers?.length ?? 0) > 0);
      const headMoved = action === "synchronize" || (prev?.headSha && pr.headSha && prev.headSha !== pr.headSha);
      if (headMoved) pr.checks = undefined;
      await savePr(pr, { source: ProviderType.GITHUB, connectionId: conn.id, checks: headMoved ? [] : undefined });
      let kind = VcsEvent_Kind.PR_UPDATED;
      if (action === "opened" || action === "reopened" || action === "ready_for_review") kind = VcsEvent_Kind.PR_OPENED;
      else if (action === "closed") kind = pr.state === PullRequest_State.MERGED ? VcsEvent_Kind.PR_MERGED : VcsEvent_Kind.PR_CLOSED;
      return [
        { ...base, kind, repositoryKey: repoKey, pr, occurredAt: pr.updatedAt, detail: { action, sender: str(payload.sender?.login) } },
        ...conflictEvent(base, prev, pr),
      ];
    }

    case "check_run": {
      const repoKey = githubRepoKey(payload.repository);
      const cr = payload.check_run;
      const check = githubCheckRun(cr);
      const sha = str(cr.head_sha);
      const rows = await prsForCommit(conn, repoKey, sha, cr.pull_requests ?? [], payload.repository);
      const prs = await applyCheck(conn, rows, check);
      if (action !== "completed" || check.state !== CheckState.FAILURE) return [];
      const detail = { sha, checks: [{ id: check.id, name: check.name, url: check.url, summary: check.summary }], workflow: check.workflow };
      if (!prs.length) return [{ ...base, kind: VcsEvent_Kind.CHECK_FAILED, repositoryKey: repoKey, headBranch: str(cr.check_suite?.head_branch), detail }];
      return prs.map((pr) => ({ ...base, kind: VcsEvent_Kind.CHECK_FAILED, repositoryKey: repoKey, pr, detail }));
    }

    case "check_suite": {
      if (action !== "completed") return [];
      const repoKey = githubRepoKey(payload.repository);
      const cs = payload.check_suite;
      const sha = str(cs.head_sha);
      const rows = await prsForCommit(conn, repoKey, sha, cs.pull_requests ?? [], payload.repository);
      const prs = await applyCheck(conn, rows, undefined);
      const detail = { sha, conclusion: str(cs.conclusion), app: str(cs.app?.name) };
      if (!prs.length) return [{ ...base, kind: VcsEvent_Kind.CHECKS_COMPLETED, repositoryKey: repoKey, headBranch: str(cs.head_branch), detail }];
      return prs.map((pr) => ({ ...base, kind: VcsEvent_Kind.CHECKS_COMPLETED, repositoryKey: repoKey, pr, detail: { ...detail, summary: pr.checks } }));
    }

    case "status": {
      const repoKey = githubRepoKey(payload.repository);
      const check = githubStatus(payload);
      const sha = str(payload.sha);
      let rows = await storedPrs(repoKey, { headSha: sha });
      if (!rows.length) {
        for (const b of payload.branches ?? []) rows.push(...(await storedPrs(repoKey, { headBranch: str(b.name) })).filter((r) => isOpen(prOf(r))));
      }
      rows = rows.filter((r, i) => rows.findIndex((x) => x.number === r.number) === i);
      const prs = await applyCheck(conn, rows, check);
      if (check.state !== CheckState.FAILURE) return [];
      const detail = { sha, checks: [{ id: check.id, name: check.name, url: check.url, summary: check.summary }] };
      if (!prs.length) return [{ ...base, kind: VcsEvent_Kind.CHECK_FAILED, repositoryKey: repoKey, headBranch: str(payload.branches?.[0]?.name), detail }];
      return prs.map((pr) => ({ ...base, kind: VcsEvent_Kind.CHECK_FAILED, repositoryKey: repoKey, pr, detail }));
    }

    case "pull_request_review": {
      const repoKey = githubRepoKey(payload.repository);
      const review = payload.review ?? {};
      const prev = await getStoredPr(repoKey, num(payload.pull_request?.number));
      const pr = mergePr(prev ? prOf(prev) : undefined, githubPr(payload.pull_request, repoKey));
      const state = str(review.state).toLowerCase();
      const reviewer = str(review.user?.login);
      if (reviewer && ["approved", "changes_requested", "dismissed"].includes(state)) pr.reviewers = { ...(pr.reviewers ?? {}), [reviewer]: state };
      if (action === "dismissed" && reviewer) pr.reviewers = { ...(pr.reviewers ?? {}), [reviewer]: "dismissed" };
      pr.reviewState = aggregateReviews(pr.reviewers ?? {}, (payload.pull_request?.requested_reviewers?.length ?? 0) > 0);
      await savePr(pr, { source: ProviderType.GITHUB, connectionId: conn.id });
      if (action !== "submitted" && action !== "dismissed") return [];
      return [
        {
          ...base,
          kind: action === "submitted" ? VcsEvent_Kind.REVIEW_SUBMITTED : VcsEvent_Kind.PR_UPDATED,
          repositoryKey: repoKey,
          pr,
          occurredAt: isoTime(review.submitted_at),
          detail: { state: action === "dismissed" ? "dismissed" : state, reviewer, review_id: str(review.id), url: str(review.html_url), body: str(review.body).slice(0, 4000) },
        },
      ];
    }

    case "pull_request_review_comment": {
      if (action !== "created") return [];
      const repoKey = githubRepoKey(payload.repository);
      const c = payload.comment ?? {};
      const prev = await getStoredPr(repoKey, num(payload.pull_request?.number));
      const pr = mergePr(prev ? prOf(prev) : undefined, githubPr(payload.pull_request, repoKey));
      await savePr(pr, { source: ProviderType.GITHUB, connectionId: conn.id });
      return [
        {
          ...base,
          kind: VcsEvent_Kind.REVIEW_COMMENT,
          repositoryKey: repoKey,
          pr,
          occurredAt: isoTime(c.created_at),
          detail: {
            comment_ids: [`r:${c.id}`],
            in_reply_to: c.in_reply_to_id ? `r:${c.in_reply_to_id}` : "",
            author: str(c.user?.login),
            body: str(c.body).slice(0, 4000),
            path: str(c.path),
            line: num(c.line ?? c.original_line),
            url: str(c.html_url),
          },
        },
      ];
    }

    case "issue_comment": {
      if (action !== "created" || !payload.issue?.pull_request) return [];
      const repoKey = githubRepoKey(payload.repository);
      const issue = payload.issue;
      const c = payload.comment ?? {};
      const { pr } = await knownPr(conn, repoKey, num(issue.number), () => ({
        repositoryKey: repoKey,
        number: num(issue.number),
        title: str(issue.title),
        url: str(issue.pull_request?.html_url || issue.html_url),
        headBranch: "",
        baseBranch: "",
        author: str(issue.user?.login),
        state: issue.state === "closed" ? PullRequest_State.CLOSED : PullRequest_State.OPEN,
        reviewState: "none",
        mergeable: false,
        hasConflicts: false,
        autoMergeEnabled: false,
        draft: false,
        updatedAt: isoTime(issue.updated_at),
      }));
      return [
        {
          ...base,
          kind: VcsEvent_Kind.REVIEW_COMMENT,
          repositoryKey: repoKey,
          pr,
          prNumber: num(issue.number),
          occurredAt: isoTime(c.created_at),
          detail: { comment_ids: [`c:${c.id}`], author: str(c.user?.login), body: str(c.body).slice(0, 4000), url: str(c.html_url) },
        },
      ];
    }

    case "push": {
      const branch = branchOf(payload.ref);
      if (!branch) return [];
      const repoKey = githubRepoKey(payload.repository);
      const after = str(payload.after);
      const heads = (await storedPrs(repoKey, { headBranch: branch })).filter((r) => isOpen(prOf(r)));
      for (const row of heads) {
        if (after && row.headSha !== after && !payload.deleted) await savePr({ ...prOf(row), headSha: after, checks: undefined }, { source: ProviderType.GITHUB, connectionId: conn.id, checks: [] });
      }
      if (!payload.deleted) scheduleConflictRecheck(conn, repoKey, branch);
      const pr = heads[0] ? { ...prOf(heads[0]), headSha: after || prOf(heads[0]).headSha, checks: undefined } : undefined;
      return [
        {
          ...base,
          kind: VcsEvent_Kind.PUSH,
          repositoryKey: repoKey,
          headBranch: branch,
          pr,
          detail: {
            ref: str(payload.ref),
            before: str(payload.before),
            after,
            commits: Array.isArray(payload.commits) ? payload.commits.length : 0,
            forced: !!payload.forced,
            deleted: !!payload.deleted,
            pusher: str(payload.pusher?.name || payload.sender?.login),
          },
        },
      ];
    }

    default:
      return [];
  }
}

/** Stored PRs of a commit; PRs listed in a check payload but unknown so far are fetched or created minimally. */
async function prsForCommit(conn: ConnectionRow, repoKey: string, sha: string, listed: any[], repository: any): Promise<StoredPrRow[]> {
  const out: StoredPrRow[] = [];
  for (const p of listed) {
    const n = num(p.number);
    if (!n) continue;
    const { row } = await knownPr(conn, repoKey, n, () => ({
      repositoryKey: repoKey,
      number: n,
      title: "",
      url: `${str(repository?.html_url)}/pull/${n}`,
      headBranch: str(p.head?.ref),
      baseBranch: str(p.base?.ref),
      author: "",
      state: PullRequest_State.OPEN,
      reviewState: "none",
      mergeable: false,
      hasConflicts: false,
      autoMergeEnabled: false,
      draft: false,
      updatedAt: "",
      headSha: str(p.head?.sha) || sha,
    }));
    if (row) out.push(row);
  }
  if (!out.length && sha) out.push(...(await storedPrs(repoKey, { headSha: sha })));
  return out.filter((r, i) => out.findIndex((x) => x.number === r.number) === i);
}

// --- GitLab --------------------------------------------------------------------------------------

export async function handleGitLabWebhook(headers: Headers, raw: string, connectionId = ""): Promise<WebhookResult> {
  const token = headers.get("x-gitlab-token") ?? "";
  if (!token) return { status: 401, body: { error: "missing X-Gitlab-Token" } };
  const conn = (await allConnections()).find((c) => {
    if (c.type !== ProviderType.GITLAB || (connectionId && c.id !== connectionId)) return false;
    const secret = connSecrets(c).webhookSecret;
    return !!secret && safeEqual(secret, token);
  });
  if (!conn) return { status: 401, body: { error: "invalid token" } };
  let payload: any;
  try {
    payload = JSON.parse(raw);
  } catch {
    return { status: 400, body: { error: "invalid JSON" } };
  }
  const delivery = headers.get("idempotency-key") || headers.get("x-gitlab-event-uuid") || headers.get("x-gitlab-webhook-uuid") || sha256(raw);
  return runDelivery(conn, delivery, () => mapGitLab(conn, payload, delivery));
}

function gitlabMinimalMr(mr: any, repoKey: string): PrData {
  return gitlabMr({ ...mr, web_url: mr.url ?? mr.web_url }, repoKey);
}

export async function mapGitLab(conn: ConnectionRow, payload: any, deliveryId: string): Promise<EventInput[]> {
  const base = { source: ProviderType.GITLAB, connectionId: conn.id, deliveryId };
  const kind = str(payload.object_kind || payload.event_type);
  const repoKey = gitlabRepoKey(payload.project ?? { web_url: payload.repository?.homepage }, conn.baseUrl);
  const projectUrl = str(payload.project?.web_url || payload.repository?.homepage);
  switch (kind) {
    case "merge_request": {
      const attrs = payload.object_attributes ?? {};
      const action = str(attrs.action);
      const prevRow = await getStoredPr(repoKey, num(attrs.iid));
      const prev = prevRow ? prOf(prevRow) : undefined;
      const next = gitlabMinimalMr(attrs, repoKey);
      if (!next.author && action === "open") next.author = str(payload.user?.username);
      const pr = mergePr(prev, next);
      const actor = str(payload.user?.username);
      if (actor && (action === "approved" || action === "approval")) pr.reviewers = { ...(pr.reviewers ?? {}), [actor]: "approved" };
      if (actor && (action === "unapproved" || action === "unapproval") && pr.reviewers) {
        const { [actor]: _, ...rest } = pr.reviewers;
        pr.reviewers = rest;
      }
      if (next.reviewState === "changes_requested") pr.reviewState = "changes_requested";
      else if (pr.reviewers && Object.keys(pr.reviewers).length) pr.reviewState = aggregateReviews(pr.reviewers, false);
      else pr.reviewState = next.reviewState;
      const headMoved = !!attrs.oldrev || (prev?.headSha && pr.headSha && prev.headSha !== pr.headSha);
      if (headMoved) pr.checks = undefined;
      await savePr(pr, { source: ProviderType.GITLAB, connectionId: conn.id, checks: headMoved ? [] : undefined });
      const out: EventInput[] = [];
      const detail = { action, sender: actor };
      if (action === "open" || action === "reopen") out.push({ ...base, kind: VcsEvent_Kind.PR_OPENED, repositoryKey: repoKey, pr, detail });
      else if (action === "merge") out.push({ ...base, kind: VcsEvent_Kind.PR_MERGED, repositoryKey: repoKey, pr, detail });
      else if (action === "close") out.push({ ...base, kind: VcsEvent_Kind.PR_CLOSED, repositoryKey: repoKey, pr, detail });
      else if (["approved", "approval", "unapproved", "unapproval"].includes(action)) {
        out.push({ ...base, kind: VcsEvent_Kind.REVIEW_SUBMITTED, repositoryKey: repoKey, pr, detail: { state: action.startsWith("un") ? "unapproved" : "approved", reviewer: actor } });
      } else out.push({ ...base, kind: VcsEvent_Kind.PR_UPDATED, repositoryKey: repoKey, pr, detail });
      out.push(...conflictEvent(base, prev, pr));
      return out;
    }

    case "pipeline": {
      const attrs = payload.object_attributes ?? {};
      const status = str(attrs.status);
      const sha = str(attrs.sha);
      const checks = (payload.builds ?? []).map((b: any) => gitlabJob({ ...b, web_url: projectUrl ? `${projectUrl}/-/jobs/${b.id}` : "" }, str(attrs.name)));
      let rows: StoredPrRow[] = [];
      if (payload.merge_request?.iid) {
        const { row } = await knownPr(conn, repoKey, num(payload.merge_request.iid), () => gitlabMinimalMr(payload.merge_request, repoKey));
        if (row) rows.push(row);
      }
      if (!rows.length && sha) rows = await storedPrs(repoKey, { headSha: sha });
      if (!rows.length && attrs.ref && !attrs.tag) rows = (await storedPrs(repoKey, { headBranch: str(attrs.ref) })).filter((r) => isOpen(prOf(r)));
      const prs = await applyCheck(conn, rows, undefined, checks);
      if (!["success", "failed", "canceled", "skipped"].includes(status)) return [];
      const detail = {
        sha,
        pipeline_id: str(attrs.id),
        status,
        failed_jobs: checks.filter((c: CheckData) => c.state === CheckState.FAILURE).map((c: CheckData) => ({ id: c.id, name: c.name, url: c.url })),
      };
      if (!prs.length) return [{ ...base, kind: VcsEvent_Kind.CHECKS_COMPLETED, repositoryKey: repoKey, headBranch: str(attrs.ref), detail }];
      return prs.map((pr) => ({ ...base, kind: VcsEvent_Kind.CHECKS_COMPLETED, repositoryKey: repoKey, pr, detail: { ...detail, summary: pr.checks } }));
    }

    case "build": {
      const status = str(payload.build_status);
      const allowFailure = !!payload.build_allow_failure;
      const check: CheckData = {
        id: `job:${payload.build_id}`,
        name: str(payload.build_name),
        state: gitlabJobState(status, allowFailure),
        url: projectUrl ? `${projectUrl}/-/jobs/${payload.build_id}` : "",
        workflow: str(payload.build_stage),
        durationMs: Math.round(num(payload.build_duration) * 1000),
        summary: str(payload.build_failure_reason),
      };
      const sha = str(payload.sha);
      let rows = sha ? await storedPrs(repoKey, { headSha: sha }) : [];
      if (!rows.length && payload.ref && !payload.tag) rows = (await storedPrs(repoKey, { headBranch: str(payload.ref) })).filter((r) => isOpen(prOf(r)));
      const prs = await applyCheck(conn, rows, check);
      if (check.state !== CheckState.FAILURE) return [];
      const detail = { sha, checks: [{ id: check.id, name: check.name, url: check.url, summary: check.summary }], pipeline_id: str(payload.pipeline_id) };
      if (!prs.length) return [{ ...base, kind: VcsEvent_Kind.CHECK_FAILED, repositoryKey: repoKey, headBranch: str(payload.ref), detail }];
      return prs.map((pr) => ({ ...base, kind: VcsEvent_Kind.CHECK_FAILED, repositoryKey: repoKey, pr, detail }));
    }

    case "note": {
      const attrs = payload.object_attributes ?? {};
      if (str(attrs.noteable_type) !== "MergeRequest" || !payload.merge_request?.iid) return [];
      if (attrs.system) return [];
      const { pr } = await knownPr(conn, repoKey, num(payload.merge_request.iid), () => gitlabMinimalMr(payload.merge_request, repoKey));
      const pos = attrs.position ?? {};
      return [
        {
          ...base,
          kind: VcsEvent_Kind.REVIEW_COMMENT,
          repositoryKey: repoKey,
          pr,
          prNumber: num(payload.merge_request.iid),
          headBranch: pr?.headBranch || str(payload.merge_request.source_branch),
          occurredAt: isoTime(attrs.created_at),
          detail: {
            comment_ids: [`${str(attrs.discussion_id)}/${attrs.id}`],
            author: str(payload.user?.username),
            body: str(attrs.note).slice(0, 4000),
            path: str(pos.new_path ?? pos.old_path),
            line: num(pos.new_line ?? pos.old_line),
            url: str(attrs.url),
          },
        },
      ];
    }

    case "push": {
      const branch = branchOf(payload.ref);
      if (!branch) return [];
      const after = str(payload.after ?? payload.checkout_sha);
      const deleted = /^0+$/.test(after);
      const heads = (await storedPrs(repoKey, { headBranch: branch })).filter((r) => isOpen(prOf(r)));
      for (const row of heads) {
        if (after && !deleted && row.headSha !== after) await savePr({ ...prOf(row), headSha: after, checks: undefined }, { source: ProviderType.GITLAB, connectionId: conn.id, checks: [] });
      }
      if (!deleted) scheduleConflictRecheck(conn, repoKey, branch);
      const pr = heads[0] ? { ...prOf(heads[0]), headSha: after, checks: undefined } : undefined;
      return [
        {
          ...base,
          kind: VcsEvent_Kind.PUSH,
          repositoryKey: repoKey,
          headBranch: branch,
          pr,
          detail: { ref: str(payload.ref), before: str(payload.before), after, commits: num(payload.total_commits_count), deleted, pusher: str(payload.user_username) },
        },
      ];
    }

    default:
      return [];
  }
}
