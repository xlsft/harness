/**
 * GET and HEAD /api/objects/:sha256: the bytes of one of the caller's objects, with ranges (usable by an <img> or a
 * <video>), for a device token or a web session. Headers: servedHeaders in src/services/objects.ts.
 */
import { ApiError } from "../../lib/errors.ts";
import { serveObject } from "../../services/objects.ts";
import { api, param, requirePrincipal } from "../util.ts";

export default api(async (event) => {
  const method = event.req.method;
  if (method !== "GET" && method !== "HEAD") throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  const p = await requirePrincipal(event);
  return serveObject(p, param(event, "sha256"), method, event.req.headers);
});
