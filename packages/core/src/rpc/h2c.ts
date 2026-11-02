/**
 * Optional HTTP/2 cleartext (h2c) listener that serves the same Connect services with all
 * three protocols, including plain gRPC (which needs HTTP/2 trailers). Enabled with
 * HARNESS_GRPC_PORT. Put TLS in front of it with a reverse proxy in production.
 */
import { createServer, type Http2Server } from "node:http2";
import { connectNodeAdapter } from "@connectrpc/connect-node";
import { registerServices } from "./services.ts";

export function startH2cServer(port: number, host = "0.0.0.0"): Promise<Http2Server> {
  const server = createServer(
    connectNodeAdapter({ routes: (router) => registerServices(router), grpc: true, grpcWeb: true, connect: true }),
  );
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host, () => resolve(server));
  });
}
