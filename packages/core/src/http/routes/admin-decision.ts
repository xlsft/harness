import { getDecisionConfigAdmin, setDecisionConfig } from "../../decision/config.ts";
import { ApiError } from "../../lib/errors.ts";
import { api, readJson, requireAdmin } from "../util.ts";

/**
 * GET /api/admin/decision   DecisionService chain configuration ({config, configured}; secrets redacted)
 * PUT /api/admin/decision   replace it (see src/decision/config.ts for the shape)
 *
 * Provider types: rules, llm (model classifier through the gateway), http (policy webhook) and
 * decision_model (a typed decision model such as TypeSafe's Jev via OpenRouter, TypeSafe or a self-hosted
 * endpoint; src/decision/decision-model.ts). Write-only secrets (http bearer_token/headers, decision_model
 * api_key) are never returned: GET shows bearer_token_set / header_names / api_key_set instead.
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
