// SpeechApi: dictation (speech to text) and "Read aloud" (text to speech), over AgentService's speech RPCs
// (proto/harness/agent/v1/speech.proto) plus the admin key methods of harness.speech.v1.SpeechService that the
// client forwards to the server. Two implementations: connect.ts (a real client) and mock.ts (canned data,
// simulated downloads, a generated tone as audio).
//
// Providers come from three places (SpeechProviderView.location):
//   - 'global': hosted APIs configured on the harness server by admins (OpenAI, ElevenLabs, Deepgram, ...);
//   - 'local': engines on the client's machine (Parakeet, Whisper, Piper through a runtime the client downloads,
//     macOS voices). Their models are downloaded on demand (downloadModel) and live on that machine only;
//   - 'browser': the frontend's own Web Speech API (id BROWSER_SPEECH_PROVIDER). Neither the client nor the server
//     lists or runs it: the UI adds and runs it itself, and may store it as the device choice.
//
// Settings have two parts: `synced` (a global provider, shared by all the user's devices) and `device` (a local
// engine or 'browser', this machine only). On a device, its own choice, when set, overrides the synced one; `stt`
// / `tts` are what applies here.

export const BROWSER_SPEECH_PROVIDER = 'browser'

export type SpeechKindName = 'stt' | 'tts'
export type SpeechLocationName = 'global' | 'local' | 'browser'

export interface SpeechModelView {
  id: string
  name: string
  description: string
  // Local models: downloaded (and runnable) on this machine.
  installed: boolean
  // Local models: on disk when installed, else the download size (approximate).
  sizeBytes: number
  // TTS: voices this model speaks with; empty: all of the provider's.
  voiceIds: string[]
  languages: string[]
}

export interface SpeechVoiceView {
  id: string
  name: string
  // BCP 47, '' when unknown / multilingual.
  language: string
  gender: '' | 'female' | 'male' | 'neutral'
  description: string
  // https sample to play, '' when none.
  previewUrl: string
  // Local voices (Piper): downloaded, and their size.
  installed: boolean
  sizeBytes: number
}

export interface SpeechProviderView {
  id: string
  name: string
  kind: SpeechKindName
  location: SpeechLocationName
  // Global: an admin set its key. Local: the runtime is installed (macOS voices: always).
  configured: boolean
  // Usable right now.
  available: boolean
  // Why not, for the settings screen.
  unavailableReason: string
  models: SpeechModelView[]
  voices: SpeechVoiceView[]
  // TTS: listVoices asks the provider for its full voice list.
  listsVoices: boolean
  defaultModel: string
  defaultVoice: string
  supportsSpeed: boolean
  supportsLanguage: boolean
  // STT: audio types accepted as they are (e.g. local engines without ffmpeg take WAV only: record WAV then).
  inputMimeTypes: string[]
  // Global, admins only: key state, base URL in use.
  keySet: boolean
  keyHint: string
  baseUrl: string
  docsUrl: string
  keyUrl: string
  // Monogram for the icon ("OA").
  icon: string
  description: string
}

export interface SpeechProvidersList {
  // Local engines first, then the server's providers.
  providers: SpeechProviderView[]
  // Why the server's providers are missing ('' when listed).
  serverError: string
  // The user may set global providers' keys (admin).
  canManageGlobal: boolean
}

export interface SttChoice {
  // '' when none.
  providerId: string
  // '' : the provider's default.
  model: string
  // ISO 639-1 / BCP 47 hint; '' detects.
  language: string
}

export interface TtsChoice {
  providerId: string
  model: string
  voice: string
  // 1 is normal; 0 means 1.
  speed: number
}

export interface SpeechSettings {
  // What applies on this device.
  stt: SttChoice
  tts: TtsChoice
  // Shared by all devices (global providers only).
  synced: { stt: SttChoice, tts: TtsChoice }
  // This device only (local engine or 'browser'); providerId '' follows the synced choice.
  device: { stt: SttChoice, tts: TtsChoice }
  // The synced part can be stored (the client is connected to a server).
  syncedAvailable: boolean
}

// Each set part replaces that part as a whole. Synced parts take global provider ids, device parts local ones or
// 'browser' ('' clears).
export interface SpeechSettingsPatch {
  syncedStt?: SttChoice
  syncedTts?: TtsChoice
  deviceStt?: SttChoice
  deviceTts?: TtsChoice
}

export interface TranscribeOptions {
  // 'audio/webm;codecs=opus' (MediaRecorder), 'audio/wav', ...
  mimeType: string
  // Omitted: the effective STT settings.
  providerId?: string
  model?: string
  language?: string
}

export interface Transcription {
  text: string
  language: string
  durationSeconds: number
  providerId: string
  model: string
}

export interface SynthesizeOptions {
  // The text is plain already (skip the Markdown cleanup).
  plainText?: boolean
  // Omitted: the effective TTS settings.
  providerId?: string
  model?: string
  voice?: string
  speed?: number
}

export interface SynthesisStart {
  // 'audio/mpeg' or 'audio/wav'.
  mimeType: string
  providerId: string
  model: string
  voice: string
}

export interface SynthesisHandlers {
  // Before the first audio bytes.
  onStart?: (info: SynthesisStart) => void
  // Audio in order; concatenated they form one playable file (MP3 frames, or one WAV).
  onChunk?: (bytes: Uint8Array) => void
}

// A running synthesis: done resolves with the whole audio (rejects on error or cancel); cancel stops it.
export interface SynthesisHandle {
  done: Promise<{ audio: Blob, mimeType: string, characters: number }>
  cancel(): void
}

export interface LocalSpeechModelView {
  // '<provider>/<model>', e.g. 'parakeet/parakeet-tdt-0.6b-v3', 'piper/en_US-lessac-medium', 'runtime/sherpa-onnx'.
  id: string
  providerId: string
  model: string
  name: string
  // 'runtime': the engine runtime every Parakeet / Whisper / Piper model needs (downloaded along with the first).
  kind: SpeechKindName | 'runtime'
  installed: boolean
  sizeBytes: number
  downloading: boolean
  description: string
  languages: string[]
  recommended: boolean
  // No runtime build for this platform.
  unsupported: boolean
}

// A download being watched: done resolves with the installed model; cancel stops watching (the client keeps
// downloading; deleteModel stops it).
export interface DownloadHandle {
  done: Promise<LocalSpeechModelView | undefined>
  cancel(): void
}

export interface SpeechApi {
  listProviders(kind?: SpeechKindName): Promise<SpeechProvidersList>
  listVoices(providerId: string, model?: string): Promise<SpeechVoiceView[]>
  getSettings(): Promise<SpeechSettings>
  updateSettings(patch: SpeechSettingsPatch): Promise<SpeechSettings>
  transcribe(audio: Blob | Uint8Array, opts: TranscribeOptions): Promise<Transcription>
  // text: Markdown as shown (cleaned up for speech by the client) unless opts.plainText.
  synthesize(text: string, opts?: SynthesizeOptions, handlers?: SynthesisHandlers): SynthesisHandle
  listLocalModels(kind?: SpeechKindName): Promise<{ models: LocalSpeechModelView[], modelsDir: string }>
  // Progress: bytes so far and the expected total (0 while unknown; includes the runtime when it comes along).
  downloadModel(id: string, onProgress?: (downloaded: number, total: number) => void): DownloadHandle
  // Stops a running download too.
  deleteModel(id: string): Promise<{ cancelledDownload: boolean }>
  // Admins: a global provider's API key (write-only) and optional base URL; returns its views per kind.
  setProviderKey(providerId: string, apiKey: string, baseUrl?: string): Promise<SpeechProviderView[]>
  clearProviderKey(providerId: string): Promise<void>
}
