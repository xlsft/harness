import { describe, expect, it } from 'vitest'
import { SideChatController, sideChatDefaults } from '~/lib/sidechat/controller'
import { MockSideChatsApi, mockAnswer } from '~/lib/sidechat/mock'
import { sideChatView, sideMessageView } from '~/lib/sidechat/connect'
import type { SideChatsApi } from '~/lib/sidechat/types'
import { MockTransport } from '~/lib/transport/mock'
import { Effort } from '~/gen/harness/llm/v1/content_pb'
import type { EffortName } from '~/lib/session/types'

const tick = (ms = 0) => new Promise(r => setTimeout(r, ms))

const MODELS: { id: string, efforts?: EffortName[] }[] = [
  { id: 'main-model', efforts: ['low', 'medium', 'high', 'xhigh'] },
  { id: 'small-model', efforts: ['low', 'medium'] },
  { id: 'no-effort', efforts: [] }
]

function setup(opts: { chunkDelay?: number, effort?: EffortName, model?: string, api?: SideChatsApi } = {}) {
  const api = opts.api ?? new MockSideChatsApi({ chunkDelay: opts.chunkDelay ?? 0, chunkSize: 25 })
  const main = { model: opts.model ?? 'main-model', effort: opts.effort ?? 'high' as EffortName }
  const ctl = new SideChatController(api, 'sess-1', () => sideChatDefaults(main.model, main.effort, MODELS), () => MODELS)
  return { api, ctl, main }
}

describe('sideChatDefaults', () => {
  it('takes the main session model and its effort, fitted to the model', () => {
    expect(sideChatDefaults('main-model', 'xhigh', MODELS)).toEqual({ model: 'main-model', effort: 'xhigh' })
    expect(sideChatDefaults('small-model', 'xhigh', MODELS)).toEqual({ model: 'small-model', effort: 'medium' })
    expect(sideChatDefaults('main-model', undefined, MODELS)).toEqual({ model: 'main-model', effort: 'medium' })
    expect(sideChatDefaults('no-effort', 'high', MODELS)).toEqual({ model: 'no-effort', effort: undefined })
  })
})

describe('SideChatController', () => {
  it('starts a new chat with the main session model and effort; a change applies to that chat only', async () => {
    const { ctl, api, main } = setup()
    await ctl.load()
    expect(ctl.chats).toEqual([])
    const a = await ctl.newChat()
    expect(a?.info).toMatchObject({ model: 'main-model', effort: 'high', title: 'Side chat' })

    await ctl.setModel('small-model')
    // Effort refitted to the new model's levels, remembered by the API.
    expect(ctl.selected?.info).toMatchObject({ model: 'small-model', effort: 'medium' })
    expect((await api.get('sess-1', a!.info.id)).chat).toMatchObject({ model: 'small-model', effort: 'medium' })
    await ctl.setEffort('low')
    expect((await api.get('sess-1', a!.info.id)).chat.effort).toBe('low')

    // The main session changed meanwhile: a new chat follows it, the old one keeps its own.
    main.model = 'main-model'
    main.effort = 'xhigh'
    const b = await ctl.newChat()
    expect(b?.info).toMatchObject({ model: 'main-model', effort: 'xhigh' })
    expect(ctl.chats.find(c => c.info.id === a!.info.id)?.info.model).toBe('small-model')

    // A reload (a fresh controller) sees both, with their models.
    const again = setup({ api })
    await again.ctl.load()
    expect(again.ctl.chats.map(c => c.info.model)).toEqual(['small-model', 'main-model'])
    expect(again.ctl.selectedId).toBe(b!.info.id)
  })

  it('asks with the quote and streams the answer into the chat', async () => {
    const { ctl } = setup({ chunkDelay: 1 })
    await ctl.load()
    const c = (await ctl.newChat('…ConnectRPC stays gRPC-compatible.'))!
    expect(c.quote).toBe('…ConnectRPC stays gRPC-compatible.')
    c.draft = 'Why not plain gRPC?'
    const asking = ctl.ask()
    // The draft and the quote are taken at once.
    expect(c.draft).toBe('')
    expect(c.quote).toBe('')
    expect(ctl.streaming).toBe(true)
    await tick(5)
    const live = c.messages.find(m => m.id === c.streamingId)
    expect(c.messages[0]).toMatchObject({ role: 'user', text: 'Why not plain gRPC?', quote: '…ConnectRPC stays gRPC-compatible.' })
    expect(live?.role).toBe('assistant')
    const full = mockAnswer('Why not plain gRPC?', '…ConnectRPC stays gRPC-compatible.')
    expect(full.startsWith(live!.text)).toBe(true)
    await asking
    expect(ctl.streaming).toBe(false)
    expect(c.messages).toHaveLength(2)
    expect(c.messages[1]).toMatchObject({ role: 'assistant', text: full, model: 'main-model', effort: 'high' })
    expect(c.messages[1]!.usage?.outputTokens).toBeGreaterThan(0)
    // The first question names the chat.
    expect(c.info).toMatchObject({ title: 'Why not plain gRPC?', messageCount: 2 })
  })

  it('stops a streaming answer and keeps what arrived', async () => {
    const { ctl, api } = setup({ chunkDelay: 5 })
    await ctl.load()
    const c = (await ctl.newChat())!
    c.draft = 'Explain the relay'
    const asking = ctl.ask()
    await tick(20)
    await ctl.stop()
    await asking
    const answer = c.messages[1]!
    expect(answer.stopped).toBe(true)
    expect(answer.text.length).toBeGreaterThan(0)
    expect(answer.text.length).toBeLessThan(mockAnswer('Explain the relay').length)
    expect(c.streamingId).toBe('')
    // Stored the same way.
    expect((await api.get('sess-1', c.info.id)).messages[1]).toMatchObject({ stopped: true, text: answer.text })
  })

  it('creates a chat on the first question when there is none, and closes chats', async () => {
    const { ctl } = setup()
    await ctl.load()
    await ctl.ask() // nothing typed: no chat
    expect(ctl.chats).toHaveLength(1)
    ctl.selected!.draft = 'hello'
    await ctl.ask()
    expect(ctl.selected!.messages.map(m => m.role)).toEqual(['user', 'assistant'])
    const first = ctl.selectedId
    await ctl.newChat()
    await ctl.close(ctl.selectedId)
    expect(ctl.chats).toHaveLength(1)
    expect(ctl.selectedId).toBe(first)
    await ctl.close(first)
    expect(ctl.chats).toEqual([])
    expect(ctl.selectedId).toBe('')
  })

  it('gives the question back when asking fails before it is stored', async () => {
    const api = new MockSideChatsApi({ chunkDelay: 0 })
    const { ctl } = setup({ api })
    await ctl.load()
    const c = (await ctl.newChat('a quote'))!
    await api.remove('sess-1', c.info.id) // gone behind the pane's back
    c.draft = 'still there?'
    await ctl.ask()
    expect(c.error).toMatch(/not found/)
    expect(c.draft).toBe('still there?')
    expect(c.quote).toBe('a quote')
  })
})

describe('MockTransport side chats', () => {
  it('defaults to the session model and streams answers', async () => {
    const t = new MockTransport()
    const [first] = await t.listSessions()
    const api = t.sideChats()
    const chat = await api.create(first!.id)
    expect(chat.model).toBe(first!.model)
    const deltas: string[] = []
    let answered = ''
    const h = api.ask(first!.id, chat.id, { text: 'What is this session about?' }, {
      onDelta: d => deltas.push(d),
      onAnswer: m => (answered = m.text)
    })
    await h.done
    expect(deltas.length).toBeGreaterThan(3)
    expect(answered).toBe(deltas.join(''))
    expect(await api.list(first!.id)).toHaveLength(1)
  }, 20_000)
})

describe('connect views', () => {
  it('maps side chats and messages from the protobuf shape', () => {
    expect(sideChatView({ sideChatId: 'sc_1', sessionId: 's', title: 'T', model: 'm', effort: Effort.XHIGH, createdAt: 'c', updatedAt: 'u', messageCount: 2, answering: false } as never))
      .toEqual({ id: 'sc_1', sessionId: 's', title: 'T', model: 'm', effort: 'xhigh', createdAt: 'c', updatedAt: 'u', messageCount: 2, answering: false })
    expect(sideMessageView({ messageId: 'm1', role: 'assistant', text: 'hi', quote: '', model: 'm', effort: Effort.UNSPECIFIED, createdAt: 'c', usage: { inputTokens: 10n, outputTokens: 2n, cacheReadInputTokens: 0n, cacheCreationInputTokens: 0n }, stopped: true, error: '' } as never))
      .toEqual({ id: 'm1', role: 'assistant', text: 'hi', quote: undefined, model: 'm', effort: undefined, usage: { inputTokens: 10, outputTokens: 2, cacheReadInputTokens: 0, cacheCreationInputTokens: 0 }, createdAt: 'c', stopped: true, error: undefined })
  })
})
