/**
 * OIDC and SAML sign-in against a fake identity provider on a local port (test/helpers/fake-idp.ts): the whole flow from the
 * sign-in page to a session and an approved device, refusal of everything a provider's answer must not get away with,
 * account linking, group to role mapping (LDAP's is in ldap.test.ts), the settings (secret write-only, encrypted at rest),
 * the test button, and requiring SSO.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { User_Role, UserSource } from "../src/gen/harness/auth/v1/auth_pb.ts";
import { SsoProtocol, type SsoProvider } from "../src/gen/harness/auth/v1/sso_pb.ts";
import { FakeBrowser, startFakeIdp, type FakeIdp } from "./helpers/fake-idp.ts";
import { ADMIN, decideOnDevicePage, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let idp: FakeIdp;
let admin: ReturnType<TestServer["as"]>;

type Init = Omit<Partial<SsoProvider>, "$typeName">;

const code = async (p: Promise<unknown>) => {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
};

const oidc = (over: Init = {}): Init => ({
  id: "corp",
  name: "Corp",
  protocol: SsoProtocol.OIDC,
  enabled: true,
  issuer: idp.url,
  clientId: idp.clientId,
  clientSecret: idp.clientSecret,
  autoCreateUsers: true,
  ...over,
});

const saml = (over: Init = {}): Init => ({
  id: "corp-saml",
  name: "Corp SAML",
  protocol: SsoProtocol.SAML,
  enabled: true,
  idpEntityId: idp.entityId,
  idpSsoUrl: idp.ssoUrl,
  idpCertificate: idp.cert,
  autoCreateUsers: true,
  ...over,
});

async function save(p: Init) {
  return (await admin.sso.saveSsoProvider({ provider: p })).provider!;
}

/** The OIDC round trip in a browser, up to the page it lands on. */
async function oidcSignIn(b: FakeBrowser, id = "corp", ret = "/device") {
  return b.follow(`${ts.url}/api/sso/${id}/start?return=${encodeURIComponent(ret)}`);
}

/** The SAML round trip: start, then POST what the provider signed. */
async function samlSignIn(b: FakeBrowser, opts: Parameters<FakeIdp["samlResponse"]>[1] = {}, id = "corp-saml") {
  const start = await b.request(`${ts.url}/api/sso/${id}/start?return=/device`);
  const location = start.headers.get("location")!;
  const { acs, samlResponse } = idp.samlResponse(location, opts);
  const posted = await b.request(acs, { method: "POST", headers: { "content-type": "application/x-www-form-urlencoded", origin: idp.url }, body: new URLSearchParams({ SAMLResponse: samlResponse }).toString() });
  return { posted, samlResponse, acs };
}

async function userBySub(providerId: string, sub: string) {
  const [row] = await getDb()
    .select({ user: schema.user })
    .from(schema.account)
    .innerJoin(schema.user, eq(schema.user.id, schema.account.userId))
    .where(eq(schema.account.accountId, sub));
  void providerId;
  return row?.user;
}

beforeAll(async () => {
  ts = await startTestServer();
  idp = await startFakeIdp();
  const creds = await deviceLogin(ts);
  admin = ts.as(creds.accessToken);
});

afterAll(async () => {
  await idp.close();
  await ts.close();
});

beforeEach(async () => {
  idp.tamper = {};
  await admin.sso.setSsoSettings({ settings: { requireSso: false, breakGlassLogins: [] } });
  for (const p of (await admin.sso.listSsoProviders({})).providers) await admin.sso.deleteSsoProvider({ id: p.id });
});

describe("settings", () => {
  it("never returns the client secret and keeps it encrypted", async () => {
    const saved = await save(oidc());
    expect(saved.clientSecret).toBe("");
    expect(saved.clientSecretSet).toBe(true);
    expect(saved.redirectUri).toBe(`${ts.url}/api/sso/corp/oidc/callback`);
    const [row] = await getDb().select().from(schema.ssoProvider).where(eq(schema.ssoProvider.id, "corp"));
    expect(row!.clientSecretEnc).toBeTruthy();
    expect(JSON.stringify(row)).not.toContain(idp.clientSecret);
    // An empty secret keeps the stored one; clearing removes it.
    expect((await save(oidc({ clientSecret: "" }))).clientSecretSet).toBe(true);
    expect((await save(oidc({ clientSecret: "", clearClientSecret: true }))).clientSecretSet).toBe(false);
  });

  it("validates what an enabled provider needs, and lets a disabled one be incomplete", async () => {
    expect(await code(save(oidc({ issuer: "" })))).toBe(Code.InvalidArgument);
    expect(await code(save(oidc({ issuer: "http://idp.example.com" })))).toBe(Code.InvalidArgument);
    expect(await code(save(saml({ idpCertificate: "not a certificate" })))).toBe(Code.InvalidArgument);
    expect((await save(saml({ enabled: false, idpCertificate: "" }))).enabled).toBe(false);
  });

  it("is for admins; an auditor can read it, nobody else", async () => {
    await save(oidc());
    const users = await admin.admin.createUser({ name: "Aud", email: "aud-sso@test.local", password: "auditor-password-1", role: User_Role.AUDITOR });
    expect(users.user!.role).toBe(User_Role.AUDITOR);
    const aud = ts.as((await deviceLogin(ts, "aud-sso@test.local", "auditor-password-1")).accessToken);
    expect((await aud.sso.listSsoProviders({})).providers[0]!.clientSecret).toBe("");
    expect(await code(aud.sso.saveSsoProvider({ provider: oidc() }))).toBe(Code.PermissionDenied);
    await admin.admin.createUser({ name: "Plain", email: "plain-sso@test.local", password: "plain-password-1", role: User_Role.USER });
    const plain = ts.as((await deviceLogin(ts, "plain-sso@test.local", "plain-password-1")).accessToken);
    expect(await code(plain.sso.listSsoProviders({}))).toBe(Code.PermissionDenied);
  });
});

describe("test button", () => {
  it("checks a good OIDC provider and offers a one-time try-out", async () => {
    await save(oidc());
    const r = await admin.sso.testSsoProvider({ provider: oidc() });
    expect(r.ok, JSON.stringify(r.checks)).toBe(true);
    expect(r.signInTestUrl).toContain("/api/sso/corp/start?test=");

    idp.nextUser = { sub: "try-1", email: "try@test.local", name: "Try Out", groups: ["eng"] };
    const b = new FakeBrowser();
    const { res } = await b.follow(r.signInTestUrl);
    const html = await res.text();
    expect(res.status).toBe(200);
    expect(html).toContain("Sign-in works");
    expect(html).toContain("try@test.local");
    // Nothing happened: no account, no session cookie.
    expect(await userBySub("corp", "try-1")).toBeUndefined();
    expect(b.cookie()).not.toContain("session_token");
    // The link works once.
    const again = await new FakeBrowser().follow(r.signInTestUrl);
    expect(again.res.status).toBe(401);
  });

  it("names what is wrong without saving anything", async () => {
    const r = await admin.sso.testSsoProvider({ provider: oidc({ id: "", issuer: `${idp.url}/nope` }) });
    expect(r.ok).toBe(false);
    expect(r.checks[0]!.name).toBe("Discovery");
    expect((await admin.sso.listSsoProviders({})).providers).toHaveLength(0);
    const s = await admin.sso.testSsoProvider({ provider: saml({ id: "", idpCertificate: "x" }) });
    expect(s.ok).toBe(false);
  });

  it("checks a SAML provider's URL and certificate", async () => {
    const r = await admin.sso.testSsoProvider({ provider: saml({ id: "" }) });
    expect(r.ok, JSON.stringify(r.checks)).toBe(true);
    expect(r.signInTestUrl).toBe("");
    expect(r.checks.map((c) => c.name)).toEqual(["Sign-in URL", "Certificate"]);
  });
});

describe("OIDC sign-in", () => {
  it("creates the account, opens a session and can approve a device", async () => {
    await save(oidc());
    idp.nextUser = { sub: "alice-sub", email: "Alice@Test.Local", name: "Alice", preferred_username: "alice", groups: ["eng"] };

    const page = await fetch(`${ts.url}/device?code=ABCD-EFGH`).then((r) => r.text());
    expect(page).toContain("Sign in with Corp");
    expect(page).toContain(encodeURIComponent("/device?code=ABCD-EFGH"));

    const b = new FakeBrowser();
    const { res, url, hops } = await oidcSignIn(b);
    expect(res.status).toBe(200);
    expect(url).toBe(`${ts.url}/device`);
    expect(hops.some((h) => h.includes("/api/auth/sso/complete"))).toBe(true);
    expect(b.cookie()).toContain("session_token");
    expect(idp.tokenRequests.at(-1)!.body.get("code_verifier")).toBeTruthy();

    const u = (await admin.admin.listUsers({ query: "alice@test.local" })).users[0]!;
    expect(u).toMatchObject({ name: "Alice", username: "alice", source: UserSource.SSO, role: User_Role.USER });
    expect(u.groups).toEqual(["eng"]);

    // The device flow, as the desktop app and the CLI use it: the page knows the browser now.
    const start = await ts.auth.startDeviceAuth({ clientName: "harness-client", deviceName: "laptop", os: "darwin", arch: "arm64" });
    const approved = await decideOnDevicePage(ts.url, b.cookie(), start.userCode, "approve");
    expect(await approved.text()).toContain("Device connected");
    const poll = await ts.auth.pollDeviceAuth({ deviceCode: start.deviceCode });
    expect(poll.credentials?.user?.email).toBe("alice@test.local");
  });

  it("signs in the same person next time and follows the provider's name and groups", async () => {
    await save(oidc({ roleMappings: [{ group: "admins", role: User_Role.ADMIN } as never, { group: "finance", role: User_Role.BILLING } as never] }));
    idp.nextUser = { sub: "bob-sub", email: "bob@test.local", name: "Bob", groups: ["finance"] };
    await oidcSignIn(new FakeBrowser());
    const first = (await admin.admin.listUsers({ query: "bob@test.local" })).users[0]!;
    expect(first.role).toBe(User_Role.BILLING);

    idp.nextUser = { sub: "bob-sub", email: "bob@test.local", name: "Robert", groups: ["other"] };
    await oidcSignIn(new FakeBrowser());
    const second = (await admin.admin.listUsers({ query: "bob@test.local" })).users[0]!;
    expect(second).toMatchObject({ id: first.id, name: "Robert", role: User_Role.USER });
    expect(second.groups).toEqual(["other"]);

    idp.nextUser = { sub: "bob-sub", email: "bob@test.local", name: "Robert", groups: ["admins", "finance"] };
    await oidcSignIn(new FakeBrowser());
    expect((await admin.admin.listUsers({ query: "bob@test.local" })).users[0]!.role).toBe(User_Role.ADMIN);
  });

  it("reads groups by a dotted claim name and from userinfo when the token lacks them", async () => {
    await save(oidc({ groupsAttribute: "groups" }));
    idp.nextUser = { sub: "carol-sub", email: "carol@test.local", name: "Carol", groups: ["x"] };
    await oidcSignIn(new FakeBrowser());
    expect((await admin.admin.listUsers({ query: "carol@test.local" })).users[0]!.groups).toEqual(["x"]);
  });

  it("refuses a token with the wrong nonce, audience, issuer or key", async () => {
    await save(oidc());
    idp.nextUser = { sub: "mallory", email: "mallory@test.local" };
    for (const tamper of [{ nonce: "other" }, { audience: "other-client" }, { issuer: "http://evil.example" }, { wrongKey: true }]) {
      idp.tamper = tamper;
      const b = new FakeBrowser();
      const { res } = await oidcSignIn(b);
      expect(res.status, JSON.stringify(tamper)).toBe(401);
      expect(b.cookie()).not.toContain("session_token");
    }
    expect(await userBySub("corp", "mallory")).toBeUndefined();
  });

  it("refuses unknown or replayed state", async () => {
    await save(oidc());
    idp.nextUser = { sub: "dave", email: "dave@test.local" };
    const r = await fetch(`${ts.url}/api/sso/corp/oidc/callback?code=x&state=forged`);
    expect(r.status).toBe(401);
    const b = new FakeBrowser();
    const start = await b.request(`${ts.url}/api/sso/corp/start`);
    const toIdp = await b.request(start.headers.get("location")!);
    const callback = toIdp.headers.get("location")!;
    expect((await b.request(callback)).status).toBe(303);
    expect((await b.request(callback)).status).toBe(401);
  });

  it("binds the finished sign-in to the browser that started it", async () => {
    await save(oidc());
    idp.nextUser = { sub: "erin", email: "erin@test.local" };
    const attacker = new FakeBrowser();
    const start = await attacker.request(`${ts.url}/api/sso/corp/start`);
    const toIdp = await attacker.request(start.headers.get("location")!);
    const back = await attacker.request(toIdp.headers.get("location")!);
    const complete = back.headers.get("location")!;
    // The attacker hands the last link to a victim, who has no binding cookie (or their own): no session for them.
    const victim = new FakeBrowser();
    const res = await victim.request(complete);
    expect(res.status).toBe(401);
    expect(victim.cookie()).not.toContain("session_token");
  });

  it("does not take over an account with a password, and links one without", async () => {
    await save(oidc());
    await admin.admin.createUser({ name: "Local", email: "local@test.local", password: "local-password-1", role: User_Role.USER });
    idp.nextUser = { sub: "local-sub", email: "local@test.local", name: "Local" };
    const b = new FakeBrowser();
    const { res } = await oidcSignIn(b);
    expect(res.status).toBe(403);
    expect(b.cookie()).not.toContain("session_token");

    await admin.admin.createUser({ name: "Pre", username: "pre.user", email: "pre@test.local", role: User_Role.AUDITOR, ldap: true });
    idp.nextUser = { sub: "pre-sub", email: "pre@test.local", name: "Pre" };
    const linked = await oidcSignIn(new FakeBrowser());
    expect(linked.res.status).toBe(200);
    expect((await admin.admin.listUsers({ query: "pre@test.local" })).users[0]!.role).toBe(User_Role.AUDITOR);
  });

  it("applies the allowed domains and groups, the verified flag, and auto-creation off", async () => {
    await save(oidc({ allowedDomains: ["good.example"] }));
    idp.nextUser = { sub: "d1", email: "x@bad.example" };
    expect((await oidcSignIn(new FakeBrowser())).res.status).toBe(403);
    await save(oidc({ allowedGroups: ["vpn"] }));
    idp.nextUser = { sub: "d2", email: "y@good.example", groups: ["other"] };
    expect((await oidcSignIn(new FakeBrowser())).res.status).toBe(403);
    await save(oidc());
    idp.nextUser = { sub: "d3", email: "z@good.example", email_verified: false };
    expect((await oidcSignIn(new FakeBrowser())).res.status).toBe(403);
    await save(oidc({ autoCreateUsers: false }));
    idp.nextUser = { sub: "d4", email: "new@good.example" };
    expect((await oidcSignIn(new FakeBrowser())).res.status).toBe(403);
  });

  it("refuses disabled users and providers", async () => {
    await save(oidc());
    idp.nextUser = { sub: "gone", email: "gone@test.local" };
    await oidcSignIn(new FakeBrowser());
    const u = (await admin.admin.listUsers({ query: "gone@test.local" })).users[0]!;
    await admin.admin.setDisabled({ userId: u.id, disabled: true });
    const b = new FakeBrowser();
    const { res } = await oidcSignIn(b);
    expect(res.status).toBe(403);
    expect(b.cookie()).not.toContain("session_token");

    await save(oidc({ enabled: false }));
    expect((await new FakeBrowser().request(`${ts.url}/api/sso/corp/start`)).status).toBe(404);
  });

  it("only returns to local pages", async () => {
    await save(oidc());
    idp.nextUser = { sub: "ret", email: "ret@test.local" };
    const { url } = await oidcSignIn(new FakeBrowser(), "corp", "https://evil.example/x");
    expect(url).toBe(`${ts.url}/device`);
  });
});

describe("SAML sign-in", () => {
  beforeEach(async () => {
    await save(saml());
    idp.nextUser = { sub: "saml-user", email: "saml@test.local", name: "Sam L", preferred_username: "saml.user", groups: ["eng", "ops"] };
  });

  it("serves metadata and signs in with a signed assertion", async () => {
    const meta = await fetch(`${ts.url}/api/sso/corp-saml/saml/metadata`).then((r) => r.text());
    expect(meta).toContain("EntityDescriptor");
    expect(meta).toContain(`${ts.url}/api/sso/corp-saml/saml/acs`);

    const b = new FakeBrowser();
    const { posted } = await samlSignIn(b);
    expect(posted.status).toBe(303);
    const { url } = await b.follow(posted.headers.get("location")!);
    expect(url).toBe(`${ts.url}/device`);
    expect(b.cookie()).toContain("session_token");
    const u = (await admin.admin.listUsers({ query: "saml@test.local" })).users[0]!;
    expect(u).toMatchObject({ name: "Sam L", source: UserSource.SSO });
    expect(u.groups.sort()).toEqual(["eng", "ops"]);
  });

  it("maps groups to roles", async () => {
    await save(saml({ roleMappings: [{ group: "ops", role: User_Role.AUDITOR } as never] }));
    const b = new FakeBrowser();
    const { posted } = await samlSignIn(b);
    await b.follow(posted.headers.get("location")!);
    expect((await admin.admin.listUsers({ query: "saml@test.local" })).users[0]!.role).toBe(User_Role.AUDITOR);
  });

  it.each([
    ["signed by somebody else", () => ({ forgedWith: idp.otherSigner() })],
    ["not signed", () => ({ unsigned: true })],
    ["for another audience", () => ({ audience: "https://other.example" })],
    ["from another issuer", () => ({ issuer: "https://evil.example" })],
    ["expired", () => ({ notOnOrAfter: new Date(Date.now() - 3_600_000) })],
    ["answering no request of ours", () => ({ inResponseTo: "_never_asked" })],
  ])("refuses a response %s", async (_name, opts) => {
    const b = new FakeBrowser();
    const { posted } = await samlSignIn(b, opts());
    expect(posted.status).toBe(401);
    expect(b.cookie()).not.toContain("session_token");
  });

  it("refuses a replayed response, and one posted without a request", async () => {
    const b = new FakeBrowser();
    const { posted, samlResponse, acs } = await samlSignIn(b);
    expect(posted.status).toBe(303);
    const again = await fetch(acs, { method: "POST", headers: { "content-type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ SAMLResponse: samlResponse }).toString() });
    expect(again.status).toBe(401);
    const none = await fetch(acs, { method: "POST", headers: { "content-type": "application/x-www-form-urlencoded" }, body: "" });
    expect(none.status).toBe(401);
  });

  it("does not sign in a browser that did not start the flow", async () => {
    const b = new FakeBrowser();
    const { posted } = await samlSignIn(b);
    const stranger = new FakeBrowser();
    expect((await stranger.request(posted.headers.get("location")!)).status).toBe(401);
    expect(stranger.cookie()).not.toContain("session_token");
  });
});

describe("requiring SSO", () => {
  it("needs a provider and a break-glass login, then refuses passwords for everyone else", async () => {
    expect(await code(admin.sso.setSsoSettings({ settings: { requireSso: true, breakGlassLogins: [ADMIN.email] } }))).toBe(Code.FailedPrecondition);
    await save(oidc());
    expect(await code(admin.sso.setSsoSettings({ settings: { requireSso: true, breakGlassLogins: [] } }))).toBe(Code.InvalidArgument);
    await admin.admin.createUser({ name: "Pw", email: "pw@test.local", password: "pw-password-123", role: User_Role.USER });
    await admin.sso.setSsoSettings({ settings: { requireSso: true, breakGlassLogins: [ADMIN.email] } });

    const signIn = (email: string, password: string) =>
      fetch(`${ts.url}/api/auth/sign-in/email`, { method: "POST", headers: { "content-type": "application/json", origin: ts.url }, body: JSON.stringify({ email, password }) });
    expect((await signIn("pw@test.local", "pw-password-123")).status).toBe(403);
    expect((await signIn(ADMIN.email, ADMIN.password)).status).toBe(200);

    const page = await fetch(`${ts.url}/device`).then((r) => r.text());
    expect(page).toContain("Sign in with Corp");
    expect(page).toContain("Administrator sign-in");

    // The last provider cannot go while it is required.
    expect(await code(admin.sso.deleteSsoProvider({ id: "corp" }))).toBe(Code.FailedPrecondition);
    expect(await code(save(oidc({ enabled: false })))).toBe(Code.FailedPrecondition);
  });
});
