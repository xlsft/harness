/**
 * The plugin keys of the managed policy: the form's lines become the settings format's objects in the POLICY document, which is
 * what clients read and enforce (packages/client/plugins), and come back as the same lines.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { lineToSource, sourceToLine } from "../src/services/managed-policy.ts";
import { deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let admin: ReturnType<TestServer["as"]>;

beforeAll(async () => {
  ts = await startTestServer();
  admin = ts.as((await deviceLogin(ts)).accessToken);
});

afterAll(async () => {
  await ts.close();
});

describe("source lines", () => {
  const cases: [string, Record<string, unknown>][] = [
    ["acme/plugins", { source: "github", repo: "acme/plugins" }],
    ["acme/*", { source: "github", repo: "acme/*" }],
    ["acme/plugins#v2", { source: "github", repo: "acme/plugins", ref: "v2" }],
    ["https://git.example.test/a/b.git", { source: "git", url: "https://git.example.test/a/b.git" }],
    ["git@git.example.test:a/b.git#main", { source: "git", url: "git@git.example.test:a/b.git", ref: "main" }],
    ["https://x.test/marketplace.json", { source: "url", url: "https://x.test/marketplace.json" }],
    ["/opt/plugins", { source: "directory", path: "/opt/plugins" }],
    ["/opt/plugins/marketplace.json", { source: "file", path: "/opt/plugins/marketplace.json" }],
    ["host:^git\\.corp\\.test$", { source: "hostPattern", hostPattern: "^git\\.corp\\.test$" }],
    ["path:^/opt/", { source: "pathPattern", pathPattern: "^/opt/" }],
  ];
  it("go both ways", () => {
    for (const [line, source] of cases) {
      expect(lineToSource(line)).toEqual(source);
      expect(sourceToLine(source)).toBe(line);
    }
  });
  it("refuse what is no source", () => {
    for (const line of ["nonsense", "ext::sh -c x", "host:(", "a/b/c"]) expect(() => lineToSource(line)).toThrow();
  });
  it("read what the form can't show as nothing", () => {
    expect(sourceToLine({ source: "npm", package: "x" })).toBe("");
    expect(sourceToLine("text")).toBe("");
  });
});

describe("PolicyService", () => {
  it("writes the keys clients read, and keeps the others", async () => {
    await admin.policy.setManagedPolicy({ policy: { denyRules: ["Bash(curl:*)"] } });
    const set = await admin.policy.setManagedPolicy({
      policy: {
        denyRules: ["Bash(curl:*)"],
        restrictPluginSources: true,
        allowedPluginSources: ["acme/*", "host:^git\\.corp\\.test$"],
        blockedPluginSources: ["acme/bad"],
        distributedMarketplaces: ["acme=acme/plugins"],
        distributedPlugins: ["fmt@acme"],
        blockedPlugins: ["evil@acme"],
      },
    });
    expect(set.policy?.allowedPluginSources).toEqual(["acme/*", "host:^git\\.corp\\.test$"]);
    expect(set.policy?.distributedMarketplaces).toEqual(["acme=acme/plugins"]);
    expect(set.policy?.distributedPlugins).toEqual(["fmt@acme"]);
    expect(set.policy?.blockedPlugins).toEqual(["evil@acme"]);

    const doc = await admin.sync.getDocument({ selector: { case: "ref", value: { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default" } } });
    const content = JSON.parse(doc.document!.content);
    expect(content.strictKnownMarketplaces).toEqual([
      { source: "github", repo: "acme/*" },
      { source: "hostPattern", hostPattern: "^git\\.corp\\.test$" },
    ]);
    expect(content.blockedMarketplaces).toEqual([{ source: "github", repo: "acme/bad" }]);
    expect(content.extraKnownMarketplaces).toEqual({ acme: { source: { source: "github", repo: "acme/plugins" } } });
    expect(content.enabledPlugins).toEqual({ "fmt@acme": true, "evil@acme": false });
    expect(content.permissions.deny).toEqual(["Bash(curl:*)"]);

    // An empty allowlist stays a lockdown; no list at all is no restriction.
    let got = await admin.policy.setManagedPolicy({ policy: { restrictPluginSources: true } });
    expect(got.policy?.restrictPluginSources).toBe(true);
    expect(got.policy?.allowedPluginSources).toEqual([]);
    got = await admin.policy.setManagedPolicy({ policy: {} });
    expect(got.policy?.restrictPluginSources).toBe(false);
    const cleared = JSON.parse((await admin.sync.getDocument({ selector: { case: "ref", value: { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default" } } })).document!.content);
    for (const key of ["strictKnownMarketplaces", "blockedMarketplaces", "extraKnownMarketplaces", "enabledPlugins"]) expect(cleared[key]).toBeUndefined();
  });

  it("refuses what the form can't mean", async () => {
    for (const policy of [
      { restrictPluginSources: true, allowedPluginSources: ["nonsense"] },
      { distributedMarketplaces: ["no-equals"] },
      { distributedPlugins: ["no-marketplace"] },
      { distributedPlugins: ["a@b"], blockedPlugins: ["a@b"] },
    ]) {
      await expect(admin.policy.setManagedPolicy({ policy })).rejects.toSatisfy((e: ConnectError) => e.code === Code.InvalidArgument);
    }
  });
});
