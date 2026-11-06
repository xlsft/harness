import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { SessionEventSchema, type SessionEvent } from '~/gen/harness/agent/v1/agent_pb'
import { McpListChanged_Kind } from '~/gen/harness/agent/v1/mcp_pb'
import { AuthState, ServerSchema } from '~/gen/harness/mcp/v1/mcp_pb'
import { MockMcpApi } from '~/lib/mcp/mock'
import {
  canSignIn,
  draftOf,
  emptyDraft,
  McpServerSource,
  McpServerState,
  needsDecision,
  parseMap,
  secretChangesOf,
  serverOf,
  sourcesFor,
  stateView,
  Transport
} from '~/lib/mcp/types'
import { createSessionState, reduce } from '~/lib/session/reducer'
import type { ToolItem } from '~/lib/session/types'

describe('the MCP form', () => {
  it('turns its fields into a server and back', () => {
    const d = { ...emptyDraft(), name: 'files', command: 'npx', args: '-y\nserver\n', env: 'A=1\nB=${secret:B}', toolTimeout: '30', maxOutputChars: '5000', allowedTools: 'read*, list', deniedTools: 'rm' }
    const s = serverOf(d)
    expect(s.transport).toBe(Transport.STDIO)
    expect(s.stdio).toMatchObject({ command: 'npx', args: ['-y', 'server'], env: { A: '1', B: '${secret:B}' } })
    expect(s.remote).toBeUndefined()
    expect(s.toolTimeoutMs).toBe(30_000n)
    expect(s.maxOutputChars).toBe(5000)
    expect(s.allowedTools).toEqual(['read*', 'list'])
    expect(s.deniedTools).toEqual(['rm'])
    const back = draftOf(create(ServerSchema, s))
    expect(back).toMatchObject({ name: 'files', args: '-y\nserver', env: 'A=1\nB=${secret:B}', toolTimeout: '30', maxOutputChars: '5000', allowedTools: 'read* list' })
  })

  it('builds a remote server, keeping what the form does not show', () => {
    const base = create(ServerSchema, { id: 'mcp_1', scope: 2, targetRoles: ['dev'], transport: Transport.SSE, remote: { url: 'https://x/sse', oauth: { authorizationServer: 'https://as' } }, version: 5n })
    const d = draftOf(base)
    expect(d.transport).toBe('sse')
    d.headers = 'Authorization: Bearer ${secret:T}\nX-A=1'
    d.oauthScopes = 'a b'
    const s = serverOf(d, base)
    expect(s.id).toBe('mcp_1')
    expect(s.targetRoles).toEqual(['dev'])
    expect(s.stdio).toBeUndefined()
    expect(s.remote).toMatchObject({ url: 'https://x/sse', headers: { 'Authorization': 'Bearer ${secret:T}', 'X-A': '1' }, oauth: { scopes: ['a', 'b'], authorizationServer: 'https://as' } })
  })

  it('never fills secret values in, and sends only changes', () => {
    const base = create(ServerSchema, { name: 'x', transport: Transport.STDIO, secrets: [{ name: 'T', hasValue: true }] })
    const d = draftOf(base)
    expect(d.secrets).toBe('')
    expect(secretChangesOf(d)).toEqual({ set: {}, remove: [] })
    d.secrets = 'NEW=value'
    d.removeSecrets = ['T']
    expect(secretChangesOf(d)).toEqual({ set: { NEW: 'value' }, remove: ['T'] })
  })

  it('refuses what it cannot read', () => {
    expect(() => parseMap('no separator', 'Environment')).toThrow(/NAME=value/)
    expect(() => serverOf({ ...emptyDraft(), name: 'x', toolTimeout: 'soon' })).toThrow(/time limit/)
  })

  it('offers only destinations a transport can have', () => {
    const values = (t: 'stdio' | 'http' | 'sse' | 'ws', org: boolean) => sourcesFor(t, org).map(o => o.value)
    expect(values('stdio', false)).toEqual([McpServerSource.DEVICE])
    expect(values('stdio', true)).toEqual([McpServerSource.DEVICE, McpServerSource.ORG])
    expect(values('http', false)).toEqual([McpServerSource.ACCOUNT, McpServerSource.DEVICE])
    expect(values('ws', false)).toEqual([McpServerSource.ACCOUNT])
  })
})

describe('what a row shows', () => {
  it('names the states', async () => {
    const { servers } = await new MockMcpApi().list('/repo')
    const by = Object.fromEntries(servers.map(e => [e.server!.name, e]))
    expect(stateView(by.files!)).toEqual({ label: 'Ready', tone: 'ok' })
    expect(stateView(by.docs!).label).toBe('Needs sign-in')
    expect(canSignIn(by.docs!)).toBe(true)
    expect(canSignIn(by.files!)).toBe(false)
    expect(by.lint!.state).toBe(McpServerState.NEEDS_TRUST)
    expect(canSignIn(by.lint!)).toBe(false)
    expect(needsDecision(by.lint!)).toBe(false)
  })
})

describe('the mock client', () => {
  it('keeps project servers behind the folder trust and an approval', async () => {
    const api = new MockMcpApi()
    expect((await api.list()).servers.some(e => e.source === McpServerSource.PROJECT)).toBe(false)
    let lint = (await api.list('/repo')).servers.find(e => e.server?.name === 'lint')!
    expect(lint.canEdit).toBe(false)
    await expect(api.setApproval(lint.server!.id, '/repo', true)).rejects.toThrow(/trust/)
    await api.setTrust('/repo', true)
    lint = (await api.list('/repo')).servers.find(e => e.server?.name === 'lint')!
    expect(needsDecision(lint)).toBe(true)
    lint = await api.setApproval(lint.server!.id, '/repo', true)
    expect(lint.state).toBe(McpServerState.READY)
  })

  it('adds, edits, toggles and removes', async () => {
    const api = new MockMcpApi()
    const e = await api.create(McpServerSource.DEVICE, serverOf({ ...emptyDraft(), name: 'mine', command: 'node' }), { set: { T: 'v' }, remove: [] })
    expect(e.server!.id).toMatch(/^mcl_/)
    expect(e.server!.secrets.map(s => s.name)).toEqual(['T'])
    expect(JSON.stringify(e.server!.secrets)).not.toContain('"v"')
    const off = await api.update({ ...e.server!, enabled: false }, { set: {}, remove: [] }, e.server!.version)
    expect(off.entry!.state).toBe(McpServerState.DISABLED)
    await api.remove(e.server!.id)
    expect((await api.list()).servers.some(x => x.server?.name === 'mine')).toBe(false)
    await expect(api.create(McpServerSource.ACCOUNT, serverOf({ ...emptyDraft(), name: 'bad', command: 'x' }), { set: {}, remove: [] })).rejects.toThrow(/stays on this device/)
    await expect(api.create(McpServerSource.DEVICE, serverOf({ ...emptyDraft(), name: 'bad name', command: 'x' }), { set: {}, remove: [] })).rejects.toThrow(/name must/)
  })

  it('reports a conflict', async () => {
    const api = new MockMcpApi()
    const { servers } = await api.list()
    const tracker = servers.find(e => e.server?.name === 'tracker')!
    const r = await api.update({ ...tracker.server! }, { set: {}, remove: [] }, 99n)
    expect(r.conflict?.server?.name).toBe('tracker')
    expect(r.entry).toBeUndefined()
  })

  it('signs in through a flow and out again', async () => {
    const api = new MockMcpApi()
    const flow = await api.startOAuth('mcp_docs')
    expect(flow.authorizationUrl).toContain(flow.flowId)
    await api.oauthFlow(flow.flowId)
    let docs = (await api.list()).servers.find(e => e.server?.id === 'mcp_docs')!
    expect(docs.state).toBe(McpServerState.READY)
    expect(docs.server!.auth!.state).toBe(AuthState.CONNECTED)
    await api.revokeAuth('mcp_docs')
    docs = (await api.list()).servers.find(e => e.server?.id === 'mcp_docs')!
    expect(docs.state).toBe(McpServerState.NEEDS_AUTH)
  })

  it('lists tools with their rules, the audit log for admins, commands and mentions', async () => {
    const api = new MockMcpApi()
    await api.setToolRule('tracker', 'search', 'deny')
    const insp = await api.inspect('mcp_tracker')
    expect(insp.tools.find(t => t.name === 'search')!.rule).toBe('deny')
    expect((await api.inspect('mcp_company')).tools.map(t => t.name)).not.toContain('delete')
    expect((await api.audit({ action: 'oauth_completed' })).entries).toHaveLength(1)
    api.admin = false
    await expect(api.audit({})).rejects.toThrow(/admins/)
    expect((await api.commands())[0]!.command).toBe('/mcp__tracker__summarize')
    expect(await api.resources('s', 'nothing')).toEqual([])
    expect((await api.resources('s', 'readme'))[0]!.mention).toBe('@tracker:res://readme')
  })
})

describe('session events', () => {
  let seq = 0n
  const ev = (event: SessionEvent['event']): SessionEvent => create(SessionEventSchema, { seq: ++seq, sessionId: 's', timestamp: new Date().toISOString(), event })

  it('shows the latest progress while the call runs', () => {
    let s = createSessionState('s')
    s = reduce(s, ev({ case: 'toolCallStarted', value: { toolUseId: 't1', name: 'mcp__x__slow', inputJson: '{}' } as never }))
    s = reduce(s, ev({ case: 'mcpToolProgress', value: { toolUseId: 't1', progress: 1, total: 4, message: 'one' } as never }))
    s = reduce(s, ev({ case: 'mcpToolProgress', value: { toolUseId: 't1', progress: 3, total: 4, message: 'three' } as never }))
    const tool = s.items.find(i => i.kind === 'tool') as ToolItem
    expect(tool.progress).toMatchObject({ progress: 3, total: 4, message: 'three' })
    s = reduce(s, ev({ case: 'toolCallFinished', value: { toolUseId: 't1', isError: false, content: [] } as never }))
    expect((s.items.find(i => i.kind === 'tool') as ToolItem).progress).toBeUndefined()
  })

  it('ignores progress for a call it does not know, and counts list changes', () => {
    let s = createSessionState('s')
    s = reduce(s, ev({ case: 'mcpToolProgress', value: { toolUseId: 'nope', progress: 1 } as never }))
    expect(s.items).toHaveLength(0)
    const before = s.mcpListVersion
    s = reduce(s, ev({ case: 'mcpListChanged', value: { server: 'x', kind: McpListChanged_Kind.PROMPTS } as never }))
    expect(s.mcpListVersion).toBe(before + 1)
  })
})
