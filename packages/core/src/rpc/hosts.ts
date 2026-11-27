/** Connect implementation of harness.relay.v1.HostsService and HostAgentService: proto <-> src/relay/hosts.ts. */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { HostAgentService, HostsService } from "../gen/harness/relay/v1/hosts_pb.ts";
import {
  cancelHostWork,
  claimHostWork,
  claimScheduleRun,
  createAgentHost,
  deleteAgentHost,
  getHostWork,
  issueGitCredentials,
  issueMoveCredentials,
  issueWorkerToken,
  listAgentHosts,
  listHostWork,
  listWantedWorkers,
  openHostPullRequest,
  queueMovedWork,
  registerAgentHost,
  reportAgentHost,
  setAwayHost,
  startHostWork,
  updateAgentHost,
  updateHostWork,
} from "../relay/hosts.ts";
import { discardAttempts, getAttemptGroup, keepAttempt, listAttemptGroups, reportAttempt, startAttempts } from "../relay/attempts.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

export function registerHostServices(router: ConnectRouter): void {
  router.service(HostsService, {
    listAgentHosts: guard(async (req, ctx: HandlerContext) => ({ hosts: await listAgentHosts(await requirePrincipal(ctx), req.all) })),
    createAgentHost: guard(async (req, ctx: HandlerContext) => createAgentHost(await requirePrincipal(ctx), req)),
    updateAgentHost: guard(async (req, ctx: HandlerContext) => ({ host: await updateAgentHost(await requirePrincipal(ctx), req) })),
    deleteAgentHost: guard(async (req, ctx: HandlerContext) => {
      await deleteAgentHost(await requirePrincipal(ctx), req.id);
      return {};
    }),
    startHostWork: guard(async (req, ctx: HandlerContext) => ({ work: await startHostWork(await requirePrincipal(ctx), req) })),
    getHostWork: guard(async (req, ctx: HandlerContext) => ({ work: await getHostWork(await requirePrincipal(ctx), req.id) })),
    listHostWork: guard(async (req, ctx: HandlerContext) => ({ work: await listHostWork(await requirePrincipal(ctx), req.hostId, req.limit) })),
    cancelHostWork: guard(async (req, ctx: HandlerContext) => ({ work: await cancelHostWork(await requirePrincipal(ctx), req.id) })),
    setAwayHost: guard(async (req, ctx: HandlerContext) => {
      await setAwayHost(await requirePrincipal(ctx), req.hostId);
      return {};
    }),
    claimScheduleRun: guard(async (req, ctx: HandlerContext) => ({ granted: await claimScheduleRun(await requirePrincipal(ctx), req.scheduleId, req.dueAt) })),
    startAttempts: guard(async (req, ctx: HandlerContext) => ({ group: await startAttempts(await requirePrincipal(ctx), req) })),
    getAttemptGroup: guard(async (req, ctx: HandlerContext) => ({ group: await getAttemptGroup(await requirePrincipal(ctx), req.id) })),
    listAttemptGroups: guard(async (req, ctx: HandlerContext) => ({ groups: await listAttemptGroups(await requirePrincipal(ctx), req.limit) })),
    reportAttempt: guard(async (req, ctx: HandlerContext) => {
      await reportAttempt(await requirePrincipal(ctx), req);
      return {};
    }),
    keepAttempt: guard(async (req, ctx: HandlerContext) => ({ group: await keepAttempt(await requirePrincipal(ctx), req.groupId, req.index) })),
    discardAttempts: guard(async (req, ctx: HandlerContext) => ({ group: await discardAttempts(await requirePrincipal(ctx), req.groupId) })),
  });
  router.service(HostAgentService, {
    registerAgentHost: guard(async (req, ctx: HandlerContext) => registerAgentHost(await requirePrincipal(ctx), req)),
    reportAgentHost: guard(async (req, ctx: HandlerContext) => reportAgentHost(await requirePrincipal(ctx), req)),
    claimHostWork: guard(async (req, ctx: HandlerContext) => claimHostWork(await requirePrincipal(ctx), req.waitSeconds, ctx.signal)),
    updateHostWork: guard(async (req, ctx: HandlerContext) => updateHostWork(await requirePrincipal(ctx), req)),
    issueGitCredentials: guard(async (req, ctx: HandlerContext) => issueGitCredentials(await requirePrincipal(ctx), req.repositoryKey)),
    openHostPullRequest: guard(async (req, ctx: HandlerContext) => openHostPullRequest(await requirePrincipal(ctx), req)),
    listWantedWorkers: guard(async (req, ctx: HandlerContext) => listWantedWorkers(await requirePrincipal(ctx), req.waitSeconds, ctx.signal)),
    issueWorkerToken: guard(async (req, ctx: HandlerContext) => issueWorkerToken(await requirePrincipal(ctx), req.userId)),
    issueMoveCredentials: guard(async (req, ctx: HandlerContext) => ({ credentials: await issueMoveCredentials(await requirePrincipal(ctx), req.workId) })),
    queueMovedWork: guard(async (req, ctx: HandlerContext) => ({ work: await queueMovedWork(await requirePrincipal(ctx), req.workId) })),
  });
}
