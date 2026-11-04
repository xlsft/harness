import { listedCapabilities, toCandidate } from "../src/services/discovery.ts";
/**
 * ProviderService (global and personal providers) and its authorization: a user never sees, changes or
 * uses another user's providers or keys; admins see a user's providers with key_set only and cannot use
 * them; keys are encrypted at rest with a per-user data key and never returned or echoed.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { Effort, Role } from "../src/gen/harness/llm/v1/content_pb.ts";
import { ProviderScope } from "../src/gen/harness/llm/v1/providers_pb.ts";
import { anthropicThinkingText } from "./fixtures/upstream.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

// Fake keys: long enough to be redacted, never real.
const ALICE_KEY = "sk-alice-fake-key-000000000000";
const BOB_KEY = "sk-bob-fake-key-11111111111111";
const GLOBAL_KEY = "sk-global-fake-key-2222222222";
// A public IP literal: passes the SSRF check without DNS; nothing is ever sent there.
const PUBLIC_URL = "https://1.1.1.1";

let ts: TestServer;
let up: MockUpstream;
let admin: ReturnType<TestServer["as"]>;
let adminToken: string;
let alice: ReturnType<TestServer["as"]>;
let bob: ReturnType<TestServer["as"]>;
let aliceId: string;
let aliceProvider: string;

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
}

const userText = (text: string) => ({ role: Role.USER, content: [{ block: { case: "text" as const, value: { text } } }] });

async function stream(client: ReturnType<TestServer["as"]>, model: string) {
  const events = [];
  try {
    for await (const r of client.gateway.streamMessage({ model, maxTokens: 100, messages: [userText("hi")] })) events.push(r.event);
  } catch (e) {
    return { events, error: e as ConnectError };
  }
  return { events };
}

/** Points a personal provider at the local mock upstream (a user cannot do this: private network). */
async function pointAtMock(id: string) {
  const { getDb, schema } = await import("../src/db/index.ts");
  await getDb().update(schema.provider).set({ baseUrl: up.url, allowPrivateNetwork: true }).where(eq(schema.provider.id, id));
}

beforeAll(async () => {
  up = await new MockUpstream().start();
  ts = await startTestServer();
  const a = await deviceLogin(ts);
  adminToken = a.accessToken;
  admin = ts.as(adminToken);
  alice = ts.as((await createUserAndLogin(ts, a.cookie, "alice@test.local")).accessToken);
  bob = ts.as((await createUserAndLogin(ts, a.cookie, "bob@test.local")).accessToken);
  aliceId = (await alice.auth.getMe({})).user!.id;

  const created = await alice.providers.createProvider({
    scope: ProviderScope.PERSONAL,
    type: "anthropic",
    displayName: "My Anthropic",
    baseUrl: PUBLIC_URL,
    apiKey: ALICE_KEY,
  });
  aliceProvider = created.provider!.id;
});

afterAll(async () => {
  await ts.close();
  await up.stop();
});

beforeEach(() => up.reset());

describe("personal providers", () => {
  it("get a generated id and never return the key", async () => {
    const p = (await alice.providers.listProviders({})).providers.find((x) => x.id === aliceProvider)!;
    expect(aliceProvider).toMatch(/^u-[a-z0-9]{10}$/);
    expect(p).toMatchObject({ scope: ProviderScope.PERSONAL, apiKeySet: true, apiKeyHint: "…0000", editable: true, displayName: "My Anthropic" });
    expect(JSON.stringify(p)).not.toContain(ALICE_KEY);
  });

  it("are invisible and unchangeable for other users (not found, not forbidden)", async () => {
    expect((await bob.providers.listProviders({})).providers.some((p) => p.id === aliceProvider)).toBe(false);
    expect(await code(bob.providers.updateProvider({ id: aliceProvider, displayName: "x" }))).toBe(Code.NotFound);
    expect(await code(bob.providers.deleteProvider({ id: aliceProvider }))).toBe(Code.NotFound);
    expect(await code(bob.providers.discoverModels({ providerId: aliceProvider }))).toBe(Code.NotFound);
    expect(await code(bob.providers.addModel({ providerId: aliceProvider, upstreamModel: "m" }))).toBe(Code.NotFound);
  });

  it("refuse private networks and caller-chosen ids", async () => {
    expect(await code(bob.providers.createProvider({ scope: ProviderScope.PERSONAL, type: "anthropic", baseUrl: up.url, apiKey: BOB_KEY }))).toBe(Code.PermissionDenied);
    expect(
      await code(bob.providers.createProvider({ scope: ProviderScope.PERSONAL, type: "anthropic", baseUrl: PUBLIC_URL, allowPrivateNetwork: true })),
    ).toBe(Code.InvalidArgument);
    expect(await code(bob.providers.createProvider({ scope: ProviderScope.PERSONAL, id: "mine", type: "anthropic", baseUrl: PUBLIC_URL }))).toBe(Code.InvalidArgument);
    expect(await code(alice.providers.updateProvider({ id: aliceProvider, allowPrivateNetwork: true }))).toBe(Code.InvalidArgument);
  });

  it("store only ciphertext, bound to the owner", async () => {
    const { getDb, schema } = await import("../src/db/index.ts");
    const { decryptSecret } = await import("../src/lib/crypto.ts");
    const row = (await getDb().select().from(schema.provider).where(eq(schema.provider.id, aliceProvider)))[0]!;
    expect(row.ownerUserId).toBe(aliceId);
    expect(JSON.stringify(row)).not.toContain(ALICE_KEY);
    // Not decryptable with the server-wide provider key: it needs the owner's data key.
    expect(() => decryptSecret(row.apiKeyEnc!, `${aliceProvider}:api_key`)).toThrow();
  });

  it("offer their models to the owner only", async () => {
    const added = await alice.providers.addModel({ providerId: aliceProvider, upstreamModel: "claude-up", displayName: "Alice Claude", contextWindow: 200000n });
    const id = added.model!.id;
    expect(id).toBe(`${aliceProvider}/claude-up`);
    expect((await alice.gateway.listModels({})).models.map((m) => m.id)).toContain(id);
    expect((await bob.gateway.listModels({})).models.map((m) => m.id)).not.toContain(id);
    expect((await admin.gateway.listModels({})).models.map((m) => m.id)).not.toContain(id);

    // Another user (admins included) cannot use it: the same answer as for a missing model, no upstream call.
    for (const c of [bob, admin]) {
      const r = await stream(c, id);
      expect(r.error?.code).toBe(Code.NotFound);
    }
    expect(up.requests).toHaveLength(0);

    // The owner can, with their key.
    await pointAtMock(aliceProvider);
    up.enqueue({ sse: anthropicThinkingText });
    const r = await stream(alice, id);
    expect(r.error).toBeUndefined();
    expect(up.requests[0]!.headers["x-api-key"]).toBe(ALICE_KEY);
  });

  it("redact the key when the upstream echoes it in an error", async () => {
    up.enqueue({ status: 400, json: { type: "error", error: { type: "invalid_request_error", message: `bad key ${ALICE_KEY}` } } });
    const r = await stream(alice, `${aliceProvider}/claude-up`);
    expect(r.error).toBeDefined();
    expect(r.error!.message).not.toContain(ALICE_KEY);
    expect(r.error!.message).toContain("[redacted]");
  });

  it("take their models with them on delete", async () => {
    const p = await bob.providers.createProvider({ scope: ProviderScope.PERSONAL, type: "openai-chat", baseUrl: `${PUBLIC_URL}/v1`, apiKey: BOB_KEY });
    const id = p.provider!.id;
    await bob.providers.addModel({ providerId: id, upstreamModel: "gpt-x" });
    await bob.providers.deleteProvider({ id });
    expect((await bob.gateway.listModels({})).models.map((m) => m.id)).not.toContain(`${id}/gpt-x`);
  });
});

describe("global providers", () => {
  it("need the admin role to create or change", async () => {
    expect(await code(alice.providers.createProvider({ scope: ProviderScope.GLOBAL, id: "shared", type: "anthropic", baseUrl: PUBLIC_URL }))).toBe(Code.PermissionDenied);
    const g = await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, id: "shared", type: "anthropic", baseUrl: up.url, apiKey: GLOBAL_KEY, allowPrivateNetwork: true });
    expect(g.provider).toMatchObject({ id: "shared", scope: ProviderScope.GLOBAL, apiKeySet: true });
    await admin.providers.addModel({ providerId: "shared", upstreamModel: "claude-up", id: "shared-claude" });

    const seen = (await alice.providers.listProviders({})).providers.find((p) => p.id === "shared")!;
    expect(seen).toMatchObject({ editable: false });
    expect(seen.models.map((m) => m.id)).toEqual(["shared-claude"]);
    expect(await code(alice.providers.updateProvider({ id: "shared", displayName: "x" }))).toBe(Code.PermissionDenied);
    expect(await code(alice.providers.removeModel({ modelId: "shared-claude" }))).toBe(Code.PermissionDenied);
    expect(await code(alice.providers.discoverModels({ providerId: "shared" }))).toBe(Code.PermissionDenied);
    expect((await alice.gateway.listModels({})).models.map((m) => m.id)).toContain("shared-claude");
  });

  it("reserve the personal id prefix", async () => {
    expect(await code(admin.providers.createProvider({ scope: ProviderScope.GLOBAL, id: "u-abc", type: "anthropic", baseUrl: PUBLIC_URL }))).toBe(Code.InvalidArgument);
  });
});

describe("admin API", () => {
  const api = (method: string, path: string, body?: unknown) => adminApi(ts.url, adminToken, method, path, body);

  it("lists and changes global providers only", async () => {
    const list = await api("GET", "/api/admin/providers");
    expect(list.body.providers.map((p: { id: string }) => p.id)).not.toContain(aliceProvider);
    expect((await api("GET", `/api/admin/providers/${aliceProvider}`)).status).toBe(404);
    expect((await api("PATCH", `/api/admin/providers/${aliceProvider}`, { api_key: "x" })).status).toBe(404);
    expect((await api("POST", `/api/admin/providers/${aliceProvider}/discover`)).status).toBe(404);
    // A global model cannot be attached to a personal provider (no reuse of a user's key).
    expect((await api("POST", "/api/admin/models", { id: "stolen", provider_id: aliceProvider, upstream_model: "x" })).status).toBe(400);
    expect((await api("GET", `/api/admin/models/${aliceProvider}/claude-up`)).status).toBe(404);
    expect((await api("PUT", "/api/admin/slots", { main: `${aliceProvider}/claude-up` })).status).toBe(400);
  });

  it("shows a user's providers with key_set only and can delete them", async () => {
    const p = await alice.providers.createProvider({ scope: ProviderScope.PERSONAL, type: "anthropic", baseUrl: PUBLIC_URL, apiKey: ALICE_KEY });
    const r = await api("GET", `/api/admin/users/${aliceId}/providers`);
    expect(r.status).toBe(200);
    const text = JSON.stringify(r.body);
    expect(text).not.toContain(ALICE_KEY);
    expect(text).not.toContain("0000");
    const row = r.body.providers.find((x: { id: string }) => x.id === p.provider!.id);
    expect(row).toMatchObject({ api_key_set: true, scope: "personal" });
    expect(row.api_key_hint).toBeUndefined();
    expect((await api("DELETE", `/api/admin/users/${aliceId}/providers/${p.provider!.id}`)).status).toBe(200);
    expect((await alice.providers.listProviders({})).providers.some((x) => x.id === p.provider!.id)).toBe(false);
  });

  it("cannot point the decision chain at a user's key", async () => {
    const r = await api("PUT", "/api/admin/decision", {
      providers: [{ type: "rules" }, { type: "decision_model", settings: { endpoint: PUBLIC_URL, model: "m", api_key_provider: aliceProvider } }],
    });
    expect(r.status).toBe(400);
    expect(await code(admin.providers.setClassifier({ classifier: { enabled: true, providerId: aliceProvider, endpoint: PUBLIC_URL, model: "m" } }))).toBe(
      Code.InvalidArgument,
    );
  });
});

describe("classifier", () => {
  it("is admin-only and edits the decision_model step of the chain", async () => {
    expect(await code(alice.providers.getClassifier({}))).toBe(Code.PermissionDenied);
    expect(await code(alice.providers.setClassifier({ classifier: { enabled: false } }))).toBe(Code.PermissionDenied);

    await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, id: "router", type: "openai-chat", baseUrl: "https://openrouter.ai/api/v1", apiKey: GLOBAL_KEY }).catch((e) => {
      // Offline test runs cannot resolve openrouter.ai; fall back to a row written directly.
      if ((e as ConnectError).code !== Code.InvalidArgument) throw e;
    });
    const { getDb, schema } = await import("../src/db/index.ts");
    const { encryptSecret } = await import("../src/lib/crypto.ts");
    const now = new Date();
    await getDb()
      .insert(schema.provider)
      .values({ id: "router", type: "openai-chat", baseUrl: "https://openrouter.ai/api/v1", apiKeyEnc: encryptSecret(GLOBAL_KEY, "router:api_key"), createdAt: now, updatedAt: now })
      .onConflictDoNothing();

    const set = await admin.providers.setClassifier({ classifier: { enabled: true, providerId: "router", minProbability: 0.9, minMargin: 0.2, onUncertain: "ask" } });
    expect(set.classifier).toMatchObject({ enabled: true, providerId: "router", endpoint: "openrouter", minProbability: 0.9, minMargin: 0.2, onUncertain: "ask" });
    const chain = await adminApi(ts.url, adminToken, "GET", "/api/admin/decision");
    expect(chain.body.config.providers.map((p: { type: string }) => p.type)).toEqual(["rules", "decision_model", "llm"]);
    expect(JSON.stringify(chain.body)).not.toContain(GLOBAL_KEY);

    const off = await admin.providers.setClassifier({ classifier: { enabled: false } });
    expect(off.classifier?.enabled).toBe(false);
    const after = await adminApi(ts.url, adminToken, "GET", "/api/admin/decision");
    expect(after.body.config.providers.map((p: { type: string }) => p.type)).toEqual(["rules", "llm"]);
  });
});

describe("slots", () => {
  const api = (method: string, path: string, body?: unknown) => adminApi(ts.url, adminToken, method, path, body);

  beforeAll(async () => {
    await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, id: "slots-p", type: "anthropic", baseUrl: up.url, apiKey: GLOBAL_KEY, allowPrivateNetwork: true });
    await admin.providers.addModel({ providerId: "slots-p", upstreamModel: "big", id: "slot-big" });
    await admin.providers.addModel({ providerId: "slots-p", upstreamModel: "tiny", id: "slot-tiny" });
  });

  it("are readable by everyone and settable by admins only", async () => {
    expect((await alice.providers.getSlots({})).editable).toBe(false);
    expect((await admin.providers.getSlots({})).editable).toBe(true);
    expect(await code(alice.providers.setSlots({ slots: { main: "slot-big" } }))).toBe(Code.PermissionDenied);
    expect(await code(admin.providers.setSlots({}))).toBe(Code.InvalidArgument);

    const set = await admin.providers.setSlots({ slots: { main: "slot-big", small: "slot-big", title: "slot-tiny" } });
    expect(set.slots).toMatchObject({ main: "slot-big", small: "slot-big", plan: "", title: "slot-tiny" });
    expect((await alice.providers.getSlots({})).slots).toMatchObject({ main: "slot-big", small: "slot-big", plan: "", title: "slot-tiny" });
  });

  it("serve the title slot in the catalog, falling back to the small one", async () => {
    await admin.providers.setSlots({ slots: { main: "slot-big", small: "slot-big", title: "slot-tiny" } });
    expect((await alice.gateway.listModels({})).slots).toMatchObject({ main: "slot-big", small: "slot-big", title: "slot-tiny" });

    await admin.providers.setSlots({ slots: { main: "slot-tiny", small: "slot-big", title: "" } });
    expect((await admin.providers.getSlots({})).slots?.title).toBe("");
    expect((await alice.gateway.listModels({})).slots).toMatchObject({ main: "slot-tiny", small: "slot-big", title: "slot-big" });
  });

  it("accept global models only", async () => {
    expect(await code(admin.providers.setSlots({ slots: { title: `${aliceProvider}/claude-up` } }))).toBe(Code.InvalidArgument);
    expect(await code(admin.providers.setSlots({ slots: { main: "no-such-model" } }))).toBe(Code.InvalidArgument);
    expect((await api("PUT", "/api/admin/slots", { title: `${aliceProvider}/claude-up` })).status).toBe(400);
  });

  it("keep the classifier slot, which the admin API also sets", async () => {
    const put = await api("PUT", "/api/admin/slots", { small: "slot-big", title: "slot-tiny", classifier: "slot-tiny" });
    expect(put.status).toBe(200);
    expect(put.body.slots).toEqual({ small: "slot-big", title: "slot-tiny", classifier: "slot-tiny" });

    await admin.providers.setSlots({ slots: { main: "slot-big", small: "slot-tiny" } });
    expect((await api("GET", "/api/admin/slots")).body.slots).toEqual({ main: "slot-big", small: "slot-tiny", classifier: "slot-tiny" });
  });
});

describe("model list prices", () => {
  it("reads per-token prices as USD per 1M tokens", () => {
    const c = toCandidate({ id: "a/b", context_length: 1048576, pricing: { prompt: "0.0000003", completion: "0.0000025", input_cache_read: "0.00000003" } });
    expect(c?.pricing).toEqual({ input: 0.3, output: 2.5, cache_read: 0.03 });
    expect(toCandidate({ id: "free", pricing: { prompt: "0", completion: "0" } })?.pricing).toEqual({ input: 0, output: 0 });
  });
  it("leaves out what is not a price", () => {
    expect(toCandidate({ id: "router", pricing: { prompt: "-1", completion: "-1" } })?.pricing).toBeUndefined();
    expect(toCandidate({ id: "half", pricing: { prompt: "0.1" } })?.pricing).toBeUndefined();
    expect(toCandidate({ id: "none" })?.pricing).toBeUndefined();
  });
});

describe("model list capabilities", () => {
  it("reads OpenRouter's reasoning block and modalities", () => {
    expect(
      listedCapabilities({
        id: "a/b",
        supported_parameters: ["tools", "reasoning"],
        reasoning: { mandatory: false, supported_efforts: ["max", "xhigh", "high", "medium", "low", "minimal", "none"], default_effort: "medium" },
        architecture: { input_modalities: ["text", "image", "file"] },
      }),
    ).toEqual({ thinking: true, effort: ["low", "medium", "high", "xhigh", "max"], images: true, pdf: true, default_effort: "medium" });
    // No allowlist: the basic levels. An allowlist of levels harness has no name for: thinking without levels.
    expect(listedCapabilities({ id: "a", reasoning: { mandatory: false, supported_efforts: null } })).toEqual({ thinking: true, effort: ["low", "medium", "high"] });
    expect(listedCapabilities({ id: "a", reasoning: { mandatory: true, supported_efforts: ["minimal"] } })).toEqual({ thinking: true });
  });
  it("falls back to supported_parameters without the block", () => {
    for (const p of ["reasoning", "include_reasoning", "reasoning_effort"]) {
      expect(listedCapabilities({ id: "a", supported_parameters: [p] })).toEqual({ thinking: true, effort: ["low", "medium", "high"] });
    }
    expect(listedCapabilities({ id: "a", supported_parameters: ["tools"], architecture: { input_modalities: ["text"] } })).toBeUndefined();
  });
  it("reads Anthropic's capabilities object", () => {
    const yes = { supported: true };
    const no = { supported: false };
    expect(
      listedCapabilities({
        id: "claude",
        capabilities: { thinking: yes, effort: { supported: true, low: yes, medium: yes, high: yes, max: no, xhigh: null }, image_input: yes, pdf_input: no },
      }),
    ).toEqual({ thinking: true, effort: ["low", "medium", "high"], images: true });
    expect(listedCapabilities({ id: "claude", capabilities: { thinking: no, effort: { supported: false, low: no }, image_input: no, pdf_input: no } })).toBeUndefined();
    expect(listedCapabilities({ id: "claude", capabilities: null })).toBeUndefined();
  });
  it("reads the default effort only when it is one of the model's levels", () => {
    const of = (reasoning: unknown) => listedCapabilities({ id: "a", reasoning })?.default_effort;
    expect(of({ supported_efforts: ["high", "low"], default_effort: "high" })).toBe("high");
    expect(of({ supported_efforts: null, default_effort: "medium" })).toBe("medium");
    // Not offered: another level, a level harness has no name for, none at all.
    expect(of({ supported_efforts: ["high", "low"], default_effort: "medium" })).toBeUndefined();
    expect(of({ supported_efforts: null, default_effort: "max" })).toBeUndefined();
    expect(of({ supported_efforts: ["high", "minimal"], default_effort: "minimal" })).toBeUndefined();
    expect(of({ supported_efforts: ["none", "low"], default_effort: "none" })).toBeUndefined();
    expect(of({ supported_efforts: ["minimal"], default_effort: "minimal" })).toBeUndefined();
    expect(of({ supported_efforts: ["low"], default_effort: null })).toBeUndefined();
    expect(of({ supported_efforts: ["low"], default_effort: ["low"] })).toBeUndefined();
    expect(of({ supported_efforts: ["low"] })).toBeUndefined();
  });
  it("says nothing for a list that says nothing", () => {
    expect(toCandidate({ id: "gpt-x", owned_by: "openai" })?.capabilities).toBeUndefined();
  });
});

describe("capabilities of added models", () => {
  const LIST = {
    data: [
      { id: "think", reasoning: { supported_efforts: ["high", "low"] }, architecture: { input_modalities: ["text", "image"] } },
      { id: "plain", architecture: { input_modalities: ["text"] } },
      { id: "silent" },
      { id: "by-hand", reasoning: { supported_efforts: null }, architecture: { input_modalities: ["image"] } },
      { id: "cleared", reasoning: { supported_efforts: null } },
      { id: "late", reasoning: { supported_efforts: null }, architecture: { input_modalities: ["image", "file"] } },
    ],
  };
  const capsOf = async (id: string) => (await admin.gateway.listModels({})).models.find((m) => m.id === id)!.capabilities!;
  const { LOW, MEDIUM, HIGH, MAX } = Effort;

  beforeAll(async () => {
    await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, id: "caps-list", type: "openai-chat", baseUrl: up.url, apiKey: GLOBAL_KEY, allowPrivateNetwork: true });
  });

  it("come from the list when added and fill in what nobody set at discovery", async () => {
    // Added before any listing: nothing known yet, and an admin's choices.
    await admin.providers.addModel({ providerId: "caps-list", upstreamModel: "late", id: "router-late" });
    await admin.providers.addModel({ providerId: "caps-list", upstreamModel: "by-hand", id: "router-by-hand" });
    await admin.providers.setModelEfforts({ modelId: "router-by-hand", efforts: [MAX] });
    await admin.providers.addModel({ providerId: "caps-list", upstreamModel: "cleared", id: "router-cleared" });
    await admin.providers.setModelEfforts({ modelId: "router-cleared", efforts: [] });
    expect((await capsOf("router-late")).effort).toEqual([]);

    up.enqueue({ json: LIST });
    await admin.providers.discoverModels({ providerId: "caps-list" });

    // Added from the list.
    await admin.providers.addModel({ providerId: "caps-list", upstreamModel: "think", id: "router-think" });
    await admin.providers.addModel({ providerId: "caps-list", upstreamModel: "plain", id: "router-plain" });
    await admin.providers.addModel({ providerId: "caps-list", upstreamModel: "silent", id: "router-silent" });
    expect(await capsOf("router-think")).toMatchObject({ thinking: true, effort: [LOW, HIGH], images: true, pdf: false });
    expect(await capsOf("router-plain")).toMatchObject({ thinking: false, effort: [], images: false });
    expect(await capsOf("router-silent")).toMatchObject({ thinking: false, effort: [], images: false });

    // Filled in at discovery.
    expect(await capsOf("router-late")).toMatchObject({ thinking: true, effort: [LOW, MEDIUM, HIGH], images: true, pdf: true });
    // What was set stays, also when it is nothing; the rest of the model is still filled in.
    expect(await capsOf("router-by-hand")).toMatchObject({ thinking: true, effort: [MAX], images: true });
    expect(await capsOf("router-cleared")).toMatchObject({ thinking: true, effort: [] });

    // Only what the list says yes to changes: a later list without them takes nothing away.
    up.enqueue({ json: { data: [{ id: "late" }, { id: "think" }] } });
    await admin.providers.discoverModels({ providerId: "caps-list" });
    expect(await capsOf("router-late")).toMatchObject({ thinking: true, effort: [LOW, MEDIUM, HIGH], images: true });
  });

  it("do not show as a marker to anyone", async () => {
    const m = await adminApi(ts.url, adminToken, "GET", "/api/admin/models/router-by-hand");
    expect(m.body.capabilities.effort).toEqual(["max"]);
    expect(m.body.capabilities.set_by_hand).toBeUndefined();
  });

  it("can be set by hand only by who may change the provider", async () => {
    expect(await code(alice.providers.setModelEfforts({ modelId: "router-think", efforts: [LOW] }))).toBe(Code.PermissionDenied);
    const own = (await alice.providers.addModel({ providerId: aliceProvider, upstreamModel: "own-effort" })).model!;
    const set = await alice.providers.setModelEfforts({ modelId: own.id, efforts: [HIGH, LOW] });
    expect(set.model?.efforts).toEqual([LOW, HIGH]);
    expect((await alice.providers.listProviders({})).providers.find((p) => p.id === aliceProvider)!.models.find((m) => m.id === own.id)?.efforts).toEqual([LOW, HIGH]);
    expect(await code(bob.providers.setModelEfforts({ modelId: own.id, efforts: [LOW] }))).toBe(Code.NotFound);
    expect(await code(admin.providers.setModelEfforts({ modelId: "nope", efforts: [] }))).toBe(Code.NotFound);
  });

  describe("recommended effort", () => {
    const list = (id: string, default_effort: string, supported_efforts: string[] | null = ["low", "medium", "high"]) => ({ json: { data: [{ id, reasoning: { supported_efforts, default_effort } }] } });
    const discover = async (reply: { json: unknown }) => {
      up.enqueue(reply);
      await admin.providers.discoverModels({ providerId: "caps-list" });
    };
    const add = (name: string) => admin.providers.addModel({ providerId: "caps-list", upstreamModel: name, id: `rec-${name}` });

    it("comes from the list on adding, and is filled in on models already added", async () => {
      await add("before");
      expect((await capsOf("rec-before")).defaultEffort).toBe(Effort.UNSPECIFIED);
      await discover(list("before", "high"));
      expect(await capsOf("rec-before")).toMatchObject({ effort: [LOW, MEDIUM, HIGH], defaultEffort: HIGH });

      await discover(list("added", "medium"));
      await add("added");
      expect((await capsOf("rec-added")).defaultEffort).toBe(MEDIUM);
      expect((await admin.providers.listProviders({})).providers.find((p) => p.id === "caps-list")!.models.find((m) => m.id === "rec-added")?.defaultEffort).toBe(MEDIUM);

      // Like the rest: a list only fills in what is empty, and takes nothing away.
      await discover(list("added", "high"));
      expect((await capsOf("rec-added")).defaultEffort).toBe(MEDIUM);
      await discover({ json: { data: [{ id: "added" }] } });
      expect((await capsOf("rec-added")).defaultEffort).toBe(MEDIUM);
    });

    it("from a list never overrides one set by hand, also none", async () => {
      await add("hand");
      await admin.providers.setModelEfforts({ modelId: "rec-hand", efforts: [LOW, MEDIUM, HIGH], defaultEffort: LOW });
      await discover(list("hand", "high"));
      expect((await capsOf("rec-hand")).defaultEffort).toBe(LOW);

      await add("none");
      await admin.providers.setModelEfforts({ modelId: "rec-none", efforts: [LOW, MEDIUM, HIGH], defaultEffort: Effort.UNSPECIFIED });
      await discover(list("none", "high"));
      expect(await capsOf("rec-none")).toMatchObject({ effort: [LOW, MEDIUM, HIGH], defaultEffort: Effort.UNSPECIFIED });
    });

    it("from a list must be one of the levels set by hand", async () => {
      await add("levels");
      await admin.providers.setModelEfforts({ modelId: "rec-levels", efforts: [MAX] });
      await discover(list("levels", "high"));
      expect(await capsOf("rec-levels")).toMatchObject({ effort: [MAX], defaultEffort: Effort.UNSPECIFIED });
      // Nor is it taken over by a model added with levels of its own.
      await discover(list("own", "high"));
      await adminApi(ts.url, adminToken, "POST", "/api/admin/models", { id: "rec-own", provider_id: "caps-list", upstream_model: "own", capabilities: { effort: ["max"] } });
      expect((await capsOf("rec-own")).defaultEffort).toBe(Effort.UNSPECIFIED);
    });

    it("set by hand must be a level of the model, and goes with it", async () => {
      await add("gone");
      await admin.providers.setModelEfforts({ modelId: "rec-gone", efforts: [LOW, HIGH], defaultEffort: HIGH });
      expect((await capsOf("rec-gone")).defaultEffort).toBe(HIGH);
      expect(await code(admin.providers.setModelEfforts({ modelId: "rec-gone", efforts: [LOW, HIGH], defaultEffort: MAX }))).toBe(Code.InvalidArgument);
      // Levels alone leave it while it is one of them.
      await admin.providers.setModelEfforts({ modelId: "rec-gone", efforts: [LOW, MEDIUM, HIGH] });
      expect((await capsOf("rec-gone")).defaultEffort).toBe(HIGH);
      await admin.providers.setModelEfforts({ modelId: "rec-gone", efforts: [LOW, MEDIUM] });
      expect((await capsOf("rec-gone")).defaultEffort).toBe(Effort.UNSPECIFIED);
      // What was set by hand stays out of the list's reach even then.
      await admin.providers.setModelEfforts({ modelId: "rec-gone", efforts: [LOW, MEDIUM, HIGH] });
      await discover(list("gone", "high"));
      expect((await capsOf("rec-gone")).defaultEffort).toBe(Effort.UNSPECIFIED);
    });

    it("set alone says nothing about the levels", async () => {
      await discover(list("alone", "high", ["low", "high"]));
      await add("alone");
      await admin.providers.setModelEfforts({ modelId: "rec-alone", efforts: [LOW, HIGH], defaultEffort: LOW });
      const stored = await adminApi(ts.url, adminToken, "GET", "/api/admin/models/rec-alone");
      expect(stored.body.capabilities).toMatchObject({ effort: ["low", "high"], default_effort: "low" });
      // Set to none through the admin API: nothing shows of the stored marker.
      await adminApi(ts.url, adminToken, "PATCH", "/api/admin/models/rec-alone", { capabilities: { default_effort: "" } });
      expect((await capsOf("rec-alone")).defaultEffort).toBe(Effort.UNSPECIFIED);
      expect((await adminApi(ts.url, adminToken, "GET", "/api/admin/models/rec-alone")).body.capabilities.default_effort).toBeUndefined();
    });

    it("can be set by hand only by who may change the provider", async () => {
      expect(await code(alice.providers.setModelEfforts({ modelId: "rec-added", efforts: [LOW, MEDIUM, HIGH], defaultEffort: LOW }))).toBe(Code.PermissionDenied);
      const own = (await alice.providers.addModel({ providerId: aliceProvider, upstreamModel: "own-recommended" })).model!;
      const set = await alice.providers.setModelEfforts({ modelId: own.id, efforts: [HIGH, LOW], defaultEffort: HIGH });
      expect(set.model).toMatchObject({ efforts: [LOW, HIGH], defaultEffort: HIGH });
    });
  });

  it("keep an admin API choice too", async () => {
    await adminApi(ts.url, adminToken, "PATCH", "/api/admin/models/router-plain", { capabilities: { images: false, effort: [] } });
    up.enqueue({ json: { data: [{ id: "plain", reasoning: { supported_efforts: null }, architecture: { input_modalities: ["image"] } }] } });
    await admin.providers.discoverModels({ providerId: "caps-list" });
    expect(await capsOf("router-plain")).toMatchObject({ thinking: true, effort: [], images: false });
  });
});
