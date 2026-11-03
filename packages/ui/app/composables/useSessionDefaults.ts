// What new sessions start with (Settings > General, the Models page's per-model effort, the new-session
// screen's remembered chips), kept in this browser under one key and shared by every caller.
import type { EffortName, PermissionModeName } from '~/lib/session/types'

export interface SessionDefaults {
  cwd?: string
  worktree?: boolean
  model?: string
  effort?: EffortName
  mode?: PermissionModeName
  // Default effort per model; levels differ between models.
  effortByModel?: Record<string, EffortName>
}

const KEY = 'harness-new-session'

export function useSessionDefaults() {
  // Re-read on every use: the new-session screen also writes this key when it starts a session.
  const prefs = useState<SessionDefaults>('session-defaults', () => ({}))
  if (import.meta.client) {
    try {
      prefs.value = JSON.parse(localStorage.getItem(KEY) ?? '{}') as SessionDefaults
    } catch {
      // Defaults.
    }
  }

  function save<K extends keyof SessionDefaults>(key: K, value: SessionDefaults[K]) {
    prefs.value = { ...prefs.value, [key]: value }
    try {
      localStorage.setItem(KEY, JSON.stringify(prefs.value))
    } catch {
      // Not remembered.
    }
  }

  // A model's default effort: its own, else the general one, fitted to the levels it offers.
  function effortOf(m: { id: string, efforts?: EffortName[] }) {
    return fitEffort(prefs.value.effortByModel?.[m.id] ?? prefs.value.effort ?? 'medium', modelEfforts(m))
  }
  function setEffort(id: string, e: EffortName) {
    save('effortByModel', { ...prefs.value.effortByModel, [id]: e })
  }

  return { prefs, save, effortOf, setEffort }
}
