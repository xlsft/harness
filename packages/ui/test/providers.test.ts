import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { SlotsSchema } from '~/gen/harness/llm/v1/gateway_pb'
import { ClassifierChoice, ClassifierSchema, GetMyClassifierResponseSchema, ProviderPresetSchema, ProviderScope, ProviderSchema } from '~/gen/harness/llm/v1/providers_pb'
import { classifierView, myClassifierView, presetView, providerView, slotsView } from '~/lib/providers/connect'
import { MockProvidersApi } from '~/lib/providers/mock'
import { contextLabel, parseHeaders, slotModelOptions } from '~/lib/providers/types'

describe('providerView', () => {
  it('maps a personal provider and its models, falling back to the id for names', () => {
    const v = providerView(create(ProviderSchema, {
      id: 'u-abc',
      scope: ProviderScope.PERSONAL,
      type: 'openai-chat',
      baseUrl: 'https://openrouter.ai/api/v1',
      apiKeySet: true,
      apiKeyHint: '…1234',
      enabled: true,
      editable: true,
      models: [{ id: 'u-abc/qwen/qwen3.7-flash', upstreamModel: 'qwen/qwen3.7-flash', contextWindow: 1_000_000n }]
    }))
    expect(v).toMatchObject({ id: 'u-abc', scope: 'personal', name: 'u-abc', keySet: true, keyHint: '…1234', editable: true })
    expect(v.models).toEqual([{ id: 'u-abc/qwen/qwen3.7-flash', name: 'u-abc/qwen/qwen3.7-flash', upstream: 'qwen/qwen3.7-flash', contextWindow: 1_000_000, efforts: [] }])
  })

  it('maps the group and preset, defaulting to an LLM custom provider', () => {
    const v = providerView(create(ProviderSchema, { id: 'typesafe', scope: ProviderScope.GLOBAL, group: 'classifier', presetId: 'typesafe', headerNames: ['X-A'] }))
    expect(v).toMatchObject({ group: 'classifier', presetId: 'typesafe', headerNames: ['X-A'] })
    expect(providerView(create(ProviderSchema, { id: 'old' }))).toMatchObject({ group: 'llm', presetId: '' })
  })

  it('maps a preset, with a monogram fallback', () => {
    const v = presetView(create(ProviderPresetSchema, {
      id: 'kimi-code',
      group: 'llm',
      displayName: 'Kimi Code',
      type: 'anthropic',
      baseUrl: 'https://api.kimi.com/coding',
      fields: [{ name: 'api_key', label: 'API key', required: true }],
      defaultModels: [{ upstreamModel: 'kimi-for-coding', contextWindow: 256_000n }]
    }))
    expect(v).toMatchObject({ id: 'kimi-code', icon: 'KI', discoversModels: false, local: false })
    expect(v.defaultModels).toEqual([{ upstream: 'kimi-for-coding', name: 'kimi-for-coding', contextWindow: 256_000 }])
  })

  it('maps the classifier with defaults when the server sends none', () => {
    expect(classifierView(undefined)).toMatchObject({ enabled: false, minProbability: 0.8, minMargin: 0.15, onUncertain: 'next' })
    expect(classifierView(create(ClassifierSchema, { enabled: true, providerId: 'router', onUncertain: 'ask', minProbability: 0.9 }))).toMatchObject({
      enabled: true,
      providerId: 'router',
      onUncertain: 'ask',
      minProbability: 0.9
    })
  })

  it('maps the caller\'s classifier override, following the default when unset', () => {
    expect(myClassifierView(create(GetMyClassifierResponseSchema, { defaultProviderId: 'typesafe' }))).toEqual({
      mine: { choice: 'default', providerId: '', llmModel: '' },
      defaultProviderId: 'typesafe',
      defaultLlmModel: ''
    })
    const r = create(GetMyClassifierResponseSchema, { mine: { choice: ClassifierChoice.PROVIDER, providerId: 'policy', llmModel: 'haiku' }, defaultLlmModel: 'opus' })
    expect(myClassifierView(r)).toMatchObject({ mine: { choice: 'provider', providerId: 'policy', llmModel: 'haiku' }, defaultLlmModel: 'opus' })
    expect(myClassifierView(create(GetMyClassifierResponseSchema, { mine: { choice: ClassifierChoice.NONE, providerId: 'stray' } })).mine).toEqual({ choice: 'none', providerId: '', llmModel: '' })
  })

  it('maps the slots, empty when the server sends none', () => {
    expect(slotsView(undefined)).toEqual({ main: '', small: '', plan: '', title: '' })
    expect(slotsView(create(SlotsSchema, { main: 'opus', title: 'haiku' }))).toEqual({ main: 'opus', small: '', plan: '', title: 'haiku' })
  })
})

describe('MockProvidersApi', () => {
  it('keeps keys write-only and qualifies personal model ids', async () => {
    const api = new MockProvidersApi(false)
    const p = await api.create({ scope: 'personal', type: 'anthropic', name: 'Mine', baseUrl: 'https://api.anthropic.com/', apiKey: 'sk-fake-key-123456' })
    expect(p).toMatchObject({ scope: 'personal', keySet: true, keyHint: '…3456', baseUrl: 'https://api.anthropic.com' })
    expect(JSON.stringify(await api.list())).not.toContain('sk-fake-key-123456')
    await api.addModel(p.id, { upstream: 'claude-x' })
    expect((await api.list()).providers.find(x => x.id === p.id)!.models[0]!.id).toBe(`${p.id}/claude-x`)
  })

  it('refuses global changes and the classifier to non-admins', async () => {
    const api = new MockProvidersApi(false)
    await expect(api.create({ scope: 'global', id: 'g', type: 'anthropic', name: '', baseUrl: 'https://x' })).rejects.toThrow()
    await expect(api.update('openrouter', { name: 'x' })).rejects.toThrow()
    await expect(api.getClassifier()).rejects.toThrow()
    expect((await api.list()).canManageGlobal).toBe(false)
  })

  it('connects a preset with only its key and offers its curated models', async () => {
    const api = new MockProvidersApi(false)
    await expect(api.create({ scope: 'personal', presetId: 'kimi-code' })).rejects.toThrow(/API key/)
    const p = await api.create({ scope: 'personal', presetId: 'kimi-code', apiKey: 'sk-fake-key-123456', baseUrl: 'https://elsewhere.example' })
    expect(p).toMatchObject({ presetId: 'kimi-code', group: 'llm', name: 'Kimi Code', baseUrl: 'https://api.kimi.com/coding', type: 'anthropic' })
    expect((await api.discover(p.id)).map(c => c.upstream)).toContain('kimi-for-coding')
    expect(await api.test(p.id)).toMatchObject({ ok: true })
  })

  it('keeps classifier providers server-wide and lets custom providers be many', async () => {
    const user = new MockProvidersApi(false)
    await expect(user.create({ scope: 'personal', presetId: 'typesafe', apiKey: 'sk-fake-key-123456' })).rejects.toThrow(/server-wide/)
    const admin = new MockProvidersApi(true)
    const a = await admin.create({ scope: 'global', group: 'llm', type: 'openai-chat', name: 'Team A', baseUrl: 'https://a.example/v1' })
    const b = await admin.create({ scope: 'global', group: 'llm', type: 'openai-chat', name: 'Team A', baseUrl: 'https://b.example/v1' })
    expect([a.id, b.id]).toEqual(['team-a', 'team-a-2'])
    const t = await admin.create({ scope: 'global', presetId: 'typesafe', apiKey: 'sk-fake-key-123456' })
    await expect(admin.addModel(t.id, { upstream: 'jev-1.13.0' })).rejects.toThrow()
    const c = await admin.setClassifier({ ...(await admin.getClassifier()), enabled: true, providerId: t.id, model: '' })
    expect(c.model).toBe('jev-1.13.0')
  })

  it('lets everyone override the classifier for themselves over the admin default', async () => {
    const user = new MockProvidersApi(false)
    expect(await user.getMyClassifier()).toEqual({ mine: { choice: 'default', providerId: '', llmModel: '' }, defaultProviderId: 'openrouter-decisions', defaultLlmModel: '' })
    await expect(user.setMyClassifier({ choice: 'provider', providerId: 'openrouter', llmModel: '' })).rejects.toThrow(/Unknown classifier/)
    await expect(user.setMyClassifier({ choice: 'default', providerId: '', llmModel: 'u-x/model' })).rejects.toThrow(/Unknown model/)
    expect((await user.setMyClassifier({ choice: 'none', providerId: 'openrouter-decisions', llmModel: 'claude-haiku' })).mine).toEqual({ choice: 'none', providerId: '', llmModel: 'claude-haiku' })
    const admin = new MockProvidersApi(true)
    await admin.setClassifier({ ...(await admin.getClassifier()), llmModel: 'claude-haiku' })
    expect((await admin.getMyClassifier()).defaultLlmModel).toBe('claude-haiku')
  })

  it('lets everyone read the slots and admins set them to global models', async () => {
    const user = new MockProvidersApi(false)
    expect(await user.getSlots()).toEqual({ slots: { main: '', small: '', plan: '', title: '' }, editable: false, defaults: { main: 'claude-haiku', small: 'claude-haiku', plan: 'claude-haiku', title: 'claude-haiku' } })
    await expect(user.setSlots({ main: '', small: '', plan: '', title: 'claude-haiku' })).rejects.toThrow(/Admin/)
    const admin = new MockProvidersApi(true)
    expect((await admin.getSlots()).editable).toBe(true)
    await expect(admin.setSlots({ main: '', small: '', plan: '', title: 'u-x/model' })).rejects.toThrow(/Unknown model/)
    expect(await admin.setSlots({ main: '', small: '', plan: '', title: 'claude-haiku' })).toMatchObject({ title: 'claude-haiku' })
    expect((await admin.getSlots()).slots.title).toBe('claude-haiku')
  })
})

describe('helpers', () => {
  it('parses header lines and rejects malformed ones', () => {
    expect(parseHeaders('X-Team: core\n\nX-Env:prod ')).toEqual({ 'X-Team': 'core', 'X-Env': 'prod' })
    expect(parseHeaders('no colon here')).toMatch(/Not a header/)
  })

  it('labels context windows', () => {
    expect([contextLabel(0), contextLabel(256_000), contextLabel(1_000_000)]).toEqual(['', '256k', '1M'])
  })

  it('offers global LLM models only as slot choices, grouped by provider', async () => {
    const api = new MockProvidersApi(true)
    await api.create({ scope: 'personal', type: 'anthropic', name: 'Mine', baseUrl: 'https://api.anthropic.com', apiKey: 'sk-fake-key-123456' })
    const mine = (await api.list()).providers.find(p => p.scope === 'personal')!
    await api.addModel(mine.id, { upstream: 'claude-x' })
    expect(slotModelOptions((await api.list()).providers)).toEqual([{ value: 'claude-haiku', label: 'Claude Haiku 5.5', hint: '200k', group: 'OpenRouter' }])
  })
})
