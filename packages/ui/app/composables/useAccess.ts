// What the signed-in user may administer, from the server (DirectoryService.GetMyAccess). It decides which settings screens
// show and whether they offer changes; the server enforces it all the same. Shared, so screens ask once.
const capabilities = ref<string[]>([])
const loaded = ref(false)
let loading: Promise<void> | null = null

export function useAccess() {
  const store = useSessionStore()

  function load(force = false): Promise<void> {
    if (loaded.value && !force) return Promise.resolve()
    if (loading) return loading
    const transport = store.transport
    if (!transport) return Promise.resolve()
    loading = transport.enterprise().access()
      .then((a) => {
        capabilities.value = a.capabilities
      })
      .catch(async () => {
        // A server from before roles: admins are all there is.
        const admin = await transport.users().isAdmin().catch(() => store.account.role === 'admin')
        capabilities.value = admin ? ['users.read', 'users.write', 'sign_in.read', 'sign_in.write'] : []
      })
      .finally(() => {
        loaded.value = true
        loading = null
      })
    return loading
  }

  const can = (cap: string) => capabilities.value.includes(cap)
  return { load, can, capabilities, loaded }
}
