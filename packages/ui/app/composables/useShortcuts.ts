// Global keyboard shortcuts (PLAN.md §4), where the browser lets us have them:
// ⌘J terminal · ⌘⇧B browser · ⌘⇧E files · ⌘; side chat · ⌘B sidebar · ⌘N new session · ⌘, settings ·
// Esc interrupts the running turn (when no card or menu takes it) · ⇧Tab cycles the permission mode · Ctrl+O verbose transcript.
import type { PermissionModeName } from '~/lib/session/types'

const MODE_CYCLE: PermissionModeName[] = ['default', 'accept_edits', 'auto', 'plan']

export function useShortcuts() {
  const store = useSessionStore()
  const route = useRoute()
  const { togglePane } = usePanes()
  const collapsed = useState('sidebar-collapsed', () => false)
  const verbose = useState('transcript-verbose', () => false)

  defineShortcuts({
    'meta_j': () => togglePane('terminal'),
    'meta_shift_b': () => togglePane('browser'),
    'meta_shift_e': () => togglePane('files'),
    'meta_;': () => togglePane('sidechat'),
    'meta_b': () => {
      collapsed.value = !collapsed.value
    },
    'meta_,': () => navigateTo('/settings'),
    'meta_n': () => {
      navigateTo({ path: '/', query: { ...route.query, new: '1' } })
    },
    'ctrl_o': () => {
      verbose.value = !verbose.value
    },
    'shift_tab': {
      usingInput: true,
      handler: () => {
        const s = store.current
        if (!s) return
        const i = MODE_CYCLE.indexOf(s.meta.permissionMode)
        store.setPermissionMode(s.meta.id, MODE_CYCLE[(i + 1) % MODE_CYCLE.length]!)
      }
    },
    'escape': {
      usingInput: true,
      handler: () => {
        const s = store.current
        if (s && s.meta.status === 'working') store.interrupt(s.meta.id)
      }
    }
  })
}
