/**
 * LDAP / Active Directory directory access (ldapts): find a user with the search account, verify their
 * password by binding as them, and read group membership. Logins only ever reach the directory through
 * escapeFilterValue (RFC 4515), and passwords are never logged or put into error messages.
 */
import { Client, InvalidCredentialsError, NoSuchObjectError, type Entry } from "ldapts";
import type { LdapRuntimeConfig } from "./ldap-config.ts";

/** A directory user mapped to account fields. */
export interface DirectoryUser {
  dn: string;
  username: string;
  email: string;
  name: string;
  /** memberOf values (when the directory exposes them). */
  groups: string[];
  /** Passes allowed_group_dn (true when none is set). */
  allowed: boolean;
  /** Member of admin_group_dn (false when none is set). */
  admin: boolean;
}

export type AuthResult =
  | { status: "ok"; user: DirectoryUser }
  | { status: "not_found" }
  | { status: "invalid_credentials" }
  | { status: "not_allowed"; user: DirectoryUser };

/** The directory could not be reached or refused the search account. */
export class LdapUnavailableError extends Error {}

/** Escapes a value for use inside an LDAP search filter (RFC 4515 section 3). */
export function escapeFilterValue(value: string): string {
  return value.replace(/[\\*()\0]/g, (c) => `\\${c.charCodeAt(0).toString(16).padStart(2, "0")}`);
}

/** The user filter with every {{username}} replaced by the escaped login. */
export function userFilter(template: string, login: string): string {
  return template.replaceAll("{{username}}", escapeFilterValue(login));
}

/** Compares DNs loosely: case-insensitive, ignoring spaces around separators. */
export function sameDn(a: string, b: string): boolean {
  const norm = (s: string) => s.toLowerCase().replace(/\s*([,=+])\s*/g, "$1").trim();
  return norm(a) === norm(b);
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message || e.constructor.name : String(e);
}

/** Opens a connection to the first URL that answers (with StartTLS when configured). */
export async function connect(cfg: LdapRuntimeConfig): Promise<{ client: Client; url: string }> {
  const failures: string[] = [];
  const tlsOptions = { ca: cfg.ca_cert || undefined, rejectUnauthorized: !cfg.skip_verify };
  for (const url of cfg.urls) {
    // ldapts speaks TLS from the start whenever tlsOptions are given, so ldap:// URLs get them only for StartTLS.
    const secure = url.startsWith("ldaps://");
    const client = new Client({ url, timeout: cfg.timeout_ms, connectTimeout: cfg.timeout_ms, tlsOptions: secure ? tlsOptions : undefined, strictDN: false });
    try {
      if (cfg.start_tls && url.startsWith("ldap://")) await client.startTLS(tlsOptions);
      return { client, url };
    } catch (e) {
      failures.push(`${url}: ${errText(e)}`);
      await client.unbind().catch(() => {});
    }
  }
  throw new LdapUnavailableError(failures.length ? failures.join("; ") : "No LDAP server URL configured");
}

/** Binds with the search account (or stays anonymous without one). */
async function bindSearchAccount(client: Client, cfg: LdapRuntimeConfig): Promise<void> {
  if (!cfg.bind_dn) return;
  try {
    await client.bind(cfg.bind_dn, cfg.bind_password);
  } catch (e) {
    if (e instanceof InvalidCredentialsError) throw new LdapUnavailableError("The search account's bind DN or password is wrong");
    throw new LdapUnavailableError(`Search account bind failed: ${errText(e)}`);
  }
}

/** Opens a connection bound as the search account; ldapts connects lazily, so the bind reaches the server. */
async function openSearch(cfg: LdapRuntimeConfig): Promise<{ client: Client; url: string }> {
  const c = await connect(cfg);
  try {
    await bindSearchAccount(c.client, cfg);
    if (!cfg.bind_dn) await c.client.search(cfg.user_base_dn, { scope: "base", filter: "(objectClass=*)", attributes: ["1.1"] }).catch((e) => {
      if (!(e instanceof NoSuchObjectError)) throw e;
    });
    return c;
  } catch (e) {
    await c.client.unbind().catch(() => {});
    if (e instanceof LdapUnavailableError) throw e;
    throw new LdapUnavailableError(`${c.url}: ${errText(e)}`);
  }
}

function attr(entry: Entry, name: string): string[] {
  const key = Object.keys(entry).find((k) => k.toLowerCase() === name.toLowerCase());
  const v = key ? entry[key] : undefined;
  if (v === undefined) return [];
  const list = Array.isArray(v) ? v : [v];
  return list.map((x) => (Buffer.isBuffer(x) ? x.toString("utf8") : String(x)));
}

async function inGroup(client: Client, groupDn: string, user: { dn: string; username: string; groups: string[] }): Promise<boolean> {
  if (user.groups.some((g) => sameDn(g, groupDn))) return true;
  // Directories without memberOf: look for the user in the group entry itself.
  const filter = `(|(member=${escapeFilterValue(user.dn)})(uniqueMember=${escapeFilterValue(user.dn)})(memberUid=${escapeFilterValue(user.username)}))`;
  try {
    const r = await client.search(groupDn, { scope: "base", filter, attributes: ["1.1"] });
    return r.searchEntries.length > 0;
  } catch (e) {
    if (e instanceof NoSuchObjectError) return false;
    throw e;
  }
}

/** Searches the user (exactly one match) on a connection bound as the search account. */
async function searchUser(client: Client, cfg: LdapRuntimeConfig, login: string): Promise<DirectoryUser | undefined> {
  const { searchEntries } = await client.search(cfg.user_base_dn, {
    scope: "sub",
    filter: userFilter(cfg.user_filter, login),
    attributes: [cfg.username_attribute, cfg.email_attribute, cfg.name_attribute, "memberOf"],
    sizeLimit: 2,
  });
  // Ambiguous logins never sign anyone in.
  if (searchEntries.length !== 1) return undefined;
  const e = searchEntries[0]!;
  const username = attr(e, cfg.username_attribute)[0] ?? login;
  const base = {
    dn: e.dn,
    username,
    email: attr(e, cfg.email_attribute)[0] ?? "",
    name: attr(e, cfg.name_attribute)[0] ?? username,
    groups: attr(e, "memberOf"),
  };
  return {
    ...base,
    allowed: cfg.allowed_group_dn ? await inGroup(client, cfg.allowed_group_dn, base) : true,
    admin: cfg.admin_group_dn ? await inGroup(client, cfg.admin_group_dn, base) : false,
  };
}

/** Connects and binds with the search account (AdminService.TestLdapConnection). */
export async function testConnection(cfg: LdapRuntimeConfig): Promise<{ url: string; message: string }> {
  const { client, url } = await openSearch(cfg);
  try {
    const r = await client.search(cfg.user_base_dn, { scope: "base", filter: "(objectClass=*)", attributes: ["1.1"] }).catch((e) => {
      if (e instanceof NoSuchObjectError) return undefined;
      throw new LdapUnavailableError(`Search failed: ${errText(e)}`);
    });
    const who = cfg.bind_dn ? `bound as ${cfg.bind_dn}` : "anonymous";
    if (!r) return { url, message: `Connected to ${url} (${who}), but the user base ${cfg.user_base_dn} does not exist` };
    return { url, message: `Connected to ${url} (${who}); user base found` };
  } finally {
    await client.unbind().catch(() => {});
  }
}

/** Looks a user up the way sign-in does, without their password. */
export async function lookupUser(cfg: LdapRuntimeConfig, login: string): Promise<DirectoryUser | undefined> {
  const { client } = await openSearch(cfg);
  try {
    return await searchUser(client, cfg, login);
  } finally {
    await client.unbind().catch(() => {});
  }
}

/**
 * Verifies a login and password against the directory: finds the user with the search account, then binds
 * as the user on a fresh connection. Throws LdapUnavailableError when the directory cannot answer.
 */
export async function authenticate(cfg: LdapRuntimeConfig, login: string, password: string): Promise<AuthResult> {
  // An empty password would be an unauthenticated bind, which many servers accept.
  if (!login || !password) return { status: "invalid_credentials" };
  const user = await lookupUser(cfg, login);
  if (!user) return { status: "not_found" };
  const { client } = await connect(cfg);
  try {
    await client.bind(user.dn, password);
  } catch (e) {
    if (e instanceof InvalidCredentialsError) return { status: "invalid_credentials" };
    throw new LdapUnavailableError(`User bind failed: ${errText(e)}`);
  } finally {
    await client.unbind().catch(() => {});
  }
  return user.allowed ? { status: "ok", user } : { status: "not_allowed", user };
}
