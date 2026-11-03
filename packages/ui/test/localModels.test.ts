import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { LocalProviderKind, LocalProviderSchema } from '~/gen/harness/agent/v1/localmodels_pb'
import { isLocalModelId, localProviderView } from '~/lib/transport/localModels'
import { MockLocalModelsApi, MockTransport } from '~/lib/transport/mock'

describe('local models mock', () => {
  it('lists Ollama with two models, LM Studio not running and a custom endpoint', async () => {
    const api = new MockLocalModelsApi({ tick: 0 })
    const { providers, disabledByPolicy } = await api.list()
    expect(disabledByPolicy).toBe(false)
    const ollama = providers.find(p => p.id === 'ollama')!
    expect(ollama.reachable).toBe(true)
    expect(ollama.models.map(m => m.id)).toEqual(['local:ollama/llama3.2:3b', 'local:ollama/qwen3:8b'])
    const lms = providers.find(p => p.id === 'lmstudio')!
    expect(lms.reachable).toBe(false)
    expect(lms.error).toMatch(/not running/)
    const custom = providers.filter(p => !p.builtin)
    expect(custom).toHaveLength(1)
    expect(custom[0]!.hasApiKey).toBe(true)
  })

  it('pulls a model with progress, then deletes it', async () => {
    const api = new MockLocalModelsApi({ tick: 0 })
    const seen: string[] = []
    let last = 0
    await api.pullOllamaModel('phi4:14b', { onProgress: (p) => {
      seen.push(p.status)
      if (p.total) last = p.completed / p.total
    } }).done
    expect(seen[0]).toBe('pulling manifest')
    expect(seen.at(-1)).toBe('success')
    expect(last).toBe(1)
    let ollama = (await api.list()).providers.find(p => p.id === 'ollama')!
    expect(ollama.models.some(m => m.name === 'phi4:14b')).toBe(true)
    await api.deleteOllamaModel('phi4:14b')
    ollama = (await api.list()).providers.find(p => p.id === 'ollama')!
    expect(ollama.models.some(m => m.name === 'phi4:14b')).toBe(false)
    await expect(api.pullOllamaModel('missing:1b', {}).done).rejects.toThrow(/does not exist/)
    await expect(api.pullOllamaModel('x', {}, 'lmstudio').done).rejects.toThrow(/not an Ollama/)
  })

  it('adds, updates and removes providers; built-ins stay', async () => {
    const api = new MockLocalModelsApi({ tick: 0 })
    const p = await api.add({ baseUrl: 'http://192.168.1.30:1234/v1', name: 'Laptop', apiKey: 'k' })
    expect(p.reachable && p.hasApiKey && !p.builtin).toBe(true)
    expect((await api.update(p.id, { apiKey: '' })).hasApiKey).toBe(false)
    expect((await api.update('ollama', { enabled: false })).enabled).toBe(false)
    await expect(api.update('ollama', { name: 'x' })).rejects.toThrow()
    await expect(api.remove('ollama')).rejects.toThrow()
    await api.remove(p.id)
    expect((await api.list()).providers.some(x => x.id === p.id)).toBe(false)
    await expect(api.add({ baseUrl: 'ftp://x' })).rejects.toThrow()
  })

  it('hides models under the server policy', async () => {
    const api = new MockLocalModelsApi({ tick: 0, disabledByPolicy: true })
    const s = await api.list()
    expect(s.disabledByPolicy).toBe(true)
    expect(s.providers.every(p => p.models.length === 0)).toBe(true)
    expect(api.pickerModels()).toEqual([])
  })

  it('merges local models into the model picker', async () => {
    const t = new MockTransport({ tick: 0 })
    const models = await t.listModels()
    const local = models.filter(m => m.local)
    expect(local.map(m => m.id)).toContain('local:ollama/qwen3:8b')
    expect(local.every(m => isLocalModelId(m.id))).toBe(true)
    expect(models.some(m => !m.local)).toBe(true)
  })
})

describe('connect mapping', () => {
  it('converts a provider', () => {
    const v = localProviderView(create(LocalProviderSchema, {
      id: 'ollama',
      kind: LocalProviderKind.OLLAMA,
      name: 'Ollama',
      reachable: true,
      contextLength: 16384n,
      models: [{ id: 'local:ollama/qwen3:8b', name: 'qwen3:8b', contextWindow: 16384n, tools: true }]
    }))
    expect(v.kind).toBe('ollama')
    expect(v.contextLength).toBe(16384)
    expect(v.models[0]).toMatchObject({ id: 'local:ollama/qwen3:8b', displayName: 'qwen3:8b', contextWindow: 16384, tools: true })
    expect(v.models[0]!.vision).toBeUndefined()
    expect(isLocalModelId('glm-4.6')).toBe(false)
  })
})
