// Settings open as a modal over whatever page is shown; `?settings=<section>` in the URL keeps it open across
// reloads, links straight to a section, and Back closes it.
export function useSettingsModal() {
  const route = useRoute()
  const router = useRouter()

  const isOpen = computed(() => typeof route.query.settings === 'string')
  const section = computed(() => String(route.query.settings || 'appearance'))

  function open(s = 'appearance') {
    if (isOpen.value) router.replace({ query: { ...route.query, settings: s } })
    else router.push({ query: { ...route.query, settings: s } })
  }
  function setSection(s: string) {
    router.replace({ query: { ...route.query, settings: s } })
  }
  function close() {
    const { settings: _, ...query } = route.query
    router.replace({ query })
  }
  return { isOpen, section, open, setSection, close }
}
