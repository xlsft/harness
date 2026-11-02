import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { PollDeviceAuthResponse_Status, User_Role } from "../src/gen/harness/auth/v1/auth_pb.ts";
import { ErrorInfoSchema, ErrorType } from "../src/gen/harness/common/v1/errors_pb.ts";
import { ADMIN, adminApi, decideOnDevicePage, deviceLogin, signIn, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;

beforeAll(async () => {
  ts = await startTestServer();
});
afterAll(async () => {
  await ts.close();
});

async function expectConnectError(p: Promise<unknown>, code: Code, type: ErrorType) {
  const err = await p.then(
    () => undefined,
    (e) => e,
  );
  expect(err).toBeInstanceOf(ConnectError);
  const ce = err as ConnectError;
  expect(ce.code).toBe(code);
  const [info] = ce.findDetails(ErrorInfoSchema);
  expect(info?.type).toBe(type);
  return ce;
}

describe("device flow", () => {
  it("runs pending -> slow_down -> approved -> expired (one-time) end to end", async () => {
    const start = await ts.auth.startDeviceAuth({ clientName: "harness-client", deviceName: "laptop", os: "darwin", arch: "arm64" });
    expect(start.userCode).toMatch(/^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$/);
    expect(start.deviceCode).toMatch(/^hdc_/);
    expect(start.verificationUri).toBe(`${ts.url}/device`);
    expect(start.verificationUriComplete).toBe(`${ts.url}/device?code=${start.userCode}`);
    expect(start.expiresInSeconds).toBe(600);
    expect(start.intervalSeconds).toBe(5);

    const p1 = await ts.auth.pollDeviceAuth({ deviceCode: start.deviceCode });
    expect(p1.status).toBe(PollDeviceAuthResponse_Status.PENDING);
    // polling again immediately is too fast
    const p2 = await ts.auth.pollDeviceAuth({ deviceCode: start.deviceCode });
    expect(p2.status).toBe(PollDeviceAuthResponse_Status.SLOW_DOWN);

    // the approval page shows the device to a signed-in user
    const cookie = await signIn(ts.url, ADMIN.email, ADMIN.password);
    const page = await fetch(`${ts.url}/device?code=${start.userCode.toLowerCase()}`, { headers: { cookie } });
    expect(page.status).toBe(200);
    const html = await page.text();
    expect(html).toContain("laptop");
    expect(html).toContain(start.userCode);

    // cross-origin form posts are rejected
    const csrf = await fetch(`${ts.url}/device`, {
      method: "POST",
      headers: { cookie, origin: "https://evil.example", "content-type": "application/x-www-form-urlencoded" },
      body: `code=${start.userCode}&action=approve`,
    });
    expect(csrf.status).toBe(403);

    expect((await decideOnDevicePage(ts.url, cookie, start.userCode, "approve")).status).toBe(200);
    const p3 = await ts.auth.pollDeviceAuth({ deviceCode: start.deviceCode });
    expect(p3.status).toBe(PollDeviceAuthResponse_Status.APPROVED);
    const c = p3.credentials!;
    expect(c.deviceId).toMatch(/^dev_/);
    expect(c.deviceToken).toMatch(/^hdt_/);
    expect(c.accessToken).toMatch(/^hat_/);
    expect(c.accessExpiresInSeconds).toBe(3600);
    expect(c.user?.email).toBe(ADMIN.email);
    expect(c.user?.role).toBe(User_Role.ADMIN);
    expect(c.user?.id).toMatch(/^usr_/);

    // the device code is single-use
    const p4 = await ts.auth.pollDeviceAuth({ deviceCode: start.deviceCode });
    expect(p4.status).toBe(PollDeviceAuthResponse_Status.EXPIRED);

    const me = await ts.as(c.accessToken).auth.getMe({});
    expect(me.user?.email).toBe(ADMIN.email);
    expect(me.device?.id).toBe(c.deviceId);
    expect(me.device?.name).toBe("laptop");
    expect(me.device?.os).toBe("darwin");
    expect(me.device?.createdAt).toMatch(/Z$/);
  });

  it("reports denied", async () => {
    const cookie = await signIn(ts.url, ADMIN.email, ADMIN.password);
    const start = await ts.auth.startDeviceAuth({ deviceName: "intruder" });
    await decideOnDevicePage(ts.url, cookie, start.userCode, "deny");
    const p = await ts.auth.pollDeviceAuth({ deviceCode: start.deviceCode });
    expect(p.status).toBe(PollDeviceAuthResponse_Status.DENIED);
    expect(p.credentials).toBeUndefined();
  });

  it("unknown device code is expired; empty is invalid", async () => {
    const p = await ts.auth.pollDeviceAuth({ deviceCode: "hdc_nope" });
    expect(p.status).toBe(PollDeviceAuthResponse_Status.EXPIRED);
    await expectConnectError(ts.auth.pollDeviceAuth({ deviceCode: "" }), Code.InvalidArgument, ErrorType.INVALID_ARGUMENT);
  });

  it("refreshes access tokens and revokes on logout", async () => {
    const creds = await deviceLogin(ts);
    const r = await ts.auth.refreshAccessToken({ deviceToken: creds.deviceToken });
    expect(r.accessToken).toMatch(/^hat_/);
    expect(r.accessToken).not.toBe(creds.accessToken);
    expect(r.accessExpiresInSeconds).toBe(3600);
    // both access tokens work until expiry
    await ts.as(r.accessToken).gateway.listModels({});
    await ts.as(creds.accessToken).gateway.listModels({});

    await ts.as(r.accessToken).auth.logout({});
    await expectConnectError(ts.as(r.accessToken).gateway.listModels({}), Code.Unauthenticated, ErrorType.UNAUTHENTICATED);
    await expectConnectError(ts.as(creds.accessToken).auth.getMe({}), Code.Unauthenticated, ErrorType.UNAUTHENTICATED);
    await expectConnectError(
      ts.auth.refreshAccessToken({ deviceToken: creds.deviceToken }),
      Code.Unauthenticated,
      ErrorType.UNAUTHENTICATED,
    );
  });
});

describe("auth guards", () => {
  it("rejects RPCs without or with a bad token", async () => {
    await expectConnectError(ts.gateway.listModels({}), Code.Unauthenticated, ErrorType.UNAUTHENTICATED);
    await expectConnectError(ts.as("hat_bogus").gateway.listModels({}), Code.Unauthenticated, ErrorType.UNAUTHENTICATED);
    await expectConnectError(ts.as("hdt_wrong_kind").prompts.getBundle({ model: "x" }), Code.Unauthenticated, ErrorType.UNAUTHENTICATED);
    await expectConnectError(ts.auth.getMe({}), Code.Unauthenticated, ErrorType.UNAUTHENTICATED);
    const stream = ts.gateway.streamMessage({ model: "x", maxTokens: 10 });
    await expectConnectError((async () => { for await (const _ of stream) { /* drain */ } })(), Code.Unauthenticated, ErrorType.UNAUTHENTICATED);
  });

  it("rejects clients below HARNESS_MIN_CLIENT_VERSION", async () => {
    process.env.HARNESS_MIN_CLIENT_VERSION = "1.2.0";
    try {
      const res = await fetch(`${ts.url}/harness.auth.v1.DeviceAuthService/StartDeviceAuth`, {
        method: "POST",
        headers: { "content-type": "application/json", "user-agent": "harness-client/1.1.9 (linux; amd64)" },
        body: "{}",
      });
      expect(res.status).toBe(400); // Connect maps FAILED_PRECONDITION to HTTP 400
      expect((await res.json()).code).toBe("failed_precondition");
      const ok = await fetch(`${ts.url}/harness.auth.v1.DeviceAuthService/StartDeviceAuth`, {
        method: "POST",
        headers: { "content-type": "application/json", "user-agent": "harness-client/1.2.0 (linux; amd64)" },
        body: "{}",
      });
      expect(ok.status).toBe(200);
    } finally {
      delete process.env.HARNESS_MIN_CLIENT_VERSION;
    }
  });

  it("protects the admin API by role", async () => {
    const admin = await deviceLogin(ts);
    expect((await adminApi(ts.url, "hat_bogus", "GET", "/api/admin/providers")).status).toBe(401);
    expect((await fetch(`${ts.url}/api/admin/providers`)).status).toBe(401);
    expect((await adminApi(ts.url, admin.accessToken, "GET", "/api/admin/providers")).status).toBe(200);

    // create a regular user through the Better Auth admin plugin (session cookie of the admin)
    const created = await fetch(`${ts.url}/api/auth/admin/create-user`, {
      method: "POST",
      headers: { cookie: admin.cookie, origin: ts.url, "content-type": "application/json" },
      body: JSON.stringify({ email: "user@test.local", password: "user-password-1", name: "User", role: "user" }),
    });
    expect(created.status).toBe(200);
    const user = await deviceLogin(ts, "user@test.local", "user-password-1");
    expect(user.user?.role).toBe(User_Role.USER);
    const denied = await adminApi(ts.url, user.accessToken, "GET", "/api/admin/providers");
    expect(denied.status).toBe(403);
    expect(denied.body.error.type).toBe("permission");
    // but the user can use the gateway
    const models = await ts.as(user.accessToken).gateway.listModels({});
    expect(models.models).toEqual([]);
  });

  it("does not allow public sign-up by default", async () => {
    const res = await fetch(`${ts.url}/api/auth/sign-up/email`, {
      method: "POST",
      headers: { "content-type": "application/json", origin: ts.url },
      body: JSON.stringify({ email: "x@test.local", password: "password-123", name: "X" }),
    });
    expect(res.ok).toBe(false);
  });
});
