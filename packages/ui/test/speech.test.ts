import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { LocalSpeechModelSchema, SpeechSettingsSchema } from '~/gen/harness/agent/v1/speech_pb'
import { SpeechKind, SpeechLocation, SpeechProviderSchema } from '~/gen/harness/speech/v1/speech_pb'
import { localModelView, speechKindPb, speechProviderView, speechSettingsView } from '~/lib/speech/connect'
import { MockSpeechApi } from '~/lib/speech/mock'
import { MockTransport } from '~/lib/transport/mock'

describe('speech views', () => {
  it('maps providers, settings and local models', () => {
    const p = speechProviderView(create(SpeechProviderSchema, {
      id: 'parakeet', displayName: 'NVIDIA Parakeet', kind: SpeechKind.STT, location: SpeechLocation.LOCAL, available: true,
      models: [{ id: 'm', displayName: 'M', installed: true, sizeBytes: 10n }], inputMimeTypes: ['audio/wav']
    }))
    expect(p).toMatchObject({ id: 'parakeet', kind: 'stt', location: 'local', available: true, models: [{ id: 'm', installed: true, sizeBytes: 10 }], inputMimeTypes: ['audio/wav'] })

    const s = speechSettingsView(create(SpeechSettingsSchema, {
      stt: { providerId: 'parakeet' }, syncedStt: { providerId: 'openai', language: 'de' }, deviceStt: { providerId: 'parakeet' }, syncedAvailable: true
    }))
    expect(s.stt.providerId).toBe('parakeet')
    expect(s.synced.stt).toEqual({ providerId: 'openai', model: '', language: 'de' })
    expect(s.device.tts).toEqual({ providerId: '', model: '', voice: '', speed: 0 })

    expect(localModelView(create(LocalSpeechModelSchema, { id: 'runtime/sherpa-onnx', kind: SpeechKind.UNSPECIFIED })).kind).toBe('runtime')
    expect(speechKindPb('tts')).toBe(SpeechKind.TTS)
    expect(speechKindPb()).toBe(SpeechKind.UNSPECIFIED)
  })
})

describe('MockSpeechApi', () => {
  it('is reachable from the mock transport', () => {
    expect(new MockTransport().speech()).toBeInstanceOf(MockSpeechApi)
  })

  it('keeps synced and device choices apart, the device one winning', async () => {
    const api = new MockSpeechApi()
    let s = await api.getSettings()
    expect(s.stt.providerId).toBe('openai')
    await expect(api.updateSettings({ syncedStt: { providerId: 'parakeet', model: '', language: '' } })).rejects.toThrow(/device/)
    await expect(api.updateSettings({ deviceTts: { providerId: 'openai', model: '', voice: '', speed: 1 } })).rejects.toThrow(/synced/)
    s = await api.updateSettings({ deviceStt: { providerId: 'parakeet', model: '', language: '' } })
    expect(s.stt.providerId).toBe('parakeet')
    expect(s.synced.stt.providerId).toBe('openai')
    s = await api.updateSettings({ deviceStt: { providerId: '', model: '', language: '' } })
    expect(s.stt.providerId).toBe('openai')
  })

  it('transcribes, synthesizes a playable WAV in chunks, downloads models', async () => {
    const api = new MockSpeechApi()
    const t = await api.transcribe(new Uint8Array(32000), { mimeType: 'audio/webm;codecs=opus' })
    expect(t).toMatchObject({ providerId: 'openai', durationSeconds: 2 })

    const chunks: Uint8Array[] = []
    let mime = ''
    const out = await api.synthesize('**Hello** there', {}, { onStart: i => (mime = i.mimeType), onChunk: c => chunks.push(c) }).done
    expect(mime).toBe('audio/wav')
    expect(chunks.length).toBeGreaterThan(0)
    const head = new Uint8Array(await out.audio.slice(0, 4).arrayBuffer())
    expect(String.fromCharCode(...head)).toBe('RIFF')
    expect(out.characters).toBe('Hello there'.length)

    const before = (await api.listProviders('stt')).providers.find(p => p.id === 'whisper')!
    expect(before.available).toBe(false)
    const progress: number[] = []
    const model = await api.downloadModel('whisper/base', d => progress.push(d)).done
    expect(model?.installed).toBe(true)
    expect(progress.at(-1)).toBe(199_000_000)
    expect((await api.listProviders('stt')).providers.find(p => p.id === 'whisper')!.available).toBe(true)
    expect((await api.deleteModel('whisper/base')).cancelledDownload).toBe(false)
  }, 10_000)

  it('manages global keys', async () => {
    const api = new MockSpeechApi()
    const views = await api.setProviderKey('elevenlabs', 'sk-elevenlabs-0000-1234')
    expect(views[0]).toMatchObject({ keySet: true, keyHint: '…1234', available: true })
    await api.clearProviderKey('elevenlabs')
    expect((await api.listProviders('tts')).providers.find(p => p.id === 'elevenlabs')!.configured).toBe(false)
  })
})
