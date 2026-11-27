// ImagesApi: what Settings > Images needs from harness.llm.v1.ImageService (docs/design/images.md). The server decides
// who may see and change what; the views carry its answers (usable, editable, canManage).

export interface ImagePricingView {
  // USD per returned image, and per 1M input / output tokens.
  perImage: number
  input: number
  output: number
}

export interface ImageModelView {
  id: string
  name: string
  providerId: string
  providerName: string
  upstreamModel: string
  // 'openai', 'gemini', 'imagen' or 'openrouter'.
  api: string
  edits: boolean
  // Global models, admins only; empty: everyone.
  roles: string[]
  enabled: boolean
  editable: boolean
  pricing: ImagePricingView | null
  personal: boolean
  usable: boolean
}

export interface ImageProviderView {
  id: string
  name: string
  personal: boolean
  api: string
}

export interface ImageSettingsView {
  models: ImageModelView[]
  providers: ImageProviderView[]
  // The server default ('' none).
  defaultModel: string
  canManage: boolean
  roles: string[]
}

export interface AddImageModelInput {
  providerId: string
  upstreamModel: string
  id?: string
  name?: string
  roles?: string[]
  pricing?: ImagePricingView
}

export interface ImageModelPatch {
  name?: string
  enabled?: boolean
  roles?: string[]
  pricing?: ImagePricingView | null
}

export interface ImagesApi {
  settings(): Promise<ImageSettingsView>
  setDefault(modelId: string): Promise<string>
  add(input: AddImageModelInput): Promise<ImageModelView>
  update(modelId: string, patch: ImageModelPatch): Promise<ImageModelView>
  remove(modelId: string): Promise<void>
}

const API_LABELS: Record<string, string> = { openai: 'OpenAI images API', gemini: 'Gemini API', imagen: 'Imagen', openrouter: 'OpenRouter' }
export const imageApiLabel = (api: string) => API_LABELS[api] ?? api
