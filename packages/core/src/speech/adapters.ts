/**
 * Hosted speech APIs behind SpeechService: one transcribe and one synthesize implementation per vendor of
 * src/speech/catalog.ts. Request shapes follow the vendors' API references (November 2026):
 *
 * Speech to text
 *   openai, groq, mistral  POST {base}/audio/transcriptions   multipart file+model(+language,prompt)  Bearer
 *   deepgram               POST {base}/listen?model=…          raw audio body                          Token
 *   elevenlabs             POST {base}/speech-to-text          multipart file+model_id                 xi-api-key
 *   assemblyai             POST {base}/upload, POST {base}/transcript, then GET {base}/transcript/{id} until done
 *   openrouter             POST {base}/chat/completions        input_audio content part (wav / mp3)    Bearer
 * Text to speech
 *   openai      POST {base}/audio/speech                        mp3 bytes, streamed
 *   elevenlabs  POST {base}/text-to-speech/{voice}/stream        mp3 bytes, streamed;  GET {base}/voices
 *   deepgram    POST {base}/speak?model=<model>-<voice>          mp3 bytes, streamed
 *   minimax     POST {base}/t2a_v2                               JSON with hex-encoded mp3
 *   gemini      POST {base}/models/{model}:generateContent       JSON with base64 PCM (16-bit, 24 kHz)
 *   cartesia    POST {base}/tts/bytes                            mp3 bytes, streamed;  GET {base}/voices
 *
 * Every request goes through the SSRF guard. Error messages never carry the key (redactSecrets) and upstream
 * bodies are cut to a short excerpt.
 */
import { FormData } from "undici";
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import { redactSecrets } from "../services/providers.ts";
import type { CatalogVoice, SpeechVendor } from "./catalog.ts";
import { audioFormatOf, baseMime, fileNameFor } from "./text.ts";

export interface SpeechConfig {
  vendor: SpeechVendor;
  displayName: string;
  baseUrl: string;
  apiKey: string;
  allowPrivateNetwork: boolean;
  /** Until the response headers (and whole JSON bodies) arrive. Default 120 s. */
  timeoutMs?: number;
  /** AssemblyAI: interval between polls. Default 1 s. */
  pollIntervalMs?: number;
}

export interface TranscribeInput {
  model: string;
  audio: Uint8Array;
  mimeType: string;
  language?: string;
  prompt?: string;
  signal?: AbortSignal;
}

export interface TranscribeOutput {
  text: string;
  language?: string;
  durationSeconds?: number;
}

export interface SynthesizeInput {
  model: string;
  voice: string;
  text: string;
  /** 1 is normal. */
  speed: number;
  language?: string;
  signal?: AbortSignal;
}

/** One synthesized piece: MP3 bytes in order, or raw 16-bit PCM (pcmRate set) that the service wraps into a WAV. */
export interface SynthesizedPiece {
  chunks: AsyncIterable<Uint8Array>;
  pcmRate?: number;
}

type Json = Record<string, any>;

const DEFAULT_TIMEOUT_MS = 120_000;
const ERROR_EXCERPT = 300;

function bearer(key: string) {
  return { authorization: `Bearer ${key}` };
}

function authHeaders(cfg: SpeechConfig): Record<string, string> {
  switch (cfg.vendor) {
    case "deepgram":
      return { authorization: `Token ${cfg.apiKey}` };
    case "elevenlabs":
      return { "xi-api-key": cfg.apiKey };
    case "assemblyai":
      return { authorization: cfg.apiKey };
    case "gemini":
      return { "x-goog-api-key": cfg.apiKey };
    case "cartesia":
      return { "x-api-key": cfg.apiKey, "cartesia-version": "2025-04-16" };
    default:
      return bearer(cfg.apiKey);
  }
}

const url = (cfg: SpeechConfig, path: string) => `${cfg.baseUrl.replace(/\/+$/, "")}${path}`;

/** Pulls a readable message out of an upstream error body. */
function errorDetail(text: string): string {
  if (!text) return "";
  try {
    const j = JSON.parse(text) as Json;
    const m = j.error?.message ?? j.detail?.message ?? j.detail?.status ?? j.err_msg ?? j.message ?? j.error ?? j.detail ?? j.base_resp?.status_msg;
    if (typeof m === "string") return m.slice(0, ERROR_EXCERPT);
    if (m) return JSON.stringify(m).slice(0, ERROR_EXCERPT);
  } catch {
    // not JSON
  }
  return /^\s*</.test(text) ? "" : text.trim().slice(0, ERROR_EXCERPT);
}

/**
 * One request with the SSRF guard and error mapping. The timeout covers the response headers; a streamed body
 * is bounded only by the caller's signal.
 */
async function call(cfg: SpeechConfig, target: string, init: { method: string; headers?: Record<string, string>; body?: any; signal?: AbortSignal }) {
  const name = cfg.displayName;
  const scrub = (t: string) => redactSecrets(t, { apiKey: cfg.apiKey, headers: {} });
  const timeout = new AbortController();
  const timer = setTimeout(() => timeout.abort(), cfg.timeoutMs ?? DEFAULT_TIMEOUT_MS);
  const signal = init.signal ? AbortSignal.any([init.signal, timeout.signal]) : timeout.signal;
  let res;
  try {
    res = await guardedFetch(target, { method: init.method, headers: { ...authHeaders(cfg), ...init.headers }, body: init.body, signal, allowPrivateNetwork: cfg.allowPrivateNetwork });
  } catch (err) {
    clearTimeout(timer);
    if (err instanceof ApiError) throw new ApiError(err.type, scrub(err.message), { upstreamStatus: err.upstreamStatus });
    if (timeout.signal.aborted) throw new ApiError("upstream", `${name} did not answer in time`);
    if (init.signal?.aborted) throw new ApiError("upstream", "The request was cancelled");
    throw new ApiError("upstream", scrub(`${name} is unreachable: ${(err as Error).message}`));
  }
  clearTimeout(timer);
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    const detail = errorDetail(text);
    const suffix = detail ? `: ${scrub(detail)}` : "";
    const status = res.status;
    if (status === 401 || status === 403) throw new ApiError("upstream", `${name} rejected the API key (HTTP ${status})${suffix}`, { upstreamStatus: status });
    if (status === 402) throw new ApiError("quota_exceeded", `${name}: the account's credits are used up (HTTP ${status})${suffix}`, { upstreamStatus: status });
    if (status === 429) {
      const after = Number(res.headers.get("retry-after"));
      throw new ApiError("rate_limit", `${name} is rate limiting requests${suffix}`, { upstreamStatus: status, retryAfterMs: Number.isFinite(after) && after > 0 ? after * 1000 : 5_000 });
    }
    if (status === 400 || status === 413 || status === 415 || status === 422) {
      throw new ApiError("invalid_request", `${name} refused the request (HTTP ${status})${suffix}`, { upstreamStatus: status });
    }
    throw new ApiError("upstream", `${name} returned HTTP ${status}${suffix}`, { upstreamStatus: status });
  }
  return res;
}

async function callJson(cfg: SpeechConfig, target: string, init: { method: string; headers?: Record<string, string>; body?: any; signal?: AbortSignal }): Promise<Json> {
  const res = await call(cfg, target, init);
  try {
    return (await res.json()) as Json;
  } catch {
    throw new ApiError("upstream", `${cfg.displayName} returned a response that is not JSON`);
  }
}

const jsonBody = (body: unknown) => ({ headers: { "content-type": "application/json" }, body: JSON.stringify(body) });

function audioBlob(input: TranscribeInput): Blob {
  return new Blob([input.audio as Uint8Array<ArrayBuffer>], { type: baseMime(input.mimeType) || "application/octet-stream" });
}

const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
const str = (v: unknown) => (typeof v === "string" && v ? v : undefined);

// ---------------------------------------------------------------------------------------------------------------
// Speech to text
// ---------------------------------------------------------------------------------------------------------------

export async function transcribe(cfg: SpeechConfig, input: TranscribeInput): Promise<TranscribeOutput> {
  switch (cfg.vendor) {
    case "openai":
    case "groq":
    case "mistral":
      return transcribeOpenAI(cfg, input);
    case "deepgram":
      return transcribeDeepgram(cfg, input);
    case "elevenlabs":
      return transcribeElevenLabs(cfg, input);
    case "assemblyai":
      return transcribeAssemblyAI(cfg, input);
    case "openrouter":
      return transcribeOpenRouter(cfg, input);
    default:
      throw new ApiError("invalid_request", `${cfg.displayName} does not offer speech to text`);
  }
}

async function transcribeOpenAI(cfg: SpeechConfig, input: TranscribeInput): Promise<TranscribeOutput> {
  const form = new FormData();
  form.set("file", audioBlob(input), fileNameFor(input.mimeType));
  form.set("model", input.model);
  if (input.language) form.set("language", input.language);
  // Whisper models report language and duration with verbose_json; the GPT-4o ones only take json.
  const whisper = cfg.vendor === "groq" || input.model.startsWith("whisper");
  if (cfg.vendor !== "mistral") {
    form.set("response_format", whisper ? "verbose_json" : "json");
    if (input.prompt) form.set("prompt", input.prompt);
  }
  const j = await callJson(cfg, url(cfg, "/audio/transcriptions"), { method: "POST", body: form, signal: input.signal });
  return {
    text: String(j.text ?? "").trim(),
    language: str(j.language),
    durationSeconds: num(j.duration) ?? num(j.usage?.prompt_audio_seconds) ?? num(j.usage?.seconds),
  };
}

async function transcribeDeepgram(cfg: SpeechConfig, input: TranscribeInput): Promise<TranscribeOutput> {
  const q = new URLSearchParams({ model: input.model, smart_format: "true", punctuate: "true" });
  if (input.language) q.set("language", input.language);
  else q.set("detect_language", "true");
  if (input.prompt && input.model.startsWith("nova-3")) {
    for (const term of input.prompt.split(/[,\n]/).map((t) => t.trim()).filter(Boolean).slice(0, 50)) q.append("keyterm", term);
  }
  const j = await callJson(cfg, url(cfg, `/listen?${q}`), {
    method: "POST",
    headers: { "content-type": baseMime(input.mimeType) || "application/octet-stream" },
    body: input.audio,
    signal: input.signal,
  });
  const ch = j.results?.channels?.[0];
  return { text: String(ch?.alternatives?.[0]?.transcript ?? "").trim(), language: str(ch?.detected_language) ?? input.language, durationSeconds: num(j.metadata?.duration) };
}

async function transcribeElevenLabs(cfg: SpeechConfig, input: TranscribeInput): Promise<TranscribeOutput> {
  const form = new FormData();
  form.set("file", audioBlob(input), fileNameFor(input.mimeType));
  form.set("model_id", input.model);
  if (input.language) form.set("language_code", input.language);
  form.set("tag_audio_events", "false");
  const j = await callJson(cfg, url(cfg, "/speech-to-text"), { method: "POST", body: form, signal: input.signal });
  const words = Array.isArray(j.words) ? j.words : [];
  return { text: String(j.text ?? "").trim(), language: str(j.language_code), durationSeconds: num(words[words.length - 1]?.end) };
}

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => (clearTimeout(t), reject(new ApiError("upstream", "The request was cancelled"))), { once: true });
  });

/** AssemblyAI transcribes asynchronously: upload, create the transcript, poll until it completes (at most 10 minutes). */
async function transcribeAssemblyAI(cfg: SpeechConfig, input: TranscribeInput): Promise<TranscribeOutput> {
  const up = await callJson(cfg, url(cfg, "/upload"), { method: "POST", headers: { "content-type": "application/octet-stream" }, body: input.audio, signal: input.signal });
  const audioUrl = str(up.upload_url);
  if (!audioUrl) throw new ApiError("upstream", `${cfg.displayName} returned no upload URL`);
  const body: Json = { audio_url: audioUrl, speech_model: input.model, punctuate: true, format_text: true };
  if (input.language) body.language_code = input.language;
  else body.language_detection = true;
  if (input.prompt) body.word_boost = input.prompt.split(/[,\n]/).map((t) => t.trim()).filter(Boolean).slice(0, 100);
  const created = await callJson(cfg, url(cfg, "/transcript"), { method: "POST", ...jsonBody(body), signal: input.signal });
  const id = str(created.id);
  if (!id) throw new ApiError("upstream", `${cfg.displayName} returned no transcript id`);
  const deadline = Date.now() + 10 * 60_000;
  let t: Json = created;
  while (t.status !== "completed") {
    if (t.status === "error") throw new ApiError("upstream", `${cfg.displayName} could not transcribe the audio: ${String(t.error ?? "unknown error").slice(0, ERROR_EXCERPT)}`);
    if (Date.now() > deadline) throw new ApiError("upstream", `${cfg.displayName} did not finish the transcript in time`);
    await sleep(cfg.pollIntervalMs ?? 1_000, input.signal);
    t = await callJson(cfg, url(cfg, `/transcript/${encodeURIComponent(id)}`), { method: "GET", signal: input.signal });
  }
  return { text: String(t.text ?? "").trim(), language: str(t.language_code), durationSeconds: num(t.audio_duration) };
}

async function transcribeOpenRouter(cfg: SpeechConfig, input: TranscribeInput): Promise<TranscribeOutput> {
  let instruction = "Transcribe this audio verbatim. Reply with the transcript only: no introduction, no quotes, no commentary. If there is no speech, reply with nothing.";
  if (input.language) instruction += ` The speech is in ${input.language}.`;
  if (input.prompt) instruction += ` Context (names and terms that may occur): ${input.prompt.slice(0, 1000)}`;
  const j = await callJson(cfg, url(cfg, "/chat/completions"), {
    method: "POST",
    ...jsonBody({
      model: input.model,
      temperature: 0,
      messages: [
        {
          role: "user",
          content: [
            { type: "text", text: instruction },
            { type: "input_audio", input_audio: { data: Buffer.from(input.audio).toString("base64"), format: audioFormatOf(input.mimeType) } },
          ],
        },
      ],
    }),
    signal: input.signal,
  });
  const content = j.choices?.[0]?.message?.content;
  const text = typeof content === "string" ? content : Array.isArray(content) ? content.map((p: Json) => (typeof p?.text === "string" ? p.text : "")).join("") : "";
  return { text: text.trim(), language: input.language };
}

// ---------------------------------------------------------------------------------------------------------------
// Text to speech
// ---------------------------------------------------------------------------------------------------------------

/** The MIME type of what a vendor's synthesis yields once the service assembled it. */
export function outputMimeOf(vendor: SpeechVendor): "audio/mpeg" | "audio/wav" {
  return vendor === "gemini" ? "audio/wav" : "audio/mpeg";
}

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

async function* streamBody(res: Awaited<ReturnType<typeof call>>): AsyncGenerator<Uint8Array> {
  if (!res.body) return;
  for await (const chunk of res.body as AsyncIterable<Uint8Array>) if (chunk.byteLength) yield chunk;
}

async function* once(bytes: Uint8Array): AsyncGenerator<Uint8Array> {
  if (bytes.byteLength) yield bytes;
}

/** Synthesizes one piece of text (at most the vendor's max_chars). */
export async function synthesize(cfg: SpeechConfig, input: SynthesizeInput): Promise<SynthesizedPiece> {
  const speed = input.speed || 1;
  switch (cfg.vendor) {
    case "openai": {
      const body: Json = { model: input.model, input: input.text, voice: input.voice, response_format: "mp3" };
      if (speed !== 1) body.speed = clamp(speed, 0.25, 4);
      return { chunks: streamBody(await call(cfg, url(cfg, "/audio/speech"), { method: "POST", ...jsonBody(body), signal: input.signal })) };
    }
    case "elevenlabs": {
      const body: Json = { text: input.text, model_id: input.model };
      if (speed !== 1) body.voice_settings = { speed: clamp(speed, 0.7, 1.2) };
      if (input.language && /_v2_5$/.test(input.model)) body.language_code = input.language;
      const target = url(cfg, `/text-to-speech/${encodeURIComponent(input.voice)}/stream?output_format=mp3_44100_128`);
      return { chunks: streamBody(await call(cfg, target, { method: "POST", ...jsonBody(body), signal: input.signal })) };
    }
    case "deepgram": {
      const q = new URLSearchParams({ model: `${input.model}-${input.voice}`, encoding: "mp3" });
      return { chunks: streamBody(await call(cfg, url(cfg, `/speak?${q}`), { method: "POST", ...jsonBody({ text: input.text }), signal: input.signal })) };
    }
    case "cartesia": {
      const body: Json = {
        model_id: input.model,
        transcript: input.text,
        voice: { mode: "id", id: input.voice },
        output_format: { container: "mp3", sample_rate: 44100, bit_rate: 128000 },
      };
      if (input.language) body.language = input.language;
      if (speed < 0.85) body.speed = "slow";
      else if (speed > 1.15) body.speed = "fast";
      return { chunks: streamBody(await call(cfg, url(cfg, "/tts/bytes"), { method: "POST", ...jsonBody(body), signal: input.signal })) };
    }
    case "minimax": {
      const body: Json = {
        model: input.model,
        text: input.text,
        stream: false,
        voice_setting: { voice_id: input.voice, speed: clamp(speed, 0.5, 2), vol: 1, pitch: 0 },
        audio_setting: { sample_rate: 32000, bitrate: 128000, format: "mp3", channel: 1 },
        language_boost: "auto",
      };
      const j = await callJson(cfg, url(cfg, "/t2a_v2"), { method: "POST", ...jsonBody(body), signal: input.signal });
      const code = Number(j.base_resp?.status_code ?? 0);
      if (code !== 0) {
        const msg = redactSecrets(String(j.base_resp?.status_msg ?? "error"), { apiKey: cfg.apiKey, headers: {} }).slice(0, ERROR_EXCERPT);
        if (code === 1004 || code === 2049) throw new ApiError("upstream", `${cfg.displayName} rejected the API key: ${msg}`);
        if (code === 1008) throw new ApiError("quota_exceeded", `${cfg.displayName}: the account's balance is used up: ${msg}`);
        if (code === 1002 || code === 1039) throw new ApiError("rate_limit", `${cfg.displayName} is rate limiting requests: ${msg}`, { retryAfterMs: 5_000 });
        throw new ApiError("upstream", `${cfg.displayName} could not synthesize the text (${code}): ${msg}`);
      }
      const hex = str(j.data?.audio);
      if (!hex) throw new ApiError("upstream", `${cfg.displayName} returned no audio`);
      return { chunks: once(new Uint8Array(Buffer.from(hex, "hex"))) };
    }
    case "gemini": {
      const body = {
        contents: [{ parts: [{ text: input.text }] }],
        generationConfig: { responseModalities: ["AUDIO"], speechConfig: { voiceConfig: { prebuiltVoiceConfig: { voiceName: input.voice } } } },
      };
      const j = await callJson(cfg, url(cfg, `/models/${encodeURIComponent(input.model)}:generateContent`), { method: "POST", ...jsonBody(body), signal: input.signal });
      const part = (j.candidates?.[0]?.content?.parts as Json[] | undefined)?.find((p) => p?.inlineData?.data);
      if (!part) throw new ApiError("upstream", `${cfg.displayName} returned no audio${j.candidates?.[0]?.finishReason ? ` (${j.candidates[0].finishReason})` : ""}`);
      const rate = Number(/rate=(\d+)/.exec(String(part.inlineData.mimeType ?? ""))?.[1] ?? 24000);
      return { chunks: once(new Uint8Array(Buffer.from(String(part.inlineData.data), "base64"))), pcmRate: rate };
    }
    default:
      throw new ApiError("invalid_request", `${cfg.displayName} does not offer text to speech`);
  }
}

// ---------------------------------------------------------------------------------------------------------------
// Voices
// ---------------------------------------------------------------------------------------------------------------

const GENDERS = new Set(["female", "male", "neutral"]);

/** Voices from the vendor's API (ElevenLabs, Cartesia). */
export async function listVoices(cfg: SpeechConfig, signal?: AbortSignal): Promise<CatalogVoice[]> {
  if (cfg.vendor === "elevenlabs") {
    const j = await callJson(cfg, url(cfg, "/voices"), { method: "GET", signal });
    return (Array.isArray(j.voices) ? j.voices : [])
      .filter((v: Json) => str(v?.voice_id))
      .map((v: Json) => {
        const labels = (v.labels ?? {}) as Json;
        const gender = String(labels.gender ?? "").toLowerCase();
        return {
          id: v.voice_id,
          display_name: str(v.name) ?? v.voice_id,
          language: str(labels.language) ?? str(v.verified_languages?.[0]?.language),
          gender: GENDERS.has(gender) ? (gender as CatalogVoice["gender"]) : undefined,
          description: [labels.accent, labels.description ?? labels.descriptive, labels.use_case ?? labels.use_case].filter((s) => typeof s === "string" && s).join(", ") || str(v.category),
          preview_url: /^https:\/\//.test(String(v.preview_url ?? "")) ? v.preview_url : undefined,
        };
      });
  }
  if (cfg.vendor === "cartesia") {
    const j = await callJson(cfg, url(cfg, "/voices?limit=100"), { method: "GET", signal });
    const list: Json[] = Array.isArray(j) ? (j as unknown as Json[]) : Array.isArray(j.data) ? j.data : [];
    return list
      .filter((v) => str(v?.id))
      .map((v) => {
        const gender = String(v.gender ?? "").toLowerCase().replace(/^masculine$/, "male").replace(/^feminine$/, "female").replace(/^gender_neutral$/, "neutral");
        return {
          id: v.id,
          display_name: str(v.name) ?? v.id,
          language: str(v.language),
          gender: GENDERS.has(gender) ? (gender as CatalogVoice["gender"]) : undefined,
          description: str(v.description),
        };
      });
  }
  throw new ApiError("invalid_request", `${cfg.displayName} has no voice list`);
}
