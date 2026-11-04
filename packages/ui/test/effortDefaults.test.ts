import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AcpAgentSchema } from '~/gen/harness/agent/v1/acp_pb'
import { Effort } from '~/gen/harness/llm/v1/content_pb'
import { ProviderSchema } from '~/gen/harness/llm/v1/providers_pb'
import { newSessionAgentEffort, sessionEffort } from '~/lib/acp/modelChoices'
import { MockAcpApi } from '~/lib/acp/mock'
import { providerView } from '~/lib/providers/connect'
import { MockProvidersApi } from '~/lib/providers/mock'
import { MODELS } from '~/lib/transport/scenario'
import { effortSliderLabel, harnessEffortLevels, startEffort } from '~/utils/composer'

const levels = ['low', 'medium', 'high'] as const

describe('the level a new session starts on', () => {
  const start = (c: Omit<Parameters<typeof startEffort>[0], 'levels'>, l: Parameters<typeof startEffort>[0]['levels'] = [...levels]) => startEffort({ ...c, levels: l })

  it('is the user\'s own for the model first', () => {
    expect(start({ own: 'low', recommended: 'high', fallback: 'medium' })).toBe('low')
  })
  it('then the recommended level, over what was chosen on other models', () => {
    expect(start({ recommended: 'high', fallback: 'low' })).toBe('high')
  })
  it('then the fallback: the general choice, else medium, or the nearest', () => {
    expect(start({ fallback: 'low' })).toBe('low')
    expect(start({})).toBe('medium')
    expect(start({}, ['low', 'max'])).toBe('low')
    expect(start({ fallback: 'max' })).toBe('high')
  })
  it('fits the user\'s own to what the model still offers', () => {
    expect(start({ own: 'max', recommended: 'low' })).toBe('high')
  })
  it('ignores a recommended level the model does not offer', () => {
    expect(start({ recommended: 'max', fallback: 'low' })).toBe('low')
  })
  it('is nothing for a model without levels', () => {
    expect(start({ own: 'low', recommended: 'high' }, [])).toBeUndefined()
  })
})

describe('the slider\'s label', () => {
  const l = harnessEffortLevels(['low', 'medium', 'high'])
  it('says so only on the recommended level', () => {
    expect(effortSliderLabel(l, 'high', 'high')).toEqual({ label: 'High', recommended: true })
    expect(effortSliderLabel(l, 'low', 'high')).toEqual({ label: 'Low', recommended: false })
  })
  it('has nothing to add without a recommendation', () => {
    expect(effortSliderLabel(l, 'high')).toEqual({ label: 'High', recommended: false })
    expect(effortSliderLabel(l, 'high', 'max')).toEqual({ label: 'High', recommended: false })
  })
  it('shows a level it does not know as is', () => {
    expect(effortSliderLabel(l, 'gone', 'gone')).toEqual({ label: 'gone', recommended: false })
  })
  it('works with an agent\'s own levels', () => {
    expect(effortSliderLabel([{ id: 'deep', label: 'Think deeply' }], 'deep', 'deep')).toEqual({ label: 'Think deeply', recommended: true })
  })
})

describe('a recommended level on an agent', () => {
  const agent = (o: { default?: string, current?: string }) => create(AcpAgentSchema, {
    efforts: [{ id: 'a', name: 'A' }, { id: 'b', name: 'B' }],
    defaultEffortId: o.default ?? '',
    currentEffortId: o.current ?? ''
  })
  it('is what a new session starts on, unless one was chosen here', () => {
    // The current level can be a level a session chose: the default is what the agent starts on.
    expect(newSessionAgentEffort(agent({ default: 'a', current: 'b' }), '')).toMatchObject({ current: 'a', recommended: 'a' })
    expect(newSessionAgentEffort(agent({ default: 'a', current: 'b' }), 'b')).toMatchObject({ current: 'b', recommended: 'a' })
    expect(newSessionAgentEffort(agent({ default: 'a' }), 'gone').current).toBe('a')
  })
  it('is not invented from the current level', () => {
    const e = newSessionAgentEffort(agent({ current: 'b' }), '')
    expect(e.current).toBe('b')
    expect(e.recommended).toBeUndefined()
  })
  it('only marks a level in a running session', () => {
    const acp = { efforts: [{ id: 'a', name: 'A' }, { id: 'b', name: 'B' }], currentEffortId: 'b', defaultEffortId: 'a' } as never
    expect(sessionEffort({ onAgent: true, unsupported: false, acp, harness: [], chosen: 'medium' })).toMatchObject({ current: 'b', recommended: 'a' })
    expect(sessionEffort({ onAgent: false, unsupported: false, harness: ['low', 'high'], chosen: 'low', recommended: 'high' })).toMatchObject({ current: 'low', recommended: 'high' })
    expect(sessionEffort({ onAgent: true, unsupported: true, acp, harness: [], chosen: 'medium' })).toEqual({ levels: [] })
  })
})

describe('the mocks', () => {
  it('recommend a level on some model and agent', async () => {
    const m = MODELS.find(x => x.defaultEffort)
    expect(m?.efforts).toContain(m?.defaultEffort)
    expect(MODELS.some(x => !x.defaultEffort)).toBe(true)
    const { agents } = await new MockAcpApi({ now: () => 0 }).listAgents()
    expect(agents.find(a => a.id === 'codex-acp')?.defaultEffortId).toBe('balanced')
  })
  it('keep a recommended level by hand like the client does', async () => {
    const api = new MockProvidersApi()
    const { providers } = await api.list()
    const p = providers.find(x => x.models.some(m => m.defaultEffort))!
    const m = p.models.find(x => x.defaultEffort)!
    const stored = async () => (await api.list()).providers.find(x => x.id === p.id)!.models.find(x => x.id === m.id)!.defaultEffort
    await api.setModelEfforts(m.id, m.efforts, 'high')
    expect(await stored()).toBe('high')
    await expect(api.setModelEfforts(m.id, ['low'], 'high')).rejects.toThrow(/not one of/)
    await api.setModelEfforts(m.id, ['low', 'medium'])
    expect(await stored()).toBeUndefined()
    await api.setModelEfforts(m.id, ['low', 'medium'], 'low')
    await api.setModelEfforts(m.id, ['low', 'medium'], '')
    expect(await stored()).toBeUndefined()
  })
})

describe('providerView', () => {
  it('maps the recommended level', () => {
    const v = providerView(create(ProviderSchema, { id: 'p', models: [
      { id: 'a', efforts: [Effort.LOW, Effort.HIGH], defaultEffort: Effort.HIGH },
      { id: 'b', efforts: [Effort.LOW] }
    ] }))
    expect(v.models.map(m => m.defaultEffort)).toEqual(['high', undefined])
  })
})
