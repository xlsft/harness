/**
 * Per-user authorization on a code host (StartUserAuth / GetUserAuthStatus / RevokeUserAuth).
 *
 * - GitHub: user-to-server OAuth of the connection's GitHub App (client id / secret). When the app has
 *   expiring user tokens enabled the token response carries a refresh token, used before expiry.
 * - GitLab: an OAuth application (gitlab.com or self-managed), authorization code + PKCE (S256), scope "api".
 * - The provider redirects to <HARNESS_PUBLIC_URL>/api/vcs/oauth/callback; the flow is found by `state`.
 * - Tokens are encrypted per (user, connection). All requests go through the SSRF guard.
 */
import { createHash, randomBytes } from "node:crypto";
import { and, eq, lt } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { GetUserAuthStatusResponse_State, ProviderType } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { Mutex } from "../lib/async.ts";
import { ApiError } from "../lib/errors.ts";
import { newId, newToken, sha256 } from "../lib/ids.ts";
import type { Principal } from "../services/devices.ts";
import { connSecrets, decryptVcs, encryptVcs, getConnectionRow, providerName, type ConnectionRow } from "./connections.ts";
import { githubApiBase } from "./github-app.ts";
import { GitHubClient } from "./github.ts";
import { GitLabClient } from "./gitlab.ts";
import { httpJson, httpRequest } from "./util.ts";

export const FLOW_TTL_MS = 10 * 60_000;
const REFRESH_MARGIN_MS = 5 * 60_000;

interface Tokens {
  access_token: string;
  refresh_token?: string;
  expires_in?: number;
  refresh_token_expires_in?: number;
  scope?: string;
  token_type?: string;
}

interface FlowData {
  codeVerifier: string;
  redirectUri: string;
}

const flowAad = (id: string) => `${id}:vcs-flow`;
const tokenAad = (userId: string, connectionId: string) => `${userId}:${connectionId}:tokens`;

export function callbackUrl(): string {
  return `${config().publicUrl}/api/vcs/oauth/callback`;
}

function oauthClient(row: ConnectionRow): { clientId: string; clientSecret: string } {
  const clientSecret = connSecrets(row).clientSecret ?? "";
  if (!row.clientId) throw new ApiError("failed_precondition", `The ${providerName(row.type)} connection has no OAuth client id configured`);
  if (row.type === ProviderType.GITHUB && !clientSecret) throw new ApiError("failed_precondition", "The GitHub connection has no client secret configured");
  return { clientId: row.clientId, clientSecret };
}

function tokenEndpoint(row: ConnectionRow): string {
  return row.type === ProviderType.GITLAB ? `${row.baseUrl}/oauth/token` : `${row.baseUrl}/login/oauth/access_token`;
}

async function postToken(row: ConnectionRow, params: Record<string, string>): Promise<Tokens> {
  const { data } = await httpJson<Tokens & { error?: string; error_description?: string }>(row, tokenEndpoint(row), "Token request", {
    method: "POST",
    headers: { accept: "application/json", "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams(params).toString(),
  });
  // GitHub answers 200 with {"error": ...} on a bad code.
  if (!data?.access_token) throw new ApiError("upstream", `Token request failed: ${data?.error_description || data?.error || "no access_token"}`);
  return data;
}

// --- start / callback / status -----------------------------------------------------------------

export async function startUserAuth(p: Principal, connectionId: string) {
  const row = await getConnectionRow(connectionId);
  const { clientId } = oauthClient(row);
  const state = newToken("vst");
  const codeVerifier = randomBytes(32).toString("base64url");
  const challenge = createHash("sha256").update(codeVerifier).digest("base64url");
  const redirectUri = callbackUrl();
  const url = new URL(row.type === ProviderType.GITLAB ? `${row.baseUrl}/oauth/authorize` : `${row.baseUrl}/login/oauth/authorize`);
  url.searchParams.set("client_id", clientId);
  url.searchParams.set("redirect_uri", redirectUri);
  url.searchParams.set("state", state);
  url.searchParams.set("code_challenge", challenge);
  url.searchParams.set("code_challenge_method", "S256");
  if (row.type === ProviderType.GITLAB) {
    url.searchParams.set("response_type", "code");
    url.searchParams.set("scope", "api");
  }
  const id = newId("vfl");
  const now = Date.now();
  await ensureDb();
  const db = getDb();
  await db.delete(schema.vcsOauthFlow).where(lt(schema.vcsOauthFlow.expiresAt, new Date(now - 86_400_000)));
  await db.insert(schema.vcsOauthFlow).values({
    id,
    userId: p.user.id,
    connectionId: row.id,
    stateHash: sha256(state),
    dataEnc: encryptVcs(JSON.stringify({ codeVerifier, redirectUri } satisfies FlowData), flowAad(id)),
    status: "pending",
    expiresAt: new Date(now + FLOW_TTL_MS),
    createdAt: new Date(now),
  });
  return { authorizationUrl: url.href, flowId: id };
}

export interface CallbackResult {
  ok: boolean;
  message: string;
  provider: string;
}

export async function handleUserAuthCallback(params: URLSearchParams): Promise<CallbackResult> {
  await ensureDb();
  const db = getDb();
  const state = params.get("state") ?? "";
  if (!state) return { ok: false, message: "The authorization response has no state parameter.", provider: "" };
  const flow = await db.query.vcsOauthFlow.findFirst({ where: eq(schema.vcsOauthFlow.stateHash, sha256(state)) });
  if (!flow) return { ok: false, message: "Unknown authorization request.", provider: "" };
  const row = await db.query.vcsConnection.findFirst({ where: eq(schema.vcsConnection.id, flow.connectionId) });
  const provider = row ? providerName(row.type) : "";
  if (flow.status !== "pending") return { ok: false, message: "This authorization request was already used.", provider };
  const finish = async (status: "completed" | "failed", error = "") => {
    const done = await db
      .update(schema.vcsOauthFlow)
      .set({ status, error: error.slice(0, 500), completedAt: new Date() })
      .where(and(eq(schema.vcsOauthFlow.id, flow.id), eq(schema.vcsOauthFlow.status, "pending")))
      .returning({ id: schema.vcsOauthFlow.id });
    return done.length > 0;
  };
  if (flow.expiresAt.getTime() < Date.now()) return { ok: false, message: "This authorization request has expired. Start again.", provider };
  if (!row) {
    await finish("failed", "The connection was deleted");
    return { ok: false, message: "The connection no longer exists.", provider };
  }
  const providerError = params.get("error");
  if (providerError) {
    const msg = `${providerError}${params.get("error_description") ? `: ${params.get("error_description")}` : ""}`;
    await finish("failed", msg);
    return { ok: false, message: `The code host returned an error (${msg.slice(0, 300)}).`, provider };
  }
  const code = params.get("code");
  if (!code) {
    await finish("failed", "No authorization code");
    return { ok: false, message: "The authorization response has no code.", provider };
  }
  const data = JSON.parse(decryptVcs(flow.dataEnc, flowAad(flow.id))) as FlowData;
  let tokens: Tokens;
  let login = "";
  try {
    const { clientId, clientSecret } = oauthClient(row);
    const p: Record<string, string> = {
      client_id: clientId,
      code,
      redirect_uri: data.redirectUri,
      code_verifier: data.codeVerifier,
      grant_type: "authorization_code",
    };
    if (clientSecret) p.client_secret = clientSecret;
    tokens = await postToken(row, p);
    login = await clientFor(row, tokens.access_token).whoami().catch(() => "");
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    await finish("failed", `Token exchange failed: ${msg}`);
    return { ok: false, message: `Token exchange failed: ${msg.slice(0, 300)}`, provider };
  }
  if (!(await finish("completed"))) return { ok: false, message: "This authorization request was already used.", provider };
  await saveTokens(flow.userId, row.id, tokens, login);
  fireUserAuthChanged(flow.userId, row.id);
  return { ok: true, message: "Authorization complete.", provider };
}

function clientFor(row: ConnectionRow, token: string) {
  return row.type === ProviderType.GITLAB ? new GitLabClient(row, { token, userToken: true }) : new GitHubClient(row, { userToken: token });
}

async function saveTokens(userId: string, connectionId: string, tokens: Tokens, login: string | undefined): Promise<void> {
  const now = Date.now();
  const values = {
    state: "connected",
    error: "",
    tokensEnc: encryptVcs(JSON.stringify(tokens), tokenAad(userId, connectionId)),
    ...(login !== undefined ? { login } : {}),
    expiresAt: tokens.expires_in ? new Date(now + tokens.expires_in * 1000) : null,
    refreshExpiresAt: tokens.refresh_token_expires_in ? new Date(now + tokens.refresh_token_expires_in * 1000) : null,
    updatedAt: new Date(now),
  };
  await getDb()
    .insert(schema.vcsUserToken)
    .values({ userId, connectionId, login: login ?? "", ...values })
    .onConflictDoUpdate({ target: [schema.vcsUserToken.userId, schema.vcsUserToken.connectionId], set: values });
}

export async function getUserAuthStatus(p: Principal, connectionId: string, flowId: string) {
  const row = await getConnectionRow(connectionId);
  await ensureDb();
  if (flowId) {
    const flow = await getDb().query.vcsOauthFlow.findFirst({
      where: and(eq(schema.vcsOauthFlow.id, flowId), eq(schema.vcsOauthFlow.userId, p.user.id), eq(schema.vcsOauthFlow.connectionId, row.id)),
    });
    if (!flow) throw new ApiError("not_found", "Authorization flow not found");
    if (flow.status === "failed") return { state: GetUserAuthStatusResponse_State.ERROR, login: "", error: flow.error };
    if (flow.status === "pending") {
      if (flow.expiresAt.getTime() < Date.now()) return { state: GetUserAuthStatusResponse_State.ERROR, login: "", error: "The authorization request expired" };
      return { state: GetUserAuthStatusResponse_State.PENDING, login: "", error: "" };
    }
  }
  const t = await tokenRow(p.user.id, row.id);
  if (t?.state === "connected" && t.tokensEnc) return { state: GetUserAuthStatusResponse_State.CONNECTED, login: t.login, error: "" };
  if (t?.state === "error") return { state: GetUserAuthStatusResponse_State.ERROR, login: t.login, error: t.error };
  return { state: GetUserAuthStatusResponse_State.NOT_CONNECTED, login: "", error: "" };
}

// --- tokens --------------------------------------------------------------------------------------

async function tokenRow(userId: string, connectionId: string) {
  return getDb().query.vcsUserToken.findFirst({ where: and(eq(schema.vcsUserToken.userId, userId), eq(schema.vcsUserToken.connectionId, connectionId)) });
}

const locks = new Map<string, Mutex>();
function lockFor(k: string): Mutex {
  let m = locks.get(k);
  if (!m) locks.set(k, (m = new Mutex()));
  return m;
}

async function markError(userId: string, connectionId: string, error: string): Promise<void> {
  await getDb()
    .update(schema.vcsUserToken)
    .set({ state: "error", error: error.slice(0, 500), tokensEnc: null, expiresAt: null, updatedAt: new Date() })
    .where(and(eq(schema.vcsUserToken.userId, userId), eq(schema.vcsUserToken.connectionId, connectionId)));
  fireUserAuthChanged(userId, connectionId);
}

/** The user's access token on a connection, refreshed before expiry; undefined when not connected. */
export async function getUserToken(userId: string, row: ConnectionRow): Promise<string | undefined> {
  await ensureDb();
  return lockFor(`${userId}:${row.id}`).run(async () => {
    const t = await tokenRow(userId, row.id);
    if (!t?.tokensEnc || t.state !== "connected") return undefined;
    const tokens = JSON.parse(decryptVcs(t.tokensEnc, tokenAad(userId, row.id))) as Tokens;
    if (!t.expiresAt || t.expiresAt.getTime() - Date.now() > REFRESH_MARGIN_MS) return tokens.access_token;
    if (!tokens.refresh_token || (t.refreshExpiresAt && t.refreshExpiresAt.getTime() <= Date.now())) {
      if (t.expiresAt.getTime() <= Date.now()) {
        await markError(userId, row.id, "The access token expired; authorize again");
        return undefined;
      }
      return tokens.access_token;
    }
    try {
      const { clientId, clientSecret } = oauthClient(row);
      const p: Record<string, string> = { client_id: clientId, grant_type: "refresh_token", refresh_token: tokens.refresh_token };
      if (clientSecret) p.client_secret = clientSecret;
      if (row.type === ProviderType.GITLAB) p.redirect_uri = callbackUrl();
      const fresh = await postToken(row, p);
      await saveTokens(userId, row.id, { ...fresh, refresh_token: fresh.refresh_token ?? tokens.refresh_token }, undefined);
      return fresh.access_token;
    } catch (err) {
      await markError(userId, row.id, `Token refresh failed: ${err instanceof Error ? err.message : String(err)}`);
      return undefined;
    }
  });
}

/** Drops a token the code host rejected (401), so the status shows that the user must authorize again. */
export async function invalidateUserToken(userId: string, connectionId: string, reason: string): Promise<void> {
  await markError(userId, connectionId, reason);
}

export async function revokeUserAuth(p: Principal, connectionId: string): Promise<void> {
  const row = await getConnectionRow(connectionId);
  await ensureDb();
  const t = await tokenRow(p.user.id, row.id);
  if (t?.tokensEnc) {
    // Best effort revocation at the code host; the local grant is dropped regardless.
    try {
      const tokens = JSON.parse(decryptVcs(t.tokensEnc, tokenAad(p.user.id, row.id))) as Tokens;
      const { clientId, clientSecret } = oauthClient(row);
      if (row.type === ProviderType.GITLAB) {
        const body = new URLSearchParams({ token: tokens.access_token, client_id: clientId });
        if (clientSecret) body.set("client_secret", clientSecret);
        await httpRequest(row, `${row.baseUrl}/oauth/revoke`, {
          method: "POST",
          headers: { "content-type": "application/x-www-form-urlencoded" },
          body: body.toString(),
          timeoutMs: 5000,
        }).then((r) => r.body?.cancel());
      } else {
        const api = githubApiBase(row.baseUrl);
        await httpRequest(row, `${api}/applications/${clientId}/grant`, {
          method: "DELETE",
          headers: { accept: "application/vnd.github+json" },
          authorization: `Basic ${Buffer.from(`${clientId}:${clientSecret}`).toString("base64")}`,
          body: { access_token: tokens.access_token },
          timeoutMs: 5000,
        }).then((r) => r.body?.cancel());
      }
    } catch {
      // ignore
    }
  }
  await getDb().delete(schema.vcsUserToken).where(and(eq(schema.vcsUserToken.userId, p.user.id), eq(schema.vcsUserToken.connectionId, row.id)));
  fireUserAuthChanged(p.user.id, row.id);
}

// --- hooks (access caches) -----------------------------------------------------------------------

const hooks = new Set<(userId: string, connectionId: string) => void>();
export function onUserAuthChanged(fn: (userId: string, connectionId: string) => void): void {
  hooks.add(fn);
}
function fireUserAuthChanged(userId: string, connectionId: string): void {
  for (const h of hooks) h(userId, connectionId);
}
