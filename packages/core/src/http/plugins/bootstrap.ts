import { definePlugin } from "nitro";
import { bootstrap } from "../../bootstrap.ts";
import { closeDb } from "../../db/index.ts";
import { startH2cServer } from "../../rpc/h2c.ts";

export default definePlugin(async (nitroApp) => {
  // Graceful shutdown (SIGTERM/SIGINT): end the pg pool, or close PGlite so its data dir is left clean.
  nitroApp.hooks.hook("close", () => closeDb());
  await bootstrap().catch((err) => {
    console.error("[harness] bootstrap failed", err);
    process.exit(1);
  });
  const grpcPort = process.env.HARNESS_GRPC_PORT;
  if (grpcPort) {
    await startH2cServer(Number(grpcPort));
    console.log(`[harness] gRPC/Connect h2c listener on :${grpcPort}`);
  }
});
