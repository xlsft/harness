/**
 * LDAP sign-in settings (AdminService.Get/SetLdapConfig), stored in the `setting` table under "ldap". The
 * search account's bind password is encrypted with a key derived from HARNESS_SECRET and never returned.
 */
import { eq } from "drizzle-orm";
import { FilterParser } from "ldapts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { cleanMappings, type RoleMapping } from "./roles.ts";

const KEY = "ldap";
const SECRET_AAD = "setting:ldap:bind_password";

export type LocalLogin = "fallback" | "break_glass";

/** Settings as admins see and edit them (no password). */
export interface LdapSettings {
  enabled: boolean;
  urls: string[];
  start_tls: boolean;
  ca_cert: string;
  skip_verify: boolean;
  bind_dn: string;
  user_base_dn: string;
  user_filter: string;
  username_attribute: string;
  email_attribute: string;
  name_attribute: string;
  allowed_group_dn: string;
  admin_group_dn: string;
  /** Group DN to role rules, first match wins, re-checked at every sign-in. */
  role_mappings: RoleMapping[];
  auto_create_users: boolean;
  local_login: LocalLogin;
  break_glass_logins: string[];
  timeout_ms: number;
}

export interface PublicLdapSettings extends LdapSettings {
  bind_password_set: boolean;
}

/** What the directory code works with: the settings plus the decrypted bind password. */
export interface LdapRuntimeConfig extends LdapSettings {
  bind_password: string;
}

interface Stored extends LdapSettings {
  bind_password_enc: string | null;
}

/** An edit: the settings plus a write-only password (undefined / "" keeps the stored one). */
export interface LdapSettingsInput extends Partial<LdapSettings> {
  bind_password?: string;
  clear_bind_password?: boolean;
}

export const DEFAULT_USER_FILTER = "(|(uid={{username}})(mail={{username}}))";
export const DEFAULT_TIMEOUT_MS = 5000;

export function defaultLdapSettings(): LdapSettings {
  return {
    enabled: false,
    urls: [],
    start_tls: false,
    ca_cert: "",
    skip_verify: false,
    bind_dn: "",
    user_base_dn: "",
    user_filter: DEFAULT_USER_FILTER,
    username_attribute: "uid",
    email_attribute: "mail",
    name_attribute: "cn",
    allowed_group_dn: "",
    admin_group_dn: "",
    role_mappings: [],
    auto_create_users: true,
    local_login: "fallback",
    break_glass_logins: [],
    timeout_ms: DEFAULT_TIMEOUT_MS,
  };
}

const secretKey = () => deriveKey("setting-secrets");

async function readStored(): Promise<Stored> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, KEY) });
  return { ...defaultLdapSettings(), bind_password_enc: null, ...((r?.value as Partial<Stored> | undefined) ?? {}) };
}

function publicView(s: Stored): PublicLdapSettings {
  const { bind_password_enc, ...rest } = s;
  return { ...rest, bind_password_set: !!bind_password_enc };
}

export async function getLdapSettings(): Promise<PublicLdapSettings> {
  return publicView(await readStored());
}

/** The stored settings with the decrypted bind password. */
export async function getLdapRuntimeConfig(): Promise<LdapRuntimeConfig> {
  const { bind_password_enc, ...rest } = await readStored();
  return { ...rest, bind_password: bind_password_enc ? decryptSecret(bind_password_enc, SECRET_AAD, secretKey()) : "" };
}

/**
 * The stored settings with `input` applied, validated (for saving, or for testing a config before it is
 * saved). An empty / missing password keeps the stored one.
 */
export async function resolveLdapConfig(input: LdapSettingsInput): Promise<LdapRuntimeConfig> {
  const current = await getLdapRuntimeConfig();
  const next = normalize({ ...current, ...stripUndefined(input) });
  let password = current.bind_password;
  if (input.clear_bind_password) password = "";
  if (input.bind_password) password = input.bind_password;
  return { ...next, bind_password: password };
}

/** Replaces the settings (unset fields keep their value), validating them when LDAP is enabled. */
export async function setLdapSettings(input: LdapSettingsInput): Promise<PublicLdapSettings> {
  const cfg = await resolveLdapConfig(input);
  validate(cfg);
  const { bind_password, ...settings } = cfg;
  const stored: Stored = { ...settings, bind_password_enc: bind_password ? encryptSecret(bind_password, SECRET_AAD, secretKey()) : null };
  const now = new Date();
  await getDb()
    .insert(schema.setting)
    .values({ key: KEY, value: stored, updatedAt: now })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value: stored, updatedAt: now } });
  return publicView(stored);
}

function stripUndefined<T extends object>(o: T): Partial<T> {
  return Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined)) as Partial<T>;
}

function normalize(s: LdapSettings): LdapSettings {
  const d = defaultLdapSettings();
  const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");
  return {
    enabled: !!s.enabled,
    urls: (Array.isArray(s.urls) ? s.urls : []).map(str).filter(Boolean),
    start_tls: !!s.start_tls,
    ca_cert: str(s.ca_cert),
    skip_verify: !!s.skip_verify,
    bind_dn: str(s.bind_dn),
    user_base_dn: str(s.user_base_dn),
    user_filter: str(s.user_filter) || d.user_filter,
    username_attribute: str(s.username_attribute) || d.username_attribute,
    email_attribute: str(s.email_attribute) || d.email_attribute,
    name_attribute: str(s.name_attribute) || d.name_attribute,
    allowed_group_dn: str(s.allowed_group_dn),
    admin_group_dn: str(s.admin_group_dn),
    role_mappings: cleanMappings(s.role_mappings),
    auto_create_users: s.auto_create_users ?? d.auto_create_users,
    local_login: s.local_login === "break_glass" ? "break_glass" : "fallback",
    break_glass_logins: [...new Set((Array.isArray(s.break_glass_logins) ? s.break_glass_logins : []).map((l) => str(l).toLowerCase()).filter(Boolean))],
    timeout_ms: Number.isInteger(s.timeout_ms) && s.timeout_ms > 0 ? Math.min(s.timeout_ms, 60_000) : d.timeout_ms,
  };
}

const ATTRIBUTE = /^[A-Za-z][A-Za-z0-9-]*$/;

/** Checks what a connection needs. Disabled settings may be incomplete (saved while being set up). */
export function validate(cfg: LdapRuntimeConfig, opts: { force?: boolean } = {}): void {
  if (!cfg.enabled && !opts.force) return;
  if (!cfg.urls.length) throw new ApiError("invalid_request", "LDAP needs at least one server URL");
  for (const u of cfg.urls) {
    let url: URL;
    try {
      url = new URL(u);
    } catch {
      throw new ApiError("invalid_request", `Invalid LDAP URL: ${u}`);
    }
    if (url.protocol !== "ldap:" && url.protocol !== "ldaps:") throw new ApiError("invalid_request", `LDAP URLs start with ldap:// or ldaps:// (${u})`);
    if (url.pathname.replace(/^\//, "") || url.search) throw new ApiError("invalid_request", `LDAP URLs have no path or query; set the base DN separately (${u})`);
  }
  if (!cfg.user_base_dn) throw new ApiError("invalid_request", "LDAP needs a user search base DN");
  if (!cfg.user_filter.includes("{{username}}")) throw new ApiError("invalid_request", "The user filter must contain {{username}}");
  try {
    FilterParser.parseString(cfg.user_filter.replaceAll("{{username}}", "x"));
  } catch (e) {
    throw new ApiError("invalid_request", `Invalid user filter: ${(e as Error).message}`);
  }
  for (const [name, v] of [
    ["username", cfg.username_attribute],
    ["email", cfg.email_attribute],
    ["name", cfg.name_attribute],
  ] as const) {
    if (!ATTRIBUTE.test(v)) throw new ApiError("invalid_request", `Invalid ${name} attribute: ${v}`);
  }
  if (cfg.bind_dn && !cfg.bind_password) throw new ApiError("invalid_request", "The search account needs a bind password");
  if (cfg.local_login === "break_glass" && !cfg.break_glass_logins.length) {
    throw new ApiError("invalid_request", "List at least one break-glass admin login, or allow local accounts as a fallback");
  }
}
