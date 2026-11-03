// Appearance: the mode (system, light, dark) and the theme picked for each kind, kept in this browser. The
// active theme's tokens go on <html> as --color-* variables with a `light` / `dark` class; the resolved
// variables are cached for the head script (nuxt.config.ts) so the next load starts in the right theme.
import {
  APPEARANCE_KEY, CUSTOM_THEMES_KEY, DEFAULT_THEME, THEME_CACHE_KEY, THEMES, themeById, themeVars,
  type Appearance, type AppearanceMode, type Theme, type ThemeKind
} from '~/themes'

const DEFAULTS: Appearance = { mode: 'system', light: DEFAULT_THEME.light, dark: DEFAULT_THEME.dark }

function read(): Appearance {
  try {
    const v = JSON.parse(localStorage.getItem(APPEARANCE_KEY) ?? '{}') as Partial<Appearance>
    return {
      mode: v.mode === 'light' || v.mode === 'dark' ? v.mode : 'system',
      light: typeof v.light === 'string' ? v.light : DEFAULTS.light,
      dark: typeof v.dark === 'string' ? v.dark : DEFAULTS.dark
    }
  } catch {
    return { ...DEFAULTS }
  }
}

function readCustom(): Theme[] {
  try {
    const v = JSON.parse(localStorage.getItem(CUSTOM_THEMES_KEY) ?? '[]')
    return Array.isArray(v) ? v.filter(t => t && typeof t.id === 'string' && t.tokens && (t.kind === 'light' || t.kind === 'dark')) : []
  } catch {
    return []
  }
}

const appearance = ref<Appearance>({ ...DEFAULTS })
const custom = ref<Theme[]>([])
const systemDark = ref(true)
let started = false

const kind = computed<ThemeKind>(() => appearance.value.mode === 'system' ? (systemDark.value ? 'dark' : 'light') : appearance.value.mode)
const theme = computed<Theme>(() => themeById(appearance.value[kind.value], kind.value, custom.value))

function apply(t: Theme) {
  const root = document.documentElement
  const vars = themeVars(t)
  for (const [k, v] of Object.entries(vars)) root.style.setProperty(k, v)
  root.classList.toggle('dark', t.kind === 'dark')
  root.classList.toggle('light', t.kind === 'light')
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', t.tokens.ground)
  try {
    localStorage.setItem(THEME_CACHE_KEY, JSON.stringify({ kind: t.kind, vars }))
  } catch { /* private mode: the next load starts from the defaults */ }
}

// Called once from the appearance plugin: reads the choice, follows the system setting and applies changes.
export function startAppearance() {
  if (started || !import.meta.client) return
  started = true
  appearance.value = read()
  custom.value = readCustom()
  const media = window.matchMedia('(prefers-color-scheme: dark)')
  systemDark.value = media.matches
  media.addEventListener('change', e => (systemDark.value = e.matches))
  watch(theme, apply, { immediate: true })
}

export function useAppearance() {
  function set(patch: Partial<Appearance>) {
    appearance.value = { ...appearance.value, ...patch }
    try {
      localStorage.setItem(APPEARANCE_KEY, JSON.stringify(appearance.value))
    } catch { /* kept for this page only */ }
  }
  function saveCustom(next: Theme[]) {
    custom.value = next
    try {
      localStorage.setItem(CUSTOM_THEMES_KEY, JSON.stringify(next))
    } catch { /* kept for this page only */ }
  }
  // Adds (or replaces, by id) an imported theme and switches its kind to it.
  function importTheme(t: Theme) {
    saveCustom([...custom.value.filter(x => x.id !== t.id), t])
    set({ [t.kind]: t.id })
  }
  function removeTheme(t: Theme) {
    saveCustom(custom.value.filter(x => x.id !== t.id))
    if (appearance.value[t.kind] === t.id) set({ [t.kind]: DEFAULT_THEME[t.kind] })
  }
  return {
    themes: computed(() => [...THEMES, ...custom.value]),
    isCustom: (t: Theme) => custom.value.some(x => x.id === t.id),
    importTheme,
    removeTheme,
    appearance: readonly(appearance),
    kind,
    theme,
    setMode: (mode: AppearanceMode) => set({ mode }),
    setTheme: (k: ThemeKind, id: string) => set({ [k]: id })
  }
}
