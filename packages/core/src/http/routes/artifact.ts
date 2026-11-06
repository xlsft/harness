/**
 * GET and HEAD /api/artifacts/:id/content: the bytes of an artifact the caller may see, under the sandbox policy of
 * src/services/artifacts.ts. ?version=N picks a version, ?download=1 asks for an attachment.
 */
import { ApiError } from "../../lib/errors.ts";
import { serveArtifact } from "../../services/artifacts.ts";
import { api, param, requirePrincipal } from "../util.ts";

export default api(async (event) => {
  const method = event.req.method;
  if (method !== "GET" && method !== "HEAD") throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  const p = await requirePrincipal(event);
  const q = event.url.searchParams;
  const version = Number.parseInt(q.get("version") ?? "0", 10);
  return serveArtifact(p, param(event, "id"), {
    version: Number.isSafeInteger(version) && version > 0 ? version : 0,
    download: q.get("download") === "1",
    method,
    ifNoneMatch: event.req.headers.get("if-none-match"),
  });
});
