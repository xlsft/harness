// What the client reports for the slash menu and the Customize screen, as these show it. The client decides what exists,
// what wins a name and what is on; here it only becomes rows.
import { tt } from '~/utils/i18n'
import { SlashCommandKind, SlashCommandResult_Action, type ListSlashCommandsResponse, type SlashCommand } from '~/gen/harness/agent/v1/slash_commands_pb'
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

/** The rows matching what's typed: names starting with it first, then a namespaced name's last part. */
export function matchSlash(options: readonly MenuOption[], token: string): MenuOption[] {
  const q = (token || '/').toLowerCase()
  const starts = options.filter(c => c.label.toLowerCase().startsWith(q))
  const inner = options.filter(c => !starts.includes(c) && c.label.toLowerCase().includes(`:${q.slice(1)}`))
  return [...starts, ...inner]
}

const SOURCE_LABELS: Record<string, string> = {
  personal: 'Personal',
  project: 'Project',
  imported: 'Imported',
  organization: 'Organization',
  builtin: 'Built-in',
  agent: 'Agent'
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

export interface CustomizeSection {
  title: string
  rows: SlashCommand[]
}

/** The Customize screen's sections: only what isn't empty, in the client's order. */
export function customizeSections(list: readonly SlashCommand[]): CustomizeSection[] {
  const pick = (kind: SlashCommandKind) => list.filter(c => c.kind === kind)
  return [
    { title: tt('Skills'), rows: pick(SlashCommandKind.SKILL) },
    { title: tt('Commands'), rows: pick(SlashCommandKind.COMMAND) },
    { title: tt('Built-in commands'), rows: pick(SlashCommandKind.BUILTIN) }
  ].filter(s => s.rows.length)
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
