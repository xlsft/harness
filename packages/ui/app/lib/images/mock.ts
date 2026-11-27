// In-memory ImagesApi for the mock transport (demo and tests). The mock user is an admin.
import type { AddImageModelInput, ImageModelPatch, ImageModelView, ImagesApi, ImageSettingsView } from './types'

export class MockImagesApi implements ImagesApi {
  private models: ImageModelView[] = [
    { id: 'gpt-image', name: 'GPT Image', providerId: 'openai', providerName: 'OpenAI', upstreamModel: 'gpt-image-1', api: 'openai', edits: true, roles: [], enabled: true, editable: true, pricing: { perImage: 0, input: 5, output: 40 }, personal: false, usable: true }
  ]

  private defaultModel = 'gpt-image'

  async settings(): Promise<ImageSettingsView> {
    return {
      models: this.models.map(m => ({ ...m })),
      providers: [{ id: 'openai', name: 'OpenAI', personal: false, api: 'openai' }, { id: 'gemini', name: 'Google Gemini', personal: false, api: 'gemini' }],
      defaultModel: this.defaultModel,
      canManage: true,
      roles: ['user', 'admin', 'auditor', 'billing']
    }
  }

  async setDefault(modelId: string) {
    if (modelId && !this.models.some(m => m.id === modelId)) throw new Error(`Unknown model ${modelId}`)
    this.defaultModel = modelId
    return modelId
  }

  async add(input: AddImageModelInput) {
    const id = input.id || input.upstreamModel
    if (this.models.some(m => m.id === id)) throw new Error(`Model ${id} already exists`)
    const api = input.providerId === 'gemini' ? (/^imagen/.test(input.upstreamModel) ? 'imagen' : 'gemini') : 'openai'
    const m: ImageModelView = { id, name: input.name || input.upstreamModel, providerId: input.providerId, providerName: input.providerId, upstreamModel: input.upstreamModel, api, edits: api !== 'imagen', roles: input.roles ?? [], enabled: true, editable: true, pricing: input.pricing ?? null, personal: false, usable: true }
    this.models.push(m)
    return { ...m }
  }

  async update(modelId: string, patch: ImageModelPatch) {
    const m = this.models.find(x => x.id === modelId)
    if (!m) throw new Error(`Image model ${modelId} not found`)
    if (patch.name !== undefined) m.name = patch.name
    if (patch.enabled !== undefined) m.enabled = patch.enabled
    if (patch.roles !== undefined) m.roles = patch.roles
    if (patch.pricing !== undefined) m.pricing = patch.pricing
    return { ...m }
  }

  async remove(modelId: string) {
    this.models = this.models.filter(m => m.id !== modelId)
    if (this.defaultModel === modelId) this.defaultModel = ''
  }
}
