// Dictation: records the microphone and turns it into text with the speech-to-text provider of Settings › Speech
// (an engine on this computer, the server's, or the browser's own recognition). Records what the provider takes
// as it is: compressed audio from MediaRecorder when it accepts it, else 16 kHz WAV (local engines without
// ffmpeg). `levels` feeds the waveform while recording.
import { BROWSER_SPEECH_PROVIDER, type SpeechProviderView } from '~/lib/speech/types'

// Enough bars to fill a wide composer; the newest is on the right.
const BARS = 240
const WAV_RATE = 16_000

type Recognition = { lang: string, continuous: boolean, interimResults: boolean, start(): void, stop(): void, abort(): void, onresult: ((e: { results: ArrayLike<ArrayLike<{ transcript: string }>> }) => void) | null, onerror: ((e: { error: string }) => void) | null, onend: (() => void) | null }

export function useDictation() {
  const store = useSessionStore()
  const state = ref<'idle' | 'starting' | 'recording' | 'transcribing'>('idle')
  const levels = ref<number[]>(Array(BARS).fill(0))
  const startedAt = ref(0)

  let stream: MediaStream | null = null
  let ctx: AudioContext | null = null
  let timer: ReturnType<typeof setInterval> | null = null
  let recorder: MediaRecorder | null = null
  let chunks: Blob[] = []
  let pcm: Float32Array[] = []
  let recognition: Recognition | null = null
  let recognized = ''
  let provider: SpeechProviderView | undefined
  let mimeType = ''
  let language = ''

  function cleanup() {
    if (timer) clearInterval(timer)
    timer = null
    stream?.getTracks().forEach(t => t.stop())
    stream = null
    void ctx?.close().catch(() => {})
    ctx = null
    recorder = null
    recognition = null
    levels.value = Array(BARS).fill(0)
  }

  // The provider in effect for dictation, and the audio type to record for it.
  async function choose() {
    const api = store.transport!.speech()
    const [settings, list] = await Promise.all([api.getSettings(), api.listProviders('stt')])
    const id = settings.stt.providerId
    if (!id) throw new Error('Choose a dictation provider in Settings › Speech.')
    language = settings.stt.language
    if (id === BROWSER_SPEECH_PROVIDER) return BROWSER_SPEECH_PROVIDER
    provider = list.providers.find(p => p.id === id)
    if (!provider) throw new Error('The dictation provider of Settings › Speech is not available.')
    if (!provider.available) throw new Error(provider.unavailableReason || `${provider.name} is not set up.`)
    const accepted = provider.inputMimeTypes
    const compressed = ['audio/webm;codecs=opus', 'audio/webm', 'audio/ogg;codecs=opus', 'audio/mp4']
      .find(t => typeof MediaRecorder !== 'undefined' && MediaRecorder.isTypeSupported(t) && (!accepted.length || accepted.some(a => t.startsWith(a.split(';')[0]!))))
    return compressed ?? 'audio/wav'
  }

  async function start() {
    if (state.value !== 'idle') return
    state.value = 'starting'
    try {
      mimeType = await choose()
      if (mimeType === BROWSER_SPEECH_PROVIDER) return startBrowser()
      stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } })
      ctx = new AudioContext()
      const source = ctx.createMediaStreamSource(stream)
      const analyser = ctx.createAnalyser()
      analyser.fftSize = 512
      source.connect(analyser)
      const buf = new Uint8Array(analyser.fftSize)
      timer = setInterval(() => {
        analyser.getByteTimeDomainData(buf)
        let sum = 0
        for (const v of buf) sum += ((v - 128) / 128) ** 2
        levels.value = [...levels.value.slice(1), Math.min(1, Math.sqrt(sum / buf.length) * 4)]
      }, 80)
      if (mimeType === 'audio/wav') {
        pcm = []
        // ScriptProcessor keeps this dependency-free; the audio is short.
        const node = ctx.createScriptProcessor(4096, 1, 1)
        node.onaudioprocess = e => pcm.push(new Float32Array(e.inputBuffer.getChannelData(0)))
        source.connect(node)
        node.connect(ctx.destination)
      } else {
        chunks = []
        recorder = new MediaRecorder(stream, { mimeType })
        recorder.ondataavailable = e => e.data.size && chunks.push(e.data)
        recorder.start(250)
      }
      startedAt.value = Date.now()
      state.value = 'recording'
    } catch (e) {
      cleanup()
      state.value = 'idle'
      throw e instanceof DOMException && e.name === 'NotAllowedError' ? new Error('Microphone access was denied.') : e
    }
  }

  function startBrowser() {
    const W = window as unknown as { SpeechRecognition?: new () => Recognition, webkitSpeechRecognition?: new () => Recognition }
    const Ctor = W.SpeechRecognition ?? W.webkitSpeechRecognition
    if (!Ctor) {
      state.value = 'idle'
      throw new Error('This browser has no built-in dictation; pick another provider in Settings › Speech.')
    }
    recognized = ''
    recognition = new Ctor()
    recognition.lang = language || navigator.language
    recognition.continuous = true
    recognition.interimResults = false
    recognition.onresult = (e) => {
      recognized = Array.from(e.results, r => r[0]?.transcript ?? '').join(' ')
      levels.value = [...levels.value.slice(1), 0.6]
    }
    recognition.start()
    startedAt.value = Date.now()
    state.value = 'recording'
  }

  // keep: transcribe and return the text; otherwise discard.
  async function stop(keep: boolean): Promise<string> {
    if (state.value !== 'recording') {
      cleanup()
      state.value = 'idle'
      return ''
    }
    if (recognition) {
      const r = recognition
      await new Promise<void>((resolve) => {
        r.onend = () => resolve()
        if (keep) r.stop()
        else r.abort()
      })
      cleanup()
      state.value = 'idle'
      return keep ? recognized.trim() : ''
    }
    const audio = await new Promise<Blob>((resolve) => {
      if (recorder && recorder.state !== 'inactive') {
        recorder.onstop = () => resolve(new Blob(chunks, { type: mimeType }))
        recorder.stop()
      } else {
        resolve(wav(pcm, ctx?.sampleRate ?? 48_000))
      }
    })
    cleanup()
    if (!keep) {
      state.value = 'idle'
      return ''
    }
    state.value = 'transcribing'
    try {
      const res = await store.transport!.speech().transcribe(audio, { mimeType })
      return res.text.trim()
    } finally {
      state.value = 'idle'
    }
  }

  onBeforeUnmount(() => {
    recognition?.abort()
    cleanup()
  })

  return { state, levels, startedAt, start, stop }
}

// Mono 16-bit PCM WAV at 16 kHz (what speech engines expect), downsampled from the context's rate.
function wav(parts: Float32Array[], rate: number): Blob {
  const total = parts.reduce((n, p) => n + p.length, 0)
  const ratio = rate / WAV_RATE
  const n = Math.floor(total / ratio)
  const flat = new Float32Array(total)
  let o = 0
  for (const p of parts) {
    flat.set(p, o)
    o += p.length
  }
  const out = new DataView(new ArrayBuffer(44 + n * 2))
  const str = (at: number, s: string) => [...s].forEach((c, i) => out.setUint8(at + i, c.charCodeAt(0)))
  str(0, 'RIFF')
  out.setUint32(4, 36 + n * 2, true)
  str(8, 'WAVE')
  str(12, 'fmt ')
  out.setUint32(16, 16, true)
  out.setUint16(20, 1, true)
  out.setUint16(22, 1, true)
  out.setUint32(24, WAV_RATE, true)
  out.setUint32(28, WAV_RATE * 2, true)
  out.setUint16(32, 2, true)
  out.setUint16(34, 16, true)
  str(36, 'data')
  out.setUint32(40, n * 2, true)
  for (let i = 0; i < n; i++) {
    const s = Math.max(-1, Math.min(1, flat[Math.floor(i * ratio)] ?? 0))
    out.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7FFF, true)
  }
  return new Blob([out.buffer], { type: 'audio/wav' })
}
