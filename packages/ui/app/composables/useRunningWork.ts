// What still runs on this machine: sessions in a turn, background tasks, agents. Quitting and restarting stop all of it.
export function useRunningWork() {
  const store = useSessionStore()

  /** A sentence naming what runs, followed by `consequence`; '' when nothing does. */
  function runningWork(consequence: string) {
    const all = Object.values(store.sessions).filter(s => !s.meta.remote)
    const count = (n: number, one: string, many: string) => n ? `${n} ${n === 1 ? one : many}` : ''
    const parts = [
      count(all.filter(s => s.meta.status === 'working' || s.meta.status === 'needs_input').length, 'session is working', 'sessions are working'),
      count(all.reduce((n, s) => n + Object.values(s.tasks).filter(t => t.state === 'running').length, 0), 'background task is running', 'background tasks are running'),
      count(all.reduce((n, s) => n + Object.values(s.subagents).filter(a => a.state === 'running' || a.state === 'queued').length, 0), 'agent is running', 'agents are running')
    ].filter(Boolean)
    return parts.length ? `${parts.join(', ')}. ${consequence}` : ''
  }

  return { runningWork }
}
