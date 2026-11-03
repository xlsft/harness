// Scripted, realistic harness.agent.v1 histories for the mock transport. The sample data follows the design
// artboards (docs/design/ui), so every screen of the design is reachable with the mock.

import { PermissionMode, PullRequest_State, SessionStatus, type SessionEvent, type SessionInfo } from '~/gen/harness/agent/v1/agent_pb'
import { EventBuilder, sessionInfo } from './events'
import type { ModelInfo } from './types'

export const MAIN_SESSION = 's-gateway'

export const MODELS: ModelInfo[] = [
  { id: 'glm-4.6', provider: 'OpenRouter', contextWindow: 200_000, price: '$0.60/M' },
  { id: 'qwen3-coder', provider: 'OpenRouter', contextWindow: 256_000, price: '$0.40/M' },
  { id: 'claude-sonnet-5-5', provider: 'Anthropic', contextWindow: 1_000_000, price: '$3/M' },
  { id: 'qwen3-coder:30b', provider: 'Ollama · local', contextWindow: 32_000, price: '', offline: true }
]

const HARNESS = 'github.com/xlsft/harness'
const PROXY = 'github.com/xlsft/proxy.xlsft.ru'

// `remote`: a session of another machine of the user (listed from the harness server).
function info(id: string, title: string, repo: string, o: Partial<{ status: SessionStatus, additions: number, deletions: number, files: number, pr: number, branch: string, minutesAgo: number, remote: { device: string, online?: boolean, controllable?: boolean } }> = {}): SessionInfo {
  const name = repo.split('/').pop()!
  return sessionInfo({
    id,
    title,
    cwd: `~/Projects/xlsft/${name}`,
    gitBranch: o.branch ?? 'main',
    model: 'glm-4.6',
    permissionMode: PermissionMode.AUTO,
    status: o.status ?? SessionStatus.IDLE,
    updatedAt: new Date(Date.now() - (o.minutesAgo ?? 60) * 60_000).toISOString(),
    repo,
    deviceId: o.remote ? `dev_${o.remote.device}` : 'dev_macbook',
    deviceName: o.remote?.device ?? 'macbook',
    remote: !!o.remote,
    deviceOnline: o.remote?.online,
    remoteControllable: !!o.remote?.controllable,
    // Remote sessions report their machine's switch (read-only here).
    relayEnabled: true,
    relayAvailable: !o.remote,
    diffStats: { filesChanged: o.files ?? 0, additions: o.additions ?? 0, deletions: o.deletions ?? 0 },
    pullRequest: o.pr ? { number: o.pr, url: `https://${repo}/pull/${o.pr}`, state: PullRequest_State.OPEN } : undefined
  })
}

export function sessionList(): SessionInfo[] {
  return [
    info(MAIN_SESSION, 'M0: LLM gateway in core', HARNESS, { status: SessionStatus.WORKING, additions: 2926, deletions: 0, files: 14, minutesAgo: 0 }),
    info('s-client-loop', 'Agent loop in client (Go)', HARNESS, { additions: 412, deletions: 38, files: 9, branch: 'feat/client-loop', minutesAgo: 12 }),
    info('s-relay-backoff', 'Relay reconnect backoff', HARNESS, { minutesAgo: 45, remote: { device: 'workstation', online: true } }),
    info('s-proto', 'ConnectRPC proto contracts', HARNESS, { pr: 3, minutesAgo: 90 }),
    info('s-import', 'Import Claude Code sessions', HARNESS, { minutesAgo: 300 }),
    info('s-tui', 'TUI on charm.land', HARNESS, { minutesAgo: 600 }),
    info('s-s6', 'Single-container s6 image', HARNESS, { minutesAgo: 900 }),
    info('s-billing', 'Hourly node billing', PROXY, { minutesAgo: 1400 }),
    info('s-referral', 'Referral bonus regression', PROXY, { status: SessionStatus.ERROR, minutesAgo: 1500 }),
    info('s-dashboard', 'Dashboard / statistics', PROXY, { minutesAgo: 2000 })
  ]
}

// Synthetic patch of `adds` added and `dels` removed lines, for edits whose body is not shown.
function patch(adds: number, dels: number, start = 1) {
  const lines = [...Array.from({ length: dels }, (_, i) => `-  // old line ${i + 1}`), ...Array.from({ length: adds }, (_, i) => `+  // new line ${i + 1}`)]
  return [{ oldStart: start, oldLines: dels, newStart: start, newLines: adds, lines }]
}

const TRANSLATE_PATCH = [{
  oldStart: 38,
  oldLines: 9,
  newStart: 38,
  newLines: 14,
  header: 'export function* toAnthropicEvents(',
  lines: [
    '   for await (const chunk of upstream) {',
    '     const delta = chunk.choices[0]?.delta',
    '-    if (chunk.usage) yield usageEvent(chunk.usage)',
    '-    if (chunk.done) return yield stopEvent()',
    '+    if (chunk.usage) usage = chunk.usage',
    '+    if (chunk.done) {',
    '+      if (opts.holdStopUntilUsage && !usage) { pending = true; continue }',
    '+      yield messageDelta(usage)',
    '+      return yield stopEvent()',
    '+    }',
    '+    if (pending && usage) return yield* flush(usage)',
    '     if (delta?.content) yield textDelta(delta.content)',
    '   }'
  ]
}]

const VITEST_OUT = [
  '✓ gateway/translate.test.ts (14)',
  '✓ gateway/stream.test.ts (6)',
  '↓ gateway/retry.test.ts (3) [skipped]',
  '',
  ' Test Files  2 passed | 1 skipped (3)',
  '      Tests  20 passed | 3 skipped (23)'
].join('\n')

const GREP_OUT = [
  'core/server/routes/gateway.ts:8:  return toAnthropicEvents(upstream, { holdStopUntilUsage: true })',
  'core/server/gateway/translate.ts:12:  holdStopUntilUsage?: boolean',
  'core/server/gateway/translate.ts:42:      if (opts.holdStopUntilUsage && !usage) {'
].join('\n')

// Labels of the mock Bash calls (ToolTitle), by command.
const BASH_TITLES: Record<string, string> = {
  'pnpm --filter core dev': 'core dev server',
  'docker compose up postgres': 'Postgres container',
  'go test ./loop -run TestLoop -count=20': 'loop test stress run',
  'pnpm --filter ui dev --port 5173': 'UI dev server on port 5173',
  'pnpm vitest run gateway': 'gateway unit tests',
  'buf generate': 'proto code generation',
  'go test ./loop': 'loop package tests',
  'pnpm lint --fix': 'lint autofix',
  'git push origin feat/gateway-stream': 'push of the gateway branch',
  'docker build -t harness-core .': 'core Docker image build',
  'pnpm --filter ui build': 'UI production build',
  'go mod tidy': 'Go module cleanup',
  'pnpm install --frozen-lockfile': 'dependency install'
}

// History of the main session. `now` anchors the in-progress turn so elapsed timers read like the design.
export function mainHistory(now: number): SessionEvent[] {
  const b = new EventBuilder(MAIN_SESSION, now - 3 * 3600_000)
  const sessions = sessionList()
  b.sessionUpdated(sessions[0]!)

  // ── turn 1: background processes ──────────────────────────────────────────
  b.user('u1', 'Start the dev servers and keep the usual checks running in the background while we work.')
  b.wait(400).turnStarted('u1', 'glm-4.6')
  const bg: { id: string, cmd: string, ago: number, out?: string }[] = [
    { id: 'core-dev', cmd: 'pnpm --filter core dev', ago: 17 * 60 + 23, out: '➜ Local:   http://localhost:3000/\n✔ Nitro built in 412 ms\nℹ POST /gateway.v1.Gateway/Stream 200 — 3.4s\nℹ POST /gateway.v1.Gateway/Stream 200 — 2.9s' },
    { id: 'postgres', cmd: 'docker compose up postgres', ago: 13 * 60 + 1 },
    { id: 'go-loop', cmd: 'go test ./loop -run TestLoop -count=20', ago: 64 },
    { id: 'ui-dev', cmd: 'pnpm --filter ui dev --port 5173', ago: 6 }
  ]
  const done: { id: string, cmd: string, ms: number, exit: number }[] = [
    { id: 'f1', cmd: 'pnpm vitest run gateway', ms: 48_000, exit: 0 },
    { id: 'f2', cmd: 'buf generate', ms: 4000, exit: 0 },
    { id: 'f3', cmd: 'go test ./loop', ms: 400, exit: 1 },
    { id: 'f4', cmd: 'pnpm lint --fix', ms: 1200, exit: 0 },
    { id: 'f5', cmd: 'git push origin feat/gateway-stream', ms: 3000, exit: 0 },
    { id: 'f6', cmd: 'docker build -t harness-core .', ms: 72_000, exit: 0 },
    { id: 'f7', cmd: 'pnpm --filter ui build', ms: 22_000, exit: 2 },
    { id: 'f8', cmd: 'go mod tidy', ms: 2000, exit: 0 },
    { id: 'f9', cmd: 'pnpm install --frozen-lockfile', ms: 17_000, exit: 0 }
  ]
  b.wait(2000).assistant('m1', 'glm-4.6', [
    { text: 'Starting the checks first, then core, Postgres and the UI dev server in the background.' },
    ...done.map(d => ({ toolUse: { id: 'tb-' + d.id, name: 'Bash', input: { command: d.cmd, run_in_background: true } } }))
  ], { input: 9000, output: 400 })
  for (const d of done) {
    b.wait(300).toolStarted('tb-' + d.id, 'Bash', { command: d.cmd, run_in_background: true }, d.cmd)
    b.wait(50).toolTitle('tb-' + d.id, BASH_TITLES[d.cmd]!)
    b.wait(100).toolFinished('tb-' + d.id, { output: `Started in the background as task ${d.id}`, summary: 'background', durationMs: 90 })
    b.taskStarted(d.id, 'tb-' + d.id, d.cmd, d.cmd)
    b.wait(d.ms).taskFinished(d.id, d.exit, d.ms)
  }
  b.wait(1500).assistant('m1b', 'glm-4.6', [{ text: 'Checks are done: `go test ./loop` and the UI build fail, the rest pass. Starting the long-running processes now.' }], { input: 9400, output: 120 })
  b.wait(200).turnFinished('success', { durationMs: 180_000, costUsd: 0.31, usage: { input: 18_400, output: 520 } })
  b.at(now - 20 * 60_000)
  b.assistant('m1c', 'glm-4.6', bg.map(t => ({ toolUse: { id: 'tb-' + t.id, name: 'Bash', input: { command: t.cmd, run_in_background: true } } })), { output: 80 })
  for (const t of bg) {
    b.at(now - t.ago * 1000)
    b.toolStarted('tb-' + t.id, 'Bash', { command: t.cmd, run_in_background: true }, t.cmd)
    b.toolTitle('tb-' + t.id, BASH_TITLES[t.cmd]!)
    b.toolFinished('tb-' + t.id, { output: `Started in the background as task ${t.id}`, summary: 'background', durationMs: 60 })
    b.taskStarted(t.id, 'tb-' + t.id, t.cmd, t.cmd)
    if (t.out) b.taskOutput(t.id, t.out)
  }

  // ── turn 2: subagents for the M1 work ─────────────────────────────────────
  b.at(now - 18 * 60_000)
  b.user('u2', 'Fan out the M1 work: Postgres, the s6 image and the TUI skeleton, plus an eval run on all five models.')
  b.wait(300).turnStarted('u2', 'glm-4.6')
  const agents = [
    { id: 'ag-pg', d: 'Move core from SQLite to Postgres', type: 'general-purpose', model: 'qwen3-coder', ago: 17 * 60 + 23, calls: 18, steps: [
      { tool: 'Explore: find SQLite usages in core', target: '', meta: '41s', status: 'done' },
      { tool: 'Plan the 0003 migration', target: '', meta: '1m 12s', status: 'done' },
      { tool: 'Write migration 0003_postgres.sql', target: '', meta: '2m 40s', status: 'running' },
      { tool: 'Port store tests to testcontainers', target: '', status: 'waiting' },
      { tool: 'Update docker-compose and docs', target: '', status: 'waiting' }
    ], active: 3 },
    { id: 'ag-s6', d: 'Single-container s6 image', type: 'general-purpose', model: 'glm-4.6', ago: 13 * 60 + 1, calls: 4, steps: [], active: 1 },
    { id: 'ag-tui', d: 'CLI TUI skeleton on charm.land', type: 'general-purpose', model: 'claude-sonnet-5-5', ago: 13 * 60 + 1, calls: 3, steps: [], active: 1 },
    { id: 'ag-evals', d: 'Run evals on 11 tasks across five models', type: 'general-purpose', model: 'glm-4.6', ago: 64, calls: 0, steps: [], active: 0 },
    { id: 'ag-scratch', d: 'Fill scratchpad and additional dirs placeholders', type: 'general-purpose', model: 'qwen3-coder', ago: 6, calls: 0, steps: [], active: 0 }
  ]
  const finishedAgents = [
    { id: 'af1', d: 'Use random free ports in e2e and rerun', ms: 12_000, ok: true },
    { id: 'af2', d: 'Rerun e2e and show full failure output', ms: 5000, ok: true },
    { id: 'af3', d: 'Run e2e, commit and push bootstrap wiring', ms: 12_000, ok: true },
    { id: 'af4', d: 'Live check: settings, memory, sync upload on device', ms: 19_000, ok: false },
    { id: 'af5', d: 'Add import/sync commands, run full client tests', ms: 27_000, ok: true },
    { id: 'af6', d: 'Commit merge, push, clean merged worktrees', ms: 5000, ok: true },
    { id: 'af7', d: 'M1 client: loop, tools, permissions, rewind', ms: 52 * 60_000 + 59_000, ok: true, calls: 26 }
  ]
  b.wait(1500).assistant('m2', 'glm-4.6', [
    { text: 'Splitting it into parallel subagents; each one reports back here.' },
    ...finishedAgents.map(a => ({ toolUse: { id: a.id, name: 'Task', input: { description: a.d, subagent_type: 'general-purpose' } } }))
  ], { input: 12_000, output: 300 })
  for (const a of finishedAgents) {
    b.wait(200).toolStarted(a.id, 'Task', { description: a.d, subagent_type: 'general-purpose' }, a.d)
    if (a.calls) b.notice('info', 'subagent_progress', { tool_use_id: a.id, steps: [], tokens: 120_000, tool_calls: a.calls, model: 'qwen3-coder' })
    b.wait(a.ms).toolFinished(a.id, { output: a.ok ? 'Done.' : 'Failed: the device did not answer the sync upload.', isError: !a.ok, durationMs: a.ms, summary: a.ok ? 'done' : 'failed' })
  }
  b.at(now - 17 * 60_000 - 30_000)
  b.assistant('m2b', 'glm-4.6', agents.map(a => ({ toolUse: { id: a.id, name: 'Task', input: { description: a.d, subagent_type: a.type } } })), { output: 200 })
  for (const a of agents) {
    b.at(now - a.ago * 1000)
    b.toolStarted(a.id, 'Task', { description: a.d, subagent_type: a.type }, a.d)
    if (a.calls || a.steps.length) {
      b.notice('info', 'subagent_progress', { tool_use_id: a.id, steps: a.steps, tokens: a.calls * 4000, tool_calls: a.calls, model: a.model, active: a.active })
    } else {
      b.notice('info', 'subagent_progress', { tool_use_id: a.id, steps: [], tokens: 0, tool_calls: 0, model: a.model })
    }
  }
  b.at(now - 16 * 60_000)
  b.assistant('m2c', 'glm-4.6', [{ text: 'All five are running. I will keep going on the gateway in the meantime.' }], { output: 60 })
  b.turnFinished('success', { durationMs: 95_000, costUsd: 0.53, usage: { input: 26_000, output: 1900 } })

  // ── turn 3: the gateway (in progress) ─────────────────────────────────────
  b.at(now - 72_000)
  b.user('u3', 'Bring up the gateway in packages/core: Anthropic Messages streaming over an OpenAI-compatible provider, keys stay on the server.')
  b.turnStarted('u3', 'glm-4.6')
  // Streamed live (deltas are never replayed): the delta timestamps give "Thought for 12s".
  b.wait(500).delta('m3a', 0, 'thinking', 'Usage arrives after the last text chunk, but we emit stop right away. So the client closes the stream before it gets the token counts. ')
  b.delta('m3a', 0, 'thinking', 'We need to buffer stop, but only for providers that behave like this — otherwise everyone pays the latency.')
  b.wait(12_000).delta('m3a', 1, 'text', 'Starting with the contract: ')
  b.assistant('m3a', 'glm-4.6', [
    { thinking: 'Usage arrives after the last text chunk, but we emit stop right away. So the client closes the stream before it gets the token counts. We need to buffer stop, but only for providers that behave like this — otherwise everyone pays the latency.' },
    { text: 'Starting with the contract: `gateway.v1.Stream` is already defined in proto/, so the Nitro handler stays thin — it only maps provider events to `content_block_delta` and back.' },
    { toolUse: { id: 't-read', name: 'Read', input: { file_path: 'proto/gateway/v1/gateway.proto' } } },
    { toolUse: { id: 't-buf', name: 'Bash', input: { command: 'buf generate' } } },
    { toolUse: { id: 't-edit-route', name: 'Edit', input: { file_path: 'packages/core/server/routes/gateway.ts' } } },
    { toolUse: { id: 't-agent-map', name: 'Task', input: { description: 'Check tool_use → function_call mapping', subagent_type: 'code-reviewer' } } }
  ], { input: 30_000, output: 2100, cacheRead: 22_000 })
  b.wait(300).toolStarted('t-read', 'Read', { file_path: 'proto/gateway/v1/gateway.proto' }, 'proto/gateway/v1/gateway.proto')
  b.wait(40).toolFinished('t-read', { result: { file: { filePath: 'proto/gateway/v1/gateway.proto', numLines: 212, totalLines: 212 } }, summary: '212 lines', durationMs: 40 })
  b.toolStarted('t-buf', 'Bash', { command: 'buf generate' }, 'buf generate')
  b.wait(400).toolTitle('t-buf', BASH_TITLES['buf generate']!)
  b.wait(4100).toolFinished('t-buf', { output: '', result: { stdout: '', stderr: '', exitCode: 0 }, summary: 'ok', durationMs: 4100 })
  b.toolStarted('t-edit-route', 'Edit', { file_path: 'packages/core/server/routes/gateway.ts' }, 'packages/core/server/routes/gateway.ts')
  b.wait(200).toolFinished('t-edit-route', { result: { filePath: 'packages/core/server/routes/gateway.ts', structuredPatch: patch(148, 6) }, summary: '+148 −6', durationMs: 200 })
  b.toolStarted('t-agent-map', 'Task', { description: 'Check tool_use → function_call mapping', subagent_type: 'code-reviewer' }, 'Check tool_use → function_call mapping')
  b.notice('info', 'subagent_progress', { tool_use_id: 't-agent-map', steps: [{ tool: 'Read', target: 'server/gateway/translate.ts', status: 'done' }, { tool: 'Grep', target: '"tool_use" in packages/core', status: 'running' }], tokens: 6400, tool_calls: 2, model: 'glm-4.6' })

  b.wait(2000).assistant('m3b', 'glm-4.6', [
    { text: 'Streaming works on `glm-4.6` and `qwen3-coder`. Usage is left: the provider sends it as a separate chunk, and until it arrives **message_stop** must not be sent.' }
  ], { output: 900 })
  b.todos([
    { content: 'Generate TS from proto', activeForm: 'Generating TS from proto', status: 'completed' },
    { content: 'Stream handler in Nitro', activeForm: 'Writing the stream handler', status: 'completed' },
    { content: 'Usage and message_stop in the right order', activeForm: 'Running tests', status: 'in_progress' },
    { content: 'Provider retry tests', activeForm: 'Writing provider retry tests', status: 'pending' }
  ])

  b.wait(1500).assistant('m3c', 'glm-4.6', [{ toolUse: { id: 't-grep', name: 'Grep', input: { pattern: 'holdStopUntilUsage', path: 'packages/' } } }], { output: 300 })
  b.toolStarted('t-grep', 'Grep', { pattern: 'holdStopUntilUsage', path: 'packages/' }, '"holdStopUntilUsage" in packages/')
  b.wait(120).toolFinished('t-grep', { output: GREP_OUT, result: { mode: 'content', numFiles: 2, numMatches: 3, content: GREP_OUT }, summary: '3 matches · 2 files', durationMs: 120 })

  b.wait(2500).assistant('m3d', 'glm-4.6', [{ toolUse: { id: 't-edit-translate', name: 'Edit', input: { file_path: 'packages/core/server/gateway/translate.ts' } } }], { output: 1400 })
  b.toolStarted('t-edit-translate', 'Edit', { file_path: 'packages/core/server/gateway/translate.ts' }, 'packages/core/server/gateway/translate.ts')
  b.wait(100).toolFinished('t-edit-translate', { result: { filePath: 'packages/core/server/gateway/translate.ts', structuredPatch: TRANSLATE_PATCH }, summary: '+7 −2', durationMs: 100 })
  b.notice('info', 'hook', { event: 'PostToolUse', command: 'pnpm lint --fix', result: '✔ 2 files fixed', ok: true, duration_ms: 1200 })

  b.wait(2000).assistant('m3e', 'glm-4.6', [{ toolUse: { id: 't-vitest', name: 'Bash', input: { command: 'pnpm vitest run gateway' } } }], { output: 200 })
  b.toolStarted('t-vitest', 'Bash', { command: 'pnpm vitest run gateway' }, 'pnpm vitest run gateway')
  b.wait(400).toolTitle('t-vitest', BASH_TITLES['pnpm vitest run gateway']!)
  b.wait(3800).toolFinished('t-vitest', { output: VITEST_OUT, result: { stdout: VITEST_OUT, stderr: '', exitCode: 0 }, summary: 'exit 0', durationMs: 3800 })

  b.at(now - 48_000)
  b.assistant('m3f', 'glm-4.6', [
    { toolUse: { id: 't-agent-usage', name: 'Task', input: { description: 'Find where usage is collected', subagent_type: 'Explore' } } }
  ], { input: 36_000, cacheRead: 22_500, output: 3500 })
  b.at(now - 41_000)
  b.toolStarted('t-agent-usage', 'Task', { description: 'Find where usage is collected', subagent_type: 'Explore' }, 'Find where usage is collected')
  b.notice('info', 'subagent_progress', {
    tool_use_id: 't-agent-usage',
    steps: [
      { tool: 'Grep', target: '"usage" in packages/core', meta: '12 matches', status: 'done' },
      { tool: 'Read', target: 'server/gateway/translate.ts', status: 'done' },
      { tool: 'Read', target: 'server/gateway/providers/openrouter.ts', status: 'done' },
      { tool: 'Read', target: 'server/gateway/providers/ollama.ts', status: 'running' }
    ],
    tokens: 18_200,
    tool_calls: 4,
    model: 'qwen3-coder'
  })
  // Started 48s ago by the client's file watcher; announced after the subagent in the transcript.
  const vitest = b.taskStarted('vitest-stream', 't-vitest-bg', 'vitest gateway.stream', 'vitest — gateway.stream')
  vitest.timestamp = new Date(now - 48_000).toISOString()
  return b.events
}

// Short, finished histories for the other sessions.
export function otherHistory(info: SessionInfo, now: number): SessionEvent[] {
  const b = new EventBuilder(info.id, Date.parse(info.updatedAt) || now)
  b.sessionUpdated(info)
  b.user(info.id + '-u1', info.title)
  b.wait(300).turnStarted(info.id + '-u1', info.model)
  b.wait(4000).assistant(info.id + '-m1', info.model, [{ text: `Picked up **${info.title}**. The branch is \`${info.gitBranch}\`; nothing is running right now.` }], { input: 4000, output: 60 })
  if (info.status === SessionStatus.ERROR) {
    b.notice('error', 'retry', 'Provider returned 529 overloaded|openrouter · glm-4.6 · attempt 5 of 5 · gave up')
    b.turnFinished('error', { durationMs: 41_000, costUsd: 0.02, usage: { input: 4000, output: 60 }, error: 'provider overloaded' })
  } else {
    b.turnFinished('success', { durationMs: 41_000, costUsd: 0.03, usage: { input: 12_400, output: 2100 } })
  }
  return b.events
}
