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
    // username: optional unique handle (3-30 chars of [a-zA-Z0-9_.], stored lowercased) usable at
    // /sign-in/username instead of the email; set at sign-up or by an admin (create-user `data.username`).
    plugins: [admin({ defaultRole: "user", adminRoles: ["admin"] }), username()],
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
export async function ensureAdmin(email: string, password: string, name = "Admin"): Promise<string> {
  const db = getDb();
  const existing = await db.query.user.findFirst({ where: eq(schema.user.email, email.toLowerCase()) });
  if (existing) {
    if (existing.role !== "admin") {
      await db.update(schema.user).set({ role: "admin", updatedAt: new Date() }).where(eq(schema.user.id, existing.id));
      return "promoted";
    }
    return "exists";
  }
  await getAuth().api.createUser({ body: { email, password, name, role: "admin" } });
  return "created";
}
