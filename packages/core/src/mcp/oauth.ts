/**
 * OAuth for remote MCP servers, run by the server on behalf of each user (PLAN §1.5).
 *
 * - StartOAuth: protected resource discovery (RFC 9728) and authorization server metadata (RFC 8414 /
 *   OIDC), dynamic client registration (RFC 7591) when supported or the configured client id, PKCE (S256)
 *   and the resource indicator (RFC 8707). Returns the authorization URL and a flow id.
 * - The provider redirects to <HARNESS_PUBLIC_URL>/api/mcp/oauth/callback; handleCallback exchanges the
 *   code and stores the tokens encrypted per (user, server).
 * - getAccessToken refreshes before expiry (REFRESH_MARGIN_MS) and serializes refreshes per (user, server).
 * - Authorization state: a changed server URL or newly configured scopes require a new authorization.
 * All outbound requests (discovery, registration, token endpoint) go through the SSRF guard.
 */
import {
  discoverAuthorizationServerMetadata,
  discoverOAuthProtectedResourceMetadata,
  discoverOAuthServerInfo,
  exchangeAuthorization,
  refreshAuthorization,
  registerClient,
  startAuthorization,
} from "@modelcontextprotocol/sdk/client/auth.js";
import type {
  AuthorizationServerMetadata,
  OAuthClientInformationMixed,
  OAuthProtectedResourceMetadata,
  OAuthTokens,
} from "@modelcontextprotocol/sdk/shared/auth.js";
import { and, eq, inArray, lt } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { AuthState, GetOAuthFlowResponse_State } from "../gen/harness/mcp/v1/mcp_pb.ts";
import { Mutex } from "../lib/async.ts";
import { ApiError } from "../lib/errors.ts";
import { newId, newToken, sha256 } from "../lib/ids.ts";
import { assertUrlAllowed, guardedFetchFn } from "../lib/ssrf.ts";
import type { Principal, PublicUser } from "../services/devices.ts";
import { audit } from "./audit.ts";
import { REMOTE_TRANSPORTS, substitute, type StoredConfig } from "./config.ts";
import { announceServer, decryptMcp, encryptMcp, getServerRow, loadSecrets, rowConfig, type ServerRow, type ServerView } from "./registry.ts";

export const FLOW_TTL_MS = 10 * 60_000;
const REFRESH_MARGIN_MS = 60_000;
/** Optional secret holding the client secret of a pre-registered OAuth client (remote.oauth.client_id). */
export const CLIENT_SECRET_NAME = "OAUTH_CLIENT_SECRET";

export interface AuthStatusView {
  state: AuthState;
  message: string;
  grantedScopes: string[];
  account: string;
  expiresAt: string;
}

interface FlowData {
  codeVerifier: string;
  /** remote.url as configured when the flow started. */
  serverUrl: string;
  issuer: string;
  redirectUri: string;
  resource: string;
  scopes: string[];
}

type AuthRow = typeof schema.mcpAuth.$inferSelect;

// --- hooks: the proxy drops pooled connections when a user's credentials change -----------------

type AuthHook = (serverId: string, userId: string) => void;
const authHooks = new Set<AuthHook>();
export function onAuthChanged(fn: AuthHook): () => void {
  authHooks.add(fn);
  return () => authHooks.delete(fn);
}
function fireAuthChanged(serverId: string, userId: string): void {
  for (const h of authHooks) h(serverId, userId);
}

// --- helpers ---------------------------------------------------------------------------------

export function redirectUri(): string {
  return `${config().publicUrl}/api/mcp/oauth/callback`;
}

const tokenAad = (userId: string, serverId: string) => `${userId}:${serverId}:tokens`;
const flowAad = (flowId: string) => `${flowId}:flow`;
const clientAad = (serverId: string, issuer: string) => `${serverId}:${issuer}:client`;

/** The server URL with secrets substituted, as an http(s) URL (ws -> http for discovery). */
export async function resolvedServerUrl(row: ServerRow, cfg: StoredConfig = rowConfig(row)): Promise<string> {
  if (!cfg.remote || !REMOTE_TRANSPORTS.has(row.transport)) throw new ApiError("invalid_request", "OAuth applies to remote MCP servers only");
  const raw = cfg.remote.url.includes("${secret:") ? substitute(cfg.remote.url, await loadSecrets(row.id)) : cfg.remote.url;
  return raw.replace(/^ws(s?):/i, "http$1:");
}

/** RFC 8707 resource indicator for a server URL: no fragment, no trailing slash on the bare origin. */
export function canonicalResource(url: string): string {
  const u = new URL(url);
  u.hash = "";
  const s = u.href;
  return u.pathname === "/" && !u.search ? s.replace(/\/$/, "") : s;
}

function scopesOf(row: ServerRow): string[] {
  return rowConfig(row).remote?.oauth?.scopes ?? [];
}

function list(v: unknown): string[] {
  return Array.isArray(v) ? (v as string[]) : [];
}

function upstreamError(err: unknown, what: string): ApiError {
  if (err instanceof ApiError) return err;
  return new ApiError("upstream", `${what}: ${err instanceof Error ? err.message : String(err)}`);
}

interface Discovery {
  issuer: string;
  metadata?: AuthorizationServerMetadata;
  resourceMetadata?: OAuthProtectedResourceMetadata;
}

async function discover(row: ServerRow, serverUrl: string, fetchFn: ReturnType<typeof guardedFetchFn>): Promise<Discovery> {
  const override = rowConfig(row).remote?.oauth?.authorizationServer;
  try {
    if (override) {
      assertUrlAllowed(override, row.allowPrivateNetwork);
      const resourceMetadata = await discoverOAuthProtectedResourceMetadata(serverUrl, {}, fetchFn).catch(() => undefined);
      const metadata = await discoverAuthorizationServerMetadata(override, { fetchFn });
      return { issuer: override, metadata, resourceMetadata };
    }
    const info = await discoverOAuthServerInfo(serverUrl, { fetchFn });
    assertUrlAllowed(info.authorizationServerUrl, row.allowPrivateNetwork);
    return { issuer: info.authorizationServerUrl, metadata: info.authorizationServerMetadata, resourceMetadata: info.resourceMetadata };
  } catch (err) {
    throw upstreamError(err, "OAuth discovery failed");
  }
}

async function loadClient(serverId: string, issuer: string, redirect: string) {
  const c = schema.mcpOauthClient;
  const r = await getDb().query.mcpOauthClient.findFirst({ where: and(eq(c.serverId, serverId), eq(c.issuer, issuer), eq(c.redirectUri, redirect)) });
  if (!r) return undefined;
  return { info: JSON.parse(decryptMcp(r.clientEnc, clientAad(serverId, issuer))) as OAuthClientInformationMixed, metadata: (r.metadata ?? undefined) as AuthorizationServerMetadata | undefined };
}

/** The configured client (remote.oauth.client_id), else a stored or newly registered dynamic client. */
async function ensureClient(
  row: ServerRow,
  d: Discovery,
  redirect: string,
  scopes: string[],
  fetchFn: ReturnType<typeof guardedFetchFn>,
): Promise<OAuthClientInformationMixed> {
  const clientId = rowConfig(row).remote?.oauth?.clientId;
  if (clientId) {
    const secret = (await loadSecrets(row.id)).get(CLIENT_SECRET_NAME);
    return secret ? { client_id: clientId, client_secret: secret } : { client_id: clientId };
  }
  const stored = await loadClient(row.id, d.issuer, redirect);
  if (stored) return stored.info;
  if (d.metadata && !d.metadata.registration_endpoint) {
    throw new ApiError(
      "failed_precondition",
      "The authorization server does not support dynamic client registration; set remote.oauth.client_id (and the OAUTH_CLIENT_SECRET secret if needed)",
    );
  }
  const methods = d.metadata?.token_endpoint_auth_methods_supported;
  const authMethod = !methods || methods.includes("none") ? "none" : methods.includes("client_secret_post") ? "client_secret_post" : "client_secret_basic";
  let info;
  try {
    info = await registerClient(d.issuer, {
      metadata: d.metadata,
      clientMetadata: {
        client_name: "xlsft`s harness",
        redirect_uris: [redirect],
        grant_types: ["authorization_code", "refresh_token"],
        response_types: ["code"],
        token_endpoint_auth_method: authMethod,
      },
      scope: scopes.length ? scopes.join(" ") : undefined,
      fetchFn,
    });
  } catch (err) {
    throw upstreamError(err, "Dynamic client registration failed");
  }
  await getDb()
    .insert(schema.mcpOauthClient)
    .values({
      serverId: row.id,
      issuer: d.issuer,
      redirectUri: redirect,
      clientEnc: encryptMcp(JSON.stringify(info), clientAad(row.id, d.issuer)),
      metadata: d.metadata ?? null,
      dynamic: true,
      createdAt: new Date(),
    })
    .onConflictDoNothing();
  return info;
}

async function clientForRefresh(row: ServerRow, auth: AuthRow) {
  const stored = await loadClient(row.id, auth.issuer, auth.redirectUri);
  const clientId = rowConfig(row).remote?.oauth?.clientId;
  if (clientId) {
    const secret = (await loadSecrets(row.id)).get(CLIENT_SECRET_NAME);
    return { info: secret ? { client_id: clientId, client_secret: secret } : { client_id: clientId }, metadata: stored?.metadata };
  }
  return stored;
}

// --- StartOAuth / callback / flow status -------------------------------------------------------

function validateReturnUrl(returnUrl: string): string {
  if (!returnUrl) return "";
  let u: URL;
  try {
    u = new URL(returnUrl, config().publicUrl);
  } catch {
    throw new ApiError("invalid_request", "return_url must be a URL");
  }
  if (u.origin !== new URL(config().publicUrl).origin) throw new ApiError("invalid_request", "return_url must be on HARNESS_PUBLIC_URL");
  return u.href;
}

export async function startOAuth(p: Principal, serverId: string, returnUrl: string) {
  const row = await getServerRow(p.user, serverId);
  const ret = validateReturnUrl(returnUrl);
  const serverUrl = await resolvedServerUrl(row);
  assertUrlAllowed(serverUrl, row.allowPrivateNetwork);
  const fetchFn = guardedFetchFn(row.allowPrivateNetwork);
  const d = await discover(row, serverUrl, fetchFn);
  const configured = scopesOf(row);
  const scopes = configured.length ? configured : (d.resourceMetadata?.scopes_supported ?? []);
  const redirect = redirectUri();
  const client = await ensureClient(row, d, redirect, scopes, fetchFn);
  const resource = d.resourceMetadata?.resource ? String(d.resourceMetadata.resource) : canonicalResource(serverUrl);
  const state = newToken("mst");
  let started;
  try {
    started = await startAuthorization(d.issuer, {
      metadata: d.metadata,
      clientInformation: client,
      redirectUrl: redirect,
      scope: scopes.length ? scopes.join(" ") : undefined,
      state,
      resource: new URL(resource),
    });
  } catch (err) {
    throw upstreamError(err, "Cannot start the authorization");
  }
  const id = newId("mfl");
  const now = Date.now();
  const data: FlowData = {
    codeVerifier: started.codeVerifier,
    serverUrl: rowConfig(row).remote?.url ?? "",
    issuer: d.issuer,
    redirectUri: redirect,
    resource,
    scopes,
  };
  await ensureDb();
  const db = getDb();
  await db.delete(schema.mcpOauthFlow).where(lt(schema.mcpOauthFlow.expiresAt, new Date(now - 86_400_000)));
  await db.insert(schema.mcpOauthFlow).values({
    id,
    userId: p.user.id,
    serverId: row.id,
    stateHash: sha256(state),
    dataEnc: encryptMcp(JSON.stringify(data), flowAad(id)),
    returnUrl: ret,
    status: "pending",
    expiresAt: new Date(now + FLOW_TTL_MS),
    createdAt: new Date(now),
  });
  return { flowId: id, authorizationUrl: started.authorizationUrl.href, expiresAt: new Date(now + FLOW_TTL_MS).toISOString() };
}

export interface CallbackResult {
  ok: boolean;
  message: string;
  serverName: string;
  returnUrl: string;
  flowId: string;
}

/** Finishes a flow from the provider redirect (query of /api/mcp/oauth/callback). */
export async function handleCallback(params: URLSearchParams): Promise<CallbackResult> {
  await ensureDb();
  const db = getDb();
  const state = params.get("state") ?? "";
  const fail = (message: string): CallbackResult => ({ ok: false, message, serverName: "", returnUrl: "", flowId: "" });
  if (!state) return fail("The authorization response has no state parameter.");
  const flow = await db.query.mcpOauthFlow.findFirst({ where: eq(schema.mcpOauthFlow.stateHash, sha256(state)) });
  if (!flow) return fail("Unknown authorization request.");
  const row = await db.query.mcpServer.findFirst({ where: eq(schema.mcpServer.id, flow.serverId) });
  const base = { serverName: row?.name ?? "", returnUrl: flow.returnUrl, flowId: flow.id };
  if (flow.status !== "pending") return { ok: false, message: "This authorization request was already used.", ...base };
  const finish = async (status: "completed" | "failed", error = "") => {
    // Only the first callback wins (status guard), so a replayed redirect cannot overwrite the result.
    const done = await db
      .update(schema.mcpOauthFlow)
      .set({ status, error, completedAt: new Date() })
      .where(and(eq(schema.mcpOauthFlow.id, flow.id), eq(schema.mcpOauthFlow.status, "pending")))
      .returning({ id: schema.mcpOauthFlow.id });
    return done.length > 0;
  };
  if (flow.expiresAt.getTime() < Date.now()) return { ok: false, message: "This authorization request has expired. Start again.", ...base };
  if (!row) {
    await finish("failed", "The MCP server was deleted");
    return { ok: false, message: "The MCP server no longer exists.", ...base };
  }
  const providerError = params.get("error");
  if (providerError) {
    const msg = `${providerError}${params.get("error_description") ? `: ${params.get("error_description")}` : ""}`.slice(0, 500);
    await finish("failed", msg);
    return { ok: false, message: `The provider returned an error (${msg}).`, ...base };
  }
  const code = params.get("code");
  if (!code) {
    await finish("failed", "No authorization code");
    return { ok: false, message: "The authorization response has no code.", ...base };
  }
  const data = JSON.parse(decryptMcp(flow.dataEnc, flowAad(flow.id))) as FlowData;
  const fetchFn = guardedFetchFn(row.allowPrivateNetwork);
  let tokens: OAuthTokens;
  try {
    const client = await clientForRefresh(row, { issuer: data.issuer, redirectUri: data.redirectUri } as AuthRow);
    if (!client) throw new ApiError("internal", "OAuth client registration is missing");
    tokens = await exchangeAuthorization(data.issuer, {
      metadata: client.metadata,
      clientInformation: client.info,
      authorizationCode: code,
      codeVerifier: data.codeVerifier,
      redirectUri: data.redirectUri,
      resource: new URL(data.resource),
      fetchFn,
    });
  } catch (err) {
    const msg = (err instanceof Error ? err.message : String(err)).slice(0, 500);
    await finish("failed", `Token exchange failed: ${msg}`);
    return { ok: false, message: `Token exchange failed: ${msg}`, ...base };
  }
  if (!(await finish("completed"))) return { ok: false, message: "This authorization request was already used.", ...base };
  await saveTokens(flow.userId, row.id, tokens, data);
  await audit({ userId: flow.userId, serverId: row.id, action: "oauth_completed", configHash: row.configHash, detail: { flowId: flow.id, issuer: data.issuer } });
  fireAuthChanged(row.id, flow.userId);
  await announceServer(row.id, flow.userId);
  return { ok: true, message: "Authorization complete.", ...base };
}

async function saveTokens(userId: string, serverId: string, tokens: OAuthTokens, data: Omit<FlowData, "codeVerifier">) {
  const now = new Date();
  const granted = tokens.scope ? tokens.scope.split(/\s+/).filter(Boolean).sort() : [...data.scopes];
  const values = {
    state: "connected",
    message: "",
    tokensEnc: encryptMcp(JSON.stringify(tokens), tokenAad(userId, serverId)),
    issuer: data.issuer,
    redirectUri: data.redirectUri,
    resource: data.resource,
    serverUrl: data.serverUrl,
    requestedScopes: [...data.scopes].sort(),
    grantedScopes: granted,
    account: "",
    expiresAt: tokens.expires_in ? new Date(now.getTime() + tokens.expires_in * 1000) : null,
    updatedAt: now,
  };
  await getDb()
    .insert(schema.mcpAuth)
    .values({ userId, serverId, ...values })
    .onConflictDoUpdate({ target: [schema.mcpAuth.userId, schema.mcpAuth.serverId], set: values });
}

export async function getOAuthFlow(p: Principal, flowId: string) {
  if (!flowId) throw new ApiError("invalid_request", "flow_id is required");
  await ensureDb();
  const flow = await getDb().query.mcpOauthFlow.findFirst({ where: and(eq(schema.mcpOauthFlow.id, flowId), eq(schema.mcpOauthFlow.userId, p.user.id)) });
  if (!flow) throw new ApiError("not_found", "OAuth flow not found");
  if (flow.status === "completed") {
    const row = await getDb().query.mcpServer.findFirst({ where: eq(schema.mcpServer.id, flow.serverId) });
    const auth = row ? (await authStatuses(p.user, [{ row, secrets: [] }])).get(row.id) : undefined;
    return { state: GetOAuthFlowResponse_State.COMPLETED, errorMessage: "", auth };
  }
  if (flow.status === "failed") return { state: GetOAuthFlowResponse_State.FAILED, errorMessage: flow.error, auth: undefined };
  if (flow.expiresAt.getTime() < Date.now()) return { state: GetOAuthFlowResponse_State.EXPIRED, errorMessage: "", auth: undefined };
  return { state: GetOAuthFlowResponse_State.PENDING, errorMessage: "", auth: undefined };
}

// --- tokens for the proxy ----------------------------------------------------------------------

const refreshLocks = new Map<string, Mutex>();
function lockFor(key: string): Mutex {
  let m = refreshLocks.get(key);
  if (!m) {
    m = new Mutex();
    refreshLocks.set(key, m);
  }
  return m;
}

async function authRow(userId: string, serverId: string): Promise<AuthRow | undefined> {
  return getDb().query.mcpAuth.findFirst({ where: and(eq(schema.mcpAuth.userId, userId), eq(schema.mcpAuth.serverId, serverId)) });
}

/** Records that the user must (re)authorize; drops stored tokens. */
export async function markNeedsAuth(userId: string, row: ServerRow, message: string): Promise<void> {
  await ensureDb();
  const cur = await authRow(userId, row.id);
  const changed = !cur || cur.state !== "needs_auth" || cur.message !== message;
  const values = { state: "needs_auth", message, tokensEnc: null, expiresAt: null, updatedAt: new Date() };
  await getDb()
    .insert(schema.mcpAuth)
    .values({ userId, serverId: row.id, ...values })
    .onConflictDoUpdate({ target: [schema.mcpAuth.userId, schema.mcpAuth.serverId], set: values });
  if (changed) {
    fireAuthChanged(row.id, userId);
    await announceServer(row.id, userId);
  }
}

/**
 * The caller's access token for a remote server, refreshed when it expires within REFRESH_MARGIN_MS
 * (or when `force` is set after the upstream rejected it). undefined when the user has no usable grant.
 */
export async function getAccessToken(userId: string, row: ServerRow, force = false): Promise<string | undefined> {
  await ensureDb();
  return lockFor(`${userId}:${row.id}`).run(async () => {
    const auth = await authRow(userId, row.id);
    if (!auth?.tokensEnc || auth.state !== "connected") return undefined;
    if (auth.serverUrl !== (rowConfig(row).remote?.url ?? "")) return undefined;
    const tokens = JSON.parse(decryptMcp(auth.tokensEnc, tokenAad(userId, row.id))) as OAuthTokens;
    const expiring = auth.expiresAt && auth.expiresAt.getTime() - Date.now() < REFRESH_MARGIN_MS;
    if (!force && !expiring) return tokens.access_token;
    if (!tokens.refresh_token) {
      if (expiring && auth.expiresAt!.getTime() <= Date.now()) {
        await markNeedsAuth(userId, row, "The access token expired");
        return undefined;
      }
      return force ? undefined : tokens.access_token;
    }
    const client = await clientForRefresh(row, auth);
    if (!client) return tokens.access_token;
    try {
      const fresh = await refreshAuthorization(auth.issuer, {
        metadata: client.metadata,
        clientInformation: client.info,
        refreshToken: tokens.refresh_token,
        resource: auth.resource ? new URL(auth.resource) : undefined,
        fetchFn: guardedFetchFn(row.allowPrivateNetwork),
      });
      await saveTokens(userId, row.id, { ...fresh, refresh_token: fresh.refresh_token ?? tokens.refresh_token }, {
        issuer: auth.issuer,
        redirectUri: auth.redirectUri,
        resource: auth.resource,
        serverUrl: auth.serverUrl,
        scopes: list(auth.requestedScopes),
      });
      return fresh.access_token;
    } catch (err) {
      await markNeedsAuth(userId, row, `Token refresh failed: ${(err instanceof Error ? err.message : String(err)).slice(0, 300)}`);
      return undefined;
    }
  });
}

export async function revokeAuth(p: Principal, serverId: string): Promise<void> {
  const row = await getServerRow(p.user, serverId);
  await ensureDb();
  const auth = await authRow(p.user.id, row.id);
  if (auth?.tokensEnc) {
    // Best effort RFC 7009 revocation; the local grant is dropped regardless.
    try {
      const tokens = JSON.parse(decryptMcp(auth.tokensEnc, tokenAad(p.user.id, row.id))) as OAuthTokens;
      const client = await clientForRefresh(row, auth);
      const endpoint = (client?.metadata as { revocation_endpoint?: string } | undefined)?.revocation_endpoint;
      if (endpoint && client) {
        const body = new URLSearchParams({ token: tokens.refresh_token ?? tokens.access_token, client_id: client.info.client_id });
        if ("client_secret" in client.info && client.info.client_secret) body.set("client_secret", client.info.client_secret);
        await guardedFetchFn(row.allowPrivateNetwork)(endpoint, {
          method: "POST",
          headers: { "content-type": "application/x-www-form-urlencoded" },
          body: body.toString(),
          signal: AbortSignal.timeout(5000),
        }).then((r) => r.body?.cancel());
      }
    } catch {
      // ignore
    }
  }
  await getDb().delete(schema.mcpToolCache).where(and(eq(schema.mcpToolCache.serverId, row.id), eq(schema.mcpToolCache.userId, p.user.id)));
  await audit({ userId: p.user.id, deviceId: p.deviceId, serverId: row.id, action: "oauth_revoked", configHash: row.configHash });
  await markNeedsAuth(p.user.id, row, "Disconnected");
}

// --- status ------------------------------------------------------------------------------------

const status = (state: AuthState, message = "", extra: Partial<AuthStatusView> = {}): AuthStatusView => ({
  state,
  message,
  grantedScopes: [],
  account: "",
  expiresAt: "",
  ...extra,
});

/** Caller's authorization state per server (one query for all servers). */
export async function authStatuses(user: PublicUser, servers: Pick<ServerView, "row" | "secrets">[]): Promise<Map<string, AuthStatusView>> {
  await ensureDb();
  const out = new Map<string, AuthStatusView>();
  const remote = servers.filter((s) => REMOTE_TRANSPORTS.has(s.row.transport));
  const rows = remote.length
    ? await getDb()
        .select()
        .from(schema.mcpAuth)
        .where(and(eq(schema.mcpAuth.userId, user.id), inArray(schema.mcpAuth.serverId, remote.map((s) => s.row.id))))
    : [];
  for (const s of servers) {
    if (!REMOTE_TRANSPORTS.has(s.row.transport)) {
      out.set(s.row.id, status(AuthState.NOT_REQUIRED));
      continue;
    }
    const cfg = rowConfig(s.row);
    const a = rows.find((r) => r.serverId === s.row.id);
    const missing = s.secrets.filter((x) => !x.hasValue && x.name !== CLIENT_SECRET_NAME).map((x) => x.name);
    if (a?.state === "connected" && a.tokensEnc) {
      const requested = new Set([...list(a.requestedScopes), ...list(a.grantedScopes)]);
      const newScopes = (cfg.remote?.oauth?.scopes ?? []).filter((sc) => !requested.has(sc));
      if (a.serverUrl !== (cfg.remote?.url ?? "")) {
        out.set(s.row.id, status(AuthState.NEEDS_AUTH, "The server URL changed; authorize again"));
      } else if (newScopes.length) {
        out.set(s.row.id, status(AuthState.NEEDS_AUTH, `New scopes requested: ${newScopes.join(" ")}`));
      } else {
        out.set(
          s.row.id,
          status(AuthState.CONNECTED, "", { grantedScopes: list(a.grantedScopes), account: a.account, expiresAt: a.expiresAt?.toISOString() ?? "" }),
        );
      }
    } else if (a?.state === "needs_auth") {
      out.set(s.row.id, status(AuthState.NEEDS_AUTH, a.message));
    } else if (a?.state === "error") {
      out.set(s.row.id, status(AuthState.ERROR, a.message));
    } else if (cfg.remote?.oauth) {
      out.set(s.row.id, status(AuthState.NEEDS_AUTH, "Not authorized yet"));
    } else if (missing.length) {
      out.set(s.row.id, status(AuthState.NEEDS_AUTH, `Secret ${missing.join(", ")} is not set`));
    } else {
      out.set(s.row.id, status(AuthState.NOT_REQUIRED));
    }
  }
  return out;
}

/** True when a token-carrying connection is expected (stored grant or configured OAuth). */
export function expectsOAuth(row: ServerRow): boolean {
  return !!rowConfig(row).remote?.oauth;
}

/** Throws needs_auth (FAILED_PRECONDITION / ERROR_TYPE_NEEDS_AUTH) when the caller's state for the server is NEEDS_AUTH. */
export async function assertAuthorized(user: PublicUser, view: Pick<ServerView, "row" | "secrets">): Promise<void> {
  const st = (await authStatuses(user, [view])).get(view.row.id)!;
  if (st.state === AuthState.NEEDS_AUTH) {
    throw new ApiError("needs_auth", `MCP server ${view.row.name} needs authorization: ${st.message || "authorize it first"}`);
  }
}
