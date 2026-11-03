/**
 * The global speech providers the server knows (harness.speech.v1.SpeechService): one entry per vendor, with what
 * it offers for speech to text (stt) and text to speech (tts). Request shapes live in src/speech/adapters.ts; each
 * base URL, model id and voice id below follows the vendor's own API reference (November 2026). Voices of vendors
 * with a voice list API (ElevenLabs, Cartesia) are only a starting set here: ListVoices asks the API.
 *
 * A vendor is configured when an admin set its key (src/speech/store.ts); there is nothing else to configure.
 */

export type SpeechVendor = "openai" | "groq" | "deepgram" | "elevenlabs" | "assemblyai" | "mistral" | "openrouter" | "minimax" | "gemini" | "cartesia";

export interface CatalogModel {
  id: string;
  display_name: string;
  description?: string;
  /** TTS: voices this model can speak with (empty: all). */
  voice_ids?: string[];
  languages?: string[];
}

export interface CatalogVoice {
  id: string;
  display_name: string;
  language?: string;
  gender?: "female" | "male" | "neutral";
  description?: string;
  preview_url?: string;
}

export interface SttSpec {
  models: CatalogModel[];
  default_model: string;
  supports_language: boolean;
  /** Base MIME types the API accepts as they are. */
  input_mime_types: string[];
}

export interface TtsSpec {
  models: CatalogModel[];
  voices: CatalogVoice[];
  default_model: string;
  default_voice: string;
  supports_speed: boolean;
  lists_voices: boolean;
  /** Longest text one request takes (characters); longer text is split. */
  max_chars: number;
}

export interface VendorSpec {
  id: SpeechVendor;
  display_name: string;
  description: string;
  base_url: string;
  docs_url: string;
  key_url: string;
  icon: string;
  stt?: SttSpec;
  tts?: TtsSpec;
}

/** What the common hosted STT APIs take without conversion (MediaRecorder's webm/opus included). */
const COMMON_AUDIO = ["audio/webm", "audio/ogg", "audio/wav", "audio/x-wav", "audio/wave", "audio/mpeg", "audio/mp3", "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/flac", "audio/aac"];

const voices = (ids: string[], extra: Partial<CatalogVoice> = {}): CatalogVoice[] =>
  ids.map((id) => ({ id, display_name: id.charAt(0).toUpperCase() + id.slice(1), ...extra }));

const OPENAI_VOICES_TTS1 = ["alloy", "ash", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer"];
const OPENAI_VOICES = [...OPENAI_VOICES_TTS1, "ballad", "verse", "marin", "cedar"];

const GEMINI_VOICES: [string, string][] = [
  ["Zephyr", "Bright"], ["Puck", "Upbeat"], ["Charon", "Informative"], ["Kore", "Firm"], ["Fenrir", "Excitable"], ["Leda", "Youthful"],
  ["Orus", "Firm"], ["Aoede", "Breezy"], ["Callirrhoe", "Easy-going"], ["Autonoe", "Bright"], ["Enceladus", "Breathy"], ["Iapetus", "Clear"],
  ["Umbriel", "Easy-going"], ["Algieba", "Smooth"], ["Despina", "Smooth"], ["Erinome", "Clear"], ["Algenib", "Gravelly"], ["Rasalgethi", "Informative"],
  ["Laomedeia", "Upbeat"], ["Achernar", "Soft"], ["Alnilam", "Firm"], ["Schedar", "Even"], ["Gacrux", "Mature"], ["Pulcherrima", "Forward"],
  ["Achird", "Friendly"], ["Zubenelgenubi", "Casual"], ["Vindemiatrix", "Gentle"], ["Sadachbia", "Lively"], ["Sadaltager", "Knowledgeable"], ["Sulafat", "Warm"],
];

const AURA2_VOICES: [string, "female" | "male"][] = [
  ["thalia", "female"], ["andromeda", "female"], ["helena", "female"], ["apollo", "male"], ["arcas", "male"], ["aries", "male"], ["asteria", "female"],
  ["athena", "female"], ["atlas", "male"], ["aurora", "female"], ["callista", "female"], ["cora", "female"], ["cordelia", "female"], ["delia", "female"],
  ["draco", "male"], ["electra", "female"], ["harmonia", "female"], ["hera", "female"], ["hermes", "male"], ["hyperion", "male"], ["iris", "female"],
  ["janus", "female"], ["juno", "female"], ["jupiter", "male"], ["luna", "female"], ["mars", "male"], ["minerva", "female"], ["neptune", "male"],
  ["odysseus", "male"], ["ophelia", "female"], ["orion", "male"], ["orpheus", "male"], ["pandora", "female"], ["phoebe", "female"], ["pluto", "male"],
  ["saturn", "male"], ["selene", "female"], ["theia", "female"], ["vesta", "female"], ["zeus", "male"],
];
const AURA1_VOICES: [string, "female" | "male"][] = [
  ["asteria", "female"], ["luna", "female"], ["stella", "female"], ["athena", "female"], ["hera", "female"], ["orion", "male"],
  ["arcas", "male"], ["perseus", "male"], ["angus", "male"], ["orpheus", "male"], ["helios", "male"], ["zeus", "male"],
];

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

export const VENDORS: VendorSpec[] = [
  {
    id: "openai",
    display_name: "OpenAI",
    description: "Whisper and GPT-4o transcription; GPT-4o mini TTS voices.",
    base_url: "https://api.openai.com/v1",
    docs_url: "https://platform.openai.com/docs/guides/speech-to-text",
    key_url: "https://platform.openai.com/api-keys",
    icon: "OA",
    stt: {
      models: [
        { id: "gpt-4o-mini-transcribe", display_name: "GPT-4o mini Transcribe" },
        { id: "gpt-4o-transcribe", display_name: "GPT-4o Transcribe" },
        { id: "whisper-1", display_name: "Whisper" },
      ],
      default_model: "gpt-4o-mini-transcribe",
      supports_language: true,
      input_mime_types: COMMON_AUDIO,
    },
    tts: {
      models: [
        { id: "gpt-4o-mini-tts", display_name: "GPT-4o mini TTS", voice_ids: OPENAI_VOICES },
        { id: "tts-1", display_name: "TTS 1", voice_ids: OPENAI_VOICES_TTS1 },
        { id: "tts-1-hd", display_name: "TTS 1 HD", voice_ids: OPENAI_VOICES_TTS1 },
      ],
      voices: voices(OPENAI_VOICES),
      default_model: "gpt-4o-mini-tts",
      default_voice: "alloy",
      supports_speed: true,
      lists_voices: false,
      max_chars: 4000,
    },
  },
  {
    id: "groq",
    display_name: "Groq",
    description: "Whisper large v3 on Groq hardware: fast and cheap dictation.",
    base_url: "https://api.groq.com/openai/v1",
    docs_url: "https://console.groq.com/docs/speech-to-text",
    key_url: "https://console.groq.com/keys",
    icon: "GQ",
    stt: {
      models: [
        { id: "whisper-large-v3-turbo", display_name: "Whisper large v3 turbo" },
        { id: "whisper-large-v3", display_name: "Whisper large v3" },
      ],
      default_model: "whisper-large-v3-turbo",
      supports_language: true,
      input_mime_types: COMMON_AUDIO,
    },
  },
  {
    id: "deepgram",
    display_name: "Deepgram",
    description: "Nova speech recognition and Aura voices.",
    base_url: "https://api.deepgram.com/v1",
    docs_url: "https://developers.deepgram.com/reference",
    key_url: "https://console.deepgram.com/",
    icon: "DG",
    stt: {
      models: [
        { id: "nova-3", display_name: "Nova 3" },
        { id: "nova-2", display_name: "Nova 2" },
      ],
      default_model: "nova-3",
      supports_language: true,
      input_mime_types: COMMON_AUDIO,
    },
    tts: {
      models: [
        { id: "aura-2", display_name: "Aura 2", voice_ids: AURA2_VOICES.map(([v]) => `${v}-en`) },
        { id: "aura", display_name: "Aura", voice_ids: AURA1_VOICES.map(([v]) => `${v}-en`) },
      ],
      voices: [
        ...AURA2_VOICES.map(([v, gender]) => ({ id: `${v}-en`, display_name: cap(v), language: "en", gender })),
        ...AURA1_VOICES.filter(([v]) => !AURA2_VOICES.some(([w]) => w === v)).map(([v, gender]) => ({ id: `${v}-en`, display_name: cap(v), language: "en", gender })),
      ],
      default_model: "aura-2",
      default_voice: "thalia-en",
      supports_speed: false,
      lists_voices: false,
      max_chars: 1900,
    },
  },
  {
    id: "elevenlabs",
    display_name: "ElevenLabs",
    description: "Scribe transcription and ElevenLabs voices (your voice library included).",
    base_url: "https://api.elevenlabs.io/v1",
    docs_url: "https://elevenlabs.io/docs/api-reference",
    key_url: "https://elevenlabs.io/app/settings/api-keys",
    icon: "EL",
    stt: {
      models: [{ id: "scribe_v1", display_name: "Scribe v1" }],
      default_model: "scribe_v1",
      supports_language: true,
      input_mime_types: COMMON_AUDIO,
    },
    tts: {
      models: [
        { id: "eleven_flash_v2_5", display_name: "Flash v2.5", description: "Lowest latency, 32 languages" },
        { id: "eleven_turbo_v2_5", display_name: "Turbo v2.5" },
        { id: "eleven_multilingual_v2", display_name: "Multilingual v2", description: "Most natural, 29 languages" },
        { id: "eleven_v3", display_name: "Eleven v3" },
      ],
      voices: [
        { id: "21m00Tcm4TlvDq8ikWAM", display_name: "Rachel", gender: "female", language: "en" },
        { id: "JBFqnCBsd6RMkjVDRZzb", display_name: "George", gender: "male", language: "en" },
        { id: "EXAVITQu4vr4xnSDxMaL", display_name: "Sarah", gender: "female", language: "en" },
        { id: "pNInz6obpgDQGcFmaJgB", display_name: "Adam", gender: "male", language: "en" },
      ],
      default_model: "eleven_flash_v2_5",
      default_voice: "21m00Tcm4TlvDq8ikWAM",
      supports_speed: true,
      lists_voices: true,
      max_chars: 5000,
    },
  },
  {
    id: "assemblyai",
    display_name: "AssemblyAI",
    description: "Universal speech recognition (upload, then poll for the transcript).",
    base_url: "https://api.assemblyai.com/v2",
    docs_url: "https://www.assemblyai.com/docs/api-reference",
    key_url: "https://www.assemblyai.com/app/api-keys",
    icon: "AA",
    stt: {
      models: [
        { id: "universal", display_name: "Universal" },
        { id: "slam-1", display_name: "Slam-1", languages: ["en"] },
      ],
      default_model: "universal",
      supports_language: true,
      input_mime_types: COMMON_AUDIO,
    },
  },
  {
    id: "mistral",
    display_name: "Mistral",
    description: "Voxtral transcription.",
    base_url: "https://api.mistral.ai/v1",
    docs_url: "https://docs.mistral.ai/capabilities/audio/",
    key_url: "https://console.mistral.ai/api-keys",
    icon: "MI",
    stt: {
      models: [{ id: "voxtral-mini-latest", display_name: "Voxtral Mini Transcribe" }],
      default_model: "voxtral-mini-latest",
      supports_language: true,
      input_mime_types: COMMON_AUDIO,
    },
  },
  {
    id: "openrouter",
    display_name: "OpenRouter",
    description: "Transcription by an audio-capable chat model (audio input on chat completions).",
    base_url: "https://openrouter.ai/api/v1",
    docs_url: "https://openrouter.ai/docs/features/multimodal/audio",
    key_url: "https://openrouter.ai/settings/keys",
    icon: "OR",
    stt: {
      models: [
        { id: "google/gemini-2.5-flash", display_name: "Gemini 2.5 Flash" },
        { id: "google/gemini-2.5-flash-lite", display_name: "Gemini 2.5 Flash Lite" },
        { id: "openai/gpt-4o-audio-preview", display_name: "GPT-4o Audio" },
      ],
      default_model: "google/gemini-2.5-flash",
      supports_language: true,
      // Chat completions audio input takes wav and mp3 (the client converts webm with ffmpeg).
      input_mime_types: ["audio/wav", "audio/x-wav", "audio/wave", "audio/mpeg", "audio/mp3"],
    },
  },
  {
    id: "minimax",
    display_name: "MiniMax",
    description: "Speech-02 voices.",
    base_url: "https://api.minimax.io/v1",
    docs_url: "https://www.minimax.io/platform/document/T2A%20V2",
    key_url: "https://www.minimax.io/platform/user-center/basic-information/interface-key",
    icon: "MM",
    tts: {
      models: [
        { id: "speech-02-turbo", display_name: "Speech-02 Turbo" },
        { id: "speech-02-hd", display_name: "Speech-02 HD" },
      ],
      voices: [
        "English_expressive_narrator", "Wise_Woman", "Friendly_Person", "Inspirational_girl", "Deep_Voice_Man", "Calm_Woman",
        "Casual_Guy", "Lively_Girl", "Patient_Man", "Young_Knight", "Determined_Man", "Lovely_Girl", "Decent_Boy", "Imposing_Manner", "Elegant_Man",
      ].map((id) => ({ id, display_name: id.replace(/_/g, " ") })),
      default_model: "speech-02-turbo",
      default_voice: "English_expressive_narrator",
      supports_speed: true,
      lists_voices: false,
      max_chars: 4000,
    },
  },
  {
    id: "gemini",
    display_name: "Google Gemini",
    description: "Gemini 2.5 native speech generation, 30 prebuilt voices.",
    base_url: "https://generativelanguage.googleapis.com/v1beta",
    docs_url: "https://ai.google.dev/gemini-api/docs/speech-generation",
    key_url: "https://aistudio.google.com/apikey",
    icon: "GE",
    tts: {
      models: [
        { id: "gemini-2.5-flash-preview-tts", display_name: "Gemini 2.5 Flash TTS" },
        { id: "gemini-2.5-pro-preview-tts", display_name: "Gemini 2.5 Pro TTS" },
      ],
      voices: GEMINI_VOICES.map(([id, d]) => ({ id, display_name: id, description: d })),
      default_model: "gemini-2.5-flash-preview-tts",
      default_voice: "Kore",
      supports_speed: false,
      lists_voices: false,
      max_chars: 3000,
    },
  },
  {
    id: "cartesia",
    display_name: "Cartesia",
    description: "Sonic voices with very low latency.",
    base_url: "https://api.cartesia.ai",
    docs_url: "https://docs.cartesia.ai/api-reference",
    key_url: "https://play.cartesia.ai/keys",
    icon: "CA",
    tts: {
      models: [
        { id: "sonic-2", display_name: "Sonic 2" },
        { id: "sonic-turbo", display_name: "Sonic Turbo" },
      ],
      voices: [
        { id: "694f9389-aac1-45b6-b726-9d9369183238", display_name: "Sarah", language: "en", gender: "female" },
        { id: "a167e0f3-df7e-4d52-a9c3-f949145efdab", display_name: "Blake", language: "en", gender: "male" },
      ],
      default_model: "sonic-2",
      default_voice: "694f9389-aac1-45b6-b726-9d9369183238",
      supports_speed: true,
      lists_voices: true,
      max_chars: 4000,
    },
  },
];

export function findVendor(id: string): VendorSpec | undefined {
  return VENDORS.find((v) => v.id === id);
}

/** Row id of a vendor's key in the provider table (kind "speech"). The colon keeps it apart from every LLM / search id. */
export const rowIdOf = (vendor: string) => `speech:${vendor}`;
