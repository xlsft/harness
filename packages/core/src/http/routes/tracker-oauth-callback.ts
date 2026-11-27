/**
 * GET /api/tracker/oauth/callback?code=...&state=...: the redirect target of a company tracker or knowledge connection's OAuth
 * (StartTrackerOAuth, StartKnowledgeOAuth). The sealed state says which.
 */
import { defineHandler } from "h3";
import { finishKnowledgeOAuth } from "../../knowledge/connections.ts";
import { finishOAuth } from "../../tracker/connections.ts";
import { openFlow } from "../../tracker/oauth-state.ts";
import { resultPage as page } from "../result-page.ts";

export default defineHandler(async (event) => {
  const params = event.url.searchParams;
  const flow = openFlow(params.get("state") ?? "");
  if ("error" in flow) return page(false, "Authorization failed", flow.error, 400);
  const r = flow.kind === "knowledge" ? await finishKnowledgeOAuth(flow, params) : await finishOAuth(flow, params);
  return r.ok ? page(true, "Connected", `“${r.name}” is authorized. You can close this window.`, 200) : page(false, "Authorization failed", r.message, 400);
});
