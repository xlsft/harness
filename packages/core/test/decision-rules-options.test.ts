/**
 * DecisionService.GetRulesOptions: the admin's rule options and rules_version reach clients (so their
 * local copy of the rules applies the same extra denies), and DecideResponse.rules_version matches.
 */
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { invalidateDecisionConfig } from "../src/decision/config.ts";
import { BUILTIN_RULES_VERSION } from "../src/decision/rules.ts";
import { rulesVersion } from "../src/decision/service.ts";
import { Decision, DecisionService, RiskCategory } from "../src/gen/harness/decision/v1/decision_pb.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let adminToken: string;
let userToken: string;

function decisionClient(accessToken?: string) {
  const transport = createConnectTransport({
    baseUrl: ts.url,
    httpVersion: "1.1",
    interceptors: accessToken
      ? [
          (next) => async (req) => {
            req.header.set("authorization", `Bearer ${accessToken}`);
            return next(req);
          },
        ]
      : [],
  });
  return createClient(DecisionService, transport);
}

const bash = (command: string) => ({ toolName: "Bash", inputJson: JSON.stringify({ command }), cwd: "/work/proj" });

async function putRules(rules: unknown) {
  const r = await adminApi(ts.url, adminToken, "PUT", "/api/admin/decision", { providers: [{ type: "rules" }], rules });
  expect(r.status, JSON.stringify(r.body)).toBe(200);
  invalidateDecisionConfig();
}

beforeAll(async () => {
  ts = await startTestServer();
  const admin = await deviceLogin(ts);
  adminToken = admin.accessToken;
  userToken = (await createUserAndLogin(ts, admin.cookie, "rules-reader@test.local")).accessToken;
});

afterAll(async () => {
  await ts.close();
});

describe("GetRulesOptions", () => {
  it("serves the default options and a version built on the rule set version", async () => {
    const r = await decisionClient(userToken).getRulesOptions({});
    expect(r.options).toMatchObject({ extraDeny: [], extraAllow: [], protectedBranches: ["main", "master", "release*"], denyAllForcePush: false });
    expect(r.rulesVersion).toMatch(new RegExp(`^${BUILTIN_RULES_VERSION}\\.[0-9a-f]{16}$`));
    expect(r.rulesVersion).toBe(rulesVersion(undefined));
  });

  it("requires authentication", async () => {
    const err = await decisionClient()
      .getRulesOptions({})
      .catch((e) => e as ConnectError);
    expect(err).toBeInstanceOf(ConnectError);
    expect((err as ConnectError).code).toBe(Code.Unauthenticated);
  });

  it("returns the admin's options to any user, with a version that follows them", async () => {
    const before = (await decisionClient(userToken).getRulesOptions({})).rulesVersion;
    const rules = {
      extra_deny: [{ pattern: "\\bnpm publish\\b", reason: "Publishing is done by CI", categories: ["production_change"] }, { pattern: "^terraform " }],
      extra_allow: ["^make deploy-docs$"],
      protected_branches: ["main", "prod/*"],
      deny_all_force_push: true,
    };
    await putRules(rules);
    const r = await decisionClient(userToken).getRulesOptions({});
    expect(r.options?.extraDeny.map((d) => ({ pattern: d.pattern, reason: d.reason, categories: d.categories }))).toEqual([
      { pattern: "\\bnpm publish\\b", reason: "Publishing is done by CI", categories: [RiskCategory.PRODUCTION_CHANGE] },
      { pattern: "^terraform ", reason: "", categories: [] },
    ]);
    expect(r.options?.extraAllow.map((a) => a.pattern)).toEqual(["^make deploy-docs$"]);
    expect(r.options?.protectedBranches).toEqual(["main", "prod/*"]);
    expect(r.options?.denyAllForcePush).toBe(true);
    expect(r.rulesVersion).not.toBe(before);

    // Stable: the same options give the same version.
    await putRules(rules);
    expect((await decisionClient(adminToken).getRulesOptions({})).rulesVersion).toBe(r.rulesVersion);

    // Decide reports the version it used, and applies the extra deny.
    const d = await decisionClient(userToken).decide(bash("npm publish --access public"));
    expect(d).toMatchObject({ decision: Decision.DENY, reason: "Publishing is done by CI", provider: "rules", rulesVersion: r.rulesVersion });
    const allowed = await decisionClient(userToken).decide(bash("make deploy-docs"));
    expect(allowed).toMatchObject({ decision: Decision.ALLOW, rulesVersion: r.rulesVersion });

    await putRules({});
    const reset = await decisionClient(userToken).getRulesOptions({});
    expect(reset.rulesVersion).toBe(before);
    expect((await decisionClient(userToken).decide(bash("ls"))).rulesVersion).toBe(before);
  });
});
