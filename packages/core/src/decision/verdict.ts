/** Validation of decisions returned by the classifier tool call and by policy webhooks. */
import { RULE_CATEGORIES } from "./rules.ts";
import { DECISION_VALUES, type DecisionValue, type ProviderDecision, type RiskCategoryName } from "./types.ts";

const MAX_REASON = 500;

/**
 * Parses `{decision, reason, categories?, confidence?}`. Decision and categories are accepted in lowercase
 * ("allow", "secret_exfiltration") or as proto enum names ("DECISION_ALLOW", "RISK_CATEGORY_DESTRUCTIVE");
 * unknown category names are dropped. Returns an error string when the shape is wrong.
 */
export function parseVerdict(raw: unknown, opts: { requireReason?: boolean } = {}): ProviderDecision | string {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return "not an object";
  const o = raw as Record<string, unknown>;
  if (typeof o.decision !== "string") return "decision is missing";
  const decision = o.decision.trim().toLowerCase().replace(/^decision_/, "") as DecisionValue;
  if (!DECISION_VALUES.includes(decision)) return `unknown decision "${o.decision}"`;
  if (o.reason !== undefined && typeof o.reason !== "string") return "reason must be a string";
  const reason = typeof o.reason === "string" ? o.reason.trim().replace(/\s+/g, " ").slice(0, MAX_REASON) : "";
  if (opts.requireReason && !reason) return "reason is missing";
  let categories: RiskCategoryName[] = [];
  if (o.categories !== undefined && o.categories !== null) {
    if (!Array.isArray(o.categories)) return "categories must be an array";
    const set = new Set<string>();
    for (const c of o.categories) {
      if (typeof c !== "string") return "categories must be strings";
      set.add(c.trim().toLowerCase().replace(/^risk_category_/, ""));
    }
    categories = RULE_CATEGORIES.filter((c) => set.has(c));
  }
  let confidence: number | undefined;
  if (o.confidence !== undefined && o.confidence !== null) {
    if (typeof o.confidence !== "number" || !Number.isFinite(o.confidence) || o.confidence < 0 || o.confidence > 1) {
      return "confidence must be a number between 0 and 1";
    }
    confidence = o.confidence;
  }
  return { decision, reason, categories, ...(confidence !== undefined ? { confidence } : {}) };
}
