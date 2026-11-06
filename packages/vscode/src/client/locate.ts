// Where a client is: already listening, or a binary to start. No editor API here, so it runs under plain Node in tests.
import { accessSync, constants, readFileSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { delimiter, join } from 'node:path'

export interface Endpoint {
  url: string
  token: string
}

export function harnessHome(env: NodeJS.ProcessEnv = process.env): string {
  return env['HARNESS_HOME'] || join(homedir(), '.harness')
}

function executable(path: string): boolean {
  try {
    if (!statSync(path).isFile()) return false
    accessSync(path, constants.X_OK)
    return true
  } catch {
    return false
  }
}

/** The binary to start: the setting wins, then PATH, then the install folder. Undefined when there is none. */
export function findBinary(setting: string, env: NodeJS.ProcessEnv = process.env, platform: string = process.platform): string | undefined {
  if (setting) return executable(setting) ? setting : undefined
  const names = platform === 'win32' ? ['harness-client.exe', 'harness-client.cmd'] : ['harness-client']
  const dirs = [...(env['PATH'] ?? '').split(delimiter).filter(Boolean), join(harnessHome(env), 'bin')]
  for (const dir of dirs) {
    for (const name of names) {
      const path = join(dir, name)
      if (executable(path)) return path
    }
  }
  return undefined
}

/** Only a loopback address: this token must not travel. */
export function loopbackUrl(raw: string): string | undefined {
  try {
    const u = new URL(raw.trim())
    if (u.protocol !== 'http:') return undefined
    if (u.hostname !== '127.0.0.1' && u.hostname !== 'localhost' && u.hostname !== '[::1]') return undefined
    return u.origin
  } catch {
    return undefined
  }
}

/** The client a person started by hand, as it announced itself. */
export function readAnnounced(home: string): Endpoint | undefined {
  try {
    const url = loopbackUrl(readFileSync(join(home, 'agent-endpoint'), 'utf8'))
    const token = readFileSync(join(home, 'agent-token'), 'utf8').trim()
    return url && token ? { url, token } : undefined
  } catch {
    return undefined
  }
}

/** The address in the line a client prints once it listens. */
export function listeningUrl(line: string): string | undefined {
  const m = /serving AgentService on (http:\/\/\S+)/.exec(line)
  return m ? loopbackUrl(m[1]!) : undefined
}

/** Whether a client that would not start says it needs a sign-in. */
export function needsSignIn(output: string): boolean {
  return /not signed in|harness-client login|no stored credentials|log ?in first/i.test(output)
}
