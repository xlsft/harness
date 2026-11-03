// SpeechApi for the mock transport: a few local engines and global providers, settings split into synced and device
// parts with the client's validation, canned transcripts, a generated tone as synthesized speech (streamed in
// chunks), and simulated model downloads.

import type {
  DownloadHandle,
  LocalSpeechModelView,
  SpeechApi,
  SpeechKindName,
  SpeechModelView,
  SpeechProviderView,
  SpeechSettings,
  SpeechSettingsPatch,
  SpeechVoiceView,
  SttChoice,
  SynthesisHandle,
  SynthesisHandlers,
  SynthesizeOptions,
  TranscribeOptions,
  TtsChoice
} from './types'
import { BROWSER_SPEECH_PROVIDER } from './types'

const LOCAL_IDS = new Set(['parakeet', 'whisper', 'piper', 'macos-say'])
const WAV_TYPES = ['audio/wav', 'audio/x-wav', 'audio/wave']
const COMMON_AUDIO = ['audio/webm', 'audio/ogg', ...WAV_TYPES, 'audio/mpeg', 'audio/mp4']

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms)
    signal?.addEventListener('abort', () => {
      clearTimeout(t)
      reject(new DOMException('aborted', 'AbortError'))
    }, { once: true })
  })

function model(id: string, name: string, extra: Partial<SpeechModelView> = {}): SpeechModelView {
  return { id, name, description: '', installed: false, sizeBytes: 0, voiceIds: [], languages: [], ...extra }
}

function voice(id: string, name: string, extra: Partial<SpeechVoiceView> = {}): SpeechVoiceView {
  return { id, name, language: '', gender: '', description: '', previewUrl: '', installed: false, sizeBytes: 0, ...extra }
}

function provider(p: Partial<SpeechProviderView> & Pick<SpeechProviderView, 'id' | 'name' | 'kind' | 'location'>): SpeechProviderView {
  return {
    configured: true,
    available: true,
    unavailableReason: '',
    models: [],
    voices: [],
    listsVoices: false,
    defaultModel: '',
    defaultVoice: '',
    supportsSpeed: false,
    supportsLanguage: false,
    inputMimeTypes: [],
    keySet: false,
    keyHint: '',
    baseUrl: '',
    docsUrl: '',
    keyUrl: '',
    icon: p.name.slice(0, 2).toUpperCase(),
    description: '',
    ...p
  }
}

interface MockLocalModel extends LocalSpeechModelView {
  progress?: number
}

// A short 440 Hz tone as a 16-bit mono WAV, long enough to hear something per character.
function toneWav(text: string, speed: number): Uint8Array {
  const rate = 16000
  const seconds = Math.min(8, Math.max(0.5, text.length * 0.05)) / (speed || 1)
  const n = Math.floor(rate * seconds)
  const buf = new ArrayBuffer(44 + n * 2)
  const v = new DataView(buf)
  const str = (o: number, s: string) => [...s].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)))
  str(0, 'RIFF')
  v.setUint32(4, 36 + n * 2, true)
  str(8, 'WAVEfmt ')
  v.setUint32(16, 16, true)
  v.setUint16(20, 1, true)
  v.setUint16(22, 1, true)
  v.setUint32(24, rate, true)
  v.setUint32(28, rate * 2, true)
  v.setUint16(32, 2, true)
  v.setUint16(34, 16, true)
  str(36, 'data')
  v.setUint32(40, n * 2, true)
  for (let i = 0; i < n; i++) {
    const fade = Math.min(1, i / 800, (n - i) / 800)
    v.setInt16(44 + i * 2, Math.round(6000 * fade * Math.sin((2 * Math.PI * 440 * i) / rate)), true)
  }
  return new Uint8Array(buf)
}

export class MockSpeechApi implements SpeechApi {
  private keys = new Map<string, string>([['openai', '…k3y9']])
  private synced: { stt: SttChoice, tts: TtsChoice } = {
    stt: { providerId: 'openai', model: 'gpt-4o-mini-transcribe', language: '' },
    tts: { providerId: 'openai', model: 'gpt-4o-mini-tts', voice: 'alloy', speed: 1 }
  }

  private device: { stt: SttChoice, tts: TtsChoice } = {
    stt: { providerId: '', model: '', language: '' },
    tts: { providerId: '', model: '', voice: '', speed: 0 }
  }

  private models: MockLocalModel[] = [
    { id: 'runtime/sherpa-onnx', providerId: 'runtime', model: 'sherpa-onnx', name: 'sherpa-onnx runtime v1.13.8', kind: 'runtime', installed: true, sizeBytes: 60_000_000, downloading: false, description: 'Runs Parakeet, Whisper and Piper on this machine', languages: [], recommended: false, unsupported: false },
    { id: 'parakeet/parakeet-tdt-0.6b-v3', providerId: 'parakeet', model: 'parakeet-tdt-0.6b-v3', name: 'Parakeet TDT 0.6B v3', kind: 'stt', installed: true, sizeBytes: 670_000_000, downloading: false, description: 'NVIDIA Parakeet, 25 European languages, fast on CPU', languages: ['en', 'de', 'fr', 'es', 'ru'], recommended: true, unsupported: false },
    { id: 'parakeet/parakeet-tdt-0.6b-v2', providerId: 'parakeet', model: 'parakeet-tdt-0.6b-v2', name: 'Parakeet TDT 0.6B v2', kind: 'stt', installed: false, sizeBytes: 460_000_000, downloading: false, description: 'NVIDIA Parakeet, English only', languages: ['en'], recommended: false, unsupported: false },
    { id: 'whisper/base', providerId: 'whisper', model: 'base', name: 'Whisper base', kind: 'stt', installed: false, sizeBytes: 199_000_000, downloading: false, description: 'Fast, good for dictation, 99 languages', languages: [], recommended: true, unsupported: false },
    { id: 'whisper/small', providerId: 'whisper', model: 'small', name: 'Whisper small', kind: 'stt', installed: false, sizeBytes: 610_000_000, downloading: false, description: 'Better accuracy, slower', languages: [], recommended: false, unsupported: false },
    { id: 'piper/en_US-lessac-medium', providerId: 'piper', model: 'en_US-lessac-medium', name: 'Lessac (en-US, medium)', kind: 'tts', installed: false, sizeBytes: 64_000_000, downloading: false, description: 'US English, neutral', languages: ['en-US'], recommended: true, unsupported: false },
    { id: 'piper/de_DE-thorsten-medium', providerId: 'piper', model: 'de_DE-thorsten-medium', name: 'Thorsten (de-DE, medium)', kind: 'tts', installed: false, sizeBytes: 64_000_000, downloading: false, description: 'German, male', languages: ['de-DE'], recommended: false, unsupported: false }
  ]

  private localModels(provider: string): SpeechModelView[] {
    return this.models.filter(m => m.providerId === provider).map(m => model(m.model, m.name, { description: m.description, installed: m.installed, sizeBytes: m.sizeBytes, languages: m.languages }))
  }

  private providers(): SpeechProviderView[] {
    const rt = this.models.find(m => m.kind === 'runtime')!.installed
    const sttLocal = (id: string, name: string, desc: string, language: boolean) => {
      const models = this.localModels(id)
      const ready = rt && models.some(m => m.installed)
      return provider({
        id, name, kind: 'stt', location: 'local', configured: rt, available: ready, description: desc, models, supportsLanguage: language,
        defaultModel: models.find(m => m.installed)?.id ?? models[0]?.id ?? '', inputMimeTypes: COMMON_AUDIO,
        unavailableReason: ready ? '' : 'Download a model to use it (the speech runtime is downloaded with the first model)'
      })
    }
    const piperVoices = this.models.filter(m => m.providerId === 'piper').map(m => voice(m.model, m.name, { language: m.languages[0] ?? '', description: m.description, installed: m.installed, sizeBytes: m.sizeBytes }))
    const piperReady = rt && piperVoices.some(v => v.installed)
    const global = (id: string, name: string, kind: SpeechKindName, extra: Partial<SpeechProviderView>) => {
      const key = this.keys.get(id)
      return provider({
        id, name, kind, location: 'global', configured: !!key, available: !!key, keySet: !!key, keyHint: key ?? '',
        unavailableReason: key ? '' : 'Not configured on the server: an admin needs to set its API key', ...extra
      })
    }
    return [
      sttLocal('parakeet', 'NVIDIA Parakeet', 'Fast, accurate on-device dictation (25 European languages).', false),
      sttLocal('whisper', 'Whisper', 'OpenAI Whisper on this machine, 99 languages.', true),
      provider({
        id: 'piper', name: 'Piper', kind: 'tts', location: 'local', configured: rt, available: piperReady, voices: piperVoices, supportsSpeed: true,
        defaultVoice: piperVoices.find(v => v.installed)?.id ?? '', unavailableReason: piperReady ? '' : 'Download a voice to use it'
      }),
      provider({
        id: 'macos-say', name: 'macOS voices', kind: 'tts', location: 'local', supportsSpeed: true,
        voices: [voice('Samantha', 'Samantha', { language: 'en-US', installed: true }), voice('Daniel', 'Daniel', { language: 'en-GB', installed: true }), voice('Anna', 'Anna', { language: 'de-DE', installed: true })]
      }),
      global('openai', 'OpenAI', 'stt', {
        models: [model('gpt-4o-mini-transcribe', 'GPT-4o mini Transcribe'), model('gpt-4o-transcribe', 'GPT-4o Transcribe'), model('whisper-1', 'Whisper')],
        defaultModel: 'gpt-4o-mini-transcribe', supportsLanguage: true, inputMimeTypes: COMMON_AUDIO
      }),
      global('groq', 'Groq', 'stt', { models: [model('whisper-large-v3-turbo', 'Whisper large v3 turbo')], defaultModel: 'whisper-large-v3-turbo', supportsLanguage: true, inputMimeTypes: COMMON_AUDIO }),
      global('deepgram', 'Deepgram', 'stt', { models: [model('nova-3', 'Nova 3')], defaultModel: 'nova-3', supportsLanguage: true, inputMimeTypes: COMMON_AUDIO }),
      global('openai', 'OpenAI', 'tts', {
        models: [model('gpt-4o-mini-tts', 'GPT-4o mini TTS'), model('tts-1', 'TTS 1')],
        voices: ['alloy', 'ash', 'coral', 'echo', 'nova', 'onyx', 'sage', 'shimmer'].map(v => voice(v, v[0]!.toUpperCase() + v.slice(1))),
        defaultModel: 'gpt-4o-mini-tts', defaultVoice: 'alloy', supportsSpeed: true
      }),
      global('elevenlabs', 'ElevenLabs', 'tts', {
        models: [model('eleven_flash_v2_5', 'Flash v2.5'), model('eleven_multilingual_v2', 'Multilingual v2')],
        voices: [voice('21m00Tcm4TlvDq8ikWAM', 'Rachel', { gender: 'female', language: 'en' })], listsVoices: true,
        defaultModel: 'eleven_flash_v2_5', defaultVoice: '21m00Tcm4TlvDq8ikWAM', supportsSpeed: true
      }),
      global('minimax', 'MiniMax', 'tts', { models: [model('speech-02-turbo', 'Speech-02 Turbo')], voices: [voice('Wise_Woman', 'Wise Woman')], defaultModel: 'speech-02-turbo', defaultVoice: 'Wise_Woman', supportsSpeed: true })
    ]
  }

  async listProviders(kind?: SpeechKindName) {
    await sleep(80)
    return { providers: this.providers().filter(p => !kind || p.kind === kind), serverError: '', canManageGlobal: true }
  }

  async listVoices(providerId: string) {
    await sleep(60)
    const p = this.providers().find(p => p.id === providerId && p.kind === 'tts')
    if (!p) throw new Error(`unknown text-to-speech provider ${providerId}`)
    if (providerId === 'elevenlabs') return [...p.voices, voice('pNInz6obpgDQGcFmaJgB', 'Adam', { gender: 'male', language: 'en', description: 'From your voice library' })]
    return p.voices
  }

  private settings(): SpeechSettings {
    return {
      stt: { ...(this.device.stt.providerId ? this.device.stt : this.synced.stt) },
      tts: { ...(this.device.tts.providerId ? this.device.tts : this.synced.tts) },
      synced: { stt: { ...this.synced.stt }, tts: { ...this.synced.tts } },
      device: { stt: { ...this.device.stt }, tts: { ...this.device.tts } },
      syncedAvailable: true
    }
  }

  async getSettings() {
    return this.settings()
  }

  async updateSettings(patch: SpeechSettingsPatch) {
    for (const id of [patch.syncedStt?.providerId, patch.syncedTts?.providerId]) {
      if (id && (LOCAL_IDS.has(id) || id === BROWSER_SPEECH_PROVIDER)) throw new Error(`"${id}" runs on this device: choose it as the device setting`)
    }
    for (const id of [patch.deviceStt?.providerId, patch.deviceTts?.providerId]) {
      if (id && !LOCAL_IDS.has(id) && id !== BROWSER_SPEECH_PROVIDER) throw new Error(`"${id}" is a server provider: choose it as the synced setting`)
    }
    if (patch.syncedStt) this.synced.stt = { ...patch.syncedStt }
    if (patch.syncedTts) this.synced.tts = { ...patch.syncedTts }
    if (patch.deviceStt) this.device.stt = { ...patch.deviceStt }
    if (patch.deviceTts) this.device.tts = { ...patch.deviceTts }
    return this.settings()
  }

  async transcribe(audio: Blob | Uint8Array, opts: TranscribeOptions) {
    const size = audio instanceof Uint8Array ? audio.byteLength : audio.size
    if (!size) throw new Error('audio is empty')
    const s = this.settings().stt
    const providerId = opts.providerId || s.providerId
    if (!providerId) throw new Error('no speech-to-text provider is chosen')
    if (providerId === BROWSER_SPEECH_PROVIDER) throw new Error('the browser\'s speech recognition runs in the frontend')
    const p = this.providers().find(p => p.id === providerId && p.kind === 'stt')
    if (!p?.available) throw new Error(p?.unavailableReason || `unknown speech-to-text provider ${providerId}`)
    await sleep(600)
    const durationSeconds = Math.max(1, Math.round(size / 16000))
    return { text: 'This is a mock transcript of what you just said.', language: opts.language || s.language || 'en', durationSeconds, providerId, model: opts.model || s.model || p.defaultModel }
  }

  synthesize(text: string, opts: SynthesizeOptions = {}, handlers: SynthesisHandlers = {}): SynthesisHandle {
    const abort = new AbortController()
    const done = (async () => {
      const s = this.settings().tts
      const providerId = opts.providerId || s.providerId
      if (!providerId) throw new Error('no text-to-speech provider is chosen')
      if (providerId === BROWSER_SPEECH_PROVIDER) throw new Error('the browser\'s speech synthesis runs in the frontend')
      const p = this.providers().find(p => p.id === providerId && p.kind === 'tts')
      if (!p?.available) throw new Error(p?.unavailableReason || `unknown text-to-speech provider ${providerId}`)
      const plain = opts.plainText ? text : text.replace(/```[\s\S]*?```/g, ' ').replace(/[*_`#>|]/g, '').trim()
      if (!plain) throw new Error('nothing to read aloud')
      const wav = toneWav(plain, opts.speed || s.speed || 1)
      const mimeType = 'audio/wav'
      await sleep(200, abort.signal)
      handlers.onStart?.({ mimeType, providerId, model: opts.model || s.model || p.defaultModel, voice: opts.voice || s.voice || p.defaultVoice })
      const parts: Uint8Array<ArrayBuffer>[] = []
      for (let off = 0; off < wav.byteLength; off += 32 * 1024) {
        const chunk = new Uint8Array(wav.slice(off, off + 32 * 1024))
        parts.push(chunk)
        handlers.onChunk?.(chunk)
        await sleep(30, abort.signal)
      }
      return { audio: new Blob(parts, { type: mimeType }), mimeType, characters: [...plain].length }
    })()
    return { done, cancel: () => abort.abort() }
  }

  async listLocalModels(kind?: SpeechKindName) {
    return { models: this.models.filter(m => !kind || m.kind === kind).map(({ progress: _p, ...m }) => ({ ...m, languages: [...m.languages] })), modelsDir: '~/.harness/speech/models' }
  }

  downloadModel(id: string, onProgress?: (downloaded: number, total: number) => void): DownloadHandle {
    const abort = new AbortController()
    const done = (async () => {
      const m = this.models.find(m => m.id === id)
      if (!m) throw new Error(`unknown speech model ${id}`)
      if (m.unsupported) throw new Error(`${m.name} is not available on this platform`)
      const rt = this.models.find(x => x.kind === 'runtime')!
      const total = m.sizeBytes + (m.kind !== 'runtime' && !rt.installed ? rt.sizeBytes : 0)
      if (m.installed) {
        onProgress?.(m.sizeBytes, m.sizeBytes)
        return { ...m }
      }
      m.downloading = true
      m.progress ??= 0
      // The mock "downloads" in about three seconds; a second watcher joins the same progress.
      while (m.progress < total && m.downloading) {
        onProgress?.(m.progress, total)
        await sleep(150, abort.signal)
        m.progress = Math.min(total, m.progress + total / 20)
      }
      if (!m.downloading) throw new Error('the download was cancelled')
      onProgress?.(total, total)
      m.downloading = false
      m.installed = true
      m.progress = undefined
      rt.installed = true
      return { ...m }
    })()
    return { done, cancel: () => abort.abort() }
  }

  async deleteModel(id: string) {
    const m = this.models.find(m => m.id === id)
    if (!m) throw new Error(`unknown speech model ${id}`)
    const cancelledDownload = m.downloading
    m.downloading = false
    m.installed = false
    m.progress = undefined
    return { cancelledDownload }
  }

  async setProviderKey(providerId: string, apiKey: string) {
    if (!apiKey.trim()) throw new Error('api_key is required')
    if (!this.providers().some(p => p.id === providerId && p.location === 'global')) throw new Error(`Unknown speech provider "${providerId}"`)
    this.keys.set(providerId, apiKey.length > 8 ? `…${apiKey.slice(-4)}` : '…')
    return this.providers().filter(p => p.id === providerId)
  }

  async clearProviderKey(providerId: string) {
    this.keys.delete(providerId)
  }
}
