/**
 * POST /api/schedules/webhook/:token: something outside asks for a scheduled task to run. token is
 * <schedule document id>.<secret>; the client that made the schedule shows the URL.
 */
import { defineHandler } from "h3";
import { MAX_WEBHOOK_BODY, receiveScheduleWebhook } from "../../services/schedule-webhook.ts";
import { errorResponse, json, param } from "../util.ts";

export default defineHandler(async (event) => {
  try {
    const raw = await event.req.text();
    if (Buffer.byteLength(raw) > MAX_WEBHOOK_BODY) return json({ error: "payload too large" }, 413);
    const r = await receiveScheduleWebhook(param(event, "token"), raw);
    const headers: Record<string, string> = r.retryAfterMs === undefined ? {} : { "retry-after": String(Math.ceil(r.retryAfterMs / 1000)) };
    return json(r.body, r.status, headers);
  } catch (err) {
    return errorResponse(err);
  }
});
