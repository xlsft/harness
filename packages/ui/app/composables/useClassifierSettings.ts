// The server-wide auto-mode classifier as the Classifiers page edits it: which connected classifier is in use
// (none: rules, then the model) and which chat model decides when it is unsure. Every change saves at once.
// Shared through provide/inject between the page, the provider cards and the model row.
import type { InjectionKey, Ref } from 'vue'
import type { ClassifierSettings } from '~/lib/providers/types'

export interface ClassifierState {
  settings: Ref<ClassifierSettings | null>
  use: (providerId: string) => Promise<void>
  setModel: (id: string) => Promise<void>
}

export const CLASSIFIER: InjectionKey<ClassifierState> = Symbol('classifier')

export function createClassifierState(onError: (msg: string) => void): ClassifierState {
  const store = useSessionStore()
  const settings = ref<ClassifierSettings | null>(null)
  const api = () => store.transport!.providers()
  const message = (e: unknown) => (e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e))

  async function save(patch: Partial<ClassifierSettings>) {
    if (!settings.value) return
    const before = settings.value
    settings.value = { ...before, ...patch }
    try {
      settings.value = await api().setClassifier({ ...settings.value, enabled: !!settings.value.providerId })
    } catch (e) {
      settings.value = before
      onError(message(e))
    }
  }

  api().getClassifier().then((c) => {
    settings.value = c
  }, e => onError(message(e)))

  return {
    settings,
    // A provider's own default decision model and endpoint apply after a switch.
    use: id => save({ providerId: id, model: '', endpoint: '' }),
    setModel: id => save({ llmModel: id })
  }
}
