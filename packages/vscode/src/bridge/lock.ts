// The lock file through which a client finds this window: $HARNESS_HOME/ide/<pid>-<port>.json. It holds the bridge's
// token, so it is mode 0600 in a directory of mode 0700, and the client ignores one that others can read.
import { chmodSync, mkdirSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

export interface LockFields {
  port: number
  token: string
  pid: number
  name: string
  folders: string[]
}

export function lockPath(home: string, pid: number, port: number): string {
  return join(home, 'ide', `${pid}-${port}.json`)
}

/** Writes the lock atomically, so that a client never reads half of one. */
export function writeLock(home: string, fields: LockFields): string {
  const dir = join(home, 'ide')
  mkdirSync(dir, { recursive: true, mode: 0o700 })
  chmodSync(dir, 0o700)
  const path = lockPath(home, fields.pid, fields.port)
  const tmp = `${path}.tmp`
  writeFileSync(tmp, JSON.stringify(fields), { mode: 0o600 })
  chmodSync(tmp, 0o600)
  renameSync(tmp, path)
  return path
}

export function removeLock(path: string) {
  rmSync(path, { force: true })
}
