/**
 * Custom roles (docs/design/roles.md): the built-ins' access is exactly what it was before custom roles, every
 * permission of the catalog opens its entry points to a custom role and nothing else does, nobody grants more than they
 * hold, and every change is audited.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { BUILTIN_ROLES, CAPABILITIES, capabilitiesOf, PERMISSION_CATALOG, type Capability } from "../src/auth/roles.ts";
import { User_Role } from "../src/gen/harness/auth/v1/auth_pb.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { ProjectScope } from "../src/gen/harness/library/v1/projects_pb.ts";
import { AgentHostScope } from "../src/gen/harness/relay/v1/hosts_pb.ts";
import { DocumentKind } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { adminApi, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

/** The table of auth/roles.ts before custom roles, copied here so a change to the built-ins fails this test. */
const BEFORE: Record<string, string[]> = {
  admin: [
    "users.read", "users.write", "audit.read", "usage.read", "quotas.read", "quotas.write", "policy.read", "policy.write",
    "sign_in.read", "sign_in.write", "directory.read", "directory.write", "limits.read", "limits.write", "hosts.read", "hosts.write",
  ],
  auditor: ["users.read", "audit.read", "usage.read", "quotas.read", "policy.read", "sign_in.read", "directory.read", "limits.read", "hosts.read"],
  billing: ["usage.read", "quotas.read", "quotas.write", "users.read", "directory.read", "limits.read"],
  user: [],
};
/** Checks that were `role === "admin"` before; each is a permission now that only admin holds among the built-ins. */
const WAS_ADMIN_ONLY = ["policy.documents", "providers.write", "mcp.write", "mcp.audit", "projects.write", "connections.write"];
/** New with custom roles: who may manage roles. auditor reads them (it reads users, who have them). */
const NEW = { "roles.read": ["admin", "auditor"], "roles.write": ["admin"] } as Record<string, string[]>;

describe("built-in roles keep their access", () => {
  it("the permissions that existed before are granted exactly as before", () => {
    for (const role of BUILTIN_ROLES) {
      const now = capabilitiesOf(role).filter((c) => BEFORE.admin!.includes(c));
      expect([...now].sort(), role).toEqual([...BEFORE[role]!].sort());
    }
  });

  it("what was admin-only stays admin-only, and the new ones go where documented", () => {
    for (const p of CAPABILITIES) {
      if (BEFORE.admin!.includes(p)) continue;
      const holders = BUILTIN_ROLES.filter((r) => capabilitiesOf(r).includes(p as Capability));
      if (WAS_ADMIN_ONLY.includes(p)) expect(holders, p).toEqual(["admin"]);
      else expect(holders.sort(), p).toEqual([...(NEW[p] ?? ["unlisted permission: add it to this test"])].sort());
    }
  });

  it("admin holds the whole catalog, user none of it; ids are unique", () => {
    expect([...capabilitiesOf("admin")].sort()).toEqual([...CAPABILITIES].sort());
    expect(capabilitiesOf("user")).toEqual([]);
    expect(new Set(PERMISSION_CATALOG.map((p) => p.id)).size).toBe(PERMISSION_CATALOG.length);
    expect(capabilitiesOf("role_does_not_exist")).toEqual([]);
  });
});

let ts: TestServer;
let admin: ReturnType<TestServer["as"]>;

const code = async (p: Promise<unknown>) => {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
};

let n = 0;
/** A user with a new custom role holding `permissions`. */
async function withRole(permissions: string[]) {
  n++;
  const role = (await admin.roles.createRole({ name: `Probe ${n}`, permissions })).role!;
  const email = `probe${n}@test.local`;
  const password = `probe-${n}-password`;
  const u = (await admin.admin.createUser({ name: `Probe ${n}`, email, password, roleId: role.id })).user!;
  const creds = await deviceLogin(ts, email, password);
  return { role, id: u.id, token: creds.accessToken, c: ts.as(creds.accessToken) };
}

beforeAll(async () => {
  ts = await startTestServer();
  admin = ts.as((await deviceLogin(ts)).accessToken);
});

afterAll(async () => {
  await ts.close();
});

type C = ReturnType<TestServer["as"]>;
/** One entry point per permission: allowed means anything but PERMISSION_DENIED (inputs may be incomplete on purpose). */
const PROBES: Record<Capability, (c: C, token: string) => Promise<unknown>> = {
  "users.read": (c) => c.admin.listUsers({}),
  "users.write": (c) => c.admin.createUser({ name: "x", email: `made${Math.random()}@test.local`, password: "password-12345", role: User_Role.USER }),
  "roles.read": (c) => c.roles.listRoles({}),
  "roles.write": (c) => c.roles.createRole({ name: `Made ${Math.random()}`, permissions: [] }),
  "directory.read": (c) => c.directory.listGroups({}),
  "directory.write": (c) => c.directory.createGroup({ name: `g${Math.random()}` }),
  "usage.read": (c) => c.usage.getUsage({}),
  "quotas.read": (c) => c.billing.getQuotas({}),
  "quotas.write": (c) => c.billing.setSessionBudget({}),
  "audit.read": (c) => c.audit.listAuditEvents({}),
  "policy.read": (c) => c.policy.getManagedPolicy({}),
  "policy.write": (c) => c.admin.setLocalModelPolicy({ policy: { allowed: true } }),
  "policy.documents": (c) =>
    c.sync.putDocument({ ref: { scope: Scope.ORG, kind: DocumentKind.POLICY, key: `probe-${Math.random()}`, projectKey: "" }, content: "{}", baseVersion: 0n, files: [], source: "test", targetRoles: [] }),
  "sign_in.read": (c) => c.sso.listSsoProviders({}),
  "sign_in.write": (c) => c.directory.createScimToken({ name: "probe" }),
  "providers.write": async (_c, token) => {
    const r = await adminApi(ts.url, token, "GET", "/api/admin/providers");
    if (r.status === 403) throw new ConnectError("denied", Code.PermissionDenied);
  },
  "limits.read": (c) => c.admin.listProviderLimitsPolicies({}),
  "limits.write": (c) => c.admin.setProviderLimitsPolicy({ policy: { providerId: "nope" } }),
  "mcp.write": async (c) => {
    if (!(await c.mcp.listServers({})).canManageOrg) throw new ConnectError("denied", Code.PermissionDenied);
  },
  "mcp.audit": (c) => c.mcp.listAudit({}),
  "hosts.read": (c) => c.hosts.listAgentHosts({ all: true }),
  "hosts.write": (c) => c.hosts.createAgentHost({ name: `team-${Math.random()}`.slice(0, 20), scope: AgentHostScope.TEAM }),
  "projects.write": (c) => c.projects.createProject({ scope: ProjectScope.GLOBAL, metadata: { ticker: `G${n}${Math.floor(Math.random() * 1000)}`.slice(0, 6), name: "Global" } }),
  "connections.write": (c) => c.vcs.putConnection({}),
};

describe("custom roles", () => {
  it("every permission has a probe", () => {
    expect(Object.keys(PROBES).sort()).toEqual([...CAPABILITIES].sort());
  });

  it("a role with no permissions is denied every entry point", async () => {
    const u = await withRole([]);
    for (const [perm, probe] of Object.entries(PROBES)) expect(await code(probe(u.c, u.token)), perm).toBe(Code.PermissionDenied);
  });

  it("each permission opens its own entry point and no other", async () => {
    for (const perm of CAPABILITIES) {
      const u = await withRole([perm]);
      expect(await code(PROBES[perm as Capability](u.c, u.token)), `${perm} allows`).not.toBe(Code.PermissionDenied);
      // ListRoles is also open to who reads users or quotas (they need the names).
      const alsoOpen: Record<string, string[]> = { "roles.read": ["users.read", "quotas.read"] };
      for (const other of CAPABILITIES) {
        if (other === perm || alsoOpen[other]?.includes(perm)) continue;
        // users.write's probe creates a user; that needs nothing else, but other probes are independent of it.
        expect(await code(PROBES[other as Capability](u.c, u.token)), `${perm} must not allow ${other}`).toBe(Code.PermissionDenied);
      }
    }
  }, 600_000);

  it("reports the caller's role and permissions", async () => {
    const u = await withRole(["audit.read", "usage.read"]);
    const a = await u.c.directory.getMyAccess({});
    expect(a.roleId).toBe(u.role.id);
    expect(a.roleName).toBe(u.role.name);
    expect(a.role).toBe(User_Role.UNSPECIFIED);
    expect([...a.capabilities].sort()).toEqual(["audit.read", "usage.read"]);
    const me = (await u.c.auth.getMe({})).user!;
    expect(me.roleId).toBe(u.role.id);
  });

  it("lists built-ins first with member counts, and refuses unknown permissions and taken names", async () => {
    const roles = (await admin.roles.listRoles({})).roles;
    expect(roles.slice(0, 4).map((r) => r.id)).toEqual(["admin", "auditor", "billing", "user"]);
    expect(roles.find((r) => r.id === "admin")!.memberCount).toBeGreaterThanOrEqual(1);
    expect(roles.find((r) => r.id === "admin")!.editable).toBe(false);
    expect(await code(admin.roles.createRole({ name: "Bad", permissions: ["everything.write"] }))).toBe(Code.InvalidArgument);
    expect(await code(admin.roles.createRole({ name: "auditor", permissions: [] }))).toBe(Code.AlreadyExists);
    await admin.roles.createRole({ name: "Support", permissions: ["users.read"] });
    expect(await code(admin.roles.createRole({ name: "SUPPORT", permissions: [] }))).toBe(Code.AlreadyExists);
    expect(await code(admin.roles.updateRole({ id: "admin", name: "Root" }))).toBe(Code.FailedPrecondition);
    expect(await code(admin.roles.deleteRole({ id: "user" }))).toBe(Code.FailedPrecondition);
  });

  it("renames, re-describes and changes permissions, effective at once", async () => {
    const u = await withRole(["audit.read"]);
    expect(await code(u.c.usage.getUsage({}))).toBe(Code.PermissionDenied);
    const r = (await admin.roles.updateRole({ id: u.role.id, name: "Analyst", description: "Reads", permissions: ["usage.read"], setPermissions: true })).role!;
    expect(r).toMatchObject({ name: "Analyst", description: "Reads", permissions: ["usage.read"], memberCount: 1 });
    await u.c.usage.getUsage({});
    expect(await code(u.c.audit.listAuditEvents({}))).toBe(Code.PermissionDenied);
    // Without set_permissions the permissions stay.
    expect((await admin.roles.updateRole({ id: u.role.id, description: "Reads usage" })).role!.permissions).toEqual(["usage.read"]);
  });

  it("deleting a role in use needs confirmation and moves its members to User", async () => {
    const u = await withRole(["audit.read"]);
    await admin.billing.setRoleQuota({ roleId: u.role.id, limits: { dailyUsd: 3 } });
    expect((await admin.billing.getQuotas({})).roles.map((q) => q.roleId)).toContain(u.role.id);
    expect(await code(admin.roles.deleteRole({ id: u.role.id }))).toBe(Code.FailedPrecondition);
    expect((await admin.roles.deleteRole({ id: u.role.id, moveMembersToUser: true })).movedMembers).toBe(1);
    expect((await admin.admin.listUsers({})).users.find((x) => x.id === u.id)!.roleId).toBe("user");
    expect(await code(u.c.audit.listAuditEvents({}))).toBe(Code.PermissionDenied);
    expect((await admin.billing.getQuotas({})).roles.map((q) => q.roleId)).not.toContain(u.role.id);
  });

  it("nobody grants, creates or reaches past their own permissions", async () => {
    const mgr = await withRole(["users.read", "users.write", "roles.read", "roles.write"]);
    const target = await withRole(["audit.read"]);
    const plain = await withRole([]);
    // Creating and changing roles: only with permissions the caller holds.
    expect(await code(mgr.c.roles.createRole({ name: "Escalate", permissions: ["audit.read"] }))).toBe(Code.PermissionDenied);
    const ok = (await mgr.c.roles.createRole({ name: "Helpdesk", permissions: ["users.read"] })).role!;
    expect(ok.editable).toBe(true);
    expect(await code(mgr.c.roles.updateRole({ id: ok.id, permissions: ["users.read", "audit.read"], setPermissions: true }))).toBe(Code.PermissionDenied);
    expect(await code(mgr.c.roles.updateRole({ id: target.role.id, name: "Mine now" }))).toBe(Code.PermissionDenied);
    expect(await code(mgr.c.roles.deleteRole({ id: target.role.id, moveMembersToUser: true }))).toBe(Code.PermissionDenied);
    // Giving roles: never admin, auditor or anything with more than the caller has; never to themselves either.
    for (const r of [{ role: User_Role.ADMIN }, { role: User_Role.AUDITOR }, { roleId: target.role.id }]) {
      expect(await code(mgr.c.admin.setRole({ userId: plain.id, ...r })), JSON.stringify(r)).toBe(Code.PermissionDenied);
      expect(await code(mgr.c.admin.setRole({ userId: mgr.id, ...r })), JSON.stringify(r)).toBe(Code.PermissionDenied);
      expect(await code(mgr.c.admin.createUser({ name: "e", email: `esc${Math.random()}@test.local`, password: "password-12345", ...r }))).toBe(Code.PermissionDenied);
    }
    await mgr.c.admin.setRole({ userId: plain.id, roleId: ok.id });
    // Reaching into an account whose role has more: no password resets, disabling, deleting or role changes.
    const me = (await admin.auth.getMe({})).user!;
    for (const id of [me.id, target.id]) {
      expect(await code(mgr.c.admin.resetPassword({ userId: id, generate: true }))).toBe(Code.PermissionDenied);
      expect(await code(mgr.c.admin.setDisabled({ userId: id, disabled: true }))).toBe(Code.PermissionDenied);
      expect(await code(mgr.c.admin.deleteUser({ userId: id }))).toBe(Code.PermissionDenied);
      expect(await code(mgr.c.admin.setRole({ userId: id, role: User_Role.USER }))).toBe(Code.PermissionDenied);
      expect(await code(mgr.c.admin.revokeUserSessions({ userId: id }))).toBe(Code.PermissionDenied);
    }
    // Directory mappings cannot hand out more either.
    const sso = await withRole(["sign_in.read", "sign_in.write"]);
    expect(await code(sso.c.directory.setScimSettings({ settings: { roleMappings: [{ group: "Admins", role: User_Role.ADMIN }] } }))).toBe(Code.PermissionDenied);
    await sso.c.directory.setScimSettings({ settings: { roleMappings: [] } });
  });

  it("unknown role ids are refused wherever a role is given", async () => {
    const plain = await withRole([]);
    expect(await code(admin.admin.setRole({ userId: plain.id, roleId: "role_nope" }))).toBe(Code.InvalidArgument);
    expect(await code(admin.billing.setRoleQuota({ roleId: "role_nope", limits: { dailyUsd: 1 } }))).toBe(Code.InvalidArgument);
    expect(await code(admin.directory.setScimSettings({ settings: { roleMappings: [{ group: "x", role: User_Role.USER, roleId: "role_nope" }] } }))).toBe(Code.InvalidArgument);
  });

  it("custom roles work in mappings, quotas, the MFA policy and project shares", async () => {
    const u = await withRole(["usage.read"]);
    const s = await admin.directory.setScimSettings({ settings: { roleMappings: [{ group: "Analysts", role: User_Role.UNSPECIFIED, roleId: u.role.id }] } });
    expect(s.settings!.roleMappings[0]).toMatchObject({ group: "Analysts", roleId: u.role.id, role: User_Role.UNSPECIFIED });
    await admin.directory.setScimSettings({ settings: { roleMappings: [] } });
    const mfa = await admin.mfa.setMfaPolicy({ policy: { required: false, roleIds: [u.role.id, "auditor"], exemptSso: false } });
    expect(mfa.policy!.roleIds).toEqual([u.role.id, "auditor"]);
    expect(mfa.policy!.roles).toEqual([User_Role.AUDITOR]);
    await admin.mfa.setMfaPolicy({ policy: { required: false, roles: [], exemptSso: false } });
  });

  it("records every change in the audit log", async () => {
    const r = (await admin.roles.createRole({ name: "Audited", permissions: ["audit.read"] })).role!;
    await admin.roles.updateRole({ id: r.id, name: "Audited 2" });
    await admin.roles.deleteRole({ id: r.id });
    const u = await withRole([]);
    await admin.admin.setRole({ userId: u.id, roleId: r.id }).catch(() => undefined);
    const actions = (await admin.audit.listAuditEvents({ limit: 500 })).events.map((e) => e.action);
    for (const a of ["role.create", "role.update", "role.delete", "user.set_role"]) expect(actions).toContain(a);
    const created = (await admin.audit.listAuditEvents({ filter: { action: "role.create", target: "Audited" } })).events[0]!;
    expect(JSON.parse(created.detailsJson)).toMatchObject({ permissions: ["audit.read"] });
  });
});
