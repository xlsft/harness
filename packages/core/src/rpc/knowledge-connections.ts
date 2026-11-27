/** Connect implementation of harness.library.v1 KnowledgeConnectionService: proto <-> src/knowledge/connections.ts. */
import { can } from "../auth/roles.ts";
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { KnowledgeConnectionService, type KnowledgeConnectionInput, type KnowledgeConnectionSchema } from "../gen/harness/library/v1/knowledge_connections_pb.ts";
import { TrackerConnectionScope } from "../gen/harness/library/v1/tracker_connections_pb.ts";
import {
  deleteKnowledgeConnection,
  hasKnowledgeSecret,
  KNOWLEDGE_PROVIDERS,
  listKnowledgeConnections,
  putKnowledgeConnection,
  startKnowledgeOAuth,
  testKnowledgeConnection,
  type KnowledgeConnectionInput as Input,
  type KnowledgeConnectionRow,
} from "../knowledge/connections.ts";
import { ApiError } from "../lib/errors.ts";
import { connectionsById } from "../tracker/connections.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";
import { authNameOrEmpty, authToProto, knowledgeCapabilitiesToProto } from "./provider-convert.ts";

/** Everyone sees that a company source exists and works; how it signs in, and why it fails, only admins. */
async function toProto(r: KnowledgeConnectionRow, admin: boolean, borrowNames: Map<string, string>): Promise<MessageInitShape<typeof KnowledgeConnectionSchema>> {
  const capabilities = KNOWLEDGE_PROVIDERS.find((c) => c.provider === r.provider);
  return {
    id: r.id,
    scope: TrackerConnectionScope.ADMIN,
    provider: r.provider,
    name: r.name,
    enabled: r.enabled,
    healthy: r.healthy,
    capabilities: capabilities && knowledgeCapabilitiesToProto(capabilities),
    ...(admin
      ? {
          baseUrl: r.baseUrl,
          auth: authToProto(r.auth),
          login: r.login,
          oauthClientId: r.oauthClientId,
          hasSecret: await hasKnowledgeSecret(r),
          allowPrivateNetwork: r.allowPrivateNetwork,
          query: r.query,
          options: r.options ?? {},
          borrow: r.borrowConnectionId ? { scope: TrackerConnectionScope.ADMIN, id: r.borrowConnectionId } : undefined,
          borrowName: borrowNames.get(r.borrowConnectionId) ?? "",
          lastError: r.lastError,
          lastCheckedAt: r.lastCheckedAt?.toISOString() ?? "",
        }
      : {}),
  };
}

function fromProto(c: KnowledgeConnectionInput | undefined): Input {
  if (!c) throw new ApiError("invalid_request", "connection is required");
  return {
    id: c.id || undefined,
    provider: c.provider,
    name: c.name,
    baseUrl: c.baseUrl,
    auth: c.borrowConnectionId ? "" : authNameOrEmpty(c.auth),
    login: c.login,
    secret: c.secret,
    oauthClientId: c.oauthClientId,
    oauthClientSecret: c.oauthClientSecret,
    enabled: c.enabled,
    allowPrivateNetwork: c.allowPrivateNetwork,
    query: c.query,
    options: { ...c.options },
    borrowConnectionId: c.borrowConnectionId || undefined,
  };
}

async function namesOf(rows: KnowledgeConnectionRow[]): Promise<Map<string, string>> {
  const byId = await connectionsById([...new Set(rows.map((r) => r.borrowConnectionId).filter(Boolean))]);
  return new Map([...byId].map(([id, t]) => [id, t.name]));
}

export function registerKnowledgeConnectionService(router: ConnectRouter): void {
  router.service(KnowledgeConnectionService, {
    listKnowledgeConnections: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const admin = can(p.user, "connections.write");
      const rows = (await listKnowledgeConnections()).filter((r) => admin || r.enabled);
      const names = admin ? await namesOf(rows) : new Map<string, string>();
      return { connections: await Promise.all(rows.map((r) => toProto(r, admin, names))), providers: KNOWLEDGE_PROVIDERS.map(knowledgeCapabilitiesToProto) };
    }),
    putKnowledgeConnection: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const row = await putKnowledgeConnection(p, fromProto(req.connection));
      return { connection: await toProto(row, true, await namesOf([row])) };
    }),
    deleteKnowledgeConnection: guard(async (req, ctx: HandlerContext) => {
      await deleteKnowledgeConnection(await requirePrincipal(ctx), req.id);
      return {};
    }),
    testKnowledgeConnection: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return await testKnowledgeConnection(p, req.id, req.draft ? fromProto(req.draft) : undefined);
    }),
    startKnowledgeOAuth: guard(async (req, ctx: HandlerContext) => ({ authorizeUrl: await startKnowledgeOAuth(await requirePrincipal(ctx), req.connectionId) })),
  });
}
