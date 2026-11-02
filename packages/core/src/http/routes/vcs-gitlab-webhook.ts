/**
 * POST /api/vcs/gitlab/webhook — GitLab group/project webhooks (X-Gitlab-Token verified against the
 * connection's webhook secret). Optional ?connection_id=vcs_... pins the connection.
 */
import { defineHandler } from "h3";
import { handleGitLabWebhook } from "../../vcs/webhooks.ts";
import { errorResponse, json } from "../util.ts";

const MAX_BODY = 25 << 20;

export default defineHandler(async (event) => {
  try {
    const raw = await event.req.text();
    if (raw.length > MAX_BODY) return json({ error: "payload too large" }, 413);
    const r = await handleGitLabWebhook(event.req.headers, raw, event.url.searchParams.get("connection_id") ?? "");
    return json(r.body, r.status);
  } catch (err) {
    return errorResponse(err);
  }
});
