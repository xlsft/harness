/**
 * Per-user classifier overrides (ProviderService.GetMyClassifier / SetMyClassifier): an admin sets the server
 * default (SetClassifier), each user may pick another global classifier provider or none, and another global
 * model for the model classifier. An override changes only that user's chain; following the default falls
 * back to the admin's choice; ids outside the global classifier providers and visible global models are
 * refused; the admin default stays as it was.
 */
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { Code, ConnectError, createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { clearClassifierOverrideCache } from "../src/decision/classifier.ts";
import { Decision, DecisionService } from "../src/gen/harness/decision/v1/decision_pb.ts";
import { ClassifierChoice, ProviderScope } from "../src/gen/harness/llm/v1/providers_pb.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

// Fake keys, never real.
const KEY = "sk-policy-fake-key-0000000000";
// A public IP literal: passes the SSRF check without DNS; nothing is ever sent there.
const PUBLIC_URL = "https://1.1.1.1";

let ts: TestServer;
let hook: Server;
let hookUrl = "";
let seen: string[] = [];
let adminToken: string;
let admin: ReturnType<TestServer["as"]>;
let alice: ReturnType<TestServer["as"]>;
let bob: ReturnType<TestServer["as"]>;
let aliceDec: Client<typeof DecisionService>;
let bobDec: Client<typeof DecisionService>;
let alicePersonalModel = "";

function decisionClient(token: string) {
  const transport = createConnectTransport({
    baseUrl: ts.url,
    httpVersion: "1.1",
    interceptors: [
      (next) => async (req) => {
        req.header.set("authorization", `Bearer ${token}`);
        return next(req);
      },
    ],
  });
  return createClient(DecisionService, transport);
}

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
}

// A command the rules leave to the classifiers.
const npm = () => ({ toolName: "Bash", inputJson: JSON.stringify({ command: "npm install left-pad" }), cwd: "/work", recentUserMessages: ["add left-pad"] });

const chainOf = async (dec: Client<typeof DecisionService>) => (await dec.listProviders({})).providers.map((p) => (p.type === "llm" ? `llm=${p.model}` : p.id));

beforeAll(async () => {
  // Two policy webhooks on one server: /a allows, /b denies.
  hook = createServer(async (req, res) => {
    for await (const _ of req);
    seen.push(req.url ?? "");
    const decision = req.url?.startsWith("/a") ? "allow" : "deny";
    res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ decision, reason: `policy ${req.url}` }));
  });
  await new Promise<void>((r) => hook.listen(0, "127.0.0.1", r));
  hookUrl = `http://127.0.0.1:${(hook.address() as AddressInfo).port}`;

  ts = await startTestServer();
  const a = await deviceLogin(ts);
  adminToken = a.accessToken;
  admin = ts.as(adminToken);
  const aliceToken = (await createUserAndLogin(ts, a.cookie, "alice@test.local")).accessToken;
  const bobToken = (await createUserAndLogin(ts, a.cookie, "bob@test.local")).accessToken;
  alice = ts.as(aliceToken);
  bob = ts.as(bobToken);
  aliceDec = decisionClient(aliceToken);
  bobDec = decisionClient(bobToken);

  for (const id of ["a", "b"]) {
    await admin.providers.createProvider({
      scope: ProviderScope.GLOBAL,
      id: `policy-${id}`,
      group: "classifier",
      type: "decision-http",
      displayName: `Policy ${id.toUpperCase()}`,
      baseUrl: `${hookUrl}/${id}`,
      apiKey: KEY,
      allowPrivateNetwork: true,
    });
  }
  // Chat models for the model classifier: two for everyone, one for admins only.
  await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, id: "chat", type: "anthropic", baseUrl: "http://127.0.0.1:9", apiKey: KEY, allowPrivateNetwork: true });
  await admin.providers.addModel({ providerId: "chat", upstreamModel: "clf-a-up", id: "clf-a" });
  await admin.providers.addModel({ providerId: "chat", upstreamModel: "clf-b-up", id: "clf-b" });
  await admin.providers.addModel({ providerId: "chat", upstreamModel: "staff-up", id: "staff-only" });
  expect((await adminApi(ts.url, adminToken, "PATCH", "/api/admin/models/staff-only", { roles: ["admin"] })).status).toBe(200);
  // A personal model of Alice's: never a classifier, not even for her.
  const mine = (await alice.providers.createProvider({ scope: ProviderScope.PERSONAL, type: "anthropic", baseUrl: PUBLIC_URL, apiKey: KEY })).provider!;
  await alice.providers.addModel({ providerId: mine.id, upstreamModel: "own" });
  alicePersonalModel = `${mine.id}/own`;

  await admin.providers.setClassifier({ classifier: { enabled: true, providerId: "policy-a", llmModel: "clf-a" } });
});

afterAll(async () => {
  await ts.close();
  hook.closeAllConnections();
  await new Promise<void>((r) => hook.close(() => r()));
});

beforeEach(() => {
  seen = [];
});

describe("my classifier", () => {
  it("follows the server default until the user overrides it, and shows the default to everyone", async () => {
    const r = await alice.providers.getMyClassifier({});
    expect(r.mine).toMatchObject({ choice: ClassifierChoice.UNSPECIFIED, providerId: "", llmModel: "" });
    expect(r).toMatchObject({ defaultProviderId: "policy-a", defaultLlmModel: "clf-a" });
    expect(await chainOf(aliceDec)).toEqual(["rules", "http:classifier", "llm=clf-a"]);
  });

  it("applies an override to that user only", async () => {
    const set = await alice.providers.setMyClassifier({ mine: { choice: ClassifierChoice.PROVIDER, providerId: "policy-b", llmModel: "clf-b" } });
    expect(set.mine).toMatchObject({ choice: ClassifierChoice.PROVIDER, providerId: "policy-b", llmModel: "clf-b" });
    expect(set.defaultProviderId).toBe("policy-a");

    expect(await chainOf(aliceDec)).toEqual(["rules", "http:policy-b", "llm=clf-b"]);
    expect(await chainOf(bobDec)).toEqual(["rules", "http:classifier", "llm=clf-a"]);

    const mineDecision = await aliceDec.decide(npm());
    expect(mineDecision).toMatchObject({ decision: Decision.DENY, provider: "http:policy-b" });
    const bobs = await bobDec.decide(npm());
    expect(bobs).toMatchObject({ decision: Decision.ALLOW, provider: "http:classifier" });
    expect(seen).toEqual(["/b", "/a"]);

    // The admin default is untouched.
    const def = await admin.providers.getClassifier({});
    expect(def.classifier).toMatchObject({ enabled: true, providerId: "policy-a", llmModel: "clf-a" });
    expect((await admin.providers.getMyClassifier({})).mine?.choice).toBe(ClassifierChoice.UNSPECIFIED);
  });

  it("can turn the decision step off for the user", async () => {
    await alice.providers.setMyClassifier({ mine: { choice: ClassifierChoice.NONE } });
    expect(await chainOf(aliceDec)).toEqual(["rules", "llm=clf-a"]);
    expect(await chainOf(bobDec)).toEqual(["rules", "http:classifier", "llm=clf-a"]);
  });

  it("falls back to the admin default when set back to follow it", async () => {
    await alice.providers.setMyClassifier({ mine: { choice: ClassifierChoice.PROVIDER, providerId: "policy-b" } });
    await alice.providers.setMyClassifier({ mine: {} });
    expect((await alice.providers.getMyClassifier({})).mine).toMatchObject({ choice: ClassifierChoice.UNSPECIFIED, providerId: "", llmModel: "" });
    expect(await chainOf(aliceDec)).toEqual(["rules", "http:classifier", "llm=clf-a"]);

    // A later change of the default reaches users who follow it.
    await admin.providers.setClassifier({ classifier: { enabled: true, providerId: "policy-b", llmModel: "clf-b" } });
    expect(await chainOf(aliceDec)).toEqual(["rules", "http:classifier", "llm=clf-b"]);
    expect((await aliceDec.decide(npm())).decision).toBe(Decision.DENY);
    await admin.providers.setClassifier({ classifier: { enabled: true, providerId: "policy-a", llmModel: "clf-a" } });
  });

  it("refuses ids outside the global classifier providers and the user's global models", async () => {
    const bad = [
      { choice: ClassifierChoice.PROVIDER, providerId: "missing" },
      { choice: ClassifierChoice.PROVIDER, providerId: "chat" },
      { choice: ClassifierChoice.PROVIDER, providerId: "" },
      { llmModel: "missing" },
      { llmModel: "staff-only" },
      { llmModel: alicePersonalModel },
    ];
    for (const mine of bad) expect(await code(alice.providers.setMyClassifier({ mine })), JSON.stringify(mine)).toBe(Code.InvalidArgument);
    // Admins may pick the admin-only model for themselves.
    expect((await admin.providers.setMyClassifier({ mine: { llmModel: "staff-only" } })).mine?.llmModel).toBe("staff-only");
    await admin.providers.setMyClassifier({ mine: {} });
    expect(await chainOf(aliceDec)).toEqual(["rules", "http:classifier", "llm=clf-a"]);
  });

  it("follows the default again when the chosen provider is disabled", async () => {
    await bob.providers.setMyClassifier({ mine: { choice: ClassifierChoice.PROVIDER, providerId: "policy-b" } });
    expect(await chainOf(bobDec)).toEqual(["rules", "http:policy-b", "llm=clf-a"]);
    await admin.providers.updateProvider({ id: "policy-b", enabled: false });
    clearClassifierOverrideCache();
    expect((await bob.providers.getMyClassifier({})).mine?.choice).toBe(ClassifierChoice.UNSPECIFIED);
    expect(await chainOf(bobDec)).toEqual(["rules", "http:classifier", "llm=clf-a"]);
    await admin.providers.updateProvider({ id: "policy-b", enabled: true });
  });
});
