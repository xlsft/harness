/** Connect implementation of harness.llm.v1.ImageService and GatewayService.GenerateImages: proto <-> src/images/service.ts. */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import type { GenerateImagesRequest } from "../gen/harness/llm/v1/gateway_pb.ts";
import { ImageService, type ImagePricing as ImagePricingMsg } from "../gen/harness/llm/v1/images_pb.ts";
import { ProviderScope } from "../gen/harness/llm/v1/providers_pb.ts";
import type { ImagePricing } from "../images/pricing.ts";
import { addImageModel, generateForUser, getImageSettings, removeImageModel, setImageDefault, updateImageModel, type ImageModelView } from "../images/service.ts";
import { checkClientVersion, requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

function pricingToProto(p: ImagePricing | null) {
  return p ? { perImage: p.per_image ?? 0, input: p.input ?? 0, output: p.output ?? 0 } : undefined;
}

function pricingFromProto(p: ImagePricingMsg | undefined): ImagePricing | undefined {
  return p ? { per_image: p.perImage, input: p.input, output: p.output } : undefined;
}

function modelToProto(m: ImageModelView) {
  return {
    id: m.id,
    displayName: m.display_name,
    providerId: m.provider_id,
    providerName: m.provider_name,
    upstreamModel: m.upstream_model,
    imageApi: m.image_api,
    edits: m.edits,
    roles: m.roles,
    enabled: m.enabled,
    editable: m.editable,
    pricing: pricingToProto(m.pricing),
    personal: m.personal,
    usable: m.usable,
  };
}

export function registerImageService(router: ConnectRouter): void {
  const user = async (ctx: HandlerContext) => {
    checkClientVersion(ctx);
    return (await requirePrincipal(ctx)).user;
  };
  router.service(ImageService, {
    getImageSettings: guard(async (_req, ctx: HandlerContext) => {
      const r = await getImageSettings(await user(ctx));
      return {
        models: r.models.map(modelToProto),
        providers: r.providers.map((p) => ({ id: p.id, displayName: p.display_name, scope: p.personal ? ProviderScope.PERSONAL : ProviderScope.GLOBAL, imageApi: p.image_api, editable: p.editable })),
        defaultModel: r.default_model,
        canManage: r.can_manage,
        roles: r.roles,
      };
    }),
    setImageDefault: guard(async (req, ctx: HandlerContext) => ({ defaultModel: await setImageDefault(await user(ctx), req.modelId) })),
    addImageModel: guard(async (req, ctx: HandlerContext) => {
      const m = await addImageModel(await user(ctx), {
        provider_id: req.providerId,
        upstream_model: req.upstreamModel,
        id: req.id,
        display_name: req.displayName,
        roles: req.roles,
        pricing: pricingFromProto(req.pricing),
      });
      return { model: modelToProto(m) };
    }),
    updateImageModel: guard(async (req, ctx: HandlerContext) => {
      const m = await updateImageModel(await user(ctx), req.modelId, {
        display_name: req.displayName,
        enabled: req.enabled,
        roles: req.roles ? req.roles.roles : undefined,
        pricing: pricingFromProto(req.pricing),
        clear_pricing: req.clearPricing,
      });
      return { model: modelToProto(m) };
    }),
    removeImageModel: guard(async (req, ctx: HandlerContext) => {
      await removeImageModel(await user(ctx), req.modelId);
      return {};
    }),
  });
}

/** GatewayService.GenerateImages. */
export async function generateImagesRpc(req: GenerateImagesRequest, ctx: HandlerContext) {
  const p = await requirePrincipal(ctx);
  const r = await generateForUser(p, {
    model: req.model,
    prompt: req.prompt,
    size: req.size,
    count: req.count,
    quality: req.quality,
    inputs: req.inputImages.map((i) => ({ data: i.data, mediaType: i.mediaType, name: i.name })),
    sessionId: req.metadata?.sessionId,
    agentId: req.metadata?.agentId,
    signal: ctx.signal,
  });
  return {
    images: r.images.map((i) => ({ data: i.data, mediaType: i.mediaType })),
    revisedPrompt: r.revisedPrompt ?? "",
    text: r.text ?? "",
    costUsd: r.costUsd,
    priced: r.priced,
    servedModel: r.servedModel,
    inputTokens: BigInt(r.inputTokens),
    outputTokens: BigInt(r.outputTokens),
  };
}
