import { definePlugin } from "nitro";
import { bootstrap } from "../../bootstrap.ts";
import { startH2cServer } from "../../rpc/h2c.ts";

export default definePlugin(async () => {
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
