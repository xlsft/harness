/** Connect implementation of harness.relay.v1.RelayService: proto <-> src/relay/service.ts. */
import { Code, ConnectError, type ConnectRouter, type HandlerContext } from "@connectrpc/connect";
import { RelayService, type CommandOptions } from "../gen/harness/relay/v1/relay_pb.ts";
import { ApiError } from "../lib/errors.ts";
import {
  ackCommands,
  commandResult,
  heartbeat,
  listHosts,
  openHostStream,
  publishEvents,
  querySession,
  registerHost,
  registerSession,
  sendSessionCommand,
  spawnSession,
  subscribeSession,
  unregisterSession,
  updatePresence,
  type CommandPayload,
  type HoldMode,
} from "../relay/service.ts";
import { requirePrincipal } from "./context.ts";
import { guard, toConnectError } from "./errors.ts";

/** Frontend label of a viewer ("web", "mobile", ...), from the x-harness-frontend header. */
function frontendOf(ctx: HandlerContext): string {
  return (ctx.requestHeader.get("x-harness-frontend") ?? "").slice(0, 32);
}

const tunnelsUnimplemented = async (): Promise<never> => {
  throw new ConnectError("Tunnels are not available yet (docs/protocol/tunnels.md)", Code.Unimplemented);
};

/** A fire-and-forget (or held) command wrapper: { request, options } -> { result }. */
function command<C extends NonNullable<CommandPayload["case"]>>(kind: C, hold: HoldMode = "optional") {
  return guard(async (req: { request?: Extract<CommandPayload, { case: C }>["value"]; options?: CommandOptions }, ctx: HandlerContext) => {
    const p = await requirePrincipal(ctx);
    if (!req.request) throw new ApiError("invalid_request", "request is required");
    const payload = { case: kind, value: req.request } as CommandPayload;
    return { result: commandResult(await sendSessionCommand(p, payload, req.options, hold, frontendOf(ctx))) };
  });
}

/** A query wrapper: { request } -> { response } taken from the ack result of the same name. */
function query<C extends "suggestFiles" | "listCheckpoints" | "listTasks" | "listOutputStyles" | "stopSubagent" | "getSubagentTranscript">(kind: C) {
  return guard(async (req: { request?: Extract<CommandPayload, { case: C }>["value"] }, ctx: HandlerContext) => {
    const p = await requirePrincipal(ctx);
    if (!req.request) throw new ApiError("invalid_request", "request is required");
    const ack = await querySession(p, { case: kind, value: req.request } as CommandPayload, frontendOf(ctx));
    if (ack.result.case !== kind) throw new ApiError("internal", `The host acked without a ${kind} result`);
    return { response: ack.result.value as never };
  });
}

export function registerRelayService(router: ConnectRouter): void {
  router.service(RelayService, {
    // --- host side ---
    registerHost: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return registerHost(p, {
        instanceId: req.instanceId,
        clientVersion: req.clientVersion,
        daemon: req.daemon,
        allowedRoots: req.allowedRoots,
        tunnels: req.tunnels,
        pty: req.pty,
      });
    }),

    async *openHostStream(req, ctx: HandlerContext) {
      try {
        const p = await requirePrincipal(ctx);
        yield* openHostStream(p, req.hostId, ctx.signal);
      } catch (err) {
        throw toConnectError(err);
      }
    },

    registerSession: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return registerSession(p, req.hostId, req.session);
    }),

    unregisterSession: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await unregisterSession(p, req.hostId, req.sessionId, req.epoch);
      return {};
    }),

    publishEvents: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return publishEvents(p, req.hostId, req.sessionId, req.epoch, req.events);
    }),

    ackCommands: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await ackCommands(p, req.hostId, req.acks);
      return {};
    }),

    heartbeat: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return heartbeat(p, req.hostId, req.sessions, req.lastLocalActivityAt);
    }),

    registerTunnel: tunnelsUnimplemented,
    closeTunnel: tunnelsUnimplemented,

    // --- viewer side ---
    listHosts: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { hosts: await listHosts(p) };
    }),

    async *subscribeSession(req, ctx: HandlerContext) {
      try {
        const p = await requirePrincipal(ctx);
        yield* subscribeSession(p, req.sessionId, req.afterSeq, req.viewerId, frontendOf(ctx), ctx.signal);
      } catch (err) {
        throw toConnectError(err);
      }
    },

    updatePresence: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await updatePresence(p, req.sessionId, req.viewerId, req.state);
      return {};
    }),

    sendUserMessage: command("sendUserMessage"),
    interrupt: command("interrupt"),
    respondToPermission: command("respondToPermission", "implied"),
    answerQuestion: command("answerQuestion", "implied"),
    respondToPlan: command("respondToPlan", "implied"),
    setModel: command("setModel"),
    setEffort: command("setEffort"),
    setPermissionMode: command("setPermissionMode"),
    setOutputStyle: command("setOutputStyle"),
    setAutoContinue: command("setAutoContinue"),
    setKeepAwake: command("setKeepAwake"),
    removeQueuedMessage: command("removeQueuedMessage"),
    stopTask: command("stopTask"),
    closeSession: command("closeSession"),
    compact: command("compact"),
    rewind: command("rewind"),

    suggestFiles: query("suggestFiles"),
    listCheckpoints: query("listCheckpoints"),
    listTasks: query("listTasks"),
    listOutputStyles: query("listOutputStyles"),
    stopSubagent: query("stopSubagent"),
    getSubagentTranscript: query("getSubagentTranscript"),

    spawnSession: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { session: await spawnSession(p, req.hostId, req.spawn, req.options, frontendOf(ctx)) };
    }),

    requestTunnel: tunnelsUnimplemented,
    listTunnels: tunnelsUnimplemented,
    authorizeTunnel: tunnelsUnimplemented,
  });
}
