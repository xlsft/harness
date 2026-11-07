/**
 * Telegram as a ChatProvider (Bot API): updates by webhook (the X-Telegram-Bot-Api-Secret-Token header checked) or by
 * long polling (telegram-delivery.ts), private chats only, and the calls the service needs (sendMessage,
 * editMessageText, answerCallbackQuery, getChat, getMe).
 *
 * HARNESS_TELEGRAM_API_URL replaces https://api.telegram.org (tests point it at a fake). The token is part of every Bot
 * API URL, so no URL, request or Telegram error text with it is ever logged or returned: errors name the method only.
 */
import { timingSafeEqual } from "node:crypto";
import { ChatProvider as ChatProviderId } from "../gen/harness/chat/v1/chat_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { sha256 } from "../lib/ids.ts";
import { storeButton } from "./buttons.ts";
import type { ChatInbound, ChatOutbound, ChatPlace, ChatProfile, ChatProvider, ChatProviderClient, ChatVerifyResult, ChatWhoAmI } from "./types.ts";

const TIMEOUT_MS = 10_000;
/** Telegram's limit for a message's text. */
export const MAX_TEXT = 4096;
export const SECRET_HEADER = "x-telegram-bot-api-secret-token";
export const ALLOWED_UPDATES = ["message", "callback_query"];

function apiBase(): string {
  return (process.env.HARNESS_TELEGRAM_API_URL ?? "").trim().replace(/\/+$/, "") || "https://api.telegram.org";
}

function safeEqual(a: string, b: string): boolean {
  const x = Buffer.from(a);
  const y = Buffer.from(b);
  return x.length === y.length && timingSafeEqual(x, y);
}

/** A bot token as @BotFather gives it: "<bot id>:<secret>". */
export function validToken(token: string): boolean {
  return /^\d{3,20}:[A-Za-z0-9_-]{20,100}$/.test(token);
}

/** Dedup key of an update: update ids are per bot, and the secret tells bots apart without naming them. */
export function updateDeliveryId(secret: string, updateId: unknown): string | undefined {
  return typeof updateId === "number" ? `telegram:${sha256(secret).slice(0, 16)}:update:${updateId}` : undefined;
}

/** Splits text at line ends where it can, so no part is longer than Telegram takes. */
export function splitText(text: string, max = MAX_TEXT): string[] {
  const out: string[] = [];
  let rest = text || " ";
  while (rest.length > max) {
    let cut = rest.lastIndexOf("\n", max);
    if (cut < max / 2) cut = max;
    out.push(rest.slice(0, cut));
    rest = rest.slice(cut).replace(/^\n/, "");
  }
  out.push(rest);
  return out;
}

const COMMAND = /^\/([a-z_]+)(?:@\w+)?(?:\s+([\s\S]*))?$/i;

/** One update as inbound items. Groups and channels are not served: a session's prompts belong in a private chat. */
export function parseUpdate(u: any, botUserId: string): ChatInbound[] {
  if (u?.callback_query) {
    const q = u.callback_query;
    const chat = q.message?.chat;
    if (!q.from?.id || !chat || chat.type !== "private") return [{ kind: "ignore" }];
    return [
      {
        kind: "action",
        place: { teamId: botUserId, channel: String(chat.id), threadId: "" },
        messageId: String(q.message.message_id ?? ""),
        userId: String(q.from.id),
        value: String(q.data ?? ""),
        ackId: String(q.id ?? ""),
      },
    ];
  }
  const m = u?.message;
  if (!m?.from?.id || m.from.is_bot || m.chat?.type !== "private") return [{ kind: "ignore" }];
  let text = String(m.text ?? m.caption ?? "").trim();
  let command: "help" | "new" | undefined;
  const c = COMMAND.exec(text);
  if (c) {
    const name = c[1]!.toLowerCase();
    const arg = (c[2] ?? "").trim();
    if ((name === "start" || name === "link") && arg) text = `link ${arg}`;
    else if (name === "start" || name === "help") {
      command = "help";
      text = "";
    } else if (name === "new") {
      command = "new";
      text = arg;
    }
  }
  if (!text && !command) return [{ kind: "ignore" }];
  const chat = String(m.chat.id);
  const id = String(m.message_id ?? "");
  const name = [m.from.first_name, m.from.last_name].filter((s) => typeof s === "string" && s).join(" ") || (m.from.username ? `@${m.from.username}` : "");
  return [
    {
      kind: "message",
      messageKey: `telegram:msg:${botUserId}:${chat}:${id}`,
      place: { teamId: botUserId, channel: chat, threadId: id },
      messageId: id,
      userId: String(m.from.id),
      text,
      addressed: true,
      userName: name,
      command,
    },
  ];
}

function keyboard(buttons: { label: string; data: string }[]): unknown {
  // Short labels share a row; a long one (an "always allow" rule) gets its own.
  const rows: { text: string; callback_data: string }[][] = [];
  let row: { text: string; callback_data: string }[] = [];
  for (const b of buttons) {
    const k = { text: b.label.slice(0, 64), callback_data: b.data };
    if (b.label.length > 20) {
      rows.push([k]);
      continue;
    }
    row.push(k);
  }
  if (row.length) rows.unshift(row);
  return { inline_keyboard: rows };
}

export class TelegramClient implements ChatProviderClient {
  constructor(private readonly token: string) {}

  async call(method: string, body: Record<string, unknown>, signal?: AbortSignal): Promise<any> {
    let res: Response;
    try {
      res = await fetch(`${apiBase()}/bot${this.token}/${method}`, {
        method: "POST",
        headers: { "content-type": "application/json; charset=utf-8" },
        body: JSON.stringify(body),
        signal: signal ?? AbortSignal.timeout(TIMEOUT_MS),
      });
    } catch (err) {
      if (signal?.aborted) throw err;
      throw new ApiError("overloaded", `Telegram is unreachable (${method}): ${err instanceof Error ? err.name : "error"}`);
    }
    let data: any;
    try {
      data = await res.json();
    } catch {
      throw new ApiError("overloaded", `Telegram answered ${method} with HTTP ${res.status}`);
    }
    if (!data?.ok) {
      const code = Number(data?.error_code ?? res.status);
      // Descriptions are Telegram's own short texts; still, keep them short and never with the token.
      const why = String(data?.description ?? "error").replaceAll(this.token, "<token>").slice(0, 120);
      if (code === 429) throw new ApiError("overloaded", `Telegram rate limited ${method}`);
      const type = code === 401 || code === 404 ? "failed_precondition" : code === 409 ? "failed_precondition" : "upstream";
      throw new ApiError(type, `Telegram refused ${method}: ${why}`);
    }
    return data.result;
  }

  async whoami(): Promise<ChatWhoAmI> {
    const me = await this.call("getMe", {});
    if (!me?.is_bot) throw new ApiError("failed_precondition", "That token is not a bot's");
    return { teamId: String(me.id), teamName: String(me.username ?? ""), botUserId: String(me.id) };
  }

  private async markup(msg: ChatOutbound): Promise<unknown> {
    if (!msg.buttons?.length) return undefined;
    const buttons = [];
    for (const b of msg.buttons) buttons.push({ label: b.label, data: await storeButton(b.value) });
    return keyboard(buttons);
  }

  async post(place: ChatPlace, msg: ChatOutbound): Promise<string> {
    const parts = splitText(msg.text);
    let id = "";
    for (let i = 0; i < parts.length; i++) {
      const last = i === parts.length - 1;
      const r = await this.call("sendMessage", {
        chat_id: place.channel,
        text: parts[i],
        link_preview_options: { is_disabled: true },
        ...(last ? { reply_markup: await this.markup(msg) } : {}),
      });
      id = String(r?.message_id ?? "");
    }
    return id;
  }

  async update(place: ChatPlace, messageId: string, msg: ChatOutbound): Promise<void> {
    try {
      await this.call("editMessageText", {
        chat_id: place.channel,
        message_id: Number(messageId),
        text: (msg.text || " ").slice(0, MAX_TEXT),
        link_preview_options: { is_disabled: true },
        reply_markup: (await this.markup(msg)) ?? { inline_keyboard: [] },
      });
    } catch (err) {
      // Editing to the same text is an error in Telegram and harmless here.
      if (err instanceof ApiError && /not modified/i.test(err.message)) return;
      throw err;
    }
  }

  /** A private chat is seen by that person only. */
  async ephemeral(place: ChatPlace, _userId: string, text: string): Promise<void> {
    await this.post(place, { text });
  }

  async profile(userId: string): Promise<ChatProfile> {
    const c = await this.call("getChat", { chat_id: userId });
    const name = [c?.first_name, c?.last_name].filter(Boolean).join(" ") || (c?.username ? `@${c.username}` : "");
    return { name: String(name), email: "", bot: false };
  }

  async answerAction(ackId: string, text?: string): Promise<void> {
    if (!ackId) return;
    await this.call("answerCallbackQuery", { callback_query_id: ackId, ...(text ? { text: text.slice(0, 200) } : {}) });
  }

  async setWebhook(url: string, secret: string): Promise<void> {
    await this.call("setWebhook", { url, secret_token: secret, allowed_updates: ALLOWED_UPDATES, max_connections: 10 });
  }

  async deleteWebhook(): Promise<void> {
    await this.call("deleteWebhook", { drop_pending_updates: false });
  }

  /** Long poll; `timeout` seconds on Telegram's side, a little more on ours. */
  async getUpdates(offset: number, timeout: number, signal: AbortSignal): Promise<any[]> {
    const own = AbortSignal.timeout((timeout + 10) * 1000);
    const r = await this.call("getUpdates", { offset, timeout, allowed_updates: ALLOWED_UPDATES }, AbortSignal.any([signal, own]));
    return Array.isArray(r) ? r : [];
  }
}

export const telegram: ChatProvider = {
  id: ChatProviderId.TELEGRAM,
  name: "Telegram",
  threading: "chat",
  startHint: "Write to me",

  verify(headers: Headers, raw: string, signingSecret: string): ChatVerifyResult {
    const got = headers.get(SECRET_HEADER) ?? "";
    if (!got) return { ok: false, status: 401, reason: "missing X-Telegram-Bot-Api-Secret-Token" };
    if (!signingSecret || !safeEqual(got, signingSecret)) return { ok: false, status: 401, reason: "invalid secret token" };
    let deliveryId: string | undefined;
    try {
      deliveryId = updateDeliveryId(signingSecret, (JSON.parse(raw) as { update_id?: unknown }).update_id);
    } catch {
      // parse() reports it
    }
    // No replay key: the header is a fixed secret, not a per-request signature. Updates are deduplicated by id.
    return { ok: true, deliveryId };
  },

  parse(_headers: Headers, raw: string, botUserId: string): ChatInbound[] {
    try {
      return parseUpdate(JSON.parse(raw), botUserId);
    } catch {
      return [{ kind: "ignore" }];
    }
  },

  client(secrets: Record<string, string>): ChatProviderClient {
    if (!secrets.botToken) throw new ApiError("failed_precondition", "The Telegram bot has no token");
    return new TelegramClient(secrets.botToken);
  },
};
