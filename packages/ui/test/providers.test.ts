import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ClassifierSchema, ProviderScope, ProviderSchema } from '~/gen/harness/llm/v1/providers_pb'
import { classifierView, providerView } from '~/lib/providers/connect'
import { MockProvidersApi } from '~/lib/providers/mock'

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
    expect(v.models).toEqual([{ id: 'u-abc/qwen/qwen3.7-flash', name: 'u-abc/qwen/qwen3.7-flash', upstream: 'qwen/qwen3.7-flash', contextWindow: 1_000_000 }])
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
})
