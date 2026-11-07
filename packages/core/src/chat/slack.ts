/**
 * Slack as a ChatProvider: request signatures (v0 HMAC-SHA256), the Events API (app_mention, message) and Interactivity
 * (block_actions), and the Web API calls the service needs (chat.postMessage, chat.update, chat.postEphemeral,
 * users.info, auth.test).
 *
 * HARNESS_SLACK_API_URL replaces https://slack.com/api (tests point it at a fake). Tokens never appear in errors or logs.
 */
import { createHmac, timingSafeEqual } from "node:crypto";
import { ChatProvider as ChatProviderId } from "../gen/harness/chat/v1/chat_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { sha256 } from "../lib/ids.ts";
import type { ChatInbound, ChatOutbound, ChatPlace, ChatProfile, ChatProvider, ChatProviderClient, ChatVerifyResult, ChatWhoAmI } from "./types.ts";

/** Requests older (or newer) than this are refused: Slack's own recommendation. */
export const MAX_SKEW_S = 5 * 60;
const TIMEOUT_MS = 10_000;

function apiBase(): string {
  return (process.env.HARNESS_SLACK_API_URL ?? "").trim().replace(/\/+$/, "") || "https://slack.com/api";
}

export function slackSignature(secret: string, timestamp: string, raw: string): string {
  return `v0=${createHmac("sha256", secret).update(`v0:${timestamp}:${raw}`).digest("hex")}`;
}

function safeEqual(a: string, b: string): boolean {
  const x = Buffer.from(a);
  const y = Buffer.from(b);
  return x.length === y.length && timingSafeEqual(x, y);
}

/** Slack mrkdwn needs &, < and > escaped; everything we send is plain text. */
export function escapeText(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/** A message's text as the person meant it: the bot's mention removed, Slack's escapes undone, links reduced to their text. */
export function plainText(text: string, botUserId: string): string {
  let t = text;
  if (botUserId) t = t.split(`<@${botUserId}>`).join(" ");
  t = t.replace(/<(https?:\/\/[^|>]+)\|([^>]+)>/g, "$2").replace(/<(https?:\/\/[^>]+)>/g, "$1");
  return t.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&").replace(/[ \t]+/g, " ").trim();
}

function isForm(headers: Headers): boolean {
  return (headers.get("content-type") ?? "").includes("application/x-www-form-urlencoded");
}

function blocksOf(msg: ChatOutbound): unknown[] | undefined {
  if (!msg.buttons?.length) return undefined;
  return [
    { type: "section", text: { type: "mrkdwn", text: escapeText(msg.text).slice(0, 2900) } },
    {
      type: "actions",
      elements: msg.buttons.map((b) => ({
        type: "button",
        action_id: b.id,
        text: { type: "plain_text", text: b.label.slice(0, 75) },
        value: b.value.slice(0, 2000),
        ...(b.style ? { style: b.style } : {}),
      })),
    },
  ];
}

class SlackClient implements ChatProviderClient {
  constructor(private readonly token: string) {}

  private async call(method: string, body: Record<string, unknown>): Promise<Record<string, any>> {
    let res: Response;
    try {
      res = await fetch(`${apiBase()}/${method}`, {
        method: "POST",
        headers: { authorization: `Bearer ${this.token}`, "content-type": "application/json; charset=utf-8" },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(TIMEOUT_MS),
      });
    } catch (err) {
      throw new ApiError("overloaded", `Slack is unreachable (${method}): ${err instanceof Error ? err.name : "error"}`);
    }
    if (res.status === 429) throw new ApiError("overloaded", `Slack rate limited ${method}`);
    let data: Record<string, any>;
    try {
      data = (await res.json()) as Record<string, any>;
    } catch {
      throw new ApiError("overloaded", `Slack answered ${method} with HTTP ${res.status}`);
    }
    if (!data.ok) {
      const code = typeof data.error === "string" ? data.error.slice(0, 100) : "unknown_error";
      const type = code === "invalid_auth" || code === "not_authed" || code === "token_revoked" || code === "account_inactive" ? "failed_precondition" : "upstream";
      throw new ApiError(type, `Slack refused ${method}: ${code}`);
    }
    return data;
  }

  async whoami(): Promise<ChatWhoAmI> {
    const d = await this.call("auth.test", {});
    return { teamId: String(d.team_id ?? ""), teamName: String(d.team ?? ""), botUserId: String(d.user_id ?? "") };
  }

  async post(place: ChatPlace, msg: ChatOutbound): Promise<string> {
    const d = await this.call("chat.postMessage", {
      channel: place.channel,
      thread_ts: place.threadId || undefined,
      text: escapeText(msg.text).slice(0, 39_000),
      blocks: blocksOf(msg),
      unfurl_links: false,
      unfurl_media: false,
    });
    return String(d.ts ?? "");
  }

  async update(place: ChatPlace, messageId: string, msg: ChatOutbound): Promise<void> {
    await this.call("chat.update", { channel: place.channel, ts: messageId, text: escapeText(msg.text).slice(0, 39_000), blocks: blocksOf(msg) ?? [] });
  }

  async ephemeral(place: ChatPlace, userId: string, text: string): Promise<void> {
    await this.call("chat.postEphemeral", { channel: place.channel, user: userId, thread_ts: place.threadId || undefined, text: escapeText(text) });
  }

  async profile(userId: string): Promise<ChatProfile> {
    const d = await this.call("users.info", { user: userId });
    const u = d.user ?? {};
    return {
      name: String(u.profile?.display_name || u.real_name || u.name || ""),
      email: String(u.profile?.email ?? ""),
      bot: !!u.is_bot,
    };
  }
}

export const slack: ChatProvider = {
  id: ChatProviderId.SLACK,
  name: "Slack",
  threading: "thread",
  startHint: "Mention me or write to me",

  verify(headers: Headers, raw: string, signingSecret: string, now = Date.now()): ChatVerifyResult {
    const sig = headers.get("x-slack-signature") ?? "";
    const ts = headers.get("x-slack-request-timestamp") ?? "";
    if (!sig || !ts) return { ok: false, status: 401, reason: "missing X-Slack-Signature or X-Slack-Request-Timestamp" };
    if (!/^\d{1,12}$/.test(ts) || Math.abs(now / 1000 - Number(ts)) > MAX_SKEW_S) return { ok: false, status: 401, reason: "stale or invalid timestamp" };
    if (!signingSecret || !safeEqual(slackSignature(signingSecret, ts, raw), sig)) return { ok: false, status: 401, reason: "invalid signature" };
    let deliveryId: string | undefined;
    if (!isForm(headers)) {
      try {
        const id = (JSON.parse(raw) as { event_id?: unknown }).event_id;
        if (typeof id === "string" && id) deliveryId = `slack:event:${id}`;
      } catch {
        // parse() reports it
      }
    }
    return { ok: true, replayKey: `slack:sig:${sha256(sig)}`, deliveryId };
  },

  parse(headers: Headers, raw: string, botUserId: string): ChatInbound[] {
    if (isForm(headers)) {
      const payload = new URLSearchParams(raw).get("payload");
      if (!payload) return [{ kind: "ignore" }];
      let p: any;
      try {
        p = JSON.parse(payload);
      } catch {
        return [{ kind: "ignore" }];
      }
      if (p?.type !== "block_actions" || !Array.isArray(p.actions)) return [{ kind: "ignore" }];
      const channel = String(p.channel?.id ?? p.container?.channel_id ?? "");
      const messageId = String(p.container?.message_ts ?? p.message?.ts ?? "");
      const threadId = String(p.container?.thread_ts ?? p.message?.thread_ts ?? messageId);
      const teamId = String(p.team?.id ?? p.user?.team_id ?? "");
      return p.actions.slice(0, 5).map((a: any) => ({
        kind: "action" as const,
        place: { teamId, channel, threadId },
        messageId,
        userId: String(p.user?.id ?? ""),
        value: String(a?.value ?? ""),
      }));
    }
    let body: any;
    try {
      body = JSON.parse(raw);
    } catch {
      return [{ kind: "ignore" }];
    }
    if (body?.type === "url_verification" && typeof body.challenge === "string") return [{ kind: "challenge", challenge: body.challenge }];
    if (body?.type !== "event_callback" || !body.event) return [{ kind: "ignore" }];
    const e = body.event;
    if (e.type !== "app_mention" && e.type !== "message") return [{ kind: "ignore" }];
    // Edits, joins, bot posts (ours included) and file shares without text do nothing.
    if (e.subtype || e.bot_id || !e.user || e.user === botUserId || typeof e.text !== "string") return [{ kind: "ignore" }];
    const ts = String(e.ts ?? "");
    const threadTs = String(e.thread_ts ?? "");
    const channel = String(e.channel ?? "");
    const im = e.type === "message" && e.channel_type === "im";
    const mentioned = !!botUserId && e.text.includes(`<@${botUserId}>`);
    const addressed = e.type === "app_mention" || im || mentioned;
    // A plain channel message outside a thread is not for us.
    if (!addressed && !threadTs) return [{ kind: "ignore" }];
    return [
      {
        kind: "message",
        messageKey: `slack:msg:${channel}:${ts}`,
        place: { teamId: String(body.team_id ?? e.team ?? ""), channel, threadId: threadTs || ts },
        messageId: ts,
        userId: String(e.user),
        text: plainText(e.text, botUserId),
        addressed,
      },
    ];
  },

  client(secrets: Record<string, string>): ChatProviderClient {
    if (!secrets.botToken) throw new ApiError("failed_precondition", "The Slack app has no bot token");
    return new SlackClient(secrets.botToken);
  },
};
