/**
 * Admin connections to external trackers (TrackerConnectionService). Secrets are one AES-256-GCM blob per connection (HKDF
 * purpose "tracker-secrets", AAD <id>:secrets), write-only, and never reach a client or a log. docs/design/tracker.md.
 */
import { createHash, randomBytes } from "node:crypto";
import { and, asc, eq, inArray } from "drizzle-orm";
import { record } from "../audit/log.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { assertUrlAllowed } from "../lib/ssrf.ts";
import type { Principal } from "../services/devices.ts";
import { TrackerError, type AuthMethodName, type Capabilities, type TrackerProvider } from "./provider.ts";
import { normalizeYouTrackUrl, YOUTRACK_CAPABILITIES, YouTrack } from "./youtrack.ts";

export type ConnectionRow = typeof schema.trackerConnection.$inferSelect;

interface Secrets {
  token?: string;
  password?: string;
  clientSecret?: string;
  accessToken?: string;
  refreshToken?: string;
  expiresAt?: number;
}

export const PROVIDERS: Capabilities[] = [YOUTRACK_CAPABILITIES];

const key = () => deriveKey("tracker-secrets");

function readSecrets(row: Pick<ConnectionRow, "id" | "secretsEnc">): Secrets {
  if (!row.secretsEnc) return {};
  return JSON.parse(decryptSecret(row.secretsEnc, `${row.id}:secrets`, key())) as Secrets;
}

function writeSecrets(id: string, s: Secrets): string {
  const clean = Object.fromEntries(Object.entries(s).filter(([, v]) => v !== undefined && v !== ""));
  return Object.keys(clean).length ? encryptSecret(JSON.stringify(clean), `${id}:secrets`, key()) : "";
}

function requireAdmin(p: Principal): void {
  if (p.user.role !== "admin") throw new ApiError("permission", "Only admins can manage company tracker connections");
}

export interface ConnectionInput {
  id?: string;
  provider: string;
  name: string;
  baseUrl: string;
  auth: AuthMethodName;
  login?: string;
  secret?: string;
  oauthClientId?: string;
  oauthClientSecret?: string;
  enabled: boolean;
  allowPrivateNetwork: boolean;
  knowledgeEnabled: boolean;
  knowledgeQuery?: string;
}

function checkInput(input: ConnectionInput): { baseUrl: string; name: string } {
  if (!PROVIDERS.some((c) => c.provider === input.provider)) throw new ApiError("invalid_request", `Unknown provider ${input.provider}`);
  if (!["token", "password", "oauth"].includes(input.auth)) throw new ApiError("invalid_request", "Choose how this connection signs in");
  const baseUrl = normalizeYouTrackUrl(input.baseUrl);
  if (!baseUrl) throw new ApiError("invalid_request", "base_url is required");
  let u: URL;
  try {
    u = new URL(baseUrl);
  } catch {
    throw new ApiError("invalid_request", "base_url must be an http(s) URL");
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") throw new ApiError("invalid_request", "base_url must be an http(s) URL");
  if (u.username || u.password || u.search || u.hash) throw new ApiError("invalid_request", "base_url must not carry credentials, a query or a fragment");
  assertUrlAllowed(baseUrl, input.allowPrivateNetwork);
  if (input.auth === "password" && !input.login?.trim()) throw new ApiError("invalid_request", "login is required for login and password");
  if (input.auth === "oauth" && !input.oauthClientId?.trim()) throw new ApiError("invalid_request", "The OAuth client id is required: an admin of the instance creates the client");
  const name = input.name.trim() || u.host;
  if (name.length > 200) throw new ApiError("invalid_request", "Names have at most 200 characters");
  return { baseUrl, name };
}

export async function listConnections(): Promise<ConnectionRow[]> {
  await ensureDb();
  const t = schema.trackerConnection;
  return getDb().select().from(t).orderBy(asc(t.name), asc(t.id));
}

export async function getConnection(id: string): Promise<ConnectionRow | undefined> {
  await ensureDb();
  return getDb().query.trackerConnection.findFirst({ where: eq(schema.trackerConnection.id, id) });
}

export async function requireConnection(id: string): Promise<ConnectionRow> {
  const row = id ? await getConnection(id) : undefined;
  if (!row) throw new ApiError("not_found", `Connection ${id} not found`);
  return row;
}

export async function connectionsById(ids: string[]): Promise<Map<string, ConnectionRow>> {
  if (!ids.length) return new Map();
  await ensureDb();
  const t = schema.trackerConnection;
  const rows = await getDb().select().from(t).where(inArray(t.id, ids));
  return new Map(rows.map((r) => [r.id, r]));
}

export async function putConnection(p: Principal, input: ConnectionInput): Promise<ConnectionRow> {
  requireAdmin(p);
  await ensureDb();
  const { baseUrl, name } = checkInput(input);
  const t = schema.trackerConnection;
  const now = new Date();
  const existing = input.id ? await getConnection(input.id) : undefined;
  if (input.id && !existing) throw new ApiError("not_found", `Connection ${input.id} not found`);
  const id = existing?.id ?? newId("tc");
  const secrets = existing ? readSecrets(existing) : {};
  if (input.auth === "token" && input.secret !== undefined) secrets.token = input.secret;
  if (input.auth === "password" && input.secret !== undefined) secrets.password = input.secret;
  if (input.oauthClientSecret !== undefined) secrets.clientSecret = input.oauthClientSecret;
  // A changed instance or client: what was authorized there means nothing here.
  if (existing && (existing.baseUrl !== baseUrl || existing.oauthClientId !== (input.oauthClientId ?? ""))) {
    delete secrets.accessToken;
    delete secrets.refreshToken;
    delete secrets.expiresAt;
  }
  const values = {
    provider: input.provider,
    name,
    baseUrl,
    auth: input.auth,
    login: input.login?.trim() ?? "",
    oauthClientId: input.oauthClientId?.trim() ?? "",
    secretsEnc: writeSecrets(id, secrets),
    enabled: input.enabled,
    allowPrivateNetwork: input.allowPrivateNetwork,
    knowledgeEnabled: input.knowledgeEnabled,
    knowledgeQuery: input.knowledgeQuery?.trim() ?? "",
    updatedAt: now,
    updatedBy: p.user.id,
  };
  const db = getDb();
  let row: ConnectionRow;
  if (existing) {
    [row] = (await db.update(t).set(values).where(eq(t.id, id)).returning()) as [ConnectionRow];
  } else {
    [row] = (await db.insert(t).values({ id, ...values, createdAt: now }).returning()) as [ConnectionRow];
  }
  await record({ action: existing ? "tracker_connection.update" : "tracker_connection.create", actor: { id: p.user.id, label: p.user.email }, target: { type: "tracker_connection", id, label: name } });
  return row;
}

/** Stages lose what used this connection, pending outputs stop, and linked cards keep their text. */
export async function dropConnectionUses(scope: "admin" | "device", id: string, boardIds?: string[]): Promise<void> {
  const db = getDb();
  const i = schema.boardStageInput;
  const o = schema.boardStageOutput;
  const c = schema.boardCard;
  const co = schema.boardCardOutput;
  const s = schema.boardStage;
  await db.transaction(async (tx) => {
    const stageIds = boardIds ? (await tx.select({ id: s.id }).from(s).where(inArray(s.boardId, boardIds))).map((r) => r.id) : undefined;
    if (stageIds && !stageIds.length) return;
    const onStages = <T extends typeof i | typeof o>(t: T) => (stageIds ? inArray(t.stageId, stageIds) : undefined);
    await tx.delete(i).where(and(eq(i.connectionScope, scope), eq(i.connectionId, id), onStages(i)));
    await tx.delete(o).where(and(eq(o.connectionScope, scope), eq(o.connectionId, id), onStages(o)));
    const cards = await tx
      .update(c)
      .set({ externalState: "disconnected", updatedAt: new Date() })
      .where(and(eq(c.connectionScope, scope), eq(c.connectionId, id), boardIds ? inArray(c.boardId, boardIds) : undefined))
      .returning({ id: c.id });
    if (cards.length) {
      await tx
        .update(co)
        .set({ status: "superseded", leaseUntil: null, updatedAt: new Date() })
        .where(
          and(
            inArray(
              co.cardId,
              cards.map((r) => r.id),
            ),
            inArray(co.status, ["pending", "running", "failed"]),
          ),
        );
    }
  });
}

export async function deleteConnection(p: Principal, id: string): Promise<void> {
  requireAdmin(p);
  const row = await requireConnection(id);
  await dropConnectionUses("admin", id);
  await getDb().delete(schema.trackerConnection).where(eq(schema.trackerConnection.id, id));
  await record({ action: "tracker_connection.delete", actor: { id: p.user.id, label: p.user.email }, target: { type: "tracker_connection", id, label: row.name } });
}

/** A provider for a stored connection. Refreshed OAuth tokens go back to the row. */
export function openProvider(row: ConnectionRow, opts: { retryDelaysMs?: number[] } = {}): TrackerProvider & { discoverHub?: YouTrack["discoverHub"] } {
  if (row.provider !== "youtrack") throw new TrackerError("other", `Unknown provider ${row.provider}`);
  const s = readSecrets(row);
  return new YouTrack({
    baseUrl: row.baseUrl,
    auth: row.auth as AuthMethodName,
    allowPrivateNetwork: row.allowPrivateNetwork,
    retryDelaysMs: opts.retryDelaysMs,
    credentials: {
      token: s.token,
      login: row.login,
      password: s.password,
      oauth: row.auth === "oauth" ? { clientId: row.oauthClientId, clientSecret: s.clientSecret, accessToken: s.accessToken, refreshToken: s.refreshToken, expiresAt: s.expiresAt } : undefined,
    },
    onOAuthTokens: async (t) => {
      const fresh = await requireConnection(row.id);
      const cur = readSecrets(fresh);
      await getDb()
        .update(schema.trackerConnection)
        .set({ secretsEnc: writeSecrets(row.id, { ...cur, accessToken: t.accessToken, refreshToken: t.refreshToken ?? cur.refreshToken, expiresAt: t.expiresAt }) })
        .where(eq(schema.trackerConnection.id, row.id));
    },
  });
}

export function hasSecret(row: ConnectionRow): boolean {
  if (!row.secretsEnc) return false;
  const s = readSecrets(row);
  return row.auth === "token" ? !!s.token : row.auth === "password" ? !!s.password : !!(s.accessToken || s.refreshToken);
}

export interface TestResult {
  ok: boolean;
  error: string;
  login: string;
  displayName: string;
}

export async function testConnection(p: Principal, id: string, draft?: ConnectionInput): Promise<TestResult> {
  requireAdmin(p);
  await ensureDb();
  let provider: TrackerProvider;
  let row: ConnectionRow | undefined;
  if (id) {
    row = await requireConnection(id);
    provider = openProvider(row);
  } else {
    if (!draft) throw new ApiError("invalid_request", "Pass a connection to test");
    const { baseUrl } = checkInput(draft);
    if (draft.auth === "oauth") return { ok: false, error: "Save the connection and authorize it first", login: "", displayName: "" };
    provider = new YouTrack({
      baseUrl,
      auth: draft.auth,
      allowPrivateNetwork: draft.allowPrivateNetwork,
      credentials: { token: draft.auth === "token" ? draft.secret : undefined, login: draft.login, password: draft.auth === "password" ? draft.secret : undefined },
    });
  }
  let result: TestResult;
  try {
    const me = await provider.test();
    result = { ok: true, error: "", login: me.login, displayName: me.displayName };
  } catch (err) {
    result = { ok: false, error: providerMessage(err), login: "", displayName: "" };
  }
  if (row) {
    await getDb()
      .update(schema.trackerConnection)
      .set({ healthy: result.ok, lastError: result.error, lastCheckedAt: new Date() })
      .where(eq(schema.trackerConnection.id, row.id));
  }
  return result;
}

export function providerMessage(err: unknown): string {
  if (err instanceof TrackerError) return err.message;
  if (err instanceof ApiError) return err.message;
  return "The provider could not be reached";
}

/** A provider for what a regular user asks of an admin connection. Its errors are shown to that user. */
async function usable(id: string): Promise<TrackerProvider> {
  const row = await requireConnection(id);
  if (!row.enabled) throw new ApiError("failed_precondition", `${row.name} is disabled`);
  return openProvider(row);
}

function toApiError(err: unknown): never {
  if (err instanceof TrackerError) {
    if (err.kind === "not_found") throw new ApiError("not_found", err.message);
    throw new ApiError(err.kind === "transient" ? "upstream" : "failed_precondition", err.message);
  }
  throw err;
}

export async function searchConnectionIssues(id: string, query: string, limit: number) {
  if (!query.trim()) throw new ApiError("invalid_request", "query is required");
  const provider = await usable(id);
  return provider.search(query, { limit: Math.min(Math.max(limit || 20, 1), 50) }).catch(toApiError);
}

export async function listConnectionFields(id: string, project: string) {
  const provider = await usable(id);
  return provider.fields(project).catch(toApiError);
}

// ---- OAuth ----

const OAUTH_FLOW_TTL_MS = 10 * 60_000;

export const oauthRedirectUri = () => `${config().publicUrl}/api/tracker/oauth/callback`;

/** The state is the flow: sealed with the server's key, so nothing is stored until it comes back. */
export async function startOAuth(p: Principal, id: string): Promise<string> {
  requireAdmin(p);
  const row = await requireConnection(id);
  if (row.auth !== "oauth") throw new ApiError("failed_precondition", "This connection does not use OAuth");
  const provider = openProvider(row);
  const hub = await provider.discoverHub!().catch(toApiError);
  const verifier = randomBytes(32).toString("base64url");
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  const state = encryptSecret(JSON.stringify({ id, verifier, exp: Date.now() + OAUTH_FLOW_TTL_MS }), "tracker-oauth-state", key());
  const u = new URL(`${hub.url}/api/rest/oauth2/auth`);
  u.search = new URLSearchParams({
    response_type: "code",
    client_id: row.oauthClientId,
    scope: hub.serviceId,
    redirect_uri: oauthRedirectUri(),
    access_type: "offline",
    state,
    code_challenge: challenge,
    code_challenge_method: "S256",
  }).toString();
  return u.href;
}

export async function finishOAuth(params: URLSearchParams): Promise<{ ok: boolean; message: string; name: string }> {
  let flow: { id: string; verifier: string; exp: number };
  try {
    flow = JSON.parse(decryptSecret(params.get("state") ?? "", "tracker-oauth-state", key())) as typeof flow;
  } catch {
    return { ok: false, message: "This authorization link is not valid.", name: "" };
  }
  if (Date.now() > flow.exp) return { ok: false, message: "This authorization took too long. Start it again.", name: "" };
  const row = await getConnection(flow.id);
  if (!row) return { ok: false, message: "The connection no longer exists.", name: "" };
  if (params.get("error")) return { ok: false, message: "The instance refused the authorization.", name: row.name };
  const code = params.get("code");
  if (!code) return { ok: false, message: "The instance sent no authorization code.", name: row.name };
  const s = readSecrets(row);
  const provider = new YouTrack({ baseUrl: row.baseUrl, auth: "oauth", credentials: {}, allowPrivateNetwork: row.allowPrivateNetwork });
  try {
    const t = await provider.exchangeToken({ grant_type: "authorization_code", code, redirect_uri: oauthRedirectUri(), code_verifier: flow.verifier }, row.oauthClientId, s.clientSecret);
    await getDb()
      .update(schema.trackerConnection)
      .set({ secretsEnc: writeSecrets(row.id, { ...s, accessToken: t.accessToken, refreshToken: t.refreshToken, expiresAt: t.expiresAt }), healthy: true, lastError: "", lastCheckedAt: new Date() })
      .where(eq(schema.trackerConnection.id, row.id));
    return { ok: true, message: "", name: row.name };
  } catch (err) {
    return { ok: false, message: providerMessage(err), name: row.name };
  }
}
