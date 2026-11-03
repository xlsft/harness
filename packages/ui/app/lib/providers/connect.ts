// ProvidersApi over harness.llm.v1.ProviderService, on the same connect transport (and agent token) as
// AgentService: the client forwards these calls to the server (packages/client/server/coreproxy.go).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import {
  ProviderScope,
  ProviderService,
  type Classifier,
  type Provider,
  type ProviderPreset
} from '~/gen/harness/llm/v1/providers_pb'
import type { Slots } from '~/gen/harness/llm/v1/gateway_pb'
import type { ClassifierSettings, ModelSlots, PresetView, ProviderGroupName, ProviderView, ProvidersApi } from './types'

const group = (g: string): ProviderGroupName => g === 'classifier' ? 'classifier' : 'llm'

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
    models: p.models.map(m => ({ id: m.id, name: m.displayName || m.id, upstream: m.upstreamModel, contextWindow: Number(m.contextWindow) }))
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
    local: p.local
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
      allowPrivateNetwork: d.scope === 'global' && !!d.allowPrivateNetwork
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
      allowPrivateNetwork: p.allowPrivateNetwork
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
}
