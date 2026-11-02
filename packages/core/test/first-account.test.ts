import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { startTestServer, type TestServer } from "./helpers/server.ts";

// A fresh server (no HARNESS_ADMIN_* bootstrap): the first visitor creates the admin account, then
// sign-up closes.
let ts: TestServer;
beforeAll(async () => {
  ts = await startTestServer({ HARNESS_ADMIN_EMAIL: "", HARNESS_ADMIN_PASSWORD: "" });
});
afterAll(() => ts.close());

const signUp = (email: string) =>
  fetch(`${ts.url}/api/auth/sign-up/email`, {
    method: "POST",
    headers: { "content-type": "application/json", origin: ts.url },
    body: JSON.stringify({ email, password: "password-123", name: email.split("@")[0] }),
  });

describe("first account", () => {
  it("offers account setup on the device page while there are no accounts", async () => {
    const html = await (await fetch(`${ts.url}/device?code=ABCD-EFGH`)).text();
    expect(html).toContain("Create the first account");
    expect(html).toContain("ABCD-EFGH");
  });

  it("makes the first account an administrator and closes sign-up after it", async () => {
    const first = await signUp("owner@test.local");
    expect(first.ok).toBe(true);
    const body = (await first.json()) as { user: { role?: string } };
    expect(body.user.role).toBe("admin");

    const second = await signUp("someone@test.local");
    expect(second.status).toBe(403);

    const html = await (await fetch(`${ts.url}/device?code=ABCD-EFGH`)).text();
    expect(html).toContain("Sign in to connect a device");
  });
});
