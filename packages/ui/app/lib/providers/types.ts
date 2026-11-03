// ProvidersApi: what the settings screens need from harness.llm.v1.ProviderService (providers in two groups,
// LLM models and classifiers; the presets to connect; their models; the default model per role; the auto-mode
// classifier). The client
// forwards these calls to the server with the user's credentials, so the UI never holds server credentials.
// Two implementations: connect.ts (the real service through the client) and mock.ts (in memory, for the
// mock transport).

export type ProviderScopeName = 'global' | 'personal'
export type ProviderGroupName = 'llm' | 'classifier'

export interface ProviderModelView {
  id: string
  name: string
  upstream: string
  contextWindow: number
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
  removeModel(modelId: string): Promise<void>
  getSlots(): Promise<SlotSettings>
  // Admin only: replaces main, small, plan and title.
  setSlots(s: ModelSlots): Promise<ModelSlots>
  // Admin only.
  getClassifier(): Promise<ClassifierSettings>
  setClassifier(c: ClassifierSettings): Promise<ClassifierSettings>
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
