/** POST /api/chat/slack/events — Slack Events API and Interactivity (X-Slack-Signature verified, replays refused). */
import { defineHandler } from "h3";
import { handleInbound, SLACK } from "../../chat/service.ts";
import { errorResponse, json } from "../util.ts";

const MAX_BODY = 1 << 20;

export default defineHandler(async (event) => {
  try {
    const raw = await event.req.text();
    if (raw.length > MAX_BODY) return json({ error: "payload too large" }, 413);
    const r = await handleInbound(SLACK, event.req.headers, raw);
    return json(r.body, r.status);
  } catch (err) {
    return errorResponse(err);
  }
});
