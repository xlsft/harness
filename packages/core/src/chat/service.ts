/**
 * Chat apps, provider-independent (docs/design/chat.md): inbound requests (verified, deduplicated, replays refused), linking,
 * starting a session on one of the person's machines through the relay, steering it from the thread, and following it to
 * render progress, permission prompts and answers.
 *
 * Sessions are followed with the relay's own history-then-live subscription from the last seq the thread rendered, so a
 * restart of the server resumes where it stopped (resumeChatWatchers).
 */
import { create } from "@bufbuild/protobuf";
import { and, eq, inArray, lt } from "drizzle-orm";
import { record as audit } from "../audit/log.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { PermissionMode, RespondToPermissionRequest_Decision, RespondToPermissionRequestSchema, SendUserMessageRequestSchema, type SessionEvent } from "../gen/harness/agent/v1/agent_pb.ts";
import { ChatProvider as ChatProviderId } from "../gen/harness/chat/v1/chat_pb.ts";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { CommandAck_Outcome, CommandOptionsSchema, DeliveryState, SpawnSessionCommandSchema } from "../gen/harness/relay/v1/relay_pb.ts";
import { DocumentKind } from "../gen/harness/sync/v1/sync_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { newId, sha256 } from "../lib/ids.ts";
import { listHostFolders, remoteStartHosts, requireRemoteStartsAllowed, sendSessionCommand, spawnSession, subscribeSession } from "../relay/service.ts";
import { toPublicUser, type Principal } from "../services/devices.ts";
import { getDocument } from "../services/sync.ts";
import { appSecrets, chatAppsDisabledByPolicy, getAppRow, identityOf, linkIdentity, noteAppEvent, providerOf, redeemCode, type AppRow } from "./app.ts";
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
const PRODUCT = "xlsft`s harness";

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
async function claim(key: string): Promise<boolean> {
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

/** An inbound request of a chat app: verify, refuse replays, answer quickly and do the work in the background. */
export async function handleInbound(providerId: number, headers: Headers, raw: string): Promise<InboundResult> {
  const provider = providerOf(providerId);
  await ensureDb();
  const app = await getAppRow(providerId);
  if (!app?.secretsEnc) return { status: 404, body: { error: `no ${provider.name} app is connected` } };
  const secrets = appSecrets(app);
  const v = provider.verify(headers, raw, secrets.signingSecret ?? "");
  if (!v.ok) return { status: v.status ?? 401, body: { error: v.reason ?? "unverified" } };
  await prune().catch(() => {});
  if (v.replayKey && !(await claim(v.replayKey))) return { status: 401, body: { error: "replayed request" } };
  if (v.deliveryId && !(await claim(v.deliveryId))) return { status: 200, body: { ok: true, duplicate: true } };
  const items = provider.parse(headers, raw, app.botUserId);
  const challenge = items.find((i) => i.kind === "challenge");
  if (challenge) return { status: 200, body: { challenge: challenge.challenge } };
  await noteAppEvent(providerId);
  const work = items.filter((i) => i.kind === "message" || i.kind === "action");
  if (work.length) {
    const client = provider.client(secrets as Record<string, string>);
    background(async () => {
      for (const it of work) {
        try {
          if (it.kind === "message") await onMessage(provider, app, client, it);
          else if (it.kind === "action") await onAction(provider, app, client, it);
        } catch (err) {
          await noteAppEvent(providerId, err instanceof Error ? err.message : String(err)).catch(() => {});
          throw err;
        }
      }
    });
  }
  return { status: 200, body: { ok: true } };
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
async function accountOf(provider: ChatProvider, app: AppRow, client: ChatProviderClient, place: ChatPlace, chatUserId: string): Promise<Principal | undefined> {
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

async function threadAt(provider: number, place: ChatPlace): Promise<ThreadRow | undefined> {
  return getDb().query.chatThread.findFirst({
    where: and(eq(schema.chatThread.provider, provider), eq(schema.chatThread.teamId, place.teamId), eq(schema.chatThread.channel, place.channel), eq(schema.chatThread.threadId, place.threadId)),
  });
}

const options = (idempotencyKey: string) => create(CommandOptionsSchema, { idempotencyKey, frontend: "slack" });

const textContent = (text: string) => [{ block: { case: "text" as const, value: { text } } }];

async function onMessage(provider: ChatProvider, app: AppRow, client: ChatProviderClient, m: Message): Promise<void> {
  const reply = (text: string) => client.post(m.place, { text });
  const link = /^link\s+([A-Za-z0-9-]{8,12})\s*$/i.exec(m.text);
  if (link && m.addressed) {
    if (!(await claim(m.messageKey))) return;
    const userId = await redeemCode(provider.id, link[1]!);
    if (!userId) {
      await reply("That link code is not valid or has expired. Create a new one in Settings > Chat apps.");
      return;
    }
    const profile = await client.profile(m.userId).catch(() => undefined);
    await linkIdentity({ provider: provider.id, teamId: m.place.teamId, externalUserId: m.userId, userId, displayName: profile?.name ?? "", via: "code" });
    const p = await principalOf(userId);
    await audit({ action: "chat.link", actor: { id: userId, label: p?.user.email ?? "" }, target: { type: "chat_identity", id: m.userId }, details: { provider: provider.name, team_id: m.place.teamId, via: "code" } });
    await reply(`Linked to your ${PRODUCT} account${p ? ` (${p.user.email})` : ""}. Mention me or write to me to start a session.`);
    return;
  }
  const thread = await threadAt(provider.id, m.place);
  if (!thread && !m.addressed) return;
  if (!(await claim(m.messageKey))) return;
  if (!app.enabled || (await chatAppsDisabledByPolicy("user"))) {
    if (m.addressed) await client.ephemeral(m.place, m.userId, `Starting sessions from ${provider.name} is turned off on this ${PRODUCT} server.`).catch(() => {});
    return;
  }
  const p = await accountOf(provider, app, client, m.place, m.userId);
  if (!p) {
    if (m.addressed) {
      await client.ephemeral(m.place, m.userId, `I don't know who you are in ${PRODUCT} yet. Create a link code under Settings > Chat apps and send it to me as "link <code>".`);
    }
    return;
  }
  if (await chatAppsDisabledByPolicy(p.user.role)) {
    if (m.addressed) await client.ephemeral(m.place, m.userId, `Your organization's policy turned chat apps off.`).catch(() => {});
    return;
  }
  if (thread) {
    // Only the person whose session it is steers it; everyone else may talk in the thread.
    if (thread.userId !== p.user.id) {
      if (m.addressed) await client.ephemeral(m.place, m.userId, "This thread steers someone else's session. Mention me in a new message to start your own.").catch(() => {});
      return;
    }
    await steer(client, thread, p, m);
    return;
  }
  await startFromChat(provider, client, p, m);
}

async function steer(client: ChatProviderClient, thread: ThreadRow, p: Principal, m: Message): Promise<void> {
  if (thread.status === "starting") {
    await client.ephemeral(m.place, m.userId, "The session is still starting; send that again in a moment.");
    return;
  }
  if (thread.status !== "active") {
    await client.post(m.place, { text: "This session has ended. Mention me in a new message to start another." });
    return;
  }
  if (!m.text) return;
  try {
    const r = await sendSessionCommand(
      p,
      { case: "sendUserMessage", value: create(SendUserMessageRequestSchema, { sessionId: thread.sessionId, uuid: `chat-${sha256(m.messageKey).slice(0, 32)}`, content: textContent(m.text) }) },
      options(`chat:${m.messageKey}`),
      "optional",
      "slack",
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
}

const MODES: Record<string, PermissionMode> = {
  default: PermissionMode.DEFAULT,
  acceptEdits: PermissionMode.ACCEPT_EDITS,
  plan: PermissionMode.PLAN,
  auto: PermissionMode.AUTO,
  dontAsk: PermissionMode.DONT_ASK,
};

/** The person's chat defaults from their synced settings (chatDefaults); empty fields when unset. */
async function chatDefaultsOf(p: Principal): Promise<ChatDefaults> {
  const empty = { deviceId: "", folder: "", permissionMode: "" };
  try {
    const doc = await getDocument(p, { ref: { scope: Scope.USER, kind: DocumentKind.SETTINGS, key: "settings", projectKey: "" } });
    const v = (JSON.parse(doc.content) as { chatDefaults?: Record<string, unknown> }).chatDefaults;
    if (!v || typeof v !== "object") return empty;
    const s = (k: string) => (typeof v[k] === "string" ? (v[k] as string) : "");
    return { deviceId: s("deviceId"), folder: s("folder"), permissionMode: s("permissionMode") };
  } catch {
    return empty;
  }
}

interface Target {
  hostId: string;
  name: string;
  folder: string;
  mode: PermissionMode;
}

async function chooseTarget(p: Principal): Promise<Target | string> {
  const d = await chatDefaultsOf(p);
  const hosts = await remoteStartHosts(p.user.id);
  const mode = MODES[d.permissionMode] ?? PermissionMode.DEFAULT;
  let host = d.deviceId ? hosts.find((h) => h.deviceId === d.deviceId) : hosts[0];
  if (!host) {
    return d.deviceId
      ? "The machine in your chat defaults is offline or does not take remote starts. Change it under Settings > Chat apps, or turn remote starts on there."
      : `None of your machines is online and taking remote starts. Turn "Allow remote starts" on in ${PRODUCT} on a machine (Settings > Remote), and keep it online.`;
  }
  let folder = d.deviceId === host.deviceId ? d.folder : "";
  if (!folder) {
    if (host.remoteStarts) {
      const folders = await listHostFolders(p, host.id, 1, "slack").catch(() => []);
      folder = folders.find((f) => f.exists)?.path ?? folders[0]?.path ?? "";
    } else {
      folder = (Array.isArray(host.allowedRoots) ? (host.allowedRoots as string[]) : [])[0] ?? "";
    }
  }
  if (!folder) return `${host.name || "Your machine"} has no recent folder to start in. Open a session there once, or choose a folder in your chat defaults.`;
  return { hostId: host.id, name: host.name, folder, mode };
}

async function startFromChat(provider: ChatProvider, client: ChatProviderClient, p: Principal, m: Message): Promise<void> {
  if (!m.text) {
    await client.post(m.place, { text: "What should I do? Mention me with a task." });
    return;
  }
  const now = new Date();
  const id = newId("cht");
  const inserted = await getDb()
    .insert(schema.chatThread)
    .values({ id, provider: provider.id, teamId: m.place.teamId, channel: m.place.channel, threadId: m.place.threadId, userId: p.user.id, status: "starting", createdAt: now, updatedAt: now })
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
  const target = await chooseTarget(p);
  if (typeof target === "string") return fail(target);
  const progressId = await client.post(m.place, { text: `Starting a session on ${target.name || "your machine"} in ${target.folder}…` });
  try {
    const session = await spawnSession(
      p,
      target.hostId,
      create(SpawnSessionCommandSchema, {
        create: { cwd: target.folder, permissionMode: target.mode },
        initialMessage: { uuid: `chat-${sha256(m.messageKey).slice(0, 32)}`, content: textContent(m.text) },
      }),
      options(`chat:spawn:${id}`),
      "slack",
      { frontend: provider.name.toLowerCase() },
    );
    await getDb().update(schema.chatThread).set({ sessionId: session.id, hostId: target.hostId, status: "active", progressId, updatedAt: new Date() }).where(eq(schema.chatThread.id, id));
    await audit({
      action: "chat.session_start",
      actor: { id: p.user.id, label: p.user.email },
      target: { type: "session", id: session.id },
      details: { provider: provider.name, team_id: m.place.teamId, channel: m.place.channel, host_id: target.hostId, folder: target.folder },
    });
    const row = await getDb().query.chatThread.findFirst({ where: eq(schema.chatThread.id, id) });
    if (row) watch(row);
  } catch (err) {
    const why = err instanceof ApiError ? err.message : "the machine did not start it";
    await getDb().update(schema.chatThread).set({ status: "failed", updatedAt: new Date() }).where(eq(schema.chatThread.id, id));
    await client.update(m.place, progressId, { text: `Could not start the session: ${why}` }).catch(() => client.post(m.place, { text: `Could not start the session: ${why}` }));
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

async function onAction(provider: ChatProvider, app: AppRow, client: ChatProviderClient, a: Action): Promise<void> {
  let v: ButtonValue;
  try {
    v = JSON.parse(a.value) as ButtonValue;
  } catch {
    return;
  }
  if (!v?.t || !v.r) return;
  const thread = await getDb().query.chatThread.findFirst({ where: eq(schema.chatThread.id, v.t) });
  if (!thread || thread.provider !== provider.id || thread.channel !== a.place.channel || thread.teamId !== a.place.teamId) return;
  const place = { teamId: thread.teamId, channel: thread.channel, threadId: thread.threadId };
  if (!app.enabled || (await chatAppsDisabledByPolicy("user"))) return;
  const id = await identityOf(provider.id, a.place.teamId, a.userId);
  if (!id || id.userId !== thread.userId) {
    await client.ephemeral(place, a.userId, "Only the person whose session this is can answer.").catch(() => {});
    return;
  }
  const p = await principalOf(thread.userId);
  if (!p || thread.status !== "active") return;
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
      options(`chat:perm:${thread.id}:${v.r}:${a.userId}`),
      "implied",
      "slack",
    );
    if (r.ack?.outcome === CommandAck_Outcome.ALREADY_RESOLVED) await client.ephemeral(place, a.userId, "That was already answered.").catch(() => {});
  } catch (err) {
    await client.ephemeral(place, a.userId, `Could not answer: ${err instanceof ApiError ? err.message : "error"}`).catch(() => {});
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
  const place = { teamId: thread.teamId, channel: thread.channel, threadId: thread.threadId };
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
  for (let attempt = 0; !s.ctl.signal.aborted; attempt++) {
    const app = await getAppRow(thread.provider);
    const p = await principalOf(thread.userId);
    if (!app?.secretsEnc || !p) return;
    const client = providerOf(thread.provider).client(appSecrets(app) as Record<string, string>);
    try {
      for await (const item of subscribeSession(p, thread.sessionId, lastSeq, `chat-${thread.id}`, "slack", s.ctl.signal)) {
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
          .where(eq(schema.chatThread.id, thread.id));
        if (ended) return;
      }
    } catch (err) {
      // The session registers with the relay a moment after the host acked the spawn: wait for it.
      if (!(err instanceof ApiError && err.type === "not_found")) console.error(`[harness] chat watcher ${thread.id}: ${err instanceof Error ? err.message : String(err)}`);
      if (err instanceof ApiError && err.type === "not_found" && attempt > 120) return;
    }
    await new Promise((r) => setTimeout(r, Math.min(500 * 2 ** Math.min(attempt, 4), 8_000)));
  }
}

/** Server start: follow the sessions of active threads again, from the seq each one reached. */
export async function resumeChatWatchers(): Promise<void> {
  await ensureDb();
  const rows = await getDb().select().from(schema.chatThread).where(inArray(schema.chatThread.status, ["active"]));
  for (const r of rows) watch(r);
}

/** Stops every watcher (tests, shutdown). */
export function stopChatWatchers(): void {
  for (const s of watchers.values()) s.ctl.abort();
  watchers.clear();
}

export const SLACK = ChatProviderId.SLACK;
