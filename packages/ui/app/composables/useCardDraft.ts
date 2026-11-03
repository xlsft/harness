// A draft typed into a transcript card (plan feedback, question answers), kept per card id so it survives
// switching sessions (the card unmounts) and reloads; clear() drops it once the card is answered.
const PREFIX = 'harness-card-draft:'

export function useCardDraft<T>(key: string, initial: () => T) {
  const draft = useState<T>(`card-draft:${key}`, () => {
    try {
      const raw = import.meta.client ? localStorage.getItem(PREFIX + key) : null
      if (raw) return JSON.parse(raw) as T
    } catch { /* unreadable: start over */ }
    return initial()
  })
  watch(draft, (v) => {
    try {
      localStorage.setItem(PREFIX + key, JSON.stringify(v))
    } catch { /* not stored */ }
  }, { deep: true })
  function clear() {
    draft.value = initial()
    try {
      localStorage.removeItem(PREFIX + key)
    } catch { /* nothing stored */ }
  }
  return { draft, clear }
}
