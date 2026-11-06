/**
 * Speed tiers and cache lifetimes a model has, from what its provider documents (read November 2026). Worked out here, not
 * stored: a provider that gains or loses a tier needs a server release and no migration.
 *
 * Tiers, as clients name them:
 *   - "standard": what a request gets with nothing asked for;
 *   - "fast": faster output at a higher price. Anthropic's fast mode (`speed: "fast"`, first-party API only, some Opus
 *     models), OpenAI's Fast mode, formerly Priority (`service_tier: "priority"`, which stays valid for it);
 *   - "flex": slower and cheaper. OpenAI's flex processing (`service_tier: "flex"`).
 *
 * `speed_multipliers` is what the tier does to the model's standard price. These are the vendors' published prices, read on
 * 2026-11-06: platform.claude.com/docs/en/about-claude/pricing (Opus 5.5 $4 to $8, Opus 5 and 4.8 $5 to $10, input; the same
 * on output) and developers.openai.com/api/docs/pricing (short context, up to 272K input tokens; the page gives other rates
 * above that for some models, and a prompt that long is billed here at the short-context ratio). OpenAI's ratio differs by
 * model, and a model the page lists without a tier does not have it, so its table names models exactly. A model not in it gets
 * no tier: a request cannot then be billed at a guess. OpenAI's Ultrafast tier is not offered here.
 *
 * Cache lifetimes: Anthropic's prompt cache lives 5 minutes unless a breakpoint asks for an hour, on every platform that
 * serves its models. OpenAI's Responses API keeps the cached prefix in memory for minutes unless asked to keep it for 24 hours.
 */
export const SPEED_TIERS = ["standard", "fast", "flex"] as const;
export const CACHE_TTLS = ["5m", "1h", "24h"] as const;
export type SpeedTier = (typeof SPEED_TIERS)[number];
export type CacheTtl = (typeof CACHE_TTLS)[number];

export interface ModelTiers {
  speed_tiers: SpeedTier[];
  cache_ttls: CacheTtl[];
  /** Price multiplier of a tier over the standard price; a tier not named costs the same. */
  speed_multipliers: Partial<Record<SpeedTier, number>>;
}

const NONE: ModelTiers = { speed_tiers: [], cache_ttls: [], speed_multipliers: {} };

const ANTHROPIC_FAST = /^claude-opus-(5-5|5|4-8)\b/;

/** OpenAI's models with a tier, and each tier's price over standard. */
const OPENAI_TIERS: Readonly<Record<string, { fast?: number; flex?: number }>> = {
  "gpt-6-astra": { fast: 2, flex: 0.5 },
  "gpt-6.1-sol": { fast: 2, flex: 0.5 },
  "gpt-6.1-terra": { fast: 2, flex: 0.5 },
  "gpt-6-sol": { fast: 2, flex: 0.5 },
  "gpt-6-luna": { fast: 2, flex: 0.5 },
  "gpt-5.6-sol": { fast: 2, flex: 0.5 },
  "gpt-5.6-terra": { fast: 2, flex: 0.5 },
  "gpt-5.6-luna": { fast: 2, flex: 0.5 },
  "gpt-5.5": { fast: 2.5, flex: 0.5 },
  "gpt-5.5-pro": { flex: 0.5 },
  "gpt-5.4": { fast: 2, flex: 0.5 },
  "gpt-5.4-pro": { flex: 0.5 },
  "gpt-5.4-mini": { fast: 2, flex: 0.5 },
  "gpt-5.4-nano": { flex: 0.5 },
  "gpt-5.3-codex": { fast: 2 },
  "gpt-5.2": { fast: 2, flex: 0.5 },
  "gpt-5.1": { fast: 2, flex: 0.5 },
  "gpt-5": { fast: 2, flex: 0.5 },
  "gpt-5-mini": { fast: 1.8, flex: 0.5 },
  "gpt-5-nano": { flex: 0.5 },
  o3: { fast: 1.75, flex: 0.5 },
};

export interface TierSource {
  presetId: string | null;
  type: string;
  upstreamModel: string;
  promptCache: boolean;
}

export function modelTiers(s: TierSource): ModelTiers {
  const anthropicFamily = s.type === "anthropic" || s.type === "bedrock-invoke" || s.type === "vertex-anthropic";
  if (anthropicFamily) {
    const fast = s.presetId === "anthropic" && ANTHROPIC_FAST.test(s.upstreamModel);
    return {
      speed_tiers: fast ? ["standard", "fast"] : [],
      cache_ttls: s.promptCache ? ["5m", "1h"] : [],
      speed_multipliers: fast ? { fast: 2 } : {},
    };
  }
  // A dated snapshot ("gpt-5.5-2026-04-23") costs what its model does.
  const tiers = s.presetId === "openai" ? OPENAI_TIERS[s.upstreamModel.replace(/-\d{4}-\d{2}-\d{2}$/, "")] : undefined;
  if (tiers) {
    return {
      speed_tiers: ["standard", ...(tiers.fast ? (["fast"] as const) : []), ...(tiers.flex ? (["flex"] as const) : [])],
      cache_ttls: s.type === "openai-responses" ? ["5m", "24h"] : [],
      speed_multipliers: tiers,
    };
  }
  return NONE;
}

/** What about a request changes its price. */
export interface BilledTier {
  multiplier?: number;
  cacheTtl?: CacheTtl;
}

/** The model's price for a request with this tier. No tier keeps the object, so nothing changes for a standard request. */
export function tieredPricing<T extends { input: number; output: number; cache_read?: number; cache_write?: number; long_context?: any }>(pricing: T | null, tier: BilledTier | undefined): T | null {
  if (!pricing || !tier) return pricing;
  const multiplier = tier.multiplier ?? 1;
  // Anthropic charges an hour-long cache write twice the input price, where a 5-minute one costs what the price says.
  const write = (p: any) => (tier.cacheTtl === "1h" ? { cache_write: p.input * 2 * multiplier } : p.cache_write !== undefined ? { cache_write: p.cache_write * multiplier } : {});
  const scale = (p: any) => ({
    ...p,
    input: p.input * multiplier,
    output: p.output * multiplier,
    ...(p.cache_read !== undefined && { cache_read: p.cache_read * multiplier }),
    ...write(p),
  });
  return { ...scale(pricing), ...(pricing.long_context && { long_context: scale(pricing.long_context) }) };
}
