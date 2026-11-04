// What new sessions start with (Settings > General, the Models page's per-model effort, the new-session
// screen's remembered chips), kept in this browser under one key and shared by every caller.
import type { EffortName, PermissionModeName } from '~/lib/session/types'

export interface SessionDefaults {
  cwd?: string
  worktree?: boolean
  model?: string
  // The model's engine when it is an ACP agent's ("acp:<agent id>"), and that agent's mode to start in.
  engine?: string
  acpMode?: string
  acpEffort?: string
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
    // A default chosen in Settings is a server's model: it replaces a remembered agent's.
    prefs.value = { ...prefs.value, ...(key === 'model' && { engine: '' }), [key]: value }
    try {
      localStorage.setItem(KEY, JSON.stringify(prefs.value))
    } catch {
      // Not remembered.
    }
  }

  // A model's default effort, in the new-session screen's order: its own, else the recommended one, else the general one.
  function effortOf(m: { id: string, efforts?: EffortName[], defaultEffort?: EffortName }) {
    return startEffort({ own: prefs.value.effortByModel?.[m.id], recommended: m.defaultEffort, fallback: prefs.value.effort, levels: modelEfforts(m) })
  }
  function setEffort(id: string, e: EffortName) {
    save('effortByModel', { ...prefs.value.effortByModel, [id]: e })
  }

  return { prefs, save, effortOf, setEffort }
}
