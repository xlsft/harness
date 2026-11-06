import assert from 'node:assert/strict'
import { chmodSync, mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { buildArgs, run, startSession, type Message } from '../src/index.ts'

const fake = fileURLToPath(new URL('./fake-client.mjs', import.meta.url))
chmodSync(fake, 0o755)

test('run resolves with the result message', async () => {
  const r = await run('hello', { binary: fake })
  assert.equal(r.subtype, 'success')
  assert.equal(r.result, 'you said hello')
  assert.equal(r.session_id, 's1')
})

test('run reports a failed turn as a result, not a rejection', async () => {
  const r = await run('fail', { binary: fake })
  assert.equal(r.is_error, true)
  assert.equal(r.subtype, 'error_max_budget_usd')
  assert.deepEqual(r.errors, ['Reached the budget'])
})

test('run carries structured output', async () => {
  const r = await run('schema', { binary: fake, jsonSchema: { type: 'object' } })
  assert.deepEqual(r.structured_output, { n: 3 })
})

test('run rejects when the process cannot do anything', async () => {
  await assert.rejects(run('x', { binary: fake, env: { FAKE_FAIL: '1' } }), /exited with 2: Error: unknown flag/)
  await assert.rejects(run('x', { binary: '/nonexistent/harness-client' }), /ENOENT/)
})

test('a session takes more messages and keeps their order', async () => {
  const s = startSession({ binary: fake })
  s.send('one', { uuid: 'c1' })
  const seen: Message[] = []
  for await (const m of s) {
    seen.push(m)
    if (m.type === 'result') break
  }
  assert.deepEqual(seen.map(m => m.type), ['system', 'user', 'assistant', 'result'])
  const user = seen[1]!
  assert.ok(user.type === 'user')
  assert.deepEqual(user.client_uuids, ['c1'])

  s.send([{ type: 'text', text: 'two' }])
  const second = await s.result()
  assert.equal(second.result, 'you said two')
  assert.equal(second.num_turns, 2)

  s.end()
  assert.equal(await s.closed, 0)
  assert.equal(await s.next(), undefined)
  assert.throws(() => s.send('late'), /has ended/)
})

test('interrupt stops the turn in progress and the session goes on', async () => {
  const s = startSession({ binary: fake })
  s.send('hold')
  // Wait for the fake to take the message, so the interrupt finds a turn to stop.
  for (;;) {
    const m = await s.next()
    if (m?.type === 'user') break
  }
  s.interrupt()
  const stopped = await s.result()
  assert.equal(stopped.interrupted, true)
  s.send('after')
  assert.equal((await s.result()).result, 'you said after')
  s.end()
  await s.closed
})

test('options become the flags of the protocol', async () => {
  const file = join(mkdtempSync(join(tmpdir(), 'sdk-')), 'args')
  await run('hello', {
    binary: fake,
    env: { FAKE_ARGS_FILE: file },
    model: 'm',
    permissionMode: 'dontAsk',
    allowedTools: ['Read', 'Bash(go test *)'],
    maxBudgetUsd: 1.5,
    maxTurns: 3,
    systemPrompt: 'S',
    appendSystemPrompt: 'A',
    jsonSchema: { type: 'object' },
    includePartialMessages: true,
    resume: 'r1',
    noSessionPersistence: true,
    extraArgs: ['--verbose']
  })
  const args: string[] = JSON.parse(readFileSync(file, 'utf8').trim())
  assert.deepEqual(args, [
    '-p', '--output-format', 'stream-json', '--input-format', 'stream-json',
    '--model', 'm', '--permission-mode', 'dontAsk',
    '--allowed-tools', 'Read', '--allowed-tools', 'Bash(go test *)',
    '--max-turns', '3', '--max-budget-usd', '1.5',
    '--system-prompt', 'S', '--append-system-prompt', 'A',
    '--json-schema', '{"type":"object"}', '--include-partial-messages',
    '--resume', 'r1', '--no-session-persistence', '--verbose'
  ])
})

test('buildArgs leaves out what is not set', () => {
  assert.deepEqual(buildArgs({}), ['-p', '--output-format', 'stream-json', '--input-format', 'stream-json'])
})
