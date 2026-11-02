// Demo data for the Terminal pane (docs/design/ui/Terminal.dc.html). The agent contract has no
// terminal yet, so the tabs and their scrollback are copied from the artboard.

export interface TermLine {
  t: string
  // Text color (hex); body text when omitted.
  c?: string
  // The prompt cursor sits at the end of this line.
  cur?: boolean
}

export interface TermTab {
  id: string
  name: string
  // Status mark after the name: '●' running (accent), '1' exit code (rose).
  status: string
  statusColor: string
  lines: TermLine[]
  // Started by the agent as a background task (shows the "Started by the agent" banner).
  agent?: { startedAgoMs: number }
  // The process exited non-zero (shows the exit footer).
  exitCode?: number
}

const A = '#2b7fff'
const G = '#8f8f8f'
const W = '#ffffff'
const OK = '#00bc7d'
const BAD = '#ff2056'
const WARN = '#fe9a00'

const L = (t: string, c?: string, cur?: boolean): TermLine => ({ t, c, cur })

export function promptLines(time: string, cmd: string, cur = false): TermLine[] {
  return [L(`╭ ${time} │ ~/Projects/xlsft/harness │ main ?2`, G), L(`╰ ❯ ${cmd}`, W, cur)]
}

const harness: TermLine[] = [
  ...promptLines('16:41', 'pnpm --filter core test'),
  L(' RUN  v3.2.4 /packages/core', A),
  L(' ✓ gateway/translate.test.ts (14) 212ms', OK),
  L(' ✓ gateway/stream.test.ts (6) 1.4s', OK),
  L(' ↓ gateway/retry.test.ts (3)', WARN),
  L(''),
  L(' Test Files  2 passed | 1 skipped (3)'),
  L('      Tests  20 passed | 3 skipped (23)'),
  L('   Duration  2.31s', G),
  L(''),
  ...promptLines('16:42', 'git status -s'),
  L(' M packages/core/server/gateway/translate.ts', WARN),
  L('?? packages/core/server/gateway/translate.test.ts', BAD),
  L('?? packages/client/', BAD),
  L(''),
  ...promptLines('16:42', '', true)
]

const dev: TermLine[] = [
  L('$ pnpm --filter core dev', G),
  L(''),
  L('  ➜ Local:   http://localhost:3000/', A),
  L('  ✔ Nitro built in 412 ms', OK),
  L('  ℹ POST /gateway.v1.Gateway/Stream 200 — 3.4s'),
  L('  ℹ POST /gateway.v1.Gateway/Stream 200 — 2.9s'),
  L('  ⚠ provider ollama: no usage in stream, estimating', WARN),
  L('  ℹ POST /gateway.v1.Gateway/Stream 200 — 61.2s'),
  L('', undefined, true)
]

const gotest: TermLine[] = [
  ...promptLines('16:39', 'cd packages/client && go test ./loop'),
  L('=== RUN   TestLoopToolCalls'),
  L('=== RUN   TestLoopToolCalls/parallel_reads'),
  L('    loop_test.go:41: expected 3 tool calls, got 2', BAD),
  L('--- FAIL: TestLoopToolCalls (0.02s)', BAD),
  L('FAIL', BAD),
  L('FAIL    harness/client/loop  0.214s', BAD)
]

export function terminalTabs(): TermTab[] {
  return [
    { id: 'harness', name: 'harness', status: '', statusColor: G, lines: harness },
    { id: 'core-dev', name: 'core dev', status: '●', statusColor: A, lines: dev, agent: { startedAgoMs: 134_000 } },
    { id: 'go-test', name: 'go test', status: '1', statusColor: BAD, lines: gotest, exitCode: 1 }
  ]
}

export const CURSOR_STYLES = ['Block', 'Bar', 'Underline'] as const
export type CursorStyle = typeof CURSOR_STYLES[number]
