/**
 * Notes between sessions and proposals, across a real core and the real Go relay host: core starts here, and
 * packages/client/relay/sessionlinks_core_test.go drives the client side against it. Skipped without a Go toolchain.
 */
import { spawnSync } from "node:child_process";
import { resolve } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const hasGo = spawnSync("go", ["version"]).status === 0;

describe.skipIf(!hasGo)("session links with the Go client", () => {
  let ts: TestServer;
  let hostToken: string;
  let viewerToken: string;

  beforeAll(async () => {
    ts = await startTestServer({ HARNESS_RELAY_HEARTBEAT_MS: "300", HARNESS_RELAY_ACK_WAIT_MS: "5000" });
    const admin = await deviceLogin(ts);
    const alice = await createUserAndLogin(ts, admin.cookie, "links@test.local", "links-password-1");
    const second = await deviceLogin(ts, "links@test.local", "links-password-1");
    hostToken = alice.accessToken;
    viewerToken = second.accessToken;
  });

  afterAll(async () => {
    await ts.close();
  });

  it("delivers notes through the relay and the sync document, and starts proposals from another device", async () => {
    // Asynchronous: the core under test runs in this process and must keep answering.
    const { spawn } = await import("node:child_process");
    const out: string[] = [];
    const code = await new Promise<number | null>((done, fail) => {
      const p = spawn("go", ["test", "./relay", "-run", "^TestSessionLinksAgainstCore$", "-count=1", "-v", "-timeout", "150s"], {
        cwd: resolve(import.meta.dirname, "../../client"),
        env: { ...process.env, HARNESS_E2E_CORE_URL: ts.url, HARNESS_E2E_TOKEN: hostToken, HARNESS_E2E_VIEWER_TOKEN: viewerToken },
      });
      p.stdout.on("data", (d) => out.push(String(d)));
      p.stderr.on("data", (d) => out.push(String(d)));
      p.on("error", fail);
      p.on("close", done);
    });
    const log = out.join("");
    expect(code, log.slice(-6000)).toBe(0);
    expect(log, "the Go test did not run").toContain("--- PASS: TestSessionLinksAgainstCore");
  }, 240_000);
});
