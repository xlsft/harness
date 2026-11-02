import { getDecisionConfigAdmin, setDecisionConfig } from "../../decision/config.ts";
import { ApiError } from "../../lib/errors.ts";
import { api, readJson, requireAdmin } from "../util.ts";

/**
 * GET /api/admin/decision   DecisionService chain configuration ({config, configured}; secrets redacted)
 * PUT /api/admin/decision   replace it (see src/decision/config.ts for the shape)
 */
export default api(async (event) => {
  const admin = await requireAdmin(event);
  switch (event.req.method) {
    case "GET":
      return getDecisionConfigAdmin();
    case "PUT":
      return setDecisionConfig(await readJson(event), admin.user.id);
    default:
      throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  }
});
