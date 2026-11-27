// The server's roles (RoleService.ListRoles, docs/design/roles.md): built-ins and the organization's custom roles, for
// every screen that shows or picks a role. Shared, so screens ask once; a server from before custom roles, or a caller
// without roles.read, gets the four built-ins.
import type { RoleView } from '~/lib/enterprise/types'
import { ROLE_HINT, ROLE_LABEL } from '~/lib/enterprise/types'

const BUILTINS: RoleView[] = (['user', 'admin', 'auditor', 'billing'] as const).map(id => ({
  id,
  name: ROLE_LABEL[id]!,
  description: ROLE_HINT[id]!,
  builtin: true,
  editable: false,
  permissions: [],
  memberCount: 0
}))

const roles = ref<RoleView[]>(BUILTINS)
const loaded = ref(false)
let loading: Promise<void> | null = null

export function useRoles() {
  const store = useSessionStore()

  function load(force = false): Promise<void> {
    if (loaded.value && !force) return Promise.resolve()
    if (loading) return loading
    const transport = store.transport
    if (!transport) return Promise.resolve()
    loading = transport.enterprise().listRoles()
      .then((list) => {
        roles.value = list.length ? list : BUILTINS
      })
      .catch(() => {
        roles.value = BUILTINS
      })
      .finally(() => {
        loaded.value = true
        loading = null
      })
    return loading
  }

  const label = (id: string) => roles.value.find(r => r.id === id)?.name || ROLE_LABEL[id] || id
  const hint = (id: string) => roles.value.find(r => r.id === id)?.description || ROLE_HINT[id] || ''
  const options = computed(() => roles.value.map(r => ({ value: r.id, label: r.name || r.id, hint: r.description || undefined })))

  return { roles, load, label, hint, options, loaded }
}
