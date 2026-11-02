// The Background tasks pane reads real session data; only the process id is missing from the
// agent contract (TaskVM has no pid). This gives a stable stand-in per task id, matching the
// design's "pid 48213" for the core dev server.

const KNOWN_PIDS: Record<string, number> = {
  'core-dev': 48213
}

export function taskPid(id: string): number {
  if (KNOWN_PIDS[id]) return KNOWN_PIDS[id]
  let h = 0
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  return 40000 + (h % 20000)
}
