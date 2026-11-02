/**
 * Fetch-API entry point for the Connect services (used by the Nitro route and by tests).
 *
 * Over the fetch pipeline (HTTP/1.1 or HTTP/2 without trailers) the Connect and gRPC-Web
 * protocols work. Plain gRPC needs HTTP/2 trailers, which the fetch Response API cannot
 * express, so it is served by the separate h2c listener in src/rpc/h2c.ts.
 */
import { createConnectRouter } from "@connectrpc/connect";
import { createFetchHandler } from "@connectrpc/connect/protocol";
import { ensureDb } from "../db/index.ts";
import { registerServices } from "./services.ts";

type FetchHandler = (req: Request) => Promise<Response>;

let table: Map<string, FetchHandler> | undefined;

function handlers(): Map<string, FetchHandler> {
  if (!table) {
    const router = registerServices(createConnectRouter({ grpc: false, grpcWeb: true, connect: true }));
    table = new Map(router.handlers.map((h) => [h.requestPath, createFetchHandler(h)]));
  }
  return table;
}

/** Returns a Response for a Connect/gRPC-Web request, or undefined if the path is not an RPC method. */
export async function handleRpc(req: Request): Promise<Response | undefined> {
  const h = handlers().get(new URL(req.url).pathname);
  if (!h) return undefined;
  await ensureDb();
  return h(req);
}
