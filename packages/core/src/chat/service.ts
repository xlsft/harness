/**
 * Chat apps, provider-independent (docs/design/chat.md): inbound requests (verified, deduplicated, replays refused), linking,
 * starting a session on one of the person's machines (or an always-on host) through the relay, steering it from the thread
 * or chat, and following it to render progress, permission prompts and answers.
 *
 * Sessions are followed with the relay's own history-then-live subscription from the last seq the thread rendered, so a
 * restart of the server resumes where it stopped (resumeChatWatchers).
 */
import { create } from "@bufbuild/protobuf";
import { and, desc, eq, inArray, lt } from "drizzle-orm";
import { record as audit } from "../audit/log.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { PermissionMode, RespondToPermissionRequest_Decision, RespondToPermissionRequestSchema, SendUserMessageRequestSchema, type SessionEvent } from "../gen/harness/agent/v1/agent_pb.ts";
import { ChatProvider as ChatProviderId } from "../gen/harness/chat/v1/chat_pb.ts";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { HostWorkState, StartHostWorkRequestSchema } from "../gen/harness/relay/v1/hosts_pb.ts";
import { CommandAck_Outcome, CommandOptionsSchema, DeliveryState, SpawnSessionCommandSchema } from "../gen/harness/relay/v1/relay_pb.ts";
import { DocumentKind } from "../gen/harness/sync/v1/sync_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { newId, sha256 } from "../lib/ids.ts";
import { startHostWork } from "../relay/hosts.ts";
import { listHostFolders, remoteStartHosts, requireRemoteStartsAllowed, sendSessionCommand, spawnSession, subscribeSession } from "../relay/service.ts";
import { toPublicUser, type Principal } from "../services/devices.ts";
import { getDocument } from "../services/sync.ts";
import { chatAppsDisabledByPolicy, identityOf, linkIdentity, loadApp, noteAppEvent, personalBotsDisabledByPolicy, providerOf, redeemCode, type ChatAppCtx } from "./app.ts";
import { buttonValue } from "./buttons.ts";
import type { ChatInbound, ChatPlace, ChatProvider, ChatProviderClient } from "./types.ts";

type ThreadRow = typeof schema.chatThread.$inferSelect;
type Message = Extract<ChatInbound, { kind: "message" }>;
type Action = Extract<ChatInbound, { kind: "action" }>;

export interface InboundResult {
  status: number;
  body: Record<string, unknown>;
}

const DELIVERY_RETENTION_MS = 24 * 3_600_000;
const PROGRESS_MIN_MS = () => Number(process.env.HARNESS_CHAT_PROGRESS_MS ?? "") || 2_000;
const HOST_WORK_POLL_MS = () => Number(process.env.HARNESS_CHAT_HOST_WORK_POLL_MS ?? "") || 2_000;
const PRODUCT = "xlsft`s harness";
/** Where people link a chat account, choose where chat sessions run and connect their own bot. */
const SETTINGS = "Settings > Remote";

// --- background work -----------------------------------------------------------------------------

const pending = new Set<Promise<unknown>>();

function background(work: () => Promise<unknown>): void {
  const p = work()
    .catch((err) => console.error(`[harness] chat: ${err instanceof Error ? err.message : String(err)}`))
    .finally(() => pending.delete(p));
  pending.add(p);
}

/** Resolves when no inbound work is pending (tests). */
export async function chatIdle(): Promise<void> {
  while (pending.size) await Promise.allSettled([...pending]);
}

/** Records a key once; false when it was already there. */
export async function claim(key: string): Promise<boolean> {
  const rows = await getDb().insert(schema.chatDelivery).values({ key, createdAt: new Date() }).onConflictDoNothing().returning({ key: schema.chatDelivery.key });
  return rows.length > 0;
}

let lastPrune = 0;
async function prune(): Promise<void> {
  if (Date.now() - lastPrune < 3_600_000) return;
  lastPrune = Date.now();
  await getDb().delete(schema.chatDelivery).where(lt(schema.chatDelivery.createdAt, new Date(Date.now() - DELIVERY_RETENTION_MS)));
}

// --- inbound -------------------------------------------------------------------------------------

/**
 * An inbound request of a chat app: verify, refuse replays, answer quickly and do the work in the background. `appKey`
 * names a personal bot ("" or "org": the organization's app).
 */
export async function handleInbound(providerId: number, headers: Headers, raw: string, appKey = ""): Promise<InboundResult> {
  const provider = providerOf(providerId);
  await ensureDb();
  const app = await loadApp(providerId, appKey);
  if (!app) return { status: 404, body: { error: `no ${provider.name} app is connected` } };
  const v = provider.verify(headers, raw, app.secrets.signingSecret ?? "");
  if (!v.ok) return { status: v.status ?? 401, body: { error: v.reason ?? "unverified" } };
  await prune().catch(() => {});
  if (v.replayKey && !(await claim(v.replayKey))) return { status: 401, body: { error: "replayed request" } };
  if (v.deliveryId && !(await claim(v.deliveryId))) return { status: 200, body: { ok: true, duplicate: true } };
  const items = provider.parse(headers, raw, app.botUserId);
  const challenge = items.find((i) => i.kind === "challenge");
  if (challenge) return { status: 200, body: { challenge: challenge.challenge } };
  await processItems(provider, app, items);
  return { status: 200, body: { ok: true } };
}

/** Verified, deduplicated items (from a webhook, or from long polling): the work happens in the background. */
export async function processItems(provider: ChatProvider, app: ChatAppCtx, items: ChatInbound[]): Promise<void> {
  await noteAppEvent(provider.id, "", app.key);
  const work = items.filter((i) => i.kind === "message" || i.kind === "action");
  if (!work.length) return;
  const client = provider.client(app.secrets as Record<string, string>);
  background(async () => {
    for (const it of work) {
      try {
        if (it.kind === "message") await onMessage(provider, app, client, it);
        else if (it.kind === "action") await onAction(provider, app, client, it);
      } catch (err) {
        await noteAppEvent(provider.id, err instanceof Error ? err.message : String(err), app.key).catch(() => {});
        throw err;
      }
    }
  });
}

async function principalOf(userId: string): Promise<Principal | undefined> {
  const u = await getDb().query.user.findFirst({ where: eq(schema.user.id, userId) });
  if (!u || u.banned) return undefined;
  return { user: toPublicUser(u), via: "session" };
}

async function directorySourced(userId: string): Promise<boolean> {
  const rows = await getDb().select({ providerId: schema.account.providerId }).from(schema.account).where(eq(schema.account.userId, userId));
  if (rows.some((r) => r.providerId === "ldap" || r.providerId.startsWith("sso:"))) return true;
  return !!(await getDb().query.scimIdentity.findFirst({ where: eq(schema.scimIdentity.userId, userId) }));
}

/** The account a chat user is, linking by email through the directory when the app allows it. */
async function accountOf(provider: ChatProvider, app: ChatAppCtx, client: ChatProviderClient, place: ChatPlace, chatUserId: string): Promise<Principal | undefined> {
  const linked = await identityOf(provider.id, place.teamId, chatUserId);
  if (linked) return principalOf(linked.userId);
  if (!app.matchByEmail) return undefined;
  const profile = await client.profile(chatUserId).catch(() => undefined);
  if (!profile?.email || profile.bot) return undefined;
  const u = await getDb().query.user.findFirst({ where: eq(schema.user.email, profile.email.toLowerCase()) });
  if (!u || u.banned) return undefined;
  if (!u.emailVerified && !(await directorySourced(u.id))) return undefined;
  await linkIdentity({ provider: provider.id, teamId: place.teamId, externalUserId: chatUserId, userId: u.id, displayName: profile.name, via: "directory" });
  await audit({ action: "chat.link", actor: { id: u.id, label: u.email }, target: { type: "chat_identity", id: chatUserId }, details: { provider: provider.name, team_id: place.teamId, via: "directory" } });
  return principalOf(u.id);
}

const LIVE = ["starting", "active"];

/** The thread a message belongs to: its own thread (Slack), or the chat's latest live session (Telegram). */
async function threadAt(provider: ChatProvider, place: ChatPlace): Promise<ThreadRow | undefined> {
  const t = schema.chatThread;
  if (provider.threading === "chat") {
    const [row] = await getDb()
      .select()
      .from(t)
      .where(and(eq(t.provider, provider.id), eq(t.teamId, place.teamId), eq(t.channel, place.channel), inArray(t.status, LIVE)))
      .orderBy(desc(t.createdAt))
      .limit(1);
    return row;
  }
  return getDb().query.chatThread.findFirst({
    where: and(eq(t.provider, provider.id), eq(t.teamId, place.teamId), eq(t.channel, place.channel), eq(t.threadId, place.threadId)),
  });
}

/** Stops following a chat's live sessions (a "new" command): the sessions go on on their machines. */
async function detachChat(provider: ChatProvider, place: ChatPlace): Promise<void> {
  const t = schema.chatThread;
  const rows = await getDb()
    .update(t)
    .set({ status: "detached", updatedAt: new Date() })
    .where(and(eq(t.provider, provider.id), eq(t.teamId, place.teamId), eq(t.channel, place.channel), inArray(t.status, LIVE)))
    .returning({ id: t.id });
  for (const r of rows) {
    watchers.get(r.id)?.ctl.abort();
    hostWaits.get(r.id)?.abort();
  }
}

const frontendOf = (provider: ChatProvider) => provider.name.toLowerCase();

const options = (provider: ChatProvider, idempotencyKey: string) => create(CommandOptionsSchema, { idempotencyKey, frontend: frontendOf(provider) });

const textContent = (text: string) => [{ block: { case: "text" as const, value: { text } } }];

function helpText(provider: ChatProvider, linked: boolean): string {
  const lines = [
    `I start ${PRODUCT} sessions on your own machine (or an always-on host) and report back here.`,
    linked ? `${provider.startHint} with a task to start one; later messages steer it.` : `First link your account: create a code under ${SETTINGS} in ${PRODUCT} and send it to me as "link <code>".`,
  ];
  if (provider.threading === "chat") lines.push("/new <task> starts another session; the one before keeps running on its machine.");
  lines.push(`Where sessions run is set under ${SETTINGS}.`);
  return lines.join("\n");
}

async function onMessage(provider: ChatProvider, app: ChatAppCtx, client: ChatProviderClient, m: Message): Promise<void> {
  const reply = (text: string) => client.post(m.place, { text });
  const link = /^link\s+([A-Za-z0-9-]{8,12})\s*$/i.exec(m.text);
  if (link && m.addressed) {
    if (!(await claim(m.messageKey))) return;
    const userId = await redeemCode(provider.id, link[1]!);
    if (!userId) {
      await reply(`That link code is not valid or has expired. Create a new one under ${SETTINGS}.`);
      return;
    }
    if (app.ownerUserId && userId !== app.ownerUserId) {
      await reply("This bot belongs to someone else. Link your account through your organization's bot or a bot of your own.");
      return;
    }
    const name = m.userName || (await client.profile(m.userId).catch(() => undefined))?.name || "";
    await linkIdentity({ provider: provider.id, teamId: m.place.teamId, externalUserId: m.userId, userId, displayName: name, via: "code" });
    const p = await principalOf(userId);
    await audit({
      action: "chat.link",
      actor: { id: userId, label: p?.user.email ?? "" },
      target: { type: "chat_identity", id: m.userId },
      details: { provider: provider.name, team_id: m.place.teamId, via: "code", ...(app.key ? { personal_bot: app.key } : {}) },
    });
    await reply(`Linked to your ${PRODUCT} account${p ? ` (${p.user.email})` : ""}. ${provider.startHint} to start a session.`);
    return;
  }
  const thread = m.command ? undefined : await threadAt(provider, m.place);
  if (!thread && !m.addressed) return;
  if (!(await claim(m.messageKey))) return;
  if (!app.enabled || (await chatAppsDisabledByPolicy("user"))) {
    if (m.addressed) await client.ephemeral(m.place, m.userId, `Starting sessions from ${provider.name} is turned off on this ${PRODUCT} server.`).catch(() => {});
    return;
  }
  const p = await accountOf(provider, app, client, m.place, m.userId);
  if (m.command === "help") {
    await reply(helpText(provider, !!p));
    return;
  }
  if (!p) {
    if (m.addressed) {
      await client.ephemeral(m.place, m.userId, `I don't know who you are in ${PRODUCT} yet. Create a link code under ${SETTINGS} and send it to me as "link <code>".`);
    }
    return;
  }
  if (await chatAppsDisabledByPolicy(p.user.role)) {
    if (m.addressed) await client.ephemeral(m.place, m.userId, `Your organization's policy turned chat apps off.`).catch(() => {});
    return;
  }
  if (app.ownerUserId) {
    // A personal bot works for its owner only, and only while the policy allows personal bots.
    if (p.user.id !== app.ownerUserId) {
      await client.ephemeral(m.place, m.userId, "This bot belongs to someone else.").catch(() => {});
      return;
    }
    if (await personalBotsDisabledByPolicy(p.user.role)) {
      await client.ephemeral(m.place, m.userId, `Your organization's policy turned personal chat bots off. Use your organization's bot.`).catch(() => {});
      return;
    }
  }
  if (thread) {
    // Only the person whose session it is steers it; everyone else may talk in the thread.
    if (thread.userId !== p.user.id) {
      if (m.addressed) await client.ephemeral(m.place, m.userId, `This thread steers someone else's session. ${provider.startHint} in a new message to start your own.`).catch(() => {});
      return;
    }
    await steer(provider, client, thread, p, m);
    return;
  }
  if (m.command === "new") await detachChat(provider, m.place);
  await startFromChat(provider, app, client, p, m);
}

async function steer(provider: ChatProvider, client: ChatProviderClient, thread: ThreadRow, p: Principal, m: Message): Promise<void> {
  if (thread.status === "starting") {
    await client.ephemeral(m.place, m.userId, "The session is still starting; send that again in a moment.");
    return;
  }
  if (thread.status !== "active") {
    await client.post(m.place, { text: `This session has ended. ${provider.startHint} in a new message to start another.` });
    return;
  }
  if (!m.text) return;
  try {
    const r = await sendSessionCommand(
      p,
      { case: "sendUserMessage", value: create(SendUserMessageRequestSchema, { sessionId: thread.sessionId, uuid: `chat-${sha256(m.messageKey).slice(0, 32)}`, content: textContent(m.text) }) },
      options(provider, `chat:${m.messageKey}`),
      "optional",
      frontendOf(provider),
    );
    if (r.delivery === DeliveryState.EXPIRED) await client.post(m.place, { text: "The machine did not take the message in time." });
  } catch (err) {
    await client.post(m.place, { text: `Could not send that to the session: ${err instanceof ApiError ? err.message : "error"}` });
  }
}

// --- where a session runs ------------------------------------------------------------------------

interface ChatDefaults {
  deviceId: string;
  folder: string;
  permissionMode: string;
  agentHostId: string;
  repositoryKey: string;
}

/** Never bypassPermissions: a chat app cannot grant it, whatever the defaults say. */
const MODES: Record<string, PermissionMode> = {
  default: PermissionMode.DEFAULT,
  acceptEdits: PermissionMode.ACCEPT_EDITS,
  plan: PermissionMode.PLAN,
  auto: PermissionMode.AUTO,
  dontAsk: PermissionMode.DONT_ASK,
};

/** The person's chat defaults from their synced settings (chatDefaults); empty fields when unset. */
async function chatDefaultsOf(p: Principal): Promise<ChatDefaults> {
  const empty = { deviceId: "", folder: "", permissionMode: "", agentHostId: "", repositoryKey: "" };
  try {
    const doc = await getDocument(p, { ref: { scope: Scope.USER, kind: DocumentKind.SETTINGS, key: "settings", projectKey: "" } });
    const v = (JSON.parse(doc.content) as { chatDefaults?: Record<string, unknown> }).chatDefaults;
    if (!v || typeof v !== "object") return empty;
    const s = (k: string) => (typeof v[k] === "string" ? (v[k] as string) : "");
    return { deviceId: s("deviceId"), folder: s("folder"), permissionMode: s("permissionMode"), agentHostId: s("agentHostId"), repositoryKey: s("repositoryKey") };
  } catch {
    return empty;
  }
}

type Target =
  | { kind: "machine"; hostId: string; name: string; folder: string; mode: PermissionMode }
  | { kind: "agentHost"; agentHostId: string; repositoryKey: string; mode: PermissionMode };

async function chooseTarget(p: Principal, frontend: string): Promise<Target | string> {
  const d = await chatDefaultsOf(p);
  if (d.agentHostId) {
    if (!d.repositoryKey) return `Your chat defaults name an always-on host but no repository. Choose one under ${SETTINGS}.`;
    // Unspecified: the host's own default.
    return { kind: "agentHost", agentHostId: d.agentHostId, repositoryKey: d.repositoryKey, mode: MODES[d.permissionMode] ?? PermissionMode.UNSPECIFIED };
  }
  const hosts = await remoteStartHosts(p.user.id);
  const mode = MODES[d.permissionMode] ?? PermissionMode.DEFAULT;
  const host = d.deviceId ? hosts.find((h) => h.deviceId === d.deviceId) : hosts[0];
  if (!host) {
    return d.deviceId
      ? `The machine in your chat defaults is offline or does not take remote starts. Change it under ${SETTINGS}, or turn remote starts on there.`
      : `None of your machines is online and taking remote starts. Turn "Allow remote starts" on in ${PRODUCT} on a machine (${SETTINGS}) and keep it online, or choose an always-on host there.`;
  }
  let folder = d.deviceId === host.deviceId ? d.folder : "";
  if (!folder) {
    if (host.remoteStarts) {
      const folders = await listHostFolders(p, host.id, 1, frontend).catch(() => []);
      folder = folders.find((f) => f.exists)?.path ?? folders[0]?.path ?? "";
    } else {
      folder = (Array.isArray(host.allowedRoots) ? (host.allowedRoots as string[]) : [])[0] ?? "";
    }
  }
  if (!folder) return `${host.name || "Your machine"} has no recent folder to start in. Open a session there once, or choose a folder in your chat defaults.`;
  return { kind: "machine", hostId: host.id, name: host.name, folder, mode };
}

async function startFromChat(provider: ChatProvider, app: ChatAppCtx, client: ChatProviderClient, p: Principal, m: Message): Promise<void> {
  if (!m.text) {
    await client.post(m.place, { text: `What should I do? ${provider.startHint} with a task.` });
    return;
  }
  const now = new Date();
  const id = newId("cht");
  const inserted = await getDb()
    .insert(schema.chatThread)
    .values({ id, provider: provider.id, teamId: m.place.teamId, channel: m.place.channel, threadId: m.place.threadId, userId: p.user.id, status: "starting", appKey: app.key, createdAt: now, updatedAt: now })
    .onConflictDoNothing()
    .returning();
  const thread = inserted[0];
  if (!thread) return; // another message of the same thread is starting it
  const fail = async (text: string) => {
    await getDb().update(schema.chatThread).set({ status: "failed", updatedAt: new Date() }).where(eq(schema.chatThread.id, id));
    await client.post(m.place, { text });
  };
  try {
    await requireRemoteStartsAllowed(p);
  } catch (err) {
    return fail(`Could not start a session: ${err instanceof ApiError ? err.message : "error"}`);
  }
  const frontend = frontendOf(provider);
  const target = await chooseTarget(p, frontend);
  if (typeof target === "string") return fail(target);
  const startDetails = { provider: provider.name, team_id: m.place.teamId, channel: m.place.channel, ...(app.key ? { personal_bot: app.key } : {}) };

  if (target.kind === "agentHost") {
    let work;
    try {
      work = await startHostWork(
        p,
        create(StartHostWorkRequestSchema, {
          hostId: target.agentHostId,
          repositoryKey: target.repositoryKey,
          prompt: m.text,
          title: m.text.slice(0, 80),
          permissionMode: target.mode,
          idempotencyKey: `chat:spawn:${id}`,
        }),
      );
    } catch (err) {
      return fail(`Could not start the session on the always-on host: ${err instanceof ApiError ? err.message : "error"}`);
    }
    const progressId = await client.post(m.place, { text: `Queued on the always-on host${work.hostName ? ` ${work.hostName}` : ""} for ${target.repositoryKey}…` });
    await getDb().update(schema.chatThread).set({ hostWorkId: work.id ?? "", progressId, updatedAt: new Date() }).where(eq(schema.chatThread.id, id));
    await audit({
      action: "chat.session_start",
      actor: { id: p.user.id, label: p.user.email },
      target: { type: "host_work", id: work.id ?? "" },
      details: { ...startDetails, agent_host_id: target.agentHostId, repository: target.repositoryKey },
    });
    const row = await getDb().query.chatThread.findFirst({ where: eq(schema.chatThread.id, id) });
    if (row) waitForHostWork(row);
    return;
  }

  const progressId = await client.post(m.place, { text: `Starting a session on ${target.name || "your machine"} in ${target.folder}…` });
  try {
    const session = await spawnSession(
      p,
      target.hostId,
      create(SpawnSessionCommandSchema, {
        create: { cwd: target.folder, permissionMode: target.mode },
        initialMessage: { uuid: `chat-${sha256(m.messageKey).slice(0, 32)}`, content: textContent(m.text) },
      }),
      options(provider, `chat:spawn:${id}`),
      frontend,
      { frontend },
    );
    await getDb().update(schema.chatThread).set({ sessionId: session.id, hostId: target.hostId, status: "active", progressId, updatedAt: new Date() }).where(eq(schema.chatThread.id, id));
    await audit({
      action: "chat.session_start",
      actor: { id: p.user.id, label: p.user.email },
      target: { type: "session", id: session.id },
      details: { ...startDetails, host_id: target.hostId, folder: target.folder },
    });
    const row = await getDb().query.chatThread.findFirst({ where: eq(schema.chatThread.id, id) });
    if (row) watch(row);
  } catch (err) {
    const why = err instanceof ApiError ? err.message : "the machine did not start it";
    await getDb().update(schema.chatThread).set({ status: "failed", updatedAt: new Date() }).where(eq(schema.chatThread.id, id));
    await client.update(m.place, progressId, { text: `Could not start the session: ${why}` }).catch(() => client.post(m.place, { text: `Could not start the session: ${why}` }));
  }
}

// --- always-on hosts: waiting for the session ----------------------------------------------------

const hostWaits = new Map<string, AbortController>();

const placeOf = (t: ThreadRow): ChatPlace => ({ teamId: t.teamId, channel: t.channel, threadId: t.threadId });

/** Follows queued work until its session exists, then follows the session (idempotent per thread). */
function waitForHostWork(thread: ThreadRow): void {
  if (hostWaits.has(thread.id) || thread.status !== "starting" || !thread.hostWorkId) return;
  const ctl = new AbortController();
  hostWaits.set(thread.id, ctl);
  void runHostWait(thread, ctl.signal)
    .catch((err) => console.error(`[harness] chat host wait ${thread.id}: ${err instanceof Error ? err.message : String(err)}`))
    .finally(() => {
      if (hostWaits.get(thread.id) === ctl) hostWaits.delete(thread.id);
    });
}

async function runHostWait(thread: ThreadRow, signal: AbortSignal): Promise<void> {
  const done = [HostWorkState.DONE, HostWorkState.FAILED, HostWorkState.CANCELLED, HostWorkState.EXPIRED];
  while (!signal.aborted) {
    const w = await getDb().query.hostWork.findFirst({ where: eq(schema.hostWork.id, thread.hostWorkId) });
    const app = await loadApp(thread.provider, thread.appKey);
    const client = app ? providerOf(thread.provider).client(app.secrets as Record<string, string>) : undefined;
    if (w?.sessionId) {
      const [row] = await getDb()
        .update(schema.chatThread)
        .set({ sessionId: w.sessionId, status: "active", updatedAt: new Date() })
        .where(and(eq(schema.chatThread.id, thread.id), eq(schema.chatThread.status, "starting")))
        .returning();
      if (row) {
        if (client && row.progressId) await client.update(placeOf(row), row.progressId, { text: `Session started on the always-on host for ${w.repositoryKey}.` }).catch(() => {});
        row.progressId = "";
        await getDb().update(schema.chatThread).set({ progressId: "" }).where(eq(schema.chatThread.id, row.id));
        watch(row);
      }
      return;
    }
    if (!w || done.includes(w.state)) {
      await getDb().update(schema.chatThread).set({ status: "failed", updatedAt: new Date() }).where(eq(schema.chatThread.id, thread.id));
      const why = w?.error || (w ? "the host did not start it" : "the work is gone");
      if (client) await client.post(placeOf(thread), { text: `Could not start the session on the always-on host: ${why}` }).catch(() => {});
      return;
    }
    await new Promise((r) => setTimeout(r, HOST_WORK_POLL_MS()));
  }
}

// --- buttons -------------------------------------------------------------------------------------

interface ButtonValue {
  /** chat_thread id */
  t: string;
  /** permission request id */
  r: string;
  /** a: allow, A: always allow, d: deny */
  d: "a" | "A" | "d";
}

async function onAction(provider: ChatProvider, app: ChatAppCtx, client: ChatProviderClient, a: Action): Promise<void> {
  let note = "";
  try {
    note = (await answerButton(provider, app, a)) ?? "";
  } finally {
    // Telegram shows a spinner on the button until the press is answered; the notice goes with the answer.
    if (client.answerAction && a.ackId) await client.answerAction(a.ackId, note).catch(() => {});
    else if (note) await client.ephemeral(a.place, a.userId, note).catch(() => {});
  }
}

/** Answers a permission prompt for its owner; returns a notice for the presser, if any. */
async function answerButton(provider: ChatProvider, app: ChatAppCtx, a: Action): Promise<string | void> {
  let v: ButtonValue;
  try {
    v = JSON.parse(await buttonValue(a.value)) as ButtonValue;
  } catch {
    return;
  }
  if (!v?.t || !v.r) return;
  const thread = await getDb().query.chatThread.findFirst({ where: eq(schema.chatThread.id, v.t) });
  if (!thread || thread.provider !== provider.id || thread.channel !== a.place.channel || thread.teamId !== a.place.teamId || thread.appKey !== app.key) return;
  if (!app.enabled || (await chatAppsDisabledByPolicy("user"))) return;
  const id = await identityOf(provider.id, a.place.teamId, a.userId);
  if (!id || id.userId !== thread.userId) return "Only the person whose session this is can answer.";
  const p = await principalOf(thread.userId);
  if (!p || thread.status !== "active") return;
  if (await chatAppsDisabledByPolicy(p.user.role)) return "Your organization's policy turned chat apps off.";
  const state = watchers.get(thread.id);
  const prompt = state?.prompts.get(v.r);
  const decision = v.d === "d" ? RespondToPermissionRequest_Decision.DENY : v.d === "A" ? RespondToPermissionRequest_Decision.ALLOW_ALWAYS : RespondToPermissionRequest_Decision.ALLOW;
  try {
    const r = await sendSessionCommand(
      p,
      {
        case: "respondToPermission",
        value: create(RespondToPermissionRequestSchema, { sessionId: thread.sessionId, requestId: v.r, decision, rule: v.d === "A" ? (prompt?.rule ?? "") : "" }),
      },
      options(provider, `chat:perm:${thread.id}:${v.r}:${a.userId}`),
      "implied",
      frontendOf(provider),
    );
    if (r.ack?.outcome === CommandAck_Outcome.ALREADY_RESOLVED) return "That was already answered.";
  } catch (err) {
    return `Could not answer: ${err instanceof ApiError ? err.message : "error"}`;
  }
}

// --- following a session -------------------------------------------------------------------------

interface Prompt {
  messageId: string;
  title: string;
  rule: string;
}

interface WatchState {
  ctl: AbortController;
  prompts: Map<string, Prompt>;
  tools: string[];
  turnText: string;
  progressId: string;
  lastProgressAt: number;
  progressTimer?: ReturnType<typeof setTimeout>;
}

const watchers = new Map<string, WatchState>();

function lastText(ev: SessionEvent): string {
  if (ev.event.case !== "assistantMessage") return "";
  return ev.event.value.content
    .map((b) => (b.block.case === "text" ? b.block.value.text : ""))
    .filter(Boolean)
    .join("\n")
    .trim();
}

function progressText(s: WatchState): string {
  const recent = s.tools.slice(-5).map((t) => `• ${t}`);
  return ["Working…", ...recent].join("\n");
}

function permissionTitle(ev: Extract<SessionEvent["event"], { case: "permissionRequest" }>["value"]): string {
  let detail = "";
  try {
    const input = JSON.parse(ev.inputJson || "{}") as Record<string, unknown>;
    const v = input.command ?? input.file_path ?? input.path ?? input.url ?? input.pattern;
    if (typeof v === "string") detail = v.length > 300 ? `${v.slice(0, 300)}…` : v;
  } catch {
    // keep the tool name only
  }
  const who = ev.agentName ? ` (${ev.agentName})` : "";
  return `Permission needed${who}: ${ev.toolName}${detail ? `\n${detail}` : ""}${ev.reason ? `\n${ev.reason}` : ""}`;
}

const DECIDED: Record<number, string> = {
  [RespondToPermissionRequest_Decision.ALLOW]: "Allowed",
  [RespondToPermissionRequest_Decision.ALLOW_ALWAYS]: "Always allowed",
  [RespondToPermissionRequest_Decision.DENY]: "Denied",
};

async function render(client: ChatProviderClient, thread: ThreadRow, s: WatchState, ev: SessionEvent): Promise<"ended" | void> {
  const place = placeOf(thread);
  const e = ev.event;
  const flushProgress = async () => {
    if (s.progressTimer) clearTimeout(s.progressTimer);
    s.progressTimer = undefined;
    s.lastProgressAt = Date.now();
    if (s.progressId) await client.update(place, s.progressId, { text: progressText(s) }).catch(() => {});
    else s.progressId = await client.post(place, { text: progressText(s) });
  };
  switch (e.case) {
    case "turnStarted":
      s.tools = [];
      s.turnText = "";
      if (!s.progressId) await flushProgress();
      return;
    case "toolCallStarted": {
      s.tools.push(e.value.summary || e.value.name);
      const wait = PROGRESS_MIN_MS() - (Date.now() - s.lastProgressAt);
      if (wait <= 0) await flushProgress();
      else if (!s.progressTimer) s.progressTimer = setTimeout(() => void flushProgress().catch(() => {}), wait);
      return;
    }
    case "assistantMessage": {
      const t = lastText(ev);
      if (t) s.turnText = t;
      return;
    }
    case "permissionRequest": {
      const title = permissionTitle(e.value);
      const rule = !e.value.oneTimeOnly ? (e.value.suggestedRules[0] ?? "") : "";
      const value = (d: ButtonValue["d"]) => JSON.stringify({ t: thread.id, r: e.value.requestId, d } satisfies ButtonValue);
      const buttons = [
        { id: "allow", label: "Allow", value: value("a"), style: "primary" as const },
        ...(rule ? [{ id: "always", label: `Always allow ${rule}`.slice(0, 75), value: value("A") }] : []),
        { id: "deny", label: "Deny", value: value("d"), style: "danger" as const },
      ];
      const messageId = await client.post(place, { text: title, buttons });
      s.prompts.set(e.value.requestId, { messageId, title, rule });
      return;
    }
    case "permissionResolved": {
      const pr = s.prompts.get(e.value.requestId);
      if (!pr) return;
      s.prompts.delete(e.value.requestId);
      await client.update(place, pr.messageId, { text: `${pr.title}\n${DECIDED[e.value.decision] ?? "Answered"}` }).catch(() => {});
      return;
    }
    case "question":
      await client.post(place, { text: `The agent asks: ${e.value.items.map((q) => q.question).join(" / ")}\nAnswer it in ${PRODUCT}, or reply here to tell it something else.` });
      return;
    case "planProposed":
      await client.post(place, { text: `The agent proposes a plan. Review it in ${PRODUCT}.` });
      return;
    case "turnFinished": {
      if (s.progressTimer) clearTimeout(s.progressTimer);
      s.progressTimer = undefined;
      if (s.progressId) {
        const n = s.tools.length;
        await client.update(place, s.progressId, { text: n ? `Done (${n} ${n === 1 ? "step" : "steps"}).` : "Done." }).catch(() => {});
        s.progressId = "";
      }
      const r = e.value;
      const text = s.turnText || (r.errorMessage ? `The turn ended with an error: ${r.errorMessage}` : "The turn ended without an answer.");
      await client.post(place, { text });
      s.turnText = "";
      s.tools = [];
      return;
    }
    case "sessionEnded":
      await client.post(place, { text: "The session ended." });
      return "ended";
    default:
      return;
  }
}

/** Follows a thread's session until it ends (idempotent per thread). */
function watch(thread: ThreadRow): void {
  if (watchers.has(thread.id) || thread.status !== "active" || !thread.sessionId) return;
  const s: WatchState = { ctl: new AbortController(), prompts: new Map(), tools: [], turnText: "", progressId: thread.progressId, lastProgressAt: 0 };
  watchers.set(thread.id, s);
  void runWatch(thread, s).finally(() => {
    if (s.progressTimer) clearTimeout(s.progressTimer);
    if (watchers.get(thread.id) === s) watchers.delete(thread.id);
  });
}

async function runWatch(thread: ThreadRow, s: WatchState): Promise<void> {
  let lastSeq = BigInt(thread.lastSeq);
  const provider = providerOf(thread.provider);
  for (let attempt = 0; !s.ctl.signal.aborted; attempt++) {
    const app = await loadApp(thread.provider, thread.appKey);
    const p = await principalOf(thread.userId);
    if (!app || !p) return;
    const client = provider.client(app.secrets as Record<string, string>);
    try {
      for await (const item of subscribeSession(p, thread.sessionId, lastSeq, `chat-${thread.id}`, frontendOf(provider), s.ctl.signal)) {
        attempt = 0;
        if (item.event?.case !== "sessionEvent") continue;
        const ev = item.event.value as SessionEvent;
        if (ev.event.case === "assistantDelta") continue;
        if (ev.seq <= lastSeq) continue;
        const ended = await render(client, thread, s, ev);
        lastSeq = ev.seq;
        await getDb()
          .update(schema.chatThread)
          .set({ lastSeq: Number(lastSeq), progressId: s.progressId, updatedAt: new Date(), ...(ended ? { status: "ended" } : {}) })
          .where(and(eq(schema.chatThread.id, thread.id), eq(schema.chatThread.status, "active")));
        if (ended) return;
      }
    } catch (err) {
      if (s.ctl.signal.aborted) return;
      // The session registers with the relay a moment after the host acked the spawn: wait for it.
      if (!(err instanceof ApiError && err.type === "not_found")) console.error(`[harness] chat watcher ${thread.id}: ${err instanceof Error ? err.message : String(err)}`);
      if (err instanceof ApiError && err.type === "not_found" && attempt > 120) return;
    }
    await new Promise((r) => setTimeout(r, Math.min(500 * 2 ** Math.min(attempt, 4), 8_000)));
  }
}

/** Server start: follow the sessions of active threads again, from the seq each one reached, and the work still queued. */
export async function resumeChatWatchers(): Promise<void> {
  await ensureDb();
  const rows = await getDb().select().from(schema.chatThread).where(inArray(schema.chatThread.status, LIVE));
  for (const r of rows) {
    if (r.status === "active") watch(r);
    else if (r.hostWorkId) waitForHostWork(r);
  }
}

/** Stops every watcher (tests, shutdown). */
export function stopChatWatchers(): void {
  for (const s of watchers.values()) s.ctl.abort();
  watchers.clear();
  for (const c of hostWaits.values()) c.abort();
  hostWaits.clear();
}

export const SLACK = ChatProviderId.SLACK;
export const TELEGRAM = ChatProviderId.TELEGRAM;
