/** GET /api/tracker/oauth/callback?code=...&state=...: the redirect target of an admin tracker connection's OAuth (StartTrackerOAuth). */
import { defineHandler } from "h3";
import { finishOAuth } from "../../tracker/connections.ts";
import { resultPage as page } from "../result-page.ts";

export default defineHandler(async (event) => {
  const r = await finishOAuth(event.url.searchParams);
  return r.ok ? page(true, "Connected", `“${r.name}” is authorized. You can close this window.`, 200) : page(false, "Authorization failed", r.message, 400);
});
