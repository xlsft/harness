/**
 * Image generation: the adapters against a fake HTTP server (OpenAI-compatible generations and edits, Gemini, Imagen,
 * OpenRouter), aspect ratios and pricing, and ImageService + GatewayService.GenerateImages end to end (allowlist by
 * role, kept out of the chat catalog and the chat gateway, server default, usage records).
 */
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { ConnectError, createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { Role } from "../src/gen/harness/llm/v1/content_pb.ts";
import { ImageService } from "../src/gen/harness/llm/v1/images_pb.ts";
import { aspectOf, generateImages, imageApiOf, type ImageTarget } from "../src/images/adapters.ts";
import { defaultImagePricing, imageCost } from "../src/images/pricing.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

// Fake keys, never real.
const KEY = "sk-image-fake-key-0000000000";

// The smallest valid PNG header bytes are enough: the adapters sniff, they do not decode.
const PNG = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 1, 2, 3, 4]);
const JPEG = Buffer.from([0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 1, 2]);
const PNG64 = PNG.toString("base64");

interface Seen {
  path: string;
  headers: IncomingMessage["headers"];
  raw: Buffer;
  json?: any;
}
type Reply = { status: number; body: unknown };

let server: Server;
let base = "";
let seen: Seen[] = [];
let replies: Reply[] = [];
let fallback: Reply = { status: 200, body: {} };

beforeAll(async () => {
  server = createServer((req, res) => {
    const parts: Buffer[] = [];
    req.on("data", (c: Buffer) => parts.push(c));
    req.on("end", () => {
      const u = new URL(req.url ?? "/", "http://x");
      const raw = Buffer.concat(parts);
      let json: any;
      if (String(req.headers["content-type"] ?? "").startsWith("application/json")) json = JSON.parse(raw.toString("utf8"));
      seen.push({ path: u.pathname, headers: req.headers, raw, json });
      if (u.pathname === "/files/out.png") return void res.writeHead(200, { "content-type": "image/png" }).end(PNG);
      const r = replies.shift() ?? fallback;
      res.writeHead(r.status, { "content-type": "application/json" }).end(JSON.stringify(r.body));
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

afterAll(() => new Promise<void>((r) => server.close(() => r())));

beforeEach(() => {
  seen = [];
  replies = [];
  fallback = { status: 200, body: {} };
});

const target = (path = "/v1"): ImageTarget => ({ displayName: "fake", baseUrl: `${base}${path}`, apiKey: KEY, headers: {}, allowPrivateNetwork: true });

describe("which API a provider speaks", () => {
  it("follows the preset or the host, and Imagen models use predict", () => {
    expect(imageApiOf({ type: "openai-chat", baseUrl: "https://api.openai.com/v1" }, "gpt-image-1")).toBe("openai");
    expect(imageApiOf({ type: "openai-chat", baseUrl: "https://generativelanguage.googleapis.com/v1beta/openai", presetId: "gemini" }, "gemini-2.5-flash-image")).toBe("gemini");
    expect(imageApiOf({ type: "openai-chat", baseUrl: "https://generativelanguage.googleapis.com/v1beta/openai" }, "imagen-4.0-generate-001")).toBe("imagen");
    expect(imageApiOf({ type: "openai-chat", baseUrl: "https://openrouter.ai/api/v1" }, "google/gemini-2.5-flash-image")).toBe("openrouter");
    expect(imageApiOf({ type: "anthropic", baseUrl: "https://api.anthropic.com" }, "x")).toBe("");
  });

  it("maps sizes to the nearest aspect ratio", () => {
    expect(aspectOf("1024x1024")).toBe("1:1");
    expect(aspectOf("1536x1024")).toBe("3:2");
    expect(aspectOf("1920x1080")).toBe("16:9");
    expect(aspectOf("")).toBeUndefined();
  });

  it("prices per image and per token, and prefers a reported cost", () => {
    expect(defaultImagePricing("openai", "dall-e-3")).toEqual({ per_image: 0.04 });
    expect(imageCost({ per_image: 0.04 }, 2, 0, 0)).toEqual({ cost: 0.08, priced: true });
    expect(imageCost({ input: 5, output: 40 }, 1, 1000, 10000)).toEqual({ cost: 0.405, priced: true });
    expect(imageCost(null, 1, 10, 10)).toEqual({ cost: 0, priced: false });
    expect(imageCost(null, 1, 10, 10, 0.01)).toEqual({ cost: 0.01, priced: true });
  });
});

describe("adapters", () => {
  it("openai: generations as JSON, base64 results, token usage", async () => {
    fallback = { status: 200, body: { data: [{ b64_json: PNG64, revised_prompt: "a red fox, watercolor" }], usage: { input_tokens: 12, output_tokens: 4160 } } };
    const out = await generateImages(target(), { api: "openai", model: "gpt-image-1", prompt: "a fox", size: "1024x1024", count: 1, quality: "low", inputs: [] });
    expect(out.images).toHaveLength(1);
    expect(out.images[0]!.mediaType).toBe("image/png");
    expect(Buffer.from(out.images[0]!.data)).toEqual(PNG);
    expect(out.revisedPrompt).toBe("a red fox, watercolor");
    expect(out.outputTokens).toBe(4160);
    const s = seen[0]!;
    expect(s.path).toBe("/v1/images/generations");
    expect(s.headers.authorization).toBe(`Bearer ${KEY}`);
    expect(s.json).toEqual({ model: "gpt-image-1", prompt: "a fox", n: 1, size: "1024x1024", quality: "low" });
  });

  it("openai: dall-e asks for base64, and a URL result is downloaded", async () => {
    fallback = { status: 200, body: { data: [{ url: `${base}/files/out.png` }] } };
    const out = await generateImages(target(), { api: "openai", model: "dall-e-3", prompt: "x", size: "", count: 1, inputs: [] });
    expect(seen[0]!.json.response_format).toBe("b64_json");
    expect(seen[1]!.path).toBe("/files/out.png");
    // The download carries no key.
    expect(seen[1]!.headers.authorization).toBeUndefined();
    expect(out.images[0]!.mediaType).toBe("image/png");
  });

  it("openai: edits as multipart with image[] parts", async () => {
    fallback = { status: 200, body: { data: [{ b64_json: PNG64 }, { b64_json: PNG64 }] } };
    const out = await generateImages(target(), {
      api: "openai",
      model: "gpt-image-1",
      prompt: "make it blue",
      size: "1024x1536",
      count: 2,
      inputs: [{ data: PNG, mediaType: "image/png", name: "logo.png" }, { data: JPEG, mediaType: "image/jpeg" }],
    });
    expect(out.images).toHaveLength(2);
    const s = seen[0]!;
    expect(s.path).toBe("/v1/images/edits");
    expect(s.headers["content-type"]).toMatch(/^multipart\/form-data/);
    const body = s.raw.toString("latin1");
    expect(body).toContain('name="image[]"; filename="logo.png"');
    expect(body).toContain('filename="image-2.jpg"');
    expect(body).toContain('name="prompt"\r\n\r\nmake it blue');
    expect(body).toContain('name="n"\r\n\r\n2');
  });

  it("maps upstream errors without leaking the key", async () => {
    fallback = { status: 401, body: { error: { message: `bad key ${KEY}` } } };
    const err = await generateImages(target(), { api: "openai", model: "gpt-image-1", prompt: "x", size: "", count: 1, inputs: [] }).catch((e) => e);
    expect(err.message).toContain("rejected the API key");
    expect(err.message).not.toContain(KEY);
    fallback = { status: 400, body: { error: { message: "Your request was rejected by the safety system." } } };
    const refused = await generateImages(target(), { api: "openai", model: "gpt-image-1", prompt: "x", size: "", count: 1, inputs: [] }).catch((e) => e);
    expect(refused.type).toBe("invalid_request");
    expect(refused.message).toContain("safety system");
  });

  it("refuses an answer that is not an image", async () => {
    fallback = { status: 200, body: { data: [{ b64_json: Buffer.from("<html>").toString("base64") }] } };
    const err = await generateImages(target(), { api: "openai", model: "gpt-image-1", prompt: "x", size: "", count: 1, inputs: [] }).catch((e) => e);
    expect(err.message).toContain("not a PNG");
  });

  it("gemini: generateContent with image modality on the API root, one request per image", async () => {
    fallback = {
      status: 200,
      body: { candidates: [{ content: { parts: [{ text: "Here you go" }, { inlineData: { mimeType: "image/png", data: PNG64 } }] } }], usageMetadata: { promptTokenCount: 7, candidatesTokenCount: 1290 } },
    };
    const out = await generateImages(target("/v1beta/openai"), {
      api: "gemini",
      model: "gemini-2.5-flash-image",
      prompt: "a fox",
      size: "1920x1080",
      count: 2,
      inputs: [{ data: JPEG, mediaType: "image/jpeg" }],
    });
    expect(out.images).toHaveLength(2);
    expect(out.outputTokens).toBe(2580);
    expect(seen).toHaveLength(2);
    const s = seen[0]!;
    expect(s.path).toBe("/v1beta/models/gemini-2.5-flash-image:generateContent");
    expect(s.headers["x-goog-api-key"]).toBe(KEY);
    expect(s.headers.authorization).toBeUndefined();
    expect(s.json.generationConfig).toEqual({ responseModalities: ["TEXT", "IMAGE"], imageConfig: { aspectRatio: "16:9" } });
    expect(s.json.contents[0].parts[1].inlineData.mimeType).toBe("image/jpeg");
  });

  it("gemini: a blocked prompt says why", async () => {
    fallback = { status: 200, body: { promptFeedback: { blockReason: "SAFETY" } } };
    const err = await generateImages(target("/v1beta/openai"), { api: "gemini", model: "gemini-2.5-flash-image", prompt: "x", size: "", count: 1, inputs: [] }).catch((e) => e);
    expect(err.message).toContain("SAFETY");
  });

  it("imagen: predict with sampleCount and aspect ratio; refuses input images", async () => {
    fallback = { status: 200, body: { predictions: [{ bytesBase64Encoded: PNG64, mimeType: "image/png" }, { bytesBase64Encoded: PNG64 }] } };
    const out = await generateImages(target("/v1beta/openai"), { api: "imagen", model: "imagen-4.0-generate-001", prompt: "a fox", size: "1536x1024", count: 2, inputs: [] });
    expect(out.images).toHaveLength(2);
    expect(seen[0]!.path).toBe("/v1beta/models/imagen-4.0-generate-001:predict");
    expect(seen[0]!.json).toEqual({ instances: [{ prompt: "a fox" }], parameters: { sampleCount: 2, aspectRatio: "4:3" } });
    const err = await generateImages(target("/v1beta/openai"), { api: "imagen", model: "imagen-4.0-generate-001", prompt: "x", size: "", count: 1, inputs: [{ data: PNG, mediaType: "image/png" }] }).catch((e) => e);
    expect(err.type).toBe("invalid_request");
  });

  it("openrouter: chat completions with image output and its reported cost", async () => {
    fallback = {
      status: 200,
      body: { choices: [{ message: { content: "done", images: [{ type: "image_url", image_url: { url: `data:image/png;base64,${PNG64}` } }] } }], usage: { prompt_tokens: 5, completion_tokens: 1300, cost: 0.039 } },
    };
    const out = await generateImages(target("/api/v1"), { api: "openrouter", model: "google/gemini-2.5-flash-image", prompt: "a fox", size: "1024x1024", count: 1, inputs: [{ data: PNG, mediaType: "image/png" }] });
    expect(out.images).toHaveLength(1);
    expect(out.reportedCostUsd).toBe(0.039);
    expect(out.text).toBe("done");
    const s = seen[0]!;
    expect(s.path).toBe("/api/v1/chat/completions");
    expect(s.json.modalities).toEqual(["image", "text"]);
    expect(s.json.image_config).toEqual({ aspect_ratio: "1:1" });
    expect(s.json.messages[0].content[1].image_url.url).toMatch(/^data:image\/png;base64,/);
  });
});

describe("ImageService and GenerateImages", () => {
  let ts: TestServer;
  let adminToken: string;
  let adminCookie: string;
  let images: Client<typeof ImageService>;
  const imagesAs = (token: string) =>
    createClient(
      ImageService,
      createConnectTransport({
        baseUrl: ts.url,
        httpVersion: "1.1",
        interceptors: [
          (next) => async (req) => {
            req.header.set("authorization", `Bearer ${token}`);
            return next(req);
          },
        ],
      }),
    );

  beforeAll(async () => {
    ts = await startTestServer();
    const login = await deviceLogin(ts);
    adminToken = login.accessToken;
    adminCookie = login.cookie;
    images = imagesAs(adminToken);
    const api = (method: string, path: string, body?: unknown) => adminApi(ts.url, adminToken, method, path, body);
    expect((await api("POST", "/api/admin/providers", { id: "oai-fake", type: "openai-chat", base_url: `${base}/v1`, api_key: KEY, allow_private_network: true })).status).toBe(201);
    expect((await api("POST", "/api/admin/providers", { id: "ant-fake", type: "anthropic", base_url: base, api_key: KEY, allow_private_network: true })).status).toBe(201);
    expect((await api("POST", "/api/admin/models", { id: "chat-t", provider_id: "oai-fake", upstream_model: "gpt-up", context_window: 100000, max_output_tokens: 1000 })).status).toBe(201);
  });

  afterAll(async () => {
    await ts.close();
  });

  it("lists the providers with an image API, and admins add image models with an allowlist", async () => {
    const s = await images.getImageSettings({});
    expect(s.canManage).toBe(true);
    expect(s.providers.map((p) => p.id)).toEqual(["oai-fake"]);
    expect(s.roles).toContain("billing");
    const added = await images.addImageModel({ providerId: "oai-fake", upstreamModel: "dall-e-3", id: "img-std", displayName: "Images" });
    expect(added.model).toMatchObject({ id: "img-std", imageApi: "openai", edits: true, usable: true, editable: true });
    expect(added.model!.pricing).toMatchObject({ perImage: 0.04 });
    await images.addImageModel({ providerId: "oai-fake", upstreamModel: "gpt-image-1", id: "img-admins", roles: ["admin"] });
    await expect(images.addImageModel({ providerId: "ant-fake", upstreamModel: "x" })).rejects.toThrow(/no image generation API/);
    await expect(images.addImageModel({ providerId: "oai-fake", upstreamModel: "y", roles: ["wizard"] })).rejects.toThrow(/Unknown role/);
  });

  it("keeps image models out of the chat catalog and the chat gateway, and sets the default", async () => {
    await expect(images.setImageDefault({ modelId: "chat-t" })).rejects.toThrow(/not an image model/);
    expect((await images.setImageDefault({ modelId: "img-std" })).defaultModel).toBe("img-std");
    const gw = ts.as(adminToken).gateway;
    const cat = await gw.listModels({});
    expect(cat.models.map((m) => m.id)).toEqual(["chat-t"]);
    expect(cat.imageModels.map((m) => m.id)).toEqual(["img-std", "img-admins"]);
    expect(cat.slots?.image).toBe("img-std");
    expect(cat.slots?.main).toBe("chat-t");
    // ProviderService.SetSlots keeps the image slot.
    await ts.as(adminToken).providers.setSlots({ slots: { main: "chat-t" } });
    expect((await gw.listModels({})).slots?.image).toBe("img-std");
    const stream = gw.streamMessage({ model: "img-std", maxTokens: 10, messages: [{ role: Role.USER, content: [{ block: { case: "text", value: { text: "hi" } } }] }] });
    const err = await (async () => {
      for await (const _ of stream) void _;
    })().catch((e) => e as ConnectError);
    expect(String(err?.message)).toMatch(/image model/);
    await expect(gw.generateImages({ model: "chat-t", prompt: "x" })).rejects.toThrow(/not an image model/);
  });

  it("generates through the gateway and records the usage with its cost", async () => {
    fallback = { status: 200, body: { data: [{ b64_json: PNG64 }, { b64_json: PNG64 }] } };
    const gw = ts.as(adminToken).gateway;
    const r = await gw.generateImages({ model: "img-std", prompt: "a fox", size: "1024x1024", count: 2, metadata: { sessionId: "sess-img-1" } });
    expect(r.images).toHaveLength(2);
    expect(r.images[0]!.mediaType).toBe("image/png");
    expect(r.costUsd).toBeCloseTo(0.08);
    expect(r.priced).toBe(true);
    expect(r.servedModel).toBe("img-std");
    expect(seen.at(-1)!.json).toMatchObject({ model: "dall-e-3", n: 2, size: "1024x1024", response_format: "b64_json" });
    const usage = await adminApi(ts.url, adminToken, "GET", "/api/admin/usage?limit=5");
    expect(usage.status).toBe(200);
    const rec = usage.body.usage.find((u: any) => u.model === "img-std");
    expect(rec).toMatchObject({ request_class: "image", session_id: "sess-img-1", images: 2, priced: true, status: "ok" });
    expect(rec.cost_usd).toBeCloseTo(0.08);
  });

  it("validates the request before any upstream call", async () => {
    const gw = ts.as(adminToken).gateway;
    seen = [];
    await expect(gw.generateImages({ model: "img-std", prompt: "" })).rejects.toThrow(/prompt is empty/);
    await expect(gw.generateImages({ model: "img-std", prompt: "x", count: 9 })).rejects.toThrow(/count/);
    await expect(gw.generateImages({ model: "img-std", prompt: "x", size: "big" })).rejects.toThrow(/size/);
    await expect(gw.generateImages({ model: "img-std", prompt: "x", inputImages: [{ data: Buffer.from("not an image"), mediaType: "image/png" }] })).rejects.toThrow(/not a PNG/);
    expect(seen).toHaveLength(0);
  });

  it("applies the allowlist: a user sees and uses only the models their role may", async () => {
    const user = await createUserAndLogin(ts, adminCookie, "img-user@test.local");
    const mine = imagesAs(user.accessToken);
    const s = await mine.getImageSettings({});
    expect(s.canManage).toBe(false);
    expect(s.models.map((m) => m.id)).toEqual(["img-std"]);
    expect(s.models[0]!.roles).toEqual([]);
    const gw = ts.as(user.accessToken).gateway;
    expect((await gw.listModels({})).imageModels.map((m) => m.id)).toEqual(["img-std"]);
    await expect(gw.generateImages({ model: "img-admins", prompt: "x" })).rejects.toThrow(/not available/);
    await expect(mine.setImageDefault({ modelId: "img-std" })).rejects.toThrow(/permission/);
    await expect(mine.updateImageModel({ modelId: "img-std", enabled: false })).rejects.toThrow(/admins/);
    // Opening the allowlist to users makes the model theirs to use.
    await images.updateImageModel({ modelId: "img-admins", roles: { roles: [] } });
    expect((await gw.listModels({})).imageModels.map((m) => m.id)).toEqual(["img-std", "img-admins"]);
  });

  it("removing the default model clears the default", async () => {
    await images.removeImageModel({ modelId: "img-std" });
    const s = await images.getImageSettings({});
    expect(s.defaultModel).toBe("");
    expect(s.models.map((m) => m.id)).toEqual(["img-admins"]);
  });
});
