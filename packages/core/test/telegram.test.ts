/**
 * Telegram against a fake Bot API and fake hosts on the relay: connecting the organization's bot (webhook) and a personal
 * bot (long polling), the secret token header and update dedup, linking by a one-time code (deep link), starting a
 * session from a private chat, progress as one edited message, permission buttons only the owner can answer, steering,
 * /new, the final answer, always-on hosts, policy switches and audit. No real bot, account or token.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { PermissionMode, RespondToPermissionRequest_Decision } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { ChatDelivery, ChatProvider } from "../src/gen/harness/chat/v1/chat_pb.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { AgentHostScope, HostWorkState } from "../src/gen/harness/relay/v1/hosts_pb.ts";
import { DocumentKind } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { chatIdle, stopChatWatchers } from "../src/chat/service.ts";
import { isPolling, stopTelegramDelivery } from "../src/chat/telegram-delivery.ts";
import { getDb, schema } from "../src/db/index.ts";
import { FakeHost } from "./helpers/fake-host.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const ORG_TOKEN = "111111:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
const BOB_TOKEN = "222222:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB";
const BOTS: Record<string, { id: number; username: string }> = {
  [ORG_TOKEN]: { id: 111111, username: "org_bot" },
  [BOB_TOKEN]: { id: 222222, username: "bob_own_bot" },
};
// Telegram user ids (a private chat's id is the user's id).
const ALICE = 9001;
const BOB = 9002;
const MALLORY = 9003;

interface Call {
  token: string;
  method: string;
  body: Record<string, any>;
}

let tg: Server;
let calls: Call[] = [];
let nextMessageId = 100;
const queued: Record<string, any[]> = {};

async function startFakeTelegram(): Promise<string> {
  tg = createServer((req, res) => {
    let raw = "";
    req.on("data", (c) => (raw += c));
    req.on("end", async () => {
      const m = /^\/bot([^/]+)\/(\w+)$/.exec(req.url ?? "");
      const reply = (o: unknown, status = 200) => {
        res.statusCode = status;
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify(o));
      };
      const token = decodeURIComponent(m?.[1] ?? "");
      const bot = BOTS[token];
      if (!m || !bot) return reply({ ok: false, error_code: 401, description: "Unauthorized" }, 401);
      const method = m[2]!;
      const body = raw ? JSON.parse(raw) : {};
      const call: Call = { token, method, body };
      if (method !== "getUpdates") calls.push(call);
      switch (method) {
        case "getMe":
          return reply({ ok: true, result: { id: bot.id, is_bot: true, first_name: "Bot", username: bot.username } });
        case "sendMessage":
          sentIds.set(call, nextMessageId + 1);
          return reply({ ok: true, result: { message_id: ++nextMessageId, chat: { id: Number(body.chat_id), type: "private" }, text: body.text } });
        case "editMessageText":
          return reply({ ok: true, result: true });
        case "answerCallbackQuery":
        case "setWebhook":
        case "deleteWebhook":
          return reply({ ok: true, result: true });
        case "getChat":
          return reply({ ok: true, result: { id: Number(body.chat_id), type: "private", first_name: `User ${body.chat_id}` } });
        case "getUpdates": {
          const q = (queued[token] ??= []);
          // Confirmed updates go, as Telegram drops them once a later offset is asked for.
          while (q.length && q[0].update_id < (body.offset ?? 0)) q.shift();
          if (!q.length) await new Promise((r) => setTimeout(r, 100));
          return reply({ ok: true, result: q.filter((u) => u.update_id >= (body.offset ?? 0)) });
        }
        default:
          return reply({ ok: false, error_code: 404, description: "Not Found: method not found" }, 404);
      }
    });
  });
  await new Promise<void>((r) => tg.listen(0, "127.0.0.1", r));
  return `http://127.0.0.1:${(tg.address() as AddressInfo).port}`;
}

let ts: TestServer;
type Clients = ReturnType<TestServer["as"]>;
let admin: Clients;
let alice: Clients;
let bob: Clients;
let aliceToken: string;
let bobToken: string;

beforeAll(async () => {
  const api = await startFakeTelegram();
  ts = await startTestServer({
    HARNESS_TELEGRAM_API_URL: api,
    HARNESS_TELEGRAM_POLL_S: "0",
    HARNESS_RELAY_HEARTBEAT_MS: "300",
    HARNESS_RELAY_ACK_WAIT_MS: "3000",
    HARNESS_RELAY_SPAWN_WAIT_MS: "3000",
    HARNESS_CHAT_PROGRESS_MS: "1",
    HARNESS_CHAT_HOST_WORK_POLL_MS: "50",
  });
  const a = await deviceLogin(ts);
  admin = ts.as(a.accessToken);
  aliceToken = (await createUserAndLogin(ts, a.cookie, "alice@test.local", "alice-password-1")).accessToken;
  bobToken = (await createUserAndLogin(ts, a.cookie, "bob@test.local", "bob-password-1")).accessToken;
  alice = ts.as(aliceToken);
  bob = ts.as(bobToken);
});

afterAll(async () => {
  stopTelegramDelivery();
  stopChatWatchers();
  await ts.close();
  await new Promise((r) => tg.close(r));
});

async function expectError(p: Promise<unknown>, code: Code) {
  const err = await p.then(
    () => undefined,
    (e) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  expect((err as ConnectError).code).toBe(code);
}

async function waitFor<T>(fn: () => T | undefined | Promise<T | undefined>, what: string, ms = 8000): Promise<T> {
  const deadline = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 25));
  }
}

let webhookSecret = "";
let updateId = 1;
let messageId = 1;

const message = (from: number, text: string) => ({
  update_id: ++updateId,
  message: { message_id: ++messageId, from: { id: from, is_bot: false, first_name: `U${from}` }, chat: { id: from, type: "private" }, date: 0, text },
});

async function hook(update: Record<string, unknown>, secret = webhookSecret, key = "org") {
  const headers: Record<string, string> = { "content-type": "application/json" };
  if (secret) headers["x-telegram-bot-api-secret-token"] = secret;
  const res = await fetch(`${ts.url}/api/chat/telegram/webhook/${key}`, { method: "POST", headers, body: JSON.stringify(update) });
  const body = (await res.json()) as Record<string, unknown>;
  await chatIdle();
  return { status: res.status, body };
}

/** The message id the fake gave a sendMessage call (calls and replies are in order). */
const sentIds = new WeakMap<Call, number>();
const progressIdOf = (c: Call) => sentIds.get(c) ?? -1;

const sent = (token: string, pred: (b: Record<string, any>) => boolean) => calls.find((c) => c.token === token && c.method === "sendMessage" && pred(c.body));

async function putSettings(token: string, chatDefaults: Record<string, string>) {
  const c = ts.as(token);
  const ref = { scope: Scope.USER, kind: DocumentKind.SETTINGS, key: "user", projectKey: "" };
  const cur = await c.sync.getDocument({ ref }).catch(() => undefined);
  const content = JSON.stringify({ ...(cur?.document?.content ? JSON.parse(cur.document.content) : {}), chatDefaults });
  await c.sync.putDocument({ ref, content });
}

describe("telegram", () => {
  it("connects the organization's bot with a webhook and never returns the token", async () => {
    await expectError(alice.chat.putChatApp({ provider: ChatProvider.TELEGRAM, botToken: ORG_TOKEN, enabled: true }), Code.PermissionDenied);
    await expectError(admin.chat.putChatApp({ provider: ChatProvider.TELEGRAM, botToken: "not-a-token", enabled: true }), Code.InvalidArgument);
    await expectError(admin.chat.putChatApp({ provider: ChatProvider.TELEGRAM, botToken: "333333:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", enabled: true }), Code.FailedPrecondition);
    const r = await admin.chat.putChatApp({ provider: ChatProvider.TELEGRAM, botToken: ORG_TOKEN, enabled: true, delivery: ChatDelivery.WEBHOOK });
    expect(r.app).toMatchObject({ configured: true, enabled: true, teamId: "111111", teamName: "org_bot", botUserId: "111111", delivery: ChatDelivery.WEBHOOK, activeDelivery: ChatDelivery.WEBHOOK, matchByEmail: false });
    expect(r.app!.requestUrl).toMatch(/\/api\/chat\/telegram\/webhook\/org$/);
    expect(JSON.stringify(r.app)).not.toContain(ORG_TOKEN);
    const set = calls.find((c) => c.method === "setWebhook");
    expect(set?.body.url).toBe(r.app!.requestUrl);
    expect(set?.body.secret_token).toMatch(/^[0-9a-f]{64}$/);
    expect(set?.body.allowed_updates).toEqual(["message", "callback_query"]);
    webhookSecret = set!.body.secret_token;
    expect(isPolling("org")).toBe(false);
    const logged = JSON.stringify(await admin.audit.listAuditEvents({ filter: { action: "chat.app_update" } }));
    expect(logged).toContain("token_changed");
    expect(logged).not.toContain(ORG_TOKEN);
    expect(logged).not.toContain(webhookSecret);
  });

  it("checks the secret token header and handles an update once", async () => {
    const u = message(MALLORY, "hello");
    expect((await hook(u, "")).status).toBe(401);
    expect((await hook(u, "0".repeat(64))).status).toBe(401);
    expect((await hook(u, webhookSecret, "cbt_nosuchbot")).status).toBe(404);
    calls = [];
    expect((await hook(u)).status).toBe(200);
    expect(sent(ORG_TOKEN, (b) => /don't know who you are/.test(b.text) && b.chat_id === String(MALLORY))).toBeTruthy();
    const n = calls.length;
    expect((await hook(u)).body).toMatchObject({ duplicate: true });
    expect(calls.length).toBe(n);
    // Groups are not served.
    await hook({ update_id: ++updateId, message: { message_id: ++messageId, from: { id: ALICE, is_bot: false }, chat: { id: -100, type: "group" }, text: "hi" } });
    expect(calls.length).toBe(n);
  });

  it("links a chat with a one-time code made in the app, through a deep link", async () => {
    const r = await alice.chat.startChatLink({ provider: ChatProvider.TELEGRAM });
    expect(r.code).toMatch(/^[A-Z0-9]{4}-[A-Z0-9]{4}$/);
    expect(r.deepLink).toBe(`https://t.me/org_bot?start=${r.code}`);
    calls = [];
    await hook(message(ALICE, `/start ${r.code}`));
    expect(sent(ORG_TOKEN, (b) => /Linked to your .* \(alice@test\.local\)/.test(b.text))).toBeTruthy();
    const mine = await alice.chat.listMyChatIdentities({});
    expect(mine.availableProviders).toContain(ChatProvider.TELEGRAM);
    expect(mine.telegramBotUsername).toBe("org_bot");
    expect(mine.identities).toMatchObject([{ provider: ChatProvider.TELEGRAM, teamId: "111111", externalUserId: String(ALICE), linkedVia: "code", displayName: `U${ALICE}` }]);
    expect((await admin.chat.listMyChatIdentities({})).identities).toEqual([]);
    // Single use.
    calls = [];
    await hook(message(MALLORY, `/link ${r.code}`));
    expect(sent(ORG_TOKEN, (b) => /not valid or has expired/.test(b.text))).toBeTruthy();
    const audit = await admin.audit.listAuditEvents({ filter: { action: "chat.link" } });
    expect(audit.events.some((e) => e.targetId === String(ALICE))).toBe(true);
    expect(JSON.stringify(audit)).not.toContain(r.code);
    // Help, linked.
    calls = [];
    await hook(message(ALICE, "/help"));
    expect(sent(ORG_TOKEN, (b) => /\/new <task>/.test(b.text))).toBeTruthy();
  });

  it("starts a session, edits one progress message, and takes permission answers from the owner only", async () => {
    const bobCode = (await bob.chat.startChatLink({ provider: ChatProvider.TELEGRAM })).code;
    await hook(message(BOB, `/start ${bobCode}`));
    const host = new FakeHost(alice, ["/work/proj", "/work/other"]);
    await host.start();
    calls = [];
    await hook(message(ALICE, "fix the failing tests"));
    const spawn = await waitFor(() => host.commands.find((c) => c.payload.case === "spawnSession"), "spawn");
    const sp = spawn.payload.value as { create?: { cwd: string; permissionMode: PermissionMode }; initialMessage?: { content: { block: { value: { text: string } } }[] } };
    expect(sp.create?.cwd).toBe("/work/proj");
    expect(sp.initialMessage?.content[0]?.block.value.text).toBe("fix the failing tests");
    const starting = await waitFor(() => sent(ORG_TOKEN, (b) => /Starting a session on .* in \/work\/proj/.test(b.text) && b.chat_id === String(ALICE)), "starting");

    await host.publish(
      { case: "turnStarted", value: {} },
      { case: "toolCallStarted", value: { toolUseId: "t1", name: "Bash", summary: "Run go test ./..." } },
      { case: "toolCallStarted", value: { toolUseId: "t2", name: "Read", summary: "Read main.go" } },
      { case: "permissionRequest", value: { requestId: "perm-1", toolName: "Bash", inputJson: JSON.stringify({ command: "go test ./..." }), suggestedRules: ["Bash(go test *)"] } },
    );
    const prompt = await waitFor(() => sent(ORG_TOKEN, (b) => /Permission needed: Bash/.test(b.text)), "permission prompt");
    const rows = prompt.body.reply_markup.inline_keyboard as { text: string; callback_data: string }[][];
    const buttons = rows.flat();
    expect(buttons.map((b) => b.text)).toEqual(["Allow", "Deny", "Always allow Bash(go test *)"]);
    for (const b of buttons) expect(Buffer.byteLength(b.callback_data)).toBeLessThanOrEqual(64);
    // Progress is one message, edited (the "Starting" message), not a new message per tool.
    await waitFor(() => calls.some((c) => c.method === "editMessageText" && /Read main\.go/.test(c.body.text)), "progress edit");
    const progressIds = new Set(calls.filter((c) => c.method === "editMessageText" && /^Working/.test(c.body.text)).map((c) => c.body.message_id));
    expect([...progressIds]).toEqual([progressIdOf(starting)]);
    expect(calls.filter((c) => c.method === "sendMessage" && /^Working/.test(c.body.text))).toHaveLength(0);

    const press = (from: number, chat: number, data: string, id: string) =>
      hook({ update_id: ++updateId, callback_query: { id, from: { id: from }, message: { message_id: Number(prompt.body.message_id ?? 0), chat: { id: chat, type: "private" } }, data } });
    // Bob is linked but it is not his session (and not his chat).
    calls = [];
    await press(BOB, ALICE, buttons[0]!.callback_data, "q-bob");
    expect(calls.find((c) => c.method === "answerCallbackQuery")?.body).toMatchObject({ callback_query_id: "q-bob", text: "Only the person whose session this is can answer." });
    await press(BOB, BOB, buttons[0]!.callback_data, "q-bob-2");
    expect(calls.filter((c) => c.method === "answerCallbackQuery")).toHaveLength(2);
    expect(host.commands.some((c) => c.payload.case === "respondToPermission")).toBe(false);

    await press(ALICE, ALICE, buttons[0]!.callback_data, "q-alice");
    const answer = await waitFor(() => host.commands.find((c) => c.payload.case === "respondToPermission"), "answer");
    expect(answer.payload.value).toMatchObject({ sessionId: host.sessionId, requestId: "perm-1", decision: RespondToPermissionRequest_Decision.ALLOW });
    expect(calls.find((c) => c.method === "answerCallbackQuery" && c.body.callback_query_id === "q-alice")).toBeTruthy();
    await host.publish({ case: "permissionResolved", value: { requestId: "perm-1", decision: RespondToPermissionRequest_Decision.ALLOW, resolvedBy: "relay:telegram" } });
    const resolved = await waitFor(() => calls.find((c) => c.method === "editMessageText" && /Allowed$/.test(c.body.text)), "resolved prompt");
    expect(resolved.body.reply_markup).toEqual({ inline_keyboard: [] });

    // A message steers the chat's session.
    await hook(message(ALICE, "also run the linter"));
    const steer = await waitFor(() => host.commands.find((c) => c.payload.case === "sendUserMessage"), "steering message");
    expect((steer.payload.value as { content: { block: { value: { text: string } } }[] }).content[0]?.block.value.text).toBe("also run the linter");

    calls = [];
    await host.publish(
      { case: "assistantMessage", value: { messageId: "m1", content: [{ block: { case: "text", value: { text: "All tests pass now." } } }] } },
      { case: "turnFinished", value: { result: 1 } },
    );
    await waitFor(() => sent(ORG_TOKEN, (b) => b.text === "All tests pass now." && b.chat_id === String(ALICE)), "final answer");
    expect(calls.find((c) => c.method === "editMessageText" && /^Done \(2 steps\)\.$/.test(c.body.text))).toBeTruthy();

    // /new starts another session; the chat steers that one from then on.
    const before = host.commands.filter((c) => c.payload.case === "spawnSession").length;
    await hook(message(ALICE, "/new write the changelog"));
    await waitFor(() => host.commands.filter((c) => c.payload.case === "spawnSession").length > before, "second spawn");
    const threads = await getDb().select().from(schema.chatThread).where(eq(schema.chatThread.channel, String(ALICE)));
    expect(threads.map((t) => t.status).sort()).toEqual(["active", "detached"]);

    const audit = await admin.audit.listAuditEvents({ filter: { action: "chat.session_start" } });
    expect(audit.events.filter((e) => e.targetType === "session").length).toBeGreaterThanOrEqual(2);
    expect(JSON.stringify(audit)).not.toContain("fix the failing tests");
    host.stop();
  });

  it("never grants bypassPermissions, whatever the chat defaults say", async () => {
    const host = new FakeHost(alice, ["/work/proj"]);
    await host.start();
    await putSettings(aliceToken, { deviceId: "", folder: "", permissionMode: "bypassPermissions" });
    await hook(message(ALICE, "/new risky task"));
    const spawn = await waitFor(() => host.commands.find((c) => c.payload.case === "spawnSession"), "spawn");
    expect((spawn.payload.value as { create?: { permissionMode: PermissionMode } }).create?.permissionMode).toBe(PermissionMode.DEFAULT);
    await putSettings(aliceToken, {});
    host.stop();
  });

  it("starts on an always-on host from the chat defaults and follows the work to its session", async () => {
    const made = await alice.hosts.createAgentHost({ name: "alice-box", scope: AgentHostScope.USER, maxSessions: 2 });
    const hostId = made.host!.id;
    await putSettings(aliceToken, { agentHostId: hostId, repositoryKey: "127.0.0.1/acme/widgets", permissionMode: "acceptEdits" });
    calls = [];
    await hook(message(ALICE, "/new add a README"));
    const queuedMsg = await waitFor(() => sent(ORG_TOKEN, (b) => /Queued on the always-on host alice-box for 127\.0\.0\.1\/acme\/widgets/.test(b.text)), "queued");
    const work = await waitFor(async () => (await getDb().select().from(schema.hostWork).where(eq(schema.hostWork.hostId, hostId)))[0], "host work");
    expect(work).toMatchObject({ prompt: "add a README", permissionMode: PermissionMode.ACCEPT_EDITS, repositoryKey: "127.0.0.1/acme/widgets" });
    // The daemon claims it and reports the session (as hosts.test.ts does through HostAgentService).
    await getDb().update(schema.hostWork).set({ state: HostWorkState.RUNNING, sessionId: "ses-on-host" }).where(eq(schema.hostWork.id, work.id));
    const started = await waitFor(() => calls.find((c) => c.method === "editMessageText" && /Session started on the always-on host/.test(c.body.text)), "started");
    expect(started.body.message_id).toBe(progressIdOf(queuedMsg));
    const [thread] = await getDb().select().from(schema.chatThread).where(eq(schema.chatThread.hostWorkId, work.id));
    expect(thread).toMatchObject({ status: "active", sessionId: "ses-on-host" });
    const audit = await admin.audit.listAuditEvents({ filter: { action: "chat.session_start" } });
    expect(audit.events.some((e) => e.targetType === "host_work" && e.targetId === work.id)).toBe(true);
    stopChatWatchers();
    await putSettings(aliceToken, {});
  });

  it("lets a person bring their own bot, polled, for themselves only", async () => {
    const cur = (await admin.policy.getManagedPolicy({})).policy!;
    await admin.policy.setManagedPolicy({ policy: { ...cur, personalChatBotsDisabled: true } });
    expect((await bob.chat.getMyChatBot({ provider: ChatProvider.TELEGRAM })).allowed).toBe(false);
    await expectError(bob.chat.putMyChatBot({ provider: ChatProvider.TELEGRAM, botToken: BOB_TOKEN, enabled: true }), Code.PermissionDenied);
    await admin.policy.setManagedPolicy({ policy: { ...cur, personalChatBotsDisabled: false } });

    await expectError(bob.chat.putMyChatBot({ provider: ChatProvider.TELEGRAM, botToken: ORG_TOKEN, enabled: true }), Code.Aborted);
    calls = [];
    const r = await bob.chat.putMyChatBot({ provider: ChatProvider.TELEGRAM, botToken: BOB_TOKEN, enabled: true, delivery: ChatDelivery.POLLING });
    expect(r.bot).toMatchObject({ enabled: true, botUserId: "222222", botUsername: "bob_own_bot", activeDelivery: ChatDelivery.POLLING });
    expect(JSON.stringify(r.bot)).not.toContain(BOB_TOKEN);
    expect(calls.some((c) => c.token === BOB_TOKEN && c.method === "deleteWebhook")).toBe(true);
    expect(isPolling(r.bot!.id)).toBe(true);
    // Owner only: nobody else sees it, admins included.
    expect((await admin.chat.getMyChatBot({ provider: ChatProvider.TELEGRAM })).bot).toBeUndefined();
    expect((await alice.chat.getMyChatBot({ provider: ChatProvider.TELEGRAM })).bot).toBeUndefined();
    await expectError(alice.chat.deleteMyChatBot({ provider: ChatProvider.TELEGRAM }), Code.NotFound);
    expect((await admin.audit.listAuditEvents({ filter: { action: "chat.bot_update" } })).events.length).toBeGreaterThan(0);

    const link = await bob.chat.startChatLink({ provider: ChatProvider.TELEGRAM, personalBot: true });
    expect(link.deepLink).toBe(`https://t.me/bob_own_bot?start=${link.code}`);
    calls = [];
    (queued[BOB_TOKEN] ??= []).push(message(BOB, `/start ${link.code}`));
    await waitFor(() => sent(BOB_TOKEN, (b) => /Linked to your .* \(bob@test\.local\)/.test(b.text)), "linked through the personal bot");
    expect((await bob.chat.listMyChatIdentities({})).identities.some((i) => i.teamId === "222222")).toBe(true);

    // Someone else's code on Bob's bot links nothing.
    const aliceCode = (await alice.chat.startChatLink({ provider: ChatProvider.TELEGRAM })).code;
    queued[BOB_TOKEN]!.push(message(ALICE, `/start ${aliceCode}`));
    await waitFor(() => sent(BOB_TOKEN, (b) => /belongs to someone else/.test(b.text) && b.chat_id === String(ALICE)), "refused link");
    expect((await alice.chat.listMyChatIdentities({})).identities.some((i) => i.teamId === "222222")).toBe(false);

    // Bob has no machine taking remote starts: the bot says so.
    queued[BOB_TOKEN]!.push(message(BOB, "do something"));
    await waitFor(() => sent(BOB_TOKEN, (b) => /None of your machines is online/.test(b.text)), "no machine");

    // Off by policy: the personal bot answers that it is off.
    await admin.policy.setManagedPolicy({ policy: { ...cur, personalChatBotsDisabled: true } });
    queued[BOB_TOKEN]!.push(message(BOB, "/new another"));
    await waitFor(() => sent(BOB_TOKEN, (b) => /personal chat bots off/.test(b.text)), "policy notice");
    await admin.policy.setManagedPolicy({ policy: { ...cur, personalChatBotsDisabled: false } });

    calls = [];
    await bob.chat.deleteMyChatBot({ provider: ChatProvider.TELEGRAM });
    expect(isPolling(r.bot!.id)).toBe(false);
    expect(calls.some((c) => c.token === BOB_TOKEN && c.method === "deleteWebhook")).toBe(true);
    expect((await bob.chat.listMyChatIdentities({})).identities.some((i) => i.teamId === "222222")).toBe(false);
    expect((await admin.audit.listAuditEvents({ filter: { action: "chat.bot_delete" } })).events.length).toBeGreaterThan(0);
  });

  it("turns off with disableChatApps and audits unlinking", async () => {
    const cur = (await admin.policy.getManagedPolicy({})).policy!;
    await admin.policy.setManagedPolicy({ policy: { ...cur, chatAppsDisabled: true } });
    expect((await alice.chat.listMyChatIdentities({})).available).toBe(false);
    calls = [];
    await hook(message(ALICE, "/new third task"));
    expect(sent(ORG_TOKEN, (b) => /turned off/.test(b.text))).toBeTruthy();
    await admin.policy.setManagedPolicy({ policy: { ...cur, chatAppsDisabled: false } });

    await alice.chat.unlinkChatIdentity({ provider: ChatProvider.TELEGRAM, teamId: "111111", externalUserId: String(ALICE) });
    expect((await alice.chat.listMyChatIdentities({})).identities).toEqual([]);
    expect((await admin.audit.listAuditEvents({ filter: { action: "chat.unlink" } })).events.length).toBeGreaterThan(0);
    calls = [];
    await hook(message(ALICE, "/new after unlinking"));
    expect(sent(ORG_TOKEN, (b) => /don't know who you are/.test(b.text))).toBeTruthy();

    await admin.chat.deleteChatApp({ provider: ChatProvider.TELEGRAM });
    expect(calls.some((c) => c.token === ORG_TOKEN && c.method === "deleteWebhook")).toBe(true);
    expect((await hook(message(ALICE, "hello"))).status).toBe(404);
  });
});
