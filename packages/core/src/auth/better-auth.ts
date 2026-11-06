import { betterAuth } from "better-auth";
import { APIError, createAuthMiddleware, getSessionFromCtx } from "better-auth/api";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { admin } from "better-auth/plugins/admin";
import { username } from "better-auth/plugins/username";
import { count, eq } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { deriveKey } from "../lib/crypto.ts";
import { newId } from "../lib/ids.ts";
import { getLdapRuntimeConfig } from "./ldap-config.ts";
import { AUTH_PEER_IP_HEADER, isBreakGlass, ldapSignIn } from "./ldap-sign-in.ts";
import { record } from "../audit/log.ts";
import { countMetric } from "../telemetry/metrics.ts";
import { mfaEndpoints } from "./mfa-endpoints.ts";
import { sessionOwesFactor } from "./mfa.ts";
import { getSsoSettings } from "./sso-config.ts";
import { ssoComplete } from "./sso-sign-in.ts";

const ID_PREFIX: Record<string, string> = {
  user: "usr",
  session: "sess",
  account: "acc",
  verification: "ver",
};

/** What a session that still owes its second factor may reach. */
const PENDING_ALLOWED = ["/mfa/", "/sign-out", "/get-session", "/sign-in/", "/sign-up/", "/sso/complete", "/ok", "/error"];

const SIGN_IN_METHOD: Record<string, string> = {
  "/sign-in/email": "password",
  "/sign-in/username": "password",
  "/sign-in/ldap": "ldap",
  "/sso/complete": "sso",
  "/sign-up/email": "sign_up",
};

function createAuth() {
  const cfg = config();
  return betterAuth({
    appName: "xlsft`s harness",
    baseURL: cfg.publicUrl,
    basePath: "/api/auth",
    secret: deriveKey("better-auth").toString("base64url"),
    trustedOrigins: [cfg.publicUrl],
    database: drizzleAdapter(getDb(), {
      provider: "pg",
      schema: {
        user: schema.user,
        session: schema.session,
        account: schema.account,
        verification: schema.verification,
      },
    }),
    emailAndPassword: {
      enabled: true,
      // Open sign-up is decided per request (hooks.before): the first account may always sign up, later
      // ones only with HARNESS_ALLOW_SIGNUP=true; otherwise an admin creates users.
      disableSignUp: false,
      minPasswordLength: 8,
    },
    session: {
      additionalFields: {
        mfaPending: { type: "boolean", required: false, defaultValue: false, input: false },
      },
    },
    hooks: {
      before: createAuthMiddleware(async (ctx) => {
        // Covers every sign-up method (today only /sign-up/email, which also takes a username).
        if (ctx.path.startsWith("/sign-up/") && !cfg.allowSignup && (await hasUsers())) {
          throw new APIError("FORBIDDEN", { message: "Sign-up is closed. Ask an administrator to create your account." });
        }
        // With LDAP in break-glass mode only the listed logins keep local password sign-in.
        if (ctx.path === "/sign-in/email" || ctx.path === "/sign-in/username") {
          const ldap = await getLdapRuntimeConfig();
          const body = (ctx.body ?? {}) as { email?: unknown; username?: unknown };
          const login = String(body.email ?? body.username ?? "");
          if (ldap.enabled && ldap.local_login === "break_glass" && !isBreakGlass(ldap, login)) {
            throw new APIError("FORBIDDEN", { message: "Sign in with your directory account." });
          }
        }
        // With SSO required, only the break-glass logins keep passwords (local or LDAP).
        if (ctx.path === "/sign-in/email" || ctx.path === "/sign-in/username" || ctx.path === "/sign-in/ldap") {
          const sso = await getSsoSettings();
          if (sso.require_sso) {
            const body = (ctx.body ?? {}) as { email?: unknown; username?: unknown; login?: unknown };
            const login = String(body.email ?? body.username ?? body.login ?? "").trim().toLowerCase();
            if (!sso.break_glass_logins.includes(login)) throw new APIError("FORBIDDEN", { message: "Sign in with your organization's identity provider." });
          }
        }
        // Afterwards the session is gone, and with it who this was.
        if (ctx.path === "/sign-out") {
          const s = await getSessionFromCtx(ctx);
          if (s) await record({ action: "auth.sign_out", actor: { id: s.user.id, label: s.user.email, ip: ctx.request?.headers.get(AUTH_PEER_IP_HEADER) ?? "" } });
        }
        // A session that owes its second factor is good for nothing else.
        if (ctx.request?.headers.get("cookie") && !PENDING_ALLOWED.some((p) => ctx.path.startsWith(p))) {
          const s = (await getSessionFromCtx(ctx)) as { session?: { mfaPending?: boolean } } | null;
          if (s?.session?.mfaPending) throw new APIError("FORBIDDEN", { message: "Finish two-factor sign-in first." });
        }
      }),
      // Sign-ins, failed or not, go to the audit log. Only the login is recorded, never what came with it.
      after: createAuthMiddleware(async (ctx) => {
        const method = SIGN_IN_METHOD[ctx.path];
        if (!method) return;
        const ip = ctx.request?.headers.get(AUTH_PEER_IP_HEADER) ?? "";
        const fresh = ctx.context.newSession;
        if (fresh) {
          const pending = (fresh.session as { mfaPending?: boolean }).mfaPending === true;
          countMetric("harness.auth.sign_ins", 1, { method, outcome: "success" });
          await record({
            action: method === "sign_up" ? "auth.sign_up" : "auth.sign_in",
            actor: { id: fresh.user.id, label: fresh.user.email, ip },
            target: { type: "user", id: fresh.user.id, label: fresh.user.email },
            details: { method, mfa_pending: pending },
          });
          return;
        }
        const returned = ctx.context.returned;
        if (!(returned instanceof APIError) || ctx.path === "/sso/complete") return;
        const body = (ctx.body ?? {}) as { email?: unknown; username?: unknown; login?: unknown };
        const login = String(body.email ?? body.username ?? body.login ?? "").slice(0, 200);
        countMetric("harness.auth.sign_ins", 1, { method, outcome: "failure" });
        await record({
          action: "auth.sign_in_failed",
          outcome: "failure",
          actor: { label: login, ip },
          details: { method, reason: returned.message, status: returned.statusCode },
        });
      }),
    },
    databaseHooks: {
      user: {
        create: {
          // The first account on a fresh server is its administrator.
          before: async (user) => ((await hasUsers()) ? undefined : { data: { ...user, role: "admin" } }),
        },
      },
      session: {
        create: {
          // Every way of signing in ends here (auth/mfa.ts).
          before: async (session, ctx) => {
            if (!(await sessionOwesFactor(session.userId, ctx?.path === "/sso/complete"))) return undefined;
            return { data: { ...session, mfaPending: true } };
          },
        },
      },
    },
    // ldapSignIn: POST /sign-in/ldap against the directory configured in AdminService (ldap-sign-in.ts).
    // username: optional unique handle (3-30 chars of [a-zA-Z0-9_.], stored lowercased) usable at
    // /sign-in/username instead of the email; set at sign-up or by an admin (create-user `data.username`).
    plugins: [admin({ defaultRole: "user", adminRoles: ["admin"] }), username(), ldapSignIn(), mfaEndpoints(), ssoComplete()],
    advanced: {
      database: {
        generateId: ({ model }) => newId(ID_PREFIX[model] ?? model.slice(0, 4)),
      },
    },
    telemetry: { enabled: false },
  });
}

export type Auth = ReturnType<typeof createAuth>;

/** Whether any account exists yet (a fresh server lets the first visitor create the admin account). */
export async function hasUsers(): Promise<boolean> {
  const [row] = await getDb().select({ n: count() }).from(schema.user);
  return (row?.n ?? 0) > 0;
}

let instance: Auth | undefined;

export function getAuth(): Auth {
  instance ??= createAuth();
  return instance;
}

export function resetAuth(): void {
  instance = undefined;
}

/**
 * Creates (or promotes) an admin user. Used by the startup bootstrap from
 * HARNESS_ADMIN_EMAIL / HARNESS_ADMIN_PASSWORD and by scripts/create-admin.ts.
 * Returns "created" | "promoted" | "exists".
 */
export async function ensureAdmin(email: string, password: string, name = "Admin", username?: string): Promise<string> {
  const db = getDb();
  const existing = await db.query.user.findFirst({ where: eq(schema.user.email, email.toLowerCase()) });
  // The admin gets a username to sign in with: the one given, else the email's local part when it is a valid one.
  const handle = await freeUsername(username ?? email.split("@")[0]!, existing?.id);
  if (existing) {
    const set: Partial<typeof schema.user.$inferInsert> = {};
    if (existing.role !== "admin") set.role = "admin";
    if (!existing.username && handle) Object.assign(set, { username: handle, displayUsername: handle });
    if (!Object.keys(set).length) return "exists";
    await db.update(schema.user).set({ ...set, updatedAt: new Date() }).where(eq(schema.user.id, existing.id));
    return set.role ? "promoted" : "updated";
  }
  await getAuth().api.createUser({ body: { email, password, name, role: "admin", data: handle ? { username: handle, displayUsername: handle } : {} } });
  return "created";
}

const USERNAME = /^[a-zA-Z0-9_.]{3,30}$/;

/** `wanted` lowercased when it is a valid username nobody else has, else undefined. */
async function freeUsername(wanted: string, self?: string): Promise<string | undefined> {
  const name = wanted.toLowerCase();
  if (!USERNAME.test(name)) return undefined;
  const taken = await getDb().query.user.findFirst({ where: eq(schema.user.username, name) });
  return !taken || taken.id === self ? name : undefined;
}
