// ProvidersApi: what the settings screens need from harness.llm.v1.ProviderService (LLM providers, their
// models and the auto-mode classifier). The client forwards these calls to the server with the user's
// credentials, so the UI never holds server credentials. Two implementations: connect.ts (the real
// service through the client) and mock.ts (in memory, for the mock transport).

export type ProviderScopeName = 'global' | 'personal'

export interface ProviderModelView {
  id: string
  name: string
  upstream: string
  contextWindow: number
}

export interface ProviderView {
  id: string
  scope: ProviderScopeName
  type: string
  name: string
  baseUrl: string
  // Keys are write-only: only whether one is set, and its last characters.
  keySet: boolean
  keyHint: string
  enabled: boolean
  allowPrivateNetwork: boolean
  // The caller may change it (admins: global providers; everyone: their own).
  editable: boolean
  models: ProviderModelView[]
}

export interface ProviderList {
  providers: ProviderView[]
  types: string[]
  canManageGlobal: boolean
}

export interface ProviderDraft {
  scope: ProviderScopeName
  // Global only; personal providers get a generated id.
  id?: string
  type: string
  name: string
  baseUrl: string
  apiKey?: string
  allowPrivateNetwork?: boolean
}

// Unset fields keep their value; apiKey '' removes the key.
export interface ProviderPatch {
  type?: string
  name?: string
  baseUrl?: string
  apiKey?: string
  enabled?: boolean
  allowPrivateNetwork?: boolean
}

export interface ModelCandidateView {
  upstream: string
  name: string
  contextWindow: number
  // Ids of this provider's models that already use it.
  modelIds: string[]
}

export interface NewModel {
  upstream: string
  name?: string
  contextWindow?: number
  // Global providers only: the catalog alias.
  id?: string
}

export interface ClassifierSettings {
  enabled: boolean
  providerId: string
  endpoint: string
  model: string
  minProbability: number
  minMargin: number
  onUncertain: 'next' | 'ask'
  timeoutMs: number
  llmModel: string
}

export interface ProvidersApi {
  list(): Promise<ProviderList>
  create(draft: ProviderDraft): Promise<ProviderView>
  update(id: string, patch: ProviderPatch): Promise<ProviderView>
  remove(id: string): Promise<void>
  discover(id: string): Promise<ModelCandidateView[]>
  addModel(providerId: string, model: NewModel): Promise<void>
  removeModel(modelId: string): Promise<void>
  // Admin only.
  getClassifier(): Promise<ClassifierSettings>
  setClassifier(c: ClassifierSettings): Promise<ClassifierSettings>
}

// The settings form for a new or edited provider.
export interface ProviderForm {
  // Provider being edited; empty for a new one.
  id: string
  scope: ProviderScopeName
  // New global providers: the id to create.
  newId: string
  type: string
  name: string
  baseUrl: string
  // A new key; empty keeps the stored one.
  apiKey: string
  clearKey: boolean
  allowPrivateNetwork: boolean
}

// Human name of a provider type.
export const PROVIDER_TYPE_LABELS: Record<string, string> = {
  'anthropic': 'Anthropic Messages',
  'openai-chat': 'OpenAI Chat Completions',
  'openai-responses': 'OpenAI Responses'
}

// Base URL suggested for a type (OpenAI-compatible URLs include /v1).
export const PROVIDER_TYPE_URLS: Record<string, string> = {
  'anthropic': 'https://api.anthropic.com',
  'openai-chat': 'https://api.openai.com/v1',
  'openai-responses': 'https://api.openai.com/v1'
}
