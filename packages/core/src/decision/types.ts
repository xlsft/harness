/**
 * Decision providers behind harness.decision.v1.DecisionService (the client's `auto` permission mode).
 *
 * A provider judges one tool call and answers ALLOW, DENY or ASK. Providers are chained by
 * src/decision/service.ts in the admin-configured order: the first ALLOW or DENY wins, ASK falls through,
 * errors follow the chain's on_error policy, and a `rules` DENY is final.
 */
import type { Principal } from "../services/devices.ts";
import type { RuleCategory, RuleDecision } from "./rules.ts";

export type DecisionValue = RuleDecision;
export type RiskCategoryName = RuleCategory;
export type ProviderType = "rules" | "llm" | "http";

/** What the client told us; the fields of harness.decision.v1.ClientFacts. */
export interface ClientFactsInput {
  subcommands: string[];
  writePaths: string[];
  networkHosts: string[];
  insideGitRepo: boolean;
  gitBranch: string;
}

/** A validated DecideRequest. `input` is `inputJson` parsed (always a JSON object). */
export interface DecideInput {
  sessionId: string;
  toolName: string;
  inputJson: string;
  input: Record<string, unknown>;
  cwd: string;
  additionalDirectories: string[];
  recentUserMessages: string[];
  assistantRationale: string;
  facts: ClientFactsInput;
}

export interface DecisionContext {
  /** The requesting user; classifier usage is charged to them. */
  principal: Principal;
  signal?: AbortSignal;
}

export interface ProviderDecision {
  decision: DecisionValue;
  /** One sentence for the user (and, for DENY, for the model). */
  reason: string;
  categories: RiskCategoryName[];
  /** 0..1 when the provider reports it. */
  confidence?: number;
  /** USD spent on this decision (classifier calls). */
  costUsd?: number;
  /** Served from the decision cache. */
  cached?: boolean;
}

export interface DecisionProvider {
  /** "rules", "llm", "llm:<id>" or "http:<id>". */
  readonly id: string;
  readonly type: ProviderType;
  readonly displayName: string;
  decide(input: DecideInput, ctx: DecisionContext): Promise<ProviderDecision>;
  /** Cheap liveness check for status pages; must not throw. */
  healthy(signal?: AbortSignal): Promise<boolean>;
  /** Catalog model the provider uses (llm providers), "" when not applicable or unresolved. */
  model?(): Promise<string>;
}

/** A provider could not decide (timeout, upstream failure, misconfiguration). Handled by on_error. */
export class ProviderError extends Error {
  constructor(
    message: string,
    readonly code: "timeout" | "upstream" | "config" | "quota" | "invalid_response" = "upstream",
  ) {
    super(message);
  }
}

export const DECISION_VALUES: readonly DecisionValue[] = ["allow", "deny", "ask"];
