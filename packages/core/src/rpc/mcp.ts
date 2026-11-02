/** Connect implementation of harness.mcp.v1.McpService: proto <-> src/mcp/*. */
import type { DescMessage, MessageInitShape } from "@bufbuild/protobuf";
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import {
  McpService,
  type AuthStatusSchema,
  type CallToolResponseSchema,
  type DeviceServerStatusSchema,
  type ServerSchema,
  type Tool as PbTool,
  type ToolSchema,
} from "../gen/harness/mcp/v1/mcp_pb.ts";
import { AsyncQueue } from "../lib/async.ts";
import { ApiError } from "../lib/errors.ts";
import { audit } from "../mcp/audit.ts";
import { getToolCatalog } from "../mcp/catalog.ts";
import { issueStdioLaunch, listDeviceStatus, reportDeviceStatus, type DeviceStatusRow } from "../mcp/devices.ts";
import { authStatuses, getOAuthFlow, revokeAuth, startOAuth, type AuthStatusView } from "../mcp/oauth.ts";
import { callTool, getPrompt, listPrompts, listResources, listTools, readResource, type ProgressEvent, type ToolData } from "../mcp/proxy.ts";
import { createServer, deleteServer, getServer, listServers, updateServer, type ServerView } from "../mcp/registry.ts";
import type { PublicUser } from "../services/devices.ts";
import { requirePrincipal } from "./context.ts";
import { guard, toConnectError } from "./errors.ts";

type Init<T extends DescMessage> = MessageInitShape<T>;

function authToProto(a: AuthStatusView | undefined): Init<typeof AuthStatusSchema> | undefined {
  if (!a) return undefined;
  return { state: a.state, message: a.message, grantedScopes: a.grantedScopes, account: a.account, expiresAt: a.expiresAt };
}

function serverToProto(v: ServerView, auth?: AuthStatusView): Init<typeof ServerSchema> {
  const r = v.row;
  const c = v.config;
  return {
    id: r.id,
    name: r.name,
    scope: r.scope,
    transport: r.transport,
    enabled: r.enabled,
    alwaysLoad: r.alwaysLoad,
    stdio: c.stdio ? { command: c.stdio.command, args: c.stdio.args, env: c.stdio.env, cwd: c.stdio.cwd } : undefined,
    remote: c.remote
      ? {
          url: c.remote.url,
          headers: c.remote.headers,
          oauth: c.remote.oauth,
          requiresUserInteraction: c.remote.requiresUserInteraction,
        }
      : undefined,
    secrets: v.secrets,
    overrides: c.overrides,
    targetRoles: Array.isArray(r.targetRoles) ? (r.targetRoles as string[]) : [],
    projectKey: r.projectKey,
    version: BigInt(r.version),
    configHash: r.configHash,
    auth: authToProto(auth),
    createdAt: r.createdAt.toISOString(),
    updatedAt: r.updatedAt.toISOString(),
  };
}

async function serversToProto(user: PublicUser, views: ServerView[]) {
  const auth = await authStatuses(user, views);
  return views.map((v) => serverToProto(v, auth.get(v.row.id)));
}

const json = (v: unknown) => (v === undefined || v === null ? "" : JSON.stringify(v));

function toolToProto(t: ToolData): Init<typeof ToolSchema> {
  return {
    name: t.name,
    title: t.title,
    description: t.description,
    inputSchemaJson: json(t.inputSchema),
    outputSchemaJson: json(t.outputSchema),
    annotationsJson: json(t.annotations),
  };
}

function parseJson(s: string, field: string): unknown {
  if (!s) return undefined;
  try {
    return JSON.parse(s);
  } catch {
    throw new ApiError("invalid_request", `${field} must be valid JSON`);
  }
}

function toolFromProto(t: PbTool): ToolData {
  if (!t.name) throw new ApiError("invalid_request", "tool name is required");
  const annotations = parseJson(t.annotationsJson, "annotations_json");
  return {
    name: t.name,
    title: t.title,
    description: t.description,
    inputSchema: parseJson(t.inputSchemaJson, "input_schema_json") ?? { type: "object" },
    outputSchema: parseJson(t.outputSchemaJson, "output_schema_json"),
    annotations: annotations && typeof annotations === "object" ? (annotations as Record<string, unknown>) : undefined,
  };
}

function statusToProto(s: DeviceStatusRow): Init<typeof DeviceServerStatusSchema> {
  return {
    serverId: s.serverId,
    configHash: s.configHash,
    availability: s.availability,
    reason: s.reason,
    approval: s.approval,
    lastError: s.lastError,
    tools: (Array.isArray(s.tools) ? (s.tools as ToolData[]) : []).map(toolToProto),
    reportedAt: s.reportedAt.toISOString(),
  };
}

export function registerMcpService(router: ConnectRouter): void {
  router.service(McpService, {
    // --- registry ---
    listServers: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const views = await listServers(p, { scopes: req.scopes, includeDisabled: req.includeDisabled, projectKey: req.projectKey });
      return { servers: await serversToProto(p.user, views) };
    }),

    getServer: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const [server] = await serversToProto(p.user, [await getServer(p, req.serverId)]);
      return { server };
    }),

    createServer: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const [server] = await serversToProto(p.user, [await createServer(p, req.server, req.secrets)]);
      return { server };
    }),

    updateServer: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await updateServer(p, req.server, req.secrets, Number(req.baseVersion));
      if ("conflict" in r) {
        const [current] = await serversToProto(p.user, [r.conflict]);
        return { result: { case: "conflict" as const, value: { current } } };
      }
      const [server] = await serversToProto(p.user, [r.server]);
      return { result: { case: "server" as const, value: server! } };
    }),

    deleteServer: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const conflict = await deleteServer(p, req.serverId, req.baseVersion === undefined ? undefined : Number(req.baseVersion));
      if (!conflict) return {};
      const [current] = await serversToProto(p.user, [conflict]);
      return { conflict: { current } };
    }),

    // --- authorization ---
    startOAuth: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return startOAuth(p, req.serverId, req.returnUrl);
    }),

    getOAuthFlow: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await getOAuthFlow(p, req.flowId);
      return { state: r.state, errorMessage: r.errorMessage, auth: authToProto(r.auth) };
    }),

    getAuthStatus: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      let views: ServerView[];
      if (req.serverIds.length) {
        views = [];
        for (const id of [...new Set(req.serverIds)]) views.push(await getServer(p, id));
      } else {
        views = await listServers(p, { includeDisabled: true });
      }
      const auth = await authStatuses(p.user, views);
      return { statuses: Object.fromEntries([...auth].map(([id, a]) => [id, authToProto(a)!])) };
    }),

    revokeAuth: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await revokeAuth(p, req.serverId);
      return {};
    }),

    // --- devices ---
    reportDeviceStatus: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await reportDeviceStatus(
        p,
        req.statuses.map((s) => ({
          serverId: s.serverId,
          configHash: s.configHash,
          availability: s.availability,
          reason: s.reason,
          approval: s.approval,
          lastError: s.lastError,
          tools: s.tools.map(toolFromProto),
        })),
      );
      return {};
    }),

    listDeviceStatus: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const entries = await listDeviceStatus(p, req.serverId, req.deviceId);
      return { entries: entries.map((e) => ({ deviceId: e.deviceId, deviceName: e.deviceName, status: statusToProto(e.status) })) };
    }),

    issueStdioLaunch: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return issueStdioLaunch(p, req.serverId, req.configHash);
    }),

    // --- proxy ---
    listTools: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await listTools(p.user, await getServer(p, req.serverId), req.cursor, ctx.signal);
      return { tools: r.tools.map(toolToProto), nextCursor: r.nextCursor };
    }),

    listPrompts: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await listPrompts(p.user, await getServer(p, req.serverId), req.cursor, ctx.signal);
      return {
        prompts: r.prompts.map((x) => ({ name: x.name, title: x.title ?? "", description: x.description ?? "", argumentsJson: json(x.arguments) })),
        nextCursor: r.nextCursor,
      };
    }),

    listResources: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await listResources(p.user, await getServer(p, req.serverId), req.cursor, ctx.signal);
      return {
        resources: r.resources.map((x) => ({
          uri: x.uri,
          name: x.name,
          title: x.title ?? "",
          description: x.description ?? "",
          mimeType: x.mimeType ?? "",
          size: BigInt(Math.max(0, Math.floor(Number((x as { size?: number }).size ?? 0)))),
        })),
        templates: r.templates.map((x) => ({
          uriTemplate: x.uriTemplate,
          name: x.name,
          title: x.title ?? "",
          description: x.description ?? "",
          mimeType: x.mimeType ?? "",
        })),
        nextCursor: r.nextCursor,
      };
    }),

    async *callTool(req, ctx: HandlerContext): AsyncGenerator<Init<typeof CallToolResponseSchema>> {
      const queue = new AsyncQueue<{ progress: ProgressEvent } | { result: { isError: boolean; resultJson: string } } | { error: unknown }>();
      let audited: Promise<unknown> = Promise.resolve();
      try {
        const p = await requirePrincipal(ctx);
        const view = await getServer(p, req.serverId);
        const started = Date.now();
        callTool(p.user, view, req.toolName, req.argumentsJson, Number(req.timeoutMs), ctx.signal, (progress) => queue.push({ progress })).then(
          (result) => {
            audited = audit({
              userId: p.user.id,
              deviceId: p.deviceId,
              serverId: view.row.id,
              action: "call_tool",
              configHash: view.row.configHash,
              detail: { tool: req.toolName, sessionId: req.sessionId, toolUseId: req.toolUseId, isError: result.isError, ms: Date.now() - started },
            });
            queue.push({ result });
          },
          (error) => {
            audited = audit({
              userId: p.user.id,
              deviceId: p.deviceId,
              serverId: view.row.id,
              action: "call_tool",
              configHash: view.row.configHash,
              detail: { tool: req.toolName, sessionId: req.sessionId, toolUseId: req.toolUseId, error: error instanceof Error ? error.message : String(error) },
            });
            queue.push({ error });
          },
        );
        for (;;) {
          const ev = await queue.next(60_000, ctx.signal);
          if (ctx.signal.aborted) return;
          if (!ev) continue;
          if ("progress" in ev) {
            yield { event: { case: "progress", value: ev.progress } };
          } else if ("result" in ev) {
            yield { event: { case: "result", value: ev.result } };
            return;
          } else {
            throw ev.error;
          }
        }
      } catch (err) {
        throw toConnectError(err);
      } finally {
        await audited;
      }
    },

    getPrompt: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await getPrompt(p.user, await getServer(p, req.serverId), req.name, req.arguments, ctx.signal);
      return { resultJson: JSON.stringify(r) };
    }),

    readResource: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await readResource(p.user, await getServer(p, req.serverId), req.uri, ctx.signal);
      return { resultJson: JSON.stringify(r) };
    }),

    // --- catalog ---
    getToolCatalog: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await getToolCatalog(p, { version: req.version, ifNoneMatch: req.ifNoneMatch, projectKey: req.projectKey });
      if (r.notModified || !r.catalog) return { notModified: true };
      return {
        notModified: false,
        catalog: {
          version: r.catalog.version,
          createdAt: r.catalog.createdAt,
          servers: r.catalog.servers.map((s) => ({
            serverId: s.serverId,
            name: s.name,
            transport: s.transport,
            alwaysLoad: s.alwaysLoad,
            tools: s.tools.map(toolToProto),
            toolsUpdatedAt: s.toolsUpdatedAt,
          })),
        },
      };
    }),
  });
}
