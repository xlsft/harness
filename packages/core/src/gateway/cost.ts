import type { ModelPricing } from "../services/models.ts";
import type { Usage } from "./types.ts";

export function emptyUsage(): Usage {
  return { input_tokens: 0, output_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 };
}

export function mergeUsage(base: Usage, patch: Partial<Usage> | undefined): Usage {
  if (!patch) return base;
  const out = { ...base };
  for (const k of Object.keys(out) as (keyof Usage)[]) {
    const v = patch[k];
    if (typeof v === "number" && Number.isFinite(v)) out[k] = v;
  }
  return out;
}

/** USD cost for normalized usage; pricing is USD per 1M tokens. Missing cache prices fall back to input price. */
export function computeCost(u: Usage, p: ModelPricing | null | undefined): number {
  if (!p) return 0;
  const cost =
    (u.input_tokens * p.input +
      u.output_tokens * p.output +
      u.cache_read_input_tokens * (p.cache_read ?? p.input) +
      u.cache_creation_input_tokens * (p.cache_write ?? p.input)) /
    1_000_000;
  return Math.round(cost * 1e8) / 1e8;
}

/** Rough token estimate used when exact counting is unavailable (~4 chars/token, fixed cost per image/PDF). */
export function estimateTokens(value: unknown): number {
  let chars = 0;
  let media = 0;
  const walk = (v: unknown, key?: string) => {
    if (typeof v === "string") {
      if (key === "data") media++;
      else chars += v.length;
    } else if (Array.isArray(v)) v.forEach((x) => walk(x));
    else if (v && typeof v === "object") for (const [k, x] of Object.entries(v)) walk(x, k);
  };
  walk(value);
  return Math.ceil(chars / 4) + media * 1600;
}
