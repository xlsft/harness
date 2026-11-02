import { listUsage } from "../../services/usage.ts";
import { api, requireAdmin } from "../util.ts";

export default api(async (event) => {
  await requireAdmin(event);
  const q = event.url.searchParams;
  return listUsage({ userId: q.get("user_id") ?? undefined, limit: Number(q.get("limit") ?? 100) });
});
