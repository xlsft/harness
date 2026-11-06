import { ensureAdmin } from "./auth/better-auth.ts";
import { ensureDb } from "./db/index.ts";
import { config } from "./env.ts";
import { fillPresetPrices } from "./services/models.ts";

/** Startup: migrations + prices from presets + optional admin bootstrap from HARNESS_ADMIN_EMAIL / HARNESS_ADMIN_PASSWORD (/ HARNESS_ADMIN_USERNAME). */
export async function bootstrap(): Promise<void> {
  await ensureDb();
  // Models added before their preset had prices. A failure here costs a price, not the server.
  await fillPresetPrices().catch((err: unknown) => console.error("[harness] could not fill preset prices", err));
  const cfg = config();
  if (cfg.adminEmail && cfg.adminPassword) {
    const r = await ensureAdmin(cfg.adminEmail, cfg.adminPassword, undefined, cfg.adminUsername);
    if (r !== "exists") console.log(`[harness] admin ${cfg.adminEmail}: ${r}`);
  }
}
