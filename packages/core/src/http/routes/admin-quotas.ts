import { eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../../db/index.ts";
import { ApiError } from "../../lib/errors.ts";
import { toPublicUser } from "../../services/devices.ts";
import { getQuotaConfig, patchQuotaConfig, setQuotaConfig, spendStatus } from "../../services/quotas.ts";
import { api, readJson, requireAdmin } from "../util.ts";

/**
 * GET   /api/admin/quotas[?user_id=]  config (+ current spend and effective limits of a user)
 * PUT   /api/admin/quotas             replace {users: {<id>: {daily_usd, monthly_usd}}, roles: {...}, session_usd}
 * PATCH /api/admin/quotas             merge; a null entry removes a user's / role's limits
 */
export default api(async (event) => {
  await requireAdmin(event);
  switch (event.req.method) {
    case "GET": {
      const quotas = await getQuotaConfig();
      const userId = event.url.searchParams.get("user_id");
      if (!userId) return { quotas };
      await ensureDb();
      const u = await getDb().query.user.findFirst({ where: eq(schema.user.id, userId) });
      if (!u) throw new ApiError("not_found", `User ${userId} not found`);
      return { quotas, user: { id: u.id, ...(await spendStatus(toPublicUser(u))) } };
    }
    case "PUT":
      return { quotas: await setQuotaConfig(await readJson(event)) };
    case "PATCH":
      return { quotas: await patchQuotaConfig(await readJson(event)) };
    default:
      throw new ApiError("invalid_request", "Method not allowed", { status: 405 });
  }
});
