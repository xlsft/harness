/**
 * Second factor: the TOTP arithmetic against RFC 6238's vectors, enrollment, the pending session on every way of signing in,
 * recovery codes, the admin requirement and what it does to connected devices.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { base32Decode, base32Encode, otpauthUri, stepOf, totpAt, verifyTotp } from "../src/auth/totp.ts";
import { getDb, schema } from "../src/db/index.ts";
import { User_Role } from "../src/gen/harness/auth/v1/auth_pb.ts";
import { ADMIN, decideOnDevicePage, deviceLogin, signIn, startTestServer, type TestServer } from "./helpers/server.ts";

const code = async (p: Promise<unknown>) => {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
};

describe("TOTP", () => {
  // RFC 6238 appendix B, SHA-1 with the ASCII secret, last 6 digits.
  const SECRET = base32Encode(Buffer.from("12345678901234567890"));
  it.each([
    [59, "287082"],
    [1111111109, "081804"],
    [1111111111, "050471"],
    [1234567890, "005924"],
    [2000000000, "279037"],
  ])("matches the RFC vector at %i", (seconds, expected) => {
    expect(totpAt(SECRET, stepOf(seconds * 1000))).toBe(expected);
  });

  it("round-trips base32", () => {
    const b = Buffer.from("hello world, this is a secret");
    expect(base32Decode(base32Encode(b)).equals(b)).toBe(true);
  });

  it("takes the neighbouring steps, refuses others, and refuses a step already used", () => {
    const now = 1_700_000_000_000;
    const s = stepOf(now);
    expect(verifyTotp(SECRET, totpAt(SECRET, s), now, 0)).toBe(s);
    expect(verifyTotp(SECRET, totpAt(SECRET, s - 1), now, 0)).toBe(s - 1);
    expect(verifyTotp(SECRET, totpAt(SECRET, s + 1), now, 0)).toBe(s + 1);
    expect(verifyTotp(SECRET, totpAt(SECRET, s + 2), now, 0)).toBeUndefined();
    expect(verifyTotp(SECRET, totpAt(SECRET, s), now, s)).toBeUndefined();
    expect(verifyTotp(SECRET, "12345", now, 0)).toBeUndefined();
  });

  it("builds the URI authenticator apps read", () => {
    const uri = otpauthUri("harness.example.com", "a@b.c", "ABC");
    expect(uri).toBe("otpauth://totp/harness.example.com:a%40b.c?secret=ABC&issuer=harness.example.com&algorithm=SHA1&digits=6&period=30");
  });
});

describe("second factor on the server", () => {
  let ts: TestServer;
  let admin: ReturnType<TestServer["as"]>;
  let adminPassword: string;
  let seq = 0;

  const post = (path: string, cookie: string, body?: unknown) =>
    fetch(`${ts.url}/api/auth${path}`, { method: "POST", headers: { "content-type": "application/json", origin: ts.url, cookie }, body: JSON.stringify(body ?? {}) });

  async function newUser(role = User_Role.USER) {
    const email = `mfa${++seq}@test.local`;
    const password = `user-password-${seq}`;
    await admin.admin.createUser({ name: `User ${seq}`, email, password, role });
    return { email, password };
  }

  /** A code of the current step for a secret; the store refuses a step twice, so tests take the next one when needed. */
  const now = (secret: string, offset = 0) => totpAt(secret, stepOf(Date.now()) + offset);

  /** Enrolls through the API with a device; returns the secret and recovery codes. */
  async function enroll(client: ReturnType<TestServer["as"]>) {
    const start = await client.mfa.startMfaEnrollment({});
    const { recoveryCodes } = await client.mfa.confirmMfaEnrollment({ code: now(start.secret, -1) });
    return { secret: start.secret, recoveryCodes };
  }

  beforeAll(async () => {
    ts = await startTestServer();
    adminPassword = ADMIN.password;
    admin = ts.as((await deviceLogin(ts)).accessToken);
  });

  afterAll(async () => {
    await ts.close();
  });

  beforeEach(async () => {
    // The requirement is per test; admins' own factor stays out of the way.
    await getDb().delete(schema.setting).where(eq(schema.setting.key, "mfa_policy"));
  });

  it("enrolls: the code confirms it, the secret is encrypted, recovery codes come once", async () => {
    const u = await newUser();
    const client = ts.as((await deviceLogin(ts, u.email, u.password)).accessToken);
    expect((await client.mfa.getMfaStatus({})).enabled).toBe(false);
    const start = await client.mfa.startMfaEnrollment({});
    expect(start.uri).toContain("otpauth://totp/");
    expect(start.qrSvg).toContain("<svg");
    expect(await code(client.mfa.confirmMfaEnrollment({ code: "000000" }))).toBe(Code.InvalidArgument);
    const { recoveryCodes } = await client.mfa.confirmMfaEnrollment({ code: now(start.secret, -1) });
    expect(recoveryCodes).toHaveLength(10);
    const st = await client.mfa.getMfaStatus({});
    expect(st).toMatchObject({ enabled: true, recoveryCodesLeft: 10 });

    const [row] = await getDb().select().from(schema.userMfa);
    const dump = JSON.stringify(await getDb().select().from(schema.userMfa));
    expect(row).toBeTruthy();
    expect(dump).not.toContain(start.secret);
    expect(dump).not.toContain(recoveryCodes[0]);
    // A second enrollment cannot silently replace the factor.
    expect(await code(client.mfa.startMfaEnrollment({}))).toBe(Code.FailedPrecondition);
  });

  it("a session owes the factor before it is good for anything", async () => {
    const u = await newUser();
    const device = await deviceLogin(ts, u.email, u.password);
    const { secret } = await enroll(ts.as(device.accessToken));

    const cookie = await signIn(ts.url, u.email, u.password);
    // The /device page asks for the code instead of anything else.
    const page = await fetch(`${ts.url}/device`, { headers: { cookie } }).then((r) => r.text());
    expect(page).toContain("Two-factor sign-in");
    expect(page).not.toContain("Connect a device");
    // It cannot approve a device, reach the admin plugin, or use cookie RPCs.
    const start = await ts.auth.startDeviceAuth({ clientName: "c", deviceName: "d", os: "o", arch: "a" });
    const approve = await decideOnDevicePage(ts.url, cookie, start.userCode, "approve");
    expect(await approve.text()).toContain("Two-factor sign-in");
    expect((await ts.auth.pollDeviceAuth({ deviceCode: start.deviceCode })).credentials).toBeUndefined();
    expect((await post("/admin/list-users", cookie)).status).toBeGreaterThanOrEqual(400);

    expect((await post("/mfa/verify", cookie, { code: "000000" })).status).toBe(401);
    expect((await post("/mfa/verify", cookie, { code: now(secret) })).status).toBe(200);
    const after = await fetch(`${ts.url}/device`, { headers: { cookie } }).then((r) => r.text());
    expect(after).toContain("Connect a device");
    const appr = await decideOnDevicePage(ts.url, cookie, start.userCode, "approve");
    expect(await appr.text()).toContain("Device connected");
  });

  it("a code works once", async () => {
    const u = await newUser();
    const { secret } = await enroll(ts.as((await deviceLogin(ts, u.email, u.password)).accessToken));
    const first = await signIn(ts.url, u.email, u.password);
    const c = now(secret, 1);
    expect((await post("/mfa/verify", first, { code: c })).status).toBe(200);
    const second = await signIn(ts.url, u.email, u.password);
    expect((await post("/mfa/verify", second, { code: c })).status).toBe(401);
  });

  it("recovery codes work once, and regeneration replaces them", async () => {
    const u = await newUser();
    const client = ts.as((await deviceLogin(ts, u.email, u.password)).accessToken);
    const { secret, recoveryCodes } = await enroll(client);
    const s1 = await signIn(ts.url, u.email, u.password);
    expect((await post("/mfa/verify", s1, { code: recoveryCodes[0] })).status).toBe(200);
    expect((await client.mfa.getMfaStatus({})).recoveryCodesLeft).toBe(9);
    const s2 = await signIn(ts.url, u.email, u.password);
    expect((await post("/mfa/verify", s2, { code: recoveryCodes[0] })).status).toBe(401);

    const fresh = (await client.mfa.regenerateRecoveryCodes({ code: now(secret, 1) })).recoveryCodes;
    expect(fresh).toHaveLength(10);
    expect(fresh).not.toContain(recoveryCodes[1]);
    const s3 = await signIn(ts.url, u.email, u.password);
    expect((await post("/mfa/verify", s3, { code: recoveryCodes[1] })).status).toBe(401);
    expect((await post("/mfa/verify", s3, { code: fresh[0] })).status).toBe(200);
  });

  it("limits wrong codes per user", async () => {
    const u = await newUser();
    await enroll(ts.as((await deviceLogin(ts, u.email, u.password)).accessToken));
    const cookie = await signIn(ts.url, u.email, u.password);
    let last = 0;
    for (let i = 0; i < 11; i++) last = (await post("/mfa/verify", cookie, { code: "000000" })).status;
    expect(last).toBe(429);
  });

  it("turning it off needs a code, and is refused while required", async () => {
    const u = await newUser();
    const client = ts.as((await deviceLogin(ts, u.email, u.password)).accessToken);
    const { secret } = await enroll(client);
    expect(await code(client.mfa.disableMfa({ code: "000000" }))).toBe(Code.InvalidArgument);
    await admin.mfa.setMfaPolicy({ policy: { required: true, roles: [User_Role.USER], exemptSso: false } });
    expect(await code(client.mfa.disableMfa({ code: now(secret, 1) }))).toBe(Code.FailedPrecondition);
    await admin.mfa.setMfaPolicy({ policy: { required: false, roles: [], exemptSso: false } });
    await client.mfa.disableMfa({ code: now(secret, 1) });
    expect((await client.mfa.getMfaStatus({})).enabled).toBe(false);
    const cookie = await signIn(ts.url, u.email, u.password);
    expect(await fetch(`${ts.url}/device`, { headers: { cookie } }).then((r) => r.text())).toContain("Connect a device");
  });

  it("the requirement: covers roles, refuses a caller it would lock out, enrolls at sign-in, stops devices", async () => {
    // An admin without a factor cannot turn it on for admins...
    expect(await code(admin.mfa.setMfaPolicy({ policy: { required: true, roles: [], exemptSso: false } }))).toBe(Code.FailedPrecondition);
    // ...but can for others.
    const before = await newUser();
    const dev = await deviceLogin(ts, before.email, before.password);
    await admin.mfa.setMfaPolicy({ policy: { required: true, roles: [User_Role.USER], exemptSso: false } });
    expect((await admin.mfa.getMfaPolicy({})).policy).toMatchObject({ required: true, roles: [User_Role.USER] });

    // A connected device of somebody without a factor cannot refresh any more.
    expect(await code(ts.auth.refreshAccessToken({ deviceToken: dev.deviceToken }))).toBe(Code.Unauthenticated);

    // Signing in owes enrollment, and the page offers it.
    const cookie = await signIn(ts.url, before.email, before.password);
    expect(await fetch(`${ts.url}/device`, { headers: { cookie } }).then((r) => r.text())).toContain("Set up two-factor sign-in");
    const start = await post("/mfa/enroll/start", cookie);
    const { secret } = (await start.json()) as { secret: string };
    const conf = await post("/mfa/enroll/confirm", cookie, { code: now(secret) });
    expect(conf.status).toBe(200);
    expect(((await conf.json()) as { recovery_codes: string[] }).recovery_codes).toHaveLength(10);
    expect(await fetch(`${ts.url}/device`, { headers: { cookie } }).then((r) => r.text())).toContain("Connect a device");
    // Now the device works again, and an admin (not covered) is unaffected.
    await ts.auth.refreshAccessToken({ deviceToken: dev.deviceToken });
    const adminCookie = await signIn(ts.url, ADMIN.email, adminPassword);
    expect(await fetch(`${ts.url}/device`, { headers: { cookie: adminCookie } }).then((r) => r.text())).toContain("Connect a device");
  });

  it("an admin resets a user's factor; only admins can", async () => {
    const u = await newUser();
    const client = ts.as((await deviceLogin(ts, u.email, u.password)).accessToken);
    await enroll(client);
    const me = (await client.auth.getMe({})).user!;
    expect(await code(client.mfa.resetUserMfa({ userId: me.id }))).toBe(Code.PermissionDenied);
    const listed = (await admin.admin.listUsers({ query: u.email })).users[0]!;
    expect(listed.mfaEnabled).toBe(true);
    await admin.mfa.resetUserMfa({ userId: me.id });
    expect((await client.mfa.getMfaStatus({})).enabled).toBe(false);
  });

  it("pending enrollment endpoints refuse a session that owes nothing", async () => {
    const u = await newUser();
    const cookie = await signIn(ts.url, u.email, u.password);
    expect((await post("/mfa/enroll/start", cookie)).status).toBe(409);
  });
});
