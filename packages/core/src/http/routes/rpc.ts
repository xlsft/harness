import { defineHandler } from "h3";
import { clientIp } from "../../rpc/context.ts";
import { handleRpc } from "../../rpc/fetch.ts";

/** Mounts the Connect services (Connect + gRPC-Web protocols) at /<package>.<Service>/<Method>. */
export default defineHandler(async (event) => {
  const ip = clientIp((event.req as { ip?: string }).ip, event.req.headers.get("x-forwarded-for"));
  const res = await handleRpc(event.req, ip);
  return res ?? new Response(JSON.stringify({ code: "unimplemented", message: "Unknown method" }), {
    status: 404,
    headers: { "content-type": "application/json" },
  });
});
