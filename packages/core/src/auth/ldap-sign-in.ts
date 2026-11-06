/**
 * Sign-in against LDAP: POST /api/auth/sign-in/ldap {login, password} (a Better Auth plugin endpoint, so the
 * result is a normal Better Auth session cookie and everything downstream, the device flow included, is
 * unchanged). The /device page posts there while LDAP is enabled.
 *
 * Order (LdapSettings.local_login):
 *   fallback     the directory first; when it refuses (or is unreachable), a local account's password;
 *   break_glass  local passwords only for the listed logins (tried first, so they work while the directory
 *                is down); everyone else signs in through the directory only. /sign-in/email and
 *                /sign-in/username refuse everyone else too (see better-auth.ts).
 * A successful bind creates (auto_create_users) or updates the local account: source LDAP (an "ldap" account
 * row keyed by the directory username), no local password, role from admin_group_dn when that is set.
 * Failed attempts are limited per login and per client address.
 */
import { APIError, createAuthEndpoint } from "better-auth/api";
import type { BetterAuthPlugin } from "better-auth";
import { setSessionCookie } from "better-auth/cookies";
import { and, eq } from "drizzle-orm";
import * as z from "zod";
import { getDb, schema } from "../db/index.ts";
import { syncSourceGroups } from "../services/groups.ts";
import { activeAdminCount } from "./admins.ts";
import type { Role } from "./roles.ts";
import { getLdapRuntimeConfig, type LdapRuntimeConfig } from "./ldap-config.ts";
import { authenticate, LdapUnavailableError, type DirectoryUser } from "./ldap.ts";

/** Internal header with the client address, set by the /api/auth route (any client-sent value is replaced). */
export const AUTH_PEER_IP_HEADER = "x-harness-auth-peer-ip";

export const LDAP_PROVIDER_ID = "ldap";

// --- failed attempt limiting ----------------------------------------------------------------------------

const WINDOW_MS = 15 * 60_000;

/** Sliding-window count of failed sign-ins per key. In memory, per process. */
class FailureLimiter {
  private failures = new Map<string, number[]>();

  constructor(private readonly now: () => number = Date.now) {}

  private recent(key: string): number[] {
    const cutoff = this.now() - WINDOW_MS;
    const list = (this.failures.get(key) ?? []).filter((t) => t > cutoff);
    if (list.length) this.failures.set(key, list);
    else this.failures.delete(key);
    return list;
  }

  /** Milliseconds until `key` may try again (0: now). */
  retryAfter(key: string, limit: number): number {
    if (limit <= 0) return 0;
    const list = this.recent(key);
    return list.length >= limit ? list[list.length - limit]! + WINDOW_MS - this.now() : 0;
  }

  fail(key: string): void {
    if (this.failures.size > 10_000) for (const k of [...this.failures.keys()]) this.recent(k);
    this.failures.set(key, [...this.recent(key), this.now()]);
  }

  reset(): void {
    this.failures.clear();
  }
}

const limiter = new FailureLimiter();
export const loginLimiter = limiter;

/** HARNESS_LOGIN_FAILURES_PER_15MIN (default 10) failed sign-ins per login; five times that per address. */
export function loginLimit(): number {
  const n = Number.parseInt(process.env.HARNESS_LOGIN_FAILURES_PER_15MIN ?? "", 10);
  return Number.isFinite(n) && n >= 0 ? n : 10;
}

/** For tests. */
export function resetLoginFailures(): void {
  limiter.reset();
}

// --- accounts -------------------------------------------------------------------------------------------

export function isBreakGlass(cfg: Pick<LdapRuntimeConfig, "break_glass_logins">, login: string): boolean {
  return cfg.break_glass_logins.includes(login.trim().toLowerCase());
}

const USERNAME = /^[a-zA-Z0-9_.]{3,30}$/;

type UserRow = typeof schema.user.$inferSelect;

/** Minimal slice of Better Auth's internal adapter used to create accounts (runs its database hooks). */
export interface AccountCreator {
  createUser(user: Record<string, unknown>, source: { method: string }): Promise<{ id: string }>;
  createAccount(account: { userId: string; providerId: string; accountId: string; password?: string | null }): Promise<unknown>;
}

/** The local user linked to a directory username, if any. */
export async function findLdapUser(username: string): Promise<UserRow | undefined> {
  const [row] = await getDb()
    .select({ user: schema.user })
    .from(schema.account)
    .innerJoin(schema.user, eq(schema.user.id, schema.account.userId))
    .where(and(eq(schema.account.providerId, LDAP_PROVIDER_ID), eq(schema.account.accountId, username.toLowerCase())))
    .limit(1);
  return row?.user;
}

/** The role the directory gives this user; undefined when roles are managed in Users. */
function directoryRole(du: DirectoryUser, cfg: LdapRuntimeConfig): Role | undefined {
  if (cfg.admin_group_dn && du.admin) return "admin";
  if (cfg.role_mappings.length) return du.mapped_role;
  return cfg.admin_group_dn ? "user" : undefined;
}

/**
 * Creates or updates the local account of a directory user who just authenticated. Refuses when auto-creation
 * is off and no account is linked, or when an unlinked local account already has the email or username (it is
 * never taken over silently).
 */
export async function provisionLdapUser(du: DirectoryUser, cfg: LdapRuntimeConfig, accounts: AccountCreator): Promise<UserRow> {
  const db = getDb();
  const handle = du.username.toLowerCase();
  const email = (du.email || `${handle.replace(/[^a-z0-9._-]/g, "_")}@ldap.invalid`).toLowerCase();
  const name = du.name || du.username;
  const existing = await findLdapUser(handle);

  if (existing) {
    const set: Partial<typeof schema.user.$inferInsert> = {};
    if (name !== existing.name) set.name = name;
    if (email !== existing.email) {
      const taken = await db.query.user.findFirst({ where: eq(schema.user.email, email) });
      if (!taken) set.email = email;
    }
    const role = directoryRole(du, cfg);
    // The directory never demotes the last admin.
    if (role && role !== existing.role && (role === "admin" || existing.role !== "admin" || (await activeAdminCount(existing.id)) > 0)) set.role = role;
    await syncSourceGroups(existing.id, "ldap", du.groups);
    if (!Object.keys(set).length) return existing;
    const [row] = await db.update(schema.user).set({ ...set, updatedAt: new Date() }).where(eq(schema.user.id, existing.id)).returning();
    return row!;
  }

  if (!cfg.auto_create_users) throw new APIError("FORBIDDEN", { message: "There is no account for this directory user yet. Ask an administrator to add you." });
  const clash = (await db.query.user.findFirst({ where: eq(schema.user.email, email) })) ?? (await db.query.user.findFirst({ where: eq(schema.user.username, handle) }));
  if (clash) {
    throw new APIError("FORBIDDEN", { message: "A local account with this email or username already exists. Ask an administrator to link or remove it." });
  }
  const usable = USERNAME.test(handle);
  const created = await accounts.createUser({
    email,
    name,
    emailVerified: !!du.email,
    role: directoryRole(du, cfg),
    ...(usable ? { username: handle, displayUsername: du.username } : {}),
  }, { method: "ldap" });
  await accounts.createAccount({ userId: created.id, providerId: LDAP_PROVIDER_ID, accountId: handle });
  await syncSourceGroups(created.id, "ldap", du.groups);
  return (await db.query.user.findFirst({ where: eq(schema.user.id, created.id) }))!;
}

// --- endpoint -------------------------------------------------------------------------------------------

const bodySchema = z.object({
  login: z.string().max(320),
  password: z.string().max(1024),
  rememberMe: z.boolean().optional(),
});

/** The slice of an endpoint context that a local password check needs. */
interface PasswordCtx {
  context: {
    adapter: { findOne(q: { model: string; where: { field: string; value: string }[] }): Promise<unknown> };
    internalAdapter: { findCredentialAccount(userId: string): Promise<{ password?: string | null } | null> };
    password: { hash(password: string): Promise<string>; verify(d: { hash: string; password: string }): Promise<boolean> };
  };
}

/** A local account's password check (no session), with the same timing for unknown users. */
async function verifyLocal(ctx: PasswordCtx, login: string, password: string): Promise<UserRow | undefined> {
  const field = login.includes("@") ? "email" : "username";
  const user = (await ctx.context.adapter.findOne({ model: "user", where: [{ field, value: login.toLowerCase() }] })) as UserRow | null;
  const account = user ? await ctx.context.internalAdapter.findCredentialAccount(user.id) : null;
  if (!account?.password) {
    await ctx.context.password.hash(password);
    return undefined;
  }
  return (await ctx.context.password.verify({ hash: account.password, password })) ? user! : undefined;
}

export function tooManyAttempts(ms: number): APIError {
  return new APIError("TOO_MANY_REQUESTS", { message: "Too many failed sign-in attempts; try again later." }, { "retry-after": String(Math.ceil(ms / 1000)) });
}

export function ldapSignIn(): BetterAuthPlugin {
  return {
    id: "harness-ldap",
    endpoints: {
      signInLdap: createAuthEndpoint("/sign-in/ldap", { method: "POST", body: bodySchema }, async (ctx) => {
        const cfg = await getLdapRuntimeConfig();
        if (!cfg.enabled) throw new APIError("NOT_FOUND", { message: "LDAP sign-in is not enabled" });
        const login = ctx.body.login.trim();
        const { password } = ctx.body;
        const ipKey = `ip:${ctx.request?.headers.get(AUTH_PEER_IP_HEADER) || "unknown"}`;
        const loginKey = `login:${login.toLowerCase()}`;
        const wait = Math.max(limiter.retryAfter(loginKey, loginLimit()), limiter.retryAfter(ipKey, loginLimit() * 5));
        if (wait > 0) throw tooManyAttempts(wait);
        const failed = (message = "Invalid login or password", status: "UNAUTHORIZED" | "FORBIDDEN" = "UNAUTHORIZED") => {
          limiter.fail(loginKey);
          limiter.fail(ipKey);
          return new APIError(status, { message });
        };
        if (!login || !password) throw failed();

        let user = cfg.local_login === "break_glass" && isBreakGlass(cfg, login) ? await verifyLocal(ctx, login, password) : undefined;
        let unavailable = false;
        if (!user) {
          try {
            const r = await authenticate(cfg, login, password);
            if (r.status === "ok") user = await provisionLdapUser(r.user, cfg, ctx.context.internalAdapter as unknown as AccountCreator);
            else if (r.status === "not_allowed") throw failed("Your directory account is not allowed to sign in here.", "FORBIDDEN");
          } catch (e) {
            if (!(e instanceof LdapUnavailableError)) throw e;
            unavailable = true;
            // The reason names servers and DNs, never the password.
            ctx.context.logger.warn(`LDAP sign-in: directory unavailable: ${e.message}`);
          }
          if (!user && cfg.local_login === "fallback") user = await verifyLocal(ctx, login, password);
        }
        if (!user) {
          if (unavailable) throw new APIError("SERVICE_UNAVAILABLE", { message: "The directory server is unavailable. Try again later." });
          throw failed();
        }

        const session = await ctx.context.internalAdapter.createSession(user.id, ctx.body.rememberMe === false);
        if (!session) throw new APIError("INTERNAL_SERVER_ERROR", { message: "Failed to create a session" });
        await setSessionCookie(ctx, { session, user }, ctx.body.rememberMe === false);
        return ctx.json({
          token: session.token,
          user: { id: user.id, email: user.email, name: user.name, username: user.username, role: user.role },
        });
      }),
    },
  };
}
