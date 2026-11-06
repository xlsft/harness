import { listUsage } from "../../services/usage.ts";
import { api, requireCapability } from "../util.ts";

export default api(async (event) => {
  await requireCapability(event, "usage.read");
  const q = event.url.searchParams;
  return listUsage({ userId: q.get("user_id") ?? undefined, limit: Number(q.get("limit") ?? 100) });
});
