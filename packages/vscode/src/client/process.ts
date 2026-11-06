// Starts `harness-client serve --listen` for this window. No editor API here, so it runs under plain Node in tests.
import { spawn, type ChildProcess } from 'node:child_process'
import { newToken } from '../bridge/server.ts'
import { listeningUrl, needsSignIn, type Endpoint } from './locate.ts'

// A webview's origin has a random host per panel; no web page can have this scheme (packages/client/origins).
export const WEBVIEW_ORIGINS = 'vscode-webview://*'

const START_TIMEOUT_MS = 30000
const KEEP_OUTPUT = 8000

export class ClientStartError extends Error {
  readonly signIn: boolean
  constructor(message: string, signIn: boolean) {
    super(message)
    this.signIn = signIn
  }
}

export interface StartedClient {
  endpoint: Endpoint
  child: ChildProcess
  stop(): void
}

export interface StartOptions {
  binary: string
  cwd?: string
  /** The lock file of this window, so that the client's sessions reach it whatever folder they are in. */
  lockFile?: string
  env?: NodeJS.ProcessEnv
  onOutput?: (line: string) => void
  onExit?: (code: number | null) => void
}

/** Starts a client on a free port. The token goes through the environment: a command line can be read by anyone. */
export function startClient(o: StartOptions): Promise<StartedClient> {
  const token = newToken()
  const child = spawn(o.binary, ['serve', '--listen', '127.0.0.1:0'], {
    cwd: o.cwd,
    stdio: ['ignore', 'pipe', 'pipe'],
    env: {
      ...(o.env ?? process.env),
      HARNESS_AGENT_TOKEN: token,
      HARNESS_AGENT_CORS_ORIGINS: WEBVIEW_ORIGINS,
      // Exit with this window, however it ends.
      HARNESS_EXIT_WITH_PARENT: '1',
      ...(o.lockFile ? { HARNESS_IDE_LOCK: o.lockFile } : {})
    }
  })
  return new Promise((resolve, reject) => {
    let output = ''
    let settled = false
    const timer = setTimeout(() => {
      if (settled) return
      settled = true
      child.kill()
      reject(new ClientStartError(`harness-client did not start listening in ${START_TIMEOUT_MS / 1000}s`, false))
    }, START_TIMEOUT_MS)
    const onData = (chunk: Buffer) => {
      const text = chunk.toString('utf8')
      output = (output + text).slice(-KEEP_OUTPUT)
      for (const line of text.split('\n')) {
        if (line.trim()) o.onOutput?.(line)
        const url = settled ? undefined : listeningUrl(line)
        if (url) {
          settled = true
          clearTimeout(timer)
          resolve({ endpoint: { url, token }, child, stop: () => child.kill() })
        }
      }
    }
    child.stdout?.on('data', onData)
    child.stderr?.on('data', onData)
    child.on('error', (err) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      reject(new ClientStartError(`could not run ${o.binary}: ${err.message}`, false))
    })
    child.on('exit', (code) => {
      if (!settled) {
        settled = true
        clearTimeout(timer)
        reject(new ClientStartError(output.trim() || `harness-client exited with code ${code}`, needsSignIn(output)))
        return
      }
      o.onExit?.(code)
    })
  })
}
