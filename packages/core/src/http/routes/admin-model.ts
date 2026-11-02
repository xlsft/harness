import { ApiError } from "../../lib/errors.ts";
import {
  clearNegotiation,
  deleteModel,
  getModelAdmin,
  listNegotiation,
  updateModel,
  type ModelInput,
} from "../../services/models.ts";
import { api, param, readJson, requireAdmin } from "../util.ts";

export default api(async (event) => {
  await requireAdmin(event);
  const id = param(event, "id");
  switch (event.req.method) {
    case "GET":
      return { ...(await getModelAdmin(id)), negotiated_off: await listNegotiation(id) };
    case "PATCH": {
      const body = await readJson<ModelInput & { reset_negotiation?: boolean }>(event);
      const { reset_negotiation, ...input } = body;
      const out = await updateModel(id, input);
      if (reset_negotiation) await clearNegotiation(id);
      return out;
    }
    case "DELETE":
      return deleteModel(id);
    default:
      throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  }
});
