// Composer data: slash commands, permission modes and efforts (Composer.dc.html).
import type { EffortName, PermissionModeName } from '~/lib/session/types'
import type { IconName } from './icons'

export interface MenuOption {
  key: string
  label: string
  detail: string
  tag?: string
  icon?: IconName
  iconCls?: string
}

// Built-in commands; skills and project commands come from the client in M2.
export const SLASH_COMMANDS: MenuOption[] = [
  { key: '/code-review', label: '/code-review', detail: 'Review the current diff for bugs', tag: 'skill' },
  { key: '/compact', label: '/compact', detail: 'Summarize and shrink the context', tag: 'built-in' },
  { key: '/model', label: '/model', detail: 'Switch model or provider', tag: 'built-in' },
  { key: '/deploy-core', label: '/deploy-core', detail: '.claude/commands/deploy-core.md', tag: 'project' },
  { key: '/clear', label: '/clear', detail: 'Start a new session with an empty context', tag: 'built-in' },
  { key: '/rewind', label: '/rewind', detail: 'Restore code or conversation to a checkpoint', tag: 'built-in' }
]

// "/compact [focus instructions]": the instructions ('' for none), or null when the text is not the command.
export function compactCommand(text: string): string | null {
  const t = text.trim()
  if (t === '/compact') return ''
  return /^\/compact\s/.test(t) ? t.slice('/compact'.length).trim() : null
}

export type ComposerMode = 'normal' | 'shell' | 'memory'

// Where a submitted draft goes: shell mode runs the command in the session's shell (RunShellCommand, never a
// chat message); memory mode and plain text are messages. Null for an empty draft.
export type ComposerRoute = { kind: 'shell', command: string } | { kind: 'message', text: string }

export function composerRoute(mode: ComposerMode, body: string, memoryTarget = 'CLAUDE.md'): ComposerRoute | null {
  const t = body.trim()
  if (!t) return null
  if (mode === 'shell') return { kind: 'shell', command: t }
  if (mode === 'memory') return { kind: 'message', text: `# ${t} (save to ${memoryTarget})` }
  return { kind: 'message', text: body }
}

export const MENTION_EXTRAS: MenuOption[] = [
  { key: 'agent:code-reviewer', label: 'code-reviewer', detail: 'agent', icon: 'ai', iconCls: 'text-accent' },
  { key: 'mcp:youtrack:HRN-42', label: 'youtrack:HRN-42', detail: 'MCP resource', icon: 'box', iconCls: 'text-muted' }
]

export const MODES: { key: PermissionModeName, label: string, detail: string, cls: string, sep?: boolean }[] = [
  { key: 'default', label: 'Ask', detail: 'Approve every edit and command', cls: 'text-strong' },
  { key: 'accept_edits', label: 'Accept edits', detail: 'File edits auto, commands ask', cls: 'text-strong' },
  { key: 'auto', label: 'Auto', detail: 'Safe actions auto, risky ones ask', cls: 'text-strong' },
  { key: 'plan', label: 'Plan', detail: 'Read only, produce a plan', cls: 'text-cyan' },
  { key: 'bypass', label: 'Bypass', detail: 'All safeguards off. Sandboxed hosts only', cls: 'text-bad', sep: true }
]

// Every mode in the client's order (the Shift+Tab cycle, then the modes outside it), for Settings; Don't ask is
// offered there only.
export const DONT_ASK_MODE: typeof MODES[number] = { key: 'dont_ask', label: 'Don\'t ask', detail: 'Never prompts: anything that would ask is denied', cls: 'text-strong' }
export const MODE_ORDER: PermissionModeName[] = ['default', 'accept_edits', 'plan', 'auto', 'dont_ask', 'bypass']

// The Shift+Tab order when the client does not report one (InitializeResponse.permission_modes).
export const MODE_CYCLE: PermissionModeName[] = ['default', 'accept_edits', 'auto', 'plan']

/** The modes of MODES the server's policy allows (InitializeResponse.disabled_permission_modes). */
export function allowedModes(disabled: readonly PermissionModeName[]) {
  return MODES.filter(m => !disabled.includes(m.key))
}

/** `mode` when the policy allows it, else the first allowed mode (Ask can never be disabled). */
export function fitMode(mode: PermissionModeName, disabled: readonly PermissionModeName[]): PermissionModeName {
  return disabled.includes(mode) ? (allowedModes(disabled)[0]?.key ?? 'default') : mode
}

/** The mode after `cur` in the Shift+Tab cycle, skipping disabled modes; modes outside the cycle go to its start. */
export function nextMode(cur: PermissionModeName, cycle: readonly PermissionModeName[], disabled: readonly PermissionModeName[]): PermissionModeName {
  const modes = (cycle.length ? cycle : MODE_CYCLE).filter(m => !disabled.includes(m))
  if (!modes.length) return 'default'
  return modes[(modes.indexOf(cur) + 1) % modes.length]!
}

export const MODE_LABEL: Record<PermissionModeName, string> = {
  default: 'Ask',
  accept_edits: 'Accept edits',
  auto: 'Auto',
  plan: 'Plan',
  dont_ask: 'Don\'t ask',
  bypass: 'Bypass'
}

export const EFFORT_LABEL: Record<EffortName, string> = { low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max' }

export const EFFORTS: EffortName[] = ['low', 'medium', 'high']
const ALL_EFFORTS: EffortName[] = ['low', 'medium', 'high', 'xhigh', 'max']

/** The effort levels a model offers; an older client without capabilities gets the basic three. */
export function modelEfforts(m?: { efforts?: EffortName[] }): EffortName[] {
  return m?.efforts ?? EFFORTS
}

/** `e` if the model offers it, else its nearest level; undefined for a model without effort. */
export function fitEffort(e: EffortName, levels: EffortName[]): EffortName | undefined {
  if (!levels.length || levels.includes(e)) return levels.length ? e : undefined
  const at = ALL_EFFORTS.indexOf(e)
  return [...levels].sort((a, b) => Math.abs(ALL_EFFORTS.indexOf(a) - at) - Math.abs(ALL_EFFORTS.indexOf(b) - at))[0]
}

/** The folder the new-session screen starts with: the one ?cwd= asks for ("+" of a sidebar group), else none ('',
 * a session without a folder). The last folder used is deliberately not remembered. */
export function newSessionFolder(query: Record<string, unknown>): string {
  return typeof query.cwd === 'string' ? query.cwd : ''
}

// Initial state for the component kit (/kit) and tests: reaches each state of Composer.dc.html directly.
export interface ComposerPreset {
  text?: string
  menu?: 'slash' | 'mention' | 'mode' | 'model'
  drop?: number
  recording?: boolean
  error?: string
  focused?: boolean
  files?: { name: string, size: number, progress?: number, error?: string, image?: boolean }[]
}
