import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { admin } from "better-auth/plugins/admin";
import { eq } from "drizzle-orm";
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
      provider: "sqlite",
      schema: {
        user: schema.user,
        session: schema.session,
        account: schema.account,
        verification: schema.verification,
      },
    }),
    emailAndPassword: {
      enabled: true,
      // Users are created by an admin unless HARNESS_ALLOW_SIGNUP=true.
      disableSignUp: !cfg.allowSignup,
      minPasswordLength: 8,
    },
    plugins: [admin({ defaultRole: "user", adminRoles: ["admin"] })],
    advanced: {
      database: {
        generateId: ({ model }) => newId(ID_PREFIX[model] ?? model.slice(0, 4)),
      },
    },
    telemetry: { enabled: false },
  });
}

export type Auth = ReturnType<typeof createAuth>;

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
