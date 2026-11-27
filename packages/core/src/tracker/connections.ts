/**
 * Admin connections to external trackers (TrackerConnectionService). Secrets are one AES-256-GCM blob per connection (HKDF
 * purpose "tracker-secrets", AAD <id>:secrets), write-only, and never reach a client or a log. docs/design/tracker.md.
 */
import { and, asc, eq, inArray } from "drizzle-orm";
import { record } from "../audit/log.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal } from "../services/devices.ts";
import { newFlow, oauthRedirectUri, type OAuthFlow } from "./oauth-state.ts";
import { TrackerError, type AuthMethodName, type Capabilities, type OpenOptions, type StoredCredentials, type TrackerModule, type TrackerProvider } from "./provider.ts";
import { TRACKERS, trackerModule } from "./registry.ts";
import { checkAddress, checkAuth, checkOptions, hrefOf, secretKey } from "./signin.ts";
import { vcsSignIn } from "./vcs-signin.ts";

export { oauthRedirectUri } from "./oauth-state.ts";

export type ConnectionRow = typeof schema.trackerConnection.$inferSelect;

type Secrets = StoredCredentials;

/** The capabilities of every tracker the server supports, in registry order. */
export const PROVIDERS: Capabilities[] = TRACKERS.map((m) => m.capabilities);

const key = () => deriveKey("tracker-secrets");

/** Decrypts a secrets blob of either kind of connection. */
export function readSecretsWith(row: { id: string; secretsEnc: string }, k: Buffer): Secrets {
  if (!row.secretsEnc) return {};
  return JSON.parse(decryptSecret(row.secretsEnc, `${row.id}:secrets`, k)) as Secrets;
}

export function writeSecretsWith(id: string, s: Secrets, k: Buffer): string {
  const clean = Object.fromEntries(Object.entries(s).filter(([, v]) => v !== undefined && v !== ""));
  return Object.keys(clean).length ? encryptSecret(JSON.stringify(clean), `${id}:secrets`, k) : "";
}

const readSecrets = (row: Pick<ConnectionRow, "id" | "secretsEnc">) => readSecretsWith(row, key());
const writeSecrets = (id: string, s: Secrets) => writeSecretsWith(id, s, key());

function moduleOf(provider: string): TrackerModule {
  const m = trackerModule(provider);
  if (!m) throw new TrackerError("other", `Unknown provider ${provider}`);
  return m;
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
  options?: Record<string, string>;
}

function checkInput(input: ConnectionInput): { baseUrl: string; name: string; auth: AuthMethodName; options: Record<string, string> } {
  const m = trackerModule(input.provider);
  if (!m) throw new ApiError("invalid_request", `Unknown provider ${input.provider}`);
  const auth = checkAuth(m.capabilities.authMethods, input.auth, input.login, input.oauthClientId);
  const u = checkAddress(input.baseUrl, { defaultBaseUrl: m.capabilities.defaultBaseUrl, allowPrivateNetwork: input.allowPrivateNetwork, normalize: m.normalizeUrl });
  const baseUrl = hrefOf(u);
  const options = checkOptions(m.capabilities.optionFields, input.options);
  m.validate?.(baseUrl, options);
  const name = input.name.trim() || u.host;
  if (name.length > 200) throw new ApiError("invalid_request", "Names have at most 200 characters");
  return { baseUrl, name, auth, options };
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
  const { baseUrl, name, auth, options } = checkInput(input);
  const t = schema.trackerConnection;
  const now = new Date();
  const existing = input.id ? await getConnection(input.id) : undefined;
  if (input.id && !existing) throw new ApiError("not_found", `Connection ${input.id} not found`);
  const id = existing?.id ?? newId("tc");
  const secrets = existing ? readSecrets(existing) : {};
  const where = secretKey(auth);
  if (where && input.secret !== undefined) secrets[where] = input.secret;
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
    auth,
    login: input.login?.trim() ?? "",
    oauthClientId: input.oauthClientId?.trim() ?? "",
    secretsEnc: writeSecrets(id, secrets),
    enabled: input.enabled,
    allowPrivateNetwork: input.allowPrivateNetwork,
    options,
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

/**
 * What a provider opens with for a stored connection: its credentials decrypted now, refreshed OAuth tokens written back to the
 * row. A knowledge connection borrowing this one opens with the same (src/knowledge/connections.ts); nothing is copied.
 */
export function trackerOpenOptions(row: ConnectionRow, opts: { retryDelaysMs?: number[] } = {}): OpenOptions {
  return {
    baseUrl: row.baseUrl,
    auth: row.auth as AuthMethodName,
    login: row.login,
    oauthClientId: row.oauthClientId,
    credentials: readSecrets(row),
    options: { ...(row.options ?? {}) },
    allowPrivateNetwork: row.allowPrivateNetwork,
    retryDelaysMs: opts.retryDelaysMs,
    onOAuthTokens: async (t) => {
      const fresh = await requireConnection(row.id);
      const cur = readSecrets(fresh);
      await getDb()
        .update(schema.trackerConnection)
        .set({ secretsEnc: writeSecrets(row.id, { ...cur, accessToken: t.accessToken, refreshToken: t.refreshToken ?? cur.refreshToken, expiresAt: t.expiresAt }) })
        .where(eq(schema.trackerConnection.id, row.id));
    },
    vcs: row.auth === "vcs" ? vcsSignIn(row.options ?? {}) : undefined,
  };
}

/** A provider for a stored connection. */
export function openProvider(row: ConnectionRow, opts: { retryDelaysMs?: number[] } = {}): TrackerProvider {
  return moduleOf(row.provider).open(trackerOpenOptions(row, opts));
}

/** Whether a method has what it signs in with. */
export function secretPresent(auth: string, s: Secrets): boolean {
  switch (auth) {
    case "token":
    case "email_token":
      return !!s.token;
    case "password":
      return !!s.password;
    case "oauth":
      return !!(s.accessToken || s.refreshToken);
    case "vcs":
      return true;
  }
  return false;
}

export function hasSecret(row: ConnectionRow): boolean {
  if (row.auth === "vcs") return true;
  if (!row.secretsEnc) return false;
  return secretPresent(row.auth, readSecrets(row));
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
    const { baseUrl, auth, options } = checkInput(draft);
    if (auth === "oauth") return { ok: false, error: "Save the connection and authorize it first", login: "", displayName: "" };
    const where = secretKey(auth);
    provider = moduleOf(draft.provider).open({
      baseUrl,
      auth,
      login: draft.login?.trim() ?? "",
      oauthClientId: "",
      credentials: where && draft.secret ? { [where]: draft.secret } : {},
      options,
      allowPrivateNetwork: draft.allowPrivateNetwork,
      vcs: auth === "vcs" ? vcsSignIn(options) : undefined,
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

export function toApiError(err: unknown): never {
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

export async function startOAuth(p: Principal, id: string): Promise<string> {
  requireAdmin(p);
  const row = await requireConnection(id);
  if (row.auth !== "oauth") throw new ApiError("failed_precondition", "This connection does not use OAuth");
  const oauth = moduleOf(row.provider).oauth;
  if (!oauth) throw new ApiError("failed_precondition", `${row.provider} has no OAuth`);
  const { state, challenge } = newFlow("tracker", id);
  return oauth.authorizeUrl({ baseUrl: row.baseUrl, clientId: row.oauthClientId, redirectUri: oauthRedirectUri(), state, challenge, allowPrivateNetwork: row.allowPrivateNetwork, options: row.options ?? {} }).catch(toApiError);
}

export async function finishOAuth(flow: OAuthFlow, params: URLSearchParams): Promise<{ ok: boolean; message: string; name: string }> {
  const row = await getConnection(flow.id);
  if (!row) return { ok: false, message: "The connection no longer exists.", name: "" };
  if (params.get("error")) return { ok: false, message: "The instance refused the authorization.", name: row.name };
  const code = params.get("code");
  if (!code) return { ok: false, message: "The instance sent no authorization code.", name: row.name };
  const oauth = trackerModule(row.provider)?.oauth;
  if (!oauth) return { ok: false, message: "This provider has no OAuth.", name: row.name };
  const s = readSecrets(row);
  try {
    const t = await oauth.exchangeCode({ baseUrl: row.baseUrl, clientId: row.oauthClientId, clientSecret: s.clientSecret, code, redirectUri: oauthRedirectUri(), verifier: flow.verifier, allowPrivateNetwork: row.allowPrivateNetwork, options: row.options ?? {} });
    await getDb()
      .update(schema.trackerConnection)
      .set({ secretsEnc: writeSecrets(row.id, { ...s, accessToken: t.accessToken, refreshToken: t.refreshToken, expiresAt: t.expiresAt }), healthy: true, lastError: "", lastCheckedAt: new Date() })
      .where(eq(schema.trackerConnection.id, row.id));
    return { ok: true, message: "", name: row.name };
  } catch (err) {
    return { ok: false, message: providerMessage(err), name: row.name };
  }
}
