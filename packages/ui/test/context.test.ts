import { create } from '@bufbuild/protobuf'
import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import { Compacted_Trigger, ContextUsage_Category, ContextUsageSchema, SessionEventSchema } from '~/gen/harness/agent/v1/agent_pb'
import { categorySwatches, contextUsageFromProto, mcpToolLabel, percentOf } from '~/lib/session/context'
import { createSessionState, reduce } from '~/lib/session/reducer'
import type { CompactItem } from '~/lib/session/types'
import { MockTransport } from '~/lib/transport/mock'
import { useSessionStore } from '~/stores/sessions'
import { compactCommand } from '~/utils/composer'

vi.mock('vue', () => ({ markRaw: <T>(v: T) => v, toRaw: <T>(v: T) => v }))

describe('context usage mapping', () => {
  const pb = create(ContextUsageSchema, {
    model: 'm',
    contextWindow: 1_000_000n,
    usedTokens: 698_200n,
    categories: [
      { category: ContextUsage_Category.MESSAGES, tokens: 640_000n },
      { category: ContextUsage_Category.MCP_TOOLS, tokens: 30_000n },
      { category: ContextUsage_Category.SYSTEM_TOOLS, tokens: 20_000n },
      { category: ContextUsage_Category.SYSTEM_PROMPT, tokens: 8_200n },
      { category: ContextUsage_Category.MCP_SERVER_INSTRUCTIONS, tokens: 0n },
      { category: ContextUsage_Category.AUTOCOMPACT_BUFFER, tokens: 80_000n },
      { category: ContextUsage_Category.FREE_SPACE, tokens: 221_800n },
      // Unknown categories (a newer client) are left out.
      { category: 99 as ContextUsage_Category, tokens: 5n }
    ],
    deferredMcpToolsTokens: 4_000n,
    mcpTools: [
      { server: 'youtrack', tool: 'search', name: 'mcp__youtrack__search', description: 'Search issues\nwith a query', tokens: 900n },
      { server: 'figma', tool: 'shot', name: 'mcp__figma__shot', description: '', tokens: 4_000n, deferred: true }
    ],
    autocompactThreshold: 920_000n,
    tokensUntilAutocompact: 221_800n,
    estimated: true
  })

  it('turns the proto into plain numbers and labelled categories in display order', () => {
    const u = contextUsageFromProto(pb)
    expect(u).toMatchObject({ model: 'm', window: 1_000_000, used: 698_200, deferredMcpTools: 4_000, deferredSystemTools: 0, autocompactThreshold: 920_000, untilAutocompact: 221_800, estimated: true, compacting: false })
    expect(u.categories.map(c => [c.key, c.label, c.tokens])).toEqual([
      ['messages', 'Messages', 640_000],
      ['mcp_tools', 'MCP tools', 30_000],
      ['system_tools', 'System tools', 20_000],
      ['system_prompt', 'System prompt', 8_200],
      ['mcp_instructions', 'MCP server instructions', 0],
      ['buffer', 'Autocompact buffer', 80_000],
      ['free', 'Free space', 221_800]
    ])
    expect(u.categories.reduce((n, c) => n + c.tokens, 0)).toBe(u.window)
    expect(u.mcpTools[1]).toEqual({ server: 'figma', tool: 'shot', name: 'mcp__figma__shot', description: '', tokens: 4_000, deferred: true })
  })

  it('colours content categories with the chart tokens in order and the buffer and free space neutrally', () => {
    const u = contextUsageFromProto(pb)
    expect(categorySwatches(u.categories)).toEqual(['bg-chart-1', 'bg-chart-2', 'bg-chart-3', 'bg-chart-4', 'bg-chart-5', 'bg-dimmed', 'bg-raised'])
  })

  it('labels MCP tools "Server · description" and rounds percentages', () => {
    const u = contextUsageFromProto(pb)
    expect(mcpToolLabel(u.mcpTools[0]!)).toBe('youtrack · Search issues')
    expect(mcpToolLabel(u.mcpTools[1]!)).toBe('figma · shot')
    expect(percentOf(698_200, 1_000_000)).toBe(70)
    expect(percentOf(5, 0)).toBe(0)
  })
})

describe('compaction', () => {
  it('maps the idle trigger of a Compacted event to the transcript card', () => {
    const s = createSessionState('s1')
    reduce(s, create(SessionEventSchema, { seq: 1n, sessionId: 's1', event: { case: 'compacted', value: { trigger: Compacted_Trigger.IDLE, tokensBefore: 120_000n, tokensAfter: 9_000n } } }))
    reduce(s, create(SessionEventSchema, { seq: 2n, sessionId: 's1', event: { case: 'compacted', value: { trigger: Compacted_Trigger.AUTO, tokensBefore: 9_000n, tokensAfter: 8_000n } } }))
    const cards = s.items.filter((i): i is CompactItem => i.kind === 'compact')
    expect(cards.map(c => [c.trigger, c.tokensBefore, c.tokensAfter])).toEqual([['idle', 120_000, 9_000], ['auto', 9_000, 8_000]])
    expect(s.contextTokens).toBe(8_000)
  })

  it('recognizes /compact with and without focus instructions', () => {
    expect(compactCommand('/compact')).toBe('')
    expect(compactCommand('  /compact keep the API notes ')).toBe('keep the API notes')
    expect(compactCommand('/compact\nfocus')).toBe('focus')
    expect(compactCommand('/compactor')).toBeNull()
    expect(compactCommand('please /compact')).toBeNull()
  })

  it('marks the session compacting until the call returns, and drops a second request meanwhile', async () => {
    setActivePinia(createPinia())
    const store = useSessionStore()
    const transport = new MockTransport({ tick: 0 })
    const calls: string[] = []
    let release!: () => void
    transport.compact = async (id, instructions) => {
      calls.push(`${id}:${instructions}`)
      await new Promise<void>(r => (release = r))
    }
    store.transport = transport
    const id = (await transport.listSessions())[0]!.id
    const run = store.compact(id, 'focus')
    expect(store.compacting[id]).toBe(true)
    await store.compact(id)
    expect(calls).toEqual([`${id}:focus`])
    release()
    await run
    expect(store.compacting[id]).toBe(false)
  })

  it('the mock breakdown shrinks after a compaction', async () => {
    setActivePinia(createPinia())
    const store = useSessionStore()
    const transport = new MockTransport({ tick: 0 })
    store.transport = transport
    const id = (await transport.listSessions())[0]!.id
    const before = await store.contextUsage(id)
    await store.compact(id)
    const after = await store.contextUsage(id)
    expect(after.used).toBeLessThan(before.used)
    expect(after.categories.reduce((n, c) => n + c.tokens, 0)).toBe(after.window)
  })
})

describe('user preferences', () => {
  it('updates only the fields given', async () => {
    const transport = new MockTransport({ tick: 0 })
    expect(await transport.getUserPreferences()).toEqual({ compactIdleSessions: true, prMonitorWakesPerHour: 30, subagentMaxDepth: 2, subagentMaxRunning: 8 })
    expect(await transport.updateUserPreferences({ compactIdleSessions: false })).toEqual({ compactIdleSessions: false, prMonitorWakesPerHour: 30, subagentMaxDepth: 2, subagentMaxRunning: 8 })
    expect(await transport.updateUserPreferences({})).toEqual({ compactIdleSessions: false, prMonitorWakesPerHour: 30, subagentMaxDepth: 2, subagentMaxRunning: 8 })
  })
})
