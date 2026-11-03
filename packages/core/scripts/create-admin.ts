// Creates or promotes an admin user.
// Usage: pnpm admin:create <email> <password> [name] [username]
// Without a username the email's local part is used when it is a valid, free one.
// Uses the same env as the server (HARNESS_DATA_DIR, HARNESS_SECRET, ...).
import { ensureAdmin } from "../src/auth/better-auth.ts";
import { ensureDb } from "../src/db/index.ts";

const [email, password, name, username] = process.argv.slice(2);
if (!email || !password) {
  console.error("usage: pnpm admin:create <email> <password> [name] [username]");
  process.exit(2);
}
await ensureDb();
console.log(`${email}: ${await ensureAdmin(email, password, name ?? "Admin", username)}`);
