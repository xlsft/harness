import { ensureAdmin } from "./auth/better-auth.ts";
import { ensureDb } from "./db/index.ts";
import { config } from "./env.ts";
import { applyModelPrices } from "./services/model-prices.ts";

/** Startup: migrations + prices from what is stored (no network) + optional admin bootstrap from HARNESS_ADMIN_EMAIL / HARNESS_ADMIN_PASSWORD (/ HARNESS_ADMIN_USERNAME). */
export async function bootstrap(): Promise<void> {
  await ensureDb();
  // Models added before their preset or the catalog had prices. A failure here costs a price, not the server.
  await applyModelPrices().catch((err: unknown) => console.error("[harness] could not apply model prices", err));
  const cfg = config();
  if (cfg.adminEmail && cfg.adminPassword) {
    const r = await ensureAdmin(cfg.adminEmail, cfg.adminPassword, undefined, cfg.adminUsername);
    if (r !== "exists") console.log(`[harness] admin ${cfg.adminEmail}: ${r}`);
  }
}
