// ProvidersApi over harness.llm.v1.ProviderService, on the same connect transport (and agent token) as
// AgentService: the client forwards these calls to the server (packages/client/server/coreproxy.go).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import {
  ProviderScope,
  ProviderService,
  type Classifier,
  type Provider
} from '~/gen/harness/llm/v1/providers_pb'
import type { ClassifierSettings, ProviderView, ProvidersApi } from './types'

export function providerView(p: Provider): ProviderView {
  return {
    id: p.id,
    scope: p.scope === ProviderScope.PERSONAL ? 'personal' : 'global',
    type: p.type,
    name: p.displayName || p.id,
    baseUrl: p.baseUrl,
    keySet: p.apiKeySet,
    keyHint: p.apiKeyHint,
    enabled: p.enabled,
    allowPrivateNetwork: p.allowPrivateNetwork,
    editable: p.editable,
    models: p.models.map(m => ({ id: m.id, name: m.displayName || m.id, upstream: m.upstreamModel, contextWindow: Number(m.contextWindow) }))
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

export class ConnectProvidersApi implements ProvidersApi {
  private client: Client<typeof ProviderService>

  constructor(transport: Transport) {
    this.client = createClient(ProviderService, transport)
  }

  async list() {
    const r = await this.client.listProviders({})
    return { providers: r.providers.map(providerView), types: r.types, canManageGlobal: r.canManageGlobal }
  }

  async create(d: Parameters<ProvidersApi['create']>[0]) {
    const r = await this.client.createProvider({
      scope: d.scope === 'global' ? ProviderScope.GLOBAL : ProviderScope.PERSONAL,
      id: d.scope === 'global' ? d.id ?? '' : '',
      type: d.type,
      displayName: d.name,
      baseUrl: d.baseUrl,
      apiKey: d.apiKey ?? '',
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

  async getClassifier() {
    return classifierView((await this.client.getClassifier({})).classifier)
  }

  async setClassifier(c: ClassifierSettings) {
    return classifierView((await this.client.setClassifier({ classifier: c })).classifier)
  }
}
