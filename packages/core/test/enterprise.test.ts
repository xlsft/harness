/**
 * Roles (what auditor and billing may and may not do, on the server), the audit log (coverage, filters, CSV, no secrets),
 * quotas per user, group and role with the personal-provider rule, usage CSV, the managed policy (document contents and what
 * the server enforces itself), session-cookie access for the web console, and serving the built UI.
 */
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Code, ConnectError } from "@connectrpc/connect";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { csvCell, scrub } from "../src/audit/log.ts";
import { getDb, schema } from "../src/db/index.ts";
import { AdminService } from "../src/gen/harness/auth/v1/admin_pb.ts";
import { AuditService } from "../src/gen/harness/auth/v1/audit_pb.ts";
import { BillingService } from "../src/gen/harness/auth/v1/billing_pb.ts";
import { DeviceAuthService, User_Role } from "../src/gen/harness/auth/v1/auth_pb.ts";
import { DirectoryService } from "../src/gen/harness/auth/v1/directory_pb.ts";
import { MfaService } from "../src/gen/harness/auth/v1/mfa_pb.ts";
import { PolicyService } from "../src/gen/harness/auth/v1/managed_policy_pb.ts";
import { SsoService } from "../src/gen/harness/auth/v1/sso_pb.ts";
import { ProviderScope, ProviderService } from "../src/gen/harness/llm/v1/providers_pb.ts";
import { ProviderLimitsService } from "../src/gen/harness/llm/v1/limits_pb.ts";
import { McpService, Transport } from "../src/gen/harness/mcp/v1/mcp_pb.ts";
import { VcsService } from "../src/gen/harness/vcs/v1/vcs_pb.ts";
import { SpeechService } from "../src/gen/harness/speech/v1/speech_pb.ts";
import { HostAgentService, HostsService } from "../src/gen/harness/relay/v1/hosts_pb.ts";
import { SyncService } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { AUDITED } from "../src/rpc/interceptors.ts";
import { adminApi, ADMIN, deviceLogin, signIn, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let adminCreds: Awaited<ReturnType<typeof deviceLogin>>;
let admin: ReturnType<TestServer["as"]>;
let auditor: ReturnType<TestServer["as"]>;
let billing: ReturnType<TestServer["as"]>;
let plain: ReturnType<TestServer["as"]>;
let plainId: string;
let billingId: string;

const code = async (p: Promise<unknown>) => {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
};

async function login(email: string, role: User_Role) {
  const password = `${email.split("@")[0]}-password`;
  const r = await admin.admin.createUser({ name: email.split("@")[0]!, email, password, role });
  return { id: r.user!.id, client: ts.as((await deviceLogin(ts, email, password)).accessToken) };
}

beforeAll(async () => {
  ts = await startTestServer();
  adminCreds = await deviceLogin(ts);
  admin = ts.as(adminCreds.accessToken);
  auditor = (await login("audrey@test.local", User_Role.AUDITOR)).client;
  const b = await login("bill@test.local", User_Role.BILLING);
  billing = b.client;
  billingId = b.id;
  const p = await login("paula@test.local", User_Role.USER);
  plain = p.client;
  plainId = p.id;
});

afterAll(async () => {
  await ts.close();
});

describe("roles", () => {
  it("reports what the caller may do", async () => {
    const mine = async (c: ReturnType<TestServer["as"]>) => (await c.directory.getMyAccess({})).capabilities;
    expect((await auditor.directory.getMyAccess({})).role).toBe(User_Role.AUDITOR);
    expect(await mine(auditor)).toContain("audit.read");
    expect((await mine(auditor)).filter((c) => c.endsWith(".write"))).toEqual([]);
    expect(await mine(billing)).toContain("quotas.write");
    expect(await mine(billing)).not.toContain("audit.read");
    expect(await mine(plain)).toEqual([]);
    expect(await mine(admin)).toContain("users.write");
  });

  it("an auditor reads everything and changes nothing", async () => {
    expect((await auditor.admin.listUsers({})).users.length).toBeGreaterThan(1);
    await auditor.admin.getLdapConfig({});
    await auditor.admin.getPermissionModePolicy({});
    await auditor.policy.getManagedPolicy({});
    await auditor.billing.getQuotas({});
    await auditor.audit.listAuditEvents({});
    await auditor.usage.getUsage({});
    await auditor.mfa.getMfaPolicy({});
    await auditor.admin.listUserSessions({ userId: plainId });

    for (const denied of [
      auditor.admin.createUser({ name: "x", email: "x@test.local", password: "password-12345", role: User_Role.USER }),
      auditor.admin.setRole({ userId: plainId, role: User_Role.ADMIN }),
      auditor.admin.setDisabled({ userId: plainId, disabled: true }),
      auditor.admin.deleteUser({ userId: plainId }),
      auditor.admin.resetPassword({ userId: plainId, generate: true }),
      auditor.admin.setLdapConfig({ config: {} }),
      auditor.admin.setPermissionModePolicy({ policy: { disabledModes: [] } }),
      auditor.policy.setManagedPolicy({ policy: {} }),
      auditor.billing.setUserQuota({ userId: plainId, limits: { dailyUsd: 1 } }),
      auditor.mfa.resetUserMfa({ userId: plainId }),
      auditor.directory.createGroup({ name: "x" }),
      auditor.directory.createScimToken({ name: "x" }),
      auditor.sso.setSsoSettings({ settings: {} }),
    ]) {
      expect(await code(denied)).toBe(Code.PermissionDenied);
    }
  });

  it("billing reads usage and sets quotas; no other administration", async () => {
    await billing.usage.getUsage({});
    await billing.billing.getQuotas({});
    await billing.billing.setUserQuota({ userId: plainId, limits: { monthlyUsd: 50 } });
    await billing.billing.setUserQuota({ userId: plainId, limits: {} });
    await billing.directory.listGroups({});
    for (const denied of [
      billing.audit.listAuditEvents({}),
      billing.admin.setRole({ userId: billingId, role: User_Role.ADMIN }),
      billing.admin.getLdapConfig({}),
      billing.policy.getManagedPolicy({}),
      billing.policy.setManagedPolicy({ policy: {} }),
      billing.sso.listSsoProviders({}),
      billing.directory.createGroup({ name: "x" }),
    ]) {
      expect(await code(denied)).toBe(Code.PermissionDenied);
    }
    const api = await adminApi(ts.url, adminCreds.accessToken, "GET", "/api/admin/quotas");
    expect(api.status).toBe(200);
  });

  it("a plain user gets none of it", async () => {
    for (const denied of [plain.admin.listUsers({}), plain.audit.listAuditEvents({}), plain.billing.getQuotas({}), plain.usage.getUsage({}), plain.directory.listGroups({}), plain.policy.getManagedPolicy({})]) {
      expect(await code(denied)).toBe(Code.PermissionDenied);
    }
  });

  it("the JSON API follows the same table", async () => {
    const token = (await deviceLogin(ts, "audrey@test.local", "audrey-password")).accessToken;
    expect((await adminApi(ts.url, token, "GET", "/api/admin/usage")).status).toBe(200);
    expect((await adminApi(ts.url, token, "GET", "/api/admin/quotas")).status).toBe(200);
    expect((await adminApi(ts.url, token, "PATCH", "/api/admin/quotas", { session_usd: 1 })).status).toBe(403);
    expect((await adminApi(ts.url, token, "GET", "/api/admin/providers")).status).toBe(403);
    const billingToken = (await deviceLogin(ts, "bill@test.local", "bill-password")).accessToken;
    expect((await adminApi(ts.url, billingToken, "PATCH", "/api/admin/quotas", { session_usd: null })).status).toBe(200);
  });

  it("the last admin rule still holds, and role names are checked", async () => {
    const me = (await admin.auth.getMe({})).user!;
    expect(await code(admin.admin.setRole({ userId: me.id, role: User_Role.AUDITOR }))).toBe(Code.FailedPrecondition);
    const r = await admin.admin.setRole({ userId: plainId, role: User_Role.BILLING });
    expect(r.user!.role).toBe(User_Role.BILLING);
    await admin.admin.setRole({ userId: plainId, role: User_Role.USER });
  });
});

describe("audit log", () => {
  it("records sign-ins, failed ones too, without the password", async () => {
    const password = "correct-horse-battery";
    await admin.admin.createUser({ name: "Log", email: "log@test.local", password, role: User_Role.USER });
    await signIn(ts.url, "log@test.local", password);
    const bad = await fetch(`${ts.url}/api/auth/sign-in/email`, {
      method: "POST",
      headers: { "content-type": "application/json", origin: ts.url },
      body: JSON.stringify({ email: "log@test.local", password: "wrong-password-value" }),
    });
    expect(bad.status).toBe(401);

    const ok = (await admin.audit.listAuditEvents({ filter: { action: "auth.sign_in", actor: "log@test.local" } })).events;
    expect(ok).toHaveLength(1);
    expect(JSON.parse(ok[0]!.detailsJson)).toMatchObject({ method: "password" });
    const failed = (await admin.audit.listAuditEvents({ filter: { action: "auth.sign_in_failed", actor: "log@test.local" } })).events;
    expect(failed).toHaveLength(1);
    expect(failed[0]!.outcome).toBe("failure");
    const everything = JSON.stringify((await admin.audit.listAuditEvents({ limit: 500 })).events);
    expect(everything).not.toContain(password);
    expect(everything).not.toContain("wrong-password-value");
  });

  it("records admin changes, also refused ones, and provider key changes without the key", async () => {
    const key = "sk-live-this-is-not-real-key-1234";
    await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, id: "audited-provider", type: "openai-chat", baseUrl: "http://127.0.0.1:9/v1", allowPrivateNetwork: true, apiKey: key });
    await admin.admin.setRole({ userId: plainId, role: User_Role.USER });
    await code(auditor.admin.setRole({ userId: plainId, role: User_Role.ADMIN }));

    const created = (await admin.audit.listAuditEvents({ filter: { action: "provider.create" } })).events;
    expect(created).toHaveLength(1);
    expect(JSON.parse(created[0]!.detailsJson)).toMatchObject({ key_changed: true });

    const roles = (await admin.audit.listAuditEvents({ filter: { action: "user.set_role", target: plainId } })).events;
    expect(roles.map((e) => e.outcome)).toContain("failure");
    expect(roles.map((e) => e.outcome)).toContain("success");
    expect(JSON.stringify((await admin.audit.listAuditEvents({ limit: 500 })).events)).not.toContain(key);
  });

  it("records every kind of policy change", async () => {
    await admin.admin.setPermissionModePolicy({ policy: { disabledModes: [] } });
    await admin.admin.setLocalModelPolicy({ policy: { allowed: true } });
    await admin.policy.setManagedPolicy({ policy: { denyRules: ["Bash(curl:*)"] } });
    const put = await admin.sync.putDocument({ ref: { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "extra", projectKey: "" }, content: "{}", baseVersion: 0n, files: [], source: "test", targetRoles: [] });
    const actions = (await admin.audit.listAuditEvents({ filter: { action: "policy" }, limit: 100 })).events.map((e) => e.action);
    expect(actions).toEqual(expect.arrayContaining(["policy.permission_modes", "policy.local_models", "policy.managed_update", "policy.document_put"]));
    expect(put.result.case).toBe("document");
    await admin.sync.deleteDocument({ id: (put.result.value as { id: string }).id });
    expect((await admin.audit.listAuditEvents({ filter: { action: "policy.document_delete" } })).events).toHaveLength(1);
    await admin.policy.setManagedPolicy({ policy: {} });
  });

  it("lists every audited method against the real services, so a rename cannot silently stop the logging", () => {
    const services = [AdminService, AuditService, BillingService, DirectoryService, MfaService, PolicyService, SsoService, ProviderService, ProviderLimitsService, McpService, VcsService, SpeechService, SyncService, DeviceAuthService, HostsService, HostAgentService];
    const known = new Set(services.flatMap((s) => s.methods.map((m) => `${s.typeName}/${m.name}`)));
    const missing = Object.keys(AUDITED).filter((k) => !known.has(k));
    expect(missing).toEqual([]);
  });

  it("filters by outcome, actor, target, time, and pages", async () => {
    const before = new Date(Date.now() - 1000).toISOString();
    for (let i = 0; i < 3; i++) await admin.directory.createGroup({ name: `paged-${i}` });
    const page1 = await admin.audit.listAuditEvents({ filter: { action: "group.create", target: "paged-", from: before }, limit: 2 });
    expect(page1.events).toHaveLength(2);
    expect(page1.nextPageToken).not.toBe("");
    const page2 = await admin.audit.listAuditEvents({ filter: { action: "group.create", target: "paged-", from: before }, limit: 2, pageToken: page1.nextPageToken });
    expect(page2.events).toHaveLength(1);
    expect(page2.nextPageToken).toBe("");
    expect((await admin.audit.listAuditEvents({ filter: { action: "group.create", to: before } })).events.every((e) => e.at < before)).toBe(true);
    expect((await admin.audit.listAuditEvents({ filter: { outcome: "failure" } })).events.every((e) => e.outcome === "failure")).toBe(true);
    expect(await code(admin.audit.listAuditEvents({ filter: { from: "soon" } }))).toBe(Code.InvalidArgument);
    expect(await code(admin.audit.listAuditEvents({ filter: { outcome: "maybe" } }))).toBe(Code.InvalidArgument);
  });

  it("exports CSV over RPC and HTTP", async () => {
    const r = await auditor.audit.exportAuditCsv({ filter: { action: "group.create" } });
    const text = new TextDecoder().decode(r.csv);
    expect(text.split("\r\n")[0]).toBe("time,action,outcome,actor_id,actor,actor_ip,target_type,target_id,target,details");
    expect(text).toContain("group.create");
    expect(r.rows).toBeGreaterThan(0);
    expect(r.truncated).toBe(false);
    const http = await fetch(`${ts.url}/api/admin/audit.csv?action=group.create`, { headers: { authorization: `Bearer ${adminCreds.accessToken}` } });
    expect(http.headers.get("content-type")).toContain("text/csv");
    expect(await http.text()).toContain("group.create");
    const deniedToken = (await deviceLogin(ts, "paula@test.local", "paula-password")).accessToken;
    expect((await fetch(`${ts.url}/api/admin/audit.csv`, { headers: { authorization: `Bearer ${deniedToken}` } })).status).toBe(403);
  });

  it("scrubs secrets by key and by value, and defuses CSV formulas", () => {
    const out = scrub({
      password: "p",
      client_secret: "s",
      apiKey: "k",
      nested: { refresh_token: "t", note: "hat_abcdef", fine: "ok", jwt: "eyJhbGciOi.eyJzdWIi.sig" },
      pem: "-----BEGIN RSA PRIVATE KEY-----\nabc",
      secret_set: true,
      list: ["sk-abc", "plain"],
    });
    expect(out).toEqual({
      password: "[redacted]",
      client_secret: "[redacted]",
      apiKey: "[redacted]",
      nested: { refresh_token: "[redacted]", note: "[redacted]", fine: "ok", jwt: "[redacted]" },
      pem: "[redacted]",
      secret_set: true,
      list: ["[redacted]", "plain"],
    });
    expect(csvCell("=HYPERLINK(1)")).toBe("'=HYPERLINK(1)");
    expect(csvCell('a"b,c')).toBe('"a""b,c"');
    expect(csvCell("+1")).toBe("'+1");
    expect(csvCell(12)).toBe("12");
  });
});

describe("quotas", () => {
  async function record(userId: string, cost: number, providerId = "org-provider") {
    await getDb().insert(schema.usageRecord).values({
      id: `use_${Math.random().toString(36).slice(2)}`,
      userId,
      modelId: "m1",
      providerId,
      inputTokens: 10,
      outputTokens: 5,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
      costUsd: cost,
      priced: true,
      status: "ok",
      createdAt: new Date(),
    } as never);
  }

  it("user over group over role, strictest group, with sources", async () => {
    const u = await login("quota1@test.local", User_Role.USER);
    const g1 = (await admin.directory.createGroup({ name: "quota-big" })).group!;
    const g2 = (await admin.directory.createGroup({ name: "quota-small" })).group!;
    await admin.directory.setGroupMembers({ id: g1.id, userIds: [u.id] });
    await admin.directory.setGroupMembers({ id: g2.id, userIds: [u.id] });

    await billing.billing.setRoleQuota({ role: User_Role.USER, limits: { dailyUsd: 100 } });
    let s = (await admin.billing.getUserQuota({ userId: u.id })).status!;
    expect(s.effective?.dailyUsd).toBe(100);
    expect(s.dailySource).toBe("role");

    await billing.billing.setGroupQuota({ groupId: g1.id, limits: { dailyUsd: 50, monthlyUsd: 500 } });
    await billing.billing.setGroupQuota({ groupId: g2.id, limits: { dailyUsd: 20 } });
    s = (await admin.billing.getUserQuota({ userId: u.id })).status!;
    expect(s.effective).toMatchObject({ dailyUsd: 20, monthlyUsd: 500 });
    expect(s.dailySource).toBe("group:quota-small");
    expect(s.monthlySource).toBe("group:quota-big");

    await billing.billing.setUserQuota({ userId: u.id, limits: { dailyUsd: 70 } });
    s = (await auditor.billing.getUserQuota({ userId: u.id })).status!;
    expect(s.effective?.dailyUsd).toBe(70);
    expect(s.dailySource).toBe("user");
    expect(s.own?.monthlyUsd).toBeUndefined();

    // The user sees their own too.
    expect((await u.client.billing.getMyQuota({})).status!.effective?.dailyUsd).toBe(70);

    // Clearing goes back down the chain.
    await billing.billing.setUserQuota({ userId: u.id, limits: {} });
    await billing.billing.setRoleQuota({ role: User_Role.USER, limits: {} });
    const q = await auditor.billing.getQuotas({});
    expect(q.users.find((x) => x.userId === u.id)).toBeUndefined();
    expect(q.groups.map((x) => x.groupId)).toEqual(expect.arrayContaining([g1.id, g2.id]));

    // A deleted group takes its limits along.
    await admin.directory.deleteGroup({ id: g2.id });
    expect((await auditor.billing.getQuotas({})).groups.map((x) => x.groupId)).not.toContain(g2.id);
    expect((await admin.billing.getUserQuota({ userId: u.id })).status!.dailySource).toBe("group:quota-big");
    await billing.billing.setGroupQuota({ groupId: g1.id, limits: {} });
  });

  it("a personal provider's spend counts towards no quota and is invisible to admins", async () => {
    const u = await login("quota2@test.local", User_Role.USER);
    await getDb().insert(schema.provider).values({ id: "org-provider", type: "openai-chat", baseUrl: "https://o.example", enabled: true, createdAt: new Date(), updatedAt: new Date() } as never).onConflictDoNothing();
    await getDb().insert(schema.provider).values({ id: "their-own", type: "openai-chat", baseUrl: "https://p.example", enabled: true, ownerUserId: u.id, createdAt: new Date(), updatedAt: new Date() } as never);
    await record(u.id, 3);
    await record(u.id, 40, "their-own");
    const s = (await admin.billing.getUserQuota({ userId: u.id })).status!;
    expect(s.dailySpentUsd).toBeCloseTo(3);
    expect(s.monthlySpentUsd).toBeCloseTo(3);
    expect((await u.client.billing.getMyQuota({})).status!.dailySpentUsd).toBeCloseTo(3);
  });

  it("the gateway enforces a group's limit", async () => {
    const { enforceQuotas } = await import("../src/services/quotas.ts");
    const u = await login("quota3@test.local", User_Role.USER);
    await getDb().insert(schema.provider).values({ id: "org-provider", type: "openai-chat", baseUrl: "https://o.example", enabled: true, createdAt: new Date(), updatedAt: new Date() } as never).onConflictDoNothing();
    const g = (await admin.directory.createGroup({ name: "quota-enforced" })).group!;
    await admin.directory.setGroupMembers({ id: g.id, userIds: [u.id] });
    await admin.billing.setGroupQuota({ groupId: g.id, limits: { dailyUsd: 1 } });
    const pub = { id: u.id, email: "quota3@test.local", name: "q", role: "user" as const };
    await expect(enforceQuotas(pub, undefined)).resolves.toBeUndefined();
    await record(u.id, 2);
    await expect(enforceQuotas(pub, undefined)).rejects.toThrow(/Daily spend limit/);
    await admin.billing.setGroupQuota({ groupId: g.id, limits: {} });
  });

  it("the session budget is set and cleared", async () => {
    await billing.billing.setSessionBudget({ sessionUsd: 5 });
    expect((await auditor.billing.getQuotas({})).sessionUsd).toBe(5);
    await billing.billing.setSessionBudget({});
    expect((await auditor.billing.getQuotas({})).sessionUsd).toBeUndefined();
  });

  it("refuses nonsense", async () => {
    expect(await code(billing.billing.setUserQuota({ userId: plainId, limits: { dailyUsd: -1 } }))).toBe(Code.InvalidArgument);
    expect(await code(billing.billing.setUserQuota({ limits: { dailyUsd: 1 } }))).toBe(Code.InvalidArgument);
  });
});

describe("usage CSV", () => {
  it("exports with personal records reduced, and only one's own for a plain user", async () => {
    const u = await login("csv@test.local", User_Role.USER);
    await getDb().insert(schema.provider).values({ id: "csv-own", type: "openai-chat", baseUrl: "https://p.example", enabled: true, ownerUserId: u.id, createdAt: new Date(), updatedAt: new Date(), displayName: "Secret Project" } as never);
    await getDb().insert(schema.usageRecord).values({ id: "use_csv1", userId: u.id, modelId: "private-model", providerId: "csv-own", inputTokens: 7, outputTokens: 1, cacheReadTokens: 0, cacheWriteTokens: 0, costUsd: 9, priced: true, status: "ok", createdAt: new Date() } as never);
    await getDb().insert(schema.usageRecord).values({ id: "use_csv2", userId: plainId, modelId: "org-model", providerId: "org-provider", inputTokens: 11, outputTokens: 2, cacheReadTokens: 0, cacheWriteTokens: 0, costUsd: 1.5, priced: true, status: "ok", createdAt: new Date() } as never);

    const all = new TextDecoder().decode((await auditor.billing.exportUsageCsv({})).csv);
    expect(all.split("\r\n")[0]).toContain("time,user_id,email");
    expect(all).toContain("org-model");
    expect(all).toContain("csv@test.local,csv,,personal,,,,7,1,0,0,,");
    expect(all).not.toContain("private-model");
    expect(all).not.toContain("Secret Project");

    const own = new TextDecoder().decode((await u.client.billing.exportUsageCsv({})).csv);
    expect(own).toContain("private-model");
    expect(own).not.toContain("org-model");
    expect(await code(u.client.billing.exportUsageCsv({ userId: plainId }))).toBe(Code.PermissionDenied);
    expect(await code(u.client.billing.exportUsageCsv({ groupId: "x" }))).toBe(Code.PermissionDenied);

    const token = (await deviceLogin(ts, "bill@test.local", "bill-password")).accessToken;
    const http = await fetch(`${ts.url}/api/admin/usage.csv?user_id=${plainId}`, { headers: { authorization: `Bearer ${token}` } });
    expect(http.status).toBe(200);
    expect(await http.text()).toContain("org-model");
  });
});

describe("managed policy", () => {
  it("is stored in the document clients read, and keeps what else lives there", async () => {
    await admin.admin.setLocalModelPolicy({ policy: { allowed: false } });
    await admin.policy.setManagedPolicy({
      policy: {
        allowRules: ["Read"],
        denyRules: ["Bash(rm:*)"],
        managedRulesOnly: true,
        sandboxRequired: true,
        sandboxOutsideRefused: true,
        restrictProviders: true,
        allowedProviders: ["corp-gateway"],
        personalProvidersDisabled: true,
        restrictMcpServers: true,
        allowedMcpServers: ["github"],
      },
    });
    const doc = await plain.sync.getDocument({ selector: { case: "ref", value: { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default", projectKey: "" } } });
    expect(JSON.parse(doc.document!.content)).toEqual({
      disableLocalModels: true,
      permissions: { allow: ["Read"], deny: ["Bash(rm:*)"] },
      sandbox: { enabled: true, allowOutside: false },
      allowManagedPermissionRulesOnly: true,
      allowedProviders: ["corp-gateway"],
      disablePersonalProviders: true,
      allowedMcpServers: ["github"],
    });
    const read = await auditor.policy.getManagedPolicy({});
    expect(read.policy).toMatchObject({ denyRules: ["Bash(rm:*)"], sandboxRequired: true, restrictProviders: true });
    expect(read.updatedAt).not.toBe("");

    await admin.policy.setManagedPolicy({ policy: {} });
    await admin.admin.setLocalModelPolicy({ policy: { allowed: true } });
    const cleared = await plain.sync.getDocument({ selector: { case: "ref", value: { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default", projectKey: "" } } });
    expect(JSON.parse(cleared.document!.content)).toEqual({});
  });

  it("keeps MCP servers out of reach, and still lets admins see them to clean up", async () => {
    const remote = (name: string) => ({ name, scope: Scope.USER, transport: Transport.STREAMABLE_HTTP, remote: { url: "http://127.0.0.1:9/mcp", headers: {} }, enabled: true });
    const github = (await plain.mcp.createServer({ server: remote("github") })).server!;
    const slack = (await plain.mcp.createServer({ server: remote("slack") })).server!;
    await admin.policy.setManagedPolicy({ policy: { restrictMcpServers: true, allowedMcpServers: ["github"] } });
    try {
      expect((await plain.mcp.listServers({})).servers.map((s) => s.name)).toEqual(["github"]);
      expect((await plain.mcp.listServers({ includeDisabled: true })).servers.map((s) => s.name).sort()).toEqual(["github", "slack"]);
      expect(await code(plain.mcp.listTools({ serverId: slack.id }))).toBe(Code.PermissionDenied);
      expect(await code(plain.mcp.createServer({ server: remote("jira") }))).toBe(Code.PermissionDenied);
      expect((await admin.policy.getManagedPolicy({})).mcpServers.map((c) => c.id)).toEqual(expect.arrayContaining(["github", "slack"]));
    } finally {
      await admin.policy.setManagedPolicy({ policy: {} });
      await plain.mcp.deleteServer({ serverId: github.id });
      await plain.mcp.deleteServer({ serverId: slack.id });
    }
  });

  it("refuses what is not a permission rule", async () => {
    expect(await code(admin.policy.setManagedPolicy({ policy: { denyRules: ["(((not a rule"] } }))).toBe(Code.InvalidArgument);
  });

  it("users cannot write it, and the server keeps providers out of reach", async () => {
    expect(await code(plain.sync.putDocument({ ref: { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default", projectKey: "" }, content: "{}", baseVersion: 0n, files: [], source: "", targetRoles: [] }))).toBe(Code.PermissionDenied);

    const { providerAllowed, mcpAllowed, serverPolicyFor } = await import("../src/services/managed-policy.ts");
    await admin.policy.setManagedPolicy({ policy: { restrictProviders: true, allowedProviders: ["good", "preset-x"], personalProvidersDisabled: true, restrictMcpServers: true, allowedMcpServers: ["github"] } });
    const p = await serverPolicyFor({ role: "user" });
    expect(providerAllowed(p, { id: "good" })).toBe(true);
    expect(providerAllowed(p, { id: "other", presetId: "preset-x" })).toBe(true);
    expect(providerAllowed(p, { id: "bad" })).toBe(false);
    expect(providerAllowed(p, { id: "good", ownerUserId: "u" })).toBe(false);
    expect(mcpAllowed(p, "github")).toBe(true);
    expect(mcpAllowed(p, "slack")).toBe(false);

    // A personal provider cannot be added, and models of others are gone from the catalog and refused.
    expect(await code(plain.providers.createProvider({ scope: 2, name: "mine", type: "openai-chat", baseUrl: "https://x.example/v1", apiKey: "k" } as never))).toBe(Code.PermissionDenied);
    const { resolveModelForUser, listModelsForUser } = await import("../src/services/models.ts");
    const now = new Date();
    await getDb().insert(schema.provider).values({ id: "bad", type: "openai-chat", baseUrl: "https://b.example", enabled: true, createdAt: now, updatedAt: now } as never).onConflictDoNothing();
    await getDb().insert(schema.model).values({ id: "bad-model", providerId: "bad", upstreamModel: "b", displayName: "Bad", enabled: true, contextWindow: 1000, maxOutputTokens: 100, capabilities: {}, createdAt: now, updatedAt: now } as never).onConflictDoNothing();
    const pub = { id: plainId, email: "p", name: "p", role: "user" as const };
    expect((await listModelsForUser(pub)).models.map((m) => m.id)).not.toContain("bad-model");
    await expect(resolveModelForUser("bad-model", pub)).rejects.toThrow(/does not allow/);

    await admin.policy.setManagedPolicy({ policy: {} });
    expect((await listModelsForUser(pub)).models.map((m) => m.id)).toContain("bad-model");
    await resolveModelForUser("bad-model", pub);
    await getDb().delete(schema.model).where(eq(schema.model.id, "bad-model"));
  });
});

describe("web console and the served UI", () => {
  it("takes the session cookie from its own origin only, and only for administration", async () => {
    const cookie = await signIn(ts.url, ADMIN.email, ADMIN.password);
    const call = (path: string, origin?: string) =>
      fetch(`${ts.url}${path}`, {
        method: "POST",
        headers: { "content-type": "application/json", "connect-protocol-version": "1", cookie, ...(origin ? { origin } : {}) },
        body: "{}",
      });
    expect((await call("/harness.auth.v1.AdminService/ListUsers", ts.url)).status).toBe(200);
    expect((await call("/harness.auth.v1.AdminService/ListUsers", "https://evil.example")).status).toBe(401);
    expect((await call("/harness.auth.v1.AdminService/ListUsers")).status).toBe(401);
    expect((await call("/harness.llm.v1.GatewayService/ListModels", ts.url)).status).toBe(401);
    expect((await call("/harness.sync.v1.SyncService/GetDocument", ts.url)).status).toBe(401);
  });

  it("serves the app when it is there, with its routes, and never over the APIs", async () => {
    expect((await fetch(`${ts.url}/`)).status).toBe(404);
    const dir = mkdtempSync(join(tmpdir(), "harness-web-"));
    mkdirSync(join(dir, "_nuxt"));
    writeFileSync(join(dir, "200.html"), "<!doctype html><title>app</title>");
    writeFileSync(join(dir, "index.html"), "<!doctype html><title>index</title>");
    writeFileSync(join(dir, "_nuxt", "a.js"), "console.log(1)");
    writeFileSync(join(dir, "..", "harness-outside-web.txt"), "outside");
    process.env.HARNESS_WEB_DIR = dir;
    try {
      const root = await fetch(`${ts.url}/`);
      expect(root.status).toBe(200);
      expect(root.headers.get("content-security-policy")).toContain("frame-ancestors 'none'");
      expect(await root.text()).toContain("index");
      expect(await fetch(`${ts.url}/settings`).then((r) => r.text())).toContain("app");
      const js = await fetch(`${ts.url}/_nuxt/a.js`);
      expect(js.headers.get("content-type")).toContain("javascript");
      expect(js.headers.get("cache-control")).toContain("immutable");
      expect((await fetch(`${ts.url}/_nuxt/missing.js`)).status).toBe(404);
      expect((await fetch(`${ts.url}/..%2Fharness-outside-web.txt`)).status).toBe(404);
      // The server's own routes are unaffected.
      expect((await fetch(`${ts.url}/api/health`)).status).toBe(200);
      expect(await fetch(`${ts.url}/device`).then((r) => r.text())).toContain("Sign in");
      expect((await fetch(`${ts.url}/api/nothing`)).status).toBe(404);
    } finally {
      delete process.env.HARNESS_WEB_DIR;
    }
  });

  it("signing in from the web UI returns to it", async () => {
    const page = await fetch(`${ts.url}/device?return=/`).then((r) => r.text());
    expect(page).toContain('location.href = "/"');
    const cookie = await signIn(ts.url, ADMIN.email, ADMIN.password);
    const signedIn = await fetch(`${ts.url}/device?return=/`, { headers: { cookie }, redirect: "manual" });
    expect(signedIn.status).toBe(303);
    expect(signedIn.headers.get("location")).toBe("/");
    // No open redirect through it.
    expect(await fetch(`${ts.url}/device?return=https://evil.example`).then((r) => r.text())).not.toContain("evil.example");
  });
});
