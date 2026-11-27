/**
 * Company knowledge source connections (KnowledgeConnectionService). Secrets are one AES-256-GCM blob per connection (HKDF purpose
 * "knowledge-secrets", AAD <id>:secrets), write-only. A connection with borrowConnectionId has none of its own: it signs in as that
 * company tracker connection, whose credentials are decrypted at every open and never copied. docs/design/tracker.md, section 6.
 */
import { asc, eq } from "drizzle-orm";
import { record } from "../audit/log.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { deriveKey } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal } from "../services/devices.ts";
import { getConnection, providerMessage, readSecretsWith, secretPresent, toApiError, trackerOpenOptions, writeSecretsWith, type ConnectionRow, type TestResult } from "../tracker/connections.ts";
import { newFlow, oauthRedirectUri, type OAuthFlow } from "../tracker/oauth-state.ts";
import { TrackerError, type AuthMethodName, type StoredCredentials } from "../tracker/provider.ts";
import { KNOWLEDGE, knowledgeModule } from "../tracker/registry.ts";
import { checkAddress, checkAuth, checkOptions, hrefOf, secretKey } from "../tracker/signin.ts";
import { vcsSignIn } from "../tracker/vcs-signin.ts";
import type { KnowledgeCapabilities, KnowledgeModule, KnowledgeOpenOptions, KnowledgeProvider } from "./provider.ts";

export type KnowledgeConnectionRow = typeof schema.knowledgeConnection.$inferSelect;

export const KNOWLEDGE_PROVIDERS: KnowledgeCapabilities[] = KNOWLEDGE.map((m) => m.capabilities);

const key = () => deriveKey("knowledge-secrets");
const readSecrets = (row: Pick<KnowledgeConnectionRow, "id" | "secretsEnc">) => readSecretsWith(row, key());
const writeSecrets = (id: string, s: StoredCredentials) => writeSecretsWith(id, s, key());

function requireAdmin(p: Principal): void {
  if (p.user.role !== "admin") throw new ApiError("permission", "Only admins can manage company knowledge sources");
}

function moduleOf(provider: string): KnowledgeModule {
  const m = knowledgeModule(provider);
  if (!m) throw new TrackerError("other", `Unknown knowledge provider ${provider}`);
  return m;
}

export interface KnowledgeConnectionInput {
  id?: string;
  provider: string;
  name: string;
  baseUrl: string;
  auth: string;
  login?: string;
  secret?: string;
  oauthClientId?: string;
  oauthClientSecret?: string;
  enabled: boolean;
  allowPrivateNetwork: boolean;
  query?: string;
  options?: Record<string, string>;
  borrowConnectionId?: string;
}

/** The tracker connection a knowledge provider borrows, checked against what it may borrow from. */
async function borrowable(m: KnowledgeModule, id: string): Promise<ConnectionRow> {
  const t = await getConnection(id);
  if (!t) throw new ApiError("failed_precondition", "The tracker connection whose sign-in this uses is gone");
  if (!m.capabilities.borrowFrom.includes(t.provider)) throw new ApiError("invalid_request", `${m.capabilities.displayName} can't use the sign-in of a ${t.provider} connection`);
  return t;
}

interface Checked {
  baseUrl: string;
  name: string;
  auth: AuthMethodName | "";
  options: Record<string, string>;
  borrow?: ConnectionRow;
}

async function checkInput(input: KnowledgeConnectionInput): Promise<Checked> {
  const m = knowledgeModule(input.provider);
  if (!m) throw new ApiError("invalid_request", `Unknown knowledge provider ${input.provider}`);
  const options = checkOptions(m.capabilities.optionFields, input.options);
  let out: Checked;
  if (input.borrowConnectionId) {
    const borrow = await borrowable(m, input.borrowConnectionId);
    // An own address with a borrowed sign-in sends the tracker's credentials there; only admins set either.
    const own = input.baseUrl.trim() ? hrefOf(checkAddress(input.baseUrl, { allowPrivateNetwork: input.allowPrivateNetwork, normalize: m.normalizeUrl })) : "";
    m.validate?.(own || borrow.baseUrl, { ...(borrow.options ?? {}), ...options });
    out = { baseUrl: own, name: input.name.trim() || `${m.capabilities.displayName} · ${borrow.name}`, auth: "", options, borrow };
  } else {
    const auth = checkAuth(m.capabilities.authMethods, input.auth, input.login, input.oauthClientId);
    const u = checkAddress(input.baseUrl, { defaultBaseUrl: m.capabilities.defaultBaseUrl, allowPrivateNetwork: input.allowPrivateNetwork, normalize: m.normalizeUrl });
    m.validate?.(hrefOf(u), options);
    out = { baseUrl: hrefOf(u), name: input.name.trim() || u.host, auth, options };
  }
  if (out.name.length > 200) throw new ApiError("invalid_request", "Names have at most 200 characters");
  return out;
}

export async function listKnowledgeConnections(): Promise<KnowledgeConnectionRow[]> {
  await ensureDb();
  const t = schema.knowledgeConnection;
  return getDb().select().from(t).orderBy(asc(t.name), asc(t.id));
}

export async function getKnowledgeConnection(id: string): Promise<KnowledgeConnectionRow | undefined> {
  await ensureDb();
  return getDb().query.knowledgeConnection.findFirst({ where: eq(schema.knowledgeConnection.id, id) });
}

/** The connection a source or document id names: its own id, or, for ids from before the split, the tracker connection it was migrated from. */
export async function knowledgeConnectionOf(id: string): Promise<KnowledgeConnectionRow | undefined> {
  if (!id) return undefined;
  const own = await getKnowledgeConnection(id);
  if (own) return own;
  return getDb().query.knowledgeConnection.findFirst({ where: eq(schema.knowledgeConnection.migratedFrom, id) });
}

async function requireKnowledgeConnection(id: string): Promise<KnowledgeConnectionRow> {
  const row = id ? await getKnowledgeConnection(id) : undefined;
  if (!row) throw new ApiError("not_found", `Knowledge source ${id} not found`);
  return row;
}

export async function putKnowledgeConnection(p: Principal, input: KnowledgeConnectionInput): Promise<KnowledgeConnectionRow> {
  requireAdmin(p);
  await ensureDb();
  const c = await checkInput(input);
  const t = schema.knowledgeConnection;
  const now = new Date();
  const existing = input.id ? await getKnowledgeConnection(input.id) : undefined;
  if (input.id && !existing) throw new ApiError("not_found", `Knowledge source ${input.id} not found`);
  const id = existing?.id ?? newId("kc");
  let secrets: StoredCredentials = existing ? readSecrets(existing) : {};
  if (c.borrow) {
    secrets = {};
  } else {
    const where = secretKey(c.auth);
    if (where && input.secret !== undefined) secrets[where] = input.secret;
    if (input.oauthClientSecret !== undefined) secrets.clientSecret = input.oauthClientSecret;
    if (existing && (existing.baseUrl !== c.baseUrl || existing.oauthClientId !== (input.oauthClientId ?? ""))) {
      delete secrets.accessToken;
      delete secrets.refreshToken;
      delete secrets.expiresAt;
    }
  }
  const values = {
    provider: input.provider,
    name: c.name,
    baseUrl: c.baseUrl,
    auth: c.auth,
    login: c.borrow ? "" : (input.login?.trim() ?? ""),
    oauthClientId: c.borrow ? "" : (input.oauthClientId?.trim() ?? ""),
    secretsEnc: writeSecrets(id, secrets),
    enabled: input.enabled,
    allowPrivateNetwork: input.allowPrivateNetwork,
    query: input.query?.trim() ?? "",
    options: c.options,
    borrowConnectionId: c.borrow?.id ?? "",
    updatedAt: now,
    updatedBy: p.user.id,
  };
  const db = getDb();
  let row: KnowledgeConnectionRow;
  if (existing) {
    [row] = (await db.update(t).set(values).where(eq(t.id, id)).returning()) as [KnowledgeConnectionRow];
  } else {
    [row] = (await db.insert(t).values({ id, ...values, createdAt: now }).returning()) as [KnowledgeConnectionRow];
  }
  await record({ action: existing ? "knowledge_connection.update" : "knowledge_connection.create", actor: { id: p.user.id, label: p.user.email }, target: { type: "knowledge_connection", id, label: c.name } });
  return row;
}

export async function deleteKnowledgeConnection(p: Principal, id: string): Promise<void> {
  requireAdmin(p);
  const row = await requireKnowledgeConnection(id);
  await getDb().delete(schema.knowledgeConnection).where(eq(schema.knowledgeConnection.id, id));
  await record({ action: "knowledge_connection.delete", actor: { id: p.user.id, label: p.user.email }, target: { type: "knowledge_connection", id, label: row.name } });
}

/** What the provider opens with: the borrowed connection's sign-in resolved now, or its own. */
async function openOptions(row: KnowledgeConnectionRow, m: KnowledgeModule): Promise<KnowledgeOpenOptions> {
  if (row.borrowConnectionId) {
    const t = await borrowable(m, row.borrowConnectionId);
    if (!t.enabled) throw new ApiError("failed_precondition", `${t.name}, whose sign-in this uses, is disabled`);
    const o = trackerOpenOptions(t);
    return {
      ...o,
      baseUrl: row.baseUrl || t.baseUrl,
      allowPrivateNetwork: row.baseUrl ? row.allowPrivateNetwork : t.allowPrivateNetwork,
      options: { ...o.options, ...(row.options ?? {}) },
      borrowedFrom: t.provider,
      trackerBaseUrl: t.baseUrl,
    };
  }
  const options = { ...(row.options ?? {}) };
  return {
    baseUrl: row.baseUrl,
    auth: row.auth as AuthMethodName,
    login: row.login,
    oauthClientId: row.oauthClientId,
    credentials: readSecrets(row),
    options,
    allowPrivateNetwork: row.allowPrivateNetwork,
    onOAuthTokens: async (t) => {
      const fresh = await requireKnowledgeConnection(row.id);
      const cur = readSecrets(fresh);
      await getDb()
        .update(schema.knowledgeConnection)
        .set({ secretsEnc: writeSecrets(row.id, { ...cur, accessToken: t.accessToken, refreshToken: t.refreshToken ?? cur.refreshToken, expiresAt: t.expiresAt }) })
        .where(eq(schema.knowledgeConnection.id, row.id));
    },
    vcs: row.auth === "vcs" ? vcsSignIn(options) : undefined,
    borrowedFrom: "",
    trackerBaseUrl: "",
  };
}

export async function openKnowledge(row: KnowledgeConnectionRow): Promise<KnowledgeProvider> {
  const m = moduleOf(row.provider);
  return m.open(await openOptions(row, m));
}

/** Whether it has what it signs in with: its own, or the borrowed connection's. */
export async function hasKnowledgeSecret(row: KnowledgeConnectionRow): Promise<boolean> {
  if (row.borrowConnectionId) {
    const t = await getConnection(row.borrowConnectionId);
    return !!t && (t.auth === "vcs" || (!!t.secretsEnc && secretPresent(t.auth, readSecretsWith(t, deriveKey("tracker-secrets")))));
  }
  if (row.auth === "vcs") return true;
  return !!row.secretsEnc && secretPresent(row.auth, readSecrets(row));
}

export async function testKnowledgeConnection(p: Principal, id: string, draft?: KnowledgeConnectionInput): Promise<TestResult> {
  requireAdmin(p);
  await ensureDb();
  let provider: KnowledgeProvider;
  let row: KnowledgeConnectionRow | undefined;
  try {
    if (id) {
      row = await requireKnowledgeConnection(id);
      provider = await openKnowledge(row);
    } else {
      if (!draft) throw new ApiError("invalid_request", "Pass a connection to test");
      const c = await checkInput(draft);
      const m = moduleOf(draft.provider);
      if (c.borrow) {
        provider = m.open(await openOptions({ ...draftRow(draft, c), borrowConnectionId: c.borrow.id }, m));
      } else {
        if (c.auth === "oauth") return { ok: false, error: "Save the connection and authorize it first", login: "", displayName: "" };
        const where = secretKey(c.auth);
        provider = m.open({
          baseUrl: c.baseUrl,
          auth: c.auth as AuthMethodName,
          login: draft.login?.trim() ?? "",
          oauthClientId: "",
          credentials: where && draft.secret ? { [where]: draft.secret } : {},
          options: c.options,
          allowPrivateNetwork: draft.allowPrivateNetwork,
          vcs: c.auth === "vcs" ? vcsSignIn(c.options) : undefined,
          borrowedFrom: "",
          trackerBaseUrl: "",
        });
      }
    }
  } catch (err) {
    if (err instanceof ApiError && err.type !== "failed_precondition") throw err;
    return { ok: false, error: providerMessage(err), login: "", displayName: "" };
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
      .update(schema.knowledgeConnection)
      .set({ healthy: result.ok, lastError: result.error, lastCheckedAt: new Date() })
      .where(eq(schema.knowledgeConnection.id, row.id));
  }
  return result;
}

function draftRow(d: KnowledgeConnectionInput, c: Checked): KnowledgeConnectionRow {
  const now = new Date();
  return {
    id: "",
    provider: d.provider,
    name: c.name,
    baseUrl: c.baseUrl,
    auth: c.auth,
    login: "",
    oauthClientId: "",
    secretsEnc: "",
    enabled: true,
    allowPrivateNetwork: d.allowPrivateNetwork,
    query: d.query ?? "",
    options: c.options,
    borrowConnectionId: "",
    migratedFrom: "",
    healthy: false,
    lastError: "",
    lastCheckedAt: null,
    createdAt: now,
    updatedAt: now,
    updatedBy: "",
  };
}

// ---- OAuth for a connection with its own sign-in ----

export async function startKnowledgeOAuth(p: Principal, id: string): Promise<string> {
  requireAdmin(p);
  const row = await requireKnowledgeConnection(id);
  if (row.borrowConnectionId) throw new ApiError("failed_precondition", "This source uses the sign-in of a tracker connection: authorize that one");
  if (row.auth !== "oauth") throw new ApiError("failed_precondition", "This connection does not use OAuth");
  const oauth = moduleOf(row.provider).oauth;
  if (!oauth) throw new ApiError("failed_precondition", `${row.provider} has no OAuth`);
  const { state, challenge } = newFlow("knowledge", id);
  return oauth.authorizeUrl({ baseUrl: row.baseUrl, clientId: row.oauthClientId, redirectUri: oauthRedirectUri(), state, challenge, allowPrivateNetwork: row.allowPrivateNetwork, options: row.options ?? {} }).catch(toApiError);
}

export async function finishKnowledgeOAuth(flow: OAuthFlow, params: URLSearchParams): Promise<{ ok: boolean; message: string; name: string }> {
  const row = await getKnowledgeConnection(flow.id);
  if (!row) return { ok: false, message: "The connection no longer exists.", name: "" };
  if (params.get("error")) return { ok: false, message: "The provider refused the authorization.", name: row.name };
  const code = params.get("code");
  if (!code) return { ok: false, message: "The provider sent no authorization code.", name: row.name };
  const oauth = knowledgeModule(row.provider)?.oauth;
  if (!oauth) return { ok: false, message: "This provider has no OAuth.", name: row.name };
  const s = readSecrets(row);
  try {
    const t = await oauth.exchangeCode({ baseUrl: row.baseUrl, clientId: row.oauthClientId, clientSecret: s.clientSecret, code, redirectUri: oauthRedirectUri(), verifier: flow.verifier, allowPrivateNetwork: row.allowPrivateNetwork, options: row.options ?? {} });
    await getDb()
      .update(schema.knowledgeConnection)
      .set({ secretsEnc: writeSecrets(row.id, { ...s, accessToken: t.accessToken, refreshToken: t.refreshToken, expiresAt: t.expiresAt }), healthy: true, lastError: "", lastCheckedAt: new Date() })
      .where(eq(schema.knowledgeConnection.id, row.id));
    return { ok: true, message: "", name: row.name };
  } catch (err) {
    return { ok: false, message: providerMessage(err), name: row.name };
  }
}
