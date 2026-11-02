import { ApiError } from "../../lib/errors.ts";
import { deleteProvider, getProvider, updateProvider, type ProviderInput } from "../../services/providers.ts";
import { api, param, readJson, requireAdmin } from "../util.ts";

export default api(async (event) => {
  await requireAdmin(event);
  const id = param(event, "id");
  switch (event.req.method) {
    case "GET":
      return getProvider(id);
    case "PATCH":
      return updateProvider(id, await readJson<ProviderInput>(event));
    case "DELETE":
      return deleteProvider(id);
    default:
      throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  }
});
