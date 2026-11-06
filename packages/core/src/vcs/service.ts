/**
 * VcsService operations (transport-agnostic): connection health, PR / CI operations with the token rules,
 * session links, auto-fix flags and event visibility.
 *
 * Token choice and access:
 * - With the caller's own token on the connection: every operation, as the user (the code host enforces
 *   repository access; writes are attributed to the user).
 * - Without it: reads use the system client (GitHub installation token, GitLab access token) but only for
 *   admins and for users who own a session on that repository (synced metadata or an explicit link);
 *   otherwise NEEDS_AUTH. Writes on GitHub need the user's token; on GitLab the configured access token
 *   may act (under the same access rule) when the user has none.
 */
import { and, eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { GetUserAuthStatusResponse_State, ProviderType } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal, PublicUser } from "../services/devices.ts";
import { sessionView } from "../services/sessions.ts";
import { getUserAuthStatus, invalidateUserToken, onUserAuthChanged } from "./auth.ts";
import { systemClient, userClient } from "./clients.ts";
import {
  allConnections,
  connectionForHost,
  getConnectionRow,
  installUrl,
  onConnectionChanged,
  providerName,
  recordHealth,
  requireAdmin,
  WEBHOOK_OK_MS,
  type ConnectionRow,
} from "./connections.ts";
import { getStoredPr, linkedSessions, mergePr, prOf, savePr, sessionsOf, storedPrs, type EventRow } from "./events.ts";
import { appJwt, GH_HEADERS, githubApiBase, hasAppCredentials } from "./github-app.ts";
import { updateBadges } from "./pipeline.ts";
import type { CreatePrInput, PrData, RepoRef, VcsProviderClient } from "./types.ts";
import { hostOfBaseUrl, httpJson, httpRequest, isOpen, parseRepoKey } from "./util.ts";

// --- connections ---------------------------------------------------------------------------------

export interface ConnectionView {
  id: string;
  type: number;
  displayName: string;
  baseUrl: string;
  appSlug: string;
  appId: string;
  healthy: boolean;
  lastError: string;
  installUrl: string;
  webhooksOk: boolean;
  lastWebhookAt: string;
}

export function connectionView(r: ConnectionRow): ConnectionView {
  return {
    id: r.id,
    type: r.type,
    displayName: r.displayName,
    baseUrl: r.baseUrl,
    appSlug: r.appSlug,
    appId: r.type === ProviderType.GITLAB ? r.appId || r.clientId : r.appId,
    healthy: r.healthy,
    lastError: r.lastError,
    installUrl: installUrl(r),
    webhooksOk: !!r.lastWebhookAt && Date.now() - r.lastWebhookAt.getTime() < WEBHOOK_OK_MS,
    lastWebhookAt: r.lastWebhookAt?.toISOString() ?? "",
  };
}

const HEALTH_TTL_MS = 60_000;

/** Test API call: GitHub GET /app with the app JWT; GitLab GET /user with the access token, else OIDC metadata. */
export async function checkHealth(row: ConnectionRow): Promise<ConnectionRow> {
  let healthy = false;
  let error = "";
  try {
    if (row.type === ProviderType.GITHUB) {
      if (!hasAppCredentials(row)) throw new ApiError("failed_precondition", "App id and private key are not configured");
      await httpJson(row, `${githubApiBase(row.baseUrl)}/app`, "GET /app", { headers: GH_HEADERS, authorization: `Bearer ${appJwt(row)}`, timeoutMs: 10_000 });
    } else {
      const client = systemClient(row);
      if (client) await client.whoami();
      else {
        const res = await httpRequest(row, `${row.baseUrl}/.well-known/openid-configuration`, { timeoutMs: 10_000 });
        await res.body?.cancel();
        if (!res.ok) throw new ApiError("upstream", `GitLab answered ${res.status}`);
      }
      if (!row.clientId && !client) throw new ApiError("failed_precondition", "Neither an OAuth application nor an access token is configured");
    }
    healthy = true;
  } catch (err) {
    error = err instanceof Error ? err.message : String(err);
  }
  await recordHealth(row.id, healthy, error);
  return { ...row, healthy, lastError: error.slice(0, 500), lastCheckedAt: new Date() };
}

export async function listConnections(p: Principal): Promise<ConnectionView[]> {
  requireAdmin(p);
  const rows = await allConnections();
  const checked = await Promise.all(
    rows.map((r) => (!r.lastCheckedAt || Date.now() - r.lastCheckedAt.getTime() > HEALTH_TTL_MS ? checkHealth(r) : Promise.resolve(r))),
  );
  return checked.map(connectionView);
}

export interface UserConnectionView {
  connectionId: string;
  type: number;
  displayName: string;
  baseUrl: string;
  host: string;
  installUrl: string;
  state: number;
  login: string;
  canWrite: boolean;
}

/** The connections any signed-in user can use, with where that user stands. Health and secrets stay with the admins. */
export async function listUserConnections(p: Principal): Promise<UserConnectionView[]> {
  const rows = await allConnections();
  return Promise.all(
    rows.map(async (r) => {
      const status = await getUserAuthStatus(p, r.id, "");
      const own = status.state === GetUserAuthStatusResponse_State.CONNECTED;
      return {
        connectionId: r.id,
        type: r.type,
        displayName: r.displayName,
        baseUrl: r.baseUrl,
        host: hostOfBaseUrl(r.baseUrl),
        installUrl: installUrl(r),
        state: status.state,
        login: status.login,
        canWrite: own || (r.type === ProviderType.GITLAB && !!systemClient(r)),
      };
    }),
  );
}

// --- clients for a repository ----------------------------------------------------------------------

export async function resolveRepo(repositoryKey: string): Promise<{ repo: RepoRef; conn: ConnectionRow }> {
  const repo = parseRepoKey(repositoryKey);
  const conn = await connectionForHost(repo.host);
  if (!conn) {
    throw new ApiError("failed_precondition", `No server connection covers ${repo.host}; use a device provider (gh / glab) on the client`);
  }
  return { repo, conn };
}

/** Whether the user owns a session on the repository (synced metadata or an explicit link). */
export async function ownsSessionOn(userId: string, repositoryKey: string): Promise<boolean> {
  await ensureDb();
  const s = await getDb().query.syncSession.findFirst({ where: and(eq(schema.syncSession.userId, userId), eq(schema.syncSession.repo, repositoryKey)) });
  if (s) return true;
  const l = await getDb().query.vcsSessionLink.findFirst({
    where: and(eq(schema.vcsSessionLink.userId, userId), eq(schema.vcsSessionLink.repositoryKey, repositoryKey)),
  });
  return !!l;
}

interface Bound {
  repo: RepoRef;
  conn: ConnectionRow;
  client: VcsProviderClient;
  asUser: boolean;
}

async function bind(p: Principal, repositoryKey: string, mode: "read" | "write"): Promise<Bound> {
  const { repo, conn } = await resolveRepo(repositoryKey);
  const own = await userClient(conn, p.user.id);
  if (own) return { repo, conn, client: own, asUser: true };
  const name = providerName(conn.type);
  if (mode === "write" && conn.type === ProviderType.GITHUB) {
    throw new ApiError("needs_auth", `Connect your ${name} account (StartUserAuth) to act as yourself`);
  }
  const sys = systemClient(conn);
  if (!sys) throw new ApiError("needs_auth", `Connect your ${name} account (StartUserAuth) first`);
  if (p.user.role !== "admin" && !(await ownsSessionOn(p.user.id, repo.key))) {
    throw new ApiError("needs_auth", `Connect your ${name} account (StartUserAuth) to access ${repo.key}`);
  }
  return { repo, conn, client: sys, asUser: false };
}

/** Runs an operation; a rejected user token is dropped so the status shows NOT_CONNECTED / ERROR. */
async function run<T>(p: Principal, b: Bound, fn: (c: VcsProviderClient) => Promise<T>): Promise<T> {
  try {
    return await fn(b.client);
  } catch (err) {
    if (b.asUser && err instanceof ApiError && err.type === "needs_auth") await invalidateUserToken(p.user.id, b.conn.id, err.message);
    throw err;
  }
}

async function cache(b: Bound, pr: PrData): Promise<PrData> {
  const prev = await getStoredPr(pr.repositoryKey, pr.number);
  await savePr(mergePr(prev ? prOf(prev) : undefined, pr), { source: b.conn.type, connectionId: b.conn.id });
  return pr;
}

// --- repositories and pull requests ----------------------------------------------------------------

export async function listRepositories(p: Principal, connectionId: string, query: string, pageToken: string) {
  const conn = await getConnectionRow(connectionId);
  const own = await userClient(conn, p.user.id);
  if (own) return own.listRepositories(query, pageToken);
  if (p.user.role !== "admin") throw new ApiError("needs_auth", `Connect your ${providerName(conn.type)} account (StartUserAuth) to list repositories`);
  const sys = systemClient(conn);
  if (!sys) throw new ApiError("failed_precondition", "The connection has no app credentials or access token");
  return sys.listRepositories(query, pageToken);
}

export async function listPullRequests(p: Principal, repositoryKey: string, headBranch: string, includeClosed: boolean): Promise<PrData[]> {
  const b = await bind(p, repositoryKey, "read");
  const prs = await run(p, b, (c) => c.listPullRequests(b.repo, { headBranch, includeClosed }));
  for (const pr of prs) await cache(b, pr);
  return prs;
}

export async function getPullRequest(p: Principal, repositoryKey: string, number: number): Promise<PrData> {
  if (!number) throw new ApiError("invalid_request", "number is required");
  const b = await bind(p, repositoryKey, "read");
  return cache(b, await run(p, b, (c) => c.getPullRequest(b.repo, number)));
}

async function ownedSession(p: Principal, sessionId: string) {
  await ensureDb();
  const s = await getDb().query.syncSession.findFirst({ where: eq(schema.syncSession.id, sessionId) });
  if (!s || s.userId !== p.user.id) throw new ApiError("not_found", `Session ${sessionId} not found`);
  return s;
}

export async function createPullRequest(p: Principal, repositoryKey: string, input: CreatePrInput & { sessionId: string }): Promise<PrData> {
  if (!input.headBranch || !input.title) throw new ApiError("invalid_request", "head_branch and title are required");
  if (input.sessionId) await ownedSession(p, input.sessionId);
  const b = await bind(p, repositoryKey, "write");
  if (!input.baseBranch) throw new ApiError("invalid_request", "base_branch is required");
  const pr = await cache(b, await run(p, b, (c) => c.createPullRequest(b.repo, input)));
  if (input.sessionId) await upsertLink(p.user.id, input.sessionId, { repositoryKey: b.repo.key, branch: input.headBranch, prNumber: pr.number });
  await updateBadges(await linkedSessions(b.repo.key, pr.headBranch, pr.number), pr);
  return pr;
}

export async function mergePullRequest(p: Principal, repositoryKey: string, number: number, method: string): Promise<PrData> {
  if (!number) throw new ApiError("invalid_request", "number is required");
  const b = await bind(p, repositoryKey, "write");
  const pr = await cache(b, await run(p, b, (c) => c.mergePullRequest(b.repo, number, method)));
  await updateBadges(await linkedSessions(b.repo.key, pr.headBranch, pr.number), pr);
  return pr;
}

export async function setAutoMerge(p: Principal, repositoryKey: string, number: number, enabled: boolean, method: string): Promise<void> {
  if (!number) throw new ApiError("invalid_request", "number is required");
  const b = await bind(p, repositoryKey, "write");
  await run(p, b, (c) => c.setAutoMerge(b.repo, number, enabled, method));
  const row = await getStoredPr(b.repo.key, number);
  if (row) await savePr({ ...prOf(row), autoMergeEnabled: enabled }, { source: b.conn.type, connectionId: b.conn.id });
}

export async function listReviewComments(p: Principal, repositoryKey: string, number: number, unresolvedOnly: boolean) {
  if (!number) throw new ApiError("invalid_request", "number is required");
  const b = await bind(p, repositoryKey, "read");
  return run(p, b, (c) => c.listReviewComments(b.repo, number, unresolvedOnly));
}

export async function postComment(p: Principal, repositoryKey: string, number: number, body: string, inReplyTo: string) {
  if (!number) throw new ApiError("invalid_request", "number is required");
  const b = await bind(p, repositoryKey, "write");
  return run(p, b, (c) => c.postComment(b.repo, number, body, inReplyTo));
}

export async function listChecks(p: Principal, repositoryKey: string, number: number, sha: string) {
  if (!number && !sha) throw new ApiError("invalid_request", "number or sha is required");
  const b = await bind(p, repositoryKey, "read");
  return run(p, b, (c) => c.listChecks(b.repo, { number: number || undefined, sha: sha || undefined }));
}

export async function getCheckLog(p: Principal, repositoryKey: string, checkId: string, maxBytes: number) {
  if (!checkId) throw new ApiError("invalid_request", "check_id is required");
  if (maxBytes < 0) throw new ApiError("invalid_request", "max_bytes must be >= 0");
  const b = await bind(p, repositoryKey, "read");
  return run(p, b, (c) => c.getCheckLog(b.repo, checkId, maxBytes));
}

// --- sessions --------------------------------------------------------------------------------------

async function upsertLink(
  userId: string,
  sessionId: string,
  v: Partial<{ repositoryKey: string; branch: string; prNumber: number; autoFixCi: boolean; autoFixReviews: boolean; autoFixConflicts: boolean }>,
): Promise<void> {
  const now = new Date();
  await getDb()
    .insert(schema.vcsSessionLink)
    .values({ sessionId, userId, ...v, updatedAt: now })
    .onConflictDoUpdate({ target: schema.vcsSessionLink.sessionId, set: { ...v, updatedAt: now } });
}

export async function linkSession(p: Principal, req: { sessionId: string; repositoryKey: string; branch: string; number: number }): Promise<PrData | undefined> {
  if (!req.sessionId) throw new ApiError("invalid_request", "session_id is required");
  const s = await ownedSession(p, req.sessionId);
  const info = sessionView(s).info;
  const repo = parseRepoKey(req.repositoryKey || info.repo);
  const branch = req.branch || info.gitBranch;
  if (!branch && !req.number) throw new ApiError("invalid_request", "branch (or number) is required");
  await upsertLink(p.user.id, s.id, { repositoryKey: repo.key, branch, prNumber: req.number || 0 });

  let pr: PrData | undefined;
  // Ask the code host when the server can; otherwise (device-provider repos, no token) use the stored state.
  try {
    const b = await bind(p, repo.key, "read");
    if (req.number) pr = await cache(b, await run(p, b, (c) => c.getPullRequest(b.repo, req.number)));
    else {
      const found = await run(p, b, (c) => c.listPullRequests(b.repo, { headBranch: branch, includeClosed: true }));
      pr = found.find(isOpen) ?? found[0];
      if (pr) await cache(b, pr);
    }
  } catch (err) {
    if (!(err instanceof ApiError) || !["needs_auth", "failed_precondition", "not_found"].includes(err.type)) throw err;
    if (req.number) {
      const row = await getStoredPr(repo.key, req.number);
      pr = row ? prOf(row) : undefined;
    } else {
      const rows = (await storedPrs(repo.key, { headBranch: branch })).map(prOf);
      pr = rows.find(isOpen) ?? rows.sort((a, b) => (a.updatedAt < b.updatedAt ? 1 : -1))[0];
    }
  }
  if (pr) {
    await upsertLink(p.user.id, s.id, { prNumber: pr.number, branch: branch || pr.headBranch });
    await updateBadges([{ sessionId: s.id, userId: p.user.id }], pr);
  }
  return pr;
}

export async function setAutoFix(p: Principal, req: { sessionId: string; ciFailures: boolean; reviewComments: boolean; conflicts: boolean }): Promise<void> {
  if (!req.sessionId) throw new ApiError("invalid_request", "session_id is required");
  const s = await ownedSession(p, req.sessionId);
  await upsertLink(p.user.id, s.id, { autoFixCi: req.ciFailures, autoFixReviews: req.reviewComments, autoFixConflicts: req.conflicts });
}

// --- event visibility ------------------------------------------------------------------------------

const ACCESS_TTL_MS = 5 * 60_000;
const DENY_TTL_MS = 60_000;
const access = new Map<string, { ok: boolean; until: number }>();

onUserAuthChanged((userId) => {
  for (const k of [...access.keys()]) if (k.startsWith(`${userId}|`)) access.delete(k);
});
onConnectionChanged(() => access.clear());

/** Whether the user can see the repository through their own token (cached). */
export async function canAccessRepo(user: PublicUser, repositoryKey: string): Promise<boolean> {
  const k = `${user.id}|${repositoryKey}`;
  const hit = access.get(k);
  if (hit && hit.until > Date.now()) return hit.ok;
  let ok = false;
  try {
    const repo = parseRepoKey(repositoryKey);
    const conn = await connectionForHost(repo.host);
    const client = conn ? await userClient(conn, user.id) : undefined;
    ok = client ? await client.canAccess(repo) : false;
  } catch {
    ok = false;
  }
  access.set(k, { ok, until: Date.now() + (ok ? ACCESS_TTL_MS : DENY_TTL_MS) });
  return ok;
}

/** Admins see every event; users see events of their sessions, their own device reports, and repos they can access. */
export async function eventVisibleTo(user: PublicUser, row: EventRow): Promise<boolean> {
  if (user.role === "admin") return true;
  if (row.actorUserId === user.id) return true;
  if (sessionsOf(row).some((s) => s.userId === user.id)) return true;
  return canAccessRepo(user, row.repositoryKey);
}
