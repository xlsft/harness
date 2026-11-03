/**
 * AdminService permission mode policy: admins only, "default" can never be disabled, unknown modes are refused,
 * the list lands in the org POLICY document "default" (other keys kept) where every signed-in user reads it, and
 * the older single-mode switches count as disabled modes and are dropped when the mode is enabled again.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { PermissionMode } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { disabledModesOf, validateDisabledModes, withDisabledModes } from "../src/services/mode-policy.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let admin: ReturnType<TestServer["as"]>;
let user: ReturnType<TestServer["as"]>;

const POLICY_REF = { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default" };

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
}

beforeAll(async () => {
  ts = await startTestServer();
  const a = await deviceLogin(ts);
  admin = ts.as(a.accessToken);
  const u = await createUserAndLogin(ts, a.cookie, "policy-member@test.local");
  user = ts.as(u.accessToken);
});
afterAll(() => ts.close());

describe("authorization", () => {
  it("refuses both methods to non-admins and anonymous callers", async () => {
    const calls = (c: ReturnType<TestServer["as"]>["admin"]) => [
      c.getPermissionModePolicy({}),
      c.setPermissionModePolicy({ policy: { disabledModes: [PermissionMode.BYPASS_PERMISSIONS] } }),
    ];
    for (const call of calls(user.admin)) expect(await code(call)).toBe(Code.PermissionDenied);
    for (const call of calls(ts.admin)) expect(await code(call)).toBe(Code.Unauthenticated);
    expect((await admin.admin.getPermissionModePolicy({})).policy?.disabledModes).toEqual([]);
  });
});

describe("policy", () => {
  it("validates the list", async () => {
    for (const disabledModes of [
      [PermissionMode.DEFAULT],
      [PermissionMode.UNSPECIFIED],
      [99 as PermissionMode],
    ]) {
      expect(await code(admin.admin.setPermissionModePolicy({ policy: { disabledModes } }))).toBe(Code.InvalidArgument);
    }
    expect(() => validateDisabledModes(["acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions", "default"])).toThrow();
    expect(validateDisabledModes(["bypassPermissions", "dontAsk", "dontAsk"])).toEqual(["dontAsk", "bypassPermissions"]);
  });

  it("stores the list in the POLICY document, keeps other keys, and every user reads it", async () => {
    await admin.sync.putDocument({ ref: POLICY_REF, content: JSON.stringify({ availableModels: ["m1"], permissions: { deny: ["Bash(rm *)"] } }) });
    const r = await admin.admin.setPermissionModePolicy({
      policy: { disabledModes: [PermissionMode.BYPASS_PERMISSIONS, PermissionMode.DONT_ASK, PermissionMode.BYPASS_PERMISSIONS] },
    });
    expect(r.policy?.disabledModes).toEqual([PermissionMode.DONT_ASK, PermissionMode.BYPASS_PERMISSIONS]);
    expect((await admin.admin.getPermissionModePolicy({})).policy?.disabledModes).toEqual([PermissionMode.DONT_ASK, PermissionMode.BYPASS_PERMISSIONS]);

    const doc = (await user.sync.getDocument({ selector: { case: "ref", value: POLICY_REF } })).document!;
    expect(JSON.parse(doc.content)).toEqual({
      availableModels: ["m1"],
      permissions: { deny: ["Bash(rm *)"], disabledModes: ["dontAsk", "bypassPermissions"] },
    });
    const feed = await user.sync.listChanges({ sinceVersion: 0n, kinds: [DocumentKind.POLICY] });
    expect(feed.changes.some((c) => c.change.case === "document" && c.change.value.key === "default")).toBe(true);

    // Clearing the list removes the key again.
    await admin.admin.setPermissionModePolicy({ policy: { disabledModes: [] } });
    const cleared = (await user.sync.getDocument({ selector: { case: "ref", value: POLICY_REF } })).document!;
    expect(JSON.parse(cleared.content)).toEqual({ availableModels: ["m1"], permissions: { deny: ["Bash(rm *)"] } });
  });

  it("reads the older switches as disabled modes and drops them when the mode is enabled", async () => {
    await admin.sync.putDocument({
      ref: POLICY_REF,
      content: JSON.stringify({ disableAutoMode: "disable", permissions: { disableBypassPermissionsMode: "disable" } }),
    });
    expect((await admin.admin.getPermissionModePolicy({})).policy?.disabledModes).toEqual([PermissionMode.AUTO, PermissionMode.BYPASS_PERMISSIONS]);
    await admin.admin.setPermissionModePolicy({ policy: { disabledModes: [PermissionMode.BYPASS_PERMISSIONS] } });
    const doc = (await admin.sync.getDocument({ selector: { case: "ref", value: POLICY_REF } })).document!;
    expect(JSON.parse(doc.content)).toEqual({ permissions: { disableBypassPermissionsMode: "disable", disabledModes: ["bypassPermissions"] } });
  });
});

describe("helpers", () => {
  it("parses and rewrites policy content", () => {
    expect(disabledModesOf({ permissions: { disabledModes: ["default", "nope", "plan", 3] } })).toEqual(["plan"]);
    expect(disabledModesOf({ permissions: { disableAutoMode: true } })).toEqual(["auto"]);
    expect(withDisabledModes({ x: 1, permissions: { disabledModes: ["plan"] } }, [])).toEqual({ x: 1 });
  });
});
