/**
 * Speech: the hosted STT / TTS adapters against a fake HTTP server (request shapes, response mapping, error
 * mapping, key scrubbing), text splitting, and SpeechService end to end (admin-only write-only keys, provider
 * listing, transcription and streamed synthesis through a configured vendor, kept out of ProviderService).
 */
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { Code, ConnectError, createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { SpeechKind, SpeechService } from "../src/gen/harness/speech/v1/speech_pb.ts";
import { listVoices, synthesize, transcribe, type SpeechConfig } from "../src/speech/adapters.ts";
import type { SpeechVendor } from "../src/speech/catalog.ts";
import { setSpeechTestOverrides } from "../src/speech/service.ts";
import { splitForSpeech } from "../src/speech/text.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

// Fake keys, never real.
const KEY = "sk-speech-fake-key-0000000000";

interface Seen {
  method: string;
  path: string;
  params: URLSearchParams;
  headers: IncomingMessage["headers"];
  raw: Buffer;
  json?: any;
}

type Reply = { status: number; body: unknown; headers?: Record<string, string> };

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
      seen.push({ method: req.method ?? "", path: u.pathname, params: u.searchParams, headers: req.headers, raw, json });
      const r = replies.shift() ?? fallback;
      if (Buffer.isBuffer(r.body)) res.writeHead(r.status, { "content-type": "audio/mpeg", ...r.headers }).end(r.body);
      else res.writeHead(r.status, { "content-type": "application/json", ...r.headers }).end(JSON.stringify(r.body));
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

const cfg = (vendor: SpeechVendor): SpeechConfig => ({ vendor, displayName: vendor, baseUrl: `${base}/v1`, apiKey: KEY, allowPrivateNetwork: true, pollIntervalMs: 5 });
const AUDIO = new Uint8Array([1, 2, 3, 4]);

async function collect(it: AsyncIterable<Uint8Array>): Promise<Buffer> {
  const out: Buffer[] = [];
  for await (const c of it) out.push(Buffer.from(c));
  return Buffer.concat(out);
}

describe("speech to text adapters", () => {
  it("openai: multipart upload with model and language, json for gpt-4o models", async () => {
    fallback = { status: 200, body: { text: " hello world " } };
    const out = await transcribe(cfg("openai"), { model: "gpt-4o-transcribe", audio: AUDIO, mimeType: "audio/webm;codecs=opus", language: "en", prompt: "harness" });
    expect(out.text).toBe("hello world");
    const s = seen[0]!;
    expect(s.path).toBe("/v1/audio/transcriptions");
    expect(s.headers.authorization).toBe(`Bearer ${KEY}`);
    expect(s.headers["content-type"]).toMatch(/^multipart\/form-data/);
    const body = s.raw.toString("latin1");
    expect(body).toContain('name="model"\r\n\r\ngpt-4o-transcribe');
    expect(body).toContain('name="language"\r\n\r\nen');
    expect(body).toContain('name="response_format"\r\n\r\njson');
    expect(body).toContain('filename="audio.webm"');
  });

  it("groq: verbose_json reports language and duration", async () => {
    fallback = { status: 200, body: { text: "hallo", language: "german", duration: 2.5 } };
    const out = await transcribe(cfg("groq"), { model: "whisper-large-v3-turbo", audio: AUDIO, mimeType: "audio/wav" });
    expect(out).toEqual({ text: "hallo", language: "german", durationSeconds: 2.5 });
    expect(seen[0]!.raw.toString("latin1")).toContain('name="response_format"\r\n\r\nverbose_json');
  });

  it("mistral: no response_format or prompt fields", async () => {
    fallback = { status: 200, body: { text: "bonjour", language: "fr", usage: { prompt_audio_seconds: 3 } } };
    const out = await transcribe(cfg("mistral"), { model: "voxtral-mini-latest", audio: AUDIO, mimeType: "audio/ogg", prompt: "x" });
    expect(out).toEqual({ text: "bonjour", language: "fr", durationSeconds: 3 });
    const body = seen[0]!.raw.toString("latin1");
    expect(body).not.toContain("response_format");
    expect(body).not.toContain('name="prompt"');
  });

  it("deepgram: raw body, token auth, language detection", async () => {
    fallback = { status: 200, body: { metadata: { duration: 1.25 }, results: { channels: [{ detected_language: "en", alternatives: [{ transcript: "hi there" }] }] } } };
    const out = await transcribe(cfg("deepgram"), { model: "nova-3", audio: AUDIO, mimeType: "audio/webm;codecs=opus", prompt: "harness, sidecar" });
    expect(out).toEqual({ text: "hi there", language: "en", durationSeconds: 1.25 });
    const s = seen[0]!;
    expect(s.path).toBe("/v1/listen");
    expect(s.headers.authorization).toBe(`Token ${KEY}`);
    expect(s.headers["content-type"]).toBe("audio/webm");
    expect(s.params.get("model")).toBe("nova-3");
    expect(s.params.get("detect_language")).toBe("true");
    expect(s.params.getAll("keyterm")).toEqual(["harness", "sidecar"]);
    expect([...s.raw]).toEqual([1, 2, 3, 4]);
  });

  it("elevenlabs: scribe with xi-api-key", async () => {
    fallback = { status: 200, body: { text: "ok", language_code: "en", words: [{ end: 0.4 }, { end: 0.9 }] } };
    const out = await transcribe(cfg("elevenlabs"), { model: "scribe_v1", audio: AUDIO, mimeType: "audio/mpeg" });
    expect(out).toEqual({ text: "ok", language: "en", durationSeconds: 0.9 });
    expect(seen[0]!.path).toBe("/v1/speech-to-text");
    expect(seen[0]!.headers["xi-api-key"]).toBe(KEY);
    expect(seen[0]!.raw.toString("latin1")).toContain('name="model_id"\r\n\r\nscribe_v1');
  });

  it("assemblyai: upload, create, poll until completed", async () => {
    replies = [
      { status: 200, body: { upload_url: "https://cdn.example/upload/1" } },
      { status: 200, body: { id: "t1", status: "queued" } },
      { status: 200, body: { id: "t1", status: "processing" } },
      { status: 200, body: { id: "t1", status: "completed", text: "done", language_code: "en_us", audio_duration: 4 } },
    ];
    const out = await transcribe(cfg("assemblyai"), { model: "universal", audio: AUDIO, mimeType: "audio/wav" });
    expect(out).toEqual({ text: "done", language: "en_us", durationSeconds: 4 });
    expect(seen.map((s) => `${s.method} ${s.path}`)).toEqual(["POST /v1/upload", "POST /v1/transcript", "GET /v1/transcript/t1", "GET /v1/transcript/t1"]);
    expect(seen[0]!.headers.authorization).toBe(KEY);
    expect(seen[1]!.json).toMatchObject({ audio_url: "https://cdn.example/upload/1", speech_model: "universal", language_detection: true });
  });

  it("assemblyai: a failed transcript is an error", async () => {
    replies = [{ status: 200, body: { upload_url: "https://cdn.example/u" } }, { status: 200, body: { id: "t2", status: "error", error: "no speech" } }];
    await expect(transcribe(cfg("assemblyai"), { model: "universal", audio: AUDIO, mimeType: "audio/wav" })).rejects.toMatchObject({ type: "upstream", message: expect.stringContaining("no speech") });
  });

  it("openrouter: input_audio content part", async () => {
    fallback = { status: 200, body: { choices: [{ message: { content: "the transcript" } }] } };
    const out = await transcribe(cfg("openrouter"), { model: "google/gemini-2.5-flash", audio: AUDIO, mimeType: "audio/wav", language: "en" });
    expect(out.text).toBe("the transcript");
    const msg = seen[0]!.json.messages[0];
    expect(seen[0]!.path).toBe("/v1/chat/completions");
    expect(msg.content[1]).toEqual({ type: "input_audio", input_audio: { data: Buffer.from(AUDIO).toString("base64"), format: "wav" } });
    expect(msg.content[0].text).toContain("in en");
  });

  it("maps errors and never echoes the key", async () => {
    fallback = { status: 401, body: { error: { message: `bad key ${KEY}` } } };
    const err = await transcribe(cfg("openai"), { model: "whisper-1", audio: AUDIO, mimeType: "audio/wav" }).catch((e) => e);
    expect(err).toMatchObject({ type: "upstream", upstreamStatus: 401 });
    expect(err.message).toContain("rejected the API key");
    expect(err.message).not.toContain(KEY);
    fallback = { status: 429, body: {}, headers: { "retry-after": "3" } };
    await expect(transcribe(cfg("groq"), { model: "whisper-large-v3", audio: AUDIO, mimeType: "audio/wav" })).rejects.toMatchObject({ type: "rate_limit", retryAfterMs: 3000 });
    fallback = { status: 402, body: { err_msg: "no credits" } };
    await expect(transcribe(cfg("deepgram"), { model: "nova-3", audio: AUDIO, mimeType: "audio/wav" })).rejects.toMatchObject({ type: "quota_exceeded", message: expect.stringContaining("no credits") });
    fallback = { status: 400, body: { detail: { message: "unsupported format" } } };
    await expect(transcribe(cfg("elevenlabs"), { model: "scribe_v1", audio: AUDIO, mimeType: "audio/wav" })).rejects.toMatchObject({ type: "invalid_request" });
  });
});

describe("text to speech adapters", () => {
  const MP3 = Buffer.from("ID3fake-mp3-bytes");

  it("openai: audio/speech with voice and speed, bytes streamed", async () => {
    fallback = { status: 200, body: MP3 };
    const out = await synthesize(cfg("openai"), { model: "tts-1", voice: "nova", text: "Hello.", speed: 1.5 });
    expect((await collect(out.chunks)).equals(MP3)).toBe(true);
    expect(out.pcmRate).toBeUndefined();
    expect(seen[0]!.path).toBe("/v1/audio/speech");
    expect(seen[0]!.json).toEqual({ model: "tts-1", input: "Hello.", voice: "nova", response_format: "mp3", speed: 1.5 });
  });

  it("elevenlabs: voice in the path, speed clamped", async () => {
    fallback = { status: 200, body: MP3 };
    await collect((await synthesize(cfg("elevenlabs"), { model: "eleven_flash_v2_5", voice: "abc", text: "Hi", speed: 2, language: "de" })).chunks);
    expect(seen[0]!.path).toBe("/v1/text-to-speech/abc/stream");
    expect(seen[0]!.params.get("output_format")).toBe("mp3_44100_128");
    expect(seen[0]!.json).toEqual({ text: "Hi", model_id: "eleven_flash_v2_5", voice_settings: { speed: 1.2 }, language_code: "de" });
  });

  it("deepgram: model and voice in one query parameter", async () => {
    fallback = { status: 200, body: MP3 };
    await collect((await synthesize(cfg("deepgram"), { model: "aura-2", voice: "thalia-en", text: "Hi", speed: 1 })).chunks);
    expect(seen[0]!.path).toBe("/v1/speak");
    expect(seen[0]!.params.get("model")).toBe("aura-2-thalia-en");
    expect(seen[0]!.params.get("encoding")).toBe("mp3");
    expect(seen[0]!.json).toEqual({ text: "Hi" });
  });

  it("cartesia: tts/bytes with version header", async () => {
    fallback = { status: 200, body: MP3 };
    await collect((await synthesize(cfg("cartesia"), { model: "sonic-2", voice: "v-1", text: "Hi", speed: 0.6 })).chunks);
    expect(seen[0]!.headers["x-api-key"]).toBe(KEY);
    expect(seen[0]!.headers["cartesia-version"]).toBe("2025-04-16");
    expect(seen[0]!.json).toMatchObject({ model_id: "sonic-2", transcript: "Hi", voice: { mode: "id", id: "v-1" }, output_format: { container: "mp3" }, speed: "slow" });
  });

  it("minimax: hex audio in JSON, status codes mapped", async () => {
    fallback = { status: 200, body: { data: { audio: Buffer.from("mp3!").toString("hex") }, base_resp: { status_code: 0, status_msg: "success" } } };
    const out = await synthesize(cfg("minimax"), { model: "speech-02-turbo", voice: "Wise_Woman", text: "Hi", speed: 1 });
    expect((await collect(out.chunks)).toString()).toBe("mp3!");
    expect(seen[0]!.path).toBe("/v1/t2a_v2");
    expect(seen[0]!.json).toMatchObject({ model: "speech-02-turbo", text: "Hi", stream: false, voice_setting: { voice_id: "Wise_Woman" }, audio_setting: { format: "mp3" } });
    fallback = { status: 200, body: { base_resp: { status_code: 1008, status_msg: "insufficient balance" } } };
    await expect(synthesize(cfg("minimax"), { model: "speech-02-turbo", voice: "Wise_Woman", text: "Hi", speed: 1 })).rejects.toMatchObject({ type: "quota_exceeded" });
  });

  it("gemini: base64 PCM with its sample rate", async () => {
    const pcm = Buffer.from([0, 1, 2, 3]);
    fallback = { status: 200, body: { candidates: [{ content: { parts: [{ inlineData: { mimeType: "audio/L16;codec=pcm;rate=24000", data: pcm.toString("base64") } }] } }] } };
    const out = await synthesize(cfg("gemini"), { model: "gemini-2.5-flash-preview-tts", voice: "Kore", text: "Hi", speed: 1 });
    expect(out.pcmRate).toBe(24000);
    expect((await collect(out.chunks)).equals(pcm)).toBe(true);
    expect(seen[0]!.path).toBe("/v1/models/gemini-2.5-flash-preview-tts:generateContent");
    expect(seen[0]!.headers["x-goog-api-key"]).toBe(KEY);
    expect(seen[0]!.json.generationConfig).toEqual({ responseModalities: ["AUDIO"], speechConfig: { voiceConfig: { prebuiltVoiceConfig: { voiceName: "Kore" } } } });
  });

  it("lists voices from ElevenLabs and Cartesia", async () => {
    fallback = { status: 200, body: { voices: [{ voice_id: "v1", name: "Rachel", labels: { gender: "female", accent: "american" }, preview_url: "https://x/p.mp3" }, { name: "no id" }] } };
    expect(await listVoices(cfg("elevenlabs"))).toEqual([{ id: "v1", display_name: "Rachel", language: undefined, gender: "female", description: "american", preview_url: "https://x/p.mp3" }]);
    fallback = { status: 200, body: { data: [{ id: "c1", name: "Blake", language: "en", gender: "masculine", description: "calm" }], has_more: false } };
    expect(await listVoices(cfg("cartesia"))).toEqual([{ id: "c1", display_name: "Blake", language: "en", gender: "male", description: "calm" }]);
  });
});

describe("splitForSpeech", () => {
  it("keeps short text whole and splits long text at sentence boundaries", () => {
    expect(splitForSpeech("  One sentence.  ", 100)).toEqual(["One sentence."]);
    const text = "First sentence here. Second sentence follows! Third one? Fourth and last.";
    const parts = splitForSpeech(text, 30);
    expect(parts.every((p) => p.length <= 30)).toBe(true);
    expect(parts.join(" ")).toBe(text);
    expect(parts[0]).toBe("First sentence here.");
  });

  it("falls back to words, then hard cuts", () => {
    expect(splitForSpeech("aaa bbb ccc ddd", 8)).toEqual(["aaa bbb", "ccc ddd"]);
    expect(splitForSpeech("x".repeat(25), 10)).toEqual(["x".repeat(10), "x".repeat(10), "x".repeat(5)]);
  });
});

// --- Through the service ---------------------------------------------------------------------------------------

describe("SpeechService", () => {
  let ts: TestServer;
  let adminSpeech: Client<typeof SpeechService>;
  let userSpeech: Client<typeof SpeechService>;
  let adminToken = "";

  function speechClient(token: string) {
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
    return createClient(SpeechService, transport);
  }

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
    adminToken = a.accessToken;
    const u = await createUserAndLogin(ts, a.cookie, "speech-user@test.local");
    adminSpeech = speechClient(a.accessToken);
    userSpeech = speechClient(u.accessToken);
    // The fake upstream stands in for every vendor.
    setSpeechTestOverrides({ baseUrl: `${base}/v1`, allowPrivateNetwork: true, pollIntervalMs: 5 });
  });

  afterAll(async () => {
    setSpeechTestOverrides({});
    await ts.close();
  });

  it("lists every vendor per kind, none configured at first", async () => {
    const stt = await userSpeech.listProviders({ kind: SpeechKind.STT });
    expect(stt.canManage).toBe(false);
    expect(stt.providers.map((p) => p.id)).toEqual(["openai", "groq", "deepgram", "elevenlabs", "assemblyai", "mistral", "openrouter"]);
    expect(stt.providers.every((p) => !p.configured && !p.available && p.kind === SpeechKind.STT)).toBe(true);
    const tts = await userSpeech.listProviders({ kind: SpeechKind.TTS });
    expect(tts.providers.map((p) => p.id)).toEqual(["openai", "deepgram", "elevenlabs", "minimax", "gemini", "cartesia"]);
    expect(tts.providers[0]).toMatchObject({ defaultModel: "gpt-4o-mini-tts", defaultVoice: "alloy", supportsSpeed: true });
    expect((await userSpeech.listProviders({})).providers.length).toBe(13);
  });

  it("only admins set keys; keys are write-only and hidden from ProviderService", async () => {
    expect(await code(userSpeech.setProviderKey({ providerId: "openai", apiKey: KEY }))).toBe(Code.PermissionDenied);
    expect(await code(adminSpeech.setProviderKey({ providerId: "nope", apiKey: KEY }))).toBe(Code.NotFound);
    expect(await code(adminSpeech.setProviderKey({ providerId: "openai", apiKey: " " }))).toBe(Code.InvalidArgument);
    const set = await adminSpeech.setProviderKey({ providerId: "openai", apiKey: KEY });
    expect(set.providers.map((p) => p.kind)).toEqual([SpeechKind.STT, SpeechKind.TTS]);
    expect(set.providers[0]).toMatchObject({ configured: true, apiKeySet: true, apiKeyHint: "…0000", baseUrl: "https://api.openai.com/v1" });
    expect(JSON.stringify(set, (_k, v) => (typeof v === "bigint" ? String(v) : v))).not.toContain(KEY);

    const mine = (await userSpeech.listProviders({ kind: SpeechKind.STT })).providers.find((p) => p.id === "openai")!;
    expect(mine).toMatchObject({ configured: true, available: true, apiKeyHint: "", baseUrl: "" });

    const admin = ts.as(adminToken);
    const providers = await admin.providers.listProviders({});
    expect(providers.providers.some((p) => p.id.startsWith("speech:"))).toBe(false);
    expect(await code(admin.providers.deleteProvider({ id: "speech:openai" }))).toBe(Code.NotFound);
  });

  it("transcribes and synthesizes through a configured vendor", async () => {
    expect(await code(userSpeech.transcribe({ providerId: "groq", audio: AUDIO, mimeType: "audio/wav" }))).toBe(Code.FailedPrecondition);
    expect(await code(userSpeech.transcribe({ providerId: "openai", audio: new Uint8Array(), mimeType: "audio/wav" }))).toBe(Code.InvalidArgument);
    expect(await code(userSpeech.transcribe({ providerId: "openai", audio: AUDIO, mimeType: "video/x-flv" }))).toBe(Code.InvalidArgument);
    expect(await code(userSpeech.transcribe({ providerId: "minimax", audio: AUDIO, mimeType: "audio/wav" }))).toBe(Code.InvalidArgument);

    fallback = { status: 200, body: { text: "dictated text" } };
    const t = await userSpeech.transcribe({ providerId: "openai", audio: AUDIO, mimeType: "audio/webm;codecs=opus", language: "en" });
    expect(t).toMatchObject({ text: "dictated text", providerId: "openai", model: "gpt-4o-mini-transcribe" });
    expect(seen[0]!.headers.authorization).toBe(`Bearer ${KEY}`);

    seen = [];
    fallback = { status: 200, body: Buffer.from("mp3data") };
    const long = Array.from({ length: 300 }, (_, i) => `Sentence number ${i}.`).join(" ");
    const parts: { audio: Uint8Array; mimeType: string; done: boolean; characters: bigint }[] = [];
    for await (const r of userSpeech.synthesize({ providerId: "openai", voice: "nova", text: long })) parts.push(r);
    expect(parts[0]!.mimeType).toBe("audio/mpeg");
    expect(parts.at(-1)).toMatchObject({ done: true, characters: BigInt(long.length) });
    // Over max_chars: one request per piece, in order.
    expect(seen.length).toBeGreaterThan(1);
    expect(seen.every((s) => s.json.voice === "nova" && s.json.input.length <= 4000)).toBe(true);
    expect(Buffer.concat(parts.map((p) => Buffer.from(p.audio))).toString()).toBe("mp3data".repeat(seen.length));
  });

  it("wraps Gemini PCM into one WAV", async () => {
    await adminSpeech.setProviderKey({ providerId: "gemini", apiKey: KEY });
    fallback = { status: 200, body: { candidates: [{ content: { parts: [{ inlineData: { mimeType: "audio/L16;codec=pcm;rate=24000", data: Buffer.from([1, 0, 2, 0]).toString("base64") } }] } }] } };
    const parts = [];
    for await (const r of userSpeech.synthesize({ providerId: "gemini", text: "Hello" })) parts.push(r);
    expect(parts[0]!.mimeType).toBe("audio/wav");
    const wav = Buffer.concat(parts.map((p) => Buffer.from(p.audio)));
    expect(wav.subarray(0, 4).toString()).toBe("RIFF");
    expect(wav.readUInt32LE(24)).toBe(24000);
    expect(wav.readUInt32LE(40)).toBe(4);
    expect(wav.length).toBe(48);
  });

  it("lists voices of a vendor with a voice API, and clears keys", async () => {
    expect(await code(userSpeech.listVoices({ providerId: "elevenlabs" }))).toBe(Code.FailedPrecondition);
    const static_ = await userSpeech.listVoices({ providerId: "openai", model: "tts-1" });
    expect(static_.voices.map((v) => v.id)).not.toContain("marin");
    await adminSpeech.setProviderKey({ providerId: "elevenlabs", apiKey: KEY });
    fallback = { status: 200, body: { voices: [{ voice_id: "x1", name: "Custom" }] } };
    expect((await userSpeech.listVoices({ providerId: "elevenlabs" })).voices.map((v) => v.id)).toEqual(["x1"]);

    expect(await code(userSpeech.clearProviderKey({ providerId: "openai" }))).toBe(Code.PermissionDenied);
    await adminSpeech.clearProviderKey({ providerId: "openai" });
    const after = (await adminSpeech.listProviders({ kind: SpeechKind.TTS })).providers.find((p) => p.id === "openai")!;
    expect(after).toMatchObject({ configured: false, apiKeySet: false });
  });
});
