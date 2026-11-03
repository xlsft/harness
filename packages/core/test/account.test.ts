/**
 * The signed-in user's own account over DeviceAuthService: the GetMe profile, UpdateMe (name, avatar data URLs
 * checked by content, size and dimensions), ChangePassword (local accounts only, current password required,
 * optional sign-out elsewhere) and the device list's current device, which cannot be revoked by itself.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { User_Role, UserSource } from "../src/gen/harness/auth/v1/auth_pb.ts";
import { ADMIN, createUserAndLogin, deviceLogin, signIn, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let adminCookie: string;

beforeAll(async () => {
  ts = await startTestServer();
  adminCookie = (await deviceLogin(ts)).cookie;
});
afterAll(() => ts.close());
beforeEach(async () => {
  const { resetPasswordAttempts } = await import("../src/services/account.ts");
  resetPasswordAttempts();
});

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
}

async function message(p: Promise<unknown>): Promise<string> {
  try {
    await p;
    return "";
  } catch (e) {
    return (e as ConnectError).rawMessage;
  }
}

// --- images ---

function png(width: number, height: number): Buffer {
  const b = Buffer.alloc(33);
  b.writeUInt32BE(0x89504e47, 0);
  b.writeUInt32BE(0x0d0a1a0a, 4);
  b.writeUInt32BE(13, 8);
  b.write("IHDR", 12, "ascii");
  b.writeUInt32BE(width, 16);
  b.writeUInt32BE(height, 20);
  return b;
}

function jpeg(width: number, height: number): Buffer {
  // SOI, an APP0 segment, then SOF0 with the size.
  const app0 = Buffer.from([0xff, 0xe0, 0x00, 0x10, ...Buffer.from("JFIF\0"), 1, 1, 0, 0, 1, 0, 1, 0, 0]);
  const sof = Buffer.alloc(19);
  sof.writeUInt16BE(0xffc0, 0);
  sof.writeUInt16BE(17, 2);
  sof[4] = 8;
  sof.writeUInt16BE(height, 5);
  sof.writeUInt16BE(width, 7);
  return Buffer.concat([Buffer.from([0xff, 0xd8]), app0, sof, Buffer.from([0xff, 0xd9])]);
}

function webpLossless(width: number, height: number): Buffer {
  const b = Buffer.alloc(30);
  b.write("RIFF", 0, "ascii");
  b.writeUInt32LE(22, 4);
  b.write("WEBP", 8, "ascii");
  b.write("VP8L", 12, "ascii");
  b.writeUInt32LE(10, 16);
  b[20] = 0x2f;
  b.writeUInt32LE((width - 1) | ((height - 1) << 14), 21);
  return b;
}

const dataUrl = (type: string, bytes: Buffer) => `data:image/${type};base64,${bytes.toString("base64")}`;

describe("avatar parsing", () => {
  it("reads the size of PNG, JPEG and WebP images", async () => {
    const { imageSize } = await import("../src/services/account.ts");
    expect(imageSize(png(64, 48))).toEqual({ type: "png", width: 64, height: 48 });
    expect(imageSize(jpeg(200, 100))).toEqual({ type: "jpeg", width: 200, height: 100 });
    expect(imageSize(webpLossless(256, 128))).toEqual({ type: "webp", width: 256, height: 128 });
    expect(imageSize(Buffer.from("<svg xmlns='http://www.w3.org/2000/svg'/>"))).toBeUndefined();
  });

  it("normalizes valid data URLs and refuses everything else", async () => {
    const { parseAvatar } = await import("../src/services/account.ts");
    expect(parseAvatar(dataUrl("jpg", jpeg(10, 10)))).toBe(dataUrl("jpeg", jpeg(10, 10)));
    expect(parseAvatar(dataUrl("webp", webpLossless(256, 256)))).toBe(dataUrl("webp", webpLossless(256, 256)));
    const bad = [
      "https://example.com/a.png",
      dataUrl("svg+xml", Buffer.from("<svg/>")),
      dataUrl("gif", Buffer.from("GIF89a")),
      // Declared PNG, actually a JPEG.
      dataUrl("png", jpeg(10, 10)),
      dataUrl("png", png(257, 10)),
      dataUrl("png", png(0, 10)),
      dataUrl("png", Buffer.concat([png(10, 10), Buffer.alloc(200 * 1024)])),
    ];
    for (const b of bad) expect(() => parseAvatar(b), b.slice(0, 40)).toThrow();
  });
});

describe("profile", () => {
  it("GetMe returns the profile with source, dates and the calling device", async () => {
    const me = await deviceLogin(ts);
    const r = await ts.as(me.accessToken).auth.getMe({});
    expect(r.user).toMatchObject({ id: me.user!.id, email: ADMIN.email, role: User_Role.ADMIN, source: UserSource.LOCAL, image: "" });
    expect(r.user!.username).toBe("admin");
    expect(r.user!.createdAt).toMatch(/Z$/);
    expect(Date.parse(r.user!.lastSignInAt)).toBeGreaterThan(Date.now() - 60_000);
    expect(r.device).toMatchObject({ id: me.deviceId, current: true, clientName: "harness-client" });
  });

  it("UpdateMe changes the name and sets and removes the avatar", async () => {
    const u = await createUserAndLogin(ts, adminCookie, "profile@test.local");
    const c = ts.as(u.accessToken).auth;
    const avatar = dataUrl("png", png(128, 128));

    let r = await c.updateMe({ name: "  New Name  ", image: avatar });
    expect(r.user).toMatchObject({ name: "New Name", image: avatar, email: "profile@test.local" });
    expect((await c.getMe({})).user).toMatchObject({ name: "New Name", image: avatar });

    // Unset fields stay as they are.
    r = await c.updateMe({ name: "Other" });
    expect(r.user).toMatchObject({ name: "Other", image: avatar });
    r = await c.updateMe({ image: "" });
    expect(r.user).toMatchObject({ name: "Other", image: "" });

    expect(await code(c.updateMe({ name: "   " }))).toBe(Code.InvalidArgument);
    expect(await code(c.updateMe({ name: "x".repeat(101) }))).toBe(Code.InvalidArgument);
    expect(await code(c.updateMe({ image: dataUrl("png", png(512, 512)) }))).toBe(Code.InvalidArgument);
    expect(await code(c.updateMe({ image: "data:image/svg+xml;base64,PHN2Zy8+" }))).toBe(Code.InvalidArgument);
    expect(await code(ts.auth.updateMe({ name: "anon" }))).toBe(Code.Unauthenticated);
  });

  it("directory users cannot change their name but can set an avatar", async () => {
    const u = await createUserAndLogin(ts, adminCookie, "dir-user@test.local");
    await linkLdap(u.user!.id, "dir-user");
    const c = ts.as(u.accessToken).auth;
    expect((await c.getMe({})).user!.source).toBe(UserSource.LDAP);
    expect(await code(c.updateMe({ name: "Renamed" }))).toBe(Code.FailedPrecondition);
    // The same name is not a change.
    expect((await c.updateMe({ name: "dir-user" })).user!.name).toBe("dir-user");
    expect((await c.updateMe({ image: dataUrl("jpeg", jpeg(64, 64)) })).user!.image).toMatch(/^data:image\/jpeg;base64,/);
  });
});

async function linkLdap(userId: string, username: string) {
  const { getDb, schema } = await import("../src/db/index.ts");
  const now = new Date();
  await getDb().insert(schema.account).values({ id: `acc_${username}`, accountId: username, providerId: "ldap", userId, createdAt: now, updatedAt: now });
}

describe("change password", () => {
  it("needs the current password and changes it through Better Auth", async () => {
    const email = "pw@test.local";
    const u = await createUserAndLogin(ts, adminCookie, email, "old-password-1");
    const c = ts.as(u.accessToken).auth;

    expect(await message(c.changePassword({ currentPassword: "wrong-password", newPassword: "new-password-1" }))).toMatch(/current password is incorrect/);
    expect(await code(c.changePassword({ currentPassword: "old-password-1", newPassword: "short" }))).toBe(Code.InvalidArgument);
    await c.changePassword({ currentPassword: "old-password-1", newPassword: "new-password-1" });
    await expect(signIn(ts.url, email, "old-password-1")).rejects.toThrow();
    await signIn(ts.url, email, "new-password-1");
    // Without revoke_other_sessions everything stays signed in.
    expect((await c.getMe({})).user!.email).toBe(email);
  });

  it("can sign out web sessions and other devices, keeping the calling one", async () => {
    const email = "pw-revoke@test.local";
    const a = await createUserAndLogin(ts, adminCookie, email, "old-password-1");
    const b = await deviceLogin(ts, email, "old-password-1");
    await ts.as(a.accessToken).auth.changePassword({ currentPassword: "old-password-1", newPassword: "new-password-1", revokeOtherSessions: true });
    expect((await ts.as(a.accessToken).auth.getMe({})).user!.email).toBe(email);
    expect(await code(ts.as(b.accessToken).auth.getMe({}))).toBe(Code.Unauthenticated);
    const session = await fetch(`${ts.url}/api/auth/get-session`, { headers: { cookie: b.cookie } });
    expect(await session.json()).toBeNull();
  });

  it("refuses directory users and limits attempts", async () => {
    const u = await createUserAndLogin(ts, adminCookie, "pw-ldap@test.local");
    await linkLdap(u.user!.id, "pw-ldap");
    expect(await message(ts.as(u.accessToken).auth.changePassword({ currentPassword: "user-password-1", newPassword: "new-password-1" }))).toMatch(/LDAP/);

    const v = await createUserAndLogin(ts, adminCookie, "pw-limit@test.local");
    const c = ts.as(v.accessToken).auth;
    for (let i = 0; i < 10; i++) expect(await code(c.changePassword({ currentPassword: "wrong", newPassword: "new-password-1" }))).toBe(Code.InvalidArgument);
    expect(await code(c.changePassword({ currentPassword: "user-password-1", newPassword: "new-password-1" }))).toBe(Code.ResourceExhausted);
  });
});

describe("devices", () => {
  it("marks the calling device and refuses to revoke it", async () => {
    const email = "devices@test.local";
    const a = await createUserAndLogin(ts, adminCookie, email);
    const b = await deviceLogin(ts, email, "user-password-1");
    const c = ts.as(a.accessToken).auth;
    const { devices } = await c.listDevices({});
    expect(devices.map((d) => [d.id, d.current]).sort()).toEqual([[a.deviceId, true], [b.deviceId, false]].sort());
    expect(devices[0]).toMatchObject({ name: "test-box", os: "linux", arch: "amd64", clientName: "harness-client" });

    expect(await code(c.revokeDevice({ deviceId: a.deviceId }))).toBe(Code.FailedPrecondition);
    expect((await c.getMe({})).device!.id).toBe(a.deviceId);
    await c.revokeDevice({ deviceId: b.deviceId });
    expect((await c.listDevices({})).devices.map((d) => d.id)).toEqual([a.deviceId]);
  });
});
