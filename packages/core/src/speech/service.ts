/**
 * Transport-agnostic SpeechService (proto/harness/speech/v1/speech.proto): the global speech providers of
 * src/speech/catalog.ts, their keys, transcription and synthesis.
 *
 * Keys are stored in the provider table as global rows of kind "speech" (id "speech:<vendor>", type = vendor),
 * encrypted like every provider key (services/providers.ts) and write-only. Only admins set or clear them; every
 * signed-in user may use a configured vendor. One key serves both kinds of a vendor (OpenAI STT and TTS).
 */
import { and, eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { assertResolvesPublic } from "../lib/ssrf.ts";
import type { PublicUser } from "../services/devices.ts";
import { encryptGlobalKey, loadProviderRuntime } from "../services/providers.ts";
import { listVoices, outputMimeOf, synthesize as synthesizePiece, transcribe as transcribeAudio, type SpeechConfig } from "./adapters.ts";
import { findVendor, rowIdOf, VENDORS, type CatalogModel, type CatalogVoice, type VendorSpec } from "./catalog.ts";
import { baseMime, splitForSpeech, wavHeader } from "./text.ts";

export type Kind = "stt" | "tts";

export const MAX_AUDIO_BYTES = 25 << 20;
export const MAX_TEXT_CHARS = 100_000;

export interface ProviderView {
  id: string;
  display_name: string;
  kind: Kind;
  configured: boolean;
  models: CatalogModel[];
  voices: CatalogVoice[];
  lists_voices: boolean;
  default_model: string;
  default_voice: string;
  supports_speed: boolean;
  supports_language: boolean;
  input_mime_types: string[];
  api_key_set: boolean;
  api_key_hint: string;
  base_url: string;
  docs_url: string;
  key_url: string;
  icon: string;
  description: string;
}

type Row = typeof schema.provider.$inferSelect;

async function speechRows(): Promise<Map<string, Row>> {
  await ensureDb();
  const rows = await getDb().select().from(schema.provider).where(eq(schema.provider.kind, "speech"));
  return new Map(rows.filter((r) => !r.ownerUserId).map((r) => [r.type, r]));
}

function view(v: VendorSpec, kind: Kind, row: Row | undefined, admin: boolean): ProviderView {
  const stt = kind === "stt" ? v.stt : undefined;
  const tts = kind === "tts" ? v.tts : undefined;
  const configured = !!row?.apiKeyEnc && row.enabled;
  return {
    id: v.id,
    display_name: v.display_name,
    kind,
    configured,
    models: (stt ?? tts)?.models ?? [],
    voices: tts?.voices ?? [],
    lists_voices: tts?.lists_voices ?? false,
    default_model: (stt ?? tts)?.default_model ?? "",
    default_voice: tts?.default_voice ?? "",
    supports_speed: tts?.supports_speed ?? false,
    supports_language: stt?.supports_language ?? false,
    input_mime_types: stt?.input_mime_types ?? [],
    api_key_set: !!row?.apiKeyEnc,
    api_key_hint: admin ? (row?.apiKeyHint ?? "") : "",
    base_url: admin ? (row?.baseUrl ?? v.base_url) : "",
    docs_url: v.docs_url,
    key_url: v.key_url,
    icon: v.icon,
    description: v.description,
  };
}

/** Every vendor for the kinds asked for (undefined: both), STT first. */
export async function listSpeechProviders(user: PublicUser, kind?: Kind): Promise<{ providers: ProviderView[]; can_manage: boolean }> {
  const rows = await speechRows();
  const admin = user.role === "admin";
  const out: ProviderView[] = [];
  for (const k of kind ? [kind] : (["stt", "tts"] as const)) {
    for (const v of VENDORS) if (k === "stt" ? v.stt : v.tts) out.push(view(v, k, rows.get(v.id), admin));
  }
  return { providers: out, can_manage: admin };
}

function requireAdmin(user: PublicUser) {
  if (user.role !== "admin") throw new ApiError("permission", "Admin role required");
}

function vendorOrThrow(id: string): VendorSpec {
  const v = findVendor(id.trim());
  if (!v) throw new ApiError("not_found", `Unknown speech provider "${id}"`);
  return v;
}

/** Admins: sets a vendor's key (and optionally a base URL); returns its views for each kind. */
export async function setSpeechProviderKey(user: PublicUser, providerId: string, apiKey: string, baseUrl?: string): Promise<ProviderView[]> {
  requireAdmin(user);
  const v = vendorOrThrow(providerId);
  const key = apiKey.trim();
  if (!key) throw new ApiError("invalid_request", "api_key is required (ClearProviderKey removes a key)");
  const base = (baseUrl?.trim() || v.base_url).replace(/\/+$/, "");
  if (base !== v.base_url) await assertResolvesPublic(base, false);
  await ensureDb();
  const id = rowIdOf(v.id);
  const now = new Date();
  const values = { ...encryptGlobalKey(id, key), baseUrl: base, enabled: true, updatedAt: now };
  await getDb()
    .insert(schema.provider)
    .values({ id, ownerUserId: null, kind: "speech", type: v.id, displayName: v.display_name, createdAt: now, ...values })
    .onConflictDoUpdate({ target: schema.provider.id, set: values });
  const rows = await speechRows();
  return (["stt", "tts"] as const).filter((k) => (k === "stt" ? v.stt : v.tts)).map((k) => view(v, k, rows.get(v.id), true));
}

export async function clearSpeechProviderKey(user: PublicUser, providerId: string): Promise<void> {
  requireAdmin(user);
  const v = vendorOrThrow(providerId);
  await ensureDb();
  await getDb().delete(schema.provider).where(and(eq(schema.provider.id, rowIdOf(v.id)), eq(schema.provider.kind, "speech")));
}

/** The decrypted config of a configured vendor (never serialize). */
async function configOf(v: VendorSpec): Promise<SpeechConfig> {
  const row = (await speechRows()).get(v.id);
  if (!row?.apiKeyEnc || !row.enabled) throw new ApiError("failed_precondition", `${v.display_name} is not configured on this server: an admin needs to set its API key`);
  const rt = await loadProviderRuntime(row.id);
  return { vendor: v.id, displayName: v.display_name, baseUrl: rt.baseUrl, apiKey: rt.apiKey ?? "", allowPrivateNetwork: false, ...testOverrides };
}

/** For tests: adapter settings (private network for fake upstreams, short polls). */
let testOverrides: Partial<SpeechConfig> = {};
export function setSpeechTestOverrides(o: Partial<SpeechConfig>): void {
  testOverrides = o;
}

function pickModel(requested: string | undefined, fallback: string, name: string): string {
  const m = requested?.trim();
  if (!m) return fallback;
  // The lists are curated, not exhaustive: an unknown id goes to the API, which knows best (OpenRouter has hundreds).
  if (!/^[A-Za-z0-9._:/-]{1,128}$/.test(m)) throw new ApiError("invalid_request", `Invalid ${name} "${m}"`);
  return m;
}

export interface TranscribeRequest {
  providerId: string;
  model?: string;
  audio: Uint8Array;
  mimeType: string;
  language?: string;
  prompt?: string;
  signal?: AbortSignal;
}

export async function transcribeForUser(_user: PublicUser, req: TranscribeRequest) {
  const v = vendorOrThrow(req.providerId);
  if (!v.stt) throw new ApiError("invalid_request", `${v.display_name} does not offer speech to text`);
  if (!req.audio.byteLength) throw new ApiError("invalid_request", "audio is empty");
  if (req.audio.byteLength > MAX_AUDIO_BYTES) throw new ApiError("invalid_request", `audio is larger than ${MAX_AUDIO_BYTES >> 20} MiB`);
  const mime = baseMime(req.mimeType || "");
  if (!mime) throw new ApiError("invalid_request", "mime_type is required");
  if (!v.stt.input_mime_types.includes(mime)) {
    throw new ApiError("invalid_request", `${v.display_name} does not accept ${mime}; send one of ${v.stt.input_mime_types.join(", ")}`);
  }
  const language = req.language?.trim();
  if (language && !/^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8})*$/.test(language)) throw new ApiError("invalid_request", `Invalid language "${language}"`);
  const model = pickModel(req.model, v.stt.default_model, "model");
  const cfg = await configOf(v);
  const out = await transcribeAudio(cfg, { model, audio: req.audio, mimeType: req.mimeType, language: language || undefined, prompt: req.prompt?.slice(0, 2000), signal: req.signal });
  return { ...out, providerId: v.id, model };
}

export interface SynthesizeRequest {
  providerId: string;
  model?: string;
  voice?: string;
  text: string;
  speed?: number;
  language?: string;
  signal?: AbortSignal;
}

export interface AudioChunk {
  audio: Uint8Array;
  mimeType?: string;
  done?: boolean;
  characters?: number;
}

/** Validates the request and returns the audio stream (validation errors throw before the first chunk). */
export async function synthesizeForUser(_user: PublicUser, req: SynthesizeRequest): Promise<AsyncGenerator<AudioChunk>> {
  const v = vendorOrThrow(req.providerId);
  const tts = v.tts;
  if (!tts) throw new ApiError("invalid_request", `${v.display_name} does not offer text to speech`);
  const text = req.text.trim();
  if (!text) throw new ApiError("invalid_request", "text is empty");
  if (text.length > MAX_TEXT_CHARS) throw new ApiError("invalid_request", `text is longer than ${MAX_TEXT_CHARS} characters`);
  const model = pickModel(req.model, tts.default_model, "model");
  const voice = pickModel(req.voice, tts.default_voice, "voice");
  const speed = req.speed && Number.isFinite(req.speed) && req.speed > 0 ? req.speed : 1;
  const cfg = await configOf(v);
  const pieces = splitForSpeech(text, tts.max_chars);
  const mime = outputMimeOf(v.id);
  return (async function* () {
    let first = true;
    const head = () => {
      const m = first ? mime : undefined;
      first = false;
      return m;
    };
    if (mime === "audio/wav") {
      // Raw PCM: collect every piece, then send one WAV (a header needs the total length).
      const pcm: Uint8Array[] = [];
      let rate = 24000;
      for (const p of pieces) {
        const out = await synthesizePiece(cfg, { model, voice, text: p, speed, language: req.language, signal: req.signal });
        rate = out.pcmRate ?? rate;
        for await (const c of out.chunks) pcm.push(c);
      }
      const total = pcm.reduce((n, c) => n + c.byteLength, 0);
      yield { audio: wavHeader(total, rate), mimeType: head() };
      for (const c of pcm) yield { audio: c };
    } else {
      for (const p of pieces) {
        const out = await synthesizePiece(cfg, { model, voice, text: p, speed, language: req.language, signal: req.signal });
        for await (const c of out.chunks) yield { audio: c, mimeType: head() };
      }
    }
    yield { audio: new Uint8Array(), mimeType: head(), done: true, characters: text.length };
  })();
}

export async function listSpeechVoices(_user: PublicUser, providerId: string, model?: string, signal?: AbortSignal): Promise<CatalogVoice[]> {
  const v = vendorOrThrow(providerId);
  if (!v.tts) throw new ApiError("invalid_request", `${v.display_name} does not offer text to speech`);
  let voices = v.tts.voices;
  if (v.tts.lists_voices) voices = await listVoices(await configOf(v), signal);
  const only = model ? v.tts.models.find((m) => m.id === model)?.voice_ids : undefined;
  return only?.length ? voices.filter((x) => only.includes(x.id)) : voices;
}
