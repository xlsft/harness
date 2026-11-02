import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { startTestServer, type TestServer } from "./helpers/server.ts";

// A fresh server (no HARNESS_ADMIN_* bootstrap): the first visitor creates the admin account, then
// sign-up closes.
let ts: TestServer;
beforeAll(async () => {
  ts = await startTestServer({ HARNESS_ADMIN_EMAIL: "", HARNESS_ADMIN_PASSWORD: "" });
});
afterAll(() => ts.close());

const post = (path: string, body: unknown, cookie?: string) =>
  fetch(`${ts.url}/api/auth${path}`, {
    method: "POST",
    headers: { "content-type": "application/json", origin: ts.url, ...(cookie ? { cookie } : {}) },
    body: JSON.stringify(body),
  });

// The same request body the setup form on /device sends.
const signUp = (email: string, username?: string) =>
  post("/sign-up/email", { email, username, password: "password-123", name: email.split("@")[0] });

const cookieOf = (res: Response) =>
  res.headers
    .getSetCookie()
    .map((c) => c.split(";")[0])
    .join("; ");

describe("first account", () => {
  it("offers account setup (with a username) on the device page while there are no accounts", async () => {
    const html = await (await fetch(`${ts.url}/device?code=ABCD-EFGH`)).text();
    expect(html).toContain("Create the first account");
    expect(html).toContain("ABCD-EFGH");
    expect(html).toMatch(/<label>Username<input name="username"[^>]* required>/);
    expect(html).toContain("username: fd.get('username')");
  });

  it("makes the first account an administrator, with its username, and closes sign-up after it", async () => {
    const first = await signUp("owner@test.local", "Owner.One");
    expect(first.ok).toBe(true);
    const body = (await first.json()) as { user: { role?: string; username?: string } };
    expect(body.user.role).toBe("admin");
    expect(body.user.username).toBe("owner.one");

    expect((await signUp("someone@test.local", "someone")).status).toBe(403);
    expect((await signUp("other@test.local")).status).toBe(403);

    const html = await (await fetch(`${ts.url}/device?code=ABCD-EFGH`)).text();
    expect(html).toContain("Sign in to connect a device");
  });
});

describe("sign-in", () => {
  it("offers one email-or-username field on the device page and picks the endpoint by '@'", async () => {
    const html = await (await fetch(`${ts.url}/device`)).text();
    expect(html).toContain('<label>Email or username<input name="login"');
    expect(html).toContain("login.includes('@')");
    expect(html).toContain("/api/auth/sign-in/username");
    expect(html).toContain("/api/auth/sign-in/email");
  });

  it("signs in by email", async () => {
    const res = await post("/sign-in/email", { email: "owner@test.local", password: "password-123" });
    expect(res.ok).toBe(true);
    expect(cookieOf(res)).toContain("session_token");
  });

  it("signs in by username, case-insensitively", async () => {
    const res = await post("/sign-in/username", { username: "OWNER.one", password: "password-123" });
    expect(res.ok).toBe(true);
    const page = await (await fetch(`${ts.url}/device`, { headers: { cookie: cookieOf(res) } })).text();
    expect(page).toContain("Connect a device");

    expect((await post("/sign-in/username", { username: "owner.one", password: "wrong-password" })).ok).toBe(false);
    expect((await post("/sign-in/username", { username: "nobody", password: "password-123" })).ok).toBe(false);
  });

  it("lets an admin create a user with a username, who then signs in by it", async () => {
    const admin = cookieOf(await post("/sign-in/email", { email: "owner@test.local", password: "password-123" }));
    const user = (email: string, username: string) => ({ email, password: "member-password-1", name: username, data: { username } });
    expect((await post("/admin/create-user", user("member@test.local", "member_1"), admin)).ok).toBe(true);
    expect((await post("/sign-in/username", { username: "member_1", password: "member-password-1" })).ok).toBe(true);

    // usernames are unique (after lowercasing) and validated
    expect((await post("/admin/create-user", user("dup@test.local", "Member_1"), admin)).ok).toBe(false);
    expect((await post("/admin/create-user", user("bad@test.local", "no spaces"), admin)).ok).toBe(false);
  });
});
