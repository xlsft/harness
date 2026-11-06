import { definePlugin } from "nitro";
import { bootstrap } from "../../bootstrap.ts";
import { closeDb } from "../../db/index.ts";
import { startH2cServer } from "../../rpc/h2c.ts";
import { migrateAvatars } from "../../services/account.ts";
import { startModelPriceRefresh } from "../../services/model-prices.ts";
import { startObjectSweep } from "../../services/objects.ts";
import { flushForwarders, kickForwarders } from "../../audit/forward.ts";
import { purgeAudit } from "../../audit/log.ts";
import { startTelemetry, stopTelemetry } from "../../telemetry/otel.ts";

export default definePlugin(async (nitroApp) => {
  // Graceful shutdown (SIGTERM/SIGINT): end the pg pool, or close PGlite so its data dir is left clean.
  startTelemetry();
  nitroApp.hooks.hook("close", async () => {
    await flushForwarders().catch(() => {});
    await stopTelemetry().catch(() => {});
    await closeDb();
  });
  await bootstrap().catch((err) => {
    console.error("[harness] bootstrap failed", err);
    process.exit(1);
  });
  // Moving avatars into the store and the sweep run in the background: a store that is still starting delays neither.
  startObjectSweep(migrateAvatars);
  startModelPriceRefresh();
  // Events written while a SIEM was unreachable or the server was down go out now; old ones past the retention go.
  kickForwarders();
  void purgeAudit().catch(() => {});
  setInterval(() => void purgeAudit().catch(() => {}), 24 * 3_600_000).unref();
  const grpcPort = process.env.HARNESS_GRPC_PORT;
  if (grpcPort) {
    await startH2cServer(Number(grpcPort));
    console.log(`[harness] gRPC/Connect h2c listener on :${grpcPort}`);
  }
});
