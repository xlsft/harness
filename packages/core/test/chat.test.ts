/**
 * Chat apps (Slack) against a fake Slack Web API and a fake host on the relay: connecting the app, request signatures and
 * replays, linking by code and by directory email, starting a session from a mention, progress, permission buttons that
 * only the owner can answer, steering from the thread, the final answer, policy switches and audit.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { RespondToPermissionRequest_Decision } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { ChatProvider } from "../src/gen/harness/chat/v1/chat_pb.ts";
import { chatIdle, stopChatWatchers } from "../src/chat/service.ts";
import { slackSignature } from "../src/chat/slack.ts";
import { FakeHost } from "./helpers/fake-host.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const SECRET = "slack-signing-secret-test";
const TEAM = "T0TEAM";
const BOT = "UBOT";

interface SlackCall {
  method: string;
  body: Record<string, any>;
}

let slackServer: Server;
let calls: SlackCall[] = [];
let nextTs = 1000;
const emails: Record<string, string> = {};

async function startFakeSlack(): Promise<string> {
  slackServer = createServer((req, res) => {
    let raw = "";
    req.on("data", (c) => (raw += c));
    req.on("end", () => {
      const method = (req.url ?? "").replace(/^\//, "");
      const body = raw ? JSON.parse(raw) : {};
      if (req.headers.authorization !== "Bearer xoxb-test-token") {
        res.end(JSON.stringify({ ok: false, error: "invalid_auth" }));
        return;
      }
      calls.push({ method, body });
      const reply = (o: unknown) => {
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify(o));
      };
      switch (method) {
        case "auth.test":
          return reply({ ok: true, team_id: TEAM, team: "Test team", user_id: BOT });
        case "chat.postMessage":
          return reply({ ok: true, ts: `${++nextTs}.0001` });
        case "chat.update":
        case "chat.postEphemeral":
          return reply({ ok: true });
        case "users.info":
          return reply({ ok: true, user: { id: body.user, real_name: body.user, profile: { email: emails[body.user] ?? "" } } });
        default:
          return reply({ ok: false, error: "unknown_method" });
      }
    });
  });
  await new Promise<void>((r) => slackServer.listen(0, "127.0.0.1", r));
  return `http://127.0.0.1:${(slackServer.address() as AddressInfo).port}`;
}

let ts: TestServer;
type Clients = ReturnType<TestServer["as"]>;
let admin: Clients;
let alice: Clients;
let bob: Clients;
let adminCookie: string;

beforeAll(async () => {
  const api = await startFakeSlack();
  ts = await startTestServer({
    HARNESS_SLACK_API_URL: api,
    HARNESS_RELAY_HEARTBEAT_MS: "300",
    HARNESS_RELAY_ACK_WAIT_MS: "3000",
    HARNESS_RELAY_SPAWN_WAIT_MS: "3000",
    HARNESS_CHAT_PROGRESS_MS: "1",
  });
  const a = await deviceLogin(ts);
  adminCookie = a.cookie;
  admin = ts.as(a.accessToken);
  alice = ts.as((await createUserAndLogin(ts, adminCookie, "alice@test.local", "alice-password-1")).accessToken);
  bob = ts.as((await createUserAndLogin(ts, adminCookie, "bob@test.local", "bob-password-1")).accessToken);
});

afterAll(async () => {
  stopChatWatchers();
  await ts.close();
  await new Promise((r) => slackServer.close(r));
});

async function expectError(p: Promise<unknown>, code: Code) {
  const err = await p.then(
    () => undefined,
    (e) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  expect((err as ConnectError).code).toBe(code);
}

async function waitFor<T>(fn: () => T | undefined | Promise<T | undefined>, what: string, ms = 6000): Promise<T> {
  const deadline = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 25));
  }
}

function sign(raw: string, at = Math.floor(Date.now() / 1000), secret = SECRET) {
  return { "x-slack-request-timestamp": String(at), "x-slack-signature": slackSignature(secret, String(at), raw) };
}

async function sendRaw(raw: string, headers: Record<string, string>) {
  const res = await fetch(`${ts.url}/api/chat/slack/events`, { method: "POST", headers, body: raw });
  return { status: res.status, body: (await res.json()) as Record<string, unknown> };
}

let eventN = 0;
async function sendEvent(event: Record<string, unknown>, eventId = `Ev${++eventN}`, at?: number) {
  const raw = JSON.stringify({ type: "event_callback", team_id: TEAM, event_id: eventId, event });
  const r = await sendRaw(raw, { "content-type": "application/json", ...sign(raw, at) });
  await chatIdle();
  return r;
}

async function press(user: string, channel: string, messageTs: string, threadTs: string, value: string) {
  const payload = JSON.stringify({ type: "block_actions", user: { id: user }, team: { id: TEAM }, channel: { id: channel }, container: { message_ts: messageTs, thread_ts: threadTs, channel_id: channel }, actions: [{ action_id: "x", value }] });
  const raw = new URLSearchParams({ payload }).toString();
  const r = await sendRaw(raw, { "content-type": "application/x-www-form-urlencoded", ...sign(raw) });
  await chatIdle();
  return r;
}

let msgTs = 5000;
const newTs = () => `${++msgTs}.000100`;

const posted = (pred: (b: Record<string, any>) => boolean) => calls.find((c) => c.method === "chat.postMessage" && pred(c.body));

describe("chat app", () => {
  it("connects with a checked token and never returns secrets", async () => {
    await expectError(alice.chat.putChatApp({ provider: ChatProvider.SLACK, botToken: "xoxb-test-token", signingSecret: SECRET, enabled: true }), Code.PermissionDenied);
    await expectError(admin.chat.putChatApp({ provider: ChatProvider.SLACK, botToken: "xoxb-wrong", signingSecret: SECRET, enabled: true }), Code.FailedPrecondition);
    const r = await admin.chat.putChatApp({ provider: ChatProvider.SLACK, botToken: "xoxb-test-token", signingSecret: SECRET, enabled: true, matchByEmail: true });
    expect(r.app).toMatchObject({ configured: true, enabled: true, teamId: TEAM, teamName: "Test team", botUserId: BOT });
    expect(r.app!.requestUrl).toMatch(/\/api\/chat\/slack\/events$/);
    expect(JSON.stringify(r.app)).not.toContain("xoxb");
    expect(JSON.stringify((await admin.chat.getChatApp({ provider: ChatProvider.SLACK })).app)).not.toContain(SECRET);
    // Saving again without secrets keeps them.
    expect((await admin.chat.putChatApp({ provider: ChatProvider.SLACK, enabled: true, matchByEmail: true })).app!.configured).toBe(true);
    const logged = await admin.audit.listAuditEvents({ filter: { action: "chat.app_update" } });
    expect(logged.events.length).toBeGreaterThan(0);
    expect(JSON.stringify(logged)).not.toContain("xoxb-test-token");
    expect(JSON.stringify(logged)).not.toContain(SECRET);
  });

  it("verifies signatures and timestamps and refuses replays", async () => {
    const raw = JSON.stringify({ type: "url_verification", challenge: "abc" });
    expect((await sendRaw(raw, { "content-type": "application/json" })).status).toBe(401);
    expect((await sendRaw(raw, { "content-type": "application/json", ...sign(raw, undefined, "other-secret") })).status).toBe(401);
    expect((await sendRaw(raw, { "content-type": "application/json", ...sign(raw, Math.floor(Date.now() / 1000) - 600) })).status).toBe(401);
    const headers = { "content-type": "application/json", ...sign(raw) };
    const ok = await sendRaw(raw, headers);
    expect(ok).toEqual({ status: 200, body: { challenge: "abc" } });
    expect((await sendRaw(raw, headers)).status).toBe(401); // the same signed request again
    // Slack's retry of an event: new signature, same event_id: answered, not processed twice.
    const ev = { type: "message", channel_type: "im", channel: "D1", user: "UNOBODY", text: "hello", ts: newTs() };
    expect((await sendEvent(ev, "EvRetry")).status).toBe(200);
    const before = calls.length;
    expect((await sendEvent(ev, "EvRetry", Math.floor(Date.now() / 1000) - 2)).body).toMatchObject({ duplicate: true });
    expect(calls.length).toBe(before);
  });

  it("asks unknown people to link, and links with a one-time code made in the app", async () => {
    calls = [];
    await sendEvent({ type: "message", channel_type: "im", channel: "DALICE", user: "UALICE", text: "hi", ts: newTs() });
    expect(calls.find((c) => c.method === "chat.postEphemeral")?.body.text).toMatch(/link code/);
    const { code } = await alice.chat.startChatLink({ provider: ChatProvider.SLACK });
    expect(code).toMatch(/^[A-Z0-9]{4}-[A-Z0-9]{4}$/);
    await sendEvent({ type: "message", channel_type: "im", channel: "DALICE", user: "UALICE", text: `link ${code}`, ts: newTs() });
    expect(posted((b) => /Linked to your/.test(b.text))).toBeTruthy();
    const mine = await alice.chat.listMyChatIdentities({});
    expect(mine.available).toBe(true);
    expect(mine.identities).toMatchObject([{ teamId: TEAM, externalUserId: "UALICE", linkedVia: "code" }]);
    // Personal: nobody else sees it, admins included.
    expect((await admin.chat.listMyChatIdentities({})).identities).toEqual([]);
    // Single use.
    await sendEvent({ type: "message", channel_type: "im", channel: "DMAL", user: "UMALLORY", text: `link ${code}`, ts: newTs() });
    expect(posted((b) => /not valid or has expired/.test(b.text))).toBeTruthy();
  });

  it("links by directory email only for verified or directory accounts", async () => {
    emails.UBOB = "bob@test.local";
    calls = [];
    await sendEvent({ type: "message", channel_type: "im", channel: "DBOB", user: "UBOB", text: "hi", ts: newTs() });
    // bob@test.local is a local account with an unverified email: no automatic link.
    expect((await bob.chat.listMyChatIdentities({})).identities).toEqual([]);
    const { code } = await bob.chat.startChatLink({ provider: ChatProvider.SLACK });
    await sendEvent({ type: "message", channel_type: "im", channel: "DBOB", user: "UBOB", text: `link ${code}`, ts: newTs() });
    expect((await bob.chat.listMyChatIdentities({})).identities).toHaveLength(1);
  });

  it("starts a session on the person's machine, renders it, and lets only the owner answer", async () => {
    const host = new FakeHost(alice, ["/work/proj", "/work/other"]);
    await host.start();
    calls = [];
    const root = newTs();
    await sendEvent({ type: "app_mention", channel: "CGEN", user: "UALICE", text: `<@${BOT}> fix the failing tests`, ts: root });
    const spawn = await waitFor(() => host.commands.find((c) => c.payload.case === "spawnSession"), "spawn");
    const sp = spawn.payload.value as { create?: { cwd: string }; initialMessage?: { content: { block: { value: { text: string } } }[] } };
    expect(sp.create?.cwd).toBe("/work/proj");
    expect(sp.initialMessage?.content[0]?.block.value.text).toBe("fix the failing tests");
    expect(posted((b) => /Starting a session on .* in \/work\/proj/.test(b.text) && b.thread_ts === root)).toBeTruthy();

    await host.publish(
      { case: "turnStarted", value: {} },
      { case: "toolCallStarted", value: { toolUseId: "t1", name: "Bash", summary: "Run go test ./..." } },
      { case: "permissionRequest", value: { requestId: "perm-1", toolName: "Bash", inputJson: JSON.stringify({ command: "go test ./..." }), suggestedRules: ["Bash(go test *)"] } },
    );
    const prompt = await waitFor(() => posted((b) => /Permission needed: Bash/.test(b.text)), "permission prompt");
    const buttons = prompt.body.blocks[1].elements as { action_id: string; value: string }[];
    expect(buttons.map((b) => b.action_id)).toEqual(["allow", "always", "deny"]);
    expect(calls.some((c) => (c.method === "chat.update" || c.method === "chat.postMessage") && /Run go test/.test(c.body.text))).toBe(true);

    // Bob is linked but it is not his session.
    calls = [];
    await press("UBOB", "CGEN", "1.0", root, buttons[0]!.value);
    expect(calls.find((c) => c.method === "chat.postEphemeral")?.body.text).toMatch(/Only the person/);
    expect(host.commands.some((c) => c.payload.case === "respondToPermission")).toBe(false);

    await press("UALICE", "CGEN", "1.0", root, buttons[0]!.value);
    const answer = await waitFor(() => host.commands.find((c) => c.payload.case === "respondToPermission"), "answer");
    expect(answer.payload.value).toMatchObject({ sessionId: host.sessionId, requestId: "perm-1", decision: RespondToPermissionRequest_Decision.ALLOW });
    await host.publish({ case: "permissionResolved", value: { requestId: "perm-1", decision: RespondToPermissionRequest_Decision.ALLOW, resolvedBy: "relay:slack" } });
    await waitFor(() => calls.find((c) => c.method === "chat.update" && /Allowed/.test(c.body.text)), "resolved prompt");

    // A reply in the thread steers the session; someone else's does not.
    await sendEvent({ type: "message", channel_type: "channel", channel: "CGEN", user: "UBOB", text: "do it differently", ts: newTs(), thread_ts: root });
    await sendEvent({ type: "message", channel_type: "channel", channel: "CGEN", user: "UALICE", text: "also run the linter", ts: newTs(), thread_ts: root });
    const steer = await waitFor(() => host.commands.find((c) => c.payload.case === "sendUserMessage"), "steering message");
    expect((steer.payload.value as { content: { block: { value: { text: string } } }[] }).content[0]?.block.value.text).toBe("also run the linter");
    expect(host.commands.filter((c) => c.payload.case === "sendUserMessage")).toHaveLength(1);

    await host.publish(
      { case: "assistantMessage", value: { messageId: "m1", content: [{ block: { case: "text", value: { text: "All tests pass now." } } }] } },
      { case: "turnFinished", value: { result: 1 } },
    );
    await waitFor(() => posted((b) => b.text === "All tests pass now." && b.thread_ts === root), "final answer");
    host.stop();
  });

  it("follows the policy switches and audits remote starts", async () => {
    const host = new FakeHost(alice, ["/work/proj"]);
    await host.start();
    const cur = (await admin.policy.getManagedPolicy({})).policy!;
    await admin.policy.setManagedPolicy({ policy: { ...cur, remoteStartsDisabled: true } });
    await expectError(alice.relay.spawnSession({ hostId: host.hostId, spawn: { create: { cwd: "/work/proj" } } }), Code.PermissionDenied);
    await expectError(alice.relay.listHostFolders({ hostId: host.hostId }), Code.PermissionDenied);
    calls = [];
    const root = newTs();
    await sendEvent({ type: "app_mention", channel: "CGEN", user: "UALICE", text: `<@${BOT}> another task`, ts: root });
    await waitFor(() => posted((b) => /remote starts off/.test(b.text)), "refusal");

    await admin.policy.setManagedPolicy({ policy: { ...cur, remoteStartsDisabled: false, chatAppsDisabled: true } });
    expect((await alice.chat.listMyChatIdentities({})).available).toBe(false);
    calls = [];
    await sendEvent({ type: "app_mention", channel: "CGEN", user: "UALICE", text: `<@${BOT}> third task`, ts: newTs() });
    expect(calls.find((c) => c.method === "chat.postEphemeral")?.body.text).toMatch(/turned off/);
    expect(host.commands.some((c) => c.payload.case === "spawnSession")).toBe(false);
    await admin.policy.setManagedPolicy({ policy: { ...cur, chatAppsDisabled: false } });

    const rows = (await admin.audit.listAuditEvents({ filter: { action: "relay.remote_start" } })).events;
    expect(rows.some((e) => e.outcome === "success")).toBe(true);
    expect(rows.some((e) => e.outcome === "failure")).toBe(true);
    host.stop();
  });

  it("refuses remote starts to a machine that does not take them", async () => {
    const host = new FakeHost(alice, ["/work/proj"]);
    await host.start(false);
    await expectError(alice.relay.spawnSession({ hostId: host.hostId, spawn: { create: { cwd: "/work/proj" } } }), Code.FailedPrecondition);
    await expectError(alice.relay.listHostFolders({ hostId: host.hostId }), Code.FailedPrecondition);
    await expectError(bob.relay.listHostFolders({ hostId: host.hostId }), Code.NotFound);
    host.stop();
  });
});
