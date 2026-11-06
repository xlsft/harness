/**
 * Native credentials of cloud providers (Bedrock, Vertex, Foundry), and the speed tiers and cache lifetimes of the gateway.
 * Everything here is fake: throwaway keys, local fake upstreams, and a CloudEnv that reaches no real metadata service or file.
 */
import { generateKeyPairSync, createVerify } from "node:crypto";
import { Code, ConnectError } from "@connectrpc/connect";
import { eq } from "drizzle-orm";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { getDb, schema } from "../src/db/index.ts";
import { encodeFrame, eventStreamToSse, readFrames } from "../src/gateway/cloud/eventstream.ts";
import { authorize, clearCloudTokens, CloudAuthError, type CloudAuth, type CloudEnv } from "../src/gateway/cloud/credentials.ts";
import { signV4 } from "../src/gateway/cloud/sigv4.ts";
import { Role } from "../src/gen/harness/llm/v1/content_pb.ts";
import { ProviderScope } from "../src/gen/harness/llm/v1/providers_pb.ts";
import { modelTiers, tieredPricing } from "../src/services/model-tiers.ts";
import { cloudBaseUrl, cloudOptionError, validateCloudOptions } from "../src/services/presets-cloud.ts";
import { findPreset } from "../src/services/presets.ts";
import { anthropicThinkingText } from "./fixtures/upstream.ts";
import { MockUpstream } from "./helpers/mock-upstream.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

// Every hostname resolves to a public address, so cloud endpoints can be connected offline. Nothing is sent there.
vi.mock("node:dns", async (orig) => {
  const dns = await orig<typeof import("node:dns")>();
  const lookup = (_host: string, opts: unknown, cb?: (...a: unknown[]) => void) => {
    const done = (typeof opts === "function" ? opts : cb) as (...a: unknown[]) => void;
    done(null, [{ address: "1.1.1.1", family: 4 }]);
  };
  return { ...dns, default: { ...dns, lookup }, lookup };
});

const stream = (...chunks: Uint8Array[]) =>
  new ReadableStream<Uint8Array>({
    start(c) {
      for (const x of chunks) c.enqueue(x);
      c.close();
    },
  });

describe("SigV4", () => {
  // AWS's published signature test suite (aws-sig-v4-test-suite): fixed credentials, date, host and service.
  const credentials = { accessKeyId: "AKIDEXAMPLE", secretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY" };
  const now = new Date(Date.UTC(2015, 7, 30, 12, 36, 0));
  const sign = (method: string, url: string) => signV4({ method, url, headers: {}, body: "", region: "us-east-1", service: "service", credentials, now }).authorization;

  it("matches the get-vanilla vector", () => {
    expect(sign("GET", "https://example.amazonaws.com/")).toBe(
      "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31",
    );
  });

  it("matches the post-vanilla vector", () => {
    expect(sign("POST", "https://example.amazonaws.com/")).toBe(
      "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b",
    );
  });

  it("signs a session token and the content type, and encodes the path once more", () => {
    const h = signV4({
      method: "POST",
      url: "https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-v1%3A0/invoke",
      headers: { "content-type": "application/json", accept: "x" },
      body: "{}",
      region: "us-east-1",
      service: "bedrock",
      credentials: { ...credentials, sessionToken: "fake-session" },
      now,
    });
    expect(h["x-amz-security-token"]).toBe("fake-session");
    expect(h.authorization).toContain("SignedHeaders=content-type;host;x-amz-date;x-amz-security-token,");
    expect(h.accept).toBe("x");
  });
});

describe("AWS event stream", () => {
  const chunk = (data: unknown) => encodeFrame({ ":event-type": "chunk", ":message-type": "event" }, JSON.stringify({ bytes: Buffer.from(JSON.stringify(data)).toString("base64") }));

  it("reads frames split anywhere", async () => {
    const frame = chunk({ type: "ping" });
    const out = [];
    for await (const f of readFrames(stream(frame.subarray(0, 5), frame.subarray(5, 30), frame.subarray(30)))) out.push(f);
    expect(out).toHaveLength(1);
    expect(out[0]!.headers[":message-type"]).toBe("event");
  });

  it("refuses a damaged frame", async () => {
    const frame = chunk({ type: "ping" });
    frame[frame.length - 6]! ^= 0xff;
    await expect(
      (async () => {
        for await (const _ of readFrames(stream(frame))) void _;
      })(),
    ).rejects.toThrow(/checksum/);
  });

  it("carries the events as SSE and an exception as an error event", async () => {
    const exception = encodeFrame({ ":message-type": "exception", ":exception-type": "throttlingException" }, JSON.stringify({ message: "slow down" }));
    const text = await new Response(eventStreamToSse(stream(chunk({ type: "message_start" }), exception))).text();
    expect(text).toContain('data: {"type":"message_start"}');
    expect(text).toContain('"type":"rate_limit_error"');
    expect(text).toContain("slow down");
  });
});

describe("cloud options", () => {
  const bedrock = findPreset("bedrock")!;
  const vertex = findPreset("vertex-anthropic")!;

  it("refuses values that would change the host", () => {
    expect(cloudOptionError("region", "evil.example.com#")).toBeTruthy();
    expect(cloudOptionError("region", "us-east-1")).toBeUndefined();
    expect(cloudOptionError("resource", "a.evil.com/x")).toBeTruthy();
    expect(cloudOptionError("project", "x/../../y")).toBeTruthy();
    expect(cloudOptionError("location", "us-east5")).toBeUndefined();
    expect(() => validateCloudOptions(bedrock, { region: "evil.example.com#", access_key_id: "AKIAFAKEFAKEFAKEFAKE" }, false)).toThrow(/Region/);
  });

  it("keeps the server's identity from personal providers", () => {
    expect(() => validateCloudOptions(bedrock, { credential_mode: "ambient", region: "us-east-1" }, true)).toThrow(/personal/);
    expect(validateCloudOptions(bedrock, { credential_mode: "ambient", region: "us-east-1" }, false).credential_mode).toBe("ambient");
  });

  it("drops what the mode does not use and asks for what it needs", () => {
    expect(validateCloudOptions(bedrock, { credential_mode: "ambient", region: "us-east-1", access_key_id: "AKIAFAKEFAKEFAKEFAKE" }, false).access_key_id).toBeUndefined();
    expect(() => validateCloudOptions(bedrock, { region: "us-east-1" }, false)).toThrow(/access key id/);
  });

  it("builds endpoints, without a prefix for Vertex's global location", () => {
    expect(cloudBaseUrl(bedrock, { region: "eu-west-1" })).toBe("https://bedrock-mantle.eu-west-1.api.aws/anthropic");
    expect(cloudBaseUrl(vertex, { project: "my-project", location: "global" })).toBe("https://aiplatform.googleapis.com/v1/projects/my-project/locations/global");
    expect(cloudBaseUrl(vertex, { project: "my-project", location: "us-east5" })).toBe("https://us-east5-aiplatform.googleapis.com/v1/projects/my-project/locations/us-east5");
  });
});

/** A CloudEnv over a table of URL handlers: anything else fails the test's request. */
function fakeEnv(routes: Record<string, (init?: RequestInit) => Response>, over: Partial<CloudEnv> = {}) {
  const calls: string[] = [];
  const env: CloudEnv = {
    env: {},
    fetch: (async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      calls.push(url);
      const route = Object.keys(routes).find((k) => url.startsWith(k));
      if (!route) throw new Error(`no route for ${url}`);
      return routes[route]!(init);
    }) as typeof fetch,
    readFile: () => {
      throw new Error("no such file");
    },
    now: () => Date.UTC(2026, 10, 6),
    home: "/nonexistent",
    googleTokenUrl: "https://google.test/token",
    azureLoginUrl: "https://login.test",
    awsImdsUrl: "http://imds.test",
    awsContainerUrl: "http://container.test",
    azureImdsUrl: "http://azure-imds.test",
    ...over,
  };
  return { env, calls };
}

const req = { method: "POST", url: "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1/messages", headers: { "content-type": "application/json" }, body: "{}" };
const aws = (over: Partial<CloudAuth>): CloudAuth => ({ kind: "aws", mode: "keys", service: "bedrock-mantle", options: { region: "us-east-1" }, ownerUserId: null, ...over });

describe("AWS credentials", () => {
  beforeEach(() => clearCloudTokens());

  it("signs with stored keys, touching nothing else", async () => {
    const { env, calls } = fakeEnv({});
    const h = await authorize(aws({ options: { region: "us-east-1", access_key_id: "AKIAFAKE" }, secret: "fake-secret" }), req, env);
    expect(h.authorization).toMatch(/^AWS4-HMAC-SHA256 Credential=AKIAFAKE\/\d+\/us-east-1\/bedrock-mantle\/aws4_request/);
    expect(calls).toEqual([]);
  });

  it("puts a Bedrock API key in x-api-key on Mantle", async () => {
    const h = await authorize(aws({ mode: "bearer", secret: "fake-bearer" }), req, fakeEnv({}).env);
    expect(h["x-api-key"]).toBe("fake-bearer");
    expect(h.authorization).toBeUndefined();
  });

  it("takes an instance role through IMDSv2, and remembers it until it expires", async () => {
    const expiry = new Date(Date.UTC(2026, 10, 6, 6)).toISOString();
    let tokenAsked = false;
    const { env, calls } = fakeEnv({
      "http://imds.test/latest/api/token": (init) => {
        tokenAsked = init?.method === "PUT";
        return new Response("imds-session");
      },
      "http://imds.test/latest/meta-data/iam/security-credentials/my-role": (init) => {
        expect((init?.headers as Record<string, string>)["x-aws-ec2-metadata-token"]).toBe("imds-session");
        return Response.json({ AccessKeyId: "ASIAFAKE", SecretAccessKey: "fake", Token: "fake-role-token", Expiration: expiry });
      },
      "http://imds.test/latest/meta-data/iam/security-credentials/": () => new Response("my-role\n"),
    });
    const h = await authorize(aws({ mode: "ambient" }), req, env);
    expect(tokenAsked).toBe(true);
    expect(h["x-amz-security-token"]).toBe("fake-role-token");
    expect(h.authorization).toContain("ASIAFAKE");
    const before = calls.length;
    await authorize(aws({ mode: "ambient" }), req, env);
    expect(calls.length).toBe(before);
  });

  it("fetches again once the role's keys are near their expiry", async () => {
    let rounds = 0;
    const { env } = fakeEnv({
      "http://imds.test/latest/api/token": () => new Response("s"),
      "http://imds.test/latest/meta-data/iam/security-credentials/r": () => {
        rounds++;
        return Response.json({ AccessKeyId: "A", SecretAccessKey: "B", Token: "T", Expiration: new Date(Date.UTC(2026, 10, 6, 0, 1)).toISOString() });
      },
      "http://imds.test/latest/meta-data/iam/security-credentials/": () => new Response("r"),
    });
    await authorize(aws({ mode: "ambient" }), req, env);
    await authorize(aws({ mode: "ambient" }), req, env);
    expect(rounds).toBe(2);
  });

  it("prefers environment keys, then the shared profile", async () => {
    const fromEnv = fakeEnv({}, { env: { AWS_ACCESS_KEY_ID: "AKIAENV", AWS_SECRET_ACCESS_KEY: "s" } });
    expect((await authorize(aws({ mode: "ambient" }), req, fromEnv.env)).authorization).toContain("AKIAENV");
    clearCloudTokens();
    const profile = fakeEnv({}, { env: { AWS_PROFILE: "work", AWS_EC2_METADATA_DISABLED: "true" }, readFile: () => "[default]\naws_access_key_id=NOPE\naws_secret_access_key=x\n[work]\naws_access_key_id = AKIAWORK\naws_secret_access_key = y\n" });
    expect((await authorize(aws({ mode: "ambient" }), req, profile.env)).authorization).toContain("AKIAWORK");
  });

  it("assumes a web identity role (Kubernetes)", async () => {
    let form = new URLSearchParams();
    const { env } = fakeEnv(
      {
        "https://sts.test/": (init) => {
          form = init?.body as URLSearchParams;
          return Response.json({ AssumeRoleWithWebIdentityResponse: { AssumeRoleWithWebIdentityResult: { Credentials: { AccessKeyId: "ASIAWEB", SecretAccessKey: "s", SessionToken: "t", Expiration: 1893456000 } } } });
        },
      },
      { env: { AWS_WEB_IDENTITY_TOKEN_FILE: "/token", AWS_ROLE_ARN: "arn:aws:iam::1:role/r", AWS_ENDPOINT_URL_STS: "https://sts.test" }, readFile: () => "jwt-fake\n" },
    );
    expect((await authorize(aws({ mode: "ambient" }), req, env)).authorization).toContain("ASIAWEB");
    expect(form.get("WebIdentityToken")).toBe("jwt-fake");
    expect(form.get("RoleArn")).toBe("arn:aws:iam::1:role/r");
  });

  it("says so when the server has no identity, and never lets a personal provider use it", async () => {
    const { env } = fakeEnv({ "http://imds.test": () => new Response("", { status: 404 }) });
    await expect(authorize(aws({ mode: "ambient" }), req, env)).rejects.toThrow(/No AWS credentials/);
    const { env: e2, calls } = fakeEnv({}, { env: { AWS_ACCESS_KEY_ID: "AKIAENV", AWS_SECRET_ACCESS_KEY: "s" } });
    await expect(authorize(aws({ mode: "ambient", ownerUserId: "u1" }), req, e2)).rejects.toBeInstanceOf(CloudAuthError);
    expect(calls).toEqual([]);
  });
});

describe("Google credentials", () => {
  beforeEach(() => clearCloudTokens());
  const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const pem = privateKey.export({ type: "pkcs8", format: "pem" }).toString();
  // Throwaway key made above; `token_uri` points somewhere it must not be followed to.
  const file = JSON.stringify({ type: "service_account", client_email: "sa@fake.iam.gserviceaccount.com", private_key: pem, private_key_id: "k1", token_uri: "http://169.254.169.254/evil" });
  const gcp = (over: Partial<CloudAuth>): CloudAuth => ({ kind: "gcp", mode: "service_account", options: { project: "my-project", location: "global" }, ownerUserId: null, ...over });

  it("exchanges a signed JWT at Google's endpoint, ignoring the file's token_uri", async () => {
    let assertion = "";
    const { env, calls } = fakeEnv({
      "https://google.test/token": (init) => {
        assertion = (init?.body as URLSearchParams).get("assertion") ?? "";
        return Response.json({ access_token: "gcp-fake-token", expires_in: 3600 });
      },
    });
    const h = await authorize(gcp({ secret: file }), req, env);
    expect(h.authorization).toBe("Bearer gcp-fake-token");
    expect(calls).toEqual(["https://google.test/token"]);
    const [head, claims, sig] = assertion.split(".");
    expect(createVerify("RSA-SHA256").update(`${head}.${claims}`).verify(publicKey, Buffer.from(sig!, "base64url"))).toBe(true);
    const payload = JSON.parse(Buffer.from(claims!, "base64url").toString());
    expect(payload).toMatchObject({ iss: "sa@fake.iam.gserviceaccount.com", aud: "https://google.test/token", scope: "https://www.googleapis.com/auth/cloud-platform" });
    await authorize(gcp({ secret: file }), req, env);
    expect(calls).toHaveLength(1);
  });

  it("uses gcloud's file for application default credentials", async () => {
    let grant = "";
    const { env } = fakeEnv(
      {
        "https://google.test/token": (init) => {
          grant = (init?.body as URLSearchParams).get("grant_type") ?? "";
          return Response.json({ access_token: "adc-fake" });
        },
      },
      { readFile: () => JSON.stringify({ type: "authorized_user", client_id: "c", client_secret: "s", refresh_token: "r" }) },
    );
    expect((await authorize(gcp({ mode: "ambient" }), req, env)).authorization).toBe("Bearer adc-fake");
    expect(grant).toBe("refresh_token");
  });

  it("falls back to the metadata server", async () => {
    let flavor = "";
    const { env } = fakeEnv(
      {
        "http://meta.test/computeMetadata/v1/instance/service-accounts/default/token": (init) => {
          flavor = (init?.headers as Record<string, string>)["metadata-flavor"] ?? "";
          return Response.json({ access_token: "meta-fake", expires_in: 100 });
        },
      },
      { env: { GCE_METADATA_HOST: "meta.test" } },
    );
    expect((await authorize(gcp({ mode: "ambient" }), req, env)).authorization).toBe("Bearer meta-fake");
    expect(flavor).toBe("Google");
  });

  it("does not quote what the token service answers", async () => {
    const { env } = fakeEnv({ "https://google.test/token": () => new Response("echo of the secret", { status: 400 }) });
    const err = await authorize(gcp({ secret: file }), req, env).catch((e) => e as Error);
    expect((err as Error).message).toBe("Google's token service answered HTTP 400");
  });
});

describe("Azure credentials", () => {
  beforeEach(() => clearCloudTokens());
  const azure = (over: Partial<CloudAuth>): CloudAuth => ({ kind: "azure", mode: "api_key", service: "openai", options: { resource: "r" }, ownerUserId: null, ...over });

  it("puts a key where the route reads it", async () => {
    expect((await authorize(azure({ secret: "fake" }), req, fakeEnv({}).env))["api-key"]).toBe("fake");
    expect((await authorize(azure({ secret: "fake", service: "anthropic" }), req, fakeEnv({}).env))["x-api-key"]).toBe("fake");
  });

  it("signs in an Entra app with its secret", async () => {
    let body = new URLSearchParams();
    const { env } = fakeEnv({
      "https://login.test/my-tenant/oauth2/v2.0/token": (init) => {
        body = init?.body as URLSearchParams;
        return Response.json({ access_token: "entra-fake", expires_in: 3600 });
      },
    });
    const h = await authorize(azure({ mode: "entra", secret: "fake-secret", options: { resource: "r", tenant_id: "my-tenant", client_id: "app" } }), req, env);
    expect(h.authorization).toBe("Bearer entra-fake");
    expect(body.get("scope")).toBe("https://cognitiveservices.azure.com/.default");
    expect(body.get("client_id")).toBe("app");
  });

  it("takes a managed identity from the instance metadata service or App Service", async () => {
    let meta = "";
    const imds = fakeEnv({
      "http://azure-imds.test/metadata/identity/oauth2/token": (init) => {
        meta = (init?.headers as Record<string, string>).metadata ?? "";
        return Response.json({ access_token: "mi-fake", expires_on: String(Date.UTC(2026, 10, 7) / 1000) });
      },
    });
    expect((await authorize(azure({ mode: "ambient" }), req, imds.env)).authorization).toBe("Bearer mi-fake");
    expect(meta).toBe("true");
    clearCloudTokens();
    let header = "";
    const app = fakeEnv(
      {
        "https://identity.test/": (init) => {
          header = (init?.headers as Record<string, string>)["x-identity-header"] ?? "";
          return Response.json({ access_token: "app-fake", expires_on: String(Date.UTC(2026, 10, 7) / 1000) });
        },
      },
      { env: { IDENTITY_ENDPOINT: "https://identity.test/", IDENTITY_HEADER: "hdr" } },
    );
    expect((await authorize(azure({ mode: "ambient" }), req, app.env)).authorization).toBe("Bearer app-fake");
    expect(header).toBe("hdr");
  });
});

describe("speed tiers and cache lifetimes", () => {
  it("offers what each provider documents", () => {
    expect(modelTiers({ presetId: "anthropic", type: "anthropic", upstreamModel: "claude-opus-5-5", promptCache: true })).toMatchObject({ speed_tiers: ["standard", "fast"], cache_ttls: ["5m", "1h"] });
    // Fast mode is first-party only; the cache choice is not.
    expect(modelTiers({ presetId: "bedrock", type: "anthropic", upstreamModel: "anthropic.claude-opus-5-5", promptCache: true })).toMatchObject({ speed_tiers: [], cache_ttls: ["5m", "1h"] });
    expect(modelTiers({ presetId: "anthropic", type: "anthropic", upstreamModel: "claude-haiku-5-5", promptCache: true }).speed_tiers).toEqual([]);
    expect(modelTiers({ presetId: "openai", type: "openai-responses", upstreamModel: "gpt-5.5", promptCache: true })).toMatchObject({ speed_tiers: ["standard", "fast", "flex"], cache_ttls: ["5m", "24h"] });
    expect(modelTiers({ presetId: null, type: "openai-chat", upstreamModel: "gpt-5.5", promptCache: true })).toMatchObject({ speed_tiers: [], cache_ttls: [] });
  });

  it("has OpenAI's published price for each tier, per model", () => {
    const of = (upstreamModel: string) => modelTiers({ presetId: "openai", type: "openai-responses", upstreamModel, promptCache: true });
    expect(of("gpt-5.5").speed_multipliers).toEqual({ fast: 2.5, flex: 0.5 });
    expect(of("gpt-5.5-2026-04-23").speed_multipliers).toEqual({ fast: 2.5, flex: 0.5 });
    expect(of("gpt-5.4").speed_multipliers).toEqual({ fast: 2, flex: 0.5 });
    expect(of("gpt-5-mini").speed_multipliers).toEqual({ fast: 1.8, flex: 0.5 });
    expect(of("o3").speed_multipliers).toEqual({ fast: 1.75, flex: 0.5 });
    // Only what the page lists for the model.
    expect(of("gpt-5.3-codex").speed_tiers).toEqual(["standard", "fast"]);
    expect(of("gpt-5-nano").speed_tiers).toEqual(["standard", "flex"]);
    for (const none of ["gpt-5.5-cyber", "o3-pro", "o4-mini", "gpt-99"]) expect(of(none).speed_tiers).toEqual([]);
  });

  it("prices a tier and an hour-long cache", () => {
    const p = { input: 4, output: 20, cache_read: 0.2, cache_write: 5 };
    expect(tieredPricing(p, undefined)).toBe(p);
    expect(tieredPricing(p, { multiplier: 2 })).toMatchObject({ input: 8, output: 40, cache_read: 0.4, cache_write: 10 });
    expect(tieredPricing(p, { cacheTtl: "1h" })).toMatchObject({ input: 4, output: 20, cache_write: 8 });
  });
});

describe("through the gateway", () => {
  let ts: TestServer;
  let up: MockUpstream;
  let admin: ReturnType<TestServer["as"]>;
  let alice: ReturnType<TestServer["as"]>;

  const text = [{ role: Role.USER, content: [{ block: { case: "text" as const, value: { text: "hi" } } }] }];
  const events = async (client: typeof admin, model: string, extra: Record<string, unknown> = {}) => {
    const out = [];
    for await (const r of client.gateway.streamMessage({ model, maxTokens: 100, messages: text, ...extra })) out.push(r.event);
    return out;
  };

  beforeAll(async () => {
    up = await new MockUpstream().start();
    ts = await startTestServer();
    const a = await deviceLogin(ts);
    admin = ts.as(a.accessToken);
    alice = ts.as((await createUserAndLogin(ts, a.cookie, "alice-cloud@test.local")).accessToken);
  });
  afterAll(async () => {
    await ts.close();
    await up.stop();
  });
  beforeEach(() => up.reset());

  const connect = (presetId: string, options: Record<string, string>, apiKey = "fake-secret-key") =>
    admin.providers.createProvider({ scope: ProviderScope.GLOBAL, presetId, baseUrl: up.url, allowPrivateNetwork: true, apiKey, options });

  it("signs a Mantle request with the stored keys, and never returns the secret", async () => {
    const { provider } = await connect("bedrock", { region: "us-east-1", access_key_id: "AKIAFAKEFAKEFAKEFAKE" });
    expect(JSON.stringify(provider)).not.toContain("fake-secret-key");
    // Who can change it sees its account; everyone else only that it is there.
    expect(provider!.options.access_key_id).toBe("AKIAFAKEFAKEFAKEFAKE");
    const seen = (await alice.providers.listProviders({})).providers.find((p) => p.id === provider!.id);
    expect(seen?.options).toEqual({});
    await admin.providers.addModel({ providerId: provider!.id, upstreamModel: "anthropic.claude-opus-5-5", id: "bedrock-opus" });
    up.enqueue({ sse: anthropicThinkingText });
    const out = await events(admin, "bedrock-opus");
    expect(out.some((e) => e.case === "messageStop")).toBe(true);
    const sent = up.requests[0]!;
    expect(sent.url).toBe("/v1/messages");
    expect(sent.headers.authorization).toMatch(/^AWS4-HMAC-SHA256 Credential=AKIAFAKEFAKEFAKEFAKE\/\d+\/us-east-1\/bedrock-mantle\/aws4_request/);
    expect(sent.headers["x-api-key"]).toBeUndefined();
    expect(sent.body.model).toBe("anthropic.claude-opus-5-5");
    // The same signature, worked out from what actually arrived.
    const date = String(sent.headers["x-amz-date"]);
    const again = signV4({
      method: "POST",
      url: `${up.url}${sent.url}`,
      headers: { "content-type": String(sent.headers["content-type"]) },
      body: JSON.stringify(sent.body),
      region: "us-east-1",
      service: "bedrock-mantle",
      credentials: { accessKeyId: "AKIAFAKEFAKEFAKEFAKE", secretAccessKey: "fake-secret-key" },
      now: new Date(`${date.slice(0, 4)}-${date.slice(4, 6)}-${date.slice(6, 8)}T${date.slice(9, 11)}:${date.slice(11, 13)}:${date.slice(13, 15)}Z`),
    });
    expect(again.authorization).toBe(sent.headers.authorization);
  });

  it("speaks InvokeModel's event stream", async () => {
    const { provider } = await connect("bedrock-invoke", { region: "us-east-1", access_key_id: "AKIAFAKEFAKEFAKEFAKE" });
    await admin.providers.addModel({ providerId: provider!.id, upstreamModel: "us.anthropic.claude-sonnet-4-6:0", id: "invoke-sonnet" });
    const frames = anthropicThinkingText
      .split("\n\n")
      .filter((b) => b.includes("data: "))
      .map((b) => b.slice(b.indexOf("data: ") + 6))
      .map((data) => encodeFrame({ ":event-type": "chunk", ":message-type": "event" }, JSON.stringify({ bytes: Buffer.from(data).toString("base64") })));
    up.enqueue({ binary: Buffer.concat(frames), headers: { "content-type": "application/vnd.amazon.eventstream" } });
    const out = await events(admin, "invoke-sonnet");
    expect(out.some((e) => e.case === "messageStop")).toBe(true);
    const sent = up.requests[0]!;
    expect(sent.url).toBe("/model/us.anthropic.claude-sonnet-4-6%3A0/invoke-with-response-stream");
    expect(sent.body.anthropic_version).toBe("bedrock-2023-05-31");
    expect(sent.body.model).toBeUndefined();
    expect(sent.body.stream).toBeUndefined();
  });

  it("refuses the server's identity and another endpoint to a personal provider", async () => {
    const code = (p: Promise<unknown>) =>
      p.then(
        () => undefined,
        (e: ConnectError) => e.code,
      );
    expect(await code(alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "bedrock", options: { credential_mode: "ambient", region: "us-east-1" } }))).toBe(Code.InvalidArgument);
    // A value that would change the host is refused, and an endpoint given is ignored: it follows from the options.
    expect(await code(alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "foundry-openai", apiKey: "fake", options: { resource: "evil.example.com/x" } }))).toBe(Code.InvalidArgument);
    const { provider } = await alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "foundry-openai", baseUrl: "https://elsewhere.example.com/", apiKey: "fake", options: { resource: "my-resource" } });
    expect(provider!.baseUrl).toBe("https://my-resource.openai.azure.com/openai/v1");
    expect(await code(alice.providers.updateProvider({ id: provider!.id, baseUrl: "https://elsewhere.example.com/" }))).toBe(Code.InvalidArgument);
    // A personal service account file is checked, and its email is all that is ever shown of it.
    expect(await code(alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "vertex-anthropic", apiKey: "not json", options: { project: "my-project" } }))).toBe(Code.InvalidArgument);
    const sa = JSON.stringify({ type: "service_account", client_email: "sa@fake.iam.gserviceaccount.com", private_key: "fake" });
    const vertex = await alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "vertex-anthropic", apiKey: sa, options: { project: "my-project" } });
    expect(vertex.provider!.apiKeyHint).toBe("sa@fake.iam.gserviceaccount.com");
  });

  it("asks for fast mode and an hour-long cache where the model has them, and prices them", async () => {
    await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, presetId: "anthropic", id: "first-party", apiKey: "fake" });
    await getDb().update(schema.provider).set({ baseUrl: up.url, allowPrivateNetwork: true }).where(eq(schema.provider.id, "first-party"));
    await admin.providers.addModel({ providerId: "first-party", upstreamModel: "claude-opus-5-5", id: "fp-opus" });
    await getDb()
      .update(schema.model)
      .set({ capabilities: { thinking: false, effort: [], images: false, pdf: false, parallel_tool_calls: true, prompt_cache: true }, pricing: { input: 4, output: 20 } })
      .where(eq(schema.model.id, "fp-opus"));
    up.enqueue({ sse: anthropicThinkingText });
    const cached = [{ type: "text", text: "sys", cache: true }];
    const out = await events(admin, "fp-opus", { speed: "fast", cacheTtl: "1h", system: cached });
    const sent = up.requests[0]!;
    expect(sent.body.speed).toBe("fast");
    expect(sent.headers["anthropic-beta"]).toBe("fast-mode-2026-02-01");
    expect(sent.body.system[0].cache_control).toEqual({ type: "ephemeral", ttl: "1h" });
    // Twice the price, and the hour-long write at twice the input price of the tier.
    const stop = out.find((e) => e.case === "messageStop")!.value as { costUsd: number };
    expect(stop.costUsd).toBeCloseTo((120 * 8 + 42 * 40 + 1000 * 8 + 50 * 16) / 1_000_000, 8);

    // The catalog says what clients may offer.
    const models = (await admin.gateway.listModels({})).models.find((m) => m.id === "fp-opus")!;
    expect(models.capabilities).toMatchObject({ speedTiers: ["standard", "fast"], cacheTtls: ["5m", "1h"] });
  });

  it("leaves a request with no choice as it was, and a custom endpoint out of fast mode", async () => {
    up.enqueue({ sse: anthropicThinkingText });
    await events(admin, "fp-opus", { system: [{ type: "text", text: "sys", cache: true }] });
    expect(up.requests[0]!.body.speed).toBeUndefined();
    expect(up.requests[0]!.headers["anthropic-beta"]).toBeUndefined();
    expect(up.requests[0]!.body.system[0].cache_control).toEqual({ type: "ephemeral" });

    await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, type: "anthropic", id: "plain", baseUrl: up.url, allowPrivateNetwork: true, apiKey: "fake" });
    await admin.providers.addModel({ providerId: "plain", upstreamModel: "claude-opus-5-5", id: "plain-opus" });
    up.reset();
    up.enqueue({ sse: anthropicThinkingText });
    await events(admin, "plain-opus", { speed: "fast" });
    expect(up.requests[0]!.body.speed).toBeUndefined();
  });

  it("drops a tier the upstream refuses and goes on", async () => {
    up.enqueue({ status: 400, json: { error: { type: "invalid_request_error", message: "speed: not available for this model" } } }, { sse: anthropicThinkingText });
    const out = await events(admin, "fp-opus", { speed: "fast" });
    expect(out.some((e) => e.case === "messageStop")).toBe(true);
    expect(up.requests[0]!.body.speed).toBe("fast");
    expect(up.requests[1]!.body.speed).toBeUndefined();
  });

  it("puts a Foundry key where the Anthropic route reads it", async () => {
    const { provider } = await connect("foundry-anthropic", { credential_mode: "api_key", resource: "my-resource" });
    await admin.providers.addModel({ providerId: provider!.id, upstreamModel: "claude-opus-5-5", id: "foundry-opus" });
    up.enqueue({ sse: anthropicThinkingText });
    await events(admin, "foundry-opus");
    expect(up.requests[0]!.headers["x-api-key"]).toBe("fake-secret-key");
    expect(up.requests[0]!.url).toBe("/v1/messages");
  });
});
