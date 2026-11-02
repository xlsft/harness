/** Connect implementation of harness.vcs.v1.VcsService: proto <-> src/vcs/*. */
import type { DescMessage, MessageInitShape } from "@bufbuild/protobuf";
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import {
  VcsService,
  type Check as PbCheck,
  type CheckSchema,
  type ChecksSummarySchema,
  type ConnectionSchema,
  type PullRequest as PbPullRequest,
  type PullRequestSchema,
  type RepositorySchema,
  type ReviewCommentSchema,
  type VcsEventSchema,
  type WatchEventsResponseSchema,
} from "../gen/harness/vcs/v1/vcs_pb.ts";
import { AsyncQueue } from "../lib/async.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal } from "../services/devices.ts";
import { getUserAuthStatus, revokeUserAuth, startUserAuth } from "../vcs/auth.ts";
import { deleteConnection, putConnection } from "../vcs/connections.ts";
import { reportDeviceProviders, reportDeviceSnapshot } from "../vcs/devices.ts";
import { eventId, eventsAfter, headSeq, parseEventId, sessionsOf, subscribeEvents, type EventRow } from "../vcs/events.ts";
import { completeGitHubAppManifest, getGitHubAppManifest } from "../vcs/manifest.ts";
import {
  connectionView,
  createPullRequest,
  eventVisibleTo,
  getCheckLog,
  getPullRequest,
  linkSession,
  listChecks,
  listConnections,
  listPullRequests,
  listRepositories,
  listReviewComments,
  mergePullRequest,
  postComment,
  setAutoFix,
  setAutoMerge,
  type ConnectionView,
} from "../vcs/service.ts";
import type { CheckData, ChecksSummaryData, CommentData, PrData, RepoData } from "../vcs/types.ts";
import { parseRepoKey } from "../vcs/util.ts";
import { requirePrincipal } from "./context.ts";
import { guard, toConnectError } from "./errors.ts";

type Init<T extends DescMessage> = MessageInitShape<T>;

export function connectionToProto(c: ConnectionView): Init<typeof ConnectionSchema> {
  return { ...c };
}

function summaryToProto(s: ChecksSummaryData | undefined): Init<typeof ChecksSummarySchema> | undefined {
  return s ? { state: s.state, total: s.total, passed: s.passed, failed: s.failed, running: s.running } : undefined;
}

export function prToProto(p: PrData): Init<typeof PullRequestSchema> {
  return {
    repositoryKey: p.repositoryKey,
    number: BigInt(p.number),
    title: p.title,
    url: p.url,
    headBranch: p.headBranch,
    baseBranch: p.baseBranch,
    author: p.author,
    state: p.state,
    reviewState: p.reviewState,
    checks: summaryToProto(p.checks),
    mergeable: p.mergeable,
    hasConflicts: p.hasConflicts,
    autoMergeEnabled: p.autoMergeEnabled,
    draft: p.draft,
    updatedAt: p.updatedAt,
  };
}

function prFromProto(p: PbPullRequest): PrData {
  return {
    repositoryKey: p.repositoryKey,
    number: Number(p.number),
    title: p.title,
    url: p.url,
    headBranch: p.headBranch,
    baseBranch: p.baseBranch,
    author: p.author,
    state: p.state,
    reviewState: p.reviewState || "none",
    checks: p.checks ? { state: p.checks.state, total: p.checks.total, passed: p.checks.passed, failed: p.checks.failed, running: p.checks.running } : undefined,
    mergeable: p.mergeable,
    hasConflicts: p.hasConflicts,
    autoMergeEnabled: p.autoMergeEnabled,
    draft: p.draft,
    updatedAt: p.updatedAt,
  };
}

function checkToProto(c: CheckData): Init<typeof CheckSchema> {
  return { ...c, durationMs: BigInt(Math.max(0, Math.round(c.durationMs))) };
}

function checkFromProto(c: PbCheck): CheckData {
  return { id: c.id, name: c.name, state: c.state, url: c.url, workflow: c.workflow, durationMs: Number(c.durationMs), summary: c.summary };
}

function commentToProto(c: CommentData): Init<typeof ReviewCommentSchema> {
  return { ...c };
}

function repoToProto(r: RepoData): Init<typeof RepositorySchema> {
  return { ...r };
}

function eventToProto(row: EventRow, p: Principal): Init<typeof VcsEventSchema> {
  const sessions = sessionsOf(row).filter((s) => p.user.role === "admin" || s.userId === p.user.id);
  return {
    id: eventId(row.seq),
    kind: row.kind,
    repositoryKey: row.repositoryKey,
    pullRequest: row.pullRequest ? prToProto(row.pullRequest as PrData) : undefined,
    sessionIds: sessions.map((s) => s.sessionId),
    occurredAt: row.occurredAt.toISOString(),
    detailJson: JSON.stringify(row.detail ?? {}),
    source: row.source,
  };
}

/** Heartbeat interval of WatchEvents (HARNESS_VCS_HEARTBEAT_MS, default and maximum 20 s). */
function heartbeatMs(): number {
  const n = Number(process.env.HARNESS_VCS_HEARTBEAT_MS ?? "");
  return Number.isFinite(n) && n > 0 ? Math.min(n, 20_000) : 20_000;
}

async function* watchEvents(
  p: Principal,
  req: { repositoryKeys: string[]; sessionIds: string[]; afterId: string },
  signal: AbortSignal,
): AsyncGenerator<Init<typeof WatchEventsResponseSchema>> {
  const keys = new Set(req.repositoryKeys.map((k) => parseRepoKey(k).key));
  const sessions = new Set(req.sessionIds);
  let cursor = 0;
  if (req.afterId) {
    cursor = parseEventId(req.afterId);
    if (!Number.isFinite(cursor)) throw new ApiError("invalid_request", "after_id must be an event id (vev_...)");
  }
  const matches = (row: EventRow) =>
    (!keys.size && !sessions.size) || keys.has(row.repositoryKey) || sessionsOf(row).some((s) => sessions.has(s.sessionId));
  const queue = new AsyncQueue<EventRow>();
  // Subscribe before the catch-up so nothing stored in between is missed; duplicates are skipped by seq.
  const unsubscribe = subscribeEvents((row) => queue.push(row));
  try {
    if (!req.afterId) cursor = await headSeq();
    for (;;) {
      const page = await eventsAfter(cursor, 500);
      for (const row of page) {
        cursor = row.seq;
        if (matches(row) && (await eventVisibleTo(p.user, row))) yield { event: { case: "vcsEvent", value: eventToProto(row, p) } };
      }
      if (page.length < 500) break;
    }
    yield { event: { case: "caughtUp", value: {} } };
    const interval = heartbeatMs();
    let lastSent = Date.now();
    while (!signal.aborted) {
      const row = await queue.next(Math.max(0, interval - (Date.now() - lastSent)), signal);
      if (signal.aborted) return;
      if (!row) {
        yield { event: { case: "heartbeat", value: {} } };
        lastSent = Date.now();
        continue;
      }
      if (row.seq <= cursor) continue;
      cursor = row.seq;
      if (!matches(row) || !(await eventVisibleTo(p.user, row))) continue;
      yield { event: { case: "vcsEvent", value: eventToProto(row, p) } };
      lastSent = Date.now();
    }
  } finally {
    unsubscribe();
  }
}

export function registerVcsService(router: ConnectRouter): void {
  router.service(VcsService, {
    // --- connections (admin) ---
    listConnections: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { connections: (await listConnections(p)).map(connectionToProto) };
    }),

    getGitHubAppManifest: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return getGitHubAppManifest(p, { baseUrl: req.baseUrl, organization: req.organization, appName: req.appName });
    }),

    completeGitHubAppManifest: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      if (p.user.role !== "admin") throw new ApiError("permission", "Only admins can manage code host connections");
      const row = await completeGitHubAppManifest(req.code, req.state, p.user.id);
      return { connection: connectionToProto(connectionView(row)) };
    }),

    putConnection: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const row = await putConnection(p, {
        id: req.id,
        type: req.type,
        displayName: req.displayName,
        baseUrl: req.baseUrl,
        appId: req.appId,
        privateKeyPem: req.privateKeyPem,
        clientId: req.clientId,
        clientSecret: req.clientSecret,
        webhookSecret: req.webhookSecret,
        accessToken: req.accessToken,
        allowPrivateNetwork: req.allowPrivateNetwork,
      });
      return { connection: connectionToProto(connectionView(row)) };
    }),

    deleteConnection: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await deleteConnection(p, req.id);
      return {};
    }),

    // --- per-user authorization ---
    startUserAuth: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return startUserAuth(p, req.connectionId);
    }),

    getUserAuthStatus: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return getUserAuthStatus(p, req.connectionId, req.flowId);
    }),

    revokeUserAuth: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await revokeUserAuth(p, req.connectionId);
      return {};
    }),

    // --- repositories and pull requests ---
    listRepositories: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await listRepositories(p, req.connectionId, req.query, req.pageToken);
      return { repositories: r.repositories.map(repoToProto), nextPageToken: r.nextPageToken };
    }),

    listPullRequests: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { pullRequests: (await listPullRequests(p, req.repositoryKey, req.headBranch, req.includeClosed)).map(prToProto) };
    }),

    getPullRequest: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { pullRequest: prToProto(await getPullRequest(p, req.repositoryKey, Number(req.number))) };
    }),

    createPullRequest: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const pr = await createPullRequest(p, req.repositoryKey, {
        headBranch: req.headBranch,
        baseBranch: req.baseBranch,
        title: req.title,
        body: req.body,
        draft: req.draft,
        sessionId: req.sessionId,
      });
      return { pullRequest: prToProto(pr) };
    }),

    mergePullRequest: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { pullRequest: prToProto(await mergePullRequest(p, req.repositoryKey, Number(req.number), req.method)) };
    }),

    setAutoMerge: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await setAutoMerge(p, req.repositoryKey, Number(req.number), req.enabled, req.method);
      return {};
    }),

    listReviewComments: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { comments: (await listReviewComments(p, req.repositoryKey, Number(req.number), req.unresolvedOnly)).map(commentToProto) };
    }),

    postComment: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { comment: commentToProto(await postComment(p, req.repositoryKey, Number(req.number), req.body, req.inReplyTo)) };
    }),

    // --- CI ---
    listChecks: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await listChecks(p, req.repositoryKey, Number(req.number), req.sha);
      return { checks: r.checks.map(checkToProto), summary: summaryToProto(r.summary) };
    }),

    getCheckLog: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return getCheckLog(p, req.repositoryKey, req.checkId, req.maxBytes);
    }),

    // --- sessions ---
    linkSession: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const pr = await linkSession(p, { sessionId: req.sessionId, repositoryKey: req.repositoryKey, branch: req.branch, number: Number(req.number) });
      return { pullRequest: pr ? prToProto(pr) : undefined };
    }),

    setAutoFix: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await setAutoFix(p, { sessionId: req.sessionId, ciFailures: req.ciFailures, reviewComments: req.reviewComments, conflicts: req.conflicts });
      return {};
    }),

    async *watchEvents(req, ctx: HandlerContext): AsyncGenerator<Init<typeof WatchEventsResponseSchema>> {
      try {
        const p = await requirePrincipal(ctx);
        yield* watchEvents(p, req, ctx.signal);
      } catch (err) {
        throw toConnectError(err);
      }
    },

    // --- device providers ---
    reportDeviceProviders: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await reportDeviceProviders(
        p,
        req.providers.map((x) => ({ type: x.type, version: x.version, hosts: x.hosts.map((h) => ({ host: h.host, login: h.login })) })),
      );
      return {};
    }),

    reportDeviceSnapshot: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const n = await reportDeviceSnapshot(p, {
        provider: req.provider,
        pullRequests: req.pullRequests.map(prFromProto),
        checks: Object.fromEntries(Object.entries(req.checks).map(([k, v]) => [k, v.checks.map(checkFromProto)])),
        observedAt: req.observedAt,
      });
      return { eventsEmitted: n };
    }),
  });
}
