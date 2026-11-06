// What the client reports for the slash menu and the Extensions screen, as these show it. The client decides what exists,
// what wins a name and what is on; here it only becomes rows.
import { SlashCommandKind, SlashCommandResult_Action, type ListSlashCommandsResponse, type SlashCommand } from '~/gen/harness/agent/v1/slash_commands_pb'
import { fuzzyScore } from '~/lib/files/tree'
import type { MenuOption } from '~/utils/composer'

/** What to ask the client about: a session, or a folder before one exists. */
export type SlashTarget = { sessionId: string } | { cwd: string }

export type SlashCatalog = Pick<ListSlashCommandsResponse, 'commands' | 'projectSkippedUntrusted' | 'listingBudgetChars' | 'listingUsedChars'>

const TAGS: Partial<Record<SlashCommandKind, string>> = {
  [SlashCommandKind.BUILTIN]: 'built-in',
  [SlashCommandKind.SKILL]: 'skill',
  [SlashCommandKind.COMMAND]: 'command',
  [SlashCommandKind.AGENT]: 'agent'
}

/** The rows of the slash menu. Before a session exists, what needs a conversation is left out. */
export function slashMenuOptions(list: readonly SlashCommand[], o: { hasSession: boolean }): MenuOption[] {
  return list
    .filter(c => c.enabled && c.userInvocable && !c.shadowedBy && (o.hasSession || !c.needsSession))
    .map(c => ({
      key: `/${c.name}`,
      label: `/${c.name}`,
      detail: [c.argumentHint, c.description].filter(Boolean).join(' · '),
      tag: TAGS[c.kind] ?? ''
    }))
}

// Edits between two strings, a swap of neighbours counting as one.
function typoDistance(a: string, b: string): number {
  const d = Array.from({ length: a.length + 1 }, (_, i) => [i, ...Array<number>(b.length).fill(0)])
  for (let j = 1; j <= b.length; j++) d[0]![j] = j
  for (let i = 1; i <= a.length; i++) {
    for (let j = 1; j <= b.length; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1
      let v = Math.min(d[i - 1]![j]! + 1, d[i]![j - 1]! + 1, d[i - 1]![j - 1]! + cost)
      if (i > 1 && j > 1 && a[i - 1] === b[j - 2] && a[i - 2] === b[j - 1]) v = Math.min(v, d[i - 2]![j - 2]! + 1)
      d[i]![j] = v
    }
  }
  return d[a.length]![b.length]!
}

/**
 * The rows matching what's typed, best first: names starting with it, a namespaced name's last part, the letters in order
 * anywhere in the name (/efort, /cr), then a near miss with a typo (/mdoel, /moddel).
 */
export function matchSlash(options: readonly MenuOption[], token: string): MenuOption[] {
  const q = (token || '/').toLowerCase()
  const word = q.slice(1)
  const name = (c: MenuOption) => c.label.toLowerCase().slice(1)
  const starts = options.filter(c => c.label.toLowerCase().startsWith(q))
  const inner = options.filter(c => !starts.includes(c) && name(c).includes(`:${word}`))
  const taken = new Set([...starts, ...inner])
  const scored = options
    .filter(c => !taken.has(c))
    .map(c => ({ c, s: fuzzyScore(word, c.label.slice(1)) }))
    .filter((x): x is { c: MenuOption, s: number } => x.s !== null)
    .sort((x, y) => y.s - x.s)
    .map(x => x.c)
  scored.forEach(c => taken.add(c))
  const allowed = word.length >= 6 ? 2 : word.length >= 3 ? 1 : 0
  const near = allowed === 0
    ? []
    : options
        .filter(c => !taken.has(c))
        .map(c => ({ c, d: typoDistance(word, name(c).slice(0, word.length + allowed)) }))
        .filter(x => x.d <= allowed)
        .sort((x, y) => x.d - y.d)
        .map(x => x.c)
  return [...starts, ...inner, ...scored, ...near]
}

const SOURCE_LABELS: Record<string, string> = {
  personal: 'Personal',
  project: 'Project',
  imported: 'Imported',
  organization: 'Organization',
  builtin: 'Built-in',
  agent: 'Agent',
  plugin: 'Plugin'
}

export function sourceLabel(source: string): string {
  return SOURCE_LABELS[source] ?? source
}

/** The facts of a row, short, for its chips. */
export function commandFacts(c: SlashCommand): string[] {
  const out: string[] = []
  if (c.shadowedBy) out.push(`replaced by ${sourceLabel(c.shadowedBy).toLowerCase()}`)
  if (c.kind !== SlashCommandKind.BUILTIN && c.kind !== SlashCommandKind.AGENT) {
    if (!c.userInvocable) out.push('model only')
    if (!c.modelInvocable) out.push('you only')
  }
  if (c.fork) out.push('runs in a subagent')
  if (c.model) out.push(`model ${c.model}`)
  if (c.effort) out.push(`effort ${c.effort}`)
  if (c.allowedTools.length) out.push(`may use ${c.allowedTools.join(', ')}`)
  if (c.paths.length) out.push(`for ${c.paths.join(', ')}`)
  return out
}


/** What's left to do after a slash command: an action once, while it is fresh, so a reload doesn't repeat it. */
export interface SlashActionVM {
  id: string
  action: SlashCommandResult_Action
  newSessionId: string
  at: number
}

export const SLASH_ACTION_FRESH_MS = 60_000

/** Whether an action still has to happen now, given those done so far. */
export function takeSlashAction(a: SlashActionVM | undefined, done: readonly string[], now: number): boolean {
  return !!a && a.action !== SlashCommandResult_Action.UNSPECIFIED && !done.includes(a.id) && now - a.at < SLASH_ACTION_FRESH_MS
}
