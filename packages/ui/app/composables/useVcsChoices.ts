// The server's code host connections as choices for a "vcs" sign-in's vcs_connection_id. Only admins can list them; for
// anyone else the list is empty and the form keeps a typed id.
import type { OptionChoice } from '~/lib/issues/types'

export function useVcsChoices() {
  const store = useSessionStore()
  const list = ref<OptionChoice[]>([])
  async function load() {
    if (!store.transport) return
    const conns = await store.transport.vcs().connections().catch(() => [])
    list.value = conns.map(c => ({ value: c.id, label: c.name || c.baseUrl, hint: c.healthy ? c.baseUrl : c.lastError || tt('not working') }))
  }
  onMounted(load)
  watch(() => store.transport, load)
  return computed(() => ({ vcs_connection_id: list.value }))
}
