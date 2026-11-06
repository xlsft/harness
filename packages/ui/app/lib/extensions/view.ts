// How the Extensions screen narrows and words what the client sent. Nothing here decides what exists or what may run.

import { tt } from '~/utils/i18n'
import type { SkillAudit } from '~/gen/harness/skills/v1/search_pb'
import { SlashCommandKind } from '~/gen/harness/agent/v1/slash_commands_pb'
import { ConnectorStatus, type ExtensionConnector, type ExtensionPluginGroupVM, type ExtensionPluginVM, type ExtensionSkill, type ExtensionSkillGroup, type ExtensionType, type ExtensionView } from './types'

export type Tone = 'ok' | 'warn' | 'bad' | 'muted'

export interface StatusView {
  label: string
  tone: Tone
}

const STATUS: Record<ConnectorStatus, StatusView> = {
  [ConnectorStatus.UNSPECIFIED]: { label: tt('Unknown'), tone: 'muted' },
  [ConnectorStatus.CONNECTED]: { label: tt('Connected'), tone: 'ok' },
  [ConnectorStatus.NEEDS_SIGN_IN]: { label: tt('Needs sign-in'), tone: 'warn' },
  [ConnectorStatus.IN_SESSIONS]: { label: tt('Connects in sessions'), tone: 'muted' },
  [ConnectorStatus.NEEDS_APPROVAL]: { label: tt('Needs approval'), tone: 'warn' },
  [ConnectorStatus.NEEDS_TRUST]: { label: tt('Folder not trusted'), tone: 'muted' },
  [ConnectorStatus.OFF]: { label: tt('Off'), tone: 'muted' },
  [ConnectorStatus.BLOCKED]: { label: tt('Blocked by policy'), tone: 'muted' },
  [ConnectorStatus.ERROR]: { label: tt('Error'), tone: 'bad' }
}

export function connectorStatus(s: ConnectorStatus): StatusView {
  return STATUS[s] ?? STATUS[ConnectorStatus.UNSPECIFIED]
}

export const TONE_CLASS: Record<Tone, string> = { ok: 'text-ok', warn: 'text-warn', bad: 'text-bad', muted: 'text-muted' }

export function parseType(v: unknown): ExtensionType {
  return v === 'connectors' || v === 'plugins' ? v : 'skills'
}

export function parseView(v: unknown): ExtensionView {
  return v === 'discover' ? 'discover' : 'yours'
}

export function shortDate(iso: string, now = new Date()): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const opts: Intl.DateTimeFormatOptions = { month: 'short', day: 'numeric', timeZone: 'UTC' }
  if (d.getUTCFullYear() !== now.getUTCFullYear()) opts.year = 'numeric'
  return d.toLocaleDateString('en-US', opts)
}

/** A source's own count, short. 0 means it gave none, and nothing is shown. */
export function installsLabel(n: bigint | number): string {
  const v = Number(n)
  if (!v) return ''
  const count = v >= 1_000_000 ? `${(v / 1_000_000).toFixed(1).replace(/\.0$/, '')}M` : v >= 1000 ? `${(v / 1000).toFixed(1).replace(/\.0$/, '')}K` : String(v)
  return v === 1 ? tt('{0} install', [count]) : tt('{0} installs', [count])
}

const AUDIT_RANK: Record<string, number> = { fail: 0, warn: 1, pass: 2 }

/** The worst verdict speaks for the skill. No audits: not known, which isn't a pass. */
export function auditSummary(audits: readonly Pick<SkillAudit, 'status'>[]): StatusView | null {
  if (!audits.length) return null
  const worst = [...audits].map(a => a.status.toLowerCase()).sort((a, b) => (AUDIT_RANK[a] ?? 1) - (AUDIT_RANK[b] ?? 1))[0]!
  return { label: tt('Audit: {0}', [worst]), tone: worst === 'pass' ? 'ok' : worst === 'fail' ? 'bad' : 'warn' }
}

export function auditTone(status: string): Tone {
  const s = status.toLowerCase()
  return s === 'pass' ? 'ok' : s === 'fail' ? 'bad' : 'warn'
}

function matches(words: string[], hay: string[]): boolean {
  const text = hay.join(' ').toLowerCase()
  return words.every(w => text.includes(w))
}

const words = (q: string) => q.toLowerCase().split(/\s+/).filter(Boolean)

export function skillTags(s: ExtensionSkill): string[] {
  const out: string[] = []
  if (s.command?.kind === SlashCommandKind.COMMAND) out.push('Command')
  if (s.blockedByPolicy) out.push('Admin')
  return out
}

export interface SkillFilters {
  query: string
  source: string
  state: string
  sort: string
}

function skillState(s: ExtensionSkill): string {
  if (s.external && !s.external.approved) return 'attention'
  if (s.blockedByPolicy || (s.command && !s.command.enabled)) return 'off'
  return 'on'
}

const skillName = (s: ExtensionSkill) => s.command?.name ?? s.external?.name ?? ''

/** Skills under their sources: what doesn't match goes, and so does a group left with nothing. */
export function narrowSkills(groups: readonly ExtensionSkillGroup[], f: SkillFilters): ExtensionSkillGroup[] {
  const w = words(f.query)
  const out: ExtensionSkillGroup[] = []
  for (const g of groups) {
    if (f.source && g.key !== f.source) continue
    const skills = g.skills.filter(s => (!f.state || skillState(s) === f.state)
      && matches(w, [skillName(s), s.command?.description ?? s.external?.description ?? '', g.title]))
    if (!skills.length) continue
    if (f.sort === 'recent') skills.sort((a, b) => (b.updatedAt || b.external?.installedAt || '').localeCompare(a.updatedAt || a.external?.installedAt || ''))
    else if (f.sort === 'name') skills.sort((a, b) => skillName(a).localeCompare(skillName(b)))
    out.push({ ...g, skills })
  }
  return out
}

function pluginState(p: ExtensionPluginVM): string {
  if (p.plugin.status === 'active') return 'on'
  return p.plugin.status === 'disabled' ? 'off' : 'attention'
}

export function narrowPlugins(groups: readonly ExtensionPluginGroupVM[], f: SkillFilters): ExtensionPluginGroupVM[] {
  const w = words(f.query)
  const out: ExtensionPluginGroupVM[] = []
  for (const g of groups) {
    if (f.source && g.key !== f.source) continue
    const plugins = g.plugins.filter(p => (!f.state || pluginState(p) === f.state)
      && matches(w, [p.plugin.name, p.plugin.displayName, p.plugin.description, p.category, p.publisher, g.title, ...p.tags]))
    if (!plugins.length) continue
    if (f.sort === 'recent') plugins.sort((a, b) => (b.plugin.installedAt ?? '').localeCompare(a.plugin.installedAt ?? ''))
    else if (f.sort === 'name') plugins.sort((a, b) => (a.plugin.displayName || a.plugin.name).localeCompare(b.plugin.displayName || b.plugin.name))
    out.push({ ...g, plugins })
  }
  return out
}

export interface ConnectorFilters {
  query: string
  manager: string
  type: string
  status: string
}

export function narrowConnectors(list: readonly ExtensionConnector[], f: ConnectorFilters): ExtensionConnector[] {
  const w = words(f.query)
  return list.filter(c => (!f.manager || c.badges.includes(f.manager))
    && (!f.type || (f.type === 'remote') === c.remote)
    && (!f.status || (f.status === 'attention' ? connectorStatus(c.status).tone === 'warn' || connectorStatus(c.status).tone === 'bad' : c.status === ConnectorStatus.CONNECTED))
    && matches(w, [c.name, c.endpoint, ...c.badges, ...c.providers.map(p => p.pluginName)]))
}

/** The folded row's second line: who brings it. */
export function providedBy(c: Pick<ExtensionConnector, 'providers'>): string {
  const names = [...new Set(c.providers.map(p => p.pluginName))]
  if (!names.length) return ''
  if (names.length === 1) return tt('Provided by the {0} plugin', [names[0]])
  return tt('Provided by the {0} and {1} plugins', [names.slice(0, -1).join(', '), names.at(-1)])
}

/** First tag, and how many more: rows stay one line. */
export function compactTags(tags: readonly string[]): { first: string, more: number } | null {
  if (!tags.length) return null
  return { first: tags[0]!, more: tags.length - 1 }
}
