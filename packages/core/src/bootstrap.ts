import { ensureAdmin } from "./auth/better-auth.ts";
import { ensureDb } from "./db/index.ts";
import { config } from "./env.ts";

/** Startup: migrations + optional admin bootstrap from HARNESS_ADMIN_EMAIL / HARNESS_ADMIN_PASSWORD. */
export async function bootstrap(): Promise<void> {
  await ensureDb();
  const cfg = config();
  if (cfg.adminEmail && cfg.adminPassword) {
    const r = await ensureAdmin(cfg.adminEmail, cfg.adminPassword);
    if (r !== "exists") console.log(`[harness] admin ${cfg.adminEmail}: ${r}`);
  }
}
