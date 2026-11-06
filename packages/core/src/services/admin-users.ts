/**
 * User administration (AdminService): accounts, roles, disabling (the admin plugin's ban), sessions and
 * devices. Transport-agnostic; callers check the admin role (src/rpc/admin.ts). Writes go through Better
 * Auth's internal adapter, as the admin plugin's own endpoints do (they need a cookie session, while the apps
 * call with device access tokens).
 *
 * Last-admin rules: the last active (not disabled) admin can be neither demoted, disabled nor deleted, and
 * nobody can disable or delete themselves.
 */
import { randomBytes } from "node:crypto";
import { and, asc, count, eq, ilike, inArray, isNull, max, or } from "drizzle-orm";
import { getAuth } from "../auth/better-auth.ts";
import { activeAdminCount } from "../auth/admins.ts";
import { LDAP_PROVIDER_ID } from "../auth/ldap-sign-in.ts";
import { normalizeRole, parseRole, type Role } from "../auth/roles.ts";
import { groupNamesOf } from "./groups.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { revokeDevice } from "./devices.ts";

export type { Role };

export interface AdminUserView {
  id: string;
  name: string;
  username: string;
  email: string;
  role: Role;
  source: "local" | "ldap" | "sso" | "scim";
  mfa_enabled: boolean;
  groups: string[];
  disabled: boolean;
  disabled_reason: string;
  created_at: string;
  last_sign_in_at: string;
  last_seen_at: string;
}

type UserRow = typeof schema.user.$inferSelect;

const USERNAME = /^[a-zA-Z0-9_.]{3,30}$/;
const EMAIL = /^[^\s@]+@[^\s@]+$/;
const MIN_PASSWORD = 8;
const MAX_PASSWORD = 128;

/** A random password for "generate and show once" (24 URL-safe characters, 144 bits). */
export function generatePassword(): string {
  return randomBytes(18).toString("base64url");
}

function checkPassword(p: string): void {
  if (p.length < MIN_PASSWORD) throw new ApiError("invalid_request", `Passwords have at least ${MIN_PASSWORD} characters`);
  if (p.length > MAX_PASSWORD) throw new ApiError("invalid_request", `Passwords have at most ${MAX_PASSWORD} characters`);
}

const iso = (d: Date | null | undefined) => (d ? d.toISOString() : "");
const later = (a: Date | null | undefined, b: Date | null | undefined) => (!a ? b : !b ? a : a > b ? a : b);

async function authCtx() {
  return getAuth().$context;
}

async function findUser(id: string): Promise<UserRow> {
  await ensureDb();
  const u = id ? await getDb().query.user.findFirst({ where: eq(schema.user.id, id) }) : undefined;
  if (!u) throw new ApiError("not_found", "User not found");
  return u;
}

async function isLdapUser(userId: string): Promise<boolean> {
  const row = await getDb().query.account.findFirst({
    where: and(eq(schema.account.userId, userId), eq(schema.account.providerId, LDAP_PROVIDER_ID)),
  });
  return !!row;
}

async function isSsoUser(userId: string): Promise<boolean> {
  const rows = await getDb().select({ providerId: schema.account.providerId }).from(schema.account).where(eq(schema.account.userId, userId));
  return rows.some((r) => r.providerId.startsWith("sso:"));
}

/** Account rows for display, with sign-in source and activity. */
async function views(users: UserRow[]): Promise<AdminUserView[]> {
  if (!users.length) return [];
  const db = getDb();
  const ids = users.map((u) => u.id);
  const accounts = await db.select({ userId: schema.account.userId, providerId: schema.account.providerId, password: schema.account.password }).from(schema.account).where(inArray(schema.account.userId, ids));
  const sourceOf = (id: string): AdminUserView["source"] => {
    const mine = accounts.filter((a) => a.userId === id);
    if (mine.some((a) => a.providerId === LDAP_PROVIDER_ID)) return "ldap";
    if (mine.some((a) => a.providerId.startsWith("sso:"))) return "sso";
    if (scim.has(id) && !mine.some((a) => a.password)) return "scim";
    return "local";
  };
  const scim = new Set((await db.select({ userId: schema.scimIdentity.userId }).from(schema.scimIdentity).where(inArray(schema.scimIdentity.userId, ids))).map((r) => r.userId));
  const mfa = new Set((await db.select({ userId: schema.userMfa.userId, enabledAt: schema.userMfa.enabledAt }).from(schema.userMfa).where(inArray(schema.userMfa.userId, ids))).filter((r) => r.enabledAt).map((r) => r.userId));
  const groups = await groupNamesOf(ids);
  const sessions = new Map(
    (
      await db
        .select({ userId: schema.session.userId, signIn: max(schema.session.createdAt), seen: max(schema.session.updatedAt) })
        .from(schema.session)
        .where(inArray(schema.session.userId, ids))
        .groupBy(schema.session.userId)
    ).map((r) => [r.userId, r]),
  );
  const devices = new Map(
    (
      await db
        .select({ userId: schema.device.userId, seen: max(schema.device.lastSeenAt) })
        .from(schema.device)
        .where(and(inArray(schema.device.userId, ids), isNull(schema.device.revokedAt)))
        .groupBy(schema.device.userId)
    ).map((r) => [r.userId, r.seen]),
  );
  return users.map((u) => {
    const s = sessions.get(u.id);
    return {
      id: u.id,
      name: u.name,
      username: u.username ?? "",
      email: u.email,
      role: normalizeRole(u.role),
      source: sourceOf(u.id),
      mfa_enabled: mfa.has(u.id),
      groups: groups.get(u.id) ?? [],
      disabled: !!u.banned,
      disabled_reason: u.banned ? (u.banReason ?? "") : "",
      created_at: iso(u.createdAt),
      last_sign_in_at: iso(s?.signIn),
      last_seen_at: iso(later(s?.seen, devices.get(u.id))),
    };
  });
}

export async function getUserView(id: string): Promise<AdminUserView> {
  return (await views([await findUser(id)]))[0]!;
}

export async function listUsers(query = "", limit = 0): Promise<AdminUserView[]> {
  await ensureDb();
  const q = query.trim();
  const like = `%${q.replace(/[\\%_]/g, (c) => `\\${c}`)}%`;
  const where = q ? or(ilike(schema.user.name, like), ilike(schema.user.email, like), ilike(schema.user.username, like)) : undefined;
  const base = getDb().select().from(schema.user).where(where).orderBy(asc(schema.user.createdAt), asc(schema.user.id));
  return views(limit > 0 ? await base.limit(limit) : await base);
}

export interface CreateUserInput {
  name: string;
  username?: string;
  email: string;
  password?: string;
  generate_password?: boolean;
  role?: Role;
  /** A directory user: no local password, linked to `username` for LDAP sign-in. */
  ldap?: boolean;
}

export async function createUser(input: CreateUserInput): Promise<{ user: AdminUserView; generated_password: string }> {
  await ensureDb();
  const db = getDb();
  const name = input.name.trim();
  const email = input.email.trim().toLowerCase();
  const username = (input.username ?? "").trim().toLowerCase();
  if (!name) throw new ApiError("invalid_request", "Name is required");
  if (!EMAIL.test(email)) throw new ApiError("invalid_request", "A valid email is required");
  if (username && !USERNAME.test(username)) throw new ApiError("invalid_request", "Usernames have 3-30 letters, digits, _ or .");
  if (input.ldap && !username) throw new ApiError("invalid_request", "Directory users need their directory username");
  if (await db.query.user.findFirst({ where: eq(schema.user.email, email) })) throw new ApiError("conflict", "A user with this email already exists");
  if (username && (await db.query.user.findFirst({ where: eq(schema.user.username, username) }))) {
    throw new ApiError("conflict", "A user with this username already exists");
  }
  let password = "";
  let generated = "";
  if (!input.ldap) {
    password = input.generate_password ? (generated = generatePassword()) : (input.password ?? "");
    checkPassword(password);
  }

  const ctx = await authCtx();
  const created = await ctx.internalAdapter.createUser({
    email,
    name,
    role: normalizeRole(input.role),
    ...(username ? { username, displayUsername: input.username!.trim() } : {}),
  }, { method: "admin" });
  if (input.ldap) await ctx.internalAdapter.createAccount({ userId: created.id, providerId: LDAP_PROVIDER_ID, accountId: username });
  else await ctx.internalAdapter.createAccount({ userId: created.id, providerId: "credential", accountId: created.id, password: await ctx.password.hash(password) });
  return { user: await getUserView(created.id), generated_password: generated };
}

export async function resetPassword(userId: string, opts: { password?: string; generate?: boolean; revoke_sessions?: boolean }): Promise<{ generated_password: string }> {
  const u = await findUser(userId);
  if (await isLdapUser(u.id)) throw new ApiError("failed_precondition", "This user signs in through LDAP; their password is managed in the directory");
  if (await isSsoUser(u.id)) throw new ApiError("failed_precondition", "This user signs in through SSO; their password is managed at the identity provider");
  const generated = opts.generate ? generatePassword() : "";
  const password = generated || opts.password || "";
  checkPassword(password);
  const ctx = await authCtx();
  const hash = await ctx.password.hash(password);
  if (await ctx.internalAdapter.findCredentialAccount(u.id)) await ctx.internalAdapter.updatePassword(u.id, hash);
  else await ctx.internalAdapter.createAccount({ userId: u.id, providerId: "credential", accountId: u.id, password: hash });
  if (opts.revoke_sessions) await revokeUserSessions(u.id);
  return { generated_password: generated };
}

export async function setRole(userId: string, role: Role): Promise<AdminUserView> {
  const u = await findUser(userId);
  parseRole(role);
  if (u.role === "admin" && role !== "admin" && !u.banned && (await activeAdminCount(u.id)) === 0) {
    throw new ApiError("failed_precondition", "This is the last admin; make someone else an admin first");
  }
  if ((u.role ?? "user") !== role) await getDb().update(schema.user).set({ role, updatedAt: new Date() }).where(eq(schema.user.id, u.id));
  return getUserView(u.id);
}

export async function setDisabled(userId: string, disabled: boolean, reason: string, selfId: string): Promise<AdminUserView> {
  const u = await findUser(userId);
  if (disabled) {
    if (u.id === selfId) throw new ApiError("failed_precondition", "You cannot disable yourself");
    if (u.role === "admin" && !u.banned && (await activeAdminCount(u.id)) === 0) throw new ApiError("failed_precondition", "This is the last admin");
    const ctx = await authCtx();
    await ctx.internalAdapter.updateUser(u.id, { banned: true, banReason: reason.trim() || "Disabled by an administrator", banExpires: null, updatedAt: new Date() });
    await revokeUserSessions(u.id);
  } else if (u.banned) {
    const ctx = await authCtx();
    await ctx.internalAdapter.updateUser(u.id, { banned: false, banReason: null, banExpires: null, updatedAt: new Date() });
  }
  return getUserView(u.id);
}

export async function deleteUser(userId: string, selfId: string): Promise<void> {
  const u = await findUser(userId);
  if (u.id === selfId) throw new ApiError("failed_precondition", "You cannot delete yourself");
  if (u.role === "admin" && !u.banned && (await activeAdminCount(u.id)) === 0) throw new ApiError("failed_precondition", "This is the last admin");
  const ctx = await authCtx();
  await ctx.internalAdapter.deleteUserSessions(u.id);
  // Devices, tokens, personal providers and the rest cascade with the user row.
  await ctx.internalAdapter.deleteUser(u.id);
}

export interface UserSessions {
  devices: (typeof schema.device.$inferSelect)[];
  web_sessions: number;
}

export async function listUserSessions(userId: string): Promise<UserSessions> {
  const u = await findUser(userId);
  const db = getDb();
  const devices = await db.query.device.findMany({ where: and(eq(schema.device.userId, u.id), isNull(schema.device.revokedAt)) });
  const seen = (d: (typeof devices)[number]) => (d.lastSeenAt ?? d.createdAt).getTime();
  const [row] = await db.select({ n: count() }).from(schema.session).where(eq(schema.session.userId, u.id));
  return { devices: devices.sort((a, b) => seen(b) - seen(a)), web_sessions: row?.n ?? 0 };
}

/** Revokes one device of the user, or every device and web session. */
export async function revokeUserSessions(userId: string, deviceId = ""): Promise<{ devices: number; web_sessions: number }> {
  const u = await findUser(userId);
  const db = getDb();
  if (deviceId) {
    const d = await db.query.device.findFirst({ where: and(eq(schema.device.id, deviceId), eq(schema.device.userId, u.id)) });
    if (!d) throw new ApiError("not_found", "Device not found");
    if (d.revokedAt) return { devices: 0, web_sessions: 0 };
    await revokeDevice(d.id);
    return { devices: 1, web_sessions: 0 };
  }
  const active = await db.query.device.findMany({ where: and(eq(schema.device.userId, u.id), isNull(schema.device.revokedAt)) });
  for (const d of active) await revokeDevice(d.id);
  const [row] = await db.select({ n: count() }).from(schema.session).where(eq(schema.session.userId, u.id));
  await (await authCtx()).internalAdapter.deleteUserSessions(u.id);
  return { devices: active.length, web_sessions: row?.n ?? 0 };
}

/** Model count per provider (for the admin view of a user's providers). */
export async function modelCounts(providerIds: string[]): Promise<Map<string, number>> {
  if (!providerIds.length) return new Map();
  const rows = await getDb()
    .select({ id: schema.model.providerId, n: count() })
    .from(schema.model)
    .where(inArray(schema.model.providerId, providerIds))
    .groupBy(schema.model.providerId);
  return new Map(rows.map((r) => [r.id ?? "", r.n]));
}
