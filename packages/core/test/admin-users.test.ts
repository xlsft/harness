/**
 * AdminService user administration: only admins may call it (PERMISSION_DENIED otherwise), the last-admin and
 * self rules, generated passwords shown once, disabling signs a user out everywhere, and a user's personal
 * providers are visible with key_set only.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { UserSource } from "../src/gen/harness/auth/v1/admin_pb.ts";
import { User_Role } from "../src/gen/harness/auth/v1/auth_pb.ts";
import { ProviderScope } from "../src/gen/harness/llm/v1/providers_pb.ts";
import { ADMIN, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let admin: ReturnType<TestServer["as"]>;
let adminId: string;
let adminCookie: string;
let user: ReturnType<TestServer["as"]>;
let userId: string;

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
}

const signInOk = async (login: string, password: string) => {
  const byEmail = login.includes("@");
  const res = await fetch(`${ts.url}/api/auth/sign-in/${byEmail ? "email" : "username"}`, {
    method: "POST",
    headers: { "content-type": "application/json", origin: ts.url },
    body: JSON.stringify(byEmail ? { email: login, password } : { username: login, password }),
  });
  return res.ok;
};

beforeAll(async () => {
  ts = await startTestServer();
  const a = await deviceLogin(ts);
  admin = ts.as(a.accessToken);
  adminCookie = a.cookie;
  adminId = a.user!.id;
  const u = await createUserAndLogin(ts, adminCookie, "member@test.local");
  user = ts.as(u.accessToken);
  userId = u.user!.id;
});
afterAll(() => ts.close());

describe("authorization", () => {
  it("refuses every method to non-admins and to anonymous callers", async () => {
    const calls = (c: ReturnType<TestServer["as"]>["admin"]) => [
      c.listUsers({}),
      c.createUser({ name: "x", email: "x@test.local", password: "password-123" }),
      c.resetPassword({ userId: adminId, generate: true }),
      c.setRole({ userId, role: User_Role.ADMIN }),
      c.setDisabled({ userId: adminId, disabled: true }),
      c.deleteUser({ userId: adminId }),
      c.listUserSessions({ userId: adminId }),
      c.revokeUserSessions({ userId: adminId }),
      c.listUserProviders({ userId: adminId }),
      c.deleteUserProvider({ userId: adminId, providerId: "x" }),
      c.getLdapConfig({}),
      c.setLdapConfig({ config: { enabled: false } }),
      c.testLdapConnection({}),
      c.testLdapUser({ login: "x" }),
    ];
    for (const call of calls(user.admin)) expect(await code(call)).toBe(Code.PermissionDenied);
    for (const call of calls(ts.admin)) expect(await code(call)).toBe(Code.Unauthenticated);
    // Nothing changed.
    expect((await admin.admin.listUsers({ query: "member" })).users[0]!.role).toBe(User_Role.USER);
  });
});

describe("users", () => {
  it("lists users with source, role and activity, and searches them", async () => {
    const { users } = await admin.admin.listUsers({});
    const me = users.find((u) => u.id === adminId)!;
    expect(me).toMatchObject({ email: ADMIN.email, role: User_Role.ADMIN, source: UserSource.LOCAL, isSelf: true, disabled: false });
    expect(me.lastSignInAt).not.toBe("");
    expect(users.find((u) => u.id === userId)).toMatchObject({ isSelf: false, role: User_Role.USER });
    expect((await admin.admin.listUsers({ query: "MEMB" })).users.map((u) => u.id)).toEqual([userId]);
    expect((await admin.admin.listUsers({ query: "nobody-matches" })).users).toEqual([]);
    expect((await admin.admin.listUsers({ query: "%" })).users).toEqual([]);
  });

  it("creates a user with a password, or with a generated one shown once", async () => {
    const r = await admin.admin.createUser({ name: "Carol", username: "Carol.C", email: "Carol@Test.local", password: "carol-password-1" });
    expect(r.generatedPassword).toBe("");
    expect(r.user).toMatchObject({ name: "Carol", username: "carol.c", email: "carol@test.local", role: User_Role.USER, source: UserSource.LOCAL });
    expect(await signInOk("carol.c", "carol-password-1")).toBe(true);

    const g = await admin.admin.createUser({ name: "Dave", email: "dave@test.local", generatePassword: true, role: User_Role.ADMIN });
    expect(g.generatedPassword.length).toBeGreaterThanOrEqual(20);
    expect(g.user!.role).toBe(User_Role.ADMIN);
    expect(await signInOk("dave@test.local", g.generatedPassword)).toBe(true);
    // Never shown again.
    expect(JSON.stringify(await admin.admin.listUsers({}))).not.toContain(g.generatedPassword);

    expect(await code(admin.admin.createUser({ name: "Dup", email: "carol@test.local", password: "password-123" }))).toBe(Code.Aborted);
    expect(await code(admin.admin.createUser({ name: "Dup", username: "CAROL.C", email: "c2@test.local", password: "password-123" }))).toBe(Code.Aborted);
    expect(await code(admin.admin.createUser({ name: "Short", email: "short@test.local", password: "short" }))).toBe(Code.InvalidArgument);
    expect(await code(admin.admin.createUser({ name: "Bad", username: "no spaces", email: "bad@test.local", password: "password-123" }))).toBe(Code.InvalidArgument);
  });

  it("resets a password (generated or set) and can sign the user out", async () => {
    const { users } = await admin.admin.listUsers({ query: "carol" });
    const carol = users[0]!;
    const g = await admin.admin.resetPassword({ userId: carol.id, generate: true });
    expect(await signInOk("carol.c", "carol-password-1")).toBe(false);
    expect(await signInOk("carol.c", g.generatedPassword)).toBe(true);
    await admin.admin.resetPassword({ userId: carol.id, password: "carol-password-2", revokeSessions: true });
    expect(await signInOk("carol.c", "carol-password-2")).toBe(true);
    expect(await code(admin.admin.resetPassword({ userId: carol.id, password: "short" }))).toBe(Code.InvalidArgument);
    expect(await code(admin.admin.resetPassword({ userId: "usr_missing", generate: true }))).toBe(Code.NotFound);
  });
});

describe("last-admin and self rules", () => {
  it("cannot demote, disable or delete the last active admin, nor disable or delete yourself", async () => {
    // Dave (an admin from above) goes away, so the bootstrap admin is the only one.
    const dave = (await admin.admin.listUsers({ query: "dave" })).users[0]!;
    await admin.admin.deleteUser({ userId: dave.id });

    expect(await code(admin.admin.setRole({ userId: adminId, role: User_Role.USER }))).toBe(Code.FailedPrecondition);
    expect(await code(admin.admin.setDisabled({ userId: adminId, disabled: true }))).toBe(Code.FailedPrecondition);
    expect(await code(admin.admin.deleteUser({ userId: adminId }))).toBe(Code.FailedPrecondition);

    // The service rule itself (a caller who is not the target): still the last admin.
    const { deleteUser, setDisabled } = await import("../src/services/admin-users.ts");
    await expect(deleteUser(adminId, "usr_someone_else")).rejects.toThrow(/last admin/);
    await expect(setDisabled(adminId, true, "", "usr_someone_else")).rejects.toThrow(/last admin/);

    // With a second admin, the first can be demoted (by the other) and promoted back.
    const second = await admin.admin.createUser({ name: "Second", email: "second@test.local", password: "second-password-1", role: User_Role.ADMIN });
    const secondId = second.user!.id;
    const other = ts.as((await deviceLogin(ts, "second@test.local", "second-password-1")).accessToken);
    expect((await other.admin.setRole({ userId: adminId, role: User_Role.USER })).user!.role).toBe(User_Role.USER);
    expect(await code(other.admin.setRole({ userId: secondId, role: User_Role.USER }))).toBe(Code.FailedPrecondition);
    expect(await code(other.admin.setDisabled({ userId: secondId, disabled: true }))).toBe(Code.FailedPrecondition);
    await other.admin.setRole({ userId: adminId, role: User_Role.ADMIN });
    // A disabled admin does not count: the other admin is then the last active one.
    await admin.admin.setDisabled({ userId: secondId, disabled: true });
    expect(await code(admin.admin.setRole({ userId: adminId, role: User_Role.USER }))).toBe(Code.FailedPrecondition);
    await admin.admin.deleteUser({ userId: secondId });
  });
});

describe("disable, sessions and devices", () => {
  it("disabling signs the user out everywhere and blocks sign-in until enabled", async () => {
    const r = await admin.admin.setDisabled({ userId, disabled: true, reason: "left the team" });
    expect(r.user).toMatchObject({ disabled: true, disabledReason: "left the team" });
    expect(await code(user.auth.getMe({}))).toBe(Code.Unauthenticated);
    expect(await signInOk("member@test.local", "user-password-1")).toBe(false);

    await admin.admin.setDisabled({ userId, disabled: false });
    expect(await signInOk("member@test.local", "user-password-1")).toBe(true);
    user = ts.as((await deviceLogin(ts, "member@test.local", "user-password-1")).accessToken);
    expect((await user.auth.getMe({})).user!.id).toBe(userId);
  });

  it("lists a user's devices and revokes one or all", async () => {
    const second = ts.as((await deviceLogin(ts, "member@test.local", "user-password-1")).accessToken);
    const s = await admin.admin.listUserSessions({ userId });
    expect(s.devices.length).toBe(2);
    expect(s.webSessions).toBeGreaterThan(0);
    const secondId = (await second.auth.getMe({})).device!.id;
    expect(await admin.admin.revokeUserSessions({ userId, deviceId: secondId })).toMatchObject({ revokedDevices: 1 });
    expect(await code(second.auth.getMe({}))).toBe(Code.Unauthenticated);
    expect((await user.auth.getMe({})).user!.id).toBe(userId);
    // A device of someone else is not found through this user.
    const mine = (await admin.auth.getMe({})).device!.id;
    expect(await code(admin.admin.revokeUserSessions({ userId, deviceId: mine }))).toBe(Code.NotFound);

    const all = await admin.admin.revokeUserSessions({ userId });
    expect(all.revokedDevices).toBe(1);
    expect(all.revokedWebSessions).toBeGreaterThan(0);
    expect(await code(user.auth.getMe({}))).toBe(Code.Unauthenticated);
    expect(await admin.admin.listUserSessions({ userId })).toMatchObject({ devices: [], webSessions: 0 });
  });
});

describe("personal providers", () => {
  it("shows whether a key is set (never the key or its hint) and deletes one", async () => {
    const u = ts.as((await deviceLogin(ts, "member@test.local", "user-password-1")).accessToken);
    const key = "sk-member-fake-key-0000000000";
    const p = (await u.providers.createProvider({ scope: ProviderScope.PERSONAL, type: "anthropic", baseUrl: "https://1.1.1.1", apiKey: key })).provider!;
    const r = await admin.admin.listUserProviders({ userId });
    expect(r.providers).toEqual([expect.objectContaining({ id: p.id, type: "anthropic", apiKeySet: true, modelCount: 0 })]);
    expect(JSON.stringify(r)).not.toContain(key.slice(-4));

    expect(await code(admin.admin.deleteUserProvider({ userId: adminId, providerId: p.id }))).toBe(Code.NotFound);
    await admin.admin.deleteUserProvider({ userId, providerId: p.id });
    expect((await admin.admin.listUserProviders({ userId })).providers).toEqual([]);
  });

  it("deletes a user with what they own", async () => {
    await admin.admin.deleteUser({ userId });
    expect((await admin.admin.listUsers({ query: "member" })).users).toEqual([]);
    expect(await signInOk("member@test.local", "user-password-1")).toBe(false);
  });
});
