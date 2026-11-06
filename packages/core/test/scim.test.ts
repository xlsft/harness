/**
 * SCIM 2.0 as a provisioning client drives it: tokens, the user lifecycle (create, match by filter, patch in the dialects
 * Okta and Azure AD use, deactivate, delete), groups and memberships, role mapping, discovery, and the audit trail.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { listAudit } from "../src/audit/log.ts";
import { User_Role, UserSource } from "../src/gen/harness/auth/v1/auth_pb.ts";
import { parseFilter, ScimError } from "../src/scim/service.ts";
import { deviceLogin, signIn, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let admin: ReturnType<TestServer["as"]>;
let token: string;

async function scim(method: string, path: string, body?: unknown, auth: string | null = token) {
  const res = await fetch(`${ts.url}/scim/v2${path}`, {
    method,
    headers: { "content-type": "application/scim+json", ...(auth ? { authorization: `Bearer ${auth}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  return { status: res.status, body: text ? JSON.parse(text) : undefined, type: res.headers.get("content-type") };
}

const patch = (Operations: unknown[]) => ({ schemas: ["urn:ietf:params:scim:api:messages:2.0:PatchOp"], Operations });
const newUser = (userName: string, over: Record<string, unknown> = {}) => ({
  schemas: ["urn:ietf:params:scim:schemas:core:2.0:User"],
  userName,
  name: { givenName: "Ada", familyName: "Lovelace" },
  emails: [{ value: userName.includes("@") ? userName : `${userName}@corp.example`, primary: true, type: "work" }],
  active: true,
  ...over,
});

beforeAll(async () => {
  ts = await startTestServer();
  admin = ts.as((await deviceLogin(ts)).accessToken);
  token = (await admin.directory.createScimToken({ name: "okta" })).value;
});

afterAll(async () => {
  await ts.close();
});

describe("tokens", () => {
  it("refuses requests without a live token", async () => {
    expect((await scim("GET", "/Users", undefined, null)).status).toBe(401);
    expect((await scim("GET", "/Users", undefined, "hsc_wrong")).status).toBe(401);
    const t = await admin.directory.createScimToken({ name: "old" });
    expect((await scim("GET", "/Users", undefined, t.value)).status).toBe(200);
    await admin.directory.revokeScimToken({ id: t.token!.id });
    expect((await scim("GET", "/Users", undefined, t.value)).status).toBe(401);
  });

  it("shows a token's value once, and only to admins", async () => {
    const s = await admin.directory.getScimSettings({});
    expect(s.baseUrl).toBe(`${ts.url}/scim/v2`);
    expect(JSON.stringify(s)).not.toContain(token);
    await admin.admin.createUser({ name: "P", email: "plain-scim@test.local", password: "plain-password-1", role: User_Role.USER });
    const plain = ts.as((await deviceLogin(ts, "plain-scim@test.local", "plain-password-1")).accessToken);
    await expect(plain.directory.createScimToken({ name: "x" })).rejects.toSatisfy((e: ConnectError) => e.code === Code.PermissionDenied);
  });
});

describe("discovery", () => {
  it("describes itself", async () => {
    const c = await scim("GET", "/ServiceProviderConfig");
    expect(c.body.patch.supported).toBe(true);
    expect(c.type).toContain("application/scim+json");
    expect((await scim("GET", "/ResourceTypes")).body.Resources.map((r: { id: string }) => r.id)).toEqual(["User", "Group"]);
    expect((await scim("GET", "/Schemas")).body.totalResults).toBe(2);
    expect((await scim("GET", "/Nope")).status).toBe(404);
  });

  it("parses the filters clients send, and refuses others", () => {
    expect(parseFilter('userName eq "a@b.c"')).toEqual({ attr: "username", value: "a@b.c" });
    expect(parseFilter('emails.value eq "x"')).toEqual({ attr: "emails.value", value: "x" });
    expect(parseFilter(null)).toBeUndefined();
    expect(() => parseFilter('userName sw "a"')).toThrow(ScimError);
  });
});

describe("users", () => {
  it("creates, finds by filter, refuses duplicates", async () => {
    const created = await scim("POST", "/Users", newUser("ada@corp.example", { externalId: "okta-1" }));
    expect(created.status).toBe(201);
    expect(created.body).toMatchObject({ userName: "ada@corp.example", externalId: "okta-1", active: true, displayName: "Ada Lovelace" });
    const id = created.body.id as string;

    const local = (await admin.admin.listUsers({ query: "ada@corp.example" })).users[0]!;
    expect(local).toMatchObject({ id, source: UserSource.SCIM, role: User_Role.USER });

    expect((await scim("GET", '/Users?filter=userName eq "ada@corp.example"')).body.Resources[0].id).toBe(id);
    expect((await scim("GET", '/Users?filter=externalId eq "okta-1"')).body.totalResults).toBe(1);
    expect((await scim("GET", '/Users?filter=emails.value eq "ada@corp.example"')).body.totalResults).toBe(1);
    expect((await scim("GET", '/Users?filter=userName eq "nobody"')).body.totalResults).toBe(0);
    expect((await scim("GET", `/Users/${id}`)).body.id).toBe(id);
    expect((await scim("GET", "/Users/usr_missing")).status).toBe(404);

    const dup = await scim("POST", "/Users", newUser("ada@corp.example"));
    expect(dup.status).toBe(409);
    expect(dup.body.scimType).toBe("uniqueness");
  });

  it("needs an address and paginates", async () => {
    expect((await scim("POST", "/Users", { userName: "noaddress" })).status).toBe(400);
    for (const n of ["p1", "p2", "p3"]) await scim("POST", "/Users", newUser(n));
    const page = await scim("GET", "/Users?startIndex=1&count=2");
    expect(page.body.itemsPerPage).toBe(2);
    expect(page.body.totalResults).toBeGreaterThanOrEqual(3);
    const next = await scim("GET", "/Users?startIndex=3&count=2");
    expect(next.body.startIndex).toBe(3);
  });

  it("adopts an account made before provisioning got to it", async () => {
    const made = await admin.admin.createUser({ name: "Early", email: "early@corp.example", password: "early-password-1", role: User_Role.USER });
    const r = await scim("POST", "/Users", newUser("early@corp.example"));
    expect(r.status).toBe(201);
    expect(r.body.id).toBe(made.user!.id);
  });

  it("deactivation signs out everywhere, reactivation lets back in", async () => {
    const r = await scim("POST", "/Users", newUser("dee@corp.example"));
    const id = r.body.id as string;
    await admin.admin.resetPassword({ userId: id, password: "dee-password-123" });
    const device = await deviceLogin(ts, "dee@corp.example", "dee-password-123");

    // Azure AD's dialect: no path, "False" as a string.
    const off = await scim("PATCH", `/Users/${id}`, patch([{ op: "Replace", value: { active: "False" } }]));
    expect(off.status).toBe(200);
    expect(off.body.active).toBe(false);
    await expect(ts.as(device.accessToken).auth.getMe({})).rejects.toSatisfy((e: ConnectError) => e.code === Code.Unauthenticated);
    await expect(signIn(ts.url, "dee@corp.example", "dee-password-123")).rejects.toThrow();

    // Okta's dialect: path and a real boolean.
    const on = await scim("PATCH", `/Users/${id}`, patch([{ op: "replace", path: "active", value: true }]));
    expect(on.body.active).toBe(true);
    await signIn(ts.url, "dee@corp.example", "dee-password-123");
  });

  it("patches and replaces attributes, ignoring ones it has no place for", async () => {
    const id = (await scim("POST", "/Users", newUser("rep@corp.example"))).body.id as string;
    const p = await scim("PATCH", `/Users/${id}`, patch([
      { op: "replace", path: "displayName", value: "New Name" },
      { op: "add", path: "title", value: "Engineer" },
      { op: "replace", path: 'emails[type eq "work"].value', value: "new@corp.example" },
    ]));
    expect(p.status).toBe(200);
    expect(p.body).toMatchObject({ displayName: "New Name", emails: [{ value: "new@corp.example" }] });
    const put = await scim("PUT", `/Users/${id}`, newUser("rep2@corp.example", { displayName: "Replaced", emails: [{ value: "rep2@corp.example", primary: true }] }));
    expect(put.body).toMatchObject({ userName: "rep2@corp.example", displayName: "Replaced" });
    expect((await scim("PATCH", `/Users/${id}`, patch([{ op: "remove", path: "x" }]))).status).toBe(400);
  });

  it("deletes, and keeps the last admin", async () => {
    const id = (await scim("POST", "/Users", newUser("del@corp.example"))).body.id as string;
    expect((await scim("DELETE", `/Users/${id}`)).status).toBe(204);
    expect((await scim("GET", `/Users/${id}`)).status).toBe(404);
    const adminRow = (await admin.admin.listUsers({})).users.find((u) => u.role === User_Role.ADMIN)!;
    expect((await scim("DELETE", `/Users/${adminRow.id}`)).status).toBe(409);
    expect((await scim("PATCH", `/Users/${adminRow.id}`, patch([{ op: "replace", path: "active", value: false }]))).status).toBe(409);
  });
});

describe("groups", () => {
  it("creates with members, patches membership in both dialects, deletes", async () => {
    const a = (await scim("POST", "/Users", newUser("g-a@corp.example"))).body.id as string;
    const b = (await scim("POST", "/Users", newUser("g-b@corp.example"))).body.id as string;

    const g = await scim("POST", "/Groups", { schemas: ["urn:ietf:params:scim:schemas:core:2.0:Group"], displayName: "Engineers", externalId: "x1", members: [{ value: a }] });
    expect(g.status).toBe(201);
    const id = g.body.id as string;
    expect(g.body.members.map((m: { value: string }) => m.value)).toEqual([a]);
    expect((await scim("POST", "/Groups", { displayName: "Engineers" })).status).toBe(409);

    await scim("PATCH", `/Groups/${id}`, patch([{ op: "add", path: "members", value: [{ value: b }] }]));
    expect((await scim("GET", `/Groups/${id}`)).body.members).toHaveLength(2);
    // Okta and Azure AD remove by filtered path.
    await scim("PATCH", `/Groups/${id}`, patch([{ op: "remove", path: `members[value eq "${a}"]` }]));
    expect((await scim("GET", `/Groups/${id}`)).body.members.map((m: { value: string }) => m.value)).toEqual([b]);
    await scim("PATCH", `/Groups/${id}`, patch([{ op: "remove", path: "members", value: [{ value: b }] }]));
    expect((await scim("GET", `/Groups/${id}`)).body.members).toHaveLength(0);
    await scim("PATCH", `/Groups/${id}`, patch([{ op: "replace", path: "displayName", value: "Eng" }]));
    expect((await scim("GET", '/Groups?filter=displayName eq "Eng"')).body.totalResults).toBe(1);
    await scim("PUT", `/Groups/${id}`, { displayName: "Eng2", members: [{ value: a }, { value: b }] });
    expect((await scim("GET", `/Groups/${id}`)).body).toMatchObject({ displayName: "Eng2" });

    // They are groups like any other to the rest of the server, but not editable there.
    const listed = (await admin.directory.listGroups({})).groups.find((x) => x.id === id)!;
    expect(listed.source).toBe("scim");
    await expect(admin.directory.deleteGroup({ id })).rejects.toSatisfy((e: ConnectError) => e.code === Code.FailedPrecondition);
    await expect(admin.directory.setGroupMembers({ id, userIds: [] })).rejects.toSatisfy((e: ConnectError) => e.code === Code.FailedPrecondition);

    expect((await scim("DELETE", `/Groups/${id}`)).status).toBe(204);
    expect((await scim("GET", `/Groups/${id}`)).status).toBe(404);
  });

  it("does not expose groups of other sources", async () => {
    const local = (await admin.directory.createGroup({ name: "local-only" })).group!;
    expect((await scim("GET", `/Groups/${local.id}`)).status).toBe(404);
  });

  it("applies the role mapping as membership changes", async () => {
    await admin.directory.setScimSettings({ settings: { roleMappings: [{ group: "Auditors", role: User_Role.AUDITOR }, { group: "Finance", role: User_Role.BILLING }] } });
    const u = (await scim("POST", "/Users", newUser("mapped@corp.example"))).body.id as string;
    const role = async () => (await admin.admin.listUsers({ query: "mapped@corp.example" })).users[0]!.role;

    const g = (await scim("POST", "/Groups", { displayName: "Finance", members: [{ value: u }] })).body.id as string;
    expect(await role()).toBe(User_Role.BILLING);
    const a = (await scim("POST", "/Groups", { displayName: "Auditors" })).body.id as string;
    await scim("PATCH", `/Groups/${a}`, patch([{ op: "add", path: "members", value: [{ value: u }] }]));
    expect(await role()).toBe(User_Role.AUDITOR);
    await scim("DELETE", `/Groups/${a}`);
    expect(await role()).toBe(User_Role.BILLING);
    await scim("PATCH", `/Groups/${g}`, patch([{ op: "remove", path: `members[value eq "${u}"]` }]));
    expect(await role()).toBe(User_Role.USER);
    await admin.directory.setScimSettings({ settings: { roleMappings: [] } });
  });
});

describe("audit", () => {
  it("records provisioning under the token's name and leaves no token in the log", async () => {
    const id = (await scim("POST", "/Users", newUser("audited@corp.example"))).body.id as string;
    const { events } = await listAudit({ action: "scim.user_create", target: id });
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ actor: "scim:okta", target: "audited@corp.example" });
    const all = await listAudit({}, 500);
    expect(JSON.stringify(all)).not.toContain(token);
  });
});
