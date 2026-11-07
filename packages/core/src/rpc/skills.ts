/**
 * Connect implementation of harness.skills.v1.SkillSearchService. The organization's policy is checked here as well as on the client,
 * so a client that ignores it gets nothing.
 */
import { Code, ConnectError, type ConnectRouter, type HandlerContext } from "@connectrpc/connect";
import { SkillSearchService } from "../gen/harness/skills/v1/search_pb.ts";
import { serverPolicyFor } from "../services/managed-policy.ts";
import { curatedSkills, searchSkills, skillSearchConfigured, SkillSearchError, skillSnapshot } from "../skills/skillssh.ts";
import { checkClientVersion, requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

async function allowed(ctx: HandlerContext): Promise<void> {
  checkClientVersion(ctx);
  const { user } = await requirePrincipal(ctx);
  if ((await serverPolicyFor(user)).externalSkillSearchDisabled) {
    throw new ConnectError("your organization's policy turned external skill search off", Code.PermissionDenied);
  }
}

function mapped<T>(f: () => Promise<T>): Promise<T> {
  return f().catch((e: unknown) => {
    if (e instanceof SkillSearchError) {
      const code = { not_configured: Code.FailedPrecondition, unavailable: Code.Unavailable, not_found: Code.NotFound, invalid: Code.InvalidArgument }[e.kind];
      throw new ConnectError(e.message, code);
    }
    throw e;
  });
}

export function registerSkillSearchService(router: ConnectRouter): void {
  router.service(SkillSearchService, {
    getSkillSearchStatus: guard(async (_req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      const { user } = await requirePrincipal(ctx);
      if ((await serverPolicyFor(user)).externalSkillSearchDisabled) return { available: false, reason: "policy" };
      return skillSearchConfigured() ? { available: true, reason: "" } : { available: false, reason: "not_configured" };
    }),
    searchSkills: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx);
      return { skills: await mapped(() => searchSkills(req.query, req.limit, ctx.signal)) };
    }),
    listCuratedSkills: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx);
      return { skills: await mapped(() => curatedSkills(req.limit, ctx.signal)) };
    }),
    getSkillSnapshot: guard(async (req, ctx: HandlerContext) => {
      await allowed(ctx);
      return await mapped(() => skillSnapshot(req.id, ctx.signal));
    }),
  });
}
