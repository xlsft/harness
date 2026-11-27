/**
 * Prices of image models: per image and per 1M tokens (docs/design/images.md). The defaults below are approximate
 * list prices of well-known models, used when an image model is added without one; an admin (or the owner of a
 * personal provider) sets the real price in Settings > Images. Nothing refreshes them: the token price catalog says
 * nothing about images.
 */
import type { ImageApi } from "./adapters.ts";

export interface ImagePricing {
  /** USD per returned image. */
  per_image?: number;
  /** USD per 1M input / output tokens, where the upstream reports tokens. */
  input?: number;
  output?: number;
}

/** First match wins; patterns are tried against the upstream model id. Approximate, November 2026. */
const DEFAULTS: { api: ImageApi[]; pattern: RegExp; pricing: ImagePricing }[] = [
  { api: ["openai"], pattern: /^gpt-image-1-mini/i, pricing: { input: 2, output: 8 } },
  { api: ["openai"], pattern: /^gpt-image/i, pricing: { input: 5, output: 40 } },
  { api: ["openai"], pattern: /^dall-e-3/i, pricing: { per_image: 0.04 } },
  { api: ["openai"], pattern: /^dall-e-2/i, pricing: { per_image: 0.02 } },
  { api: ["gemini"], pattern: /pro-image/i, pricing: { input: 2, output: 120 } },
  { api: ["gemini"], pattern: /image/i, pricing: { input: 0.3, output: 30 } },
  { api: ["imagen"], pattern: /ultra/i, pricing: { per_image: 0.06 } },
  { api: ["imagen"], pattern: /fast/i, pricing: { per_image: 0.02 } },
  { api: ["imagen"], pattern: /./, pricing: { per_image: 0.04 } },
];

export function defaultImagePricing(api: ImageApi, upstreamModel: string): ImagePricing | null {
  const model = upstreamModel.replace(/^models\//, "");
  return DEFAULTS.find((d) => d.api.includes(api) && d.pattern.test(model))?.pricing ?? null;
}

/** USD for a request: per image, plus tokens. An upstream's own figure (OpenRouter) goes over the computed one. */
export function imageCost(p: ImagePricing | null, images: number, inputTokens: number, outputTokens: number, reported?: number): { cost: number; priced: boolean } {
  if (reported !== undefined) return { cost: round(reported), priced: true };
  if (!p) return { cost: 0, priced: false };
  const cost = (p.per_image ?? 0) * images + ((p.input ?? 0) * inputTokens + (p.output ?? 0) * outputTokens) / 1_000_000;
  return { cost: round(cost), priced: true };
}

const round = (n: number) => Math.round(n * 1e8) / 1e8;

export function validImagePricing(p: unknown): ImagePricing {
  const o = (p ?? {}) as Record<string, unknown>;
  const out: ImagePricing = {};
  for (const k of ["per_image", "input", "output"] as const) {
    const v = o[k];
    if (v === undefined || v === null) continue;
    if (typeof v !== "number" || !Number.isFinite(v) || v < 0) throw new Error(`pricing.${k} must be a non-negative number`);
    out[k] = v;
  }
  return out;
}
