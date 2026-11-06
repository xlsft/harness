// The desktop app's own updates: looks at start and every few hours, stays quiet unless there is one, and only touches
// anything when the user says so. The update is downloaded and verified by the shell (src-tauri/src/update.rs); restarting
// stops whatever runs here, so it warns first and the user can keep working and restart later.
import { askConfirm, isDesktop, updateCheck, updateInstall, updateRestart } from '~/lib/desktop'

const CHECK_EVERY_MS = 4 * 60 * 60 * 1000

export type AppUpdatePhase = 'idle' | 'available' | 'installing' | 'installed'

export function useAppUpdate() {
  const phase = useState<AppUpdatePhase>('app-update-phase', () => 'idle')
  const version = useState('app-update-version', () => '')
  const notes = useState('app-update-notes', () => '')
  const current = useState('app-update-current', () => '')
  // Why this build can't update; empty when it can.
  const unavailable = useState('app-update-unavailable', () => '')
  const error = useState('app-update-error', () => '')
  const { runningWork } = useRunningWork()

  async function check() {
    // Once installed, the next start is what's left; a found update stays until it's acted on.
    if (!isDesktop() || phase.value === 'installing' || phase.value === 'installed') return
    try {
      const r = await updateCheck()
      current.value = r.current
      unavailable.value = r.state === 'unavailable' ? r.reason : ''
      if (r.state === 'available') {
        version.value = r.version
        notes.value = r.notes
        phase.value = 'available'
      } else {
        phase.value = 'idle'
      }
    } catch {
      // Offline, or GitHub is having a moment: the next check will do.
    }
  }

  async function restart() {
    const work = runningWork('Restarting stops them.')
    if (work && !(await askConfirm(work, 'Restart to update?', 'Restart'))) return
    await updateRestart()
  }

  /** Downloads and installs, then restarts unless work began meanwhile and the user would rather finish it. */
  async function install() {
    if (phase.value !== 'available') return
    const work = runningWork('They keep running while the update downloads; you decide when to restart.')
    if (work && !(await askConfirm(work, 'Download the update?', 'Download'))) return
    phase.value = 'installing'
    error.value = ''
    try {
      await updateInstall()
      phase.value = 'installed'
    } catch (e) {
      phase.value = 'available'
      error.value = e instanceof Error ? e.message : String(e)
      return
    }
    await restart()
  }

  return { phase, version, notes, current, unavailable, error, check, install, restart }
}

let started = false

/** Starts the periodic check, once per page load. */
export function startAppUpdateChecks() {
  if (started || !isDesktop()) return
  started = true
  const { check } = useAppUpdate()
  void check()
  setInterval(() => void check(), CHECK_EVERY_MS)
}
