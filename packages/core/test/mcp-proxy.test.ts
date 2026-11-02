/** McpService proxy and OAuth against local fixtures (SDK MCP server + fake authorization server). */
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { and, eq } from "drizzle-orm";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import {
  AuthState,
  GetOAuthFlowResponse_State,
  OAuthConfigSchema,
  RemoteConfigSchema,
  Transport, type CallToolResponse,
  type Server,
  type ServerSchema,
} from "../src/gen/harness/mcp/v1/mcp_pb.ts";
import { DocumentKind } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { poolSize, sweepIdle } from "../src/mcp/proxy.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";
import { startMcpUpstream, type Upstream } from "./helpers/mcp-upstream.ts";

let ts: TestServer;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let alice: Awaited<ReturnType<typeof deviceLogin>>;
let bob: Awaited<ReturnType<typeof deviceLogin>>;
let A: TestServer["mcp"];
let B: TestServer["mcp"];
let plain: Upstream;
let oauth: Upstream;

beforeAll(async () => {
  ts = await startTestServer();
  admin = await deviceLogin(ts);
  alice = await createUserAndLogin(ts, admin.cookie, "alice@test.local");
  bob = await createUserAndLogin(ts, admin.cookie, "bob@test.local");
  A = ts.as(alice.accessToken).mcp;
  B = ts.as(bob.accessToken).mcp;
  plain = await startMcpUpstream();
  oauth = await startMcpUpstream({ oauth: true });
});

afterAll(async () => {
  await ts.close();
  await plain.close();
  await oauth.close();
});

async function expectError(p: Promise<unknown>, code: Code) {
  const err = await p.then(
    () => undefined,
    (e) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  expect((err as ConnectError).code).toBe(code);
  return err as ConnectError;
}

type ServerInit = MessageInitShape<typeof ServerSchema>;
/** Plain init fields (no $typeName), so test helpers can spread overrides. */
type ServerFields = Partial<Omit<ServerInit, "$typeName">>;

async function remote(c: TestServer["mcp"], name: string, url: string, extra: ServerFields = {}, secrets: Record<string, string> = {}): Promise<Server> {
  const r = await c.createServer({
    server: { name, scope: Scope.USER, transport: Transport.STREAMABLE_HTTP, enabled: true, remote: { url }, ...extra },
    secrets: { set: secrets },
  });
  return r.server!;
}

/** Test servers listen on 127.0.0.1: only an admin can let the proxy reach them. */
async function allowPrivate(id: string) {
  const r = await adminApi(ts.url, admin.accessToken, "PATCH", `/api/admin/mcp/servers/${id}`, { allow_private_network: true });
  expect(r.status).toBe(200);
}

async function collect(stream: AsyncIterable<CallToolResponse>): Promise<CallToolResponse[]> {
  const out: CallToolResponse[] = [];
  for await (const ev of stream) out.push(ev);
  return out;
}

describe("SSRF guard", () => {
  it("blocks private addresses unless an admin allows them", async () => {
    const s = await remote(A, "local", `${plain.url}/mcp`);
    await expectError(A.listTools({ serverId: s.id }), Code.PermissionDenied);
    const callErr = await expectError(collect(A.callTool({ serverId: s.id, toolName: "echo", argumentsJson: '{"text":"x"}' })), Code.PermissionDenied);
    expect(callErr.message).toContain("allow_private_network");
    const byName = await remote(A, "localhost", plain.url.replace("127.0.0.1", "localhost") + "/mcp");
    await expectError(A.listTools({ serverId: byName.id }), Code.PermissionDenied);
    const ws = await A.createServer({ server: { name: "ws-local", scope: Scope.USER, transport: Transport.WS, enabled: true, remote: { url: "ws://127.0.0.1:9/mcp" } } });
    await expectError(A.listTools({ serverId: ws.server!.id }), Code.PermissionDenied);
    const meta = await remote(A, "metadata", "http://169.254.169.254/mcp");
    await expectError(A.listTools({ serverId: meta.id }), Code.PermissionDenied);
    await expectError(A.startOAuth({ serverId: meta.id }), Code.PermissionDenied);
    expect(plain.seenHeaders).toHaveLength(0);

    // only admins may lift the guard
    const denied = await adminApi(ts.url, alice.accessToken, "PATCH", `/api/admin/mcp/servers/${s.id}`, { allow_private_network: true });
    expect(denied.status).toBe(403);
    await allowPrivate(s.id);
    expect((await A.listTools({ serverId: s.id })).tools.length).toBeGreaterThan(0);

    // moving the server to another origin drops the exemption
    const u = await A.updateServer({ server: { ...s, remote: create(RemoteConfigSchema, { url: `${oauth.url}/mcp` }) }, baseVersion: s.version });
    expect(u.result.case).toBe("server");
    await expectError(A.listTools({ serverId: s.id }), Code.PermissionDenied);
  });
});

describe("proxy", () => {
  let s: Server;

  beforeAll(async () => {
    s = await remote(A, "fixture", `${plain.url}/mcp`, { remote: { url: `${plain.url}/mcp`, headers: { "x-api-key": "${secret:KEY}" } } }, { KEY: "key-123" });
    await allowPrivate(s.id);
  });

  it("lists tools with annotations and injects secret headers server-side", async () => {
    const r = await A.listTools({ serverId: s.id });
    expect(r.tools.map((t) => t.name).sort()).toEqual(["echo", "fail", "peek", "slow"]);
    const peek = r.tools.find((t) => t.name === "peek")!;
    expect(JSON.parse(peek.annotationsJson)).toMatchObject({ readOnlyHint: true, destructiveHint: false });
    expect(peek.title).toBe("Peek");
    expect(JSON.parse(r.tools.find((t) => t.name === "echo")!.inputSchemaJson)).toMatchObject({ type: "object", properties: { text: { type: "string" } } });
    expect(plain.seenHeaders.at(-1)!["x-api-key"]).toBe("key-123");
    // bob cannot use alice's server
    await expectError(B.listTools({ serverId: s.id }), Code.NotFound);
  });

  it("calls tools and returns the upstream result", async () => {
    const events = await collect(A.callTool({ serverId: s.id, toolName: "echo", argumentsJson: '{"text":"hi"}', sessionId: "sess-1", toolUseId: "tu-1" }));
    expect(events).toHaveLength(1);
    const ev = events[0]!.event;
    if (ev.case !== "result") throw new Error("expected result");
    expect(ev.value.isError).toBe(false);
    expect(JSON.parse(ev.value.resultJson)).toMatchObject({ content: [{ type: "text", text: "echo: hi" }] });

    const failed = await collect(A.callTool({ serverId: s.id, toolName: "fail" }));
    expect(failed[0]!.event.case === "result" && failed[0]!.event.value.isError).toBe(true);

    await expectError(collect(A.callTool({ serverId: s.id, toolName: "echo", argumentsJson: "[1]" })), Code.InvalidArgument);
    const audit = await getDb().select().from(schema.mcpAudit).where(and(eq(schema.mcpAudit.serverId, s.id), eq(schema.mcpAudit.action, "call_tool")));
    expect(audit.some((a) => (a.detail as { toolUseId?: string }).toolUseId === "tu-1")).toBe(true);
  });

  it("streams progress, then exactly one result", async () => {
    const events = await collect(A.callTool({ serverId: s.id, toolName: "slow", argumentsJson: '{"steps":3,"delayMs":20}' }));
    const progress = events.filter((e) => e.event.case === "progress");
    expect(progress.length).toBe(3);
    expect(progress.map((e) => (e.event.case === "progress" ? [e.event.value.progress, e.event.value.total, e.event.value.message] : []))).toEqual([
      [1, 3, "step 1"],
      [2, 3, "step 2"],
      [3, 3, "step 3"],
    ]);
    expect(events.at(-1)!.event.case).toBe("result");
    expect(events.filter((e) => e.event.case === "result")).toHaveLength(1);
  });

  it("propagates cancellation upstream", async () => {
    const ac = new AbortController();
    const before = plain.cancelled.length;
    const seen: CallToolResponse[] = [];
    const run = (async () => {
      for await (const ev of A.callTool({ serverId: s.id, toolName: "slow", argumentsJson: '{"steps":100,"delayMs":30}' }, { signal: ac.signal })) {
        seen.push(ev);
        if (seen.length === 1) ac.abort();
      }
    })();
    await run.catch(() => undefined);
    expect(seen[0]?.event.case).toBe("progress");
    for (let i = 0; i < 100 && plain.cancelled.length === before; i++) await new Promise((r) => setTimeout(r, 20));
    expect(plain.cancelled.length).toBe(before + 1);
  });

  it("proxies prompts and resources (with templates)", async () => {
    const prompts = await A.listPrompts({ serverId: s.id });
    expect(prompts.prompts.map((p) => p.name)).toEqual(["greet"]);
    expect(JSON.parse(prompts.prompts[0]!.argumentsJson)).toEqual([expect.objectContaining({ name: "name", required: true })]);
    const got = await A.getPrompt({ serverId: s.id, name: "greet", arguments: { name: "Ann" } });
    expect(JSON.parse(got.resultJson).messages[0].content.text).toBe("Hello, Ann!");

    const res = await A.listResources({ serverId: s.id });
    expect(res.resources.map((r) => r.uri)).toEqual(["res://hello"]);
    expect(res.resources[0]!.mimeType).toBe("text/plain");
    expect(res.templates.map((t) => t.uriTemplate)).toEqual(["res://items/{id}"]);
    const read = await A.readResource({ serverId: s.id, uri: "res://items/7" });
    expect(JSON.parse(read.resultJson).contents[0].text).toBe("item 7");
  });

  it("pools one upstream session per (server, user) and closes it when idle", async () => {
    const sessions = plain.stats.sessions;
    await A.listTools({ serverId: s.id });
    await A.listTools({ serverId: s.id });
    await Promise.all([A.listPrompts({ serverId: s.id }), A.listResources({ serverId: s.id })]);
    expect(plain.stats.sessions).toBe(sessions);
    expect(await poolSize(s.id)).toBe(1);
    sweepIdle(Date.now() + 3_600_000);
    for (let i = 0; i < 50 && (await poolSize(s.id)) > 0; i++) await new Promise((r) => setTimeout(r, 10));
    expect(await poolSize(s.id)).toBe(0);
    await A.listTools({ serverId: s.id });
    expect(plain.stats.sessions).toBe(sessions + 1);
  });

  it("marks tools of requires_user_interaction servers and serves SSE servers", async () => {
    const sse = await A.createServer({
      server: { name: "sse", scope: Scope.USER, transport: Transport.SSE, enabled: true, remote: { url: `${plain.url}/sse`, requiresUserInteraction: true } },
    });
    await allowPrivate(sse.server!.id);
    const r = await A.listTools({ serverId: sse.server!.id });
    expect(r.tools.map((t) => t.name).sort()).toEqual(["echo", "fail", "peek", "slow"]);
    for (const t of r.tools) expect(JSON.parse(t.annotationsJson).requiresUserInteraction).toBe(true);
    const events = await collect(A.callTool({ serverId: sse.server!.id, toolName: "echo", argumentsJson: '{"text":"sse"}' }));
    const last = events.at(-1)!.event;
    if (last.case !== "result") throw new Error("expected result");
    expect(JSON.parse(last.value.resultJson).content[0].text).toBe("echo: sse");

    // the catalog includes remote tools fetched through the proxy
    const cat = await A.getToolCatalog({});
    const entry = cat.catalog!.servers.find((x) => x.serverId === sse.server!.id)!;
    expect(entry.tools.map((t) => t.name).sort()).toEqual(["echo", "fail", "peek", "slow"]);
    expect(JSON.parse(entry.tools[0]!.annotationsJson).requiresUserInteraction).toBe(true);
  });

  it("rejects stdio servers and disabled servers", async () => {
    const st = await A.createServer({ server: { name: "local-cli", scope: Scope.USER, transport: Transport.STDIO, enabled: true, stdio: { command: "x" } } });
    await expectError(A.listTools({ serverId: st.server!.id }), Code.InvalidArgument);
    const off = await remote(A, "off", `${plain.url}/mcp`, { enabled: false });
    await expectError(A.listTools({ serverId: off.id }), Code.FailedPrecondition);
  });
});

describe("OAuth", () => {
  let s: Server;

  async function authorize(serverId: string) {
    const start = await A.startOAuth({ serverId });
    expect(start.flowId).toMatch(/^mfl_/);
    expect(Date.parse(start.expiresAt)).toBeGreaterThan(Date.now());
    expect((await A.getOAuthFlow({ flowId: start.flowId })).state).toBe(GetOAuthFlowResponse_State.PENDING);
    // the "browser": provider redirects to our callback
    const auth = await fetch(start.authorizationUrl, { redirect: "manual" });
    expect(auth.status).toBe(302);
    const callback = auth.headers.get("location")!;
    expect(callback.startsWith(`${ts.url}/api/mcp/oauth/callback?`)).toBe(true);
    const page = await fetch(callback);
    return { start, callback, page, html: await page.text() };
  }

  beforeAll(async () => {
    s = await remote(A, "oauthed", `${oauth.url}/mcp`, { remote: { url: `${oauth.url}/mcp`, oauth: { scopes: ["mcp:read"] } } });
    await allowPrivate(s.id);
  });

  it("needs authorization before use", async () => {
    expect((await A.getServer({ serverId: s.id })).server!.auth?.state).toBe(AuthState.NEEDS_AUTH);
    await expectError(A.listTools({ serverId: s.id }), Code.FailedPrecondition);
    // a server without OAuth hints learns it from the upstream 401
    const learned = await remote(A, "learned", `${oauth.url}/mcp`);
    await allowPrivate(learned.id);
    expect((await A.getAuthStatus({ serverIds: [learned.id] })).statuses[learned.id]!.state).toBe(AuthState.NOT_REQUIRED);
    await expectError(A.listTools({ serverId: learned.id }), Code.FailedPrecondition);
    expect((await A.getAuthStatus({ serverIds: [learned.id] })).statuses[learned.id]!.state).toBe(AuthState.NEEDS_AUTH);
  });

  it("runs discovery, dynamic registration and PKCE, then stores tokens server-side", async () => {
    const sync = ts.as(alice.accessToken).sync;
    const headBefore = (await sync.listChanges({ sinceVersion: 0n, pageSize: 1000 })).nextVersion;
    const { start, callback, page, html } = await authorize(s.id);
    expect(page.status).toBe(200);
    expect(html).toContain("Connected");

    const authUrl = new URL(start.authorizationUrl);
    expect(authUrl.origin).toBe(oauth.url);
    expect(authUrl.searchParams.get("code_challenge_method")).toBe("S256");
    expect(authUrl.searchParams.get("redirect_uri")).toBe(`${ts.url}/api/mcp/oauth/callback`);
    expect(authUrl.searchParams.get("scope")).toBe("mcp:read");
    expect(authUrl.searchParams.get("resource")).toBe(`${oauth.url}/mcp`);
    expect(oauth.stats.registrations).toBe(1);
    expect(oauth.stats.tokenRequests.at(-1)!.get("code_verifier")).toBeTruthy();

    const flow = await A.getOAuthFlow({ flowId: start.flowId });
    expect(flow.state).toBe(GetOAuthFlowResponse_State.COMPLETED);
    expect(flow.auth?.state).toBe(AuthState.CONNECTED);
    await expectError(B.getOAuthFlow({ flowId: start.flowId }), Code.NotFound);

    const st = (await A.getAuthStatus({})).statuses[s.id]!;
    expect(st.state).toBe(AuthState.CONNECTED);
    expect(st.grantedScopes).toEqual(["mcp:read"]);
    expect(Date.parse(st.expiresAt)).toBeGreaterThan(Date.now());

    // replaying the redirect does nothing
    const replay = await fetch(callback);
    expect(replay.status).toBe(400);

    // tokens are encrypted at rest and never part of a response
    const [row] = await getDb().select().from(schema.mcpAuth).where(and(eq(schema.mcpAuth.serverId, s.id), eq(schema.mcpAuth.userId, alice.user!.id)));
    expect(row!.tokensEnc).toMatch(/^v1\./);

    // the proxy now works with the injected bearer token
    const tools = await A.listTools({ serverId: s.id });
    expect(tools.tools.length).toBe(4);
    const bearer = oauth.seenHeaders.at(-1)!.authorization!;
    expect(bearer).toMatch(/^Bearer /);
    expect(row!.tokensEnc).not.toContain(bearer.slice(7));

    // the auth change is announced so other devices refetch
    const changes = await sync.listChanges({ sinceVersion: headBefore, kinds: [DocumentKind.MCP_SERVER], pageSize: 1000 });
    expect(changes.changes.some((c) => c.change.case === "document" && c.change.value.key === s.id)).toBe(true);

    // alice's user-scope server is invisible to bob
    await expectError(B.startOAuth({ serverId: s.id }), Code.NotFound);
  });

  it("refreshes after an upstream 401 and before expiry", async () => {
    const refreshes = oauth.stats.refreshes;
    // the provider dropped the access token: 401 -> forced refresh -> retried with the new token
    oauth.revokeAccessTokens();
    await A.listTools({ serverId: s.id });
    expect(oauth.stats.refreshes).toBe(refreshes + 1);
    await A.listTools({ serverId: s.id });
    expect(oauth.stats.refreshes).toBe(refreshes + 1);
    // a token that expires within the refresh margin (60 s) is refreshed before it is used
    await getDb()
      .update(schema.mcpAuth)
      .set({ expiresAt: new Date(Date.now() + 10_000) })
      .where(and(eq(schema.mcpAuth.serverId, s.id), eq(schema.mcpAuth.userId, alice.user!.id)));
    await A.listTools({ serverId: s.id });
    expect(oauth.stats.refreshes).toBe(refreshes + 2);
    await A.listTools({ serverId: s.id });
    expect(oauth.stats.refreshes).toBe(refreshes + 2);
  });

  it("requires a new authorization when the grant is gone or scopes change", async () => {
    oauth.revokeAll();
    await expectError(A.listTools({ serverId: s.id }), Code.FailedPrecondition);
    const st = (await A.getAuthStatus({ serverIds: [s.id] })).statuses[s.id]!;
    expect(st.state).toBe(AuthState.NEEDS_AUTH);

    // authorize again: the registered client is reused
    const again = await authorize(s.id);
    expect(again.page.status).toBe(200);
    expect(oauth.stats.registrations).toBe(1);
    expect((await A.getAuthStatus({ serverIds: [s.id] })).statuses[s.id]!.state).toBe(AuthState.CONNECTED);

    // new scopes requested by the config -> NEEDS_AUTH until re-authorized
    const cur = (await A.getServer({ serverId: s.id })).server!;
    const u = await A.updateServer({
      server: { ...cur, remote: { ...cur.remote!, oauth: create(OAuthConfigSchema, { scopes: ["mcp:read", "mcp:write"] }) } },
      baseVersion: cur.version,
    });
    expect(u.result.case).toBe("server");
    const st2 = (await A.getAuthStatus({ serverIds: [s.id] })).statuses[s.id]!;
    expect(st2.state).toBe(AuthState.NEEDS_AUTH);
    expect(st2.message).toContain("mcp:write");
    await expectError(A.listTools({ serverId: s.id }), Code.FailedPrecondition);
    const third = await authorize(s.id);
    expect(new URL(third.start.authorizationUrl).searchParams.get("scope")).toBe("mcp:read mcp:write");
    expect((await A.getAuthStatus({ serverIds: [s.id] })).statuses[s.id]!.state).toBe(AuthState.CONNECTED);
  });

  it("keeps authorizations per user for shared org servers", async () => {
    const ADM = ts.as(admin.accessToken).mcp;
    const org = (
      await ADM.createServer({
        server: { name: "org-oauth", scope: Scope.ORG, transport: Transport.STREAMABLE_HTTP, enabled: true, remote: { url: `${oauth.url}/mcp`, oauth: { scopes: ["mcp:read"] } } },
      })
    ).server!;
    await allowPrivate(org.id);
    expect((await authorize(org.id)).page.status).toBe(200);
    expect((await A.getAuthStatus({ serverIds: [org.id] })).statuses[org.id]!.state).toBe(AuthState.CONNECTED);
    expect((await A.listTools({ serverId: org.id })).tools.length).toBe(4);
    expect((await B.getAuthStatus({ serverIds: [org.id] })).statuses[org.id]!.state).toBe(AuthState.NEEDS_AUTH);
    await expectError(B.listTools({ serverId: org.id }), Code.FailedPrecondition);
  });

  it("revokes and reports provider errors", async () => {
    await A.revokeAuth({ serverId: s.id });
    expect((await A.getAuthStatus({ serverIds: [s.id] })).statuses[s.id]!.state).toBe(AuthState.NEEDS_AUTH);
    await expectError(A.listTools({ serverId: s.id }), Code.FailedPrecondition);

    // the provider denies: the flow fails with its error
    const start = await A.startOAuth({ serverId: s.id, returnUrl: "/account/mcp" });
    const denied = new URL(start.authorizationUrl);
    denied.searchParams.set("deny", "1");
    const auth = await fetch(denied, { redirect: "manual" });
    const cb = await fetch(auth.headers.get("location")!, { redirect: "manual" });
    // with a return_url the browser is sent back there
    expect(cb.status).toBe(303);
    const loc = new URL(cb.headers.get("location")!);
    expect(loc.pathname).toBe("/account/mcp");
    expect(loc.searchParams.get("mcp_status")).toBe("failed");
    const flow = await A.getOAuthFlow({ flowId: start.flowId });
    expect(flow.state).toBe(GetOAuthFlowResponse_State.FAILED);
    expect(flow.errorMessage).toContain("access_denied");

    await expectError(A.startOAuth({ serverId: s.id, returnUrl: "https://evil.example.com/" }), Code.InvalidArgument);
    const bad = await fetch(`${ts.url}/api/mcp/oauth/callback?state=nope&code=x`);
    expect(bad.status).toBe(400);
  });
});
