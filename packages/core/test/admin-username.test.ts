import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { ensureAdmin } from "../src/auth/better-auth.ts";
import { ADMIN, startTestServer, type TestServer } from "./helpers/server.ts";

// Admins created by the bootstrap (HARNESS_ADMIN_*) or `pnpm admin:create` get a username too, so they can
// sign in with either.
let ts: TestServer;
beforeAll(async () => {
  ts = await startTestServer({ HARNESS_ADMIN_USERNAME: "Root.Op" });
});
afterAll(() => ts.close());

const signIn = (username: string, password: string) =>
  fetch(`${ts.url}/api/auth/sign-in/username`, {
    method: "POST",
    headers: { "content-type": "application/json", origin: ts.url },
    body: JSON.stringify({ username, password }),
  });

describe("admin username", () => {
  it("takes HARNESS_ADMIN_USERNAME for the bootstrap admin", async () => {
    expect((await signIn("root.op", ADMIN.password)).ok).toBe(true);
  });

  it("defaults to the email's local part", async () => {
    expect(await ensureAdmin("ops@test.local", "ops-password-1")).toBe("created");
    expect((await signIn("ops", "ops-password-1")).ok).toBe(true);
  });

  it("skips a username someone already has", async () => {
    expect(await ensureAdmin("root.op@other.local", "other-password-1")).toBe("created");
    expect((await signIn("root.op", ADMIN.password)).ok).toBe(true);
  });
});
