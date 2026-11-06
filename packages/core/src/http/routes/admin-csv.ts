/**
 * CSV for scripts and scheduled reports, with an access token (bearer) or a session:
 *   GET /api/admin/audit.csv?action=&actor=&target=&outcome=&from=&to=
 *   GET /api/admin/usage.csv?from=&to=&user_id=&group_id=
 * The same exports the Settings screens get through AuditService and BillingService.
 */
import { auditCsv } from "../../audit/log.ts";
import { usageCsv } from "../../services/usage-export.ts";
import { api, requireCapability } from "../util.ts";

export default api(async (event) => {
  const q = event.url.searchParams;
  const get = (k: string) => q.get(k) ?? undefined;
  const audit = event.url.pathname.endsWith("audit.csv");
  const r = audit
    ? (await requireCapability(event, "audit.read"), await auditCsv({ action: get("action"), actor: get("actor"), target: get("target"), outcome: get("outcome"), from: get("from"), to: get("to") }))
    : await usageCsv({ from: get("from"), to: get("to"), user_id: get("user_id"), group_id: get("group_id") }, (await requireCapability(event, "usage.read")).user.id);
  return new Response(r.csv, {
    headers: {
      "content-type": "text/csv; charset=utf-8",
      "content-disposition": `attachment; filename="harness-${audit ? "audit" : "usage"}.csv"`,
      "cache-control": "no-store",
      "x-harness-rows": String(r.rows),
      "x-harness-truncated": String(r.truncated),
    },
  });
});
