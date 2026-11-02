import { listDecisionLog } from "../../decision/service.ts";
import { api, requireAdmin } from "../util.ts";

/** GET /api/admin/decision/log?limit=&session_id=&user_id=   recent auto-mode decisions, newest first. */
export default api(async (event) => {
  await requireAdmin(event);
  const q = event.url.searchParams;
  return listDecisionLog({
    limit: q.get("limit") ? Number.parseInt(q.get("limit")!, 10) : undefined,
    sessionId: q.get("session_id") || undefined,
    userId: q.get("user_id") || undefined,
  });
});
