import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import type { AddressInfo } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { findBinary, harnessHome, listeningUrl, loopbackUrl, needsSignIn, readAnnounced } from '../../src/client/locate.ts'
import { ClientStartError, startClient, WEBVIEW_ORIGINS } from '../../src/client/process.ts'
import { answers, call, RpcError } from '../../src/client/rpc.ts'

const temp = () => mkdtempSync(join(tmpdir(), 'harness-vscode-client-'))

function script(dir: string, name: string, body: string): string {
  const path = join(dir, name)
  writeFileSync(path, `#!/bin/sh\n${body}\n`)
  chmodSync(path, 0o755)
  return path
}

describe('finding a binary', { skip: process.platform === 'win32' }, () => {
  it('takes the setting first, and only if it runs', () => {
    const dir = temp()
    const bin = script(dir, 'custom', 'exit 0')
    assert.equal(findBinary(bin, { PATH: '' }), bin)
    assert.equal(findBinary(join(dir, 'missing'), { PATH: dir }), undefined)
    const plain = join(dir, 'plain')
    writeFileSync(plain, 'not executable')
    assert.equal(findBinary(plain, { PATH: '' }), undefined)
  })
  it('then looks on PATH, then in the install folder', () => {
    const dir = temp()
    const home = temp()
    mkdirSync(join(home, 'bin'))
    const installed = script(join(home, 'bin'), 'harness-client', 'exit 0')
    assert.equal(findBinary('', { PATH: dir, HARNESS_HOME: home }), installed)
    const onPath = script(dir, 'harness-client', 'exit 0')
    assert.equal(findBinary('', { PATH: dir, HARNESS_HOME: home }), onPath)
    assert.equal(findBinary('', { PATH: '', HARNESS_HOME: temp() }), undefined)
  })
})

describe('addresses', () => {
  it('only takes loopback', () => {
    assert.equal(loopbackUrl('http://127.0.0.1:7801/'), 'http://127.0.0.1:7801')
    assert.equal(loopbackUrl(' http://localhost:7801\n'), 'http://localhost:7801')
    assert.equal(loopbackUrl('https://127.0.0.1:7801'), undefined)
    assert.equal(loopbackUrl('http://192.168.1.2:7801'), undefined)
    assert.equal(loopbackUrl('http://127.0.0.1.evil.example:7801'), undefined)
    assert.equal(loopbackUrl('nonsense'), undefined)
  })
  it('reads the line a client prints', () => {
    assert.equal(listeningUrl('harness-client: serving AgentService on http://127.0.0.1:53111 (Connect over HTTP/1.1 and h2c)'), 'http://127.0.0.1:53111')
    assert.equal(listeningUrl('harness-client: serving AgentService on unix:///tmp/a.sock'), undefined)
  })
  it('finds an announced client, with both files or not at all', () => {
    const home = temp()
    assert.equal(readAnnounced(home), undefined)
    writeFileSync(join(home, 'agent-endpoint'), 'http://127.0.0.1:7801\n')
    assert.equal(readAnnounced(home), undefined)
    writeFileSync(join(home, 'agent-token'), 'secret\n')
    assert.deepEqual(readAnnounced(home), { url: 'http://127.0.0.1:7801', token: 'secret' })
    writeFileSync(join(home, 'agent-endpoint'), 'http://192.168.1.2:7801\n')
    assert.equal(readAnnounced(home), undefined)
  })
  it('has a default home', () => {
    assert.equal(harnessHome({ HARNESS_HOME: '/x' }), '/x')
    assert.ok(harnessHome({}).endsWith('.harness'))
  })
  it('recognises a sign-in problem', () => {
    assert.equal(needsSignIn('Error: not logged in; run `harness login <url>`'), true)
    assert.equal(needsSignIn('listen tcp: address already in use'), false)
  })
})

describe('starting a client', { skip: process.platform === 'win32' }, () => {
  // A fake: reports what it was given and "listens".
  const fake = (dir: string, body: string) => script(dir, 'harness-client', body)

  it('passes the token and origins through the environment', async () => {
    const dir = temp()
    const out = join(dir, 'seen')
    const bin = fake(dir, `echo "$@" > ${out}.args; env > ${out}.env; echo 'harness-client: serving AgentService on http://127.0.0.1:49999 (Connect)' >&2; exec sleep 30`)
    const c = await startClient({ binary: bin, lockFile: '/x/lock.json' })
    try {
      assert.equal(c.endpoint.url, 'http://127.0.0.1:49999')
      assert.ok(c.endpoint.token.length >= 32)
      const { readFileSync } = await import('node:fs')
      const args = readFileSync(`${out}.args`, 'utf8')
      assert.equal(args.trim(), 'serve --listen 127.0.0.1:0')
      assert.ok(!args.includes(c.endpoint.token), 'the token must not be on the command line')
      const env = readFileSync(`${out}.env`, 'utf8')
      assert.ok(env.includes(`HARNESS_AGENT_TOKEN=${c.endpoint.token}`))
      assert.ok(env.includes(`HARNESS_AGENT_CORS_ORIGINS=${WEBVIEW_ORIGINS}`))
      assert.ok(env.includes('HARNESS_EXIT_WITH_PARENT=1'))
      assert.ok(env.includes('HARNESS_IDE_LOCK=/x/lock.json'))
    } finally {
      c.stop()
    }
  })

  it('says when one would not start, and whether a sign-in helps', async () => {
    const dir = temp()
    const signIn = fake(dir, `echo 'Error: not logged in; run \`harness login <url>\`' >&2; exit 1`)
    await assert.rejects(startClient({ binary: signIn }), (err: unknown) => err instanceof ClientStartError && err.signIn)
    const other = fake(dir, `echo 'something else' >&2; exit 3`)
    await assert.rejects(startClient({ binary: other }), (err: unknown) => err instanceof ClientStartError && !err.signIn && err.message.includes('something else'))
    await assert.rejects(startClient({ binary: join(dir, 'gone') }), ClientStartError)
  })

  it('reports an exit after it listened', async () => {
    const dir = temp()
    const bin = fake(dir, `echo 'harness-client: serving AgentService on http://127.0.0.1:49998 (x)' >&2; sleep 0.05; exit 4`)
    const code = await new Promise<number | null>((resolve, reject) => {
      startClient({ binary: bin, onExit: resolve }).catch(reject)
    })
    assert.equal(code, 4)
  })
})

describe('rpc', () => {
  it('speaks Connect JSON and carries the error code', async () => {
    const seen: { url?: string, auth?: string, proto?: string }[] = []
    const server = createServer((req, res) => {
      seen.push({ url: req.url, auth: req.headers.authorization, proto: req.headers['connect-protocol-version'] as string })
      if (req.url?.endsWith('/Bad')) {
        res.writeHead(404, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ code: 'not_found', message: 'no such thing' }))
        return
      }
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ ok: true }))
    })
    await new Promise<void>(r => server.listen(0, '127.0.0.1', r))
    const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
    try {
      const ep = { url, token: 'tok' }
      assert.deepEqual(await call(ep, 'svc', 'Good'), { ok: true })
      assert.deepEqual(seen[0], { url: '/svc/Good', auth: 'Bearer tok', proto: '1' })
      await assert.rejects(call(ep, 'svc', 'Bad'), (e: unknown) => e instanceof RpcError && e.code === 'not_found' && e.message === 'no such thing')
      assert.equal(await answers(ep), true)
    } finally {
      await new Promise(r => server.close(r))
    }
    assert.equal(await answers({ url, token: 'tok' }), false)
  })
})
