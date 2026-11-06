import assert from 'node:assert/strict'
import { readFileSync, statSync, mkdtempSync, existsSync } from 'node:fs'
import { request } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, it, after, before } from 'node:test'
import { lockPath, removeLock, writeLock } from '../../src/bridge/lock.ts'
import { hostAllowed, startBridge, type BridgeServer } from '../../src/bridge/server.ts'
import type { BridgeHandlers, GetDiagnosticsResponse, PingResponse, ReadBufferResponse, ShowDiffRequest, ShowDiffResponse } from '../../src/protocol.ts'

const GOLDEN = join(import.meta.dirname, '..', '..', '..', 'client', 'ide', 'testdata', 'bridge')
const golden = <T>(name: string): T => JSON.parse(readFileSync(join(GOLDEN, name), 'utf8')) as T

interface Answer {
  status: number
  body: Record<string, unknown>
}

function post(port: number, method: string, body: unknown, headers: Record<string, string> = {}, signal?: AbortSignal): Promise<Answer> {
  return new Promise((resolve, reject) => {
    const req = request({ host: '127.0.0.1', port, method: 'POST', path: `/harness.ide.v1.EditorBridgeService/${method}`, headers: { 'Content-Type': 'application/json', ...headers }, signal }, (res) => {
      const chunks: Buffer[] = []
      res.on('data', c => chunks.push(c as Buffer))
      res.on('end', () => {
        const text = Buffer.concat(chunks).toString()
        resolve({ status: res.statusCode ?? 0, body: text ? JSON.parse(text) as Record<string, unknown> : {} })
      })
    })
    req.on('error', reject)
    req.end(JSON.stringify(body))
  })
}

describe('bridge', () => {
  let bridge: BridgeServer
  let lastDiff: ShowDiffRequest | undefined
  let opened: unknown
  let diffAborted: () => void = () => {}
  const aborted = new Promise<void>(r => (diffAborted = r))

  const handlers: BridgeHandlers = {
    ping: () => golden<PingResponse>('ping_response.json'),
    showDiff: (req, signal) => {
      lastDiff = req
      if (req.diffId === 'hold') {
        return new Promise<ShowDiffResponse>((resolve) => {
          signal.addEventListener('abort', () => {
            diffAborted()
            resolve({ outcome: 'DIFF_OUTCOME_DISMISSED' })
          })
        })
      }
      return Promise.resolve(golden<ShowDiffResponse>(req.diffId === 'reject' ? 'show_diff_rejected.json' : 'show_diff_accepted.json'))
    },
    getDiagnostics: () => golden<GetDiagnosticsResponse>('get_diagnostics_response.json'),
    openFile: (req) => {
      opened = req
    },
    readBuffer: () => golden<ReadBufferResponse>('read_buffer_response.json'),
    editApplied: () => {
      throw new Error('boom')
    }
  }

  before(async () => {
    bridge = await startBridge(handlers)
  })
  after(() => bridge.close())

  const auth = () => ({ Authorization: `Bearer ${bridge.token}` })

  it('needs the token', async () => {
    assert.equal((await post(bridge.port, 'Ping', {})).status, 401)
    assert.equal((await post(bridge.port, 'Ping', {}, { Authorization: 'Bearer nope' })).status, 401)
    assert.equal((await post(bridge.port, 'Ping', {}, auth())).status, 200)
  })

  it('refuses a browser, however it got the token', async () => {
    const r = await post(bridge.port, 'Ping', {}, { ...auth(), Origin: 'https://evil.example' })
    assert.equal(r.status, 403)
  })

  it('refuses a name rebound to loopback', async () => {
    const r = await post(bridge.port, 'Ping', {}, { ...auth(), Host: `evil.example:${bridge.port}` })
    assert.equal(r.status, 403)
    assert.equal(hostAllowed(`127.0.0.1:${bridge.port}`, bridge.port), true)
    assert.equal(hostAllowed(`localhost:${bridge.port}`, bridge.port), true)
    assert.equal(hostAllowed(`127.0.0.1:${bridge.port + 1}`, bridge.port), false)
    assert.equal(hostAllowed(undefined, bridge.port), false)
  })

  it('listens on loopback only', async () => {
    assert.ok(bridge.port > 0)
    const other = await post(bridge.port, 'Unknown', {}, auth())
    assert.equal(other.status, 501)
    assert.equal(other.body['code'], 'unimplemented')
  })

  it('speaks the wire format of the golden files', async () => {
    const req = golden<ShowDiffRequest>('show_diff_request.json')
    const accepted = await post(bridge.port, 'ShowDiff', req, auth())
    assert.deepEqual(accepted.body, golden('show_diff_accepted.json'))
    assert.deepEqual(lastDiff, req)
    const rejected = await post(bridge.port, 'ShowDiff', { ...req, diffId: 'reject' }, auth())
    assert.deepEqual(rejected.body, golden('show_diff_rejected.json'))
    assert.deepEqual((await post(bridge.port, 'GetDiagnostics', {}, auth())).body, golden('get_diagnostics_response.json'))
    assert.deepEqual((await post(bridge.port, 'ReadBuffer', {}, auth())).body, golden('read_buffer_response.json'))
    assert.deepEqual((await post(bridge.port, 'Ping', {}, auth())).body, golden('ping_response.json'))
    const open = golden('open_file_request.json')
    assert.equal((await post(bridge.port, 'OpenFile', open, auth())).status, 200)
    assert.deepEqual(opened, open)
  })

  it('reports a failing handler as an error', async () => {
    const r = await post(bridge.port, 'EditApplied', {}, auth())
    assert.equal(r.status, 500)
    assert.equal(r.body['code'], 'internal')
  })

  it('tells a held diff when the client hangs up', async () => {
    const stop = new AbortController()
    const call = post(bridge.port, 'ShowDiff', { diffId: 'hold' }, auth(), stop.signal)
    while (lastDiff?.diffId !== 'hold') await new Promise(r => setImmediate(r))
    stop.abort()
    await assert.rejects(call)
    await aborted
  })
})

describe('lock file', () => {
  it('is private, and goes away', () => {
    const home = mkdtempSync(join(tmpdir(), 'harness-vscode-lock-'))
    const path = writeLock(home, { port: 4242, token: 't', pid: 7, name: 'Editor', folders: ['/repo'] })
    assert.equal(path, lockPath(home, 7, 4242))
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')), { port: 4242, token: 't', pid: 7, name: 'Editor', folders: ['/repo'] })
    if (process.platform !== 'win32') {
      assert.equal(statSync(path).mode & 0o777, 0o600)
      assert.equal(statSync(join(home, 'ide')).mode & 0o777, 0o700)
    }
    removeLock(path)
    assert.equal(existsSync(path), false)
    removeLock(path)
  })
})
