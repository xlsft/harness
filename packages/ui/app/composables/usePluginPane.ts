// Plugin UI (docs/design/plugin-ui.md): which plugin pane is open in each session's right pane, and the panes and status-bar
// items the client says may show there. The list is asked again when the session or its folder changes, and when Extensions
// changes a plugin (refresh()).
import { ICONS, type IconName } from '~/utils/icons'
import type { PluginUiListVM } from '~/lib/plugins/types'

export interface OpenPluginPane {
  pluginId: string
  paneId: string
  title: string
}

const EMPTY: PluginUiListVM = { panes: [], statusItems: [], pluginNames: {}, blocked: '', contentHashes: {} }

// An icon a plugin names, if the app has it.
export function pluginIcon(name: string, fallback: IconName = 'layout'): IconName {
  return name in ICONS ? name as IconName : fallback
}

export function usePluginPane() {
  const open = useState<Record<string, OpenPluginPane | undefined>>('plugin-pane', () => ({}))
  const lists = useState<Record<string, PluginUiListVM>>('plugin-ui-lists', () => ({}))
  const version = useState('plugin-ui-version', () => 0)
  const { openPane } = usePanes()
  const store = useSessionStore()

  function show(sessionId: string, pane: OpenPluginPane) {
    open.value = { ...open.value, [sessionId]: pane }
    openPane('plugin')
  }

  // The client answers only for sessions it runs; anything else shows no plugin UI.
  async function load(sessionId: string) {
    const s = store.sessions[sessionId]
    if (!store.transport || !s || s.meta.remote) {
      lists.value = { ...lists.value, [sessionId]: EMPTY }
      return
    }
    try {
      lists.value = { ...lists.value, [sessionId]: await store.transport.plugins().listUi(sessionId) }
    } catch {
      lists.value = { ...lists.value, [sessionId]: EMPTY }
    }
  }

  function refresh() {
    version.value++
  }

  return { open, lists, version, show, load, refresh }
}
