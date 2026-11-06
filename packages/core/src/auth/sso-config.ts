/**
 * Identity provider configuration for OIDC and SAML sign-in (harness.auth.v1.SsoService). The client secret is encrypted
 * with a key derived from HARNESS_SECRET and never returned; the rest is not secret (a SAML certificate is public).
 */
import { X509Certificate, randomBytes } from "node:crypto";
import { asc, eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { cleanMappings, type RoleMapping } from "./roles.ts";

export type SsoProtocol = "oidc" | "saml";

/** Non-secret settings, as stored in sso_provider.config. */
export interface SsoStored {
  issuer: string;
  client_id: string;
  scopes: string[];
  idp_entity_id: string;
  idp_sso_url: string;
  idp_certificate: string;
  email_attribute: string;
  name_attribute: string;
  username_attribute: string;
  groups_attribute: string;
  auto_create_users: boolean;
  allowed_domains: string[];
  allowed_groups: string[];
  role_mappings: RoleMapping[];
}

export interface SsoProviderView extends SsoStored {
  id: string;
  name: string;
  protocol: SsoProtocol;
  enabled: boolean;
  client_secret_set: boolean;
  redirect_uri: string;
  saml_acs_url: string;
  saml_entity_id: string;
  saml_metadata_url: string;
}

/** What the sign-in code works with: the view plus the decrypted secret. */
export interface SsoRuntime extends SsoProviderView {
  client_secret: string;
}

export interface SsoInput extends Partial<SsoStored> {
  id?: string;
  name?: string;
  protocol?: SsoProtocol;
  enabled?: boolean;
  client_secret?: string;
  clear_client_secret?: boolean;
}

const secretKey = () => deriveKey("setting-secrets");
const secretAad = (id: string) => `sso_provider:${id}:client_secret`;
const ID = /^[a-z0-9][a-z0-9-]{0,62}$/;

export function ssoUrls(id: string) {
  const base = config().publicUrl;
  return {
    redirect_uri: `${base}/api/sso/${id}/oidc/callback`,
    saml_acs_url: `${base}/api/sso/${id}/saml/acs`,
    saml_entity_id: `${base}/api/sso/${id}/saml/metadata`,
    saml_metadata_url: `${base}/api/sso/${id}/saml/metadata`,
  };
}

function strings(v: unknown): string[] {
  return [...new Set((Array.isArray(v) ? v : []).map((s) => (typeof s === "string" ? s.trim() : "")).filter(Boolean))];
}

function normalize(c: Partial<SsoStored>): SsoStored {
  const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");
  return {
    issuer: str(c.issuer).replace(/\/+$/, ""),
    client_id: str(c.client_id),
    scopes: strings(c.scopes),
    idp_entity_id: str(c.idp_entity_id),
    idp_sso_url: str(c.idp_sso_url),
    idp_certificate: str(c.idp_certificate),
    email_attribute: str(c.email_attribute),
    name_attribute: str(c.name_attribute),
    username_attribute: str(c.username_attribute),
    groups_attribute: str(c.groups_attribute),
    auto_create_users: c.auto_create_users ?? true,
    allowed_domains: strings(c.allowed_domains).map((d) => d.toLowerCase().replace(/^@/, "")),
    allowed_groups: strings(c.allowed_groups),
    role_mappings: cleanMappings(c.role_mappings),
  };
}

type Row = typeof schema.ssoProvider.$inferSelect;

function toView(r: Row): SsoProviderView {
  return {
    ...normalize((r.config ?? {}) as Partial<SsoStored>),
    id: r.id,
    name: r.name,
    protocol: r.protocol === "saml" ? "saml" : "oidc",
    enabled: r.enabled,
    client_secret_set: !!r.clientSecretEnc,
    ...ssoUrls(r.id),
  };
}

function toRuntime(r: Row): SsoRuntime {
  return { ...toView(r), client_secret: r.clientSecretEnc ? decryptSecret(r.clientSecretEnc, secretAad(r.id), secretKey()) : "" };
}

export async function listSsoProviders(): Promise<SsoProviderView[]> {
  await ensureDb();
  return (await getDb().select().from(schema.ssoProvider).orderBy(asc(schema.ssoProvider.name))).map(toView);
}

/** What the sign-in page shows: enabled providers only, nothing but their name. */
export async function ssoButtons(): Promise<{ id: string; name: string }[]> {
  return (await listSsoProviders()).filter((p) => p.enabled).map((p) => ({ id: p.id, name: p.name }));
}

export async function getSsoRuntime(id: string): Promise<SsoRuntime | undefined> {
  await ensureDb();
  const r = await getDb().query.ssoProvider.findFirst({ where: eq(schema.ssoProvider.id, id) });
  return r ? toRuntime(r) : undefined;
}

/** A URL of an identity provider: HTTPS, or HTTP to this machine only (a local test provider). */
export function checkIdpUrl(raw: string, what: string): URL {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    throw new ApiError("invalid_request", `${what} is not a valid URL`);
  }
  const loopback = u.hostname === "localhost" || u.hostname === "127.0.0.1" || u.hostname === "[::1]";
  if (u.protocol !== "https:" && !(u.protocol === "http:" && loopback)) throw new ApiError("invalid_request", `${what} must use https:// (http:// only for localhost)`);
  return u;
}

/** Checks what signing in needs. */
export function validateSso(c: SsoRuntime | (SsoStored & { protocol: SsoProtocol; client_secret?: string; name?: string })): void {
  if (c.protocol === "oidc") {
    if (!c.issuer) throw new ApiError("invalid_request", "OIDC needs the issuer URL");
    checkIdpUrl(c.issuer, "The issuer");
    if (!c.client_id) throw new ApiError("invalid_request", "OIDC needs a client ID");
    return;
  }
  if (!c.idp_sso_url) throw new ApiError("invalid_request", "SAML needs the identity provider's sign-in URL");
  checkIdpUrl(c.idp_sso_url, "The sign-in URL");
  if (!c.idp_entity_id) throw new ApiError("invalid_request", "SAML needs the identity provider's entity ID");
  if (!c.idp_certificate) throw new ApiError("invalid_request", "SAML needs the identity provider's signing certificate");
  parseCertificate(c.idp_certificate);
}

export function parseCertificate(pem: string): X509Certificate {
  const body = pem.includes("BEGIN CERTIFICATE") ? pem : `-----BEGIN CERTIFICATE-----\n${pem}\n-----END CERTIFICATE-----`;
  try {
    return new X509Certificate(body);
  } catch {
    throw new ApiError("invalid_request", "The signing certificate is not a valid PEM certificate");
  }
}

/** The stored provider with `input` applied, for saving or for testing before it is saved. */
export async function resolveSso(input: SsoInput): Promise<SsoRuntime> {
  await ensureDb();
  const id = input.id?.trim() ?? "";
  const current = id ? await getDb().query.ssoProvider.findFirst({ where: eq(schema.ssoProvider.id, id) }) : undefined;
  const base: Partial<SsoRuntime> = current ? toRuntime(current) : {};
  const stripped = Object.fromEntries(Object.entries(input).filter(([, v]) => v !== undefined));
  const merged = { ...base, ...stripped } as SsoRuntime;
  const secret = input.clear_client_secret ? "" : input.client_secret || base.client_secret || "";
  return {
    ...normalize(merged),
    id,
    name: (input.name ?? base.name ?? "").trim(),
    protocol: (input.protocol ?? base.protocol) === "saml" ? "saml" : "oidc",
    enabled: input.enabled ?? base.enabled ?? false,
    client_secret: secret,
    client_secret_set: !!secret,
    ...ssoUrls(id || "new"),
  };
}

/** Creates or updates a provider. A provider being set up may be incomplete while it is disabled. */
export async function saveSsoProvider(input: SsoInput): Promise<SsoProviderView> {
  const c = await resolveSso(input);
  if (!c.name) throw new ApiError("invalid_request", "A provider needs a name");
  if (c.enabled) validateSso(c);
  const db = getDb();
  let id = c.id;
  const exists = id ? await db.query.ssoProvider.findFirst({ where: eq(schema.ssoProvider.id, id) }) : undefined;
  if (exists && !c.enabled && exists.enabled) await assertStillEnforceable(id);
  if (!id) id = `${c.name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40) || "sso"}-${randomBytes(3).toString("hex")}`;
  if (!ID.test(id)) throw new ApiError("invalid_request", "A provider id has lowercase letters, digits and dashes");
  const { client_secret, enabled, name, protocol, client_secret_set: _s, redirect_uri: _a, saml_acs_url: _b, saml_entity_id: _c, saml_metadata_url: _d, id: _e, ...stored } = c;
  const clientSecretEnc = client_secret ? encryptSecret(client_secret, secretAad(id), secretKey()) : null;
  const now = new Date();
  const values = { name, protocol, enabled, config: stored, clientSecretEnc, updatedAt: now };
  const [row] = await db
    .insert(schema.ssoProvider)
    .values({ id, createdAt: now, ...values })
    .onConflictDoUpdate({ target: schema.ssoProvider.id, set: values })
    .returning();
  return toView(row!);
}

export async function deleteSsoProvider(id: string): Promise<void> {
  await ensureDb();
  await assertStillEnforceable(id);
  await getDb().delete(schema.ssoProvider).where(eq(schema.ssoProvider.id, id));
}

/** require_sso needs a provider to sign in with: the last enabled one cannot go while it is on. */
async function assertStillEnforceable(id: string): Promise<void> {
  const s = await getSsoSettings();
  if (!s.require_sso) return;
  const others = (await listSsoProviders()).filter((p) => p.enabled && p.id !== id);
  if (!others.length) throw new ApiError("failed_precondition", "Sign-in requires SSO and this is the only enabled provider; turn that requirement off first");
}

// --- server-wide settings ------------------------------------------------------------------------------

const SETTINGS_KEY = "sso_settings";

export interface SsoSettings {
  require_sso: boolean;
  break_glass_logins: string[];
}

export async function getSsoSettings(): Promise<SsoSettings> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, SETTINGS_KEY) });
  const v = (r?.value ?? {}) as Partial<SsoSettings>;
  return { require_sso: !!v.require_sso, break_glass_logins: Array.isArray(v.break_glass_logins) ? v.break_glass_logins : [] };
}

export async function setSsoSettings(input: SsoSettings): Promise<SsoSettings> {
  const s: SsoSettings = { require_sso: !!input.require_sso, break_glass_logins: strings(input.break_glass_logins).map((l) => l.toLowerCase()) };
  if (s.require_sso) {
    if (!(await ssoButtons()).length) throw new ApiError("failed_precondition", "Enable an identity provider before requiring SSO");
    if (!s.break_glass_logins.length) throw new ApiError("invalid_request", "List at least one break-glass admin login, so a broken identity provider cannot lock everyone out");
  }
  await ensureDb();
  const now = new Date();
  await getDb()
    .insert(schema.setting)
    .values({ key: SETTINGS_KEY, value: s, updatedAt: now })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value: s, updatedAt: now } });
  return s;
}
