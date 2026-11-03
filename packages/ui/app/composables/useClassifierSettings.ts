// The auto-mode classifier as the Classifiers page edits it, on two levels:
//   - the server default (admins): which connected classifier is in use for everyone (none: rules, then the
//     model) and which chat model decides when it is unsure;
//   - the caller's own override (everyone): another connected classifier or none, and another model, or
//     follow the server default.
// Every change saves at once. Shared through provide/inject between the page, the provider cards and the
// model row.
import type { ComputedRef, InjectionKey, Ref } from 'vue'
import type { ClassifierOverride, ClassifierSettings, MyClassifier } from '~/lib/providers/types'

export interface ClassifierState {
  // The server default in full (admins only; null otherwise or while loading).
  settings: Ref<ClassifierSettings | null>
  // The caller's override and the server default it applies over (null while loading).
  my: Ref<MyClassifier | null>
  // The classifier deciding the caller's tool calls ('' none).
  effectiveProviderId: ComputedRef<string>
  // Loads both; the server default in full only for admins.
  load: (admin: boolean) => Promise<void>
  // Admins: the server default.
  setDefault: (providerId: string) => Promise<void>
  setDefaultModel: (id: string) => Promise<void>
  // Everyone: the caller's own choice.
  setMine: (patch: Partial<ClassifierOverride>) => Promise<void>
}

export const CLASSIFIER: InjectionKey<ClassifierState> = Symbol('classifier')

export function createClassifierState(onError: (msg: string) => void): ClassifierState {
  const store = useSessionStore()
  const settings = ref<ClassifierSettings | null>(null)
  const my = ref<MyClassifier | null>(null)
  const api = () => store.transport!.providers()
  const message = (e: unknown) => (e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e))

  // The caller's view of the default follows the admin's edits.
  function syncDefault() {
    if (!settings.value || !my.value) return
    my.value = { ...my.value, defaultProviderId: settings.value.enabled ? settings.value.providerId : '', defaultLlmModel: settings.value.llmModel }
  }

  async function saveDefault(patch: Partial<ClassifierSettings>) {
    if (!settings.value) return
    const before = settings.value
    settings.value = { ...before, ...patch }
    try {
      settings.value = await api().setClassifier({ ...settings.value, enabled: !!settings.value.providerId })
    } catch (e) {
      settings.value = before
      onError(message(e))
    }
    syncDefault()
  }

  async function setMine(patch: Partial<ClassifierOverride>) {
    if (!my.value) return
    const before = my.value
    const mine = { ...before.mine, ...patch }
    if (mine.choice !== 'provider') mine.providerId = ''
    my.value = { ...before, mine }
    try {
      my.value = await api().setMyClassifier(mine)
    } catch (e) {
      my.value = before
      onError(message(e))
    }
  }

  async function load(admin: boolean) {
    try {
      const [m, s] = await Promise.all([api().getMyClassifier(), admin ? api().getClassifier() : Promise.resolve(null)])
      my.value = m
      settings.value = s
    } catch (e) {
      onError(message(e))
    }
  }

  const effectiveProviderId = computed(() => {
    const m = my.value
    if (!m) return ''
    if (m.mine.choice === 'provider') return m.mine.providerId
    return m.mine.choice === 'none' ? '' : m.defaultProviderId
  })

  return {
    settings,
    my,
    effectiveProviderId,
    load,
    // A provider's own default decision model and endpoint apply after a switch.
    setDefault: id => saveDefault({ providerId: id, model: '', endpoint: '' }),
    setDefaultModel: id => saveDefault({ llmModel: id }),
    setMine
  }
}
