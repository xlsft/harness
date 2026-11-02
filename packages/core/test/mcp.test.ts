/** McpService: registry (CRUD, CAS, scopes, secrets, sync notifications), stdio devices, tool catalog. */
import { toJsonString, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { ErrorInfoSchema, ErrorType } from "../src/gen/harness/common/v1/errors_pb.ts";
import {
  ApprovalState,
  AuthState,
  Availability,
  CreateServerResponseSchema,
  ListServersResponseSchema,
  Transport,
  type ServerSchema,
  type Server,
} from "../src/gen/harness/mcp/v1/mcp_pb.ts";
import { DocumentKind, type WatchChangesResponse } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { purgeCatalogSnapshots } from "../src/mcp/catalog.ts";
import { issueStdioLaunch } from "../src/mcp/devices.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let alice: Awaited<ReturnType<typeof deviceLogin>>;
let bob: Awaited<ReturnType<typeof deviceLogin>>;
let A: TestServer["mcp"];
let B: TestServer["mcp"];
let ADM: TestServer["mcp"];

const SECRET = "s3cret-value-never-returned";

beforeAll(async () => {
  ts = await startTestServer({ HARNESS_SYNC_HEARTBEAT_MS: "60", HARNESS_MCP_LAUNCH_RATE_PER_MIN: "1000" });
  admin = await deviceLogin(ts);
  alice = await createUserAndLogin(ts, admin.cookie, "alice@test.local");
  bob = await createUserAndLogin(ts, admin.cookie, "bob@test.local");
  A = ts.as(alice.accessToken).mcp;
  B = ts.as(bob.accessToken).mcp;
  ADM = ts.as(admin.accessToken).mcp;
});

afterAll(async () => {
  await ts.close();
});

async function expectError(p: Promise<unknown>, code: Code, type?: ErrorType) {
  const err = await p.then(
    () => undefined,
    (e) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  expect((err as ConnectError).code).toBe(code);
  if (type !== undefined) expect((err as ConnectError).findDetails(ErrorInfoSchema)[0]?.type).toBe(type);
  return err as ConnectError;
}

type ServerInit = MessageInitShape<typeof ServerSchema>;
/** Plain init fields (no $typeName), so test helpers can spread overrides. */
type ServerFields = Partial<Omit<ServerInit, "$typeName">>;

function stdio(name: string, extra: ServerFields = {}): ServerInit {
  return {
    name,
    scope: Scope.USER,
    transport: Transport.STDIO,
    enabled: true,
    stdio: { command: "npx", args: ["-y", "some-mcp"], env: { API_KEY: "${secret:API_KEY}" } },
    ...extra,
  };
}

async function create(c: TestServer["mcp"], server: ServerInit, secrets: Record<string, string> = {}): Promise<Server> {
  const r = await c.createServer({ server, secrets: { set: secrets } });
  expect(toJsonString(CreateServerResponseSchema, r)).not.toContain(SECRET);
  return r.server!;
}

describe("registry", () => {
  it("creates, reads and lists servers with write-only secrets", async () => {
    const s = await create(A, stdio("files", { stdio: { command: "npx", args: ["${secret:TOKEN}"], env: { API_KEY: "${secret:API_KEY}" } } }), {
      API_KEY: SECRET,
    });
    expect(s.id).toMatch(/^mcp_/);
    expect(s.version).toBeGreaterThan(0n);
    expect(s.configHash).toMatch(/^[0-9a-f]{64}$/);
    expect(s.scope).toBe(Scope.USER);
    expect(s.secrets.map((x) => [x.name, x.hasValue])).toEqual([
      ["API_KEY", true],
      ["TOKEN", false],
    ]);
    expect(s.secrets[0]!.updatedAt).not.toBe("");
    expect(s.auth?.state).toBe(AuthState.NOT_REQUIRED);

    const got = await A.getServer({ serverId: s.id });
    expect(got.server!.version).toBe(s.version);
    const list = await A.listServers({});
    expect(toJsonString(ListServersResponseSchema, list)).not.toContain(SECRET);
    expect(list.servers.map((x) => x.id)).toContain(s.id);

    // stored encrypted
    const rows = await getDb().select().from(schema.mcpSecret).where(eq(schema.mcpSecret.serverId, s.id));
    expect(rows).toHaveLength(1);
    expect(rows[0]!.valueEnc).not.toContain(SECRET);

    // other users cannot see it
    await expectError(B.getServer({ serverId: s.id }), Code.NotFound);
    expect((await B.listServers({})).servers.map((x) => x.id)).not.toContain(s.id);

    // names are unique per scope (ABORTED, not a CAS conflict)
    await expectError(A.createServer({ server: stdio("files") }), Code.Aborted, ErrorType.CONFLICT);
    // ... but another user may use the same name
    await create(B, stdio("files"));
  });

  it("validates input", async () => {
    const bad = (server: ServerInit, code = Code.InvalidArgument) => expectError(A.createServer({ server }), code);
    await bad(stdio("bad name"));
    await bad(stdio("x", { transport: Transport.UNSPECIFIED }));
    await bad(stdio("x", { stdio: { command: "" } }));
    await bad({ name: "r", scope: Scope.USER, transport: Transport.STREAMABLE_HTTP, remote: { url: "ftp://x" } });
    await bad({ name: "r", scope: Scope.USER, transport: Transport.WS, remote: { url: "https://x" } });
    await bad({ name: "r", scope: Scope.USER, transport: Transport.SSE, remote: { url: "https://u:p@x" } });
    await bad(stdio("x", { scope: Scope.PROJECT }));
    await bad(stdio("x", { scope: Scope.USER, projectKey: "github.com/a/b" }));
    await bad(stdio("x", { targetRoles: ["user"] }));
    await bad(stdio("x", { stdio: { command: "a", env: { "BAD-KEY": "1" } } }));
    await bad(stdio("x", { stdio: { command: "a", args: ["${secret:bad-name}"] } }));
    await bad(stdio("x", { overrides: [{ command: "b" }] }));
    await expectError(A.createServer({ server: stdio("x"), secrets: { set: { "bad-name": "v" } } }), Code.InvalidArgument);
    await expectError(A.createServer({ server: stdio("x"), secrets: { remove: ["A"] } }), Code.InvalidArgument);
    // org scope needs admin
    await bad(stdio("org-x", { scope: Scope.ORG }), Code.PermissionDenied);
  });

  it("updates with CAS, config_hash tracks launch-relevant fields only", async () => {
    const s = await create(A, stdio("cas"), { API_KEY: SECRET });
    // stale base_version -> Conflict response carrying the current server
    const stale = await A.updateServer({ server: { ...stdio("cas", { enabled: false }), id: s.id }, baseVersion: s.version - 1n });
    expect(stale.result.case).toBe("conflict");
    expect(stale.result.case === "conflict" && stale.result.value.current?.version).toBe(s.version);

    // toggling enabled bumps the version but keeps the hash
    const u1 = await A.updateServer({ server: { ...stdio("cas", { enabled: false }), id: s.id }, baseVersion: s.version });
    if (u1.result.case !== "server") throw new Error("expected server");
    const v1 = u1.result.value;
    expect(v1.version).toBeGreaterThan(s.version);
    expect(v1.enabled).toBe(false);
    expect(v1.configHash).toBe(s.configHash);

    // a new secret value keeps the hash (values are not part of it) but bumps the version
    const u2 = await A.updateServer({ server: { ...stdio("cas", { enabled: false }), id: s.id }, baseVersion: v1.version, secrets: { set: { API_KEY: "other" } } });
    if (u2.result.case !== "server") throw new Error("expected server");
    expect(u2.result.value.configHash).toBe(s.configHash);
    expect(u2.result.value.version).toBeGreaterThan(v1.version);

    // identical write is a no-op
    const same = await A.updateServer({ server: { ...stdio("cas", { enabled: false }), id: s.id }, baseVersion: u2.result.value.version });
    expect(same.result.case === "server" && same.result.value.version).toBe(u2.result.value.version);

    // args change -> new hash
    const u3 = await A.updateServer({
      server: { ...stdio("cas", { enabled: false, stdio: { command: "npx", args: ["-y", "other"], env: { API_KEY: "${secret:API_KEY}" } } }), id: s.id },
      baseVersion: u2.result.value.version,
    });
    if (u3.result.case !== "server") throw new Error("expected server");
    expect(u3.result.value.configHash).not.toBe(s.configHash);

    // removing a referenced secret keeps the name with has_value=false
    const u4 = await A.updateServer({ server: { ...u3.result.value }, baseVersion: u3.result.value.version, secrets: { remove: ["API_KEY"] } });
    if (u4.result.case !== "server") throw new Error("expected server");
    expect(u4.result.value.secrets.map((x) => [x.name, x.hasValue])).toEqual([["API_KEY", false]]);

    // scope cannot change; other users cannot update
    await expectError(A.updateServer({ server: { ...u4.result.value, scope: Scope.PROJECT, projectKey: "github.com/a/b" }, baseVersion: u4.result.value.version }), Code.InvalidArgument);
    await expectError(B.updateServer({ server: { ...u4.result.value }, baseVersion: u4.result.value.version }), Code.NotFound);

    // delete: stale -> conflict, then unconditional
    const del1 = await A.deleteServer({ serverId: s.id, baseVersion: s.version });
    expect(del1.conflict?.current?.id).toBe(s.id);
    expect((await A.deleteServer({ serverId: s.id, baseVersion: u4.result.value.version })).conflict).toBeUndefined();
    await expectError(A.getServer({ serverId: s.id }), Code.NotFound);
    await expectError(A.updateServer({ server: { ...u4.result.value }, baseVersion: u4.result.value.version }), Code.NotFound);
    expect(await getDb().select().from(schema.mcpSecret).where(eq(schema.mcpSecret.serverId, s.id))).toHaveLength(0);
  });

  it("org servers: admins write, role targeting controls visibility; project servers filter by key", async () => {
    const everyone = await create(ADM, stdio("org-all", { scope: Scope.ORG }));
    const admins = await create(ADM, stdio("org-admins", { scope: Scope.ORG, targetRoles: ["admin"] }));
    const aliceIds = (await A.listServers({ scopes: [Scope.ORG] })).servers.map((s) => s.id);
    expect(aliceIds).toContain(everyone.id);
    expect(aliceIds).not.toContain(admins.id);
    await expectError(A.getServer({ serverId: admins.id }), Code.NotFound);
    await expectError(A.updateServer({ server: { ...everyone }, baseVersion: everyone.version }), Code.PermissionDenied);
    await expectError(A.deleteServer({ serverId: everyone.id }), Code.PermissionDenied);
    expect((await ADM.listServers({ scopes: [Scope.ORG] })).servers.map((s) => s.id)).toEqual(expect.arrayContaining([everyone.id, admins.id]));

    const p1 = await create(A, stdio("proj", { scope: Scope.PROJECT, projectKey: "github.com/acme/one" }));
    const p2 = await create(A, stdio("proj", { scope: Scope.PROJECT, projectKey: "github.com/acme/two" }));
    const filtered = await A.listServers({ scopes: [Scope.PROJECT], projectKey: "github.com/acme/one" });
    expect(filtered.servers.map((s) => s.id)).toEqual([p1.id]);
    expect((await A.listServers({ scopes: [Scope.PROJECT] })).servers.map((s) => s.id)).toEqual(expect.arrayContaining([p1.id, p2.id]));

    // disabled servers are hidden unless asked for
    const off = await create(A, stdio("off", { enabled: false }));
    expect((await A.listServers({})).servers.map((s) => s.id)).not.toContain(off.id);
    expect((await A.listServers({ includeDisabled: true })).servers.map((s) => s.id)).toContain(off.id);
  });

  it("announces every change through SyncService as DOCUMENT_KIND_MCP_SERVER", async () => {
    const sync = ts.as(alice.accessToken).sync;
    const bobSync = ts.as(bob.accessToken).sync;
    let head = 0n;
    for (;;) {
      const r = await sync.listChanges({ sinceVersion: head, pageSize: 1000 });
      head = r.nextVersion;
      if (!r.hasMore) break;
    }
    const ac = new AbortController();
    const events: WatchChangesResponse[] = [];
    const watching = (async () => {
      try {
        for await (const ev of sync.watchChanges({ sinceVersion: head, kinds: [DocumentKind.MCP_SERVER] }, { signal: ac.signal })) events.push(ev);
      } catch {
        // aborted
      }
    })();
    const waitFor = async (pred: () => boolean) => {
      for (let i = 0; i < 200 && !pred(); i++) await new Promise((r) => setTimeout(r, 20));
      expect(pred()).toBe(true);
    };
    await waitFor(() => events.some((e) => e.event.case === "caughtUp"));

    const s = await create(A, stdio("watched"));
    await waitFor(() => events.some((e) => e.event.case === "change" && e.event.value.change.case === "document" && e.event.value.change.value.key === s.id));
    const doc = events.find((e) => e.event.case === "change" && e.event.value.change.case === "document" && e.event.value.change.value.key === s.id)!;
    if (doc.event.case !== "change" || doc.event.value.change.case !== "document") throw new Error("unreachable");
    expect(doc.event.value.change.value.version).toBe(s.version);
    expect(doc.event.value.change.value.kind).toBe(DocumentKind.MCP_SERVER);
    expect(doc.event.value.change.value.content).toBe("");

    const u = await A.updateServer({ server: { ...s, alwaysLoad: true }, baseVersion: s.version });
    const v2 = u.result.case === "server" ? u.result.value.version : 0n;
    await waitFor(() => events.some((e) => e.event.case === "change" && e.event.value.change.case === "document" && e.event.value.change.value.version === v2));

    await A.deleteServer({ serverId: s.id });
    await waitFor(() => events.some((e) => e.event.case === "change" && e.event.value.change.case === "tombstone" && e.event.value.change.value.key === s.id));
    ac.abort();
    await watching;

    // bob's feed does not contain alice's user-scope servers; org servers are announced to everyone
    const bobFeed = await bobSync.listChanges({ sinceVersion: 0n, kinds: [DocumentKind.MCP_SERVER], pageSize: 1000 });
    const keys = bobFeed.changes.map((c) => (c.change.case === "document" || c.change.case === "tombstone" ? c.change.value.key : ""));
    expect(keys).not.toContain(s.id);
    const org = await create(ADM, stdio("org-announced", { scope: Scope.ORG }));
    const bobFeed2 = await bobSync.listChanges({ sinceVersion: 0n, kinds: [DocumentKind.MCP_SERVER], pageSize: 1000 });
    expect(bobFeed2.changes.some((c) => c.change.case === "document" && c.change.value.key === org.id)).toBe(true);

    // the notification entries cannot be written through SyncService
    await expectError(sync.putDocument({ ref: { scope: Scope.USER, kind: DocumentKind.MCP_SERVER, key: s.id }, content: "" }), Code.InvalidArgument);
  });
});

describe("stdio devices", () => {
  let server: Server;

  beforeAll(async () => {
    server = await create(
      A,
      stdio("launcher", {
        stdio: { command: "npx", args: ["-y", "pkg", "--token=${secret:TOKEN}"], env: { API_KEY: "${secret:API_KEY}", PLAIN: "1" }, cwd: "" },
        overrides: [{ os: "linux", command: "/opt/bin/pkg", env: { EXTRA: "x" } }, { os: "windows", command: "pkg.exe" }],
      }),
      { API_KEY: SECRET, TOKEN: "tok-123" },
    );
  });

  it("rejects launches without an approval for the exact hash", async () => {
    await expectError(A.issueStdioLaunch({ serverId: server.id, configHash: server.configHash }), Code.FailedPrecondition);
    // approved, but for another hash
    await A.reportDeviceStatus({ statuses: [{ serverId: server.id, configHash: "0".repeat(64), availability: Availability.AVAILABLE, approval: ApprovalState.APPROVED }] });
    await expectError(A.issueStdioLaunch({ serverId: server.id, configHash: server.configHash }), Code.FailedPrecondition);
    // the hash the device asks for must match the current config
    await expectError(A.issueStdioLaunch({ serverId: server.id, configHash: "0".repeat(64) }), Code.FailedPrecondition, ErrorType.FAILED_PRECONDITION);
    // pending / denied
    await A.reportDeviceStatus({ statuses: [{ serverId: server.id, configHash: server.configHash, availability: Availability.AVAILABLE, approval: ApprovalState.DENIED }] });
    await expectError(A.issueStdioLaunch({ serverId: server.id, configHash: server.configHash }), Code.FailedPrecondition);
  });

  it("issues the resolved launch for an approved hash, with overrides and secrets", async () => {
    await A.reportDeviceStatus({ statuses: [{ serverId: server.id, configHash: server.configHash, availability: Availability.AVAILABLE, approval: ApprovalState.APPROVED }] });
    const r = await A.issueStdioLaunch({ serverId: server.id, configHash: server.configHash });
    // deviceLogin registers the device with os "linux"
    expect(r.command).toBe("/opt/bin/pkg");
    expect(r.args).toEqual(["-y", "pkg", "--token=tok-123"]);
    expect(r.env).toEqual({ API_KEY: SECRET, PLAIN: "1", EXTRA: "x" });
    const ttl = Date.parse(r.expiresAt) - Date.now();
    expect(ttl).toBeGreaterThan(30_000);
    expect(ttl).toBeLessThanOrEqual(60_000);

    // audit log entry without values
    const audit = await getDb().select().from(schema.mcpAudit).where(eq(schema.mcpAudit.serverId, server.id));
    const launch = audit.find((a) => a.action === "stdio_launch");
    expect(launch?.deviceId).toBe(alice.deviceId);
    expect(launch?.configHash).toBe(server.configHash);
    expect(JSON.stringify(audit)).not.toContain(SECRET);
    expect(JSON.stringify(audit)).not.toContain("tok-123");
  });

  it("rejects non-device callers, foreign servers and missing secrets", async () => {
    // A web session (cookie) is not a device token: the RPC layer does not even authenticate it ...
    const res = await fetch(`${ts.url}/harness.mcp.v1.McpService/IssueStdioLaunch`, {
      method: "POST",
      headers: { "content-type": "application/json", cookie: alice.cookie },
      body: JSON.stringify({ serverId: server.id, configHash: server.configHash }),
    });
    expect(res.status).toBe(401);
    // ... and the service refuses principals that are not device tokens.
    await expect(
      issueStdioLaunch({ user: { id: alice.user!.id, email: "alice@test.local", name: "alice", role: "user" }, via: "session" }, server.id, server.configHash),
    ).rejects.toMatchObject({ type: "permission" });
    // bob cannot launch or report alice's server
    await expectError(B.issueStdioLaunch({ serverId: server.id, configHash: server.configHash }), Code.NotFound);
    await expectError(B.reportDeviceStatus({ statuses: [{ serverId: server.id, configHash: server.configHash, approval: ApprovalState.APPROVED }] }), Code.NotFound);

    // a referenced secret without a value -> FAILED_PRECONDITION
    const s = await create(A, stdio("nosecret"));
    await A.reportDeviceStatus({ statuses: [{ serverId: s.id, configHash: s.configHash, approval: ApprovalState.APPROVED }] });
    await expectError(A.issueStdioLaunch({ serverId: s.id, configHash: s.configHash }), Code.FailedPrecondition);

    // remote servers are never launched
    const remote = await create(A, { name: "remote-x", scope: Scope.USER, transport: Transport.STREAMABLE_HTTP, enabled: true, remote: { url: "https://mcp.example.com/mcp" } });
    await A.reportDeviceStatus({ statuses: [{ serverId: remote.id, configHash: remote.configHash, approval: ApprovalState.APPROVED }] });
    await expectError(A.issueStdioLaunch({ serverId: remote.id, configHash: remote.configHash }), Code.InvalidArgument);
  });

  it("invalidates approvals when the config changes and rate-limits launches", async () => {
    const u = await A.updateServer({ server: { ...server, stdio: { ...server.stdio!, args: ["-y", "pkg@2", "--token=${secret:TOKEN}"] } }, baseVersion: server.version });
    if (u.result.case !== "server") throw new Error("expected server");
    const next = u.result.value;
    expect(next.configHash).not.toBe(server.configHash);
    await expectError(A.issueStdioLaunch({ serverId: server.id, configHash: server.configHash }), Code.FailedPrecondition);
    await expectError(A.issueStdioLaunch({ serverId: server.id, configHash: next.configHash }), Code.FailedPrecondition);
    await A.reportDeviceStatus({ statuses: [{ serverId: server.id, configHash: next.configHash, availability: Availability.AVAILABLE, approval: ApprovalState.APPROVED }] });
    expect((await A.issueStdioLaunch({ serverId: server.id, configHash: next.configHash })).args[1]).toBe("pkg@2");
    server = next;

    process.env.HARNESS_MCP_LAUNCH_RATE_PER_MIN = "2";
    try {
      await A.issueStdioLaunch({ serverId: server.id, configHash: server.configHash });
      await A.issueStdioLaunch({ serverId: server.id, configHash: server.configHash });
      await expectError(A.issueStdioLaunch({ serverId: server.id, configHash: server.configHash }), Code.ResourceExhausted, ErrorType.RATE_LIMITED);
    } finally {
      process.env.HARNESS_MCP_LAUNCH_RATE_PER_MIN = "1000";
    }
  });

  it("lists per-device status", async () => {
    await A.reportDeviceStatus({
      statuses: [
        { serverId: server.id, configHash: server.configHash, availability: Availability.UNAVAILABLE_ON_DEVICE, reason: "command not found: npx", approval: ApprovalState.APPROVED, lastError: "spawn ENOENT" },
      ],
    });
    const r = await A.listDeviceStatus({ serverId: server.id });
    expect(r.entries).toHaveLength(1);
    expect(r.entries[0]!.deviceId).toBe(alice.deviceId);
    expect(r.entries[0]!.deviceName).toBe("test-box");
    expect(r.entries[0]!.status!.availability).toBe(Availability.UNAVAILABLE_ON_DEVICE);
    expect(r.entries[0]!.status!.reason).toBe("command not found: npx");
    expect(r.entries[0]!.status!.reportedAt).not.toBe("");
    expect((await B.listDeviceStatus({})).entries.filter((e) => e.status!.serverId === server.id)).toHaveLength(0);
    await expectError(A.reportDeviceStatus({ statuses: [{ serverId: "mcp_nope" }] }), Code.NotFound);
  });
});

describe("tool catalog", () => {
  it("serves versioned snapshots that stay frozen", async () => {
    const s = await create(A, stdio("cat-stdio", { alwaysLoad: true, stdio: { command: "uvx", args: ["thing"] } }));
    const tool = (name: string) => ({ name, description: `${name} tool`, inputSchemaJson: '{"type":"object","properties":{"q":{"type":"string"}}}', annotationsJson: '{"readOnlyHint":true}' });
    await A.reportDeviceStatus({ statuses: [{ serverId: s.id, configHash: s.configHash, availability: Availability.AVAILABLE, approval: ApprovalState.APPROVED, tools: [tool("alpha")] }] });

    const c1 = await A.getToolCatalog({});
    expect(c1.notModified).toBe(false);
    const v1 = c1.catalog!.version;
    expect(v1).toMatch(/^cat_[0-9a-f]{40}$/);
    const entry = c1.catalog!.servers.find((x) => x.serverId === s.id)!;
    expect(entry.alwaysLoad).toBe(true);
    expect(entry.transport).toBe(Transport.STDIO);
    expect(entry.tools.map((t) => t.name)).toEqual(["alpha"]);
    expect(JSON.parse(entry.tools[0]!.inputSchemaJson)).toEqual({ type: "object", properties: { q: { type: "string" } } });
    expect(entry.toolsUpdatedAt).not.toBe("");

    // unchanged -> not_modified, same version
    expect((await A.getToolCatalog({ ifNoneMatch: v1 })).notModified).toBe(true);
    // a report without tools keeps the last list
    await A.reportDeviceStatus({ statuses: [{ serverId: s.id, configHash: s.configHash, approval: ApprovalState.APPROVED }] });
    expect((await A.getToolCatalog({ ifNoneMatch: v1 })).notModified).toBe(true);

    // tool list changes -> new version; the old one is still served unchanged
    await A.reportDeviceStatus({ statuses: [{ serverId: s.id, configHash: s.configHash, approval: ApprovalState.APPROVED, tools: [tool("alpha"), tool("beta")] }] });
    const c2 = await A.getToolCatalog({ ifNoneMatch: v1 });
    expect(c2.notModified).toBe(false);
    const v2 = c2.catalog!.version;
    expect(v2).not.toBe(v1);
    expect(c2.catalog!.servers.find((x) => x.serverId === s.id)!.tools.map((t) => t.name)).toEqual(["alpha", "beta"]);
    const frozen = await A.getToolCatalog({ version: v1 });
    expect(frozen.catalog!.version).toBe(v1);
    expect(frozen.catalog!.servers.find((x) => x.serverId === s.id)!.tools.map((t) => t.name)).toEqual(["alpha"]);
    expect(frozen.catalog!.createdAt).toBe(c1.catalog!.createdAt);

    // project servers only with their project_key
    const p = await create(A, stdio("cat-proj", { scope: Scope.PROJECT, projectKey: "github.com/acme/cat" }));
    expect((await A.getToolCatalog({})).catalog!.servers.map((x) => x.serverId)).not.toContain(p.id);
    const withProject = await A.getToolCatalog({ projectKey: "github.com/acme/cat" });
    expect(withProject.catalog!.servers.map((x) => x.serverId)).toContain(p.id);
    expect(withProject.catalog!.version).not.toBe(v2);

    // versions are per user, unknown versions are NOT_FOUND
    await expectError(B.getToolCatalog({ version: v1 }), Code.NotFound);
    await expectError(A.getToolCatalog({ version: "cat_unknown" }), Code.NotFound);

    // kept for >= 7 days after the last use
    expect(await purgeCatalogSnapshots(Date.now() + 6 * 86_400_000)).toBe(0);
    expect((await A.getToolCatalog({ version: v1 })).catalog!.version).toBe(v1);
    expect(await purgeCatalogSnapshots(Date.now() + 8 * 86_400_000)).toBeGreaterThan(0);
    await expectError(A.getToolCatalog({ version: v1 }), Code.NotFound);
  });
});
