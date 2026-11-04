// ProvidersApi: what the settings screens need from harness.llm.v1.ProviderService (providers in three groups,
// LLM models, classifiers and web search; the presets to connect; their models; the default model per role; the
// auto-mode classifier; the search provider in use). The client
// forwards these calls to the server with the user's credentials, so the UI never holds server credentials.
// Two implementations: connect.ts (the real service through the client) and mock.ts (in memory, for the
// mock transport).

import type { EffortName } from '~/lib/session/types'

export type ProviderScopeName = 'global' | 'personal'
export type ProviderGroupName = 'llm' | 'classifier' | 'search'
// LLM providers: 'metered' (pay as you go: priced models count cost) or 'plan' (a subscription / coding plan:
// tokens only, never cost). Other groups: ''.
export type BillingName = 'metered' | 'plan' | ''

export interface ProviderModelView {
  id: string
  name: string
  upstream: string
  contextWindow: number
  // Effort levels the model takes, lowest first.
  efforts: EffortName[]
}

export interface ProviderView {
  id: string
  scope: ProviderScopeName
  group: ProviderGroupName
  // The preset it was connected from; '' for a custom provider.
  presetId: string
  type: string
  name: string
  baseUrl: string
  // Keys are write-only: only whether one is set, and its last characters.
  keySet: boolean
  keyHint: string
  headerNames: string[]
  enabled: boolean
  allowPrivateNetwork: boolean
  // The caller may change it (admins: global providers; everyone: their own).
  editable: boolean
  models: ProviderModelView[]
  // Search providers: their options by name (see PresetView.options); unset ones follow the default.
  options?: Record<string, string>
  // From its preset; custom providers are 'metered'.
  billing?: BillingName
}

export interface ProviderList {
  providers: ProviderView[]
  types: string[]
  canManageGlobal: boolean
}

export interface PresetFieldView {
  name: 'api_key' | 'base_url' | string
  label: string
  required: boolean
  placeholder: string
}

export interface PresetModelView {
  upstream: string
  name: string
  contextWindow: number
}

// A well-known service the user connects by entering only its fields.
export interface PresetView {
  id: string
  group: ProviderGroupName
  name: string
  description: string
  type: string
  baseUrl: string
  fields: PresetFieldView[]
  docsUrl: string
  keyUrl: string
  defaultModels: PresetModelView[]
  // Monogram drawn as the icon.
  icon: string
  discoversModels: boolean
  // Runs on the local network: only a global connection may reach it.
  local: boolean
  // Search presets: the settings a connected provider may change.
  options?: PresetOptionView[]
  // Ships with the server (the bundled SearXNG): nothing to connect; its provider id is the preset id.
  builtin?: boolean
  billing?: BillingName
}

export interface PresetOptionView {
  name: string
  label: string
  kind: 'number' | 'select' | 'text'
  choices: { value: string, label: string }[]
  defaultValue: string
  placeholder: string
  min: number
  max: number
}

export interface ProtocolView {
  id: string
  group: ProviderGroupName
  label: string
  baseUrlHint: string
}

export interface PresetList {
  presets: PresetView[]
  protocols: ProtocolView[]
}

export interface ProviderDraft {
  scope: ProviderScopeName
  // Connect a preset: only its fields are needed.
  presetId?: string
  // Custom providers.
  group?: ProviderGroupName
  type?: string
  name?: string
  baseUrl?: string
  apiKey?: string
  headers?: Record<string, string>
  allowPrivateNetwork?: boolean
  // Search providers.
  options?: Record<string, string>
}

// Unset fields keep their value; apiKey '' removes the key.
export interface ProviderPatch {
  type?: string
  name?: string
  baseUrl?: string
  apiKey?: string
  // Replaces all extra headers.
  headers?: Record<string, string>
  enabled?: boolean
  allowPrivateNetwork?: boolean
  // Search providers: replaces all options ('' values follow the default).
  options?: Record<string, string>
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

export interface TestResult {
  ok: boolean
  message: string
  modelCount: number
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

// A user's own classifier over the server default: 'default' follows it, 'none' runs no classifier (rules,
// then the model), 'provider' uses providerId. llmModel '' follows the server default's model.
export type ClassifierChoiceName = 'default' | 'none' | 'provider'

export interface ClassifierOverride {
  choice: ClassifierChoiceName
  providerId: string
  llmModel: string
}

export interface MyClassifier {
  mine: ClassifierOverride
  // The server default as configured: its classifier ('' none) and model ('' the classifier / small slot).
  defaultProviderId: string
  defaultLlmModel: string
}

// The search provider serving web searches: the server default (admins set it; 'searxng' is the built-in
// SearXNG, which also applies when nothing else is chosen) and the caller's own choice ('' follows it).
export interface SearchSettings {
  defaultProviderId: string
  myProviderId: string
  effectiveProviderId: string
  // The caller may change the default (admins).
  editable: boolean
}

export const BUILTIN_SEARCH_ID = 'searxng'

// Server-wide default model per role, as configured ('' follows the server default). `title` falls back to
// `small`; `small` also serves summaries, web fetch and compaction.
export interface ModelSlots {
  main: string
  small: string
  plan: string
  title: string
}

export interface SlotSettings {
  slots: ModelSlots
  // The caller may change them (admins).
  editable: boolean
  // What each slot resolves to while empty ("Server default").
  defaults: ModelSlots
}

export const emptySlots = (): ModelSlots => ({ main: '', small: '', plan: '', title: '' })

// The models a slot may point at: global LLM models, grouped by provider.
export function slotModelOptions(providers: ProviderView[]): { value: string, label: string, hint?: string, group?: string }[] {
  return providers
    .filter(p => p.scope === 'global' && p.group === 'llm')
    .flatMap(p => p.models.map(m => ({ value: m.id, label: m.name, hint: contextLabel(m.contextWindow) || undefined, group: p.name })))
}

export interface ProvidersApi {
  presets(): Promise<PresetList>
  list(): Promise<ProviderList>
  create(draft: ProviderDraft): Promise<ProviderView>
  update(id: string, patch: ProviderPatch): Promise<ProviderView>
  remove(id: string): Promise<void>
  discover(id: string): Promise<ModelCandidateView[]>
  test(id: string): Promise<TestResult>
  addModel(providerId: string, model: NewModel): Promise<void>
  // Sets by hand the levels a model takes, for lists that do not say.
  setModelEfforts(modelId: string, efforts: EffortName[]): Promise<void>
  removeModel(modelId: string): Promise<void>
  getSlots(): Promise<SlotSettings>
  // Admin only: replaces main, small, plan and title.
  setSlots(s: ModelSlots): Promise<ModelSlots>
  // Admin only: the server default.
  getClassifier(): Promise<ClassifierSettings>
  setClassifier(c: ClassifierSettings): Promise<ClassifierSettings>
  // Anyone: the caller's own override and the server default it applies over.
  getMyClassifier(): Promise<MyClassifier>
  setMyClassifier(o: ClassifierOverride): Promise<MyClassifier>
  // Web search: anyone reads it and sets their own choice; the default is admin only.
  getSearch(): Promise<SearchSettings>
  setSearchDefault(providerId: string): Promise<SearchSettings>
  setMySearch(providerId: string): Promise<SearchSettings>
}

// Parses "Name: value" lines into headers; returns an error string for a malformed line.
export function parseHeaders(text: string): Record<string, string> | string {
  const out: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const i = line.indexOf(':')
    const name = i > 0 ? line.slice(0, i).trim() : ''
    if (!/^[A-Za-z0-9-]+$/.test(name)) return `Not a header: ${line}`
    out[name] = line.slice(i + 1).trim()
  }
  return out
}

// "1M", "256k", or '' when unknown.
export function contextLabel(n: number): string {
  if (!n) return ''
  return n >= 1_000_000 ? `${Math.round(n / 100_000) / 10}M` : `${Math.round(n / 1000)}k`
}
