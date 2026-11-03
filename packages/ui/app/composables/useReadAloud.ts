// Read aloud: one message speaks at a time, app-wide. The text-to-speech provider of Settings › Speech speaks it
// (an engine on this computer or the server's); with none chosen, or the browser's own voice chosen, the browser
// speaks it (Web Speech).
import { guessLang, speakable } from '~/lib/speech/speakable'
import { BROWSER_SPEECH_PROVIDER } from '~/lib/speech/types'

let player: HTMLAudioElement | null = null
let synthesis: { cancel(): void } | null = null

const AUTO_KEY = 'harness-read-aloud-auto'

// Read each new answer aloud as it finishes (Settings › Speech); this device only.
export function useAutoReadAloud() {
  const on = useState<boolean>('read-aloud-auto', () => {
    try {
      return import.meta.client && localStorage.getItem(AUTO_KEY) === '1'
    } catch {
      return false
    }
  })
  watch(on, (v) => {
    try {
      localStorage.setItem(AUTO_KEY, v ? '1' : '0')
    } catch { /* not stored */ }
  })
  return on
}

export function useReadAloud() {
  const store = useSessionStore()
  const toast = useToast()
  const speaking = useState<string>('read-aloud', () => '')
  // The key being synthesized (before any sound).
  const loading = useState<string>('read-aloud-loading', () => '')
  const supported = import.meta.client

  function stop() {
    if (import.meta.client && 'speechSynthesis' in window) window.speechSynthesis.cancel()
    synthesis?.cancel()
    synthesis = null
    player?.pause()
    player = null
    speaking.value = ''
    loading.value = ''
  }

  function browser(key: string, text: string) {
    if (!('speechSynthesis' in window)) return
    const u = new SpeechSynthesisUtterance(text)
    const lang = guessLang(text)
    if (lang) u.lang = lang
    u.onend = u.onerror = () => {
      if (speaking.value === key) speaking.value = ''
    }
    speaking.value = key
    window.speechSynthesis.speak(u)
  }

  async function toggle(key: string, markdown: string) {
    if (!supported) return
    if (speaking.value === key || loading.value === key) return stop()
    stop()
    const text = speakable(markdown)
    if (!text) return
    const api = store.transport?.speech()
    let providerId = ''
    try {
      providerId = (await api?.getSettings())?.tts.providerId ?? ''
    } catch { /* no settings: the browser's voice */ }
    if (!api || !providerId || providerId === BROWSER_SPEECH_PROVIDER) return browser(key, text)

    loading.value = key
    speaking.value = key
    try {
      const handle = api.synthesize(text, { plainText: true })
      synthesis = handle
      const { audio } = await handle.done
      if (speaking.value !== key) return
      const url = URL.createObjectURL(audio)
      player = new Audio(url)
      player.onended = player.onerror = () => {
        URL.revokeObjectURL(url)
        if (speaking.value === key) speaking.value = ''
      }
      loading.value = ''
      await player.play()
    } catch (e) {
      if (speaking.value !== key) return
      stop()
      toast.add({ title: 'Could not read aloud', description: e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e), color: 'error' })
    }
  }

  return { speaking, loading, supported, toggle, stop }
}
