// The slash menu's rows, asked from the client each time the menu is about to open: files on disk change between uses.
// `useSlashActions` (mounted once, by the session page) does what a command left to do: open a picker, a session, a page.
import { SlashCommandResult_Action } from '~/gen/harness/agent/v1/slash_commands_pb'
import { slashMenuOptions, takeSlashAction, type SlashTarget } from '~/lib/session/slashCommands'
import type { MenuOption } from '~/utils/composer'

export function useSlashMenu(target: () => SlashTarget | null, text: () => string) {
  const store = useSessionStore()
  const options = ref<MenuOption[]>([])
  let asked = 0
  async function refresh() {
    const t = target()
    const n = ++asked
    if (!t || !store.transport) {
      options.value = []
      return
    }
    try {
      const res = await store.transport.listSlashCommands(t)
      if (n === asked) options.value = slashMenuOptions(res.commands, { hasSession: 'sessionId' in t })
    } catch {
      // The menu stays as it was: what's typed still goes to the client, which knows.
    }
  }
  watch(() => text().trimStart().startsWith('/'), (typing) => {
    if (typing) void refresh()
  }, { immediate: true })
  watch(() => JSON.stringify(target()), () => {
    options.value = []
    if (text().trimStart().startsWith('/')) void refresh()
  })
  return options
}

// Which picker of the session controls a command asked for.
export function usePickerRequest() {
  return useState<'' | 'model' | 'effort'>('composer-picker', () => '')
}

export function useSlashActions() {
  const store = useSessionStore()
  const done = useState<string[]>('slash-actions-done', () => [])
  const picker = usePickerRequest()

  watch(() => store.current?.slashAction?.id, async () => {
    const a = store.current?.slashAction
    if (!a || !takeSlashAction(a, done.value, Date.now())) return
    done.value = [...done.value, a.id].slice(-50)
    switch (a.action) {
      case SlashCommandResult_Action.PICK_MODEL:
        picker.value = 'model'
        break
      case SlashCommandResult_Action.PICK_EFFORT:
        picker.value = 'effort'
        break
      case SlashCommandResult_Action.OPEN_CUSTOMIZE:
        await navigateTo('/customize')
        break
      case SlashCommandResult_Action.OPEN_SCHEDULED:
        await navigateTo('/scheduled')
        break
      case SlashCommandResult_Action.OPEN_SESSION:
        await store.refreshList()
        store.open(a.newSessionId)
        break
    }
  }, { immediate: true })
}
