// In-memory ProvidersApi for the mock transport. Follows the server's rules closely enough to exercise the
// settings screens: personal providers get generated ids and `<provider id>/<upstream>` model ids, global
// ones need the admin role, keys are write-only (set flag and hint only).

import type { ClassifierSettings, ModelCandidateView, NewModel, ProviderDraft, ProviderList, ProviderPatch, ProviderView, ProvidersApi } from './types'

const TYPES = ['anthropic', 'openai-chat', 'openai-responses']

const CANDIDATES: ModelCandidateView[] = [
  { upstream: 'anthropic/claude-haiku-5.5', name: 'Claude Haiku 5.5', contextWindow: 200_000, modelIds: [] },
  { upstream: 'openai/gpt-6-luna', name: 'GPT-6 Luna', contextWindow: 400_000, modelIds: [] },
  { upstream: 'qwen/qwen3.7-flash', name: 'Qwen 3.7 Flash', contextWindow: 1_000_000, modelIds: [] }
]

function hint(key: string) {
  return key.length > 8 ? `…${key.slice(-4)}` : '…'
}

export class MockProvidersApi implements ProvidersApi {
  private providers: ProviderView[]
  private classifier: ClassifierSettings
  private seq = 0

  constructor(private readonly admin = true) {
    this.providers = [{
      id: 'openrouter',
      scope: 'global',
      type: 'openai-chat',
      name: 'OpenRouter',
      baseUrl: 'https://openrouter.ai/api/v1',
      keySet: true,
      keyHint: '…a1b2',
      enabled: true,
      allowPrivateNetwork: false,
      editable: admin,
      models: [{ id: 'claude-haiku', name: 'Claude Haiku 5.5', upstream: 'anthropic/claude-haiku-5.5', contextWindow: 200_000 }]
    }]
    this.classifier = {
      enabled: true,
      providerId: 'openrouter',
      endpoint: 'openrouter',
      model: 'typesafe/jev-1.13',
      minProbability: 0.8,
      minMargin: 0.15,
      onUncertain: 'next',
      timeoutMs: 3000,
      llmModel: ''
    }
  }

  private find(id: string) {
    const p = this.providers.find(x => x.id === id)
    if (!p) throw new Error(`Provider ${id} not found`)
    return p
  }

  private writable(id: string) {
    const p = this.find(id)
    if (!p.editable) throw new Error('Only admins can change global providers')
    return p
  }

  async list(): Promise<ProviderList> {
    return { providers: structuredClone(this.providers), types: [...TYPES], canManageGlobal: this.admin }
  }

  async create(d: ProviderDraft) {
    if (d.scope === 'global' && !this.admin) throw new Error('Admin role required')
    if (d.scope === 'global' && !d.id) throw new Error('id is required')
    if (!d.baseUrl) throw new Error('base_url is required')
    const id = d.scope === 'global' ? d.id! : `u-mock${++this.seq}`
    const p: ProviderView = {
      id,
      scope: d.scope,
      type: d.type,
      name: d.name || id,
      baseUrl: d.baseUrl.replace(/\/+$/, ''),
      keySet: !!d.apiKey,
      keyHint: d.apiKey ? hint(d.apiKey) : '',
      enabled: true,
      allowPrivateNetwork: d.scope === 'global' && !!d.allowPrivateNetwork,
      editable: true,
      models: []
    }
    this.providers.push(p)
    return structuredClone(p)
  }

  async update(id: string, patch: ProviderPatch) {
    const p = this.writable(id)
    if (patch.type !== undefined) p.type = patch.type
    if (patch.name !== undefined) p.name = patch.name || p.id
    if (patch.baseUrl !== undefined) p.baseUrl = patch.baseUrl
    if (patch.apiKey !== undefined) {
      p.keySet = !!patch.apiKey
      p.keyHint = patch.apiKey ? hint(patch.apiKey) : ''
    }
    if (patch.enabled !== undefined) p.enabled = patch.enabled
    if (patch.allowPrivateNetwork !== undefined && p.scope === 'global') p.allowPrivateNetwork = patch.allowPrivateNetwork
    return structuredClone(p)
  }

  async remove(id: string) {
    const p = this.writable(id)
    if (p.scope === 'global' && p.models.length) throw new Error(`Provider is used by model ${p.models[0]!.id}`)
    this.providers = this.providers.filter(x => x.id !== id)
  }

  async discover(id: string) {
    const p = this.writable(id)
    return CANDIDATES.map(c => ({ ...c, modelIds: p.models.filter(m => m.upstream === c.upstream).map(m => m.id) }))
  }

  async addModel(providerId: string, m: NewModel) {
    const p = this.writable(providerId)
    const id = p.scope === 'personal' ? `${p.id}/${m.upstream}` : m.id || m.upstream
    if (this.providers.some(x => x.models.some(y => y.id === id))) throw new Error(`Model ${id} already exists`)
    p.models.push({ id, name: m.name || m.upstream, upstream: m.upstream, contextWindow: m.contextWindow || 128_000 })
  }

  async removeModel(modelId: string) {
    const p = this.providers.find(x => x.models.some(m => m.id === modelId))
    if (!p) throw new Error(`Model ${modelId} not found`)
    this.writable(p.id)
    p.models = p.models.filter(m => m.id !== modelId)
  }

  async getClassifier() {
    if (!this.admin) throw new Error('Admin role required')
    return { ...this.classifier }
  }

  async setClassifier(c: ClassifierSettings) {
    if (!this.admin) throw new Error('Admin role required')
    if (c.enabled && !this.providers.some(p => p.id === c.providerId && p.scope === 'global')) throw new Error(`Unknown global provider ${c.providerId}`)
    this.classifier = { ...c }
    return { ...this.classifier }
  }
}
