import { ApiError } from "../../lib/errors.ts";
import { createProvider, listProviders, type ProviderInput } from "../../services/providers.ts";
import { api, json, readJson, requireAdmin } from "../util.ts";

export default api(async (event) => {
  await requireAdmin(event);
  switch (event.req.method) {
    case "GET":
      return { providers: await listProviders() };
    case "POST":
      return json(await createProvider(await readJson<ProviderInput>(event)), 201);
    default:
      throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  }
});
