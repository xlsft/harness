// In-memory ProvidersApi for the mock transport. Follows the server's rules closely enough to exercise the
// settings screens: presets need only their fields, personal providers get generated ids and
// `<provider id>/<upstream>` model ids, global ones and classifier providers need the admin role, keys are
// write-only (set flag and hint only). The preset list is a sample of the server's.

import type {
  ClassifierSettings,
  ModelCandidateView,
  NewModel,
  PresetList,
  PresetView,
  ProviderDraft,
  ProviderList,
  ProviderPatch,
  ProviderView,
  ProvidersApi
} from './types'

const TYPES = ['anthropic', 'openai-chat', 'openai-responses']

const KEY = { name: 'api_key', label: 'API key', required: true, placeholder: '' }

const PRESETS: PresetView[] = [
  {
    id: 'openrouter',
    group: 'llm',
    name: 'OpenRouter',
    description: 'Hundreds of models from every major lab behind one key.',
    type: 'openai-chat',
    baseUrl: 'https://openrouter.ai/api/v1',
    fields: [KEY],
    docsUrl: 'https://openrouter.ai/docs/quickstart',
    keyUrl: 'https://openrouter.ai/settings/keys',
    defaultModels: [],
    icon: 'OR',
    discoversModels: true,
    local: false
  },
  {
    id: 'anthropic',
    group: 'llm',
    name: 'Anthropic',
    description: 'Claude models through the Claude API.',
    type: 'anthropic',
    baseUrl: 'https://api.anthropic.com',
    fields: [KEY],
    docsUrl: 'https://platform.claude.com/docs/en/api/overview',
    keyUrl: 'https://platform.claude.com/settings/keys',
    defaultModels: [],
    icon: 'AN',
    discoversModels: true,
    local: false
  },
  {
    id: 'kimi-code',
    group: 'llm',
    name: 'Kimi Code',
    description: 'The kimi.com coding subscription (Anthropic-compatible endpoint).',
    type: 'anthropic',
    baseUrl: 'https://api.kimi.com/coding',
    fields: [KEY],
    docsUrl: 'https://www.kimi.com/code/docs/en/',
    keyUrl: 'https://www.kimi.com/code/console',
    defaultModels: [
      { upstream: 'kimi-for-coding', name: 'Kimi for Coding', contextWindow: 0 },
      { upstream: 'k3', name: 'K3', contextWindow: 0 }
    ],
    icon: 'KC',
    discoversModels: false,
    local: false
  },
  {
    id: 'ollama',
    group: 'llm',
    name: 'Ollama',
    description: 'Models served by Ollama on your network.',
    type: 'openai-chat',
    baseUrl: 'http://localhost:11434/v1',
    fields: [
      { name: 'base_url', label: 'Server URL', required: true, placeholder: 'http://localhost:11434/v1' },
      { name: 'api_key', label: 'API key (optional)', required: false, placeholder: '' }
    ],
    docsUrl: 'https://docs.ollama.com/api/openai-compatibility',
    keyUrl: '',
    defaultModels: [],
    icon: 'OL',
    discoversModels: true,
    local: true
  },
  {
    id: 'openrouter-decisions',
    group: 'classifier',
    name: 'OpenRouter decisions',
    description: 'TypeSafe\'s Jev decision model through OpenRouter\'s decisions endpoint.',
    type: 'decision-model',
    baseUrl: 'https://openrouter.ai/api/alpha/decisions',
    fields: [KEY],
    docsUrl: '',
    keyUrl: 'https://openrouter.ai/settings/keys',
    defaultModels: [{ upstream: 'typesafe/jev-1.13', name: 'Jev 1.13', contextWindow: 0 }],
    icon: 'OR',
    discoversModels: false,
    local: false
  },
  {
    id: 'typesafe',
    group: 'classifier',
    name: 'TypeSafe',
    description: 'The Jev decision model from TypeSafe\'s own API.',
    type: 'decision-model',
    baseUrl: 'https://api.typesafe.ai/v1/systemone',
    fields: [KEY],
    docsUrl: '',
    keyUrl: '',
    defaultModels: [{ upstream: 'jev-1.13.0', name: 'Jev 1.13', contextWindow: 0 }],
    icon: 'TS',
    discoversModels: false,
    local: false
  }
]

const PROTOCOLS: PresetList['protocols'] = [
  { id: 'openai-chat', group: 'llm', label: 'OpenAI Chat Completions', baseUrlHint: 'https://api.example.com/v1' },
  { id: 'openai-responses', group: 'llm', label: 'OpenAI Responses', baseUrlHint: 'https://api.example.com/v1' },
  { id: 'anthropic', group: 'llm', label: 'Anthropic Messages', baseUrlHint: 'https://api.example.com' },
  { id: 'decision-model', group: 'classifier', label: 'Typed decision model', baseUrlHint: 'https://decisions.example.com/v1/decide' },
  { id: 'decision-http', group: 'classifier', label: 'HTTP policy webhook', baseUrlHint: 'https://policy.example.com/decide' }
]

const CANDIDATES: ModelCandidateView[] = [
  { upstream: 'anthropic/claude-haiku-5.5', name: 'Claude Haiku 5.5', contextWindow: 200_000, modelIds: [] },
  { upstream: 'openai/gpt-6-luna', name: 'GPT-6 Luna', contextWindow: 400_000, modelIds: [] },
  { upstream: 'qwen/qwen3.7-flash', name: 'Qwen 3.7 Flash', contextWindow: 1_000_000, modelIds: [] }
]

function hint(key: string) {
  return key.length > 8 ? `…${key.slice(-4)}` : '…'
}

function slug(s: string) {
  return s.toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^[^a-z0-9]+|-+$/g, '') || 'provider'
}

export class MockProvidersApi implements ProvidersApi {
  private providers: ProviderView[]
  private classifier: ClassifierSettings
  private seq = 0

  constructor(private readonly admin = true) {
    this.providers = [
      {
        id: 'openrouter',
        scope: 'global',
        group: 'llm',
        presetId: 'openrouter',
        type: 'openai-chat',
        name: 'OpenRouter',
        baseUrl: 'https://openrouter.ai/api/v1',
        keySet: true,
        keyHint: '…a1b2',
        headerNames: [],
        enabled: true,
        allowPrivateNetwork: false,
        editable: admin,
        models: [{ id: 'claude-haiku', name: 'Claude Haiku 5.5', upstream: 'anthropic/claude-haiku-5.5', contextWindow: 200_000 }]
      },
      {
        id: 'openrouter-decisions',
        scope: 'global',
        group: 'classifier',
        presetId: 'openrouter-decisions',
        type: 'decision-model',
        name: 'OpenRouter decisions',
        baseUrl: 'https://openrouter.ai/api/alpha/decisions',
        keySet: true,
        keyHint: '…c3d4',
        headerNames: [],
        enabled: true,
        allowPrivateNetwork: false,
        editable: admin,
        models: []
      }
    ]
    this.classifier = {
      enabled: true,
      providerId: 'openrouter-decisions',
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

  async presets(): Promise<PresetList> {
    return { presets: structuredClone(PRESETS), protocols: structuredClone(PROTOCOLS) }
  }

  async list(): Promise<ProviderList> {
    return { providers: structuredClone(this.providers), types: [...TYPES], canManageGlobal: this.admin }
  }

  async create(d: ProviderDraft) {
    if (d.scope === 'global' && !this.admin) throw new Error('Admin role required')
    const preset = d.presetId ? PRESETS.find(p => p.id === d.presetId) : undefined
    if (d.presetId && !preset) throw new Error(`Unknown preset ${d.presetId}`)
    const group = preset?.group ?? d.group ?? 'llm'
    if (group === 'classifier' && d.scope !== 'global') throw new Error('Classifier providers are server-wide: only admins add them, globally')
    if (preset?.fields.some(f => f.name === 'api_key' && f.required) && !d.apiKey) throw new Error(`${preset.name} needs an API key`)
    const baseUrl = (preset && !preset.fields.some(f => f.name === 'base_url') ? preset.baseUrl : d.baseUrl || preset?.baseUrl || '').replace(/\/+$/, '')
    if (!baseUrl) throw new Error('base_url is required')
    if (d.scope === 'personal' && /localhost|127\.0\.0\.1/.test(baseUrl)) throw new Error('Personal providers cannot reach private networks')
    const name = d.name || preset?.name || ''
    let id = `u-mock${++this.seq}`
    if (d.scope === 'global') {
      const root = slug(preset?.id ?? name)
      id = root
      for (let n = 2; this.providers.some(p => p.id === id); n++) id = `${root}-${n}`
    }
    const p: ProviderView = {
      id,
      scope: d.scope,
      group,
      presetId: preset?.id ?? '',
      type: preset?.type ?? d.type ?? 'openai-chat',
      name: name || id,
      baseUrl,
      keySet: !!d.apiKey,
      keyHint: d.apiKey ? hint(d.apiKey) : '',
      headerNames: Object.keys(d.headers ?? {}),
      enabled: true,
      allowPrivateNetwork: d.scope === 'global' && (!!d.allowPrivateNetwork || !!preset?.local),
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
    if (patch.headers !== undefined) p.headerNames = Object.keys(patch.headers)
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
    const preset = PRESETS.find(x => x.id === p.presetId)
    const list = preset && (!preset.discoversModels || p.group === 'classifier')
      ? preset.defaultModels.map(m => ({ upstream: m.upstream, name: m.name, contextWindow: m.contextWindow, modelIds: [] }))
      : CANDIDATES
    return list.map(c => ({ ...c, modelIds: p.models.filter(m => m.upstream === c.upstream).map(m => m.id) }))
  }

  async test(id: string) {
    const p = this.writable(id)
    if (!p.keySet && !p.presetId.startsWith('ollama')) return { ok: false, message: 'The key was rejected (HTTP 401)', modelCount: 0 }
    if (p.group === 'classifier') return { ok: true, message: 'Connected: a sample "ls" was answered allow', modelCount: 0 }
    return { ok: true, message: `Connected, ${CANDIDATES.length} models offered`, modelCount: CANDIDATES.length }
  }

  async addModel(providerId: string, m: NewModel) {
    const p = this.writable(providerId)
    if (p.group === 'classifier') throw new Error('Classifier providers serve no chat models')
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
    const p = this.providers.find(x => x.id === c.providerId)
    const preset = PRESETS.find(x => x.id === p?.presetId)
    this.classifier = { ...c, model: c.model || (p?.type === 'decision-model' ? preset?.defaultModels[0]?.upstream ?? '' : '') }
    return { ...this.classifier }
  }
}
