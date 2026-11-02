/**
 * Admin JSON API for MCP server settings outside the RPC contract.
 * PATCH /api/admin/mcp/servers/:id {"allow_private_network": bool} — lets the proxy reach private and
 * loopback addresses for this server (reset automatically when its URL moves to another origin).
 */
import { ApiError } from "../../lib/errors.ts";
import { setAllowPrivateNetwork } from "../../mcp/registry.ts";
import { api, param, readJson, requireAdmin } from "../util.ts";

export default api(async (event) => {
  await requireAdmin(event);
  const id = param(event, "id");
  if (event.req.method !== "PATCH") throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  const body = await readJson<{ allow_private_network?: unknown }>(event);
  if (typeof body.allow_private_network !== "boolean") throw new ApiError("invalid_request", "allow_private_network must be a boolean");
  const row = await setAllowPrivateNetwork(id, body.allow_private_network);
  return { id: row.id, name: row.name, allow_private_network: row.allowPrivateNetwork };
});
