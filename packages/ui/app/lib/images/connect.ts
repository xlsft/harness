// ImagesApi over harness.llm.v1.ImageService, on the agent transport: the client forwards it to the server
// (packages/client/server/coreproxy.go).
import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { ImageService, type ImageModel, type ImagePricing } from '~/gen/harness/llm/v1/images_pb'
import { ProviderScope } from '~/gen/harness/llm/v1/providers_pb'
import type { AddImageModelInput, ImageModelPatch, ImageModelView, ImagePricingView, ImagesApi, ImageSettingsView } from './types'

function pricingView(p: ImagePricing | undefined): ImagePricingView | null {
  return p ? { perImage: p.perImage, input: p.input, output: p.output } : null
}

export function imageModelView(m: ImageModel): ImageModelView {
  return {
    id: m.id,
    name: m.displayName || m.id,
    providerId: m.providerId,
    providerName: m.providerName,
    upstreamModel: m.upstreamModel,
    api: m.imageApi,
    edits: m.edits,
    roles: m.roles,
    enabled: m.enabled,
    editable: m.editable,
    pricing: pricingView(m.pricing),
    personal: m.personal,
    usable: m.usable
  }
}

export class ConnectImagesApi implements ImagesApi {
  private client: Client<typeof ImageService>

  constructor(transport: Transport) {
    this.client = createClient(ImageService, transport)
  }

  async settings(): Promise<ImageSettingsView> {
    const r = await this.client.getImageSettings({})
    return {
      models: r.models.map(imageModelView),
      providers: r.providers.map(p => ({ id: p.id, name: p.displayName || p.id, personal: p.scope === ProviderScope.PERSONAL, api: p.imageApi })),
      defaultModel: r.defaultModel,
      canManage: r.canManage,
      roles: r.roles
    }
  }

  async setDefault(modelId: string) {
    return (await this.client.setImageDefault({ modelId })).defaultModel
  }

  async add(input: AddImageModelInput) {
    const r = await this.client.addImageModel({ providerId: input.providerId, upstreamModel: input.upstreamModel, id: input.id ?? '', displayName: input.name ?? '', roles: input.roles ?? [], pricing: input.pricing })
    return imageModelView(r.model!)
  }

  async update(modelId: string, patch: ImageModelPatch) {
    const r = await this.client.updateImageModel({
      modelId,
      displayName: patch.name,
      enabled: patch.enabled,
      roles: patch.roles ? { roles: patch.roles } : undefined,
      pricing: patch.pricing ?? undefined,
      clearPricing: patch.pricing === null
    })
    return imageModelView(r.model!)
  }

  async remove(modelId: string) {
    await this.client.removeImageModel({ modelId })
  }
}
