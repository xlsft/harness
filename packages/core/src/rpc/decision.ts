/** Connect implementation of harness.decision.v1.DecisionService: proto <-> src/decision/service.ts. */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { Decision, DecisionService, OnError, RiskCategory } from "../gen/harness/decision/v1/decision_pb.ts";
import { decide, getRulesOptions, listDecisionProviders } from "../decision/service.ts";
import type { DecisionValue, RiskCategoryName } from "../decision/types.ts";
import { checkClientVersion, requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

const DECISION: Record<DecisionValue, Decision> = {
  allow: Decision.ALLOW,
  deny: Decision.DENY,
  ask: Decision.ASK,
};

export function categoryToProto(c: RiskCategoryName): RiskCategory {
  return RiskCategory[c.toUpperCase() as keyof typeof RiskCategory] ?? RiskCategory.UNSPECIFIED;
}

export function registerDecisionService(router: ConnectRouter): void {
  router.service(DecisionService, {
    decide: guard(async (req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      const p = await requirePrincipal(ctx);
      const r = await decide(
        p,
        {
          sessionId: req.sessionId,
          toolName: req.toolName,
          inputJson: req.inputJson,
          cwd: req.cwd,
          additionalDirectories: req.additionalDirectories,
          recentUserMessages: req.recentUserMessages,
          assistantRationale: req.assistantRationale,
          facts: req.facts
            ? {
                subcommands: req.facts.subcommands,
                writePaths: req.facts.writePaths,
                networkHosts: req.facts.networkHosts,
                insideGitRepo: req.facts.insideGitRepo,
                gitBranch: req.facts.gitBranch,
              }
            : undefined,
          provider: req.provider,
        },
        ctx.signal,
      );
      return {
        decision: DECISION[r.decision],
        reason: r.reason,
        categories: r.categories.map(categoryToProto),
        provider: r.provider,
        confidence: r.confidence ?? 0,
        cached: r.cached,
        latencyMs: BigInt(r.latencyMs),
        costUsd: r.costUsd,
        rulesVersion: r.rulesVersion,
      };
    }),
    getRulesOptions: guard(async (_req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      await requirePrincipal(ctx);
      const r = await getRulesOptions();
      return {
        rulesVersion: r.rulesVersion,
        options: {
          extraDeny: r.options.extra_deny.map((d) => ({ pattern: d.pattern, reason: d.reason ?? "", categories: (d.categories ?? []).map(categoryToProto) })),
          extraAllow: r.options.extra_allow.map((pattern) => ({ pattern, reason: "", categories: [] })),
          protectedBranches: r.options.protected_branches,
          denyAllForcePush: r.options.deny_all_force_push,
        },
      };
    }),
    listProviders: guard(async (_req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      await requirePrincipal(ctx);
      const r = await listDecisionProviders(ctx.signal);
      return {
        providers: r.providers.map((p) => ({ id: p.id, type: p.type, displayName: p.displayName, healthy: p.healthy, model: p.model })),
        onError: r.onError === "next" ? OnError.NEXT : OnError.ASK,
      };
    }),
  });
}
