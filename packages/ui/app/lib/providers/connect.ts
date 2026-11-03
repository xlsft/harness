// ProvidersApi over harness.llm.v1.ProviderService, on the same connect transport (and agent token) as
// AgentService: the client forwards these calls to the server (packages/client/server/coreproxy.go).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import {
  ProviderScope,
  ProviderService,
  type Classifier,
  ClassifierChoice,
  type GetMyClassifierResponse,
  type Provider,
  type ProviderPreset,
  type SearchSettings as SearchSettingsMsg
} from '~/gen/harness/llm/v1/providers_pb'
import type { Slots } from '~/gen/harness/llm/v1/gateway_pb'
import type { ClassifierOverride, ClassifierSettings, ModelSlots, MyClassifier, PresetView, ProviderGroupName, ProviderView, ProvidersApi, SearchSettings } from './types'

const group = (g: string): ProviderGroupName => g === 'classifier' || g === 'search' ? g : 'llm'

export function providerView(p: Provider): ProviderView {
  return {
    id: p.id,
    scope: p.scope === ProviderScope.PERSONAL ? 'personal' : 'global',
    group: group(p.group),
    presetId: p.presetId,
    type: p.type,
    name: p.displayName || p.id,
    baseUrl: p.baseUrl,
    keySet: p.apiKeySet,
    keyHint: p.apiKeyHint,
    headerNames: [...p.headerNames],
    enabled: p.enabled,
    allowPrivateNetwork: p.allowPrivateNetwork,
    editable: p.editable,
    models: p.models.map(m => ({ id: m.id, name: m.displayName || m.id, upstream: m.upstreamModel, contextWindow: Number(m.contextWindow) })),
    options: { ...p.options }
  }
}

export function presetView(p: ProviderPreset): PresetView {
  return {
    id: p.id,
    group: group(p.group),
    name: p.displayName,
    description: p.description,
    type: p.type,
    baseUrl: p.baseUrl,
    fields: p.fields.map(f => ({ name: f.name, label: f.label, required: f.required, placeholder: f.placeholder })),
    docsUrl: p.docsUrl,
    keyUrl: p.keyUrl,
    defaultModels: p.defaultModels.map(m => ({ upstream: m.upstreamModel, name: m.displayName || m.upstreamModel, contextWindow: Number(m.contextWindow) })),
    icon: p.icon || p.displayName.slice(0, 2).toUpperCase(),
    discoversModels: p.discoversModels,
    local: p.local,
    options: p.options.map(o => ({
      name: o.name,
      label: o.label,
      kind: o.kind === 'number' || o.kind === 'select' ? o.kind : 'text',
      choices: o.choices.map(c => ({ value: c.value, label: c.label })),
      defaultValue: o.defaultValue,
      placeholder: o.placeholder,
      min: o.min,
      max: o.max
    })),
    builtin: p.builtin
  }
}

export function searchSettingsView(s: SearchSettingsMsg | undefined): SearchSettings {
  return {
    defaultProviderId: s?.defaultProviderId || 'searxng',
    myProviderId: s?.myProviderId ?? '',
    effectiveProviderId: s?.effectiveProviderId || s?.defaultProviderId || 'searxng',
    editable: s?.editable ?? false
  }
}

export function classifierView(c: Classifier | undefined): ClassifierSettings {
  return {
    enabled: c?.enabled ?? false,
    providerId: c?.providerId ?? '',
    endpoint: c?.endpoint ?? '',
    model: c?.model ?? '',
    minProbability: c?.minProbability ?? 0.8,
    minMargin: c?.minMargin ?? 0.15,
    onUncertain: c?.onUncertain === 'ask' ? 'ask' : 'next',
    timeoutMs: c?.timeoutMs ?? 3000,
    llmModel: c?.llmModel ?? ''
  }
}

const CHOICE_NAMES = {
  [ClassifierChoice.UNSPECIFIED]: 'default',
  [ClassifierChoice.NONE]: 'none',
  [ClassifierChoice.PROVIDER]: 'provider'
} as const

export function myClassifierView(r: Pick<GetMyClassifierResponse, 'mine' | 'defaultProviderId' | 'defaultLlmModel'>): MyClassifier {
  const choice = CHOICE_NAMES[r.mine?.choice ?? ClassifierChoice.UNSPECIFIED] ?? 'default'
  return {
    mine: { choice, providerId: choice === 'provider' ? r.mine?.providerId ?? '' : '', llmModel: r.mine?.llmModel ?? '' },
    defaultProviderId: r.defaultProviderId,
    defaultLlmModel: r.defaultLlmModel
  }
}

const CHOICE_PROTO = { default: ClassifierChoice.UNSPECIFIED, none: ClassifierChoice.NONE, provider: ClassifierChoice.PROVIDER } as const

export function slotsView(s: Slots | undefined): ModelSlots {
  return { main: s?.main ?? '', small: s?.small ?? '', plan: s?.plan ?? '', title: s?.title ?? '' }
}

export class ConnectProvidersApi implements ProvidersApi {
  private client: Client<typeof ProviderService>

  constructor(transport: Transport) {
    this.client = createClient(ProviderService, transport)
  }

  async presets() {
    const r = await this.client.listPresets({})
    return {
      presets: r.presets.map(presetView),
      protocols: r.protocols.map(p => ({ id: p.id, group: group(p.group), label: p.label, baseUrlHint: p.baseUrlHint }))
    }
  }

  async list() {
    const r = await this.client.listProviders({})
    return { providers: r.providers.map(providerView), types: r.types, canManageGlobal: r.canManageGlobal }
  }

  async create(d: Parameters<ProvidersApi['create']>[0]) {
    const r = await this.client.createProvider({
      scope: d.scope === 'global' ? ProviderScope.GLOBAL : ProviderScope.PERSONAL,
      presetId: d.presetId ?? '',
      group: d.presetId ? '' : d.group ?? '',
      type: d.type ?? '',
      displayName: d.name ?? '',
      baseUrl: d.baseUrl ?? '',
      apiKey: d.apiKey ?? '',
      headers: d.headers ?? {},
      allowPrivateNetwork: d.scope === 'global' && !!d.allowPrivateNetwork,
      options: d.options ?? {}
    })
    return providerView(r.provider!)
  }

  async update(id: string, p: Parameters<ProvidersApi['update']>[1]) {
    const r = await this.client.updateProvider({
      id,
      type: p.type,
      displayName: p.name,
      baseUrl: p.baseUrl,
      apiKey: p.apiKey,
      headers: p.headers ? { values: p.headers } : undefined,
      enabled: p.enabled,
      allowPrivateNetwork: p.allowPrivateNetwork,
      options: p.options ? { values: p.options } : undefined
    })
    return providerView(r.provider!)
  }

  async remove(id: string) {
    await this.client.deleteProvider({ id })
  }

  async discover(id: string) {
    const r = await this.client.discoverModels({ providerId: id })
    return r.models.map(m => ({ upstream: m.upstreamModel, name: m.displayName, contextWindow: Number(m.contextWindow), modelIds: m.modelIds }))
  }

  async test(id: string) {
    const r = await this.client.testProvider({ providerId: id })
    return { ok: r.ok, message: r.message, modelCount: r.modelCount }
  }

  async addModel(providerId: string, m: Parameters<ProvidersApi['addModel']>[1]) {
    await this.client.addModel({
      providerId,
      upstreamModel: m.upstream,
      displayName: m.name ?? '',
      id: m.id ?? '',
      contextWindow: BigInt(m.contextWindow ?? 0)
    })
  }

  async removeModel(modelId: string) {
    await this.client.removeModel({ modelId })
  }

  async getSlots() {
    const r = await this.client.getSlots({})
    return { slots: slotsView(r.slots), editable: r.editable, defaults: slotsView(r.defaults) }
  }

  async setSlots(s: ModelSlots) {
    return slotsView((await this.client.setSlots({ slots: s })).slots)
  }

  async getClassifier() {
    return classifierView((await this.client.getClassifier({})).classifier)
  }

  async setClassifier(c: ClassifierSettings) {
    return classifierView((await this.client.setClassifier({ classifier: c })).classifier)
  }

  async getMyClassifier() {
    return myClassifierView(await this.client.getMyClassifier({}))
  }

  async getSearch() {
    return searchSettingsView((await this.client.getSearch({})).settings)
  }

  async setSearchDefault(providerId: string) {
    return searchSettingsView((await this.client.setSearchDefault({ providerId })).settings)
  }

  async setMySearch(providerId: string) {
    return searchSettingsView((await this.client.setMySearch({ providerId })).settings)
  }

  async setMyClassifier(o: ClassifierOverride) {
    return myClassifierView(await this.client.setMyClassifier({ mine: { choice: CHOICE_PROTO[o.choice], providerId: o.providerId, llmModel: o.llmModel } }))
  }
}
