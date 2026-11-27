// The editor bridge: harness.ide.v1.EditorBridgeService over Connect unary JSON, on 127.0.0.1 with a bearer token. No
// editor API here, so it runs under plain Node in tests. The rules are in docs/design/ide.md.
import { createHash, randomBytes, timingSafeEqual } from 'node:crypto'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import { BridgeError, type BridgeHandlers } from '../protocol.ts'

const PREFIX = '/harness.ide.v1.EditorBridgeService/'
// A proposed file is capped at 4 MiB on each side by the client, in JSON.
const MAX_BODY = 32 << 20

// The HTTP status of each Connect code used here.
const STATUS: Record<string, number> = {
  invalid_argument: 400,
  unauthenticated: 401,
  permission_denied: 403,
  not_found: 404,
  resource_exhausted: 429,
  canceled: 499,
  unimplemented: 501,
  internal: 500,
  unavailable: 503
}

export interface BridgeServer {
  port: number
  token: string
  close(): Promise<void>
}

export function newToken(): string {
  return randomBytes(32).toString('hex')
}

function sha(s: string): Buffer {
  return createHash('sha256').update(s).digest()
}

function reply(res: ServerResponse, status: number, body: unknown) {
  if (res.writableEnded || res.destroyed) return
  res.writeHead(status, { 'Content-Type': 'application/json' })
  res.end(JSON.stringify(body))
}

function fail(res: ServerResponse, code: string, message: string) {
  reply(res, STATUS[code] ?? 500, { code, message })
}

function readBody(req: IncomingMessage): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    let size = 0
    req.on('data', (chunk: Buffer) => {
      size += chunk.length
      if (size > MAX_BODY) {
        reject(new BridgeError('resource_exhausted', 'the request is too large'))
        req.destroy()
        return
      }
      chunks.push(chunk)
    })
    req.on('end', () => {
      try {
        const text = Buffer.concat(chunks).toString('utf8')
        resolve(text ? JSON.parse(text) : {})
      } catch {
        reject(new BridgeError('invalid_argument', 'the body is not JSON'))
      }
    })
    req.on('error', reject)
  })
}

/** Whether a Host header names this server on loopback: anything else is a page that rebound its own name to us. */
export function hostAllowed(host: string | undefined, port: number): boolean {
  return host === `127.0.0.1:${port}` || host === `localhost:${port}`
}

async function dispatch(method: string, body: unknown, handlers: BridgeHandlers, signal: AbortSignal): Promise<unknown> {
  // Each request type is a bag of optional fields, read by name by its handler.
  const req = (body ?? {}) as never
  switch (method) {
    case 'Ping': return handlers.ping()
    case 'ShowDiff': return handlers.showDiff(req, signal)
    case 'GetDiagnostics': return handlers.getDiagnostics(req)
    case 'OpenFile':
      await handlers.openFile(req)
      return {}
    case 'ReadBuffer': return handlers.readBuffer(req)
    case 'EditApplied':
      await handlers.editApplied(req)
      return {}
    default:
      throw new BridgeError('unimplemented', `no method ${method}`)
  }
}

/** Starts the bridge on a free loopback port. */
export function startBridge(handlers: BridgeHandlers, token: string = newToken()): Promise<BridgeServer> {
  const want = sha(token)
  let port = 0
  const handle = async (req: IncomingMessage, res: ServerResponse) => {
    // A browser always says where a request comes from, and nothing that may call this is a browser.
    if (req.headers.origin !== undefined || !hostAllowed(req.headers.host, port)) {
      fail(res, 'permission_denied', 'not from a local client')
      return
    }
    const auth = req.headers.authorization ?? ''
    const given = sha(auth.startsWith('Bearer ') ? auth.slice(7) : '')
    if (!timingSafeEqual(given, want)) {
      fail(res, 'unauthenticated', 'missing or invalid bearer token')
      return
    }
    const path = (req.url ?? '').split('?')[0] ?? ''
    if (req.method !== 'POST' || !path.startsWith(PREFIX)) {
      fail(res, 'unimplemented', 'no such method')
      return
    }
    if (!(req.headers['content-type'] ?? '').startsWith('application/json')) {
      fail(res, 'invalid_argument', 'this bridge speaks JSON')
      return
    }
    const abort = new AbortController()
    res.on('close', () => {
      if (!res.writableFinished) abort.abort()
    })
    try {
      const body = await readBody(req)
      const out = await dispatch(path.slice(PREFIX.length), body, handlers, abort.signal)
      reply(res, 200, out ?? {})
    } catch (err) {
      if (err instanceof BridgeError) fail(res, err.code, err.message)
      else fail(res, 'internal', err instanceof Error ? err.message : String(err))
    }
  }
  const server: Server = createServer((req, res) => {
    void handle(req, res)
  })
  return new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      port = (server.address() as AddressInfo).port
      resolve({
        port,
        token,
        close: () => new Promise<void>((done) => {
          server.close(() => done())
          server.closeAllConnections()
        })
      })
    })
  })
}
