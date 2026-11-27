/**
 * How Telegram updates reach the server, per app or personal bot: a webhook (setWebhook with the bot's secret token, which
 * the route checks) when the server has a public HTTPS URL or the admin chose it, long polling (getUpdates) otherwise.
 *
 * Polling runs in this process, one loop per bot. Telegram refuses two concurrent getUpdates for one bot, so polling
 * assumes one server replica, as the chat watchers do. Offsets live in memory: after a restart Telegram hands out the
 * updates it still holds, and the delivery ids (chat_delivery) keep any of them from being handled twice.
 */
import { ChatDelivery, ChatProvider as ChatProviderId } from "../gen/harness/chat/v1/chat_pb.ts";
import { activeDelivery, loadApp, noteAppEvent, requestUrl, telegramApps, type AppSecrets, type ChatAppCtx } from "./app.ts";
import { claim, processItems } from "./service.ts";
import { parseUpdate, telegram, TelegramClient, updateDeliveryId } from "./telegram.ts";

const TELEGRAM = ChatProviderId.TELEGRAM;
const POLL_S = () => {
  const n = Number(process.env.HARNESS_TELEGRAM_POLL_S ?? "");
  return Number.isFinite(n) && n >= 0 && process.env.HARNESS_TELEGRAM_POLL_S ? Math.min(n, 50) : 25;
};

const pollers = new Map<string, AbortController>();

const errText = (err: unknown) => (err instanceof Error ? err.message : String(err));

function stopPoller(key: string): void {
  pollers.get(key)?.abort();
  pollers.delete(key);
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(t);
        resolve();
      },
      { once: true },
    );
  });
}

/** Whether a bot is polled by this process now (tests, diagnostics). */
export function isPolling(appKey: string): boolean {
  return pollers.has(appKey === "org" ? "" : appKey);
}

/** After an app or bot was saved or removed (`removed` holds the secrets of a removed one). */
export async function syncTelegramDelivery(appKey: string, removed?: AppSecrets): Promise<void> {
  const key = appKey === "org" ? "" : appKey;
  stopPoller(key);
  if (removed) {
    if (removed.botToken) await new TelegramClient(removed.botToken).deleteWebhook().catch(() => {});
    return;
  }
  const app = await loadApp(TELEGRAM, key);
  if (app) await configure(app);
}

/**
 * Points Telegram at the server or starts polling. A failure is recorded on the app (last error), not thrown: the
 * settings are saved either way, and the settings screen shows the error.
 */
async function configure(app: ChatAppCtx): Promise<void> {
  const client = new TelegramClient(app.secrets.botToken ?? "");
  try {
    if (activeDelivery(app.delivery) === ChatDelivery.WEBHOOK) {
      await client.setWebhook(requestUrl(TELEGRAM, app.key), app.secrets.signingSecret ?? "");
      return;
    }
    // getUpdates is refused while a webhook is set.
    await client.deleteWebhook();
  } catch (err) {
    await noteAppEvent(TELEGRAM, errText(err), app.key).catch(() => {});
    if (activeDelivery(app.delivery) === ChatDelivery.WEBHOOK) return;
  }
  const ctl = new AbortController();
  pollers.set(app.key, ctl);
  void runPoller(app.key, ctl.signal).finally(() => {
    if (pollers.get(app.key) === ctl) pollers.delete(app.key);
  });
}

async function runPoller(key: string, signal: AbortSignal): Promise<void> {
  let offset = 0;
  let failures = 0;
  while (!signal.aborted) {
    const app = await loadApp(TELEGRAM, key).catch(() => undefined);
    if (!app?.secrets.botToken || activeDelivery(app.delivery) !== ChatDelivery.POLLING) return;
    const client = new TelegramClient(app.secrets.botToken);
    try {
      const updates = await client.getUpdates(offset, POLL_S(), signal);
      failures = 0;
      for (const u of updates) {
        if (typeof u?.update_id === "number") offset = Math.max(offset, u.update_id + 1);
        const id = updateDeliveryId(app.secrets.signingSecret ?? "", u?.update_id);
        if (id && !(await claim(id))) continue;
        await processItems(telegram, app, parseUpdate(u, app.botUserId));
      }
    } catch (err) {
      if (signal.aborted) return;
      failures++;
      await noteAppEvent(TELEGRAM, errText(err), key).catch(() => {});
      await sleep(Math.min(1000 * 2 ** Math.min(failures, 5), 30_000), signal);
    }
  }
}

/** Server start: every Telegram app and bot gets its webhook set again or its polling started. */
export async function resumeTelegramDelivery(): Promise<void> {
  for (const app of await telegramApps()) await configure(app);
}

/** Stops every poller (tests, shutdown). Webhooks stay set: Telegram retries until the server is back. */
export function stopTelegramDelivery(): void {
  for (const c of pollers.values()) c.abort();
  pollers.clear();
}
