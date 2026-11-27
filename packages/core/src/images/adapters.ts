/**
 * Image generation APIs behind GatewayService.GenerateImages (docs/design/images.md). The API follows from the
 * model's provider (imageApiOf); request shapes follow the vendors' API references (November 2026):
 *
 *   openai      POST {base}/images/generations   JSON {model, prompt, n, size, quality}          Bearer
 *               POST {base}/images/edits         multipart image[] + prompt, model, n, size      Bearer
 *               Any OpenAI-compatible server (OpenAI, xAI, Together, a local LocalAI...) speaks this.
 *   gemini      POST {root}/models/{m}:generateContent  responseModalities TEXT+IMAGE, inlineData   x-goog-api-key
 *   imagen      POST {root}/models/{m}:predict   instances[{prompt}], parameters{sampleCount, aspectRatio}
 *   openrouter  POST {base}/chat/completions     modalities ["image","text"], message.images[]   Bearer
 *
 * {root} is the Gemini API root: the Gemini preset's OpenAI-compatible base URL without its "/openai" suffix. Every
 * request goes through the SSRF guard; error messages never carry the key and upstream bodies are cut short.
 */
import { FormData } from "undici";
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import { redactSecrets } from "../services/providers.ts";

export type ImageApi = "openai" | "gemini" | "imagen" | "openrouter";

/** The provider's side of a request (decrypted; never serialize). */
export interface ImageTarget {
  displayName: string;
  baseUrl: string;
  apiKey?: string;
  headers: Record<string, string>;
  allowPrivateNetwork: boolean;
  /** Until the whole answer arrived. Default 5 minutes: large images take a while. */
  timeoutMs?: number;
}

export interface ImageInput {
  data: Uint8Array;
  mediaType: string;
  name?: string;
}

export interface ImageRequest {
  api: ImageApi;
  model: string;
  prompt: string;
  /** "WxH", or "" for the API's default. */
  size: string;
  count: number;
  quality?: string;
  inputs: ImageInput[];
  signal?: AbortSignal;
}

export interface ImageOutput {
  images: { data: Uint8Array; mediaType: string }[];
  revisedPrompt?: string;
  text?: string;
  inputTokens: number;
  outputTokens: number;
  /** USD, when the upstream reports what the request cost (OpenRouter). */
  reportedCostUsd?: number;
}

/** The provider fields imageApiOf reads. */
export interface ProviderShape {
  type: string;
  baseUrl: string;
  presetId?: string | null;
}

/** Which image API a provider speaks, "" when it has none (Anthropic and the cloud platforms of Anthropic models). */
export function providerImageApi(p: ProviderShape): "openai" | "gemini" | "openrouter" | "" {
  const host = hostOf(p.baseUrl);
  if (p.presetId === "gemini" || host === "generativelanguage.googleapis.com") return "gemini";
  if (p.presetId === "openrouter" || host === "openrouter.ai") return "openrouter";
  if (p.type === "openai-chat" || p.type === "openai-responses") return "openai";
  return "";
}

/** The API a model of the provider is called through: Imagen models of the Gemini API use its predict method. */
export function imageApiOf(p: ProviderShape, upstreamModel: string): ImageApi | "" {
  const api = providerImageApi(p);
  if (api === "gemini" && /^(models\/)?imagen/i.test(upstreamModel)) return "imagen";
  return api;
}

/** Whether the API takes input images: Imagen generates from text only. */
export function apiTakesInputs(api: ImageApi): boolean {
  return api !== "imagen";
}

function hostOf(url: string): string {
  try {
    return new URL(url).hostname.toLowerCase();
  } catch {
    return "";
  }
}

const ASPECTS = ["1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"];
const IMAGEN_ASPECTS = ["1:1", "3:4", "4:3", "9:16", "16:9"];

/** "WxH" as the nearest aspect ratio of `allowed` (by log ratio); undefined for no size. */
export function aspectOf(size: string, allowed = ASPECTS): string | undefined {
  const m = /^(\d+)x(\d+)$/.exec(size);
  if (!m) return undefined;
  const want = Math.log(Number(m[1]) / Number(m[2]));
  let best = allowed[0]!;
  let gap = Infinity;
  for (const a of allowed) {
    const [w, h] = a.split(":").map(Number) as [number, number];
    const d = Math.abs(Math.log(w / h) - want);
    if (d < gap) {
      best = a;
      gap = d;
    }
  }
  return best;
}

type Json = Record<string, any>;

const DEFAULT_TIMEOUT_MS = 300_000;
const ERROR_EXCERPT = 300;
/** Largest image accepted from an upstream. */
export const MAX_OUTPUT_IMAGE_BYTES = 50 << 20;

const join = (base: string, path: string) => `${base.replace(/\/+$/, "")}${path}`;

function errorDetail(text: string): string {
  if (!text) return "";
  try {
    const j = JSON.parse(text) as Json;
    const e = Array.isArray(j) ? j[0]?.error : j.error;
    const m = e?.message ?? j.message ?? e ?? j.detail;
    if (typeof m === "string") return m.slice(0, ERROR_EXCERPT);
    if (m) return JSON.stringify(m).slice(0, ERROR_EXCERPT);
  } catch {
    // not JSON
  }
  return /^\s*</.test(text) ? "" : text.trim().slice(0, ERROR_EXCERPT);
}

function authHeaders(t: ImageTarget, api: ImageApi): Record<string, string> {
  if (!t.apiKey) return {};
  return api === "gemini" || api === "imagen" ? { "x-goog-api-key": t.apiKey } : { authorization: `Bearer ${t.apiKey}` };
}

async function call(t: ImageTarget, api: ImageApi, target: string, init: { headers?: Record<string, string>; body: any; signal?: AbortSignal }): Promise<Json> {
  const name = t.displayName;
  const scrub = (s: string) => redactSecrets(s, { apiKey: t.apiKey, headers: t.headers });
  const timeout = new AbortController();
  const timer = setTimeout(() => timeout.abort(), t.timeoutMs ?? DEFAULT_TIMEOUT_MS);
  const signal = init.signal ? AbortSignal.any([init.signal, timeout.signal]) : timeout.signal;
  try {
    let res;
    try {
      res = await guardedFetch(target, { method: "POST", headers: { ...t.headers, ...authHeaders(t, api), ...init.headers }, body: init.body, signal, allowPrivateNetwork: t.allowPrivateNetwork });
    } catch (err) {
      if (err instanceof ApiError) throw new ApiError(err.type, scrub(err.message), { upstreamStatus: err.upstreamStatus });
      if (timeout.signal.aborted) throw new ApiError("upstream", `${name} did not answer in time`);
      if (init.signal?.aborted) throw new ApiError("upstream", "The request was cancelled");
      throw new ApiError("upstream", scrub(`${name} is unreachable: ${(err as Error).message}`));
    }
    if (!res.ok) {
      const detail = errorDetail(await res.text().catch(() => ""));
      const suffix = detail ? `: ${scrub(detail)}` : "";
      const status = res.status;
      if (status === 401 || status === 403) throw new ApiError("upstream", `${name} rejected the API key (HTTP ${status})${suffix}`, { upstreamStatus: status });
      if (status === 402) throw new ApiError("quota_exceeded", `${name}: the account's credits are used up (HTTP ${status})${suffix}`, { upstreamStatus: status });
      if (status === 429) {
        const after = Number(res.headers.get("retry-after"));
        throw new ApiError("rate_limit", `${name} is rate limiting requests${suffix}`, { upstreamStatus: status, retryAfterMs: Number.isFinite(after) && after > 0 ? after * 1000 : 5_000 });
      }
      if (status === 400 || status === 404 || status === 413 || status === 415 || status === 422) {
        throw new ApiError("invalid_request", `${name} refused the request (HTTP ${status})${suffix}`, { upstreamStatus: status });
      }
      throw new ApiError("upstream", `${name} returned HTTP ${status}${suffix}`, { upstreamStatus: status });
    }
    try {
      return (await res.json()) as Json;
    } catch {
      if (timeout.signal.aborted) throw new ApiError("upstream", `${name} did not answer in time`);
      throw new ApiError("upstream", `${name} returned a response that is not JSON`);
    }
  } finally {
    clearTimeout(timer);
  }
}

const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : 0);

/** Sniffs PNG, JPEG, WebP and GIF; undefined for anything else (never trusted from a header alone). */
export function sniffImage(b: Uint8Array): string | undefined {
  if (b.length >= 8 && b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47) return "image/png";
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "image/jpeg";
  if (b.length >= 12 && b[0] === 0x52 && b[1] === 0x49 && b[2] === 0x46 && b[3] === 0x46 && b[8] === 0x57 && b[9] === 0x45 && b[10] === 0x42 && b[11] === 0x50) return "image/webp";
  if (b.length >= 6 && b[0] === 0x47 && b[1] === 0x49 && b[2] === 0x46) return "image/gif";
  return undefined;
}

function decodeImage(t: ImageTarget, b64: string): { data: Uint8Array; mediaType: string } {
  if (b64.length > (MAX_OUTPUT_IMAGE_BYTES * 4) / 3 + 4) throw new ApiError("upstream", `${t.displayName} returned an image larger than ${MAX_OUTPUT_IMAGE_BYTES >> 20} MiB`);
  const data = new Uint8Array(Buffer.from(b64, "base64"));
  const mediaType = sniffImage(data);
  if (!mediaType) throw new ApiError("upstream", `${t.displayName} returned something that is not a PNG, JPEG, WebP or GIF image`);
  return { data, mediaType };
}

/** data:image/png;base64,... */
function fromDataUrl(t: ImageTarget, url: string): { data: Uint8Array; mediaType: string } | undefined {
  const m = /^data:[^;,]*;base64,(.*)$/s.exec(url);
  return m ? decodeImage(t, m[1]!) : undefined;
}

/** A hosted result URL (dall-e answers URLs by default): fetched through the SSRF guard, with no credentials. */
async function fetchImage(t: ImageTarget, url: string, signal?: AbortSignal): Promise<{ data: Uint8Array; mediaType: string }> {
  const res = await guardedFetch(url, { method: "GET", signal, allowPrivateNetwork: t.allowPrivateNetwork }).catch((err: Error) => {
    throw new ApiError("upstream", `Could not download the image ${t.displayName} returned: ${err.message}`);
  });
  if (!res.ok) throw new ApiError("upstream", `Could not download the image ${t.displayName} returned (HTTP ${res.status})`);
  const buf = new Uint8Array(await res.arrayBuffer());
  if (buf.byteLength > MAX_OUTPUT_IMAGE_BYTES) throw new ApiError("upstream", `${t.displayName} returned an image larger than ${MAX_OUTPUT_IMAGE_BYTES >> 20} MiB`);
  const mediaType = sniffImage(buf);
  if (!mediaType) throw new ApiError("upstream", `${t.displayName} returned something that is not an image`);
  return { data: buf, mediaType };
}

export async function generateImages(t: ImageTarget, req: ImageRequest): Promise<ImageOutput> {
  switch (req.api) {
    case "openai":
      return req.inputs.length ? openaiEdit(t, req) : openaiGenerate(t, req);
    case "gemini":
      return gemini(t, req);
    case "imagen":
      return imagen(t, req);
    case "openrouter":
      return openrouter(t, req);
  }
}

// --- OpenAI-compatible ----------------------------------------------------------------------------------------

/** dall-e answers URLs unless asked for base64; gpt-image always answers base64 and refuses response_format. */
const wantsResponseFormat = (model: string) => /dall-e/i.test(model);

async function openaiResult(t: ImageTarget, j: Json, signal?: AbortSignal): Promise<ImageOutput> {
  const data = Array.isArray(j.data) ? j.data : [];
  const images = [];
  let revised: string | undefined;
  for (const d of data) {
    if (typeof d?.b64_json === "string") images.push(decodeImage(t, d.b64_json));
    else if (typeof d?.url === "string") images.push(fromDataUrl(t, d.url) ?? (await fetchImage(t, d.url, signal)));
    if (!revised && typeof d?.revised_prompt === "string") revised = d.revised_prompt;
  }
  if (!images.length) throw new ApiError("upstream", `${t.displayName} returned no image`);
  return { images, revisedPrompt: revised, inputTokens: num(j.usage?.input_tokens), outputTokens: num(j.usage?.output_tokens) };
}

async function openaiGenerate(t: ImageTarget, req: ImageRequest): Promise<ImageOutput> {
  const body: Json = { model: req.model, prompt: req.prompt, n: req.count };
  if (req.size) body.size = req.size;
  if (req.quality) body.quality = req.quality;
  if (wantsResponseFormat(req.model)) body.response_format = "b64_json";
  const j = await call(t, "openai", join(t.baseUrl, "/images/generations"), { headers: { "content-type": "application/json" }, body: JSON.stringify(body), signal: req.signal });
  return openaiResult(t, j, req.signal);
}

const extOf = (mediaType: string) => (mediaType === "image/jpeg" ? "jpg" : mediaType.split("/")[1] || "png");

async function openaiEdit(t: ImageTarget, req: ImageRequest): Promise<ImageOutput> {
  const form = new FormData();
  form.set("model", req.model);
  form.set("prompt", req.prompt);
  form.set("n", String(req.count));
  if (req.size) form.set("size", req.size);
  if (req.quality) form.set("quality", req.quality);
  if (wantsResponseFormat(req.model)) form.set("response_format", "b64_json");
  // gpt-image takes several images as image[]; dall-e-2 takes one as image.
  const field = req.inputs.length > 1 || !/dall-e/i.test(req.model) ? "image[]" : "image";
  req.inputs.forEach((img, i) => {
    form.append(field, new Blob([img.data as Uint8Array<ArrayBuffer>], { type: img.mediaType }), img.name || `image-${i + 1}.${extOf(img.mediaType)}`);
  });
  const j = await call(t, "openai", join(t.baseUrl, "/images/edits"), { body: form, signal: req.signal });
  return openaiResult(t, j, req.signal);
}

// --- Gemini API -----------------------------------------------------------------------------------------------

/** The Gemini API root for native methods: the OpenAI-compatible base URL of the preset without "/openai". */
export function geminiRoot(baseUrl: string): string {
  return baseUrl.replace(/\/+$/, "").replace(/\/openai$/, "");
}

const modelPath = (model: string) => (model.startsWith("models/") ? model : `models/${model}`);

async function geminiOnce(t: ImageTarget, req: ImageRequest): Promise<ImageOutput> {
  const parts: Json[] = [{ text: req.prompt }];
  for (const img of req.inputs) parts.push({ inlineData: { mimeType: img.mediaType, data: Buffer.from(img.data).toString("base64") } });
  const generationConfig: Json = { responseModalities: ["TEXT", "IMAGE"] };
  const aspect = aspectOf(req.size);
  if (aspect) generationConfig.imageConfig = { aspectRatio: aspect };
  const url = join(geminiRoot(t.baseUrl), `/${modelPath(req.model)}:generateContent`);
  const j = await call(t, "gemini", url, { headers: { "content-type": "application/json" }, body: JSON.stringify({ contents: [{ role: "user", parts }], generationConfig }), signal: req.signal });
  const images = [];
  const texts: string[] = [];
  for (const c of Array.isArray(j.candidates) ? j.candidates : []) {
    for (const p of Array.isArray(c?.content?.parts) ? c.content.parts : []) {
      const inline = p?.inlineData ?? p?.inline_data;
      if (typeof inline?.data === "string") images.push(decodeImage(t, inline.data));
      else if (typeof p?.text === "string" && !p.thought) texts.push(p.text);
    }
  }
  if (!images.length) {
    const why = j.promptFeedback?.blockReason ?? j.candidates?.[0]?.finishReason;
    throw new ApiError("upstream", `${t.displayName} returned no image${why ? ` (${why})` : ""}${texts.length ? `: ${texts.join(" ").slice(0, ERROR_EXCERPT)}` : ""}`);
  }
  const u = j.usageMetadata ?? {};
  return { images, text: texts.join("\n").trim() || undefined, inputTokens: num(u.promptTokenCount), outputTokens: num(u.candidatesTokenCount) };
}

async function imagen(t: ImageTarget, req: ImageRequest): Promise<ImageOutput> {
  if (req.inputs.length) throw new ApiError("invalid_request", `${req.model} generates from text only; it takes no input images`);
  const parameters: Json = { sampleCount: req.count };
  const aspect = aspectOf(req.size, IMAGEN_ASPECTS);
  if (aspect) parameters.aspectRatio = aspect;
  const url = join(geminiRoot(t.baseUrl), `/${modelPath(req.model)}:predict`);
  const j = await call(t, "imagen", url, { headers: { "content-type": "application/json" }, body: JSON.stringify({ instances: [{ prompt: req.prompt }], parameters }), signal: req.signal });
  const images = [];
  for (const p of Array.isArray(j.predictions) ? j.predictions : []) {
    if (typeof p?.bytesBase64Encoded === "string") images.push(decodeImage(t, p.bytesBase64Encoded));
  }
  if (!images.length) throw new ApiError("upstream", `${t.displayName} returned no image (the prompt may have been filtered)`);
  return { images, inputTokens: 0, outputTokens: 0 };
}

/** Gemini image models answer one image per request (no candidateCount for images): several take several requests. */
async function gemini(t: ImageTarget, req: ImageRequest): Promise<ImageOutput> {
  return repeat(req, () => geminiOnce(t, req));
}

// --- OpenRouter -----------------------------------------------------------------------------------------------

async function openrouterOnce(t: ImageTarget, req: ImageRequest): Promise<ImageOutput> {
  const content: Json[] = [{ type: "text", text: req.prompt }];
  for (const img of req.inputs) content.push({ type: "image_url", image_url: { url: `data:${img.mediaType};base64,${Buffer.from(img.data).toString("base64")}` } });
  const body: Json = { model: req.model, messages: [{ role: "user", content }], modalities: ["image", "text"], stream: false, usage: { include: true } };
  const aspect = aspectOf(req.size);
  if (aspect) body.image_config = { aspect_ratio: aspect };
  const j = await call(t, "openrouter", join(t.baseUrl, "/chat/completions"), { headers: { "content-type": "application/json" }, body: JSON.stringify(body), signal: req.signal });
  const msg = j.choices?.[0]?.message ?? {};
  const images = [];
  for (const im of Array.isArray(msg.images) ? msg.images : []) {
    const url = im?.image_url?.url ?? im?.url;
    if (typeof url !== "string") continue;
    images.push(fromDataUrl(t, url) ?? (await fetchImage(t, url, req.signal)));
  }
  const text = typeof msg.content === "string" ? msg.content.trim() : "";
  if (!images.length) throw new ApiError("upstream", `${t.displayName} returned no image${text ? `: ${text.slice(0, ERROR_EXCERPT)}` : ""}`);
  const cost = j.usage?.cost;
  return {
    images,
    text: text || undefined,
    inputTokens: num(j.usage?.prompt_tokens),
    outputTokens: num(j.usage?.completion_tokens),
    reportedCostUsd: typeof cost === "number" && Number.isFinite(cost) && cost >= 0 ? cost : undefined,
  };
}

/** Chat completions answer one message: several images take several requests. */
async function openrouter(t: ImageTarget, req: ImageRequest): Promise<ImageOutput> {
  return repeat(req, () => openrouterOnce(t, req));
}

/** Requests one after another (so a failure stops early) until `count` images came back. */
async function repeat(req: ImageRequest, once: () => Promise<ImageOutput>): Promise<ImageOutput> {
  const out: ImageOutput = { images: [], inputTokens: 0, outputTokens: 0 };
  const texts: string[] = [];
  for (let i = 0; i < req.count && out.images.length < req.count; i++) {
    const r = await once();
    out.images.push(...r.images.slice(0, req.count - out.images.length));
    out.inputTokens += r.inputTokens;
    out.outputTokens += r.outputTokens;
    if (r.reportedCostUsd !== undefined) out.reportedCostUsd = (out.reportedCostUsd ?? 0) + r.reportedCostUsd;
    if (r.text) texts.push(r.text);
  }
  if (texts.length) out.text = texts.join("\n");
  return out;
}
