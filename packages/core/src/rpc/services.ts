/**
 * Connect service implementations: thin adapters from protobuf messages to the
 * transport-agnostic services in src/services.
 */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { DeviceAuthService, PollDeviceAuthResponse_Status, User_Role } from "../gen/harness/auth/v1/auth_pb.ts";
import { GatewayService, type StreamMessageResponse } from "../gen/harness/llm/v1/gateway_pb.ts";
import { PromptService } from "../gen/harness/prompts/v1/prompts_pb.ts";
import { SyncService } from "../gen/harness/sync/v1/sync_pb.ts";
import { config } from "../env.ts";
import type { GatewayEvent } from "../gateway/types.ts";
import { ApiError } from "../lib/errors.ts";
import {
  describePrincipal,
  listDevices,
  pollDeviceToken,
  refreshAccessToken,
  revokeDevice,
  revokeDeviceFor,
  startDeviceAuthorization,
  type DeviceInfo,
  type PublicUser,
} from "../services/devices.ts";
import { countTokens, prepareMessages, runMessages } from "../services/gateway.ts";
import { listModelsForUser } from "../services/models.ts";
import { getPromptBundle } from "../services/prompts.ts";
import { checkClientVersion, rateLimitByIp, requirePrincipal } from "./context.ts";
import { bundleToProto, eventToProto, messagesFromProto, modelsToProto, streamRequestToCanonical, toolsFromProto } from "./convert.ts";
import { guard, toConnectError } from "./errors.ts";
import { registerSyncService } from "./sync.ts";

const POLL_STATUS = {
  pending: PollDeviceAuthResponse_Status.PENDING,
  slow_down: PollDeviceAuthResponse_Status.SLOW_DOWN,
  approved: PollDeviceAuthResponse_Status.APPROVED,
  denied: PollDeviceAuthResponse_Status.DENIED,
  expired: PollDeviceAuthResponse_Status.EXPIRED,
} as const;

function deviceToProto(d: DeviceInfo) {
  return { id: d.id, name: d.name, os: d.os, arch: d.arch, createdAt: d.created_at, lastSeenAt: d.last_seen_at ?? "" };
}

function userToProto(u: PublicUser) {
  return { id: u.id, email: u.email, name: u.name, role: u.role === "admin" ? User_Role.ADMIN : User_Role.USER };
}

/**
 * Interleaves `ping` events into a stream whenever nothing was sent for `intervalMs`.
 * Also stops pulling from the source when the consumer goes away.
 */
export async function* withKeepalive(source: AsyncIterator<GatewayEvent>, intervalMs: number): AsyncGenerator<GatewayEvent> {
  let pending: Promise<IteratorResult<GatewayEvent>> | undefined;
  try {
    for (;;) {
      pending ??= source.next();
      let timer: NodeJS.Timeout | undefined;
      const tick = new Promise<"ping">((r) => {
        timer = setTimeout(() => r("ping"), intervalMs);
      });
      const res = await Promise.race([pending, tick]);
      clearTimeout(timer);
      if (res === "ping") {
        yield { event: "ping", data: { type: "ping" } };
        continue;
      }
      pending = undefined;
      if (res.done) return;
      yield res.value;
    }
  } finally {
    await source.return?.(undefined);
  }
}

export function registerServices(router: ConnectRouter): ConnectRouter {
  router.service(DeviceAuthService, {
    startDeviceAuth: guard(async (req, ctx: HandlerContext) => {
      checkClientVersion(ctx);
      rateLimitByIp(ctx, "device_start");
      const r = await startDeviceAuthorization({
        client_name: req.clientName,
        device_name: req.deviceName,
        os: req.os,
        arch: req.arch,
        client_version: req.clientVersion,
      });
      return {
        deviceCode: r.device_code,
        userCode: r.user_code,
        verificationUri: r.verification_uri,
        verificationUriComplete: r.verification_uri_complete,
        expiresInSeconds: r.expires_in,
        intervalSeconds: r.interval,
      };
    }),

    pollDeviceAuth: guard(async (req, ctx: HandlerContext) => {
      rateLimitByIp(ctx, "device_poll");
      const r = await pollDeviceToken(req.deviceCode, ctx.requestHeader.get("user-agent") ?? undefined);
      if (r.status !== "approved") return { status: POLL_STATUS[r.status] };
      const c = r.credentials;
      return {
        status: POLL_STATUS.approved,
        credentials: {
          deviceId: c.device_id,
          deviceToken: c.device_token,
          accessToken: c.access_token,
          accessExpiresInSeconds: c.access_expires_in,
          user: userToProto(c.user),
        },
      };
    }),

    refreshAccessToken: guard(async (req, ctx: HandlerContext) => {
      rateLimitByIp(ctx, "device_refresh");
      const r = await refreshAccessToken(req.deviceToken);
      return { accessToken: r.access_token, accessExpiresInSeconds: r.access_expires_in };
    }),

    getMe: guard(async (_req, ctx: HandlerContext) => {
      const me = await describePrincipal(await requirePrincipal(ctx));
      return {
        user: userToProto(me.user),
        device: me.device ? deviceToProto(me.device) : undefined,
      };
    }),

    logout: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      if (!p.deviceId) throw new ApiError("invalid_request", "Not authenticated with a device token");
      await revokeDevice(p.deviceId);
      return {};
    }),

    listDevices: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { devices: (await listDevices(p, req.userId || undefined)).map(deviceToProto) };
    }),

    revokeDevice: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await revokeDeviceFor(p, req.deviceId);
      return {};
    }),
  });

  router.service(GatewayService, {
    listModels: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return modelsToProto(await listModelsForUser(p.user));
    }),

    async *streamMessage(req, ctx: HandlerContext): AsyncGenerator<StreamMessageResponse> {
      let events: AsyncGenerator<GatewayEvent>;
      try {
        const p = await requirePrincipal(ctx);
        const prepared = await prepareMessages(p, streamRequestToCanonical(req));
        events = withKeepalive(runMessages(prepared, p, ctx.signal), config().pingIntervalMs);
      } catch (err) {
        throw toConnectError(err);
      }
      try {
        for await (const ev of events) {
          const msg = eventToProto(ev);
          if (msg) yield msg;
        }
      } catch (err) {
        throw toConnectError(err);
      }
    },

    countTokens: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await countTokens(p, {
        model: req.model,
        system: req.system.map((t) => ({ type: "text", text: t.text })),
        messages: messagesFromProto(req.messages),
        tools: req.tools.length ? toolsFromProto(req.tools) : undefined,
      });
      return { inputTokens: BigInt(r.input_tokens), estimated: r.estimated };
    }),
  });

  router.service(PromptService, {
    getBundle: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const bundle = await getPromptBundle(p.user, req.model || undefined);
      if (req.ifNoneMatch && req.ifNoneMatch === bundle.version) return { notModified: true };
      return { notModified: false, bundle: bundleToProto(bundle) };
    }),
  });

  registerSyncService(router);

  return router;
}

/** Fully-qualified service names, used to mount the Connect handlers in Nitro. */
export const SERVICE_NAMES = [DeviceAuthService.typeName, GatewayService.typeName, PromptService.typeName, SyncService.typeName];
