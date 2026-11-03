// SpeechApi over AgentService's speech RPCs, on the same connect transport (and agent token) as the rest of
// AgentService; the admin key methods go to harness.speech.v1.SpeechService, which the client forwards to the
// server (packages/client/server/coreproxy.go).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import type { LocalSpeechModel, SpeechSettings as SpeechSettingsPb, SttSettings, TtsSettings } from '~/gen/harness/agent/v1/speech_pb'
import { SpeechKind, SpeechLocation, SpeechService, type SpeechProvider, type SpeechVoice } from '~/gen/harness/speech/v1/speech_pb'
import type {
  DownloadHandle,
  LocalSpeechModelView,
  SpeechApi,
  SpeechKindName,
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

export const speechKindPb = (k?: SpeechKindName) => (k === 'stt' ? SpeechKind.STT : k === 'tts' ? SpeechKind.TTS : SpeechKind.UNSPECIFIED)
const kindName = (k: SpeechKind): SpeechKindName => (k === SpeechKind.TTS ? 'tts' : 'stt')
const locationName = (l: SpeechLocation) => (l === SpeechLocation.LOCAL ? 'local' : l === SpeechLocation.BROWSER ? 'browser' : 'global')
const genderName = (g: string): SpeechVoiceView['gender'] => (g === 'female' || g === 'male' || g === 'neutral' ? g : '')

export function voiceView(v: SpeechVoice): SpeechVoiceView {
  return {
    id: v.id,
    name: v.displayName || v.id,
    language: v.language,
    gender: genderName(v.gender),
    description: v.description,
    previewUrl: v.previewUrl,
    installed: v.installed,
    sizeBytes: Number(v.sizeBytes)
  }
}

export function speechProviderView(p: SpeechProvider): SpeechProviderView {
  return {
    id: p.id,
    name: p.displayName || p.id,
    kind: kindName(p.kind),
    location: locationName(p.location),
    configured: p.configured,
    available: p.available,
    unavailableReason: p.unavailableReason,
    models: p.models.map(m => ({
      id: m.id,
      name: m.displayName || m.id,
      description: m.description,
      installed: m.installed,
      sizeBytes: Number(m.sizeBytes),
      voiceIds: [...m.voiceIds],
      languages: [...m.languages]
    })),
    voices: p.voices.map(voiceView),
    listsVoices: p.listsVoices,
    defaultModel: p.defaultModel,
    defaultVoice: p.defaultVoice,
    supportsSpeed: p.supportsSpeed,
    supportsLanguage: p.supportsLanguage,
    inputMimeTypes: [...p.inputMimeTypes],
    keySet: p.apiKeySet,
    keyHint: p.apiKeyHint,
    baseUrl: p.baseUrl,
    docsUrl: p.docsUrl,
    keyUrl: p.keyUrl,
    icon: p.icon,
    description: p.description
  }
}

const stt = (s?: SttSettings): SttChoice => ({ providerId: s?.providerId ?? '', model: s?.model ?? '', language: s?.language ?? '' })
const tts = (s?: TtsSettings): TtsChoice => ({ providerId: s?.providerId ?? '', model: s?.model ?? '', voice: s?.voice ?? '', speed: s?.speed ?? 0 })

export function speechSettingsView(s?: SpeechSettingsPb): SpeechSettings {
  return {
    stt: stt(s?.stt),
    tts: tts(s?.tts),
    synced: { stt: stt(s?.syncedStt), tts: tts(s?.syncedTts) },
    device: { stt: stt(s?.deviceStt), tts: tts(s?.deviceTts) },
    syncedAvailable: s?.syncedAvailable ?? false
  }
}

export function localModelView(m: LocalSpeechModel): LocalSpeechModelView {
  return {
    id: m.id,
    providerId: m.providerId,
    model: m.model,
    name: m.displayName || m.id,
    kind: m.kind === SpeechKind.UNSPECIFIED ? 'runtime' : kindName(m.kind),
    installed: m.installed,
    sizeBytes: Number(m.sizeBytes),
    downloading: m.downloading,
    description: m.description,
    languages: [...m.languages],
    recommended: m.recommended,
    unsupported: m.unsupported
  }
}

async function bytesOf(audio: Blob | Uint8Array): Promise<Uint8Array> {
  return audio instanceof Uint8Array ? audio : new Uint8Array(await audio.arrayBuffer())
}

export class ConnectSpeechApi implements SpeechApi {
  private client: Client<typeof AgentService>
  private core: Client<typeof SpeechService>

  constructor(transport: Transport) {
    this.client = createClient(AgentService, transport)
    this.core = createClient(SpeechService, transport)
  }

  async listProviders(kind?: SpeechKindName) {
    const res = await this.client.listSpeechProviders({ kind: speechKindPb(kind) })
    return { providers: res.providers.map(speechProviderView), serverError: res.serverError, canManageGlobal: res.canManageGlobal }
  }

  async listVoices(providerId: string, model = '') {
    return (await this.client.listSpeechVoices({ providerId, model })).voices.map(voiceView)
  }

  async getSettings() {
    return speechSettingsView((await this.client.getSpeechSettings({})).settings)
  }

  async updateSettings(patch: SpeechSettingsPatch) {
    const res = await this.client.updateSpeechSettings({
      syncedStt: patch.syncedStt,
      syncedTts: patch.syncedTts,
      deviceStt: patch.deviceStt,
      deviceTts: patch.deviceTts
    })
    return speechSettingsView(res.settings)
  }

  async transcribe(audio: Blob | Uint8Array, opts: TranscribeOptions) {
    const res = await this.client.transcribeAudio({
      audio: await bytesOf(audio),
      mimeType: opts.mimeType,
      providerId: opts.providerId ?? '',
      model: opts.model ?? '',
      language: opts.language ?? ''
    })
    return { text: res.text, language: res.language, durationSeconds: res.durationSeconds, providerId: res.providerId, model: res.model }
  }

  synthesize(text: string, opts: SynthesizeOptions = {}, handlers: SynthesisHandlers = {}): SynthesisHandle {
    const abort = new AbortController()
    const done = (async () => {
      const parts: Uint8Array<ArrayBuffer>[] = []
      let mimeType = ''
      let characters = 0
      const stream = this.client.synthesizeSpeech(
        { text, plainText: opts.plainText ?? false, providerId: opts.providerId ?? '', model: opts.model ?? '', voice: opts.voice ?? '', speed: opts.speed ?? 0 },
        { signal: abort.signal }
      )
      for await (const r of stream) {
        if (r.mimeType && !mimeType) {
          mimeType = r.mimeType
          handlers.onStart?.({ mimeType, providerId: r.providerId, model: r.model, voice: r.voice })
        }
        if (r.audio.length) {
          const chunk = new Uint8Array(r.audio)
          parts.push(chunk)
          handlers.onChunk?.(chunk)
        }
        if (r.done) characters = Number(r.characters)
      }
      return { audio: new Blob(parts, { type: mimeType }), mimeType, characters }
    })()
    return { done, cancel: () => abort.abort() }
  }

  async listLocalModels(kind?: SpeechKindName) {
    const res = await this.client.listLocalSpeechModels({ kind: speechKindPb(kind) })
    return { models: res.models.map(localModelView), modelsDir: res.modelsDir }
  }

  downloadModel(id: string, onProgress?: (downloaded: number, total: number) => void): DownloadHandle {
    const abort = new AbortController()
    const done = (async () => {
      let model: LocalSpeechModelView | undefined
      for await (const r of this.client.downloadSpeechModel({ id }, { signal: abort.signal })) {
        onProgress?.(Number(r.downloadedBytes), Number(r.totalBytes))
        if (r.done && r.model) model = localModelView(r.model)
      }
      return model
    })()
    return { done, cancel: () => abort.abort() }
  }

  async deleteModel(id: string) {
    return { cancelledDownload: (await this.client.deleteSpeechModel({ id })).cancelledDownload }
  }

  async setProviderKey(providerId: string, apiKey: string, baseUrl = '') {
    return (await this.core.setProviderKey({ providerId, apiKey, baseUrl })).providers.map(speechProviderView)
  }

  async clearProviderKey(providerId: string) {
    await this.core.clearProviderKey({ providerId })
  }
}
