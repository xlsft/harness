import { ApiError } from "../../lib/errors.ts";
import { deleteProvider, listUserProviders } from "../../services/providers.ts";
import { api, param, requireCapability } from "../util.ts";
import { assertMayGrant } from "../../auth/roles.ts";
import { userRole } from "../../services/admin-users.ts";

/**
 * GET    /api/admin/users/:userId/providers       a user's personal providers (key_set only, no key hint)
 * DELETE /api/admin/users/:userId/providers/:id   deletes one (with its models)
 *
 * Admins can neither read nor use personal keys (services/providers.ts).
 */
export default api(async (event) => {
  const p = await requireCapability(event, "users.write");
  const userId = param(event, "userId");
  // Like AdminService: an account is reached only with every permission its role has.
  assertMayGrant(p.user, await userRole(userId));
  const id = (event.context.params as Record<string, string> | undefined)?.id;
  if (event.req.method === "GET" && !id) {
    return { providers: (await listUserProviders(userId)).map(({ api_key_hint: _hint, ...p }) => p) };
  }
  if (event.req.method === "DELETE" && id) return deleteProvider(decodeURIComponent(id), { userId });
  throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
});
