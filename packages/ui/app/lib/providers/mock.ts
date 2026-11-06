// In-memory ProvidersApi for the mock transport. Follows the server's rules closely enough to exercise the
// settings screens: presets need only their fields, personal providers get generated ids and
// `<provider id>/<upstream>` model ids, global ones and classifier providers need the admin role, keys are
// write-only (set flag and hint only). Search providers may be global or personal; the built-in SearXNG is the
// default until an admin picks another, and everyone may pick their own. The preset list is a sample of the
// server's.

import type {
  ClassifierOverride,
  ClassifierSettings,
  ModelCandidateView,
  ModelSlots,
  NewModel,
  ModelPriceView,
  PriceCatalogView,
  PresetList,
  PresetView,
  ProviderDraft,
  ProviderList,
  ProviderPatch,
  ProviderView,
  ProvidersApi,
  PresetOptionView
} from './types'
import type { EffortName } from '~/lib/session/types'
import { BUILTIN_SEARCH_ID, emptySlots } from './types'

const TYPES = ['anthropic', 'openai-chat', 'openai-responses']

const KEY = { name: 'api_key', label: 'API key', required: true, placeholder: '' }

const RESULTS = (max: number): PresetOptionView => ({ name: 'max_results', label: 'Results', kind: 'number', choices: [], defaultValue: '10', placeholder: '', min: 1, max })
const SAFE: PresetOptionView = {
  name: 'safe_search',
  label: 'Safe search',
  kind: 'select',
  choices: [{ value: 'off', label: 'Off' }, { value: 'moderate', label: 'Moderate' }, { value: 'strict', label: 'Strict' }],
  defaultValue: 'moderate',
  placeholder: '',
  min: 0,
  max: 0
}
const COUNTRY: PresetOptionView = { name: 'country', label: 'Country', kind: 'text', choices: [], defaultValue: '', placeholder: 'Two-letter code, e.g. US', min: 0, max: 0 }
const SEARXNG_OPTIONS = [RESULTS(25), SAFE]

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
    icon: 'openrouter',
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
    icon: 'anthropic',
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
    icon: 'kimi',
    discoversModels: false,
    local: false,
    billing: 'plan'
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
    icon: 'ollama',
    discoversModels: true,
    local: true
  },
  {
    id: 'openrouter-decisions',
    group: 'classifier',
    name: 'OpenRouter decisions',
    description: 'Decision model through OpenRouter\'s decisions endpoint.',
    type: 'decision-model',
    baseUrl: 'https://openrouter.ai/api/alpha/decisions',
    fields: [KEY],
    docsUrl: '',
    keyUrl: 'https://openrouter.ai/settings/keys',
    defaultModels: [{ upstream: 'typesafe/jev-1.13', name: 'Jev 1.13', contextWindow: 0 }],
    icon: 'openrouter',
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
  },
  {
    id: BUILTIN_SEARCH_ID,
    group: 'search',
    name: 'SearXNG (built in)',
    description: 'The metasearch engine that ships with the server. No key needed.',
    type: 'searxng',
    baseUrl: '',
    fields: [],
    docsUrl: 'https://docs.searxng.org/dev/search_api.html',
    keyUrl: '',
    defaultModels: [],
    icon: 'searxng',
    discoversModels: false,
    local: false,
    builtin: true
  },
  {
    id: 'searxng-url',
    group: 'search',
    name: 'SearXNG',
    description: 'Your own SearXNG instance (the JSON format must be enabled).',
    type: 'searxng',
    baseUrl: '',
    fields: [{ name: 'base_url', label: 'Instance URL', required: true, placeholder: 'https://searx.example.com' }],
    docsUrl: 'https://docs.searxng.org/dev/search_api.html',
    keyUrl: '',
    defaultModels: [],
    icon: 'searxng',
    discoversModels: false,
    local: true,
    options: SEARXNG_OPTIONS
  },
  {
    id: 'brave',
    group: 'search',
    name: 'Brave Search',
    description: 'Brave\'s independent web index through the Brave Search API.',
    type: 'brave',
    baseUrl: 'https://api.search.brave.com/res/v1/web/search',
    fields: [KEY],
    docsUrl: 'https://api-dashboard.search.brave.com/app/documentation/web-search/query',
    keyUrl: '',
    defaultModels: [],
    icon: 'brave',
    discoversModels: false,
    local: false,
    options: [RESULTS(20), SAFE, COUNTRY]
  },
  {
    id: 'tavily',
    group: 'search',
    name: 'Tavily',
    description: 'Search built for AI agents, with page excerpts.',
    type: 'tavily',
    baseUrl: 'https://api.tavily.com/search',
    fields: [KEY],
    docsUrl: 'https://docs.tavily.com/documentation/api-reference/endpoint/search',
    keyUrl: '',
    defaultModels: [],
    icon: 'tavily',
    discoversModels: false,
    local: false,
    options: [RESULTS(20)]
  }
]

const PROTOCOLS: PresetList['protocols'] = [
  { id: 'openai-chat', group: 'llm', label: 'OpenAI Chat Completions', baseUrlHint: 'https://api.example.com/v1' },
  { id: 'openai-responses', group: 'llm', label: 'OpenAI Responses', baseUrlHint: 'https://api.example.com/v1' },
  { id: 'anthropic', group: 'llm', label: 'Anthropic Messages', baseUrlHint: 'https://api.example.com' },
  { id: 'decision-model', group: 'classifier', label: 'Typed decision model', baseUrlHint: 'https://decisions.example.com/v1/decide' },
  { id: 'decision-http', group: 'classifier', label: 'HTTP policy webhook', baseUrlHint: 'https://policy.example.com/decide' },
  { id: 'searxng', group: 'search', label: 'SearXNG-compatible', baseUrlHint: 'https://searx.example.com' }
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
  private priceCatalog: PriceCatalogView = { enabled: true, updatedAt: '2026-11-06T09:00:00.000Z' }
  private classifier: ClassifierSettings
  private slots: ModelSlots = emptySlots()
  private mine: ClassifierOverride = { choice: 'default', providerId: '', llmModel: '' }
  private searchDefault = ''
  private mySearch = ''
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
        models: [{ id: 'claude-haiku', name: 'Claude Haiku 5.5', upstream: 'anthropic/claude-haiku-5.5', contextWindow: 200_000, efforts: ['low', 'medium', 'high'], defaultEffort: 'medium' }]
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
      },
      {
        id: 'openai',
        scope: 'personal',
        group: 'llm',
        presetId: 'openai',
        type: 'openai-responses',
        name: 'OpenAI',
        baseUrl: 'https://api.openai.com/v1',
        keySet: true,
        keyHint: '…e5f6',
        headerNames: [],
        enabled: true,
        allowPrivateNetwork: false,
        editable: true,
        billing: 'metered',
        models: [
          { id: 'gpt-6.1-sol', name: 'GPT-6.1 Sol', upstream: 'gpt-6.1-sol', contextWindow: 400_000, efforts: ['low', 'medium', 'high'], defaultEffort: 'medium', pricing: { input: 2, output: 10, cacheRead: 0.1, cacheWrite: 2.5 }, pricingSource: 'catalog', pricingUpdatedAt: '2026-11-05T09:00:00.000Z', priceChangeHeld: { proposed: { input: 20, output: 100, cacheRead: 1, cacheWrite: 25 }, source: 'catalog', heldAt: '2026-11-06T09:00:00.000Z' } }
        ]
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
    return { providers: structuredClone(this.providers), types: [...TYPES], canManageGlobal: this.admin, priceCatalog: { ...this.priceCatalog } }
  }

  async create(d: ProviderDraft) {
    if (d.scope === 'global' && !this.admin) throw new Error('Admin role required')
    const preset = d.presetId ? PRESETS.find(p => p.id === d.presetId) : undefined
    if (d.presetId && !preset) throw new Error(`Unknown preset ${d.presetId}`)
    if (preset?.builtin) throw new Error(`${preset.name} is built in: there is nothing to connect`)
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
      models: [],
      billing: group === 'llm' ? preset?.billing ?? 'metered' : '',
      ...(group === 'search' ? { options: this.checkOptions(preset ?? PRESETS.find(x => x.id === 'searxng-url'), d.options) } : {})
    }
    this.providers.push(p)
    return structuredClone(p)
  }

  // Options a search preset declares: numbers in range, choices from the list; '' drops one.
  private checkOptions(preset: PresetView | undefined, input: Record<string, string> | undefined) {
    const out: Record<string, string> = {}
    for (const [name, v] of Object.entries(input ?? {})) {
      const spec = preset?.options?.find(o => o.name === name)
      if (!spec) throw new Error(`Unknown option ${name}`)
      if (!v.trim()) continue
      if (spec.kind === 'number' && !(Number.isInteger(Number(v)) && Number(v) >= spec.min && Number(v) <= spec.max)) {
        throw new Error(`${spec.label} must be a whole number from ${spec.min} to ${spec.max}`)
      }
      if (spec.kind === 'select' && !spec.choices.some(c => c.value === v)) throw new Error(`${spec.label} must be one of ${spec.choices.map(c => c.value).join(', ')}`)
      out[name] = spec.name === 'country' ? v.trim().toUpperCase() : v.trim()
    }
    return out
  }

  async update(id: string, patch: ProviderPatch) {
    const p = this.writable(id)
    if (patch.options !== undefined) {
      if (p.group !== 'search') throw new Error('Only search providers have options')
      p.options = this.checkOptions(PRESETS.find(x => x.id === (p.presetId || 'searxng-url')), patch.options)
    }
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
    if (id === BUILTIN_SEARCH_ID) return { ok: true, message: 'Connected, 5 results for a sample query', modelCount: 0 }
    const p = this.writable(id)
    if (p.group === 'search') {
      return p.keySet || p.type === 'searxng' ? { ok: true, message: 'Connected, 5 results for a sample query', modelCount: 0 } : { ok: false, message: `${p.name} rejected the API key (HTTP 401)`, modelCount: 0 }
    }
    if (!p.keySet && !p.presetId.startsWith('ollama')) return { ok: false, message: 'The key was rejected (HTTP 401)', modelCount: 0 }
    if (p.group === 'classifier') return { ok: true, message: 'Connected: a sample "ls" was answered allow', modelCount: 0 }
    return { ok: true, message: `Connected, ${CANDIDATES.length} models offered`, modelCount: CANDIDATES.length }
  }

  async addModel(providerId: string, m: NewModel) {
    const p = this.writable(providerId)
    if (p.group === 'classifier') throw new Error('Classifier providers serve no chat models')
    if (p.group === 'search') throw new Error('Search providers serve no chat models')
    const id = p.scope === 'personal' ? `${p.id}/${m.upstream}` : m.id || m.upstream
    if (this.providers.some(x => x.models.some(y => y.id === id))) throw new Error(`Model ${id} already exists`)
    p.models.push({ id, name: m.name || m.upstream, upstream: m.upstream, contextWindow: m.contextWindow || 128_000, efforts: [] })
  }

  async setModelEfforts(modelId: string, efforts: EffortName[], defaultEffort?: EffortName | '') {
    const p = this.providers.find(x => x.models.some(m => m.id === modelId))
    if (!p) throw new Error(`Model ${modelId} not found`)
    this.writable(p.id)
    if (defaultEffort && !efforts.includes(defaultEffort)) throw new Error(`[invalid_argument] ${defaultEffort} is not one of the model's effort levels`)
    const m = p.models.find(m => m.id === modelId)!
    m.efforts = efforts
    const recommended = defaultEffort === undefined ? m.defaultEffort : defaultEffort
    m.defaultEffort = recommended && efforts.includes(recommended) ? recommended : undefined
  }

  async setModelPricing(modelId: string, pricing: ModelPriceView | null) {
    const p = this.providers.find(x => x.models.some(m => m.id === modelId))
    if (!p) throw new Error(`Model ${modelId} not found`)
    this.writable(p.id)
    if (p.billing === 'plan') throw new Error('[invalid_argument] A subscription plan\'s requests carry no price')
    const m = p.models.find(m => m.id === modelId)!
    m.pricing = pricing ?? undefined
    m.pricingSetByHand = true
    m.pricingSource = 'hand'
    m.pricingUpdatedAt = new Date().toISOString()
    m.priceChangeHeld = undefined
  }

  async acceptModelPriceChange(modelId: string) {
    const p = this.providers.find(x => x.models.some(m => m.id === modelId))
    if (!p) throw new Error(`Model ${modelId} not found`)
    this.writable(p.id)
    const m = p.models.find(m => m.id === modelId)!
    if (!m.priceChangeHeld) throw new Error(`[invalid_argument] No price change is held for ${modelId}`)
    m.pricing = m.priceChangeHeld.proposed
    m.pricingSource = m.priceChangeHeld.source
    m.pricingUpdatedAt = new Date().toISOString()
    m.priceChangeHeld = undefined
  }

  async refreshModelPrices() {
    if (!this.admin) throw new Error('Admin role required')
    this.priceCatalog = { enabled: true, updatedAt: new Date().toISOString() }
    return { ok: true, message: 'Prices refreshed', updated: 0, held: 0, catalog: { ...this.priceCatalog } }
  }

  async removeModel(modelId: string) {
    const p = this.providers.find(x => x.models.some(m => m.id === modelId))
    if (!p) throw new Error(`Model ${modelId} not found`)
    this.writable(p.id)
    p.models = p.models.filter(m => m.id !== modelId)
  }

  async getSlots() {
    const first = this.providers.find(p => p.scope === 'global' && p.group === 'llm')?.models[0]?.id ?? ''
    return { slots: { ...this.slots }, editable: this.admin, defaults: { main: first, small: first, plan: first, title: this.slots.small || first } }
  }

  async setSlots(s: ModelSlots) {
    if (!this.admin) throw new Error('Admin role required')
    const global = new Set(this.providers.filter(p => p.scope === 'global' && p.group === 'llm').flatMap(p => p.models.map(m => m.id)))
    for (const [name, id] of Object.entries(s)) {
      if (id && !global.has(id)) throw new Error(`Unknown model ${id} for slot ${name}`)
    }
    this.slots = { main: s.main, small: s.small, plan: s.plan, title: s.title }
    return { ...this.slots }
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

  async getMyClassifier() {
    return {
      mine: { ...this.mine },
      defaultProviderId: this.classifier.enabled ? this.classifier.providerId : '',
      defaultLlmModel: this.classifier.llmModel
    }
  }

  async setMyClassifier(o: ClassifierOverride) {
    if (o.choice === 'provider' && !this.providers.some(p => p.id === o.providerId && p.scope === 'global' && p.group === 'classifier' && p.enabled)) {
      throw new Error(`Unknown classifier provider ${o.providerId}`)
    }
    if (o.llmModel && !this.providers.some(p => p.scope === 'global' && p.group === 'llm' && p.models.some(m => m.id === o.llmModel))) {
      throw new Error(`Unknown model ${o.llmModel}`)
    }
    this.mine = { choice: o.choice, providerId: o.choice === 'provider' ? o.providerId : '', llmModel: o.llmModel }
    return this.getMyClassifier()
  }

  // A search provider the caller may use: enabled, global or their own (every personal one is the caller's here).
  private usableSearch(id: string, globalOnly = false) {
    return id === BUILTIN_SEARCH_ID || this.providers.some(p => p.id === id && p.group === 'search' && p.enabled && (p.scope === 'global' || !globalOnly))
  }

  async getSearch() {
    const def = this.searchDefault && this.usableSearch(this.searchDefault, true) ? this.searchDefault : BUILTIN_SEARCH_ID
    const mine = this.mySearch && this.usableSearch(this.mySearch) ? this.mySearch : ''
    return { defaultProviderId: def, myProviderId: mine, effectiveProviderId: mine || def, editable: this.admin }
  }

  async setSearchDefault(providerId: string) {
    if (!this.admin) throw new Error('Admin role required')
    if (providerId && !this.usableSearch(providerId, true)) throw new Error(`Unknown search provider ${providerId}`)
    this.searchDefault = providerId === BUILTIN_SEARCH_ID ? '' : providerId
    return this.getSearch()
  }

  async setMySearch(providerId: string) {
    if (providerId && !this.usableSearch(providerId)) throw new Error(`Unknown search provider ${providerId}`)
    this.mySearch = providerId
    return this.getSearch()
  }
}
