import { discoverModels } from "../../services/discovery.ts";
import { api, param, requireAdmin } from "../util.ts";

/** POST /api/admin/providers/:id/discover: candidate models from the upstream's /v1/models. */
export default api(async (event) => {
  await requireAdmin(event);
  return discoverModels(param(event, "id"), true);
});
