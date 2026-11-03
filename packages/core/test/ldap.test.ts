/**
 * LDAP sign-in against an in-process fake directory (test/helpers/fake-ldap.ts): filter escaping, bind
 * success and failure, group restriction and admin mapping, auto-provisioning (and pre-provisioning when it
 * is off), local fallback / break-glass, the failed-attempt limit, the settings (bind password write-only and
 * encrypted at rest) and the test actions. Disabled LDAP leaves sign-in as it was.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { and, eq } from "drizzle-orm";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { LdapLocalLogin, UserSource, type LdapConfig } from "../src/gen/harness/auth/v1/admin_pb.ts";
import { User_Role } from "../src/gen/harness/auth/v1/auth_pb.ts";
import { escapeFilterValue, userFilter } from "../src/auth/ldap.ts";
import { FakeLdap } from "./helpers/fake-ldap.ts";
import { ADMIN, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const BASE = "ou=people,dc=example,dc=org";
const GROUPS = "ou=groups,dc=example,dc=org";
const SERVICE = { dn: "cn=svc,dc=example,dc=org", password: "svc-password-not-real" };

let ts: TestServer;
let dir: FakeLdap;
let admin: ReturnType<TestServer["as"]>;

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
}

function person(uid: string, extra: Record<string, string | string[]> = {}) {
  return {
    dn: `uid=${uid},${BASE}`,
    password: `${uid}-ldap-password`,
    attributes: { objectClass: "inetOrgPerson", uid, mail: `${uid}@example.org`, cn: `${uid[0]!.toUpperCase()}${uid.slice(1)} Example`, ...extra },
  };
}

type ConfigInit = Omit<Partial<LdapConfig>, "$typeName">;
const config = (over: ConfigInit = {}): ConfigInit => ({
  enabled: true,
  urls: [dir.url],
  bindDn: SERVICE.dn,
  bindPassword: SERVICE.password,
  userBaseDn: BASE,
  userFilter: "(|(uid={{username}})(mail={{username}}))",
  autoCreateUsers: true,
  localLogin: LdapLocalLogin.FALLBACK,
  timeoutMs: 2000,
  ...over,
});

const ldapSignIn = (login: string, password: string) =>
  fetch(`${ts.url}/api/auth/sign-in/ldap`, {
    method: "POST",
    headers: { "content-type": "application/json", origin: ts.url },
    body: JSON.stringify({ login, password }),
  });

const localSignIn = (email: string, password: string) =>
  fetch(`${ts.url}/api/auth/sign-in/email`, {
    method: "POST",
    headers: { "content-type": "application/json", origin: ts.url },
    body: JSON.stringify({ email, password }),
  });

async function userByEmail(email: string) {
  return (await admin.admin.listUsers({ query: email })).users.find((u) => u.email === email);
}

beforeAll(async () => {
  dir = await new FakeLdap().start();
  dir.add(
    { dn: SERVICE.dn, password: SERVICE.password, attributes: { objectClass: "person", cn: "svc" } },
    { dn: BASE, attributes: { objectClass: "organizationalUnit", ou: "people" } },
    person("alice", { memberOf: [`cn=staff,${GROUPS}`, `cn=admins,${GROUPS}`] }),
    person("bob", { memberOf: [`cn=staff,${GROUPS}`] }),
    person("eve"),
    // No memberOf: membership only in the group entry (member / memberUid).
    person("carol"),
    { dn: `cn=staff,${GROUPS}`, attributes: { objectClass: "groupOfNames", cn: "staff", member: [`uid=alice,${BASE}`, `uid=bob,${BASE}`, `uid=carol,${BASE}`] } },
    { dn: `cn=admins,${GROUPS}`, attributes: { objectClass: "posixGroup", cn: "admins", memberUid: ["alice", "carol"] } },
    // Collides with a local account created below.
    person("local", { mail: "local@test.local" }),
    person("pre"),
  );
  ts = await startTestServer({ HARNESS_LOGIN_FAILURES_PER_15MIN: "3" });
  admin = ts.as((await deviceLogin(ts)).accessToken);
});

afterAll(async () => {
  await ts.close();
  await dir.stop();
});

beforeEach(async () => {
  const { resetLoginFailures } = await import("../src/auth/ldap-sign-in.ts");
  resetLoginFailures();
});

describe("filter escaping", () => {
  it("escapes the RFC 4515 special characters", () => {
    expect(escapeFilterValue("a*b(c)d\\e\0f")).toBe("a\\2ab\\28c\\29d\\5ce\\00f");
    expect(userFilter("(uid={{username}})", "*)(uid=*")).toBe("(uid=\\2a\\29\\28uid=\\2a)");
    expect(escapeFilterValue("plain.user@example.org")).toBe("plain.user@example.org");
  });
});

describe("disabled LDAP", () => {
  it("keeps the local sign-in form and refuses the LDAP endpoint", async () => {
    expect((await ldapSignIn("alice", "alice-ldap-password")).status).toBe(404);
    const html = await (await fetch(`${ts.url}/device`)).text();
    expect(html).toContain("const r = false ?");
    expect((await localSignIn(ADMIN.email, ADMIN.password)).ok).toBe(true);
    expect(dir.binds).toEqual([]);
  });
});

describe("settings", () => {
  it("validates, keeps the bind password write-only and stores it encrypted", async () => {
    expect(await code(admin.admin.setLdapConfig({ config: config({ urls: ["http://example.org"] }) }))).toBe(Code.InvalidArgument);
    expect(await code(admin.admin.setLdapConfig({ config: config({ userFilter: "(uid=x)" }) }))).toBe(Code.InvalidArgument);
    expect(await code(admin.admin.setLdapConfig({ config: config({ userBaseDn: "" }) }))).toBe(Code.InvalidArgument);
    expect(await code(admin.admin.setLdapConfig({ config: config({ localLogin: LdapLocalLogin.BREAK_GLASS }) }))).toBe(Code.InvalidArgument);
    // Disabled settings may be incomplete while being set up.
    await admin.admin.setLdapConfig({ config: { enabled: false, urls: [dir.url] } });

    const saved = (await admin.admin.setLdapConfig({ config: config({ enabled: false }) })).config!;
    expect(saved).toMatchObject({ enabled: false, bindDn: SERVICE.dn, bindPassword: "", bindPasswordSet: true, usernameAttribute: "uid", nameAttribute: "cn" });
    expect(JSON.stringify(await admin.admin.getLdapConfig({}))).not.toContain(SERVICE.password);

    const { getDb, schema } = await import("../src/db/index.ts");
    const row = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, "ldap") });
    expect(JSON.stringify(row!.value)).not.toContain(SERVICE.password);

    // An empty password keeps the stored one.
    expect((await admin.admin.setLdapConfig({ config: config({ enabled: false, bindPassword: "" }) })).config!.bindPasswordSet).toBe(true);
  });

  it("tests the connection and a user lookup, including unsaved changes", async () => {
    const ok = await admin.admin.testLdapConnection({ config: config() });
    expect(ok).toMatchObject({ ok: true, url: dir.url });
    const bad = await admin.admin.testLdapConnection({ config: config({ bindPassword: "wrong-password" }) });
    expect(bad.ok).toBe(false);
    expect(bad.message).not.toContain("wrong-password");
    expect((await admin.admin.testLdapConnection({ config: config({ urls: ["ldap://127.0.0.1:1"] }) })).ok).toBe(false);

    const u = await admin.admin.testLdapUser({ config: config({ adminGroupDn: `cn=admins,${GROUPS}`, allowedGroupDn: `cn=staff,${GROUPS}` }), login: "carol" });
    expect(u).toMatchObject({ found: true, dn: `uid=carol,${BASE}`, username: "carol", email: "carol@example.org", name: "Carol Example", allowed: true, admin: true, userId: "" });
    const eve = await admin.admin.testLdapUser({ config: config({ allowedGroupDn: `cn=staff,${GROUPS}` }), login: "eve@example.org" });
    expect(eve).toMatchObject({ found: true, allowed: false, admin: false });
    expect((await admin.admin.testLdapUser({ config: config(), login: "*" })).found).toBe(false);
  });
});

describe("sign-in", () => {
  beforeAll(async () => {
    await admin.admin.setLdapConfig({ config: config({ allowedGroupDn: `cn=staff,${GROUPS}`, adminGroupDn: `cn=admins,${GROUPS}` }) });
  });

  it("switches the device page to the LDAP endpoint", async () => {
    const html = await (await fetch(`${ts.url}/device`)).text();
    expect(html).toContain("const r = true ?");
    expect(html).toContain("/api/auth/sign-in/ldap");
    expect(html).toContain('<label>Email or username<input name="login"');
  });

  it("binds, creates the account (LDAP source, no local password, admin by group) and issues a session", async () => {
    const res = await ldapSignIn("alice", "alice-ldap-password");
    expect(res.status).toBe(200);
    const cookie = res.headers.getSetCookie().map((c) => c.split(";")[0]).join("; ");
    expect(cookie).toContain("session_token");
    expect(await (await fetch(`${ts.url}/device`, { headers: { cookie } })).text()).toContain("Connect a device");
    expect(dir.binds).toContain(`uid=alice,${BASE}`);

    const u = (await userByEmail("alice@example.org"))!;
    expect(u).toMatchObject({ username: "alice", name: "Alice Example", source: UserSource.LDAP, role: User_Role.ADMIN });
    const { getDb, schema } = await import("../src/db/index.ts");
    const credential = await getDb().query.account.findFirst({ where: and(eq(schema.account.userId, u.id), eq(schema.account.providerId, "credential")) });
    expect(credential).toBeUndefined();
    // Their password lives in the directory.
    expect(await code(admin.admin.resetPassword({ userId: u.id, generate: true }))).toBe(Code.FailedPrecondition);

    // Signing in again updates the same account; the device flow works as for local users.
    expect((await ldapSignIn("alice@example.org", "alice-ldap-password")).ok).toBe(true);
    expect((await admin.admin.listUsers({ query: "alice" })).users.length).toBe(1);
  });

  it("maps membership from the group entry when the directory has no memberOf", async () => {
    expect((await ldapSignIn("carol", "carol-ldap-password")).ok).toBe(true);
    expect((await userByEmail("carol@example.org"))!.role).toBe(User_Role.ADMIN);
    expect((await ldapSignIn("bob", "bob-ldap-password")).ok).toBe(true);
    expect((await userByEmail("bob@example.org"))!.role).toBe(User_Role.USER);
  });

  it("refuses a wrong or empty password, unknown users and injected filters", async () => {
    expect((await ldapSignIn("bob", "wrong-password")).status).toBe(401);
    const binds = dir.binds.length;
    expect((await ldapSignIn("bob", "")).status).toBe(401);
    expect(dir.binds.length).toBe(binds);
    expect((await ldapSignIn("nobody", "whatever-password")).status).toBe(401);

    const res = await ldapSignIn("*)(uid=*", "whatever-password");
    expect(res.status).toBe(401);
    expect(dir.filters).toContain("(|(uid=\\2a\\29\\28uid=\\2a)(mail=\\2a\\29\\28uid=\\2a))");
    expect((await ldapSignIn("*", "alice-ldap-password")).status).toBe(401);
  });

  it("refuses users outside the allowed group", async () => {
    const res = await ldapSignIn("eve", "eve-ldap-password");
    expect(res.status).toBe(403);
    expect(await userByEmail("eve@example.org")).toBeUndefined();
  });

  it("never takes over an existing local account", async () => {
    await admin.admin.createUser({ name: "Local", email: "local@test.local", password: "local-password-1" });
    await admin.admin.setLdapConfig({ config: config({ allowedGroupDn: "", adminGroupDn: "" }) });
    const res = await ldapSignIn("local", "local-ldap-password");
    expect(res.status).toBe(403);
    expect((await userByEmail("local@test.local"))!.source).toBe(UserSource.LOCAL);
  });

  it("falls back to local accounts when the directory refuses", async () => {
    expect((await ldapSignIn(ADMIN.email, ADMIN.password)).ok).toBe(true);
    expect((await ldapSignIn("local@test.local", "local-password-1")).ok).toBe(true);
    expect((await localSignIn("local@test.local", "local-password-1")).ok).toBe(true);
  });

  it("with auto-creation off, only signs in pre-provisioned directory users", async () => {
    await admin.admin.setLdapConfig({ config: config({ autoCreateUsers: false, allowedGroupDn: "", adminGroupDn: "" }) });
    expect((await ldapSignIn("pre", "pre-ldap-password")).status).toBe(403);
    expect(await userByEmail("pre@example.org")).toBeUndefined();

    const created = await admin.admin.createUser({ name: "Pre", username: "pre", email: "pre@example.org", ldap: true });
    expect(created.user).toMatchObject({ source: UserSource.LDAP });
    expect(created.generatedPassword).toBe("");
    expect((await ldapSignIn("pre", "pre-ldap-password")).ok).toBe(true);
  });

  it("break-glass: local sign-in only for listed admins, even while the directory is down", async () => {
    await admin.admin.setLdapConfig({ config: config({ localLogin: LdapLocalLogin.BREAK_GLASS, breakGlassLogins: [ADMIN.email.toUpperCase()] }) });
    expect((await localSignIn("local@test.local", "local-password-1")).status).toBe(403);
    expect((await ldapSignIn("local@test.local", "local-password-1")).status).toBe(401);
    expect((await localSignIn(ADMIN.email, ADMIN.password)).ok).toBe(true);
    expect((await ldapSignIn("bob", "bob-ldap-password")).ok).toBe(true);

    await admin.admin.setLdapConfig({ config: config({ urls: ["ldap://127.0.0.1:1"], localLogin: LdapLocalLogin.BREAK_GLASS, breakGlassLogins: [ADMIN.email] }) });
    expect((await ldapSignIn(ADMIN.email, ADMIN.password)).ok).toBe(true);
    expect((await ldapSignIn("bob", "bob-ldap-password")).status).toBe(503);
    await admin.admin.setLdapConfig({ config: config() });
  });

  it("limits failed attempts per login", async () => {
    for (let i = 0; i < 3; i++) expect((await ldapSignIn("bob", `wrong-${i}`)).status).toBe(401);
    const blocked = await ldapSignIn("bob", "bob-ldap-password");
    expect(blocked.status).toBe(429);
    expect(Number(blocked.headers.get("retry-after"))).toBeGreaterThan(0);
    // Other logins are unaffected.
    expect((await ldapSignIn("alice", "alice-ldap-password")).ok).toBe(true);
  });

  it("refuses disabled LDAP users", async () => {
    const bob = (await userByEmail("bob@example.org"))!;
    await admin.admin.setDisabled({ userId: bob.id, disabled: true });
    expect((await ldapSignIn("bob", "bob-ldap-password")).status).toBe(403);
  });
});
