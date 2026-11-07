/** POST /api/chat/telegram/webhook/:key — Telegram updates for the organization's bot ("org") or a personal bot (its id). */
import { defineHandler } from "h3";
import { handleInbound, TELEGRAM } from "../../chat/service.ts";
import { errorResponse, json } from "../util.ts";

const MAX_BODY = 1 << 20;

export default defineHandler(async (event) => {
  try {
    const key = (event.context.params as Record<string, string> | undefined)?.key ?? "";
    if (!/^(org|cbt_[A-Za-z0-9]{1,40})$/.test(key)) return json({ error: "not found" }, 404);
    const raw = await event.req.text();
    if (raw.length > MAX_BODY) return json({ error: "payload too large" }, 413);
    const r = await handleInbound(TELEGRAM, event.req.headers, raw, key);
    return json(r.body, r.status);
  } catch (err) {
    return errorResponse(err);
  }
});
