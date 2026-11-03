// Global keyboard shortcuts (PLAN.md §4), where the browser lets us have them:
// ⌘J terminal · ⌘⇧B browser · ⌘⇧E files · ⌘; side chat · ⌘B sidebar · ⌘N new session · ⌘, settings ·
// Esc interrupts the running turn (when no card or menu takes it) · ⇧Tab cycles the permission mode · Ctrl+O verbose transcript.

export function useShortcuts() {
  const store = useSessionStore()
  const route = useRoute()
  const { togglePane } = usePanes()
  const collapsed = useState('sidebar-collapsed', () => false)
  const verbose = useState('transcript-verbose', () => false)
  const settings = useSettingsModal()

  defineShortcuts({
    'meta_j': () => togglePane('terminal'),
    'meta_shift_b': () => togglePane('browser'),
    'meta_shift_e': () => togglePane('files'),
    'meta_;': () => togglePane('sidechat'),
    'meta_b': () => {
      collapsed.value = !collapsed.value
    },
    'meta_,': () => settings.open(),
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
        // The client's cycle, without the modes the server's policy disables.
        store.setPermissionMode(s.meta.id, nextMode(s.meta.permissionMode, store.permissionModes, store.disabledModes))
      }
    },
    'escape': {
      usingInput: true,
      handler: () => {
        // Esc closes the settings modal instead (SettingsModal).
        if (settings.isOpen.value) return
        const s = store.current
        if (s && s.meta.status === 'working') store.interrupt(s.meta.id)
      }
    }
  })
}
