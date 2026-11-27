import { ApiError } from "../../lib/errors.ts";
import { getSlots, setSlots, type Slots } from "../../services/models.ts";
import { api, readJson, requireCapability } from "../util.ts";

export default api(async (event) => {
  await requireCapability(event, "providers.write");
  switch (event.req.method) {
    case "GET":
      return { slots: await getSlots() };
    case "PUT":
      return { slots: await setSlots(await readJson<Slots>(event)) };
    default:
      throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  }
});
