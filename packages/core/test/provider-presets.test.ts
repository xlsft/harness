/**
 * Provider presets and groups (ProviderService.ListPresets, CreateProvider with preset_id / group,
 * TestProvider, the classifier group): a preset needs only its fields, custom providers may be many,
 * classifier providers are global only and feed the decision chain, and rows created before presets
 * existed are matched to a preset by base URL.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ProviderScope } from "../src/gen/harness/llm/v1/providers_pb.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

// Every hostname resolves to a public address, so presets on real hosts can be created offline. Nothing is
// sent there: the tests only talk to the local mock upstream (private network, global providers).
vi.mock("node:dns", async (orig) => {
  const dns = await orig<typeof import("node:dns")>();
  const lookup = (_host: string, opts: unknown, cb?: (...a: unknown[]) => void) => {
    const done = (typeof opts === "function" ? opts : cb) as (...a: unknown[]) => void;
    done(null, [{ address: "1.1.1.1", family: 4 }]);
  };
  return { ...dns, default: { ...dns, lookup }, lookup };
});

// Fake keys, never real.
const KEY = "sk-preset-fake-key-0000000000";
const USER_KEY = "sk-user-fake-key-11111111111";

let ts: TestServer;
let up: MockUpstream;
let admin: ReturnType<TestServer["as"]>;
let adminToken: string;
let alice: ReturnType<TestServer["as"]>;

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
}

beforeAll(async () => {
  up = await new MockUpstream().start();
  ts = await startTestServer();
  const a = await deviceLogin(ts);
  adminToken = a.accessToken;
  admin = ts.as(adminToken);
  alice = ts.as((await createUserAndLogin(ts, a.cookie, "alice@test.local")).accessToken);
});

afterAll(async () => {
  await ts.close();
  await up.stop();
});

beforeEach(() => up.reset());

describe("ListPresets", () => {
  it("serves LLM and classifier presets with what the user must enter", async () => {
    const r = await alice.providers.listPresets({});
    const ids = r.presets.map((p) => p.id);
    expect(ids).toEqual(expect.arrayContaining(["openrouter", "anthropic", "openai", "kimi-code", "moonshot", "moonshot-cn", "zai", "deepseek", "gemini", "xai", "ollama"]));
    expect(ids).toEqual(expect.arrayContaining(["openrouter-decisions", "typesafe"]));
    // LLM presets first, then classifiers.
    const groups = r.presets.map((p) => p.group);
    expect(groups.lastIndexOf("llm")).toBeLessThan(groups.indexOf("classifier"));

    const kimi = r.presets.find((p) => p.id === "kimi-code")!;
    expect(kimi).toMatchObject({ type: "anthropic", baseUrl: "https://api.kimi.com/coding", discoversModels: false });
    expect(kimi.fields.map((f) => f.name)).toEqual(["api_key"]);
    expect(kimi.defaultModels.map((m) => m.upstreamModel)).toContain("kimi-for-coding");

    const ollama = r.presets.find((p) => p.id === "ollama")!;
    expect(ollama).toMatchObject({ local: true, discoversModels: true });
    expect(ollama.fields.find((f) => f.name === "base_url")?.required).toBe(true);
    expect(ollama.fields.find((f) => f.name === "api_key")?.required).toBe(false);

    expect(r.protocols.map((p) => `${p.group}:${p.id}`)).toEqual(
      expect.arrayContaining(["llm:anthropic", "llm:openai-chat", "llm:openai-responses", "classifier:decision-model", "classifier:decision-http"]),
    );
  });
});

describe("connecting a preset", () => {
  it("needs only the key; type, name, URL and a global id come from the preset", async () => {
    const r = await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, presetId: "deepseek", apiKey: KEY });
    expect(r.provider).toMatchObject({
      id: "deepseek",
      scope: ProviderScope.GLOBAL,
      presetId: "deepseek",
      group: "llm",
      type: "openai-chat",
      displayName: "DeepSeek",
      baseUrl: "https://api.deepseek.com",
      apiKeySet: true,
    });
    expect(JSON.stringify(r.provider)).not.toContain(KEY);
    // A second connection of the same preset gets the next free id.
    const again = await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, presetId: "deepseek", apiKey: KEY });
    expect(again.provider!.id).toBe("deepseek-2");
  });

  it("refuses a preset without its required key and ignores a URL the preset does not ask for", async () => {
    expect(await code(alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "anthropic" }))).toBe(Code.InvalidArgument);
    expect(await code(alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "nope", apiKey: USER_KEY }))).toBe(Code.InvalidArgument);
    const r = await alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "kimi-code", apiKey: USER_KEY, baseUrl: "https://evil.example" });
    expect(r.provider).toMatchObject({ scope: ProviderScope.PERSONAL, presetId: "kimi-code", baseUrl: "https://api.kimi.com/coding", type: "anthropic" });
    expect(r.provider!.id).toMatch(/^u-/);
  });

  it("offers the curated models of a preset without a model list, without calling the service", async () => {
    const p = (await alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "moonshot", apiKey: USER_KEY })).provider!;
    const d = await alice.providers.discoverModels({ providerId: p.id });
    expect(d.models.map((m) => m.upstreamModel)).toEqual(expect.arrayContaining(["kimi-k3.1-pro", "kimi-k3"]));
    expect(d.models.find((m) => m.upstreamModel === "kimi-k3.1-pro")?.contextWindow).toBe(1_000_000n);
    await alice.providers.addModel({ providerId: p.id, upstreamModel: "kimi-k3.1-pro" });
    const again = await alice.providers.discoverModels({ providerId: p.id });
    expect(again.models.find((m) => m.upstreamModel === "kimi-k3.1-pro")?.modelIds).toEqual([`${p.id}/kimi-k3.1-pro`]);
    expect(up.requests).toHaveLength(0);
  });

  it("connects a local preset globally with private network access, and tests it", async () => {
    // Personal providers run on the server too, so they cannot reach a private address.
    expect(await code(alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "ollama", baseUrl: `${up.url}/v1` }))).toBe(Code.PermissionDenied);
    const p = (await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, presetId: "ollama", baseUrl: `${up.url}/v1` })).provider!;
    expect(p).toMatchObject({ id: "ollama", baseUrl: `${up.url}/v1`, allowPrivateNetwork: true, apiKeySet: false });

    up.enqueue({ json: { data: [{ id: "qwen3" }, { id: "llama4" }] } });
    expect(await admin.providers.testProvider({ providerId: "ollama" })).toMatchObject({ ok: true, modelCount: 2 });
    expect(up.requests[0]!.url).toBe("/v1/models");
    up.enqueue({ status: 401, json: { error: { message: "bad key" } } });
    expect(await admin.providers.testProvider({ providerId: "ollama" })).toMatchObject({ ok: false, message: expect.stringContaining("401") });
    up.enqueue({ status: 404, json: {} });
    expect(await admin.providers.testProvider({ providerId: "ollama" })).toMatchObject({ ok: true, modelCount: 0 });
    // Testing a global provider is an admin action.
    expect(await code(alice.providers.testProvider({ providerId: "ollama" }))).toBe(Code.PermissionDenied);
  });
});

describe("custom providers", () => {
  it("may be many, each with every field set by the user", async () => {
    const a = await alice.providers.createProvider({ scope: ProviderScope.PERSONAL, type: "openai-chat", displayName: "Router A", baseUrl: "https://a.example/v1", apiKey: USER_KEY });
    const b = await alice.providers.createProvider({
      scope: ProviderScope.PERSONAL,
      type: "anthropic",
      displayName: "Proxy B",
      baseUrl: "https://b.example",
      apiKey: USER_KEY,
      headers: { "X-Team": "core" },
    });
    for (const r of [a, b]) expect(r.provider).toMatchObject({ presetId: "", group: "llm" });
    expect(b.provider!.headerNames).toEqual(["X-Team"]);
    const g = await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, type: "openai-responses", displayName: "Team Gateway", baseUrl: "https://gw.example/v1" });
    expect(g.provider!.id).toBe("team-gateway");
    expect(await code(alice.providers.createProvider({ scope: ProviderScope.PERSONAL, type: "decision-model", baseUrl: "https://c.example" }))).toBe(Code.InvalidArgument);
  });
});

describe("rows created before presets", () => {
  it("map to the preset with the same type and base URL, else stay custom", async () => {
    const { getDb, schema } = await import("../src/db/index.ts");
    const now = new Date();
    await getDb()
      .insert(schema.provider)
      .values([
        { id: "legacy-router", type: "openai-chat", baseUrl: "https://openrouter.ai/api/v1/", createdAt: now, updatedAt: now },
        { id: "legacy-vllm", type: "openai-chat", baseUrl: "https://vllm.internal.example/v1", createdAt: now, updatedAt: now },
      ]);
    const list = (await admin.providers.listProviders({})).providers;
    expect(list.find((p) => p.id === "legacy-router")).toMatchObject({ presetId: "openrouter", group: "llm" });
    expect(list.find((p) => p.id === "legacy-vllm")).toMatchObject({ presetId: "", group: "llm" });
  });
});

describe("classifier providers", () => {
  it("are global only and serve no chat models", async () => {
    expect(await code(alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "typesafe", apiKey: USER_KEY }))).toBe(Code.InvalidArgument);
    const p = (await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, presetId: "typesafe", apiKey: KEY })).provider!;
    expect(p).toMatchObject({ id: "typesafe", group: "classifier", type: "decision-model", baseUrl: "https://api.typesafe.ai/v1/systemone" });
    expect(await code(admin.providers.addModel({ providerId: "typesafe", upstreamModel: "jev-1.13.0" }))).toBe(Code.InvalidArgument);
    expect((await admin.providers.discoverModels({ providerId: "typesafe" })).models.map((m) => m.upstreamModel)).toEqual(["jev-1.13.0"]);
  });

  it("drive the classifier from a preset with only a key", async () => {
    await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, presetId: "openrouter-decisions", apiKey: KEY });
    const set = await admin.providers.setClassifier({ classifier: { enabled: true, providerId: "openrouter-decisions" } });
    expect(set.classifier).toMatchObject({ enabled: true, providerId: "openrouter-decisions", endpoint: "openrouter", model: "typesafe/jev-1.13" });
    const chain = await adminApi(ts.url, adminToken, "GET", "/api/admin/decision");
    expect(chain.body.config.providers[1]).toMatchObject({ type: "decision_model", settings: { endpoint: "openrouter", api_key_provider: "openrouter-decisions" } });
    expect(JSON.stringify(chain.body)).not.toContain(KEY);
  });

  it("test a custom decision model with one sample decision", async () => {
    await admin.providers.createProvider({
      scope: ProviderScope.GLOBAL,
      group: "classifier",
      type: "decision-model",
      displayName: "Local Jev",
      baseUrl: `${up.url}/decide`,
      apiKey: KEY,
      allowPrivateNetwork: true,
    });
    await admin.providers.setClassifier({ classifier: { enabled: true, providerId: "local-jev", model: "jev-local" } });
    up.enqueue({ json: { model: "jev-local", answers: { decision: { type: "choice", choice: "allow", probabilities: { allow: 0.9, ask: 0.05, deny: 0.05 } } } } });
    const r = await admin.providers.testProvider({ providerId: "local-jev" });
    expect(r).toMatchObject({ ok: true, message: expect.stringContaining("allow") });
    expect(up.requests[0]).toMatchObject({ url: "/decide", body: { model: "jev-local" } });
    expect(up.requests[0]!.headers.authorization).toBe(`Bearer ${KEY}`);
  });

  it("use a custom HTTP policy webhook with the provider's key and headers", async () => {
    await admin.providers.createProvider({
      scope: ProviderScope.GLOBAL,
      group: "classifier",
      type: "decision-http",
      displayName: "Policy",
      baseUrl: `${up.url}/policy`,
      apiKey: KEY,
      headers: { "X-Tenant": "t1" },
      allowPrivateNetwork: true,
    });
    const set = await admin.providers.setClassifier({ classifier: { enabled: true, providerId: "policy" } });
    expect(set.classifier).toMatchObject({ enabled: true, providerId: "policy" });
    const chain = await adminApi(ts.url, adminToken, "GET", "/api/admin/decision");
    const types = chain.body.config.providers.map((p: { type: string }) => p.type);
    expect(types).toEqual(["rules", "http", "llm"]);
    expect(chain.body.config.providers[1].settings).toMatchObject({ url: `${up.url}/policy`, credentials_provider: "policy" });

    up.enqueue({ json: { decision: "allow", reason: "fine" } });
    expect(await admin.providers.testProvider({ providerId: "policy" })).toMatchObject({ ok: true });
    expect(up.requests[0]!.headers.authorization).toBe(`Bearer ${KEY}`);
    expect(up.requests[0]!.headers["x-tenant"]).toBe("t1");

    // Switching back to a decision model replaces the managed step.
    await admin.providers.setClassifier({ classifier: { enabled: true, providerId: "typesafe" } });
    const after = await adminApi(ts.url, adminToken, "GET", "/api/admin/decision");
    expect(after.body.config.providers.map((p: { type: string }) => p.type)).toEqual(["rules", "decision_model", "llm"]);
  });
});
