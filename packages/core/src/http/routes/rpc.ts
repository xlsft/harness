import { defineHandler } from "h3";
import { handleRpc } from "../../rpc/fetch.ts";

/** Mounts the Connect services (Connect + gRPC-Web protocols) at /<package>.<Service>/<Method>. */
export default defineHandler(async (event) => {
  const res = await handleRpc(event.req);
  return res ?? new Response(JSON.stringify({ code: "unimplemented", message: "Unknown method" }), {
    status: 404,
    headers: { "content-type": "application/json" },
  });
});
