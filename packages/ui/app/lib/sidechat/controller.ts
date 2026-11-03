// The side chat pane's state and actions, over a SideChatsApi: the session's side chats as tabs, the messages of
// the selected one, a streaming answer with Stop, a draft and a quote per chat, and the model / effort of each
// chat. Plain TypeScript: the pane wraps it in reactive() (so every method must be called on that proxy).

import { fitEffort, modelEfforts } from '~/utils/composer'
import type { EffortName } from '~/lib/session/types'
import type { AskHandle, SideChatInfo, SideChatMessage, SideChatsApi } from './types'

export interface SideChatDefaults {
  model: string
  effort?: EffortName
}

export interface SideChatView {
  info: SideChatInfo
  messages: SideChatMessage[]
  // Messages fetched (get) at least once.
  loaded: boolean
  // The composer of this chat: the question being typed and the quote it is about.
  draft: string
  quote: string
  // The id of the assistant message streaming right now ('' when idle).
  streamingId: string
  // The last failure of this chat (ask, model change), shown above the composer.
  error: string
}

const STREAMING_ID = 'streaming'

// What a new side chat starts with: the main session's model and its effort, fitted to the levels the model
// offers (undefined for a model without effort). mainEffort is the effort the main composer shows.
export function sideChatDefaults(mainModel: string, mainEffort: EffortName | undefined, models: { id: string, efforts?: EffortName[] }[]): SideChatDefaults {
  const m = models.find(x => x.id === mainModel)
  return { model: mainModel, effort: fitEffort(mainEffort ?? 'medium', modelEfforts(m)) }
}

function errorText(err: unknown) {
  return err instanceof Error ? err.message : String(err)
}

export class SideChatController {
  chats: SideChatView[] = []
  selectedId = ''
  // The first list is loading.
  loading = false
  // A failure outside any chat (list, create).
  error = ''
  private handles = new Map<string, AskHandle>()

  constructor(
    private api: SideChatsApi,
    readonly sessionId: string,
    private defaults: () => SideChatDefaults,
    private models: () => { id: string, efforts?: EffortName[] }[] = () => []
  ) {}

  get selected(): SideChatView | undefined {
    return this.chats.find(c => c.info.id === this.selectedId)
  }

  get streaming() {
    return !!this.selected?.streamingId
  }

  private view(info: SideChatInfo): SideChatView {
    return { info, messages: [], loaded: false, draft: '', quote: '', streamingId: '', error: '' }
  }

  async load() {
    this.loading = true
    this.error = ''
    try {
      const list = await this.api.list(this.sessionId)
      this.chats = list.map(info => this.chats.find(c => c.info.id === info.id) ?? this.view(info))
      if (!this.selected && this.chats.length) await this.select(this.chats.at(-1)!.info.id)
      else if (this.selected) await this.fetch(this.selected.info.id)
    } catch (err) {
      this.error = errorText(err)
    } finally {
      this.loading = false
    }
  }

  async select(id: string) {
    if (!this.chats.some(c => c.info.id === id)) return
    this.selectedId = id
    const c = this.selected!
    if (!c.loaded) await this.fetch(id)
  }

  private async fetch(id: string) {
    const c = this.chats.find(x => x.info.id === id)
    if (!c || c.streamingId) return
    try {
      const { chat, messages } = await this.api.get(this.sessionId, id)
      const now = this.chats.find(x => x.info.id === id)
      if (!now || now.streamingId) return
      now.info = chat
      now.messages = messages
      now.loaded = true
    } catch (err) {
      c.error = errorText(err)
    }
  }

  // A new side chat with the main session's model and effort (and a quote to ask about), selected.
  async newChat(quote = '') {
    this.error = ''
    try {
      const d = this.defaults()
      const info = await this.api.create(this.sessionId, { model: d.model || undefined, effort: d.effort })
      const v = this.view(info)
      v.loaded = true
      v.quote = quote
      this.chats.push(v)
      this.selectedId = info.id
      return this.selected
    } catch (err) {
      this.error = errorText(err)
      return undefined
    }
  }

  // Sends the selected chat's draft (with its quote); creates a chat first when there is none.
  async ask() {
    let c = this.selected
    if (!c) {
      c = await this.newChat()
      if (!c) return
    }
    const text = c.draft.trim()
    if (!text || c.streamingId) return
    const id = c.info.id
    const quote = c.quote
    c.draft = ''
    c.quote = ''
    c.error = ''
    c.streamingId = STREAMING_ID
    const chat = () => this.chats.find(x => x.info.id === id)
    const streamingMsg = () => chat()?.messages.find(m => m.id === STREAMING_ID)
    const handle = this.api.ask(this.sessionId, id, { text, quote: quote || undefined }, {
      onQuestion: (m) => {
        const v = chat()
        if (!v) return
        v.messages.push(m, { id: STREAMING_ID, role: 'assistant', text: '', createdAt: m.createdAt, model: v.info.model, effort: v.info.effort })
      },
      onDelta: (t) => {
        const m = streamingMsg()
        if (m) m.text += t
      },
      onAnswer: (m, info) => {
        const v = chat()
        if (!v) return
        const i = v.messages.findIndex(x => x.id === STREAMING_ID)
        if (i >= 0) v.messages.splice(i, 1, m)
        else v.messages.push(m)
        if (info) v.info = info
        v.streamingId = ''
        if (m.error) v.error = m.error
      }
    })
    this.handles.set(id, handle)
    try {
      await handle.done
    } catch (err) {
      const v = chat()
      if (v) {
        v.error = errorText(err)
        // The question never arrived: give the text back.
        if (!v.messages.some(m => m.id === STREAMING_ID)) {
          v.draft = text
          v.quote = quote
        }
      }
    } finally {
      this.handles.delete(id)
      const v = chat()
      if (v?.streamingId) {
        // The stream ended without an answer (cancelled or failed): keep what arrived.
        const i = v.messages.findIndex(x => x.id === STREAMING_ID)
        if (i >= 0) {
          const m = v.messages[i]!
          if (m.text) v.messages.splice(i, 1, { ...m, id: `local-${Date.now()}`, stopped: true })
          else v.messages.splice(i, 1)
        }
        v.streamingId = ''
      }
    }
  }

  // Stops the selected chat's answer; the text so far stays.
  async stop() {
    const c = this.selected
    if (!c?.streamingId) return
    const id = c.info.id
    try {
      const stopped = await this.api.stop(this.sessionId, id)
      if (!stopped) this.handles.get(id)?.cancel()
    } catch {
      this.handles.get(id)?.cancel()
    }
  }

  // Changes the selected chat's model (effort refitted to its levels); remembered for this chat only.
  async setModel(model: string) {
    const c = this.selected
    if (!c || model === c.info.model) return
    const m = this.models().find(x => x.id === model)
    const effort = fitEffort(c.info.effort ?? this.defaults().effort ?? 'medium', modelEfforts(m))
    await this.patch(c, { model, effort })
  }

  async setEffort(effort: EffortName) {
    const c = this.selected
    if (!c || effort === c.info.effort) return
    await this.patch(c, { effort })
  }

  private async patch(c: SideChatView, patch: { model?: string, effort?: EffortName }) {
    const id = c.info.id
    const before = c.info
    c.info = { ...c.info, ...patch }
    try {
      const info = await this.api.update(this.sessionId, id, patch)
      const v = this.chats.find(x => x.info.id === id)
      if (v) v.info = { ...info, answering: v.info.answering }
    } catch (err) {
      const v = this.chats.find(x => x.info.id === id)
      if (v) {
        v.info = before
        v.error = errorText(err)
      }
    }
  }

  // Deletes a side chat (stopping its answer) and selects its neighbour.
  async close(id: string) {
    const i = this.chats.findIndex(c => c.info.id === id)
    if (i < 0) return
    this.handles.get(id)?.cancel()
    try {
      await this.api.remove(this.sessionId, id)
    } catch (err) {
      this.error = errorText(err)
      return
    }
    const at = this.chats.findIndex(c => c.info.id === id)
    if (at >= 0) this.chats.splice(at, 1)
    if (this.selectedId === id) {
      const next = this.chats[Math.max(0, at - 1)]
      this.selectedId = ''
      if (next) await this.select(next.info.id)
    }
  }
}
