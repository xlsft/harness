// The model list's content: the server's models by provider, then a group per connected ACP agent. What may be picked
// is the client's decision (AcpAgent.selectable and .models, AcpSessionState.models, the refusal text); this only puts
// it in rows. An agent's model ids are its own and may equal a server model's or another agent's, so a row is
// identified by its key: "<engine>/<id>" for an agent's model, the id for a server's.

import { tt } from '~/utils/i18n'
import { AcpModelsStatus, SessionFeature, type AcpAgent, type AcpSessionState } from '~/gen/harness/agent/v1/acp_pb'
import type { EffortName } from '~/lib/session/types'
import type { ModelInfo } from '~/lib/transport/types'
import { fitEffort, harnessEffortLevels, type EffortLevel } from '~/utils/composer'

// What the list is for.
export type ModelScope
  // A new session: every server model and every connected agent's.
  = { kind: 'new' }
  // Only server models: nothing else can run there (a side chat, continuing another machine's session).
    | { kind: 'server' }
  // A running session: its engine ("harness" or "acp:<agent id>") and its agent's state.
    | { kind: 'session', engine: string, acp: AcpSessionState | null }

export interface ModelRow {
  key: string
  id: string
  name: string
  // The server's id shown next to its name; empty when it says nothing more.
  detail: string
  // "" for a server model.
  engine: string
  offline: boolean
  contextWindow: number
  price: string
  // Set: shown, but it cannot be picked.
  disabledReason: string
}

export interface ModelSection {
  key: string
  title: string
  rows: ModelRow[]
  // A line under the title: why the group's rows are disabled, or how finding its models went.
  note: string
  // The note reports a failure a refresh may fix.
  failed: boolean
}

export interface ModelChoicesInput {
  models: ModelInfo[]
  agents: AcpAgent[]
  refusal: string
  scope: ModelScope
  query?: string
  favorites?: string[]
}

export const modelKey = (id: string, engine = '') => (engine ? `${engine}/${id}` : id)

function serverRow(m: ModelInfo): ModelRow {
  return {
    key: m.id,
    id: m.id,
    name: m.displayName || m.id,
    detail: m.displayName && m.displayName !== m.id ? m.displayId || m.id : '',
    engine: '',
    offline: !!m.offline,
    contextWindow: m.contextWindow,
    price: m.price,
    disabledReason: ''
  }
}

function agentRow(m: { id: string, name: string }, engine: string, disabledReason = ''): ModelRow {
  return { key: modelKey(m.id, engine), id: m.id, name: m.name || m.id, detail: '', engine, offline: false, contextWindow: 0, price: '', disabledReason }
}

function agentNote(a: AcpAgent) {
  if (a.modelsStatus === AcpModelsStatus.PROBING && !a.models.length) return 'Finding models…'
  if (a.modelsStatus === AcpModelsStatus.FAILED) return `Could not read its models: ${a.modelsError}`
  return ''
}

// The sections to show. Empty groups go away while searching; favorites come first and stay what they are (a disabled
// row stays disabled there).
export function modelSections(input: ModelChoicesInput): ModelSection[] {
  const { scope } = input
  const q = (input.query ?? '').trim().toLowerCase()
  const groups: ModelSection[] = []
  const acpSession = scope.kind === 'session' && scope.engine !== 'harness'

  if (acpSession) {
    // Only what the session accepts.
    const engine = scope.engine
    groups.push({ key: engine, title: scope.acp?.agentName || 'Agent', rows: (scope.acp?.models ?? []).map(m => agentRow(m, engine)), note: '', failed: false })
  } else {
    const byProvider = new Map<string, ModelRow[]>()
    for (const m of input.models) {
      const p = m.providerName || m.provider || 'Models'
      if (!byProvider.has(p)) byProvider.set(p, [])
      byProvider.get(p)!.push(serverRow(m))
    }
    for (const [title, rows] of byProvider) groups.push({ key: `provider:${title}`, title, rows, note: '', failed: false })
    if (scope.kind !== 'server') {
      const reason = scope.kind === 'session' ? input.refusal : ''
      for (const a of input.agents) {
        if (!a.selectable) continue
        const rows = a.models.map(m => agentRow(m, a.engine, reason))
        groups.push({ key: a.engine, title: a.name, rows, note: reason && rows.length ? reason : agentNote(a), failed: a.modelsStatus === AcpModelsStatus.FAILED })
      }
    }
  }

  const matches = (r: ModelRow, group: string) => !q || [r.id, r.name, group].some(x => x.toLowerCase().includes(q))
  const favorites = input.favorites ?? []
  const fav: ModelRow[] = []
  const out: ModelSection[] = []
  for (const g of groups) {
    const rows = g.rows.filter(r => matches(r, g.title))
    fav.push(...rows.filter(r => favorites.includes(r.key)))
    const rest = rows.filter(r => !favorites.includes(r.key))
    if (rest.length || (!q && !g.rows.length && g.note)) out.push({ ...g, rows: rest })
  }
  if (fav.length) out.unshift({ key: 'favorites', title: tt('Favorites'), rows: fav, note: '', failed: false })
  return out
}

// What Enter picks: the first row that can be picked.
export function firstPickable(sections: ModelSection[]): ModelRow | undefined {
  return sections.flatMap(s => s.rows).find(r => !r.offline && !r.disabledReason)
}

// An agent's effort levels as the chip shows them: the agent's ids and names, whatever it calls them.
export function agentEffortLevels(efforts: { id: string, name: string }[]): EffortLevel[] {
  return efforts.map(e => ({ id: e.id, label: e.name || e.id }))
}

// The effort chip of a running session: the agent's levels on an agent, the model's on harness, nothing where the
// client says the session has no effort. `recommended` only marks a level: a session keeps the one it is on.
export function sessionEffort(c: { onAgent: boolean, unsupported: boolean, acp?: AcpSessionState | null, harness: EffortName[], chosen: EffortName, recommended?: EffortName }): { levels: EffortLevel[], current?: string, recommended?: string } {
  if (c.unsupported) return { levels: [] }
  if (c.onAgent) return { levels: agentEffortLevels(c.acp?.efforts ?? []), current: c.acp?.currentEffortId || undefined, recommended: c.acp?.defaultEffortId || undefined }
  return { levels: harnessEffortLevels(c.harness), current: fitEffort(c.chosen, c.harness), recommended: c.recommended }
}

// The effort chip of the new-session screen on an agent, from what the client has cached about it: the level chosen
// here while the agent still lists it, else its default, which is what it starts on and what it recommends, else the
// current level last reported.
export function newSessionAgentEffort(agent: AcpAgent | undefined, chosen: string): { levels: EffortLevel[], current?: string, recommended?: string } {
  if (!agent || agent.unsupportedFeatures.includes(SessionFeature.EFFORT)) return { levels: [] }
  const current = agent.efforts.some(e => e.id === chosen) ? chosen : agent.defaultEffortId || agent.currentEffortId
  return { levels: agentEffortLevels(agent.efforts), current: current || undefined, recommended: agent.defaultEffortId || undefined }
}

// What the new-session screen starts a session with. On an agent the server's effort and permission mode do not apply,
// the agent's mode and effort do, and only what the agent supports goes along.
export function newSessionChoice<E, M>(c: { engine: string, agent?: AcpAgent, acpMode: string, acpEffort?: string, effort?: E, mode: M, worktree?: { baseRef: string } }) {
  if (!c.engine) return { effort: c.effort, mode: c.mode, worktree: c.worktree, engine: undefined, acpModeId: undefined, acpEffortId: undefined }
  return {
    effort: undefined,
    mode: undefined,
    worktree: c.agent?.unsupportedFeatures.includes(SessionFeature.WORKTREE) ? undefined : c.worktree,
    engine: c.engine,
    acpModeId: c.acpMode || undefined,
    acpEffortId: c.agent?.unsupportedFeatures.includes(SessionFeature.EFFORT) ? undefined : c.acpEffort || undefined
  }
}

// The new-session screen's chosen engine and model, kept only while the client still offers them. A model nobody can
// tell about yet (the agent is being probed for the first time) stays chosen.
export function fitAcpChoice(choice: { engine: string, model: string }, agents: AcpAgent[]): { engine: string, model: string } {
  if (!choice.engine) return choice
  const agent = agents.find(a => a.engine === choice.engine)
  if (!agent?.selectable) return { engine: '', model: '' }
  if (agent.models.length && !agent.models.some(m => m.id === choice.model)) return { engine: agent.engine, model: agent.currentModelId || agent.models[0]!.id }
  return choice
}
