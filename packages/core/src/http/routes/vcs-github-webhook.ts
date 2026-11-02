/** POST /api/vcs/github/webhook — GitHub App webhooks (X-Hub-Signature-256 verified). */
import { defineHandler } from "h3";
import { handleGitHubWebhook } from "../../vcs/webhooks.ts";
import { errorResponse, json } from "../util.ts";

const MAX_BODY = 25 << 20;

export default defineHandler(async (event) => {
  try {
    const raw = await event.req.text();
    if (raw.length > MAX_BODY) return json({ error: "payload too large" }, 413);
    const r = await handleGitHubWebhook(event.req.headers, raw);
    return json(r.body, r.status);
  } catch (err) {
    return errorResponse(err);
  }
});
