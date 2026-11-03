// Read aloud: one message speaks at a time, app-wide. Uses the browser's voice (Web Speech) for now; the speech
// providers of Settings › Speech take over once they are set up.
import { guessLang, speakable } from '~/lib/speech/speakable'

export function useReadAloud() {
  const speaking = useState<string>('read-aloud', () => '')
  const supported = import.meta.client && 'speechSynthesis' in window

  function stop() {
    if (supported) window.speechSynthesis.cancel()
    speaking.value = ''
  }

  function toggle(key: string, markdown: string) {
    if (!supported) return
    if (speaking.value === key) return stop()
    stop()
    const text = speakable(markdown)
    if (!text) return
    const u = new SpeechSynthesisUtterance(text)
    const lang = guessLang(text)
    if (lang) u.lang = lang
    u.onend = u.onerror = () => {
      if (speaking.value === key) speaking.value = ''
    }
    speaking.value = key
    window.speechSynthesis.speak(u)
  }

  return { speaking, supported, toggle, stop }
}
