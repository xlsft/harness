/**
 * Transport-agnostic DecisionService (proto/harness/decision/v1/decision.proto): the provider chain,
 * request validation, per-user rate limiting and the audit log.
 *
 * Chain semantics (DecisionChain.decide):
 *   1. every `rules` provider runs first; a rules DENY is final, even if a later provider would allow;
 *   2. then providers run in the configured order: the first ALLOW or DENY decides, ASK falls through
 *      unless the provider marks it final (a `decision_model` sure that the user should decide);
 *   3. a provider error follows on_error: "ask" (default) answers ASK at once, "next" falls through;
 *   4. when nobody decides the answer is ASK from provider "none".
 */
import { and, desc, eq, type SQL } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId, sha256 } from "../lib/ids.ts";
import { TokenBucketLimiter } from "../lib/rate-limit.ts";
import type { Principal } from "../services/devices.ts";
import { cacheFor } from "./cache.ts";
import { decisionModelKeyResolver, httpCredentialsResolver, loadDecisionConfig, type DecisionConfig, type OnErrorPolicy, type RulesExtras } from "./config.ts";
import { DecisionModelProvider, decisionEndpointUrl } from "./decision-model.ts";
import { HttpProvider } from "./http.ts";
import { LlmProvider } from "./llm.ts";
import { RulesProvider } from "./rules-provider.ts";
import { BUILTIN_RULES_VERSION, DEFAULT_PROTECTED_BRANCHES } from "./rules.ts";
import {
  ProviderError,
  type ClientFactsInput,
  type DecideInput,
  type DecisionContext,
  type DecisionProvider,
  type ProviderDecision,
  type ProviderType,
} from "./types.ts";

export interface ChainResult extends ProviderDecision {
  /** Provider that decided, or "none". */
  provider: string;
  cached: boolean;
  costUsd: number;
  latencyMs: number;
  /** Provider failures met on the way (on_error applied). */
  errors: string[];
}

export class DecisionChain {
  constructor(
    readonly providers: DecisionProvider[],
    readonly onError: OnErrorPolicy = "ask",
  ) {}

  /** A chain of just one provider (admin debugging: DecideRequest.provider). */
  only(id: string): DecisionChain {
    const p = this.providers.find((x) => x.id === id);
    if (!p) throw new ApiError("invalid_request", `Unknown decision provider "${id}"`);
    return new DecisionChain([p], this.onError);
  }

  async decide(input: DecideInput, ctx: DecisionContext): Promise<ChainResult> {
    const started = Date.now();
    const errors: string[] = [];
    let cost = 0;
    const done = new Map<DecisionProvider, ProviderDecision | ProviderError>();

    const run = async (p: DecisionProvider): Promise<ProviderDecision | ProviderError> => {
      const known = done.get(p);
      if (known) return known;
      let r: ProviderDecision | ProviderError;
      try {
        r = await p.decide(input, ctx);
        cost += r.costUsd ?? 0;
      } catch (err) {
        if (ctx.signal?.aborted) throw err;
        r = err instanceof ProviderError ? err : new ProviderError((err as Error).message || String(err));
        if (!(err instanceof ProviderError)) console.error(`[harness] decision provider ${p.id} failed`, err);
        errors.push(`${p.id}: ${r.message}`);
      }
      done.set(p, r);
      return r;
    };
    const finish = (provider: string, d: ProviderDecision): ChainResult => ({
      decision: d.decision,
      reason: d.reason,
      categories: d.categories,
      ...(d.confidence !== undefined ? { confidence: d.confidence } : {}),
      provider,
      cached: !!d.cached,
      costUsd: cost,
      latencyMs: Date.now() - started,
      errors,
    });

    for (const p of this.providers.filter((x) => x.type === "rules")) {
      const r = await run(p);
      if (!(r instanceof ProviderError) && r.decision === "deny") return finish(p.id, r);
    }
    let lastAsk: ProviderDecision | undefined;
    for (const p of this.providers) {
      const r = await run(p);
      if (r instanceof ProviderError) {
        if (this.onError === "next") continue;
        return finish(p.id, { decision: "ask", reason: `${p.displayName} could not decide: ${r.message}`, categories: [] });
      }
      if (r.decision !== "ask" || r.final) return finish(p.id, r);
      lastAsk = r;
    }
    return finish("none", {
      decision: "ask",
      reason: lastAsk?.reason || (errors.length ? "No decision provider could decide" : "No decision provider made a decision"),
      categories: lastAsk?.categories ?? [],
      ...(lastAsk?.confidence !== undefined ? { confidence: lastAsk.confidence } : {}),
    });
  }
}

// --- registry -----------------------------------------------------------------------------

export function buildProviders(cfg: DecisionConfig): DecisionProvider[] {
  return cfg.providers.map((p): DecisionProvider => {
    switch (p.type) {
      case "rules":
        return new RulesProvider(
          {
            extraDeny: cfg.rules.extra_deny,
            extraAllow: cfg.rules.extra_allow,
            protectedBranches: cfg.rules.protected_branches,
            denyAllForcePush: cfg.rules.deny_all_force_push,
          },
          p.display_name,
        );
      case "llm":
        return new LlmProvider({
          id: p.id,
          displayName: p.display_name,
          settings: p.settings,
          classifierModel: cfg.classifier_model,
          cache: cacheFor(p.id),
          cacheTtlMs: cfg.cache_ttl_seconds * 1000,
        });
      case "http": {
        return new HttpProvider({
          id: p.id,
          displayName: p.display_name,
          url: p.settings.url,
          credentials: httpCredentialsResolver(p),
          allowPrivateNetwork: p.settings.allow_private_network,
          timeoutMs: p.settings.timeout_ms,
          healthUrl: p.settings.health_url,
        });
      }
      case "decision_model":
        return new DecisionModelProvider({
          id: p.id,
          displayName: p.display_name,
          url: decisionEndpointUrl(p.settings.endpoint),
          model: p.settings.model,
          apiKey: decisionModelKeyResolver(p),
          allowPrivateNetwork: p.settings.allow_private_network,
          timeoutMs: p.settings.timeout_ms,
          minProbability: p.settings.min_probability,
          minMargin: p.settings.min_margin,
          onUncertain: p.settings.on_uncertain,
          cache: cacheFor(p.id),
          cacheTtlMs: cfg.cache_ttl_seconds * 1000,
        });
    }
  });
}

let current: { version: string; chain: DecisionChain; config: DecisionConfig } | undefined;
let override: DecisionChain | undefined;

/** The chain built from the stored configuration (rebuilt when it changes). */
export async function decisionChain(): Promise<{ chain: DecisionChain; config: DecisionConfig }> {
  const loaded = await loadDecisionConfig();
  if (override) return { chain: override, config: loaded.config };
  if (!current || current.version !== loaded.version) {
    current = { version: loaded.version, chain: new DecisionChain(buildProviders(loaded.config), loaded.config.on_error), config: loaded.config };
  }
  return { chain: current.chain, config: current.config };
}

/** For tests: replaces the configured chain (undefined restores it). */
export function setDecisionChainForTests(chain?: DecisionChain): void {
  override = chain;
  current = undefined;
}

// --- request validation --------------------------------------------------------------------

const MAX_INPUT_JSON = 256 * 1024;
const MAX_MESSAGES = 20;
const MAX_TEXT = 16 * 1024;

export interface DecideRequestInput {
  sessionId?: string;
  toolName: string;
  inputJson: string;
  cwd?: string;
  additionalDirectories?: string[];
  recentUserMessages?: string[];
  assistantRationale?: string;
  facts?: Partial<ClientFactsInput>;
  provider?: string;
}

const strings = (v: string[] | undefined, max: number, each: number) => (v ?? []).slice(-max).map((s) => String(s).slice(0, each));

export function validateDecideRequest(r: DecideRequestInput): DecideInput {
  const toolName = (r.toolName ?? "").trim();
  if (!toolName) throw new ApiError("invalid_request", "tool_name is required");
  if (toolName.length > 256) throw new ApiError("invalid_request", "tool_name is too long");
  const inputJson = r.inputJson || "{}";
  if (inputJson.length > MAX_INPUT_JSON) throw new ApiError("invalid_request", `input_json is larger than ${MAX_INPUT_JSON} bytes`);
  let input: unknown;
  try {
    input = JSON.parse(inputJson);
  } catch {
    throw new ApiError("invalid_request", "input_json must be valid JSON");
  }
  if (!input || typeof input !== "object" || Array.isArray(input)) throw new ApiError("invalid_request", "input_json must be a JSON object");
  const f = r.facts ?? {};
  return {
    sessionId: (r.sessionId ?? "").slice(0, 128),
    toolName,
    inputJson,
    input: input as Record<string, unknown>,
    cwd: (r.cwd ?? "").slice(0, 4096),
    additionalDirectories: strings(r.additionalDirectories, 64, 4096),
    recentUserMessages: strings(r.recentUserMessages, MAX_MESSAGES, MAX_TEXT),
    assistantRationale: (r.assistantRationale ?? "").slice(0, MAX_TEXT),
    facts: {
      subcommands: strings(f.subcommands, 256, 8192),
      writePaths: strings(f.writePaths, 256, 4096),
      networkHosts: strings(f.networkHosts, 64, 512),
      insideGitRepo: !!f.insideGitRepo,
      gitBranch: (f.gitBranch ?? "").slice(0, 256),
    },
  };
}

// --- entry points ------------------------------------------------------------------------------

let limiter: TokenBucketLimiter | undefined;

function enforceRateLimit(userId: string, perMin: number): void {
  if (perMin <= 0) return;
  if (!limiter || limiter.capacity !== perMin) limiter = new TokenBucketLimiter(perMin, perMin / 60);
  const wait = limiter.take(userId);
  if (wait > 0) throw new ApiError("rate_limit", "Too many decision requests; slow down", { retryAfterMs: wait });
}

/** For tests. */
export function resetDecisionRateLimit(): void {
  limiter = undefined;
}

export async function decide(principal: Principal, req: DecideRequestInput, signal?: AbortSignal): Promise<ChainResult & { rulesVersion: string }> {
  const input = validateDecideRequest(req);
  const { chain: configured, config } = await decisionChain();
  let chain = configured;
  if (req.provider) {
    if (principal.user.role !== "admin") throw new ApiError("permission", "Only admins can force a decision provider");
    chain = configured.only(req.provider);
  }
  enforceRateLimit(principal.user.id, config.rate_limit_per_min);
  const result = await chain.decide(input, { principal, signal });
  await recordDecision(principal, input, result).catch((e) => console.error("[harness] failed to record decision", e));
  return { ...result, rulesVersion: rulesVersion(config.rules) };
}

// --- rule options for clients ------------------------------------------------------------------

/** The admin's rule options with every field present (older stored configs may lack some). */
export function normalizeRulesOptions(rules: Partial<RulesExtras> | undefined): RulesExtras {
  return {
    extra_deny: (rules?.extra_deny ?? []).map((d) => ({ pattern: d.pattern, ...(d.reason ? { reason: d.reason } : {}), categories: [...(d.categories ?? [])] })),
    extra_allow: [...(rules?.extra_allow ?? [])],
    protected_branches: rules?.protected_branches?.length ? [...rules.protected_branches] : [...DEFAULT_PROTECTED_BRANCHES],
    deny_all_force_push: !!rules?.deny_all_force_push,
  };
}

/**
 * Version of the rules a client should apply: the built-in rule set version plus a stable hash of the
 * admin's options. Changes whenever either changes; equal options give equal versions on every replica.
 */
export function rulesVersion(rules: Partial<RulesExtras> | undefined): string {
  const o = normalizeRulesOptions(rules);
  const canonical = JSON.stringify([
    o.extra_deny.map((d) => [d.pattern, d.reason ?? "", d.categories ?? []]),
    o.extra_allow,
    o.protected_branches,
    o.deny_all_force_push,
  ]);
  return `${BUILTIN_RULES_VERSION}.${sha256(canonical).slice(0, 16)}`;
}

/** GetRulesOptions: the configured rule options and their version (any authenticated user). */
export async function getRulesOptions(): Promise<{ rulesVersion: string; options: RulesExtras }> {
  const { config } = await loadDecisionConfig();
  return { rulesVersion: rulesVersion(config.rules), options: normalizeRulesOptions(config.rules) };
}

export interface ProviderStatus {
  id: string;
  type: ProviderType;
  displayName: string;
  healthy: boolean;
  model: string;
}

export async function listDecisionProviders(signal?: AbortSignal): Promise<{ providers: ProviderStatus[]; onError: OnErrorPolicy }> {
  const { chain } = await decisionChain();
  const providers = await Promise.all(
    chain.providers.map(async (p) => ({
      id: p.id,
      type: p.type,
      displayName: p.displayName,
      healthy: await p.healthy(signal).catch(() => false),
      model: p.model ? await p.model().catch(() => "") : "",
    })),
  );
  return { providers, onError: chain.onError };
}

// --- audit log ------------------------------------------------------------------------------

const PREVIEW_CHARS = 300;

function preview(input: DecideInput): string {
  const cmd = input.toolName === "Bash" && typeof input.input.command === "string" ? input.input.command : input.inputJson;
  return cmd.length > PREVIEW_CHARS ? `${cmd.slice(0, PREVIEW_CHARS)}…` : cmd;
}

async function recordDecision(principal: Principal, input: DecideInput, r: ChainResult): Promise<void> {
  await ensureDb();
  await getDb()
    .insert(schema.decisionLog)
    .values({
      id: newId("dcs"),
      userId: principal.user.id,
      deviceId: principal.deviceId ?? null,
      sessionId: input.sessionId,
      toolName: input.toolName,
      inputHash: sha256(input.inputJson),
      inputPreview: preview(input),
      decision: r.decision,
      provider: r.provider,
      categories: r.categories,
      reason: r.reason,
      confidence: r.confidence ?? null,
      latencyMs: r.latencyMs,
      costUsd: r.costUsd,
      cached: r.cached,
      error: r.errors.length ? r.errors.join("; ").slice(0, 1000) : null,
      createdAt: new Date(),
    });
}

export async function listDecisionLog(opts: { limit?: number; sessionId?: string; userId?: string }) {
  await ensureDb();
  const limit = Math.min(Math.max(1, Number.isFinite(opts.limit) && opts.limit ? opts.limit : 100), 1000);
  const conds: SQL[] = [];
  if (opts.sessionId) conds.push(eq(schema.decisionLog.sessionId, opts.sessionId));
  if (opts.userId) conds.push(eq(schema.decisionLog.userId, opts.userId));
  const q = getDb().select().from(schema.decisionLog);
  const rows = await (conds.length ? q.where(and(...conds)) : q).orderBy(desc(schema.decisionLog.createdAt)).limit(limit);
  return {
    decisions: rows.map((r) => ({
      id: r.id,
      user_id: r.userId,
      device_id: r.deviceId,
      session_id: r.sessionId,
      tool_name: r.toolName,
      input_hash: r.inputHash,
      input_preview: r.inputPreview,
      decision: r.decision,
      provider: r.provider,
      categories: r.categories as string[],
      reason: r.reason,
      confidence: r.confidence,
      latency_ms: r.latencyMs,
      cost_usd: r.costUsd,
      cached: r.cached,
      error: r.error,
      created_at: r.createdAt.toISOString(),
    })),
  };
}
