// Creates or promotes an admin user.
// Usage: pnpm admin:create <email> <password> [name]
// Uses the same env as the server (HARNESS_DATA_DIR, HARNESS_SECRET, ...).
import { ensureAdmin } from "../src/auth/better-auth.ts";
import { ensureDb } from "../src/db/index.ts";

const [email, password, name] = process.argv.slice(2);
if (!email || !password) {
  console.error("usage: pnpm admin:create <email> <password> [name]");
  process.exit(2);
}
await ensureDb();
console.log(`${email}: ${await ensureAdmin(email, password, name ?? "Admin")}`);
