import { ApiError } from "../../lib/errors.ts";
import { createModel, listModelsAdmin, type ModelInput } from "../../services/models.ts";
import { api, json, readJson, requireCapability } from "../util.ts";

export default api(async (event) => {
  await requireCapability(event, "providers.write");
  switch (event.req.method) {
    case "GET":
      return { models: await listModelsAdmin() };
    case "POST":
      return json(await createModel(await readJson<ModelInput>(event)), 201);
    default:
      throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  }
});
