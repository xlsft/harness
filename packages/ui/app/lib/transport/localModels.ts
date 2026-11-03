// Local LLM providers of the client's machine (AgentService ListLocalProviders and friends,
// proto/harness/agent/v1/localmodels.proto): Ollama, LM Studio, llama.cpp, MLX, vLLM, Jan and custom
// OpenAI-compatible endpoints the client calls directly. Everything here is per device: the client keeps the
// providers, their keys and their models on its own machine, nothing is synced through the server.
//
// Local model ids are "local:<provider id>/<model name>"; they also appear in listModels() with `local: true`.

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import {
  LocalProviderKind,
  type LocalModel as LocalModelPb,
  type LocalProvider as LocalProviderPb
} from '~/gen/harness/agent/v1/localmodels_pb'

export type LocalProviderKindName = 'ollama' | 'lmstudio' | 'llamacpp' | 'mlx' | 'vllm' | 'jan' | 'custom'

export interface LocalModel {
  // "local:<provider id>/<name>"
  id: string
  // The provider's own name ("qwen3:8b").
  name: string
  displayName: string
  // The context window the client uses (0: unknown).
  contextWindow: number
  // The model's own limit, when reported (0: unknown).
  maxContextWindow: number
  // Capabilities; undefined when the provider does not say.
  tools?: boolean
  vision?: boolean
  thinking?: boolean
  // Ollama details (0 / empty elsewhere).
  sizeBytes: number
  family: string
  parameterSize: string
  quantization: string
  modifiedAt: string
}

export interface LocalProvider {
  id: string
  kind: LocalProviderKindName
  name: string
  baseUrl: string
  enabled: boolean
  // A default endpoint: it can be disabled, not removed or moved.
  builtin: boolean
  reachable: boolean
  version: string
  // Why the last probe failed (e.g. "not running (connection refused)").
  error: string
  hasApiKey: boolean
  models: LocalModel[]
  // RFC 3339; empty when never probed.
  checkedAt: string
  // Ollama: the context window requested from models (0: the client's default, 32768).
  contextLength: number
}

export interface LocalProvidersState {
  providers: LocalProvider[]
  // The server's admin disallows local models on clients.
  disabledByPolicy: boolean
}

export interface AddLocalProviderInput {
  // Default 'custom' (any OpenAI-compatible server).
  kind?: LocalProviderKindName
  name?: string
  // The server root for Ollama, the /v1 prefix for OpenAI-compatible servers.
  baseUrl: string
  apiKey?: string
  contextLength?: number
}

// Unset fields stay as they are; built-in providers take enabled and contextLength only.
export interface LocalProviderPatch {
  name?: string
  baseUrl?: string
  // '' removes the stored key.
  apiKey?: string
  enabled?: boolean
  contextLength?: number
}

export interface PullProgress {
  // "pulling manifest", "downloading", "verifying sha256 digest", "success", ...
  status: string
  digest: string
  total: number
  completed: number
  done: boolean
}

export interface PullHandlers {
  onProgress?: (p: PullProgress) => void
}

export interface PullHandle {
  // Resolves when the pull succeeded; rejects when it failed or was cancelled.
  done: Promise<void>
  cancel(): void
}

export interface LocalModelsApi {
  // Cached probes (refreshed by the client when older than a minute); refresh probes now.
  list(opts?: { refresh?: boolean }): Promise<LocalProvidersState>
  // Adds an endpoint; it is probed right away.
  add(input: AddLocalProviderInput): Promise<LocalProvider>
  update(id: string, patch: LocalProviderPatch): Promise<LocalProvider>
  // Built-in providers cannot be removed (disable them instead).
  remove(id: string): Promise<void>
  // Probes all enabled providers, or one.
  refresh(id?: string): Promise<LocalProvidersState>
  // Downloads a model into an Ollama server (default: the built-in "ollama"), streaming progress.
  pullOllamaModel(model: string, handlers: PullHandlers, providerId?: string): PullHandle
  deleteOllamaModel(model: string, providerId?: string): Promise<void>
}

const KIND_NAME: Record<LocalProviderKind, LocalProviderKindName> = {
  [LocalProviderKind.UNSPECIFIED]: 'custom',
  [LocalProviderKind.OLLAMA]: 'ollama',
  [LocalProviderKind.LM_STUDIO]: 'lmstudio',
  [LocalProviderKind.LLAMA_CPP]: 'llamacpp',
  [LocalProviderKind.MLX]: 'mlx',
  [LocalProviderKind.VLLM]: 'vllm',
  [LocalProviderKind.JAN]: 'jan',
  [LocalProviderKind.CUSTOM]: 'custom'
}

const KIND_PB: Record<LocalProviderKindName, LocalProviderKind> = {
  ollama: LocalProviderKind.OLLAMA,
  lmstudio: LocalProviderKind.LM_STUDIO,
  llamacpp: LocalProviderKind.LLAMA_CPP,
  mlx: LocalProviderKind.MLX,
  vllm: LocalProviderKind.VLLM,
  jan: LocalProviderKind.JAN,
  custom: LocalProviderKind.CUSTOM
}

export function localModelView(m: LocalModelPb): LocalModel {
  return {
    id: m.id,
    name: m.name,
    displayName: m.displayName || m.name,
    contextWindow: Number(m.contextWindow),
    maxContextWindow: Number(m.maxContextWindow),
    tools: m.tools,
    vision: m.vision,
    thinking: m.thinking,
    sizeBytes: Number(m.sizeBytes),
    family: m.family,
    parameterSize: m.parameterSize,
    quantization: m.quantization,
    modifiedAt: m.modifiedAt
  }
}

export function localProviderView(p: LocalProviderPb | undefined): LocalProvider {
  if (!p) throw new Error('the client returned no local provider')
  return {
    id: p.id,
    kind: KIND_NAME[p.kind] ?? 'custom',
    name: p.name,
    baseUrl: p.baseUrl,
    enabled: p.enabled,
    builtin: p.builtin,
    reachable: p.reachable,
    version: p.version,
    error: p.error,
    hasApiKey: p.hasApiKey,
    models: p.models.map(localModelView),
    checkedAt: p.checkedAt,
    contextLength: Number(p.contextLength)
  }
}

/** True for "local:<provider>/<name>" model ids. */
export const isLocalModelId = (id: string) => /^local:[^/]+\/.+/.test(id)

export class ConnectLocalModelsApi implements LocalModelsApi {
  private client: Client<typeof AgentService>

  constructor(transport: Transport) {
    this.client = createClient(AgentService, transport)
  }

  async list(opts: { refresh?: boolean } = {}) {
    const res = await this.client.listLocalProviders({ refresh: opts.refresh ?? false })
    return { providers: res.providers.map(localProviderView), disabledByPolicy: res.disabledByPolicy }
  }

  async add(input: AddLocalProviderInput) {
    const res = await this.client.addLocalProvider({
      kind: KIND_PB[input.kind ?? 'custom'],
      name: input.name ?? '',
      baseUrl: input.baseUrl,
      apiKey: input.apiKey ?? '',
      contextLength: BigInt(input.contextLength ?? 0)
    })
    return localProviderView(res.provider)
  }

  async update(id: string, patch: LocalProviderPatch) {
    const res = await this.client.updateLocalProvider({
      id,
      name: patch.name,
      baseUrl: patch.baseUrl,
      apiKey: patch.apiKey,
      enabled: patch.enabled,
      contextLength: patch.contextLength === undefined ? undefined : BigInt(patch.contextLength)
    })
    return localProviderView(res.provider)
  }

  async remove(id: string) {
    await this.client.removeLocalProvider({ id })
  }

  async refresh(id = '') {
    const res = await this.client.refreshLocalProviders({ id })
    return { providers: res.providers.map(localProviderView), disabledByPolicy: res.disabledByPolicy }
  }

  pullOllamaModel(model: string, handlers: PullHandlers, providerId = ''): PullHandle {
    const abort = new AbortController()
    const done = (async () => {
      let ok = false
      for await (const p of this.client.pullOllamaModel({ providerId, model }, { signal: abort.signal })) {
        const progress = { status: p.status, digest: p.digest, total: Number(p.total), completed: Number(p.completed), done: p.done }
        handlers.onProgress?.(progress)
        if (p.done) ok = true
      }
      if (!ok) throw new Error(`the pull of ${model} ended without success`)
    })()
    return { done, cancel: () => abort.abort() }
  }

  async deleteOllamaModel(model: string, providerId = '') {
    await this.client.deleteOllamaModel({ providerId, model })
  }
}
