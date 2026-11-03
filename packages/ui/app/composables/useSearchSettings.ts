// The web search provider as the Search page edits it, on two levels like the classifier:
//   - the server default (admins): a provider connected for everyone, or the built-in SearXNG;
//   - the caller's own choice (everyone): any search provider they may use, or follow the default.
// Every change saves at once. Shared through provide/inject between the page and its provider rows.
import type { ComputedRef, InjectionKey, Ref } from 'vue'
import type { ProvidersApi, SearchSettings } from '~/lib/providers/types'
import { BUILTIN_SEARCH_ID } from '~/lib/providers/types'

export interface SearchState {
  // null while loading.
  settings: Ref<SearchSettings | null>
  // The provider serving the caller's searches now.
  effectiveProviderId: ComputedRef<string>
  load: () => Promise<void>
  // Admins: make a provider the default for everyone, or ('') go back to the built-in.
  setDefault: (providerId: string) => Promise<void>
  // Everyone: use a provider for themselves, or ('') follow the default.
  setMine: (providerId: string) => Promise<void>
}

export const SEARCH: InjectionKey<SearchState> = Symbol('search')

export function createSearchState(api: () => ProvidersApi, onError: (msg: string) => void): SearchState {
  const settings = ref<SearchSettings | null>(null)
  const message = (e: unknown) => (e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e))

  async function save(patch: Partial<SearchSettings>, call: () => Promise<SearchSettings>) {
    const before = settings.value
    if (!before) return
    // Optimistic: the switches flip at once and go back if the server refuses.
    const next = { ...before, ...patch }
    settings.value = { ...next, effectiveProviderId: next.myProviderId || next.defaultProviderId }
    try {
      settings.value = await call()
    } catch (e) {
      settings.value = before
      onError(message(e))
    }
  }

  return {
    settings,
    effectiveProviderId: computed(() => settings.value?.effectiveProviderId ?? BUILTIN_SEARCH_ID),
    async load() {
      try {
        settings.value = await api().getSearch()
      } catch (e) {
        onError(message(e))
      }
    },
    setDefault: id => save({ defaultProviderId: id || BUILTIN_SEARCH_ID }, () => api().setSearchDefault(id)),
    setMine: id => save({ myProviderId: id }, () => api().setMySearch(id))
  }
}
