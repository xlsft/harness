import { betterAuth } from "better-auth";
import { APIError, createAuthMiddleware } from "better-auth/api";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { admin } from "better-auth/plugins/admin";
import { username } from "better-auth/plugins/username";
import { count, eq } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { deriveKey } from "../lib/crypto.ts";
import { newId } from "../lib/ids.ts";
import { getLdapRuntimeConfig } from "./ldap-config.ts";
import { isBreakGlass, ldapSignIn } from "./ldap-sign-in.ts";

const ID_PREFIX: Record<string, string> = {
  user: "usr",
  session: "sess",
  account: "acc",
  verification: "ver",
};

function createAuth() {
  const cfg = config();
  return betterAuth({
    appName: "harness",
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
      }),
    },
    databaseHooks: {
      user: {
        create: {
          // The first account on a fresh server is its administrator.
          before: async (user) => ((await hasUsers()) ? undefined : { data: { ...user, role: "admin" } }),
        },
      },
    },
    // ldapSignIn: POST /sign-in/ldap against the directory configured in AdminService (ldap-sign-in.ts).
    // username: optional unique handle (3-30 chars of [a-zA-Z0-9_.], stored lowercased) usable at
    // /sign-in/username instead of the email; set at sign-up or by an admin (create-user `data.username`).
    plugins: [admin({ defaultRole: "user", adminRoles: ["admin"] }), username(), ldapSignIn()],
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
