#!/usr/bin/env node
// A fake harness-client that speaks the headless protocol from a script, for the SDK's tests.
import { appendFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

const argv = process.argv.slice(2)
if (process.env.FAKE_ARGS_FILE) appendFileSync(process.env.FAKE_ARGS_FILE, JSON.stringify(argv) + '\n')
if (process.env.FAKE_FAIL) {
  process.stderr.write('Error: unknown flag\n')
  process.exit(2)
}

const say = v => process.stdout.write(JSON.stringify({ session_id: 's1', uuid: 'u', ...v }) + '\n')
say({ type: 'system', subtype: 'init', model: 'fake' })
process.stdout.write('not json, skipped\n')

let holding = false
let turns = 0
const finish = (text, extra = {}) => {
  turns++
  say({ type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text }] } })
  say({ type: 'result', subtype: 'success', is_error: false, result: text, num_turns: turns, duration_ms: 1, duration_api_ms: 1, total_cost_usd: 0, usage: {}, permission_denials: [], ...extra })
}

const lines = createInterface({ input: process.stdin })
lines.on('line', (line) => {
  const m = JSON.parse(line)
  if (m.type === 'control' && m.subtype === 'interrupt') {
    if (holding) {
      holding = false
      say({ type: 'result', subtype: 'error_during_execution', is_error: true, interrupted: true, errors: [], num_turns: turns, duration_ms: 1, duration_api_ms: 1, total_cost_usd: 0, usage: {}, permission_denials: [] })
    }
    return
  }
  const text = typeof m.message.content === 'string' ? m.message.content : m.message.content.map(b => b.text ?? '').join('')
  say({ type: 'user', message: { role: 'user', content: [{ type: 'text', text }] }, ...(m.uuid && { client_uuids: [m.uuid] }) })
  if (text === 'hold') {
    holding = true
    return
  }
  if (text === 'fail') {
    say({ type: 'result', subtype: 'error_max_budget_usd', is_error: true, errors: ['Reached the budget'], num_turns: 0, duration_ms: 1, duration_api_ms: 1, total_cost_usd: 1, usage: {}, permission_denials: [] })
    process.exitCode = 1
    return
  }
  if (text === 'schema') {
    finish('{"n":3}', { structured_output: { n: 3 } })
    return
  }
  finish(`you said ${text}`)
})
