/**
 * One-click GitHub App registration (GitHub App manifest flow), github.com or GHES, user or organization.
 *
 * GetGitHubAppManifest builds the manifest (permissions, events, webhook / callback / setup URLs) and a
 * state; the admin's browser posts it to GitHub, which redirects to /api/vcs/github/manifest/callback with
 * a one-time code. The callback (or CompleteGitHubAppManifest) exchanges it via
 * POST /app-manifests/{code}/conversions and stores the app id, slug, private key, webhook secret and
 * OAuth client credentials as a connection. Completing the same state twice returns the same connection.
 */
import { and, eq, lt } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { ProviderType } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { newId, newToken, sha256 } from "../lib/ids.ts";
import { assertResolvesPublic, assertUrlAllowed } from "../lib/ssrf.ts";
import type { Principal } from "../services/devices.ts";
import { callbackUrl } from "./auth.ts";
import { getConnectionRow, insertGitHubAppConnection, normalizeBaseUrl, requireAdmin, webhookUrl, type ConnectionRow } from "./connections.ts";
import { githubApiBase } from "./github-app.ts";
import { httpJson, num, str } from "./util.ts";

export const MANIFEST_TTL_MS = 60 * 60_000;

/** Permissions the server needs (merging and auto-merge need contents: write). */
export const APP_PERMISSIONS = {
  checks: "read",
  statuses: "read",
  actions: "read",
  pull_requests: "write",
  issues: "write",
  contents: "write",
  metadata: "read",
} as const;

export const APP_EVENTS = [
  "check_run",
  "check_suite",
  "status",
  "pull_request",
  "pull_request_review",
  "pull_request_review_comment",
  "issue_comment",
  "push",
] as const;

export function manifestCallbackUrl(): string {
  return `${config().publicUrl}/api/vcs/github/manifest/callback`;
}

export function setupUrl(): string {
  return `${config().publicUrl}/api/vcs/github/setup`;
}

/** Whether a base URL needs the private-network exemption (its host is private or does not resolve publicly). */
async function isPrivate(baseUrl: string): Promise<boolean> {
  try {
    assertUrlAllowed(baseUrl, false);
    await assertResolvesPublic(baseUrl, false);
    return false;
  } catch (err) {
    if (err instanceof ApiError && err.type === "permission") return true;
    throw err;
  }
}

export async function getGitHubAppManifest(p: Principal, req: { baseUrl: string; organization: string; appName: string }) {
  requireAdmin(p);
  const baseUrl = normalizeBaseUrl(req.baseUrl, ProviderType.GITHUB);
  const org = req.organization.trim();
  if (org && !/^[A-Za-z0-9][A-Za-z0-9-]{0,38}$/.test(org)) throw new ApiError("invalid_request", "organization must be a GitHub login");
  const name = (req.appName.trim() || "harness").slice(0, 34);
  // The admin typed this URL: a GHES on a private network gets the exemption (the request has no flag).
  const allowPrivateNetwork = await isPrivate(baseUrl);
  const publicUrl = config().publicUrl;
  const manifest = {
    name,
    url: publicUrl,
    hook_attributes: { url: webhookUrl(ProviderType.GITHUB), active: true },
    redirect_url: manifestCallbackUrl(),
    callback_urls: [callbackUrl()],
    setup_url: setupUrl(),
    setup_on_update: true,
    request_oauth_on_install: false,
    public: false,
    default_permissions: APP_PERMISSIONS,
    default_events: APP_EVENTS,
  };
  const state = newToken("vgm");
  const now = Date.now();
  await ensureDb();
  const db = getDb();
  await db.delete(schema.vcsManifestFlow).where(lt(schema.vcsManifestFlow.expiresAt, new Date(now - 86_400_000)));
  await db.insert(schema.vcsManifestFlow).values({
    id: newId("vmf"),
    userId: p.user.id,
    stateHash: sha256(state),
    baseUrl,
    organization: org,
    appName: name,
    allowPrivateNetwork,
    status: "pending",
    expiresAt: new Date(now + MANIFEST_TTL_MS),
    createdAt: new Date(now),
  });
  const post = new URL(org ? `${baseUrl}/organizations/${org}/settings/apps/new` : `${baseUrl}/settings/apps/new`);
  post.searchParams.set("state", state);
  return { manifestJson: JSON.stringify(manifest), postUrl: post.href, state };
}

/**
 * Exchanges the manifest code. `userId` (from the RPC) must be the admin who started the flow; the browser
 * callback passes undefined and is authorized by the state alone.
 */
export async function completeGitHubAppManifest(code: string, state: string, userId?: string): Promise<ConnectionRow> {
  if (!code || !state) throw new ApiError("invalid_request", "code and state are required");
  await ensureDb();
  const db = getDb();
  const flow = await db.query.vcsManifestFlow.findFirst({ where: eq(schema.vcsManifestFlow.stateHash, sha256(state)) });
  if (!flow || (userId && flow.userId !== userId)) throw new ApiError("not_found", "Unknown manifest flow");
  if (flow.status === "completed" && flow.connectionId) return getConnectionRow(flow.connectionId);
  if (flow.status !== "pending") throw new ApiError("failed_precondition", `The manifest flow failed: ${flow.error}`);
  if (flow.expiresAt.getTime() < Date.now()) throw new ApiError("failed_precondition", "The manifest flow expired; start again");
  const target = { allowPrivateNetwork: flow.allowPrivateNetwork };
  let data: any;
  try {
    ({ data } = await httpJson<any>(target, `${githubApiBase(flow.baseUrl)}/app-manifests/${encodeURIComponent(code)}/conversions`, "GitHub App manifest conversion", {
      method: "POST",
      headers: { accept: "application/vnd.github+json" },
    }));
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    await db.update(schema.vcsManifestFlow).set({ status: "failed", error: msg.slice(0, 500) }).where(eq(schema.vcsManifestFlow.id, flow.id));
    throw err;
  }
  if (!data?.id || !data.pem) throw new ApiError("upstream", "The manifest conversion returned no app credentials");
  // Claim the flow first so concurrent completions cannot create two connections.
  const claimed = await db
    .update(schema.vcsManifestFlow)
    .set({ status: "completing" })
    .where(and(eq(schema.vcsManifestFlow.id, flow.id), eq(schema.vcsManifestFlow.status, "pending")))
    .returning({ id: schema.vcsManifestFlow.id });
  if (!claimed.length) throw new ApiError("conflict", "The manifest flow is being completed");
  try {
    const row = await insertGitHubAppConnection(flow.userId, {
      baseUrl: flow.baseUrl,
      appId: String(num(data.id)),
      slug: str(data.slug),
      name: str(data.name) || flow.appName,
      clientId: str(data.client_id),
      secrets: { privateKeyPem: str(data.pem), webhookSecret: str(data.webhook_secret), clientSecret: str(data.client_secret) },
      allowPrivateNetwork: flow.allowPrivateNetwork,
    });
    await db.update(schema.vcsManifestFlow).set({ status: "completed", connectionId: row.id }).where(eq(schema.vcsManifestFlow.id, flow.id));
    return row;
  } catch (err) {
    await db
      .update(schema.vcsManifestFlow)
      .set({ status: "failed", error: (err instanceof Error ? err.message : String(err)).slice(0, 500) })
      .where(eq(schema.vcsManifestFlow.id, flow.id));
    throw err;
  }
}
